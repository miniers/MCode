package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// nmemFake 模拟 Nowledge Mem 的三个端点，并记录收到的请求。
type nmemFake struct {
	mu       sync.Mutex
	spaces   []map[string]any
	content  string
	imports  []map[string]any
	requests []string
	srv      *httptest.Server
	// importStatus 非 0 时 /threads/import 返回该状态码（模拟上传失败）。
	importStatus int
	// importGate 非 nil 时 /threads/import 在 gate 关闭（或客户端取消）前不返回，
	// 用于制造在飞的慢请求，让 Close 必须靠取消网络 context 才能结束。
	importGate chan struct{}
	// importEntered 在第一个导入请求到达服务端时收到一个信号（缓冲 1）。
	importEntered chan struct{}
	gateOnce      sync.Once
	enteredOnce   sync.Once
}

func newNmemFake(t *testing.T, spaces ...string) *nmemFake {
	t.Helper()
	f := &nmemFake{content: "today briefing"}
	for _, s := range spaces {
		f.spaces = append(f.spaces, map[string]any{"id": s, "key": s, "name": s})
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/spaces":
			_ = json.NewEncoder(w).Encode(map[string]any{"spaces": f.spaces})
		case "/agent/working-memory":
			_ = json.NewEncoder(w).Encode(map[string]any{"exists": true, "content": f.content})
		case "/threads/import":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			gate := f.importGate
			entered := f.importEntered
			status := f.importStatus
			if gate != nil {
				// 在锁内使用 once，与 gateImports 重置 once 互斥（-race 下也不会报竞态）。
				f.enteredOnce.Do(func() { entered <- struct{}{} })
			}
			f.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
					// 客户端已取消时按取消处理，不记成功，避免被取消的导入看起来落了库。
					if r.Context().Err() != nil {
						return
					}
				case <-r.Context().Done():
					return
				}
			}
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			f.mu.Lock()
			f.imports = append(f.imports, payload)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true,"failed_count":0,"results":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	// 后注册先执行：先释放可能阻塞的导入请求，再关服务，避免 httptest.Server.Close 挂住。
	t.Cleanup(f.releaseImports)
	return f
}

// gateImports 让 /threads/import 阻塞到 releaseImports 或客户端取消，返回“已进入”信号。
// 每次调用都重置 once，保证重复开闸时 releaseImports 关的是当前 gate。
func (f *nmemFake) gateImports() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importGate = make(chan struct{})
	f.importEntered = make(chan struct{}, 1)
	f.gateOnce = sync.Once{}
	f.enteredOnce = sync.Once{}
	return f.importEntered
}

// releaseImports 解除导入阻塞（可重复调用）。
func (f *nmemFake) releaseImports() {
	f.mu.Lock()
	defer f.mu.Unlock()
	gate := f.importGate
	if gate != nil {
		f.gateOnce.Do(func() { close(gate) })
	}
}

// setImportStatus 设置 /threads/import 的响应状态码（0 表示成功）。
func (f *nmemFake) setImportStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importStatus = status
}

func (f *nmemFake) client() *nmem.Client { return nmem.New(f.srv.URL, "nmem_test") }

func (f *nmemFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.requests {
		if p == path {
			n++
		}
	}
	return n
}

func (f *nmemFake) importCalls() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.imports...)
}

// newNmemTestBackend 构建一个挂了 store、且带有一个项目与会话的 MemoryBackend。
// projectPath 为项目目录（可能需要是 git 仓库），threadID 为已有会话 ID。
func newNmemTestBackend(t *testing.T, projectPath string) (*MemoryBackend, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "nmem.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return newNmemBackendOnStore(t, s, projectPath), s
}

// newNmemBackendOnStore 在已有 store 上构建 backend：测试里用同一个 store 重建 backend
// 来模拟进程重启，验证持久待同步集合能被恢复。
func newNmemBackendOnStore(t *testing.T, s *store.Store, projectPath string) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{})
	b := NewMemoryBackend(hub)
	t.Cleanup(func() {
		b.Close()
		hub.Close()
	})

	if err := b.AttachStore(context.Background(), s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: projectPath})
	return b
}

// nmemStorePath 返回本测试专用的数据库文件路径：同一测试内可以重复 Open 来模拟进程重启。
func nmemStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nmem.db")
}

// openNmemStoreAt 在指定数据库文件上打开 store，用于真正重开同一个库验证磁盘状态。
// 测试可能自己先关一次库再重开，所以兜底关闭忽略重复关闭的错误。
func openNmemStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func nmemSettings(cfg config.MemoryConfig) func() config.MemoryConfig {
	return func() config.MemoryConfig { return cfg }
}

// waitNmemBackend 等待后台同步收尾（triggerNowledgeSync 的 goroutine 纳入 b.wg）。
func waitNmemBackend(b *MemoryBackend) { b.wg.Wait() }

