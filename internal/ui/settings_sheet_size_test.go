package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsSheetSize(t *testing.T) {
	for _, tc := range []struct { id string; width float32 }{{"provider", 720}} {
		t.Run(tc.id, func(t *testing.T) {
			sh, ok := settingsTreeSheetOf(tc.id)
			if !ok { t.Fatalf("missing sheet %q", tc.id) }
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			openSettingsSheet(st, tc.id, "")
			t.Cleanup(func() { closeSettingsSheet(st) })
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sh) }, 1200, 1000)
			tt.Frame()
			bounds, ok := tt.Find("设置面板 " + sh.Title)
			if !ok || bounds.W != tc.width { t.Fatalf("sheet bounds = %v, found = %v, want width %v", bounds, ok, tc.width) }
			if _, ok := tt.Find("调整弹窗大小"); !ok { t.Fatal("missing resize grip") }
		})
	}
}
