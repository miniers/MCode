package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPopoverCardDismissedAfterOutsideClick(t *testing.T) {
	open, dismissed := true, 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() {
			anchor := ui.Box(c).Absolute().Left(20).Top(20).Width(80).Height(24)
			PopoverCard(c, PopoverProps{Anchor: anchor, Open: &open, Width: 120, OnDismiss: func() { dismissed++ }}, func() {
				ui.Text(c, "浮层内容")
			})
		})
	}, 400, 300)
	tt.Frame()
	tt.Frame()
	tt.ClickAt(350, 250)
	tt.Frame()
	if open || dismissed != 1 {
		t.Fatalf("点击外部后 open=%v，回调次数=%d", open, dismissed)
	}
}

func TestPopoverCardClosedDoesNotRender(t *testing.T) {
	open, rendered := false, 0
	tt := ui.NewTester(func(c *ui.Context) {
		anchor := ui.Box(c).Width(80).Height(24)
		PopoverCard(c, PopoverProps{Anchor: anchor, Open: &open}, func() { rendered++ })
		PopoverCard(c, PopoverProps{Anchor: anchor}, func() { rendered++ })
	}, 400, 300)
	tt.Frame()
	if rendered != 0 {
		t.Fatalf("关闭的浮层渲染了 %d 次", rendered)
	}
}
