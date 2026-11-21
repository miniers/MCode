package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestUIReview33TerminalCmdK 验证命令面板快捷键在终端聚焦时交给终端处理（不打开且不吞按键），
// 在终端未聚焦时照常打开命令面板，且支持自定义快捷键覆盖与菜单派发分支。
func TestUIReview33TerminalCmdK(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })

	t.Run("终端聚焦时按命令面板快捷键不打开命令面板", func(t *testing.T) {
		tt, state, st := setupTerminalFindTester(t)
		_ = openAndFocusTerminalTab(t, tt, state, st)

		mods, key, ok := shortcutBindingFor("command-palette")
		if !ok {
			t.Fatal("缺少 command-palette 快捷键绑定")
		}
		tt.Key(mods, key)
		tt.Frame()

		if getPaletteState(state).open {
			t.Fatal("终端聚焦时按命令面板快捷键不应打开命令面板")
		}
		if tt.HasText("↑↓ 选择") {
			t.Fatal("终端聚焦时界面不应渲染命令面板")
		}
	})

	t.Run("终端未聚焦时按同一快捷键打开命令面板", func(t *testing.T) {
		t.Run("终端未打开", func(t *testing.T) {
			tt, state, _ := setupTerminalFindTester(t)

			mods, key, ok := shortcutBindingFor("command-palette")
			if !ok {
				t.Fatal("缺少 command-palette 快捷键绑定")
			}
			tt.Key(mods, key)
			tt.Frame()

			if !getPaletteState(state).open {
				t.Fatal("终端未打开时按命令面板快捷键应打开命令面板")
			}
			if !tt.HasText("↑↓ 选择") {
				t.Fatalf("命令面板打开后应显示面板内容，texts=%v", tt.Texts())
			}
		})

		t.Run("终端已打开但焦点在消息输入框", func(t *testing.T) {
			tt, state, st := setupTerminalFindTester(t)
			_ = openAndFocusTerminalTab(t, tt, state, st)

			if err := tt.Click("消息输入框"); err != nil {
				t.Fatalf("点击消息输入框失败: %v", err)
			}
			tt.Frame()
			terminalMu.Lock()
			focused := st.focused
			terminalMu.Unlock()
			if focused {
				t.Fatal("焦点切回消息输入框后 terminalState.focused 应为 false")
			}

			mods, key, ok := shortcutBindingFor("command-palette")
			if !ok {
				t.Fatal("缺少 command-palette 快捷键绑定")
			}
			tt.Key(mods, key)
			tt.Frame()

			if !getPaletteState(state).open {
				t.Fatal("终端失焦后按命令面板快捷键应打开命令面板")
			}
		})
	})

	t.Run("自定义命令面板快捷键按有效绑定判断", func(t *testing.T) {
		tt, state, st := setupTerminalFindTester(t)
		setShortcutOverrides(map[string]string{"command-palette": "Cmd+Shift+L"})
		t.Cleanup(func() { setShortcutOverrides(nil) })

		mods, key, ok := shortcutBindingFor("command-palette")
		if !ok || mods != ui.Cmd|ui.Shift || key != ui.KeyL {
			t.Fatalf("command-palette 有效绑定应为 Cmd+Shift+L，实际 ok=%v mods=%v key=%v", ok, mods, key)
		}

		_ = openAndFocusTerminalTab(t, tt, state, st)
		tt.Key(mods, key)
		tt.Frame()
		if getPaletteState(state).open {
			t.Fatal("终端聚焦时按自定义命令面板快捷键不应打开命令面板")
		}

		if err := tt.Click("消息输入框"); err != nil {
			t.Fatalf("点击消息输入框失败: %v", err)
		}
		tt.Frame()

		tt.Key(mods, key)
		tt.Frame()
		if !getPaletteState(state).open {
			t.Fatal("终端失焦后按自定义命令面板快捷键应打开命令面板")
		}
	})

	t.Run("菜单动作 command-palette 同样尊重终端聚焦", func(t *testing.T) {
		tt, state, st := setupTerminalFindTester(t)
		_ = openAndFocusTerminalTab(t, tt, state, st)

		state.enqueueAction("command-palette", 0)
		tt.Frame()
		if getPaletteState(state).open {
			t.Fatal("终端聚焦时菜单动作 command-palette 不应打开命令面板")
		}

		if err := tt.Click("消息输入框"); err != nil {
			t.Fatalf("点击消息输入框失败: %v", err)
		}
		tt.Frame()

		state.enqueueAction("command-palette", 0)
		tt.Frame()
		if !getPaletteState(state).open {
			t.Fatal("终端失焦后菜单动作 command-palette 应打开命令面板")
		}
	})

	t.Run("terminalFocused 状态判定", func(t *testing.T) {
		stubTerminals(t)
		if terminalFocused(nil) {
			t.Fatal("nil chatState 不应判定为终端聚焦")
		}

		s := newChatState(NewFakeClient())
		defer s.close()
		s.selectedThreadID = "t1"
		st := getTerminalState(s)

		if terminalFocused(s) {
			t.Fatal("未展开且无标签时不应判定为终端聚焦")
		}

		tab, err := createTerminalTab(s, st, t.TempDir(), false)
		if err != nil {
			t.Fatalf("createTerminalTab 失败: %v", err)
		}

		terminalMu.Lock()
		st.visible = false
		st.focused = true
		terminalMu.Unlock()
		if terminalFocused(s) {
			t.Fatal("终端不可见时不应判定为终端聚焦")
		}

		terminalMu.Lock()
		st.visible = true
		st.focused = false
		terminalMu.Unlock()
		if terminalFocused(s) {
			t.Fatal("终端未聚焦时不应判定为终端聚焦")
		}

		terminalMu.Lock()
		st.focused = true
		terminalMu.Unlock()
		if !terminalFocused(s) {
			t.Fatal("终端可见、聚焦且存在活动标签时应返回 true")
		}

		terminalMu.Lock()
		origTerm := tab.term
		tab.term = nil
		terminalMu.Unlock()
		if terminalFocused(s) {
			t.Fatal("活动标签终端实例为空时不应判定为终端聚焦")
		}
		terminalMu.Lock()
		tab.term = origTerm
		terminalMu.Unlock()
	})
}
