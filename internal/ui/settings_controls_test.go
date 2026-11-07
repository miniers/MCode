package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsControls_LinkRowClick(t *testing.T) {
	for _, tc := range []struct {
		name    string
		x       float32
		enabled bool
	}{
		{"标题", 24, true},
		{"空白区", 300, true},
		{"禁用", 300, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, nil)
			row := setting{
				ID: "row-test-link", Title: "入口",
				Enabled: func(*SettingsState) bool { return tc.enabled },
				Control: link(func(*SettingsState) string { return "值" }, "test-sheet"),
			}
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSetting(c, st, row) }, 600, 100)
			tt.Frame()
			label, ok := tt.Find("入口")
			if !ok {
				t.Fatal("未找到入口标题")
			}
			tt.ClickAt(tc.x, label.Y+label.H/2)
			tt.Frame()
			opened, _ := settingsTreeSheetState(st)
			if tc.enabled && opened != "test-sheet" || !tc.enabled && opened != "" {
				t.Fatalf("面板状态不符合预期：%q", opened)
			}
		})
	}
}

// TestSettingsControls_ToggleByLabel 校验开关行改用 Switch 组件后仍按「切换+标题」定位，
// 点一下就翻转并落盘。
func TestSettingsControls_ToggleByLabel(t *testing.T) {
	dataDir := settingsTestDir(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(dataDir, nil, s)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1200, 1600)
	tt.Frame()

	const label = "切换完成后通知"
	if _, ok := tt.Find(label); !ok {
		t.Fatalf("找不到开关行 %q：%v", label, tt.Texts())
	}

	st.mu.Lock()
	before := st.cfg.Notifications.NotifyWhenFinished
	st.mu.Unlock()

	if err := tt.Click(label); err != nil {
		t.Fatalf("点击 %q 失败: %v", label, err)
	}
	tt.Frame()

	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Notifications.NotifyWhenFinished == !before
	})
	settingsSaveWriters.Wait()
}
