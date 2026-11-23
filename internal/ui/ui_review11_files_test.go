package mcodeui

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type uiReview11LoadFailPage struct {
	browserTestPage
	loadErr     error
	loadCalls   atomic.Int32
	reloadCalls atomic.Int32
}

func (p *uiReview11LoadFailPage) LoadURL(target string) error {
	p.loadCalls.Add(1)
	if p.loadErr != nil {
		return p.loadErr
	}
	return p.browserTestPage.LoadURL(target)
}

func (p *uiReview11LoadFailPage) Reload() {
	p.reloadCalls.Add(1)
}

func TestUIReview11LoadURLErrorResetsLoading(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	page := &uiReview11LoadFailPage{loadErr: errors.New("connection refused")}
	tab := getPreviewState(s)
	previewMu.Lock()
	browserPromoteTabLocked(tab)
	previewNextPageID++
	tab.page, tab.pageID = page, previewNextPageID
	tab.url = "https://example.test/fail"
	tab.addr = tab.url
	previewMu.Unlock()

	loadPreviewPage(s, tab)

	previewMu.Lock()
	loading := tab.loading
	errMsg := tab.err
	previewMu.Unlock()
	if loading {
		t.Fatal("LoadURL 返回错误后 loading 未复位为 false")
	}
	if !strings.Contains(errMsg, "加载预览失败") {
		t.Fatalf("期望记录加载失败错误，实际为 %q", errMsg)
	}

	inspectorSelect(s, sidePanePreviewID, false)
	tester := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		renderPreviewPanel(c, s)
	}, 800, 600)
	tester.Frame()

	if _, ok := tester.Find("停止"); ok {
		t.Fatal("LoadURL 失败后刷新按钮仍处于「停止」态")
	}
	if _, ok := tester.Find("刷新"); !ok {
		t.Fatal("LoadURL 失败后刷新按钮未恢复为「刷新」")
	}
	if _, ok := tester.Find("重试"); !ok {
		t.Fatal("预览出错时未渲染「重试」按钮")
	}
	if err := tester.Click("重试"); err != nil {
		t.Fatalf("点击重试失败: %v", err)
	}
	if page.reloadCalls.Load() != 1 {
		t.Fatalf("点击重试后 reload 调用次数 = %d，期望 1", page.reloadCalls.Load())
	}
}

func TestUIReview11ConsoleBadgeWithoutErrors(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	settings := NewSettingsState(settingsTestDir(t), nil, s)
	enabled := true
	settings.cfg.Workbench.BrowserConsoleCapture = &enabled
	setChatSettings(s, settings)

	tab := getPreviewState(s)
	previewMu.Lock()
	browserPromoteTabLocked(tab)
	previewNextPageID++
	tab.page, tab.pageID = &previewTestPage{}, previewNextPageID
	tab.consoleConfigured, tab.pageConsoleCapture = true, true
	tab.url = "https://example.test/warn"
	tab.addr = tab.url
	tab.consoleErrors = []previewConsoleError{
		{Sequence: 1, Kind: "warn", Message: "只有警告"},
	}
	previewMu.Unlock()

	inspectorSelect(s, sidePanePreviewID, false)
	tester := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		renderPreviewPanel(c, s)
	}, 800, 600)
	tester.Frame()

	if _, ok := tester.Find("控制台"); !ok {
		t.Fatal("只有 warn 日志时控制台按钮不存在")
	}
	if tester.HasText("0") {
		t.Fatal("无 error 日志时控制台按钮不应显示 0")
	}

	if err := tester.Click("控制台"); err != nil {
		t.Fatalf("点击控制台按钮失败: %v", err)
	}
	tester.Frame()
	previewMu.Lock()
	open := tab.consoleOpen
	tab.consoleErrors = nil
	previewMu.Unlock()
	if !open {
		t.Fatal("点击控制台按钮后抽屉未打开")
	}

	tester.Frame()
	if _, ok := tester.Find("控制台"); !ok {
		t.Fatal("清空日志且抽屉打开时控制台按钮不应消失")
	}
	if err := tester.Click("控制台"); err != nil {
		t.Fatalf("再次点击控制台按钮收起失败: %v", err)
	}
	previewMu.Lock()
	stillOpen := tab.consoleOpen
	previewMu.Unlock()
	if stillOpen {
		t.Fatal("点击控制台按钮未能收起抽屉")
	}
}