// closeWithin 在 d 内等待 Close 返回，返回实际耗时；超时只标记失败，不无限阻塞测试。
func closeWithin(t *testing.T, b *MemoryBackend, d time.Duration) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		b.Close()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(d):
		t.Fatalf("Close 在 %s 内没有返回", d)
		return d
	}
}

// nmemTwoMessages 返回两条可导入的正文消息（用户 + 助手）。
func nmemTwoMessages() []store.Message {
	now := time.Now().UTC()
	return []store.Message{
		{Role: "user", Text: "hi", CreatedAt: now},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: now},
	}
}

// nmemPendingIDs 读出持久待同步集合。
func nmemPendingIDs(t *testing.T, s *store.Store) []string {
	t.Helper()
	raw, ok, err := s.GetSetting(context.Background(), nowledgeSyncPendingKey)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		t.Fatalf("解析待同步集合失败: %v（原文 %q）", err, raw)
	}
	return ids
}

// nmemPendingContains 判断持久待同步集合是否包含该会话。
func nmemPendingContains(t *testing.T, s *store.Store, threadID string) bool {
	t.Helper()
	for _, id := range nmemPendingIDs(t, s) {
		if id == threadID {
			return true
		}
	}
	return false
}

// seedThread 在 store 里写一个会话及其消息。
func seedThread(t *testing.T, s *store.Store, threadID string, msgs []store.Message) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateThread(ctx, store.Thread{
		ID:        threadID,
		ProjectID: "p1",
		Title:     "会话标题",
		Provider:  "claude",
		Model:     "opus",
	}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i, m := range msgs {
		m.ID = fmt.Sprintf("%s-msg-%d", threadID, i)
		m.ThreadID = threadID
		if _, err := s.AppendMessage(ctx, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
}

func gitRepoDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Skipf("跳过：git 不可用（%v）%s", err, out)
	}
	return dir
}

// nowledge 为 nil 时任何功能都不发请求。
func TestNowledgeNilSendsNoRequest(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: time.Now().UTC()},
	})

	if got := b.buildGuidance(t.TempDir(), 0, false); strings.Contains(got, "Nowledge Working Memory") {
		t.Fatalf("未注入客户端时不应包含 Working Memory 段: %q", got)
	}
	b.syncNowledgeThread("t1", time.Second)
	b.triggerNowledgeSync("t1")
	waitNmemBackend(b)

	if n := fake.count("/threads/import") + fake.count("/spaces") + fake.count("/agent/working-memory"); n != 0 {
		t.Fatalf("nowledge 为 nil 时不应发请求，实际 %d 次: %v", n, fake.requests)
	}
}

// 关闭开关后既不同步也不注入。
func TestNowledgeDisabledSwitches(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: time.Now().UTC()},
	})

	off := false
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{
		InjectWorkingMemory: &off,
		SyncThreads:         &off,
	}))

	if got := b.buildGuidance(t.TempDir(), 0, false); strings.Contains(got, "Nowledge Working Memory") {
		t.Fatalf("关闭注入开关后不应包含 Working Memory 段: %q", got)
	}
	b.syncNowledgeThread("t1", time.Second)
	b.triggerNowledgeSync("t1")
	waitNmemBackend(b)

	if len(fake.requests) != 0 {
		t.Fatalf("关闭开关后不应发请求，实际: %v", fake.requests)
	}
}

// 会话同步只导入用户与助手的正文，并带上正确的 ID 前缀、来源与元数据。
func TestNowledgeSyncFiltersMessages(t *testing.T) {
	dir := gitRepoDir(t, "mcode-nmem")
	fake := newNmemFake(t, "mcode-nmem")
	b, s := newNmemTestBackend(t, dir)

	now := time.Now().UTC()
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "实现登录", CreatedAt: now},
		{Role: "assistant", Kind: "thinking", Text: "思考中", CreatedAt: now},
		{Role: "assistant", Kind: "tool", Text: "Read file", CreatedAt: now},
		{Role: "tool", Kind: "tool", Text: "tool output", CreatedAt: now},
		{Role: "user", Text: "[编排] 已派发子代理：登录", CreatedAt: now},
		{Role: "assistant", Kind: "error", Text: "出错：boom", CreatedAt: now},
		{Role: "assistant", Kind: "assistant", Text: "已完成", CreatedAt: now},
		{Role: "system", Kind: "info", Text: "系统提示", CreatedAt: now},
	})
	// 编排派发的系统消息走 user 角色，同样应被剔除。
	if _, err := s.AppendMessage(context.Background(), store.Message{
		ID: "notice", ThreadID: "t1", Role: "user", Text: "[编排] 子代理汇报：登录已实现", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.syncNowledgeThread("t1", 5*time.Second)

	imports := fake.importCalls()
	if len(imports) != 1 {
		t.Fatalf("/threads/import 次数 = %d, want 1（requests=%v）", len(imports), fake.requests)
	}
	payload := imports[0]
	if got := payload["thread_id"]; got != "mcode-t1" {
		t.Fatalf("thread_id = %v, want mcode-t1", got)
	}
	if got := payload["source"]; got != "mcode" {
		t.Fatalf("source = %v, want mcode", got)
	}
	if got := payload["title"]; got != "会话标题" {
		t.Fatalf("title = %v, want 会话标题", got)
	}
	// git 仓库名与空间同名，空间应解析成 mcode-nmem 而不是 default。
	if got := payload["space_id"]; got != "mcode-nmem" {
		t.Fatalf("space_id = %v, want mcode-nmem（requests=%v）", got, fake.requests)
	}

	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2 条正文", payload["messages"])
	}
	wantRoles := []string{"user", "assistant"}
	wantTexts := []string{"实现登录", "已完成"}
	for i, m := range msgs {
		item := m.(map[string]any)
		if item["role"] != wantRoles[i] || item["content"] != wantTexts[i] {
			t.Fatalf("messages[%d] = %v, want role %s content %s", i, item, wantRoles[i], wantTexts[i])
		}
	}

	meta, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %v", payload["metadata"])
	}
	for _, key := range []string{"project_path", "thread_id", "provider", "model"} {
		if _, ok := meta[key]; !ok {
			t.Fatalf("metadata 缺少 %s: %v", key, meta)
		}
	}
	if meta["thread_id"] != "t1" || meta["provider"] != "claude" || meta["model"] != "opus" {
		t.Fatalf("metadata = %v", meta)
	}
}

