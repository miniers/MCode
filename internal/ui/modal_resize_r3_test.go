package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTextDialogs_Resize(t *testing.T) {
	for _, tc := range []struct {
		name, key, label string
		open func(*chatState)
		render func(*ui.Context, *chatState)
	}{
		{"output", toolOutputDialogKey, "tool-output-dialog-card", func(s *chatState) { s.toolOutputDialog = &toolOutputDialogState{title: "输出", text: strings.Repeat("长输出\n", 100)} }, renderToolOutputDialog},
		{"shortcuts", shortcutHelpKey, "shortcut-help-card", func(s *chatState) { s.toggleShortcutHelp() }, renderShortcutHelp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)
			t.Cleanup(func() { modalDragReset(tc.key) })
			tc.open(s)
			tt := ui.NewTester(func(c *ui.Context) { tc.render(c, s) }, 1200, 1000)
			tt.Frame()
			initial, ok := tt.Find(tc.label)
			if !ok { t.Fatal("未找到弹窗") }
			x, y := initial.X+initial.W-4, initial.Y+initial.H-4
			tt.Press(x, y)
			tt.Move(x+30, y+30)
			tt.Release(x+30, y+30)
			tt.Frame()
			larger, ok := tt.Find(tc.label)
			if !ok || larger.W <= initial.W || larger.H <= initial.H { t.Fatalf("拖大前 %v，拖大后 %v", initial, larger) }
			x, y = larger.X+larger.W-4, larger.Y+larger.H-4
			tt.Press(x, y)
			tt.Move(x-1100, y-900)
			tt.Release(x-1100, y-900)
			tt.Frame()
			small, ok := tt.Find(tc.label)
			if !ok || small.W != 360 || small.H != 240 { t.Fatalf("最小尺寸 = %v", small) }
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			tc.open(s)
			tt.Frame()
			reopened, ok := tt.Find(tc.label)
			if !ok || reopened.W != initial.W || reopened.H != initial.H { t.Fatalf("重新打开尺寸 = %v，初始 = %v", reopened, initial) }
		})
	}
}
