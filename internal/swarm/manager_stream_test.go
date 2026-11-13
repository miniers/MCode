package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/testenv"
)

// testClock 提供可手动推进的注入时钟，驱动看门狗与落地等待超时。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// beeByThread 从状态快照中取出指定 threadID 的 bee。
func beeByThread(t *testing.T, m *Manager, leadThreadID, threadID string) Bee {
	t.Helper()
	for _, h := range m.State(leadThreadID).Bees {
		if h.ThreadID == threadID {
			return h
		}
	}
	t.Fatalf("状态中未找到 bee %s", threadID)
	return Bee{}
}

// 流式逐条派发：连续三次 DispatchSingleEntry 产生递增 Index 与按顺序的花名册名字。
func TestDispatchSingleEntry_StreamedOrder(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-stream-order"
	var indexes []int
	var names []string
	for i := 0; i < 3; i++ {
		bee, notices, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Entry: Delegation{
				Task:   fmt.Sprintf("task-%d", i),
				Prompt: fmt.Sprintf("prompt-%d", i),
			},
		})
		if err != nil {
			t.Fatalf("第 %d 次 DispatchSingleEntry 失败: %v", i, err)
		}
		if len(notices) != 0 {
			t.Errorf("第 %d 次派发不应产生 notices: %v", i, notices)
		}
		if bee.ThreadID == "" {
			t.Fatalf("第 %d 次派发未返回 bee", i)
		}
		indexes = append(indexes, bee.Index)
		names = append(names, bee.Name)
	}

	for i, index := range indexes {
		if index != i {
			t.Errorf("第 %d 个 bee 的 Index = %d，期望 %d（按派发顺序递增）", i, index, i)
		}
		if want := RosterName(i); names[i] != want {
			t.Errorf("第 %d 个 bee 的名字 = %q，期望 %q", i, names[i], want)
		}
	}
	if st := mgr.State(leadID); len(st.Bees) != 3 {
		t.Fatalf("状态中应有 3 个 bee，实际 %d", len(st.Bees))
	}
}

// bee 预算：只统计 RequestStartedAt 之后启动的 bee，达到 DefaultBeeBudget 后返回 ErrBeeBudget。
func TestDispatchSingleEntry_BeeBudget(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
	})

	leadID := "lead-bee-budget"
	first, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "first", Prompt: "first"},
	})
	if err != nil {
		t.Fatalf("预算未满时应能派发: %v", err)
	}

	// 标记请求开始：此前启动的 bee 不计入本次请求的预算。
	clock.Advance(time.Second)
	mgr.BeginLeadRequest(leadID)

	mgr.mu.Lock()
	lead := mgr.leads[leadID]
	lead.bees = []Bee{first}
	for i := 1; i < DefaultBeeBudget; i++ {
		lead.bees = append(lead.bees, Bee{
			ThreadID:  fmt.Sprintf("fabricated-%d", i),
			Index:     i,
			Status:    StatusRunning,
			StartedAt: clock.Now(),
		})
	}
	lead.nextIndex = DefaultBeeBudget
	mgr.mu.Unlock()

	// 预算内（DefaultBeeBudget-1 个新 bee）仍可继续派发。
	if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "second", Prompt: "second"},
	}); err != nil {
		t.Fatalf("预算内应能继续派发: %v", err)
	}

	// 达到 DefaultBeeBudget 后拒绝继续派发。
	if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "overflow", Prompt: "overflow"},
	}); !errors.Is(err, ErrBeeBudget) {
		t.Fatalf("达到 bee 预算后应返回 ErrBeeBudget，实际: %v", err)
	}
}

