package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/testenv"
)

func newPopoverShortcutFakeClient() *FakeClient {
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"},
				{ID: "claude-3-5-haiku", Name: "Claude 3.5 Haiku"},
			},
		},
	})
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "first", Provider: "claude", Model: "claude-3-7-sonnet", UpdatedAt: now},
				{ID: "t2", ProjectID: "p1", Title: "second", Provider: "claude", Model: "claude-3-7-sonnet", UpdatedAt: now.Add(-time.Minute)},
			},
		},
	)
	return fake
}

func workspaceViewOf(s *chatState) string {
	ws := getWorkspaceState(s)
	if ws == nil {
		return ""
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	return ws.view
}

func TestPopoverYieldsGlobalShortcuts(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	resetComposerModelPickerForTest(t)

	t.Run("anchoredPopover打开时让路关闭后恢复", func(t *testing.T) {
		fake := newPopoverShortcutFakeClient()
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 700)
		waitFor(t, tt, func() bool {
			state.mu.Lock()
			defer state.mu.Unlock()
			return len(state.threads) == 2 && state.selectedThreadID == "t1"
		})

		// 先在聊天视图打开经过 anchoredPopover 的侧栏过滤浮层，验证全局快捷键全部让路。
		state.sidebarFilterOpen = true
		tt.Frame()
		if !tt.HasText("最近 24 小时活跃") {
			t.Fatalf("过滤浮层未渲染，texts: %v", tt.Texts())
		}

		tt.Key(ui.Cmd, ui.KeyB)
		tt.Frame()
		if state.sidebarCollapsed {
			t.Fatal("浮层打开时 ⌘B 不应折叠侧栏")
		}

		tt.Key(ui.Cmd, ui.KeyK)
		tt.Frame()
		if getPaletteState(state).open {
			t.Fatal("浮层打开时 ⌘K 不应打开命令面板")
		}

		tt.Key(ui.Cmd, ui.Key2)
		tt.Frame()
		state.mu.Lock()
		selected := state.selectedThreadID
		state.mu.Unlock()
		if selected != "t1" {
			t.Fatalf("浮层打开时 ⌘2 不应切换会话，实际 %q", selected)
		}

		// 按 Esc 关闭浮层后，全局快捷键恢复生效。
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if state.sidebarFilterOpen || tt.HasText("最近 24 小时活跃") {
			t.Fatal("按 Esc 后过滤浮层应已关闭")
		}

		tt.Key(ui.Cmd, ui.KeyB)
		tt.Frame()
		if !state.sidebarCollapsed {
			t.Fatal("浮层关闭后 ⌘B 应正常折叠侧栏")
		}
		tt.Key(ui.Cmd, ui.KeyB)
		tt.Frame()
		if state.sidebarCollapsed {
			t.Fatal("浮层关闭后 ⌘B 应正常展开侧栏")
		}

		tt.Key(ui.Cmd, ui.Key2)
		waitFor(t, tt, func() bool {
			state.mu.Lock()
			defer state.mu.Unlock()
			return state.selectedThreadID == "t2"
		})

		// 打开工作区后再打开工作区内的 anchoredPopover：按 Esc 只关闭浮层、不关闭工作区；再次按 Esc 才关闭工作区。
		state.openWorkspace("tasks")
		tt.Frame()
		if got := workspaceViewOf(state); got != "tasks" {
			t.Fatalf("工作区应已打开为 tasks，实际 %q", got)
		}
		if err := tt.Click("新建菜单"); err != nil {
			t.Fatalf("点击工作区内新建菜单失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			return tt.HasText("新建目标")
		})

		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if tt.HasText("新建目标") {
			t.Fatal("按 Esc 后工作区内浮层应已关闭")
		}
		if got := workspaceViewOf(state); got != "tasks" {
			t.Fatalf("浮层打开时按 Esc 不应关闭工作区，实际 %q", got)
		}

		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if got := workspaceViewOf(state); got != "chat" {
			t.Fatalf("浮层关闭后按 Esc 应关闭工作区回到 chat，实际 %q", got)
		}
	})

	t.Run("modelPicker直接调用PopoverBase时同样让路", func(t *testing.T) {
		resetComposerModelPickerForTest(t)
		fake := newPopoverShortcutFakeClient()
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 700)
		waitFor(t, tt, func() bool {
			return tt.HasText("Claude 3.7 Sonnet")
		})

		if err := tt.Click("选择模型"); err != nil {
			t.Fatalf("点击选择模型触发器失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			return tt.HasText("选择其他模型")
		})

		tt.Key(ui.Cmd, ui.KeyB)
		tt.Frame()
		if state.sidebarCollapsed {
			t.Fatal("模型浮层打开时 ⌘B 不应折叠侧栏")
		}

		tt.Key(ui.Cmd, ui.Key2)
		tt.Frame()
		state.mu.Lock()
		selected := state.selectedThreadID
		state.mu.Unlock()
		if selected != "t1" {
			t.Fatalf("模型浮层打开时 ⌘2 不应切换会话，实际 %q", selected)
		}

		// 按 Esc 关闭模型浮层，之后快捷键恢复。
		tt.Key(0, ui.KeyEscape)
		waitFor(t, tt, func() bool {
			return !tt.HasText("选择其他模型")
		})

		tt.Key(ui.Cmd, ui.KeyB)
		tt.Frame()
		if !state.sidebarCollapsed {
			t.Fatal("模型浮层关闭后 ⌘B 应正常折叠侧栏")
		}
	})

	t.Run("按窗口隔离且原生菜单动作照常执行", func(t *testing.T) {
		fakeA := newPopoverShortcutFakeClient()
		stateA, viewA := buildRoot(fakeA)
		t.Cleanup(stateA.close)
		ttA := ui.NewTester(viewA, 1000, 700)

		fakeB := newPopoverShortcutFakeClient()
		stateB, viewB := buildRoot(fakeB)
		t.Cleanup(stateB.close)
		ttB := ui.NewTester(viewB, 1000, 700)

		waitFor(t, ttA, func() bool { return ttA.HasText("first") })
		waitFor(t, ttB, func() bool { return ttB.HasText("first") })

		// 仅窗口 A 打开浮层。
		stateA.sidebarFilterOpen = true
		ttA.Frame()
		if !ttA.HasText("最近 24 小时活跃") {
			t.Fatal("窗口 A 浮层未打开")
		}

		// 窗口 B 无浮层，快捷键不受窗口 A 影响。
		ttB.Key(ui.Cmd, ui.KeyB)
		ttB.Frame()
		if !stateB.sidebarCollapsed {
			t.Fatal("窗口 A 打开浮层不应阻塞窗口 B 的快捷键")
		}

		// 窗口 A 仍拦截键盘快捷键，但原生菜单派发的动作照常执行。
		ttA.Key(ui.Cmd, ui.KeyB)
		ttA.Frame()
		if stateA.sidebarCollapsed {
			t.Fatal("窗口 A 浮层打开时键盘 ⌘B 不应触发")
		}

		stateA.enqueueAction("toggle-sidebar", 0)
		ttA.Frame()
		if !stateA.sidebarCollapsed {
			t.Fatal("浮层打开时原生菜单派发的 toggle-sidebar 应照常执行")
		}
	})
}
