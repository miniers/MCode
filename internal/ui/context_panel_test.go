package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeProjectPath 是 NewFakeClient 的默认项目路径。buildRoot 渲染时会把面板同步到当前项目，
// 用例里手设 s.projects 必须用同一条路径，否则 setProject 会把全局层重置回项目层。
const fakeProjectPath = "/path/to/test"

// contextTestClient 在 FakeClient 之上替换读写/列目录/监听，按需让调用阻塞。
type contextTestClient struct {
	*FakeClient
	read   func(string, string) (ContextDoc, error)
	write  func(string, string, string, string) (ContextDoc, error)
	list   func(string) ([]ContextDoc, error)
	watch  func(string, func()) (func(), error)
	create func(string, string) (ContextDoc, error)
}

func (f *contextTestClient) ReadContextDoc(path, name string) (ContextDoc, error) {
	if f.read != nil {
		return f.read(path, name)
	}
	return f.FakeClient.ReadContextDoc(path, name)
}

func (f *contextTestClient) WriteContextDoc(path, name, content, hash string) (ContextDoc, error) {
	if f.write != nil {
		return f.write(path, name, content, hash)
	}
	return f.FakeClient.WriteContextDoc(path, name, content, hash)
}

func (f *contextTestClient) ContextDocs(path string) ([]ContextDoc, error) {
	if f.list != nil {
		return f.list(path)
	}
	return f.FakeClient.ContextDocs(path)
}

func (f *contextTestClient) WatchContextDocs(path string, change func()) (func(), error) {
	if f.watch != nil {
		return f.watch(path, change)
	}
	return f.FakeClient.WatchContextDocs(path, change)
}

func (f *contextTestClient) CreateContextDoc(path, name string) (ContextDoc, error) {
	if f.create != nil {
		return f.create(path, name)
	}
	return f.FakeClient.CreateContextDoc(path, name)
}

func awaitContext(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("项目文档操作未完成")
	}
}

// loadContextDocsSync 的清单始终包含四篇标准文档，缺失时 Exists=false。
func TestContextPanel_ManifestAlwaysListsStandard(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("decisions.md", "# Decisions")
	fake.SetContextDoc("api-spec.md", "# API")

	s := newChatState(fake)
	t.Cleanup(s.close)
	if err := loadContextDocsSync(s, "/project"); err != nil {
		t.Fatal(err)
	}
	cp := getContextPanelState(s)
	standard := cp.manifest.Standard()
	if len(standard) != 4 {
		t.Fatalf("expected 4 standard documents, got %d", len(standard))
	}
	wantExist := map[string]bool{"instructions": false, "overview": false, "decisions": true, "notes": false}
	for _, doc := range standard {
		if doc.Exists != wantExist[doc.Name] {
			t.Fatalf("standard %q Exists=%v", doc.Name, doc.Exists)
		}
	}
	customs := cp.manifest.Custom()
	if len(customs) != 1 || customs[0].RefID() != "docs/api-spec.md" {
		t.Fatalf("unexpected custom docs: %+v", customs)
	}

	// 标准文档缺失时打开模板，首次保存按空哈希落盘。
	if err := selectContextDocSync(s, "instructions"); err != nil {
		t.Fatal(err)
	}
	if cp.selected != "instructions.md" || cp.baseHash != "" || (!strings.HasPrefix(cp.draft, "# Project Instructions") && !strings.HasPrefix(cp.draft, "# 项目规则")) {
		t.Fatalf("missing standard doc should open its template: %+v", cp)
	}
	cp.draft = "# Rules"
	cp.dirty = true
	if err := saveContextDocSync(s); err != nil {
		t.Fatalf("save template failed: %v", err)
	}
	if doc, err := fake.ReadContextDoc("/project", "instructions.md"); err != nil || doc.Content != "# Rules" {
		t.Fatalf("standard doc not written: %+v, %v", doc, err)
	}
	if cp.showSaved != true || cp.conflict || cp.dirty {
		t.Fatalf("saved badge/dirty state wrong: %+v", cp)
	}
}