// 委派轮次：BeginLeadRequest 重置计数，NoteDelegationRound 递增并随 LeadRecord 持久化、可恢复。
func TestLeadRequestRounds_PersistRestore(t *testing.T) {
	testenv.TempHome(t)
	clock := newTestClock()
	persister := newFakePersister()

	mgr := NewManager(Options{Persister: persister, Now: clock.Now})
	leadID := "lead-rounds"

	mgr.BeginLeadRequest(leadID)
	if n := mgr.DelegationRounds(leadID); n != 0 {
		t.Fatalf("新请求的委派轮次应为 0，实际 %d", n)
	}
	for want := 1; want <= MaxDelegationRounds; want++ {
		if got := mgr.NoteDelegationRound(leadID); got != want {
			t.Fatalf("NoteDelegationRound = %d，期望 %d", got, want)
		}
	}
	if n := mgr.DelegationRounds(leadID); n != MaxDelegationRounds {
		t.Fatalf("委派轮次应为 %d，实际 %d", MaxDelegationRounds, n)
	}

	persister.mu.Lock()
	rec := persister.leads[leadID]
	persister.mu.Unlock()
	if rec.DelegationRounds != MaxDelegationRounds {
		t.Fatalf("持久化的委派轮次 = %d，期望 %d", rec.DelegationRounds, MaxDelegationRounds)
	}
	if rec.RequestStartedAt.IsZero() {
		t.Fatalf("请求起始时间应随 LeadRecord 持久化")
	}

	restored := NewManager(Options{Persister: persister, Now: clock.Now})
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}
	if n := restored.DelegationRounds(leadID); n != MaxDelegationRounds {
		t.Fatalf("恢复后的委派轮次 = %d，期望 %d", n, MaxDelegationRounds)
	}
	restored.mu.Lock()
	started := restored.leads[leadID].requestStartedAt
	restored.mu.Unlock()
	if !started.Equal(rec.RequestStartedAt) {
		t.Errorf("恢复后的请求起始时间 = %v，期望 %v", started, rec.RequestStartedAt)
	}
}

// 看门狗：2 分钟无输出标记思考中、出现进展立即归零、默认 15 分钟无输出且无文件改动熔断。
func TestWatchdog_ThinkingAndNoEditStall(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	var stopMu sync.Mutex
	var stopped []string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stopped = append(stopped, threadID)
			stopMu.Unlock()
			return nil
		},
	})

	leadID := "lead-watchdog"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID

	// 第一次 Tick 只建立进度快照，不标记思考。
	mgr.Tick(ctx)
	if h := beeByThread(t, mgr, leadID, beeThreadID); h.ThinkingMinutes != 0 {
		t.Fatalf("初始 ThinkingMinutes 应为 0，实际 %d", h.ThinkingMinutes)
	}

	// 无输出达到 2 分钟后标记为思考中，且不能被停止。
	clock.Advance(2 * time.Minute)
	mgr.Tick(ctx)
	if h := beeByThread(t, mgr, leadID, beeThreadID); h.ThinkingMinutes != 2 {
		t.Fatalf("2 分钟无输出后 ThinkingMinutes 应为 2，实际 %d", h.ThinkingMinutes)
	} else if h.Status != StatusRunning {
		t.Fatalf("思考中的 bee 不应被停止，实际状态 %s", h.Status)
	}

	// 出现新进展后立即归零。
	mgr.mu.Lock()
	mgr.leads[leadID].bees[0].Summary = "正在处理"
	mgr.mu.Unlock()
	clock.Advance(30 * time.Second)
	mgr.Tick(ctx)
	if h := beeByThread(t, mgr, leadID, beeThreadID); h.ThinkingMinutes != 0 {
		t.Fatalf("重新有进展后 ThinkingMinutes 应归零，实际 %d", h.ThinkingMinutes)
	}

	// 无输出且工作副本始终没有文件改动：默认 15 分钟熔断，5 分钟时仍正常运行。
	clock.Advance(5 * time.Minute)
	mgr.Tick(ctx)
	if h := beeByThread(t, mgr, leadID, beeThreadID); h.Status != StatusRunning {
		t.Fatalf("5 分钟无输出且无改动时仍应运行（默认 15 分钟），实际状态 %s", h.Status)
	}

	clock.Advance(10 * time.Minute)
	mgr.Tick(ctx)
	h := beeByThread(t, mgr, leadID, beeThreadID)
	if h.Status != StatusFailed {
		t.Fatalf("15 分钟无输出且无改动应判为失败，实际状态 %s", h.Status)
	}
	if !strings.Contains(h.Summary, "看门狗") || !strings.Contains(h.Summary, "无文件改动") {
		t.Errorf("熔断原因不符: %q", h.Summary)
	}
	if h.ThinkingMinutes != 0 {
		t.Errorf("熔断后 ThinkingMinutes 应归零，实际 %d", h.ThinkingMinutes)
	}
	stopMu.Lock()
	stopCount := len(stopped)
	stopMu.Unlock()
	if stopCount != 1 || stopped[0] != beeThreadID {
		t.Errorf("应恰好调用一次 StopSession，实际 %v", stopped)
	}
}

