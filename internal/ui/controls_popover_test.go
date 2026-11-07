package mcodeui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPopoverEntrance(t *testing.T) {
	for _, tc := range []struct{ progress, scale, y, opacity float32 }{
		{0, 0.95, Space1, 0},
		{1, 1, 0, 1},
	} {
		scale, y, opacity := popoverEntrance(tc.progress)
		if math.Abs(float64(scale-tc.scale)) > 0.00001 || y != tc.y || opacity != tc.opacity {
			t.Errorf("进度 %v: 得到 %v, %v, %v", tc.progress, scale, y, opacity)
		}
	}
}

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

func TestPopoverMenuItemClickAndDisabled(t *testing.T) {
	open, clicked, disabled := true, 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() {
			anchor := ui.Box(c).Absolute().Left(20).Top(20).Width(80).Height(24)
			PopoverCard(c, PopoverProps{Anchor: anchor, Open: &open, Width: 180}, func() {
				MenuItem(c, "enabled", "执行操作", MenuItemOpts{OnClick: func() { clicked++ }})
				MenuItem(c, "disabled", "禁用操作", MenuItemOpts{Disabled: true, OnClick: func() { disabled++ }})
			})
		})
	}, 400, 300)
	tt.Advance(DurBase)
	if err := tt.Click("执行操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("禁用操作"); err != nil {
		t.Fatal(err)
	}
	if clicked != 1 || disabled != 0 {
		t.Fatalf("点击回调=%d，禁用回调=%d", clicked, disabled)
	}
}

func TestDialogButtonClick(t *testing.T) {
	open, clicked := true, 0
	tt := ui.NewTester(func(c *ui.Context) {
		Dialog(c, DialogProps{Key: "dialog-test", Open: &open, Title: "测试弹窗", Width: 240, Footer: func() {
			Button(c, ButtonOpts{Text: "保存更改", OnClick: func() { clicked++ }})
		}}, func() { ui.Text(c, "弹窗正文") })
	}, 400, 300)
	tt.Frame()
	if err := tt.Click("保存更改"); err != nil {
		t.Fatal(err)
	}
	if clicked != 1 {
		t.Fatalf("按钮回调次数=%d", clicked)
	}
}