func TestContextPanel_SelectAndCASConflict(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("notes.md", "# Original")
	s := newChatState(fake)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	if err := selectContextDocSync(s, "notes.md"); err != nil {
		t.Fatal(err)
	}
	cp := getContextPanelState(s)
	cp.draft, cp.dirty = "# Local", true

	fake.SetContextDoc("notes.md", "# External")
	if err := saveContextDocSync(s); !errors.Is(err, ErrContextConflict) || !cp.conflict {
		t.Fatalf("expected CAS conflict, got %v (conflict=%v)", err, cp.conflict)
	}
	if cp.conflictMsg != "文件已被外部修改，存在并发冲突" {
		t.Fatalf("unexpected conflict message: %q", cp.conflictMsg)
	}

	if err := reloadContextDocSync(s); err != nil {
		t.Fatal(err)
	}
	if cp.conflict || cp.draft != "# External" {
		t.Fatalf("reload should adopt disk content: %+v", cp)
	}

	cp.draft, cp.dirty = "# Overwrite", true
	fake.SetContextDoc("notes.md", "# Concurrent")
	_ = saveContextDocSync(s)
	if err := overwriteContextDocSync(s); err != nil {
		t.Fatal(err)
	}
	doc, _ := fake.ReadContextDoc("/project", "notes.md")
	if doc.Content != "# Overwrite" || cp.conflict {
		t.Fatalf("overwrite failed: %+v conflict=%v", doc, cp.conflict)
	}
}

// 外部修改时不静默替换正在编辑的内容，而是提示冲突。
func TestContextPanel_WatcherChange(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("instructions.md", "# Initial")
	s := newChatState(fake)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "instructions.md")
	cp := getContextPanelState(s)

	cp.draft, cp.dirty = "# Local draft", true
	fake.SetContextDoc("instructions.md", "# Disk change")
	s.handleContextDocChange()
	if !cp.conflict || cp.draft != "# Local draft" {
		t.Fatalf("external change must keep the draft and flag a conflict: %+v", cp)
	}

	cp.conflict, cp.dirty = false, false
	cp.draft, cp.savedContent = "# Synced", "# Synced"
	fake.SetContextDoc("instructions.md", "# Silent")
	s.handleContextDocChange()
	if !cp.conflict || cp.draft != "# Synced" {
		t.Fatalf("external change replaced visible content: %+v", cp)
	}
}

func TestContextPanel_StaleReadAndProjectIsolation(t *testing.T) {
	fake := NewFakeClient()
	// 自定义文档不能与标准文档同名：docs/notes.md 会被 ParseRef 当作根部 notes.md。
	fake.SetContextDoc("docs/scratch.md", "base")
	fake.SetContextDoc("docs/roadmap.md", "roadmap")
	fake.contextDocProjectPath("/project", "docs/scratch.md")
	fake.contextDocProjectPath("/project", "docs/roadmap.md")
	s := newChatState(fake)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "docs/scratch.md")
	cp := getContextPanelState(s)

	// 迟到的读取不能覆盖新选择。
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	client := &contextTestClient{FakeClient: fake}
	client.read = func(path, name string) (ContextDoc, error) {
		if name == "docs/scratch.md" {
			once.Do(func() { close(started) })
			<-release
		}
		return fake.ReadContextDoc(path, name)
	}
	s.client = client
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = selectContextDocSync(s, "docs/scratch.md")
	}()
	awaitContext(t, started)
	if err := selectContextDocSync(s, "docs/roadmap.md"); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitContext(t, done)
	if cp.selected != "docs/roadmap.md" || cp.draft != "roadmap" {
		t.Fatalf("过期读取覆盖了新选择：%+v", cp)
	}

	// 切换文档保留草稿：离开时暂存，回到该文档时恢复。
	// 切换项目保留旧草稿、不泄漏到新项目；文档绑在 /project 上，/other-project 看不到。
	cp.draft, cp.dirty = "unsaved", true
	_ = selectContextDocSync(s, "docs/scratch.md")
	_ = selectContextDocSync(s, "docs/roadmap.md")
	if cp.draft != "unsaved" || !cp.dirty {
		t.Fatal("切换文档丢失草稿")
	}
	_ = loadContextDocsSync(s, "/other-project")
	if cp.selected != "" || cp.baseHash != "" || cp.draft != "" {
		t.Fatal("切换项目未清除旧文档身份")
	}
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "docs/roadmap.md")
	if cp.draft != "unsaved" {
		t.Fatal("返回项目未恢复草稿")
	}
}