// 落地等待：Lead 正在编辑同一文件时保持 running 等待，落定后合入；持续占用则超时并保存补丁。
func TestTick_SettledCheckoutWaitAndTimeout(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	var touchedMu sync.Mutex
	touched := []string{}
	active := false
	mgr := NewManager(Options{
		Git:           runner,
		Driver:        driver,
		WorktreeDir:   t.TempDir(),
		Now:           clock.Now,
		SettleTimeout: 30 * time.Minute,
		LeadTouchedPaths: func(leadThreadID string) ([]string, bool) {
			touchedMu.Lock()
			defer touchedMu.Unlock()
			return append([]string(nil), touched...), active
		},
	})

	leadID := "lead-settle"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task 1", Prompt: "prompt 1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	first := bees[0]
	if err := os.WriteFile(filepath.Join(first.WorktreePath, "foo.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	driver.setSessionState(first.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})

	// Lead 正在编辑 ./foo.txt（斜杠清理后与改动文件相交）：本 tick 不落地。
	touchedMu.Lock()
	touched = []string{"./foo.txt"}
	active = true
	touchedMu.Unlock()

	finishedAt := clock.Now()
	mgr.Tick(ctx)
	h := beeByThread(t, mgr, leadID, first.ThreadID)
	if h.Status != StatusRunning {
		t.Fatalf("等待落定期间应保持 running，实际状态 %s", h.Status)
	}
	if h.MergeWaitSince == nil {
		t.Fatalf("等待落定时应记录 MergeWaitSince")
	}
	if h.FinishedAt == nil || !h.FinishedAt.Equal(finishedAt) {
		t.Fatalf("等待期间结束时间不符: %v", h.FinishedAt)
	}
	clock.Advance(time.Minute)
	mgr.Tick(ctx)
	h = beeByThread(t, mgr, leadID, first.ThreadID)
	if h.Status != StatusRunning || h.FinishedAt == nil || !h.FinishedAt.Equal(finishedAt) {
		t.Fatalf("再次等待后结束时间不符: %+v", h)
	}
	if _, err := os.Stat(filepath.Join(dir, "foo.txt")); !os.IsNotExist(err) {
		t.Fatalf("等待期间不应写入主工作区: %v", err)
	}

	// Lead 落定后正常合入，并清除等待起点。
	touchedMu.Lock()
	active = false
	touchedMu.Unlock()

	clock.Advance(time.Minute)
	mgr.Tick(ctx)
	h = beeByThread(t, mgr, leadID, first.ThreadID)
	if h.Status != StatusCompleted || !h.Merged || h.MergeError != "" {
		t.Fatalf("落定后应完成合入: %+v", h)
	}
	if h.MergeWaitSince != nil {
		t.Fatalf("合入后应清除 MergeWaitSince")
	}
	if h.FinishedAt == nil || !h.FinishedAt.Equal(finishedAt) {
		t.Fatalf("合入后结束时间不符: %v", h.FinishedAt)
	}
	content, err := os.ReadFile(filepath.Join(dir, "foo.txt"))
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("主工作区未合入 bee 改动: %q, %v", string(content), err)
	}

	// 第二个 bee：Lead 持续占用同一文件，等待超时后补丁另存为 .patch 文件。
	secondBees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Birch", Task: "task 2", Prompt: "prompt 2"}},
	})
	if err != nil {
		t.Fatalf("第二次 Dispatch 失败: %v", err)
	}
	second := secondBees[0]
	if err := os.WriteFile(filepath.Join(second.WorktreePath, "bar.txt"), []byte("bar\n"), 0644); err != nil {
		t.Fatalf("写入第二个 bee 工作副本失败: %v", err)
	}
	driver.setSessionState(second.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})

	touchedMu.Lock()
	touched = []string{"bar.txt"}
	active = true
	touchedMu.Unlock()

	mgr.Tick(ctx)
	h2 := beeByThread(t, mgr, leadID, second.ThreadID)
	if h2.Status != StatusRunning || h2.MergeWaitSince == nil {
		t.Fatalf("Lead 占用同一文件时应进入等待: %+v", h2)
	}

	clock.Advance(30 * time.Minute)
	mgr.Tick(ctx)
	h2 = beeByThread(t, mgr, leadID, second.ThreadID)
	if !strings.Contains(h2.MergeError, "等待主工作区落定超时") {
		t.Fatalf("超时后应写入 MergeError，实际: %+v", h2)
	}
	if h2.Merged {
		t.Errorf("超时后不应标记为已合入")
	}
	if h2.MergeWaitSince != nil {
		t.Errorf("超时后应清除 MergeWaitSince")
	}
	if _, err := os.Stat(filepath.Join(dir, "bar.txt")); !os.IsNotExist(err) {
		t.Fatalf("超时后不应写入主工作区: %v", err)
	}
	_, patchPath, found := strings.Cut(h2.MergeError, "补丁已保存到 ")
	if !found || patchPath == "" {
		t.Fatalf("超时后应保存补丁并写入路径: %q", h2.MergeError)
	}
	if _, err := os.Stat(patchPath); err != nil {
		t.Fatalf("超时后补丁应保存到 %s: %v", patchPath, err)
	}
}