// 有效消息少于 2 条时跳过导入。
func TestNowledgeSyncSkipsShortThread(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "thinking", Text: "思考", CreatedAt: time.Now().UTC()},
	})

	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.syncNowledgeThread("t1", 5*time.Second)
	waitNmemBackend(b)

	if n := fake.count("/threads/import"); n != 0 {
		t.Fatalf("有效消息不足 2 条时不应导入，实际 %d 次", n)
	}
}

func TestLocalMemoryGuidance(t *testing.T) {
	workDir := t.TempDir()
	b, _ := newNmemTestBackend(t, workDir)
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	calls := 0
	b.SetLocalMemory(func(budget int) string {
		calls++
		if budget != (config.MemoryConfig{}).WorkingMemoryBudget() {
			t.Fatalf("预算错误: %d", budget)
		}
		return "GLOBAL RULES"
	})
	got := b.buildGuidance(workDir, 0, false)
	if !strings.Contains(got, localMemoryToolsNote+"\n\nGLOBAL RULES") {
		t.Fatalf("本地工具说明和全局规则应依次注入: %q", got)
	}
	if calls != 1 {
		t.Fatalf("会话注入应调用一次全局规则回调, 实际 %d 次", calls)
	}
	// 全局规则对 bee 也注入（Working Memory 同样给 bee），但工具说明只给非 bee 会话。
	bee := b.buildGuidance(workDir, 0, true)
	if strings.Contains(bee, localMemoryToolsNote) || !strings.Contains(bee, "GLOBAL RULES") || calls != 2 {
		t.Fatalf("bee 应只注入全局规则、不注入工具说明: %q, calls=%d", bee, calls)
	}
	fake := newNmemFake(t, "mcode")
	b.SetNowledge(fake.client())
	if got := b.buildGuidance(workDir, 0, false); strings.Contains(got, localMemoryToolsNote) || strings.Contains(got, "GLOBAL RULES") || calls != 2 {
		t.Fatalf("Nowledge 启用时不应注入本地记忆: %q, calls=%d", got, calls)
	}
	if got := b.buildGuidance(workDir, 0, true); strings.Contains(got, localMemoryToolsNote) || strings.Contains(got, "GLOBAL RULES") || calls != 2 {
		t.Fatalf("Nowledge 启用时 bee 也不应注入本地记忆: %q, calls=%d", got, calls)
	}
	b.SetNowledge(nil)
	b.SetLocalMemory(func(int) string { return "  " })
	got = b.buildGuidance(workDir, 0, false)
	if !strings.HasSuffix(got, localMemoryToolsNote) {
		t.Fatalf("空规则不应追加空块: %q", got)
	}
	disabled := false
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{InjectMemoryToolsNote: &disabled}))
	b.SetLocalMemory(func(int) string { return "GLOBAL RULES" })
	got = b.buildGuidance(workDir, 0, false)
	if strings.Contains(got, localMemoryToolsNote) || !strings.Contains(got, "GLOBAL RULES") {
		t.Fatalf("关闭工具说明后仍应注入全局规则: %q", got)
	}
}

// 本地记忆后端不注入 NMEM_SPACE；没注册本地回调时沿用 memspace 的解析结果。
func TestTurnMemoryEnvSkipsLocalBackend(t *testing.T) {
	b, _ := newNmemTestBackend(t, t.TempDir())
	// 临时目录不在 git 仓库内，memspace.Env 会稳定返回 nil。
	workDir := t.TempDir()
	if got := b.turnMemoryEnv(workDir); !reflect.DeepEqual(got, memspace.Env(workDir)) {
		t.Fatalf("未注册本地回调时应沿用 memspace.Env, 实际为 %v", got)
	}
	b.SetLocalMemory(func(int) string { return "GLOBAL RULES" })
	if got := b.turnMemoryEnv(workDir); got != nil {
		t.Fatalf("本地记忆后端不应注入 NMEM_SPACE, 实际为 %v", got)
	}
}

