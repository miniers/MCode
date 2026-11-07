package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 点击头部圆形新建会话按钮进入待创建会话：不立即建会话，输入首个字符后才建。
func TestChatToolbarNewThreadButton(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	var state *chatState
	tt := ui.NewTester(Root(fake, func(s *chatState) { state = s }), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	if err := tt.Click("新建会话 ⌘N"); err != nil {
		t.Fatalf("click new-thread button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.pendingThread != nil && state.selectedThreadID == ""
	})
	if got := fake.CreateThreadCalls(); got != 0 {
		t.Fatalf("点击新建会话不应立即创建会话，got %d", got)
	}
}

// 侧栏收起时点会话列表按钮弹出 Popover；点某项切换选中会话、关闭弹层，侧栏保持收起。
func TestChatToolbarThreadsPopover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
		_, ok := tt.Find("切换到会话 第二个会话")
		return ok
	})
	if err := tt.Click("切换到会话 第二个会话"); err != nil {
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

// 胶囊组按钮分别切换项目文档、终端、浏览器预览与看板状态。
func TestChatToolbarAuxGroupButtons(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubTerminals(t)
	fake := NewFakeClient()
	s, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(s) })
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	// 项目文档：点击按钮打开，关闭直接调用头部动作，避免再次点击命中按钮提示层
	if err := tt.Click("项目文档 (.mcode/context) ⇧⌘K"); err != nil {
		t.Fatalf("click context button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return getContextPanelState(s).IsOpen() })
	s.headerToggleContextPanel()
	waitFor(t, tt, func() bool { return !getContextPanelState(s).IsOpen() })

	// 终端：显示后再点一次隐藏
	if err := tt.Click("终端 ⌘J"); err != nil {
		t.Fatalf("click terminal button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return getTerminalState(s).visible })
	if err := tt.Click("终端 ⌘J"); err != nil {
		t.Fatalf("click terminal button again failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !getTerminalState(s).visible })

	// 浏览器预览：显示后再点一次隐藏
	if err := tt.Click("浏览器预览 ⇧⌘B"); err != nil {
		t.Fatalf("click preview button failed: %v", err)
	}
	waitFor(t, tt, func() bool { return getPreviewState(s).visible })
	if err := tt.Click("浏览器预览 ⇧⌘B"); err != nil {
		t.Fatalf("click preview button again failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !getPreviewState(s).visible })

	// 看板：切到任务工作区视图
	if err := tt.Click("看板"); err != nil {
		t.Fatalf("click kanban button failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		ws := getWorkspaceState(s)
		workspaceMu.Lock()
		defer workspaceMu.Unlock()
		return ws.view == "tasks"
	})
}

// 头部动作方法直接切换终端、项目文档、预览、改动与看板状态。
func TestChatToolbarAuxActions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	s.headerOpenKanban()
	ws := getWorkspaceState(s)
	workspaceMu.Lock()
	view := ws.view
	workspaceMu.Unlock()
	if view != "tasks" {
		t.Fatalf("headerOpenKanban 应切到看板视图，当前为 %q", view)
	}
}

// 项目动作组在标题右侧、辅助面板组左侧：「打开方式」「执行命令」「Git 操作」同属一个胶囊组，
// 有项目工作目录时执行命令按钮显示（无脚本时菜单给出空态与添加入口）。
func TestChatToolbarActionGroupPlacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := newGitFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("feature/login") })
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("在其它应用中打开")
		return ok
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("运行项目脚本")
		return ok
	})
	// 提交（Git 操作组）与打开方式、执行命令同处标题栏动作胶囊组
	if _, ok := tt.Find("提交与推送"); !ok {
		t.Fatal("Git 仓库会话标题栏应显示提交按钮")
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