func TestContextPanel_DeletedDocumentConflict(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("notes.md", "base")
	s := newChatState(fake)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "notes.md")
	cp := getContextPanelState(s)
	fake.mu.Lock()
	delete(fake.contextDocs, "notes.md")
	fake.mu.Unlock()
	s.handleContextDocChange()
	if !cp.conflict || cp.draft != "base" {
		t.Fatal("外部删除未保留内容并提示冲突")
	}
	if err := saveContextDocSync(s); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("普通保存未阻止覆盖：%v", err)
	}
}

func TestContextPanel_LateWatcherStoppedOnClose(t *testing.T) {
	// 监听注册会卡在 release 上，关闭路径要等到超时才返回：把等待时间调小，别让用例白等 2 秒。
	restoreWait := contextPanelCloseWait
	contextPanelCloseWait = 20 * time.Millisecond
	t.Cleanup(func() { contextPanelCloseWait = restoreWait })
	fake := NewFakeClient()
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := &contextTestClient{FakeClient: fake}
	client.watch = func(string, func()) (func(), error) {
		close(started)
		<-release
		return func() { close(stopped) }, nil
	}
	s := newChatState(client)
	s.projects = []Project{{Path: "/project"}}
	s.toggleContextPanel()
	if !getContextPanelState(s).IsOpen() {
		t.Fatal("toggle should open the panel synchronously")
	}
	s.applyPendingContextToggle()
	awaitContext(t, started)
	s.close()
	close(release)
	awaitContext(t, stopped)
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	if _, ok := contextPanelStates[s]; ok {
		t.Fatal("关闭后仍保留项目文档状态")
	}
}

func TestContextPanel_SavePreservesNewEdits(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("notes.md", "base")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := &contextTestClient{FakeClient: fake}
	client.write = func(path, name, content, hash string) (ContextDoc, error) {
		close(started)
		<-release
		return fake.WriteContextDoc(path, name, content, hash)
	}
	s := newChatState(client)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "notes.md")
	cp := getContextPanelState(s)
	cp.draft = "first edit"
	go func() {
		defer close(done)
		_ = saveContextDocSync(s)
	}()
	awaitContext(t, started)
	s.editContextDraft("new edit during save")
	close(release)
	awaitContext(t, done)
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	if !cp.dirty || cp.savedContent != "first edit" || cp.draft != "new edit during save" {
		t.Fatalf("保存吞掉了新草稿：%+v", cp)
	}
}

func TestContextPanel_AutoSaveLatestDraft(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("notes.md", "base")
	saved := make(chan struct{})
	var once sync.Once
	client := &contextTestClient{FakeClient: fake}
	client.write = func(path, name, content, hash string) (ContextDoc, error) {
		doc, err := fake.WriteContextDoc(path, name, content, hash)
		once.Do(func() { close(saved) })
		return doc, err
	}
	s := newChatState(client)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "notes.md")
	cp := getContextPanelState(s)
	cp.open = true
	s.editContextDraft("first")
	s.editContextDraft("latest")
	awaitContext(t, saved)
	doc, err := fake.ReadContextDoc("/project", "notes.md")
	if err != nil || doc.Content != "latest" || fake.ContextWriteCalls() != 1 {
		t.Fatalf("防抖保存结果：%+v, %v", doc, err)
	}
}

func TestContextPanel_CreateCustomDoc(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	if err := createContextDocSync(s, "design"); err != nil {
		t.Fatal(err)
	}
	cp := getContextPanelState(s)
	if cp.selected != "docs/design.md" || cp.baseHash == "" || len(cp.manifest.Custom()) != 1 {
		t.Fatalf("创建未加载文档：%+v", cp)
	}
	fake.SetContextDoc("docs/design.md", "do not overwrite")
	if err := createContextDocSync(s, "design.md"); err == nil {
		t.Fatal("重复创建未报错")
	}
	doc, _ := fake.ReadContextDoc("/project", "docs/design.md")
	if doc.Content != "do not overwrite" {
		t.Fatal("重复创建覆盖已有内容")
	}
	if err := createContextDocSync(s, "../escape"); err == nil {
		t.Fatal("非法名称应被拒绝")
	}
}