// 开启注入时首轮指导词包含 Working Memory 段，并按预算截断。
func TestNowledgeWorkingMemorySection(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	workDir := t.TempDir()
	b, _ := newNmemTestBackend(t, workDir)
	fake.content = strings.Repeat("记", 3000)

	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{WorkingMemoryMaxCharacters: 1000}))

	got := b.buildGuidance(workDir, 0, false)
	head := "## Nowledge Working Memory (space: default)"
	idx := strings.Index(got, head)
	if idx < 0 {
		t.Fatalf("缺少 Working Memory 段: %q", got)
	}
	section := got[idx:]
	if !strings.Contains(section, "truncated") {
		t.Fatalf("超出预算时应标注已截断: %q", section)
	}
	// 预算按 rune 计数：正文被截到 1000 个汉字。
	if n := strings.Count(section, "记"); n != 1000 {
		t.Fatalf("正文 rune 数 = %d, want 1000", n)
	}

	// 1 分钟缓存：同一 workDir 再次构建不再发请求。
	requests := fake.count("/agent/working-memory")
	b.buildGuidance(workDir, 0, false)
	if n := fake.count("/agent/working-memory"); n != requests {
		t.Fatalf("缓存失效：/agent/working-memory 请求从 %d 增加到 %d", requests, n)
	}
}

// 关闭注入开关时首轮指导词不含 Working Memory 段。
func TestNowledgeWorkingMemoryDisabled(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, _ := newNmemTestBackend(t, t.TempDir())

	off := false
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{InjectWorkingMemory: &off}))

	if got := b.buildGuidance(t.TempDir(), 0, false); strings.Contains(got, "Nowledge Working Memory") {
		t.Fatalf("关闭后不应注入: %q", got)
	}
	if n := fake.count("/agent/working-memory"); n != 0 {
		t.Fatalf("/agent/working-memory 请求次数 = %d, want 0", n)
	}
}

// 记忆工具说明段开关：bee 的 guidance 不含；开关关闭时主会话不含；默认（nil）时主会话包含。
func TestNowledgeMemoryToolsNoteSwitch(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	workDir := t.TempDir()
	b, _ := newNmemTestBackend(t, workDir)

	// 未接入 Nowledge 时均不含
	if got := b.defaultGuidanceBuilder(workDir); strings.Contains(got, "## Nowledge memory tools") {
		t.Fatalf("未接入 Nowledge 时主会话不应包含记忆工具说明: %q", got)
	}
	if got := b.beeGuidanceBuilder(workDir); strings.Contains(got, "## Nowledge memory tools") {
		t.Fatalf("未接入 Nowledge 时 bee 不应包含记忆工具说明: %q", got)
	}

	b.SetNowledge(fake.client())

	// 默认（nil）时：主会话包含，bee 不包含
	defaultLead := b.defaultGuidanceBuilder(workDir)
	if !strings.Contains(defaultLead, "## Nowledge memory tools") {
		t.Fatalf("默认（nil）时主会话应包含记忆工具说明: %q", defaultLead)
	}
	beeLead := b.beeGuidanceBuilder(workDir)
	if strings.Contains(beeLead, "## Nowledge memory tools") {
		t.Fatalf("bee 会话不应包含记忆工具说明: %q", beeLead)
	}

	// 开关关闭时：主会话与 bee 均不含
	off := false
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{
		InjectMemoryToolsNote: &off,
	}))
	disabledLead := b.defaultGuidanceBuilder(workDir)
	if strings.Contains(disabledLead, "## Nowledge memory tools") {
		t.Fatalf("开关关闭时主会话不应包含记忆工具说明: %q", disabledLead)
	}
	disabledBee := b.beeGuidanceBuilder(workDir)
	if strings.Contains(disabledBee, "## Nowledge memory tools") {
		t.Fatalf("开关关闭时 bee 不应包含记忆工具说明: %q", disabledBee)
	}

	// 开关显式开启时：主会话包含，bee 仍不含
	on := true
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{
		InjectMemoryToolsNote: &on,
	}))
	enabledLead := b.defaultGuidanceBuilder(workDir)
	if !strings.Contains(enabledLead, "## Nowledge memory tools") {
		t.Fatalf("开关开启时主会话应包含记忆工具说明: %q", enabledLead)
	}
	enabledBee := b.beeGuidanceBuilder(workDir)
	if strings.Contains(enabledBee, "## Nowledge memory tools") {
		t.Fatalf("开关开启时 bee 仍不应包含记忆工具说明: %q", enabledBee)
	}
}

// setNowledgeIdleDelay 把防抖延迟临时改成毫秒级，测试结束后恢复。
func setNowledgeIdleDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := nowledgeSyncIdleDelay
	nowledgeSyncIdleDelay = d
	t.Cleanup(func() { nowledgeSyncIdleDelay = old })
}

