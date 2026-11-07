package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestToolOutputFoldBoundary(t *testing.T) {
	for _, size := range []int{299, 300, 301} {
		t.Run(strings.Repeat("x", size), func(t *testing.T) {
			s := newChatState(NewFakeClient())
			text := strings.Repeat("字", size)
			m := Message{ID: "fold", Role: "tool", Text: text}
			tt := ui.NewTester(func(c *ui.Context) { renderToolDetail(c, s, m) }, 800, 600)
			if got := tt.HasText("展开全部"); got != (size > foldRunThreshold) {
				t.Fatalf("长度 %d 展开按钮 = %v", size, got)
			}
			if size > foldRunThreshold {
				clickText(t, tt, "展开全部")
				waitFor(t, tt, func() bool { return tt.HasText(text) })
			} else if !tt.HasText(text) {
				t.Fatal("短输出应完整显示")
			}
		})
	}
}
