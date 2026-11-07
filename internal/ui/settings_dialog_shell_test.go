package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsWindowShell(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 640, 420)
	tt.Frame()
	root, ok := tt.Find("设置")
	if !ok || root.X != 0 || root.Y != 0 || root.W != 640 || root.H != 420 {
		t.Fatalf("设置窗口应铺满内容区：%+v", root)
	}
	if tt.HasText("返回") {
		t.Fatal("设置导航仍显示返回")
	}
}

func TestSettingsWindowSheetEscape(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	openSettingsSheet(st, "shortcuts", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 960, 680)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if sheet, _ := settingsTreeSheetState(st); sheet != "" || !st.IsOpen() {
		t.Fatalf("Esc 后 sheet = %q，设置打开 = %v", sheet, st.IsOpen())
	}
}