// waitUntilNmem 轮询等待条件成立，避免依赖固定长睡眠。
func waitUntilNmem(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%s）", timeout)
}

// seedNmemThread 在 store 里写一个可自定义元数据的会话及其两条正文消息。
func seedNmemThread(t *testing.T, s *store.Store, thread store.Thread, texts ...string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i, text := range texts {
		role := "user"
		kind := ""
		if i%2 == 1 {
			role, kind = "assistant", "assistant"
		}
		if _, err := s.AppendMessage(ctx, store.Message{
			ID:        fmt.Sprintf("%s-m-%d", thread.ID, i),
			ThreadID:  thread.ID,
			Role:      role,
			Kind:      kind,
			Text:      text,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
}

// 延迟窗口内多次 scheduleNowledgeSync 只导入一次。
func TestNowledgeScheduleDebounces(t *testing.T) {
	setNowledgeIdleDelay(t, 40*time.Millisecond)
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: time.Now().UTC()},
	})
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	for i := 0; i < 3; i++ {
		b.scheduleNowledgeSync("t1")
		time.Sleep(10 * time.Millisecond)
	}
	waitUntilNmem(t, 2*time.Second, func() bool { return fake.count("/threads/import") == 1 })
	time.Sleep(80 * time.Millisecond)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("防抖窗口内的多次排期应只导入 1 次，实际 %d 次", n)
	}
}

// 立即同步会取消待触发的定时器，不会重复导入。
func TestNowledgeSyncCancelsScheduledTimer(t *testing.T) {
	setNowledgeIdleDelay(t, 200*time.Millisecond)
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: time.Now().UTC()},
	})
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	b.scheduleNowledgeSync("t1")
	b.syncNowledgeThread("t1", 5*time.Second)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("/threads/import 次数 = %d, want 1", n)
	}
	if b.cancelNowledgeSyncTimer("t1") {
		t.Fatal("立即同步后不应还有待触发的定时器")
	}
	time.Sleep(300 * time.Millisecond)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("定时器已被取消，不应再导入，实际 %d 次", n)
	}
}

// 有效消息数不变时不重复发请求，新增消息后再发一次。
func TestNowledgeSyncSkipsUnchangedMessages(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: time.Now().UTC()},
	})
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	b.syncNowledgeThread("t1", 5*time.Second)
	b.syncNowledgeThread("t1", 5*time.Second)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("消息没变化时第二次不应发请求，实际 %d 次", n)
	}

	if _, err := s.AppendMessage(context.Background(), store.Message{
		ID: "t1-extra", ThreadID: "t1", Role: "user", Text: "再问一句", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	b.syncNowledgeThread("t1", 5*time.Second)
	imports := fake.importCalls()
	if len(imports) != 2 {
		t.Fatalf("/threads/import 次数 = %d, want 2", len(imports))
	}
	msgs, _ := imports[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("第二次导入的消息数 = %d, want 3", len(msgs))
	}
}

// bee 子会话默认不同步，开启 SyncHelperThreads 后才导入。
func TestNowledgeSkipsHelperThreadsByDefault(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedNmemThread(t, s, store.Thread{
		ID:             "bee1",
		ProjectID:      "p1",
		Title:          "bee 任务",
		IsHelper:       true,
		ParentThreadID: "t1",
	}, "任务说明", "汇报结果")

	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.syncNowledgeThread("bee1", 5*time.Second)
	if n := fake.count("/threads/import"); n != 0 {
		t.Fatalf("bee 子会话默认不应导入，实际 %d 次", n)
	}

	on := true
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{SyncHelperThreads: &on}))
	b.syncNowledgeThread("bee1", 5*time.Second)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("开启 SyncHelperThreads 后应导入 1 次，实际 %d 次", n)
	}
}

// 自动化运行会话默认不同步，开启 SyncAutomationThreads 后才导入。
func TestNowledgeSkipsAutomationThreadsByDefault(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedNmemThread(t, s, store.Thread{
		ID:        "auto1",
		ProjectID: "p1",
		Title:     "Automation: 每日巡检",
	}, "巡检任务", "巡检结果")

	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.syncNowledgeThread("auto1", 5*time.Second)
	if n := fake.count("/threads/import"); n != 0 {
		t.Fatalf("自动化运行会话默认不应导入，实际 %d 次", n)
	}

	on := true
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{SyncAutomationThreads: &on}))
	b.syncNowledgeThread("auto1", 5*time.Second)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("开启 SyncAutomationThreads 后应导入 1 次，实际 %d 次", n)
	}
}

// 只排期未发请求时，Close 不发起任何退出导入、及时返回，持久待同步标记保留给下次启动。
func TestNowledgeCloseLeavesPendingWithoutImport(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	b.scheduleNowledgeSync("t1")
	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("排期后应写入持久待同步标记")
	}

	if d := closeWithin(t, b, 2*time.Second); d > time.Second {
		t.Fatalf("Close 耗时 %s，退出不应等待同步", d)
	}
	if n := fake.count("/threads/import"); n != 0 {
		t.Fatalf("Close 不应发起退出导入，实际 %d 次", n)
	}
	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("Close 后待同步标记应保留")
	}
}