// 快捷键 ⇧⌘K 打开居中弹窗（不再是右侧分栏），再按一次关闭。
func TestContextPanel_UIIntegration(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("instructions.md", "# Hello Context Panel")

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText(".mcode/context/ • CAS 并发保护") {
		t.Fatal("context panel should be initially closed")
	}

	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	waitFor(t, tt, func() bool {
		return tt.HasText("项目文档") && tt.HasText(".mcode/context/ • CAS 并发保护") &&
			tt.HasText("Instructions") && tt.HasText("Decisions") && tt.HasText("在 Finder 中打开")
	})

	if tt.HasText("收件箱") {
		t.Fatal("收件箱不应出现")
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	waitFor(t, tt, func() bool { return !tt.HasText(".mcode/context/ • CAS 并发保护") })
}

// 文档视图：编辑框、详情行、冲突横幅（从磁盘重新加载 / 覆盖磁盘）。
func TestContextPanel_ConflictBannerAndEditor(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("notes.md", "# Initial")
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	cp := getContextPanelState(s)
	tt := ui.NewTester(view, 1400, 900)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	waitFor(t, tt, func() bool { return tt.HasText("Notes") })
	if err := tt.Click("Notes"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		contextPanelMu.Lock()
		loaded := cp.selected == "notes.md" && !cp.loading
		contextPanelMu.Unlock()
		if !loaded {
			return false
		}
		_, ok := tt.Find("项目文档内容")
		return ok
	})
	if !tt.HasText("notes.md") || !tt.HasText("工作草稿、设计笔记与零散想法") {
		t.Fatal("doc details row should show the file name and subtitle")
	}

	if err := tt.Click("项目文档内容"); err != nil {
		t.Fatal(err)
	}
	tt.Type("local edit")
	waitFor(t, tt, func() bool {
		contextPanelMu.Lock()
		defer contextPanelMu.Unlock()
		return strings.Contains(cp.draft, "local edit") && cp.dirty
	})
	fake.SetContextDoc("notes.md", "# External")
	waitFor(t, tt, func() bool {
		return tt.HasText("文件已被外部修改，存在并发冲突") &&
			tt.HasText("从磁盘重新加载") && tt.HasText("覆盖磁盘")
	})
	if err := tt.Click("从磁盘重新加载"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		contextPanelMu.Lock()
		reloaded := cp.draft == "# External" && !cp.conflict
		contextPanelMu.Unlock()
		return reloaded && !tt.HasText("从磁盘重新加载")
	})
}

// lockedContextPanelCopy 在 contextPanelMu 保护下复制面板状态，
// 供测试在后台 goroutine 运行期间安全断言。
func lockedContextPanelCopy(cp *contextPanelState) contextPanelState {
	contextPanelMu.Lock()
	defer contextPanelMu.Unlock()
	return *cp
}

// 有未保存修改时切换文档或关闭之前，先确认「保存 / 放弃 / 取消」。
func TestContextPanel_UnsavedLeaveConfirm(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("notes.md", "# Notes")
	s := newChatState(fake)
	t.Cleanup(s.close)
	_ = loadContextDocsSync(s, "/project")
	_ = selectContextDocSync(s, "notes.md")
	cp := getContextPanelState(s)
	contextPanelMu.Lock()
	cp.open = true
	cp.draft, cp.dirty = "# Unsaved", true
	contextPanelMu.Unlock()

	s.switchContextSection("overview.md")
	if st := lockedContextPanelCopy(cp); !st.leaveOpen || st.section != "notes.md" || st.leaveTo != "overview.md" {
		t.Fatalf("switching with unsaved edits should ask first: %+v", st)
	}
	s.cancelContextLeave()
	if st := lockedContextPanelCopy(cp); st.leaveOpen || !st.dirty || st.draft != "# Unsaved" {
		t.Fatalf("cancel should keep the draft: %+v", st)
	}

	s.switchContextSection("overview.md")
	s.resolveContextLeave(false)
	// 切换文档是异步读取的，等读取完成后 selected、section、draft 才一致。
	waitForAutoDormant(t, func() bool {
		contextPanelMu.Lock()
		defer contextPanelMu.Unlock()
		return cp.selected == "overview.md" && !cp.dirty
	})
	st := lockedContextPanelCopy(cp)
	section, draft := st.section, st.draft
	if section != "overview.md" || (!strings.HasPrefix(draft, "# Architecture") && !strings.HasPrefix(draft, "# 项目概览")) {
		t.Fatalf("discard should switch without saving: section=%q draft=%q %+v", section, draft, st)
	}

	// 关闭时同样确认；关闭要等确认后才发生，选择保存后先落盘再关闭。
	contextPanelMu.Lock()
	cp.draft, cp.dirty = "# Save me", true
	contextPanelMu.Unlock()
	s.toggleContextPanel()
	if st := lockedContextPanelCopy(cp); !st.leaveOpen || !st.leaveClos || !st.open {
		t.Fatalf("closing with unsaved edits should ask first: %+v", st)
	}
	s.resolveContextLeave(true)
	waitForAutoDormant(t, func() bool {
		contextPanelMu.Lock()
		defer contextPanelMu.Unlock()
		return !cp.open
	})
	if doc, _ := fake.ReadContextDoc("/project", "overview.md"); doc.Content != "# Save me" {
		t.Fatalf("save should write the current doc before closing: %+v", doc)
	}

	// 打开时再次 toggle 且无未保存修改：立即同步关闭，不再等根层渲染。
	contextPanelMu.Lock()
	cp.dirty = false
	contextPanelMu.Unlock()
	s.toggleContextPanel()
	if st := lockedContextPanelCopy(cp); !st.open {
		t.Fatal("toggle should reopen the panel synchronously")
	}
	s.toggleContextPanel()
	if st := lockedContextPanelCopy(cp); st.open || st.pendingToggle {
		t.Fatalf("clean toggle should close synchronously: %+v", st)
	}
}

