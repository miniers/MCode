package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsNavHitA11yLabelIsTitle(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsNav(c, st)
	}, 900, 1400)
	tt.Frame()

	if err := tt.Click("搜索设置"); err != nil {
		t.Fatalf("点击搜索设置失败: %v", err)
	}
	tt.Type("排队")
	tt.Frame()

	info, ok := tt.Access("运行中排队消息")
	if !ok {
		t.Fatalf("未找到命中行的无障碍信息: %v", tt.Texts())
	}
	if info.Name != "运行中排队消息" || strings.HasPrefix(info.Name, "settings-nav-hit-") {
		t.Fatalf("命中行无障碍标签 = %q，期望为设置标题且不以 settings-nav-hit- 开头", info.Name)
	}
	for _, a := range tt.AccessAll() {
		if strings.HasPrefix(a.Name, "settings-nav-hit-") {
			t.Fatalf("无障碍树仍出现内部 ID 标签 %q", a.Name)
		}
	}
}

func TestSettingsShortcutResetConfirmEscKeepsWindow(t *testing.T) {
	resetSettingsWindowForTest()
	t.Cleanup(resetSettingsWindowForTest)

	owner := newChatState(NewFakeClient())
	t.Cleanup(owner.close)
	st := NewSettingsState(settingsTestDir(t), nil, owner)
	setChatSettings(owner, st)
	openSettingsWindowHeadless(owner, "")
	openSettingsSheet(st, "shortcuts", "")

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, 680)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()

	if err := settingsClick(t, tt, "全部恢复默认"); err != nil {
		t.Fatalf("点击全部恢复默认失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.shortcutResetConfirmOpen && tt.HasText("全部恢复默认快捷键？")
	})

	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	if st.shortcutResetConfirmOpen || tt.HasText("全部恢复默认快捷键？") {
		t.Fatal("按 Esc 后「全部恢复默认快捷键」确认框应关闭")
	}
	if !st.IsOpen() || currentSettingsWindowState() != st {
		t.Fatal("确认框按 Esc 不应关闭设置窗口")
	}
	if sheet, _ := settingsTreeSheetState(st); sheet != "shortcuts" {
		t.Fatalf("确认框按 Esc 不应关闭快捷键面板，当前 sheet=%q", sheet)
	}

	// 直接处于无二级面板但确认框打开态时，窗口 Esc 守卫也必须拦住关闭。
	closeSettingsSheet(st)
	st.shortcutResetConfirmOpen = true
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !st.IsOpen() || currentSettingsWindowState() != st {
		t.Fatal("shortcutResetConfirmOpen 为真时按 Esc 不应触发设置窗口关闭")
	}
}

func TestSettingsVisualTileA11yLabel(t *testing.T) {
	selected := "worktree"
	opts := []visualPickerOption{
		{value: "worktree", label: "独立副本 (Worktree)", icon: IconGitBranch()},
		{value: "shared", label: "共享工作区 (Shared)", icon: IconFolder()},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderVisualPicker(c, "picker-test-visual", &selected, opts, false, nil)
	}, 600, 200)
	tt.Frame()

	for _, opt := range opts {
		info, ok := tt.Access(opt.label)
		if !ok {
			t.Fatalf("未找到方块读屏标签 %q", opt.label)
		}
		if info.Name != opt.label {
			t.Fatalf("方块读屏标签 = %q，期望 %q", info.Name, opt.label)
		}
		if _, leaked := tt.Access(opt.value); leaked {
			t.Fatalf("方块不应以内部枚举值 %q 作为读屏标签", opt.value)
		}
	}
}
