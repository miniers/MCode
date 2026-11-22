package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// 侧栏收起时点击头部新建会话按钮后立即创建并选中临时会话；侧栏展开时该按钮隐藏。
func TestChatToolbarNewThreadButton(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	var state *chatState
	tt := ui.NewTester(Root(fake, func(s *chatState) { state = s }), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	newLabel := "新建会话 " + shortcutDisplay("⌘N")
	if _, ok := tt.Find(newLabel); ok {
		t.Fatal("侧栏展开时不应显示头部新建会话按钮")
	}

	state.toggleSidebar()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(newLabel)
		return ok
	})

	if err := tt.Click(newLabel); err != nil {
		t.Fatalf("click new-thread button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		th, ok := state.threadSnapshotLocked(state.selectedThreadID)
		return ok && th.IsProvisional
	})
	if got := fake.CreateThreadCalls(); got != 1 {
		t.Fatalf("点击新建会话应立即创建临时会话，got %d", got)
	}
}

// 侧栏收起时点会话列表按钮弹出 Popover；点某项切换选中会话、关闭弹层，侧栏保持收起。
func TestChatToolbarThreadsPopover(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"], Thread{ID: "t2", ProjectID: "p1", Title: "第二个会话"})
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	// 侧栏展开时不渲染会话列表按钮
	if _, ok := tt.Find("会话列表"); ok {
		t.Fatal("侧栏展开时不应渲染会话列表按钮")
	}

	s.toggleSidebar()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("会话列表")
		return ok
	})
	if err := tt.Click("会话列表"); err != nil {
		t.Fatalf("click threads button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("第二个会话")
		return ok
	})
	if err := tt.Click("第二个会话"); err != nil {
		t.Fatalf("click thread item failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == "t2"
	})

	if ts := getChatToolbarState(s); ts.threadsPopoverOpen {
		t.Fatal("选择会话后会话列表 Popover 应关闭")
	}
	s.mu.Lock()
	collapsed := s.sidebarCollapsed
	s.mu.Unlock()
	if !collapsed {
		t.Fatal("从 Popover 选择会话不应展开侧栏")
	}
}

// 标题栏会话下拉支持搜索过滤、回车切换并关闭，以及 Esc 关闭且不切换。
func TestChatThreadsPopoverSearch(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.threads["p1"] = append(fake.threads["p1"],
		Thread{ID: "t2", ProjectID: "p1", Title: "部署脚本"},
		Thread{ID: "t3", ProjectID: "p1", Title: "代码评审"},
	)
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	s.toggleSidebar()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("会话列表")
		return ok
	})

	if err := tt.Click("会话列表"); err != nil {
		t.Fatalf("click threads button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.Focused("搜索会话") })

	tt.Type("部署")
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleButton, "部署脚本"); !ok {
		t.Fatal("输入「部署」后应显示匹配会话「部署脚本」")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "代码评审"); ok {
		t.Fatal("输入「部署」后不应显示「代码评审」")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "测试会话1"); ok {
		t.Fatal("输入「部署」后不应显示「测试会话1」")
	}

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		selected := s.selectedThreadID
		s.mu.Unlock()
		return selected == "t2" && !getChatToolbarState(s).threadsPopoverOpen
	})

	if err := tt.Click("会话列表"); err != nil {
		t.Fatalf("reopen threads popover failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.Focused("搜索会话") })
	if _, ok := tt.AccessByRole(ui.RoleButton, "代码评审"); !ok {
		t.Fatal("重新打开下拉应重置搜索框并显示全部会话")
	}

	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool { return !getChatToolbarState(s).threadsPopoverOpen })
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	if selected != "t2" {
		t.Fatalf("按 Esc 关闭后不应切换会话，期望 t2，实际 %q", selected)
	}
}

