package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKbdKeys(t *testing.T) {
	tests := []struct {
		shortcut string
		want     []string
	}{
		{"⌘K", []string{"⌘", "K"}},
		{"Cmd+Shift+P", []string{"⇧", "⌘", "P"}},
		{"⇧⌘B", []string{"⇧", "⌘", "B"}},
		{"Esc", []string{"Esc"}},
		{"Option+Cmd+Up", []string{"⌥", "⌘", "↑"}},
		{"⌘Tab", []string{"⌘", "Tab"}},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.shortcut, func(t *testing.T) {
			if got := kbdKeys(tt.shortcut); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("kbdKeys(%q) = %q，期望 %q", tt.shortcut, got, tt.want)
			}
		})
	}
}

func TestKbdRowVisibleKeys(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		KbdRow(c, "test-kbd", "Cmd+Shift+P", "Esc")
	}, 400, 100)
	for _, label := range []string{"⇧", "⌘", "P", "Esc"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("未找到键帽 %q，当前文字：%q", label, tt.Texts())
		}
	}
}