// 未注入客户端（或关闭同步开关）时 scheduleNowledgeSync 不创建定时器。
func TestNowledgeScheduleWithoutClient(t *testing.T) {
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", []store.Message{
		{Role: "user", Text: "hi", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Kind: "assistant", Text: "hello", CreatedAt: time.Now().UTC()},
	})

	b.scheduleNowledgeSync("t1")
	b.mu.Lock()
	n := len(b.nowledgeSync)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("nowledge 为 nil 时不应创建定时器状态，实际 %d 条", n)
	}

	fake := newNmemFake(t, "mcode")
	off := false
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{SyncThreads: &off}))
	b.scheduleNowledgeSync("t1")
	b.mu.Lock()
	n = len(b.nowledgeSync)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("关闭同步开关后不应创建定时器状态，实际 %d 条", n)
	}
}

// Close 取消在飞的导入：不必等原来的 10 秒预算，慢请求被请求取消唤醒，标记保留。
func TestNowledgeCloseCancelsInFlightImport(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	entered := fake.gateImports()
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	b.triggerNowledgeSync("t1")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("导入请求没有在 2 秒内到达服务端")
	}

	// 请求还阻塞在服务端：只有取消 b.ctx 才能让它结束。
	closeWithin(t, b, 2*time.Second)
	// 此时导入请求仍阻塞在屏障上，可以确定地断言它没被当成成功。
	if n := len(fake.importCalls()); n != 0 {
		t.Fatalf("被取消的导入不应算成功，实际记录 %d 次", n)
	}
	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("导入被取消后待同步标记应保留")
	}
	fake.releaseImports()
}

// 真正关闭旧 backend 与 store，再用同一个数据库文件重新 store.Open 建新 backend，
// 证明待同步标记来自磁盘，重启后补传并清标记；成功后再次恢复不重复导入。
func TestNowledgeResumeAfterRestart(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	path := nmemStorePath(t)

	// 先建 backend（AddProject 会写入项目行）再 seed 会话，否则外键约束失败。
	s1 := openNmemStoreAt(t, path)
	b1 := newNmemBackendOnStore(t, s1, t.TempDir())
	b1.SetNowledge(fake.client())
	b1.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	seedThread(t, s1, "t1", nmemTwoMessages())

	b1.scheduleNowledgeSync("t1")
	closeWithin(t, b1, 2*time.Second)
	if !nmemPendingContains(t, s1, "t1") {
		t.Fatal("退出前应保留持久待同步标记")
	}
	// 关掉旧 store，重新打开同一个库：标记必须来自磁盘而不是同一个 Store 对象。
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭旧 store 失败: %v", err)
	}

	s2 := openNmemStoreAt(t, path)
	b2 := newNmemBackendOnStore(t, s2, t.TempDir())
	b2.SetNowledge(fake.client())
	b2.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b2.ResumeNowledgeSync()
	waitNmemBackend(b2)

	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("重启后应补传 1 次，实际 %d 次", n)
	}
	if nmemPendingContains(t, s2, "t1") {
		t.Fatal("补传成功后应清掉持久标记")
	}

	// 再次恢复：消息没变化，不应重复导入。
	b2.ResumeNowledgeSync()
	waitNmemBackend(b2)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("再次恢复不应重复导入，实际 %d 次", n)
	}
}

// HTTP 失败时保留标记，重启后可以补传成功。
func TestNowledgeResumeRetriesAfterImportFailure(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	fake.setImportStatus(http.StatusInternalServerError)
	path := nmemStorePath(t)

	s1 := openNmemStoreAt(t, path)
	b1 := newNmemBackendOnStore(t, s1, t.TempDir())
	b1.SetNowledge(fake.client())
	b1.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	seedThread(t, s1, "t1", nmemTwoMessages())

	b1.scheduleNowledgeSync("t1")
	b1.ResumeNowledgeSync()
	waitNmemBackend(b1)
	closeWithin(t, b1, 2*time.Second)

	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("应尝试导入 1 次，实际 %d 次", n)
	}
	if !nmemPendingContains(t, s1, "t1") {
		t.Fatal("HTTP 失败后待同步标记应保留")
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭旧 store 失败: %v", err)
	}

	fake.setImportStatus(0)
	s2 := openNmemStoreAt(t, path)
	b2 := newNmemBackendOnStore(t, s2, t.TempDir())
	b2.SetNowledge(fake.client())
	b2.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b2.ResumeNowledgeSync()
	waitNmemBackend(b2)

	if n := fake.count("/threads/import"); n != 2 {
		t.Fatalf("重启后应重试导入，实际累计 %d 次", n)
	}
	if nmemPendingContains(t, s2, "t1") {
		t.Fatal("重试成功后应清掉持久标记")
	}
}