func TestUIReview11WatchTickIdleNoChange(t *testing.T) {
	client := newWatchTestClient(treeTestBase())
	s, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	waitFor(t, tt, func() bool { return client.generationCalls("t1") >= 1 })

	st := getFileManagerState(s)
	w := fileWatchStateForTest(t, st)
	waitFor(t, tt, func() bool {
		w.mu.Lock()
		inFlight := w.inFlight
		hasGen := w.hasGen
		w.mu.Unlock()
		fileManagerMu.Lock()
		gitLoaded := st.tree != nil && st.tree.gitLoaded
		fileManagerMu.Unlock()
		return !inFlight && hasGen && gitLoaded
	})

	// 间隔内重复 tick 返回 false。
	if got := fileTreeWatchTickState(s, st, "t1", w, 0); got {
		t.Fatal("间隔内空闲 tick 应返回 false")
	}

	// 模拟计时器到点触发（工作区未变化），应返回 false。
	w.mu.Lock()
	w.lastCheck = time.Now().Add(-2 * fileTreeWatchInterval)
	seq := w.timerSeq
	w.mu.Unlock()
	if got := fileTreeWatchTickExisting(s, st, "t1", seq); got {
		t.Fatal("工作区未变化时定时器 tick 应返回 false")
	}

	// 等后台检查收尾后再次验证早退路径返回 false。
	waitFor(t, tt, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.inFlight
	})
	if got := fileTreeWatchTickExisting(s, st, "other-thread", seq); got {
		t.Fatal("不匹配的会话早退应返回 false")
	}
	if got := fileTreeWatchTickExisting(s, nil, "t1", seq); got {
		t.Fatal("nil state 早退应返回 false")
	}
}

func TestUIReview11ChangedRowsCached(t *testing.T) {
	st := &fileManagerState{threadID: "t1"}
	fileManagerMu.Lock()
	tree := ensureFileTreeLocked(st)
	fileTreeApplyGitLocked(tree, []GitChangedFile{
		{Path: "a.txt", Status: "M"},
		{Path: "sub/b.go", Status: "A"},
	})
	before := fileTreeChangedRowsBuilds.Load()

	roots1, _, _ := fileTreeChangedRowsLocked(tree)
	if got := fileTreeChangedRowsBuilds.Load(); got != before+1 {
		fileManagerMu.Unlock()
		t.Fatalf("首次构建计数 = %d，期望 %d", got, before+1)
	}
	if len(roots1) != 2 || !tree.changedOutline.Open.Has("sub") {
		fileManagerMu.Unlock()
		t.Fatalf("首次构建结果或默认展开异常：roots=%v openSub=%v", roots1, tree.changedOutline.Open.Has("sub"))
	}

	// 用户手动收起目录后，git 未变化时的重复调用不重建且保持收起。
	tree.changedOutline.Open.Remove("sub")
	for range 5 {
		fileTreeChangedRowsLocked(tree)
	}
	if got := fileTreeChangedRowsBuilds.Load(); got != before+1 {
		fileManagerMu.Unlock()
		t.Fatalf("git 未变化时不应重建，计数 = %d，期望 %d", got, before+1)
	}
	if tree.changedOutline.Open.Has("sub") {
		fileManagerMu.Unlock()
		t.Fatal("缓存命中时不应覆盖用户收起的目录")
	}

	// git 变化时缓存失效并重新构建。
	fileTreeApplyGitLocked(tree, []GitChangedFile{
		{Path: "a.txt", Status: "M"},
		{Path: "sub/b.go", Status: "A"},
		{Path: "sub/c.go", Status: "?"},
	})
	fileTreeChangedRowsLocked(tree)
	after := fileTreeChangedRowsBuilds.Load()
	openSub := tree.changedOutline.Open.Has("sub")
	fileManagerMu.Unlock()
	if after != before+2 {
		t.Fatalf("git 变化后构建计数 = %d，期望 %d", after, before+2)
	}
	if !openSub {
		t.Fatal("git 数据变化后新增/变更父目录应默认展开")
	}
}