// 同一项目内多次开关不会重载文档，也不会丢掉已打开的项目。
func TestContextPanel_ToggleKeepsProject(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}

	s.toggleContextPanel()
	cp := getContextPanelState(s)
	if !cp.open {
		t.Fatal("toggle should open the panel synchronously")
	}
	s.applyPendingContextToggle()
	waitForAutoDormant(t, func() bool {
		contextPanelMu.Lock()
		defer contextPanelMu.Unlock()
		return cp.open && cp.projectPath == "/project" && !cp.loading
	})
	s.toggleContextPanel()
	if cp.open {
		t.Fatal("second toggle should close the panel")
	}
	contextPanelMu.Lock()
	stop := cp.stopWatch
	contextPanelMu.Unlock()
	if stop != nil {
		t.Fatal("closing should unregister the watcher")
	}
}

func TestContextPanel_GlobalLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fake := NewFakeClient()
	// 全局层只有本地记忆后端会读，层切换与全局读写都按本地后端模拟。
	fake.memoryBackend = "local"
	fake.SetContextDoc("notes.md", "project notes")
	s := newChatState(fake)
	closed := false
	t.Cleanup(func() {
		if !closed {
			s.close()
		}
	})
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	cp := getContextPanelState(s)
	cp.open = true
	if err := loadContextDocsSync(s, "/project"); err != nil {
		t.Fatal(err)
	}
	s.switchContextLayer(true)
	if err := loadContextDocsSync(s, "/project"); err != nil {
		t.Fatal(err)
	}
	if cp.activePath() != home || !cp.global || len(cp.manifest.Standard()) != 4 {
		t.Fatal("全局清单或路径错误")
	}
	if err := selectContextDocSync(s, "notes.md"); err != nil {
		t.Fatal(err)
	}
	cp.draft = "global notes"
	if err := saveContextDocSync(s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".mcode", "context", "notes.md"))
	if err != nil || string(data) != "global notes" {
		t.Fatalf("全局保存失败: %q %v", data, err)
	}
	s.switchContextLayer(false)
	if err := loadContextDocsSync(s, "/project"); err != nil {
		t.Fatal(err)
	}
	if err := selectContextDocSync(s, "notes.md"); err != nil {
		t.Fatal(err)
	}
	if cp.global || cp.draft != "project notes" {
		t.Fatal("切回项目失败")
	}
	s.switchContextLayer(true)
	contextPanelMu.Lock()
	cp.setProject("/other-project")
	global := cp.global
	contextPanelMu.Unlock()
	if global {
		t.Fatal("切换项目应回到项目层")
	}
	s.close()
	closed = true
	contextDir := filepath.Join(home, ".mcode", "context")
	if err := os.RemoveAll(contextDir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(contextDir); !os.IsNotExist(err) {
		t.Fatalf("关闭后全局目录被重新创建或无法检查: %v", err)
	}
}

// hasExactContextText 判断当前帧里是否有整段等于 want 的文本（HasText 是子串匹配，不够精确）。
func hasExactContextText(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if text == want {
			return true
		}
	}
	return false
}