// 导入期间追加消息并再次排期：旧批次成功不清标记，后续补传包含新增消息。
func TestNowledgeImportKeepsPendingWhenNewWorkArrives(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	entered := fake.gateImports()
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	b.triggerNowledgeSync("t1")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("导入请求没有在 2 秒内到达服务端")
	}

	if _, err := s.AppendMessage(context.Background(), store.Message{
		ID: "t1-extra-1", ThreadID: "t1", Role: "user", Text: "追加一句", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	b.scheduleNowledgeSync("t1")
	fake.releaseImports()
	waitNmemBackend(b)

	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("导入期间到达的新工作应保留待同步标记")
	}

	b.ResumeNowledgeSync()
	waitNmemBackend(b)
	imports := fake.importCalls()
	if len(imports) != 2 {
		t.Fatalf("/threads/import 次数 = %d, want 2", len(imports))
	}
	msgs, ok := imports[1]["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("补传的消息数 = %d, want 3", len(msgs))
	}
	if nmemPendingContains(t, s, "t1") {
		t.Fatal("补传成功后应清掉持久标记")
	}
}

// 两个会话并发记账都保留，不会互相覆盖。
func TestNowledgeConcurrentPendingMarks(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	seedThread(t, s, "t2", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	var wg sync.WaitGroup
	for _, id := range []string{"t1", "t2"} {
		wg.Add(1)
		go func(threadID string) {
			defer wg.Done()
			b.scheduleNowledgeSync(threadID)
		}(id)
	}
	wg.Wait()

	for _, id := range []string{"t1", "t2"} {
		if !nmemPendingContains(t, s, id) {
			t.Fatalf("并发记账丢失会话 %s：%v", id, nmemPendingIDs(t, s))
		}
	}
}

// 关闭同步开关时恢复不发请求也不清标记，重新开启后可以补传。
func TestNowledgeResumeDisabledKeepsPending(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.scheduleNowledgeSync("t1")

	off := false
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{SyncThreads: &off}))
	b.ResumeNowledgeSync()
	waitNmemBackend(b)
	if len(fake.requests) != 0 {
		t.Fatalf("关闭同步后不应发请求，实际: %v", fake.requests)
	}
	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("关闭同步时不应清掉待同步标记")
	}

	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.ResumeNowledgeSync()
	waitNmemBackend(b)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("重新开启后应补传 1 次，实际 %d 次", n)
	}
	if nmemPendingContains(t, s, "t1") {
		t.Fatal("补传成功后应清掉持久标记")
	}
}

// 多次恢复合并成一次导入，Close 之后不再有后台任务使用 store。
func TestNowledgeResumeThenCloseLeavesNoBackgroundDBUse(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	path := nmemStorePath(t)
	s := openNmemStoreAt(t, path)
	b := newNmemBackendOnStore(t, s, t.TempDir())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	// 先建 backend（项目行已落库）再 seed 会话，否则外键约束失败。
	seedThread(t, s, "t1", nmemTwoMessages())
	b.scheduleNowledgeSync("t1")

	for i := 0; i < 3; i++ {
		b.ResumeNowledgeSync()
	}
	waitNmemBackend(b)
	closeWithin(t, b, 2*time.Second)

	// Close 返回即代表在飞同步与记账都已结束（wg.Wait 加落盘屏障），不需要额外睡眠。
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("多次恢复应只导入 1 次，实际 %d 次", n)
	}
	// Close 返回后不应还有后台任务使用 store：此时关闭 store 必须安全。
	if err := s.Close(); err != nil {
		t.Fatalf("Close 之后关闭 store 失败: %v", err)
	}
}

// 恢复不是新工作：阻塞首个恢复请求期间多次/并发恢复，放行后恰好只有一次请求，
// 不增代也不设补跑；失败标记保留，之后显式再恢复才发第二次请求。
func TestNowledgeResumeSkipsWhileRunningAndKeepsPending(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	fake.setImportStatus(http.StatusInternalServerError)
	entered := fake.gateImports()
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.scheduleNowledgeSync("t1")

	b.ResumeNowledgeSync()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("恢复导入没有在 2 秒内到达服务端")
	}

	b.mu.Lock()
	genBefore := b.nowledgeSync["t1"].generation
	b.mu.Unlock()

	// 首个请求还没完成：多次与并发恢复都不应追加请求，也不应增代。
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.ResumeNowledgeSync()
		}()
	}
	wg.Wait()
	b.ResumeNowledgeSync()

	b.mu.Lock()
	genAfter := b.nowledgeSync["t1"].generation
	pendingAfter := b.nowledgeSync["t1"].pending
	b.mu.Unlock()
	if genAfter != genBefore {
		t.Fatalf("恢复不应增代：%d → %d", genBefore, genAfter)
	}
	if pendingAfter {
		t.Fatal("恢复不应设置补跑标记")
	}

	// 重复恢复没有留补跑：放行后 running 收尾不会再自己跑一遍。
	fake.releaseImports()
	waitNmemBackend(b)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("重复恢复不应追加请求，实际 %d 次", n)
	}
	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("HTTP 失败后待同步标记应保留")
	}

	// 显式再恢复一次才发第二次请求。
	b.ResumeNowledgeSync()
	waitNmemBackend(b)
	if n := fake.count("/threads/import"); n != 2 {
		t.Fatalf("显式再次恢复应发第二次请求，实际累计 %d 次", n)
	}
}