func TestUIReview11TreeEnterOpensFile(t *testing.T) {
	s, tt := openFileTreeForTest(t, treeTestBase())
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") && tt.HasText("sub") })

	// 点击目录 sub 让树获得焦点并展开，再按 Enter 应切换收起/展开。
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("click sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	if !treeOpenDirForTest(s, "sub") {
		t.Fatal("点击目录后应处于展开状态")
	}

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if treeOpenDirForTest(s, "sub") {
		t.Fatal("选中目录按 Enter 应收起目录")
	}

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })

	// 收起 sub 后下移选中 a.txt，按 Enter 打开该文件。
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
}

func TestUIReview11RootFileHeaderHidesPath(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	tester := ui.NewTester(func(c *ui.Context) {
		renderFileManagerPanes(c, s, fileManagerPanelSnapshot{
			tabs:   []fileManagerTabSnapshot{{path: "README.md", kind: "text", content: "body", mdPreview: true}},
			active: "README.md",
		}, true)
	}, 480, 160)
	tester.Frame()

	if tester.HasText("README.md") {
		t.Fatalf("未拆分时窗格顶部不应显示文件头或路径条，Texts=%v", tester.Texts())
	}

	testerSub := ui.NewTester(func(c *ui.Context) {
		renderFileManagerContent(c, s, fileManagerPanelSnapshot{
			tabs:   []fileManagerTabSnapshot{{path: "docs/README.md", kind: "text", content: "body", mdPreview: true}},
			active: "docs/README.md",
		}, false, false)
	}, 480, 160)
	testerSub.Frame()
	if testerSub.HasText("docs/README.md") {
		t.Fatalf("未拆分时不应显示路径副标题，Texts=%v", testerSub.Texts())
	}

	testerSplit := ui.NewTester(func(c *ui.Context) {
		renderFileManagerContent(c, s, fileManagerPanelSnapshot{
			tabs: []fileManagerTabSnapshot{
				{path: "README.md", kind: "text", content: "root", mdPreview: true},
				{path: "docs/README.md", kind: "text", content: "sub", mdPreview: true},
			},
			active:      "docs/README.md",
			splitAnchor: "README.md",
			splitSide:   "docs/README.md",
		}, false, false)
	}, 680, 200)
	testerSplit.Frame()
	if !testerSplit.HasText("docs/README.md") {
		t.Fatalf("拆分双栏时窗格路径条应显示相对路径，Texts=%v", testerSplit.Texts())
	}
}

func TestUIReview11TruncatedRowTooltip(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	longName := "very_long_component_filename_that_must_truncate_in_narrow_sidebar.tsx"
	entry := FileEntry{Name: longName, Path: "src/components/" + longName}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(120).Children(func() {
			renderFileManagerRow(c, s, fileManagerRowProps{
				key:   "trunc-row",
				icon:  IconFile(),
				name:  entry.Name,
				entry: entry,
			})
		})
	}, 200, 80)
	// 第一帧完成布局测量，第二帧按 LayoutBounds 挂上 Tooltip。
	tester.Frame()
	tester.Frame()

	info, ok := tester.Access(longName)
	if !ok {
		t.Fatalf("未找到文件名无障碍节点 %q: %v", longName, tester.AccessAll())
	}
	if info.Description != entry.Path {
		t.Fatalf("截断行 Tooltip/Description = %q，期望完整路径 %q", info.Description, entry.Path)
	}
}