// 胶囊组按钮分别切换项目文档、终端、浏览器预览与看板状态。
func TestChatToolbarAuxGroupButtons(t *testing.T) {
	testenv.TempHome(t)
	stubTerminals(t)
	fake := NewFakeClient()
	s, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(s) })
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	// 项目文档：点击按钮打开，关闭直接调用头部动作，避免再次点击命中按钮提示层
	if err := tt.Click("更多工具"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	if err := tt.Click("项目文档"); err != nil {
		t.Fatalf("click context button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return getContextPanelState(s).IsOpen() })
	s.headerToggleContextPanel()
	waitFor(t, tt, func() bool { return !getContextPanelState(s).IsOpen() })

	// 终端：显示后再点一次隐藏
	if err := tt.Click("终端"); err != nil {
		t.Fatalf("click terminal button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return getTerminalState(s).visible })
	if err := tt.Click("终端"); err != nil {
		t.Fatalf("click terminal button again failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !getTerminalState(s).visible })

	// 右栏：打开浏览器标签，再关闭右栏。
	inspectorSelect(s, sidePanePreviewID, false)
	waitFor(t, tt, func() bool { return inspectorPaneOpen(s, sidePanePreviewID) })
	if err := tt.Click("右栏"); err != nil {
		t.Fatalf("click inspector button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !inspectorPaneOpen(s, sidePanePreviewID) })

	// 更多菜单不再保留「任务」入口。
	countTasks := func() int {
		n := 0
		for _, txt := range tt.Texts() {
			if txt == "任务" {
				n++
			}
		}
		return n
	}
	beforeTasks := countTasks()
	if err := tt.Click("更多工具"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	waitFor(t, tt, func() bool { return tt.HasText("项目文档") })
	if countTasks() != beforeTasks {
		t.Fatalf("更多菜单不应再保留任务入口, texts=%q", tt.Texts())
	}
}

// 头部动作方法直接切换终端、项目文档、预览与改动状态。
func TestChatToolbarAuxActions(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)

	s.headerToggleTerminal()
	if !getTerminalState(s).visible {
		t.Fatal("headerToggleTerminal 应显示终端")
	}
	s.headerToggleTerminal()
	if getTerminalState(s).visible {
		t.Fatal("headerToggleTerminal 应再次隐藏终端")
	}

	s.headerToggleContextPanel()
	if !getContextPanelState(s).IsOpen() {
		t.Fatal("headerToggleContextPanel 应打开项目文档面板")
	}

	s.headerTogglePreview()
	if !getPreviewState(s).visible {
		t.Fatal("headerTogglePreview 应显示浏览器预览")
	}

	s.headerToggleDiff()
	if !getDiffPanelState(s).IsOpen() {
		t.Fatal("headerToggleDiff 应打开改动面板")
	}
}

// 项目动作组在标题右侧、辅助面板组左侧：有项目工作目录时「打开方式」与「执行命令」显示，不再显示「提交与推送」。
func TestChatToolbarActionGroupPlacement(t *testing.T) {
	testenv.TempHome(t)
	fake := newGitFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("在其它应用中打开")
		return ok
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("运行项目脚本")
		return ok
	})
	if _, ok := tt.Find("提交与推送"); ok {
		t.Fatal("标题栏不再显示提交按钮")
	}
	if !s.headerScriptsButtonVisible("t1") {
		t.Fatal("有项目工作目录时执行命令按钮应显示")
	}
}

func TestChatToolbarShortcuts(t *testing.T) {
	if id, _, ok := matchShortcut(ui.Cmd, ui.KeyJ); !ok || id != "toggle-terminal" {
		t.Fatalf("⌘J 应解析到 toggle-terminal，得到 id=%q ok=%v", id, ok)
	}
	if id, _, ok := matchShortcut(ui.Cmd, ui.KeyN); !ok || id != "new-thread" {
		t.Fatalf("⌘N 应解析到 new-thread，得到 id=%q ok=%v", id, ok)
	}

	registered := make(map[registeredShortcut]bool, len(registeredShortcutsList))
	for _, sc := range registeredShortcutsList {
		registered[sc] = true
	}
	if !registered[registeredShortcut{ui.Cmd, ui.KeyJ}] {
		t.Fatal("registeredShortcutsList 缺少 ⌘J 绑定")
	}
	if !registered[registeredShortcut{ui.Cmd, ui.KeyN}] {
		t.Fatal("registeredShortcutsList 缺少 ⌘N 绑定")
	}
}