// 层切换只给本地记忆后端：nmem 后端下面板固定在项目层，不渲染「项目 / 全局」分段控件。
func TestContextPanel_LayerSwitchOnlyForLocalBackend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		want    bool
	}{
		{name: "local", backend: "local", want: true},
		{name: "nmem", backend: "nmem", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fake := NewFakeClient()
			fake.memoryBackend = tc.backend
			s, view := buildRoot(fake)
			t.Cleanup(s.close)
			cp := getContextPanelState(s)
			if cp == nil {
				t.Fatal("未能创建项目文档面板状态")
			}
			contextPanelMu.Lock()
			cp.open = true
			contextPanelMu.Unlock()

			tt := ui.NewTester(view, 1000, 700)
			waitFor(t, tt, func() bool { return hasExactContextText(tt, "项目文档") })
			// 分段控件的选项文本为「项目」「全局」，nmem 下整块不渲染。
			if got := hasExactContextText(tt, "全局"); got != tc.want {
				t.Fatalf("%s 后端层切换渲染状态错误：want %v，got %v；texts=%v", tc.backend, tc.want, got, tt.Texts())
			}
		})
	}
}

// 后台任务卡在关闭路径上时，close 不能无限等：超过 contextPanelCloseWait 后按时返回。
func TestContextPanel_CloseTimesOutOnStuckWork(t *testing.T) {
	restoreWait := contextPanelCloseWait
	contextPanelCloseWait = 20 * time.Millisecond
	t.Cleanup(func() { contextPanelCloseWait = restoreWait })

	s := newChatState(NewFakeClient())
	cp := getContextPanelState(s)
	if cp == nil {
		t.Fatal("未能创建项目文档面板状态")
	}
	cp.work.Add(1)
	release := make(chan struct{})
	go func() {
		<-release
		cp.work.Done()
	}()

	start := time.Now()
	s.close()
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("关闭在后台任务阻塞时没有按时返回：%v", elapsed)
	}

	close(release)
	cp.work.Wait()
}

// 全局层的头部标题与副标题改为全局记忆文案。
func TestContextPanel_GlobalLayerHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.memoryBackend = "local"
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	// 渲染时会把面板同步到当前项目：路径与 fake 的当前项目不一致会被 setProject 重置全局层，
	// 所以这里统一用 fake 的默认项目路径（见 fakeProjectPath）。
	s.projects = []Project{{ID: "p1", Path: fakeProjectPath}}
	cp := getContextPanelState(s)
	contextPanelMu.Lock()
	cp.open = true
	contextPanelMu.Unlock()
	if err := loadContextDocsSync(s, fakeProjectPath); err != nil {
		t.Fatal(err)
	}
	s.switchContextLayer(true)
	if err := loadContextDocsSync(s, fakeProjectPath); err != nil {
		t.Fatal(err)
	}

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return hasExactContextText(tt, "全局记忆") &&
			hasExactContextText(tt, "全局记忆与规则，位于 ~/.mcode/context")
	})
	if hasExactContextText(tt, "项目文档") {
		t.Fatalf("全局层不应显示项目文档标题：%v", tt.Texts())
	}
}

// 取不到用户主目录时，全局层要把原因写进错误条，而不是留一片空白清单。
func TestContextPanel_GlobalPathUnavailable(t *testing.T) {
	t.Setenv("HOME", "")
	if home, err := os.UserHomeDir(); err == nil {
		t.Skipf("当前平台 os.UserHomeDir 不依赖 HOME（返回 %q），跳过全局路径错误断言", home)
	}
	fake := NewFakeClient()
	fake.memoryBackend = "local"
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	// 先按 fake 的当前项目路径绑定，切层后路径不再变化，setProject 才不会把全局层重置回项目层。
	s.projects = []Project{{ID: "p1", Path: fakeProjectPath}}
	cp := getContextPanelState(s)
	contextPanelMu.Lock()
	cp.open = true
	contextPanelMu.Unlock()
	if err := loadContextDocsSync(s, fakeProjectPath); err != nil {
		t.Fatal(err)
	}
	s.switchContextLayer(true)
	if err := loadContextDocsSync(s, fakeProjectPath); err != nil {
		t.Fatal(err)
	}

	contextPanelMu.Lock()
	errMsg := cp.err
	contextPanelMu.Unlock()
	if errMsg != contextGlobalPathErr {
		t.Fatalf("全局文档不可用时未记录原因：%q", errMsg)
	}

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText(contextGlobalPathErr) })
}