// 目标工作区有未提交改动时：与 bee 改动不在同一行则可以直接落地，两处改动同时保留在目标工作区。
func TestTick_MergeWaitsForLocalEdits(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	// 准备 3 行文本并提交
	initial := "# Demo Repo\nline 2\nline 3\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(initial), 0644); err != nil {
		t.Fatalf("写入 README.md: %v", err)
	}
	gitTestRun(t, dir, "add", "README.md")
	gitTestRun(t, dir, "commit", "-q", "-m", "init lines")

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-local-edits"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Ivan", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]
	// bee 修改第 3 行
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "README.md"), []byte("# Demo Repo\nline 2\nline 3 from bee\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	// 目标工作区有用户未提交改动（修改第 1 行）
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Demo Repo modified by user\nline 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("写入目标工作区失败: %v", err)
	}
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})

	mgr.Tick(ctx)
	h := beeByThread(t, mgr, leadID, bee.ThreadID)
	if h.Status != StatusCompleted || !h.Merged || h.MergeError != "" {
		t.Fatalf("未提交改动不在同一行时应直接落地合入: %+v", h)
	}
	if h.MergeWaitSince != nil {
		t.Errorf("合入后不应保留 MergeWaitSince")
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("读取目标工作区失败: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, "modified by user") || !strings.Contains(contentStr, "from bee") {
		t.Fatalf("合入后目标工作区应同时包含用户改动与 bee 改动: %q", contentStr)
	}
}

// 用户把自己的改动提交了、且与 bee 改动在同一处引发冲突时：
// 主工作区文件与落地前完全一致、不含冲突标记，冲突被交回给 bee 在其 worktree 中解决（包含冲突标记），
// Driver 收到一次新的 StartSession，新 bee 的 ConflictHandbacks 为 1。
func TestTick_MergeConflictsWithCommittedUserEdits(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-conflict-user-edits"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Ivan", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "README.md"), []byte("# Demo Repo\nhead line\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	// 目标工作区同路径上是用户自己提交的、与补丁冲突的内容
	userContent := "# Demo Repo\nmine line\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(userContent), 0644); err != nil {
		t.Fatalf("写入目标工作区失败: %v", err)
	}
	gitTestRun(t, dir, "add", "README.md")
	gitTestRun(t, dir, "commit", "-q", "-m", "user edits")
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})

	mgr.Tick(ctx)

	// 1. 验证主工作区文件与落地前完全一致，且无冲突标记
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("读取目标工作区失败: %v", err)
	}
	if string(content) != userContent {
		t.Fatalf("主工作区文件应与落地前完全一致: got %q, want %q", string(content), userContent)
	}
	if strings.Contains(string(content), "<<<<<<<") || strings.Contains(string(content), ">>>>>>>") {
		t.Fatalf("主工作区不应包含冲突标记: %q", string(content))
	}

	// 2. 验证冲突被交回：Driver 收到新的 StartSession，新 bee 状态为 running 且 ConflictHandbacks 为 1
	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("预期 1 个 bee，实际: %d", len(st.Bees))
	}
	newBee := st.Bees[0]
	if newBee.ThreadID == bee.ThreadID {
		t.Fatalf("冲突交回后应生成新的会话 ThreadID")
	}
	if newBee.Status != StatusRunning {
		t.Fatalf("交回的新 bee 应处于 running 状态，实际: %s", newBee.Status)
	}
	if newBee.ConflictHandbacks != 1 {
		t.Fatalf("新 bee 的 ConflictHandbacks 应为 1，实际: %d", newBee.ConflictHandbacks)
	}
	if !strings.Contains(newBee.Prompt, "冲突标记") {
		t.Fatalf("新 bee 的 Prompt 应包含冲突解决引导，实际: %q", newBee.Prompt)
	}

	// 3. 验证 bee worktree 中写入了冲突标记
	wtContent, err := os.ReadFile(filepath.Join(bee.WorktreePath, "README.md"))
	if err != nil {
		t.Fatalf("读取 bee 工作副本失败: %v", err)
	}
	if !strings.Contains(string(wtContent), "<<<<<<<") || !strings.Contains(string(wtContent), ">>>>>>>") {
		t.Fatalf("bee worktree 中应包含冲突标记，实际: %q", string(wtContent))
	}
}