// 成功场景：恢复阻塞期间多次恢复也只导入一次，成功后清标记，不会无意义补跑。
func TestNowledgeResumeSuccessNoExtraRun(t *testing.T) {
	setNowledgeIdleDelay(t, time.Hour)
	fake := newNmemFake(t, "mcode")
	entered := fake.gateImports()
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b.scheduleNowledgeSync("t1")

	b.ResumeNowledgeSync()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("恢复导入没有在 2 秒内到达服务端")
	}
	b.ResumeNowledgeSync()
	b.ResumeNowledgeSync()
	fake.releaseImports()
	waitNmemBackend(b)

	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("重复恢复不应追加请求，实际 %d 次", n)
	}
	if nmemPendingContains(t, s, "t1") {
		t.Fatal("补传成功后应清掉持久标记")
	}
	// 标记已清：再次恢复读不到待同步集合，不应再发请求。
	b.ResumeNowledgeSync()
	waitNmemBackend(b)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("成功后再次恢复不应发请求，实际 %d 次", n)
	}
}

// 退出取消后才走到收尾的轮次（首次排期发生在 Close 期间）也要把待同步标记落盘：
// 退出不发起任何 HTTP，重开数据库后能补传。
func TestNowledgeClosePersistsTurnScheduledDuringShutdown(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	path := nmemStorePath(t)
	s1 := openNmemStoreAt(t, path)
	b1 := newNmemBackendOnStore(t, s1, t.TempDir())
	b1.SetNowledge(fake.client())
	b1.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	seedThread(t, s1, "t1", nmemTwoMessages())

	// 模拟一轮对话正在收尾：轮次本身登记在 b.wg 上（与 launchTurnRun 一致），
	// Close 取消 b.ctx 之后才走到 updateTurnFinish → scheduleNowledgeSync。
	scheduled := make(chan struct{})
	b1.wg.Add(1)
	go func() {
		defer b1.wg.Done()
		defer close(scheduled)
		<-b1.ctx.Done()
		b1.scheduleNowledgeSync("t1")
	}()

	closeWithin(t, b1, 2*time.Second)
	select {
	case <-scheduled:
	default:
		t.Fatal("Close 返回时收尾排期还没结束")
	}
	if n := fake.count("/threads/import"); n != 0 {
		t.Fatalf("退出不应发起导入，实际 %d 次", n)
	}
	if !nmemPendingContains(t, s1, "t1") {
		t.Fatal("退出期间才排期的会话也应写进持久待同步集合")
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭旧 store 失败: %v", err)
	}

	s2 := openNmemStoreAt(t, path)
	b2 := newNmemBackendOnStore(t, s2, t.TempDir())
	b2.SetNowledge(fake.client())
	b2.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))
	b2.ResumeNowledgeSync()
	waitNmemBackend(b2)
	if n := fake.count("/threads/import"); n != 1 {
		t.Fatalf("重启后应补传 1 次，实际 %d 次", n)
	}
	if nmemPendingContains(t, s2, "t1") {
		t.Fatal("补传成功后应清掉持久标记")
	}
}

// 直接调用同步入口（删除、压缩路径）也受 Close 生命周期管理：Close 会等它结束才返回，
// 在飞的请求被 b.ctx 取消唤醒，Close 之后 store 可安全关闭。
func TestNowledgeDirectSyncWaitsForClose(t *testing.T) {
	fake := newNmemFake(t, "mcode")
	entered := fake.gateImports()
	b, s := newNmemTestBackend(t, t.TempDir())
	seedThread(t, s, "t1", nmemTwoMessages())
	b.SetNowledge(fake.client())
	b.SetNowledgeSettingsFunc(nmemSettings(config.MemoryConfig{}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.syncNowledgeThread("t1", 10*time.Second)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("导入请求没有在 2 秒内到达服务端")
	}

	// 请求还阻塞在服务端：Close 必须先取消 b.ctx，并等到这次直接调用返回。
	closeWithin(t, b, 2*time.Second)
	// syncNowledgeThread 内的 wg.Done 在返回外层 goroutine 之前执行，Close 的 wg.Wait
	// 可能先返回；这里给外层 goroutine 的收尾回调留一个调度窗口，一直不完成仍算失败。
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 返回后直接同步调用仍未结束")
	}
	if n := len(fake.importCalls()); n != 0 {
		t.Fatalf("被取消的导入不应算成功，实际记录 %d 次", n)
	}
	if !nmemPendingContains(t, s, "t1") {
		t.Fatal("导入被取消后待同步标记应保留")
	}
	fake.releaseImports()
	if err := s.Close(); err != nil {
		t.Fatalf("Close 之后关闭 store 失败: %v", err)
	}
}
