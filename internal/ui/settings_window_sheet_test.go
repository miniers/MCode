package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsWindowSheetCloseKeepsWindow(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	openSettingsSheet(st, "shortcuts", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 640, 420)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()
	if sheet, _ := settingsTreeSheetState(st); sheet != "shortcuts" {
		t.Fatal("二级面板未打开")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if sheet, _ := settingsTreeSheetState(st); sheet != "" || !st.IsOpen() {
		t.Fatal("Esc 应只关闭二级面板")
	}
	st.Close()
	if sheet, _ := settingsTreeSheetState(st); sheet != "" || st.IsOpen() {
		t.Fatal("关闭设置应清理二级面板")
	}
}