// 批次落地回调：全部 bee 结束且合入后按分组回调一次，重复 Tick 不重复回调。
func TestBatchLandedCallback_FiresOnce(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	var mu sync.Mutex
	var batches []LandedBatch
	reports := 0
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport: func(leadThreadID, message string) error {
			mu.Lock()
			reports++
			mu.Unlock()
			return nil
		},
		OnBatchLanded: func(ctx context.Context, b LandedBatch) {
			mu.Lock()
			batches = append(batches, b)
			mu.Unlock()
		},
	})

	leadID := "lead-batch"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Aspen", Task: "task 1", Prompt: "prompt 1"},
			{Name: "Birch", Task: "task 2", Prompt: "prompt 2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	for i, h := range bees {
		if err := os.WriteFile(filepath.Join(h.WorktreePath, fmt.Sprintf("f%d.txt", i)), []byte("x\n"), 0644); err != nil {
			t.Fatalf("写入 bee 工作副本失败: %v", err)
		}
		driver.setSessionState(h.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	}

	mgr.Tick(ctx)

	mu.Lock()
	got := len(batches)
	var first LandedBatch
	if got > 0 {
		first = batches[0]
	}
	reportCount := reports
	mu.Unlock()

	if got != 1 {
		t.Fatalf("OnBatchLanded 应回调一次，实际 %d", got)
	}
	if reportCount != 1 {
		t.Errorf("汇报应发送一次，实际 %d", reportCount)
	}
	if len(first.Bees) != 2 {
		t.Fatalf("批次应包含 2 个 bee，实际 %d", len(first.Bees))
	}
	if first.ProjectID != "proj-1" || first.TargetWorkDir != dir {
		t.Errorf("批次分组信息不符: %+v", first)
	}
	for _, h := range first.Bees {
		if !h.Merged || h.MergeError != "" || !h.Delivered {
			t.Errorf("批次 bee 状态不符: %+v", h)
		}
	}

	mgr.Tick(ctx)
	mu.Lock()
	got = len(batches)
	mu.Unlock()
	if got != 1 {
		t.Fatalf("重复 Tick 后 OnBatchLanded 仍应只回调一次，实际 %d", got)
	}
}

// 脏路径不再触发等待：未提交改动无冲突时直接落地合入；Lead 忙碌类等待仍用 SettleTimeout（默认 30 分钟）。
func TestTick_DirtyPathWaitUsesShortTimeout(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	// 准备多行文件以避免内容冲突
	initial := "# Demo Repo\nline 2\nline 3\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(initial), 0644); err != nil {
		t.Fatalf("写入 README.md: %v", err)
	}
	gitTestRun(t, dir, "add", "README.md")
	gitTestRun(t, dir, "commit", "-q", "-m", "init lines")

	var touchedMu sync.Mutex
	var touched []string
	active := false
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		LeadTouchedPaths: func(leadThreadID string) ([]string, bool) {
			touchedMu.Lock()
			defer touchedMu.Unlock()
			return append([]string(nil), touched...), active
		},
	})

	leadID := "lead-dirty-short-timeout"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task 1", Prompt: "prompt 1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	first := bees[0]
	// bee 改第 3 行
	if err := os.WriteFile(filepath.Join(first.WorktreePath, "README.md"), []byte("# Demo Repo\nline 2\nline 3 from bee\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	// 目标工作区有用户未提交改动（改第 1 行）
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Demo Repo modified by user\nline 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("写入目标工作区失败: %v", err)
	}
	driver.setSessionState(first.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})

	mgr.Tick(ctx)
	h := beeByThread(t, mgr, leadID, first.ThreadID)
	// 脏路径不再触发等待，直接落地
	if h.Status != StatusCompleted || !h.Merged {
		t.Fatalf("脏路径不再触发等待，应直接合入: %+v", h)
	}
	if h.MergeWaitSince != nil {
		t.Fatalf("不应记录 MergeWaitSince")
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("读取目标工作区失败: %v", err)
	}
	if !strings.Contains(string(content), "modified by user") || !strings.Contains(string(content), "from bee") {
		t.Fatalf("主工作区应同时保留用户改动与 bee 改动: %q", string(content))
	}

	// 第二个 bee：Lead 持续占用同一文件，这类等待仍用 SettleTimeout（30 分钟）。
	secondBees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Birch", Task: "task 2", Prompt: "prompt 2"}},
	})
	if err != nil {
		t.Fatalf("第二次 Dispatch 失败: %v", err)
	}
	second := secondBees[0]
	if err := os.WriteFile(filepath.Join(second.WorktreePath, "foo.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("写入第二个 bee 工作副本失败: %v", err)
	}
	driver.setSessionState(second.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})

	touchedMu.Lock()
	touched = []string{"foo.txt"}
	active = true
	touchedMu.Unlock()

	mgr.Tick(ctx)
	h2 := beeByThread(t, mgr, leadID, second.ThreadID)
	if h2.Status != StatusRunning || h2.MergeWaitSince == nil {
		t.Fatalf("Lead 占用同一文件时应进入等待: %+v", h2)
	}

	// 10 分钟时仍应等待：Lead 忙碌类等待不受 3 分钟的脏路径上限影响。
	clock.Advance(10 * time.Minute)
	mgr.Tick(ctx)
	h2 = beeByThread(t, mgr, leadID, second.ThreadID)
	if h2.Status != StatusRunning {
		t.Fatalf("10 分钟时 Lead 忙碌等待应继续，实际状态 %s", h2.Status)
	}

	// 累计 30 分钟：SettleTimeout 到期。
	clock.Advance(20 * time.Minute)
	mgr.Tick(ctx)
	h2 = beeByThread(t, mgr, leadID, second.ThreadID)
	if !strings.Contains(h2.MergeError, "等待主工作区落定超时（30 分钟）") {
		t.Fatalf("Lead 忙碌等待应按 30 分钟超时，实际: %q", h2.MergeError)
	}
	if h2.Merged {
		t.Fatalf("超时后不应标记为已合入: %+v", h2)
	}
}

// 汇报里补丁已另存时给出补丁路径与重试指引；已合入的 bee 不出现这些内容。
func TestBuildReport_SavedPatchRetryGuidance(t *testing.T) {
	const patchPath = "/tmp/swarm-patches/lead-0.patch"
	const dirtyPatchPath = "/tmp/swarm-patches/lead-1.patch"

	conflictBee := Bee{
		Name:          "Aspen",
		Task:          "task 1",
		Status:        StatusCompleted,
		ChangedFiles:  []string{"internal/ui/sidebar.go"},
		ConflictFiles: []string{"internal/ui/sidebar.go"},
		MergeError:    "补丁合并冲突，已复原冲突文件，补丁已保存到 " + patchPath,
	}
	dirtyBee := Bee{
		Name:         "Birch",
		Task:         "task 2",
		Status:       StatusCompleted,
		ChangedFiles: []string{"internal/swarm/manager.go", "internal/ui/sidebar.go"},
		MergeError:   "等待主工作区落定超时（3 分钟）（目标工作区这些路径有未提交改动，本次不落地，稍后重试: internal/swarm/manager.go、internal/ui/sidebar.go），补丁已保存到 " + dirtyPatchPath,
	}
	report := BuildReport([]Bee{conflictBee, dirtyBee})

	for _, want := range []string{
		patchPath,
		dirtyPatchPath,
		"- 补丁改动文件：internal/swarm/manager.go、internal/ui/sidebar.go",
		"- 补丁冲突或脏路径：internal/ui/sidebar.go",
		"- 补丁冲突或脏路径：internal/swarm/manager.go、internal/ui/sidebar.go",
		"请派一个默认档位的 bee 读取该补丁文件",
		"每个补丁只重试一次",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("补丁已保存时汇报应包含 %q:\n%s", want, report)
		}
	}

	merged := Bee{
		Name:         "Cedar",
		Task:         "task 3",
		Status:       StatusCompleted,
		Merged:       true,
		ChangedFiles: []string{"internal/swarm/swarm.go"},
	}
	mergedReport := BuildReport([]Bee{merged})
	for _, bad := range []string{patchPath, "补丁未合入", "请派一个默认档位的 bee", "补丁已保存到"} {
		if strings.Contains(mergedReport, bad) {
			t.Fatalf("已合入的 bee 汇报不应包含 %q:\n%s", bad, mergedReport)
		}
	}
}
