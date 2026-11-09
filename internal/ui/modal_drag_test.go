package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func testDraggableSheet(c *ui.Context, key string, open *bool, title string, width float32, draggable, resizable bool, body func()) {
	if open == nil || !*open {
		if draggable || resizable { modalDragReset(key) }
		return
	}
	w, h := c.Size()
	height := float32(0)
	if resizable {
		if width <= 0 { width = 560 }
		width, height = modalResizeSize(key, width, 420, 360, 240, w, h)
	}
	Sheet(c, SheetProps{Key: key, Label: "dialog-card-" + key, Open: open, Width: width, Height: height}, func() {
		ui.Column(c).FillWidth().Grow(1).Padding(Space6).Gap(Space4).Children(func() {
			titleRow := ui.Row(c).FillWidth()
			if draggable { titleRow = modalDragHandle(c, key, titleRow) }
			titleRow.Children(func() { ui.Text(c, title).FontSize(TypeTitle).FontWeight(weightFor(c, 600)).TextColor(ThemePrimaryText(c)) })
			if body != nil { body() }
		})
		if resizable { modalResizeGrips(c, key, width, height) }
	})
}

func TestModalResize_Integration(t *testing.T) {
	for _, tc := range []struct {
		name string
		x, y, dx, dy, dw, dh float32
	}{
		{"left", 2, 210, -30, 0, 30, 0},
		{"right", 498, 210, 30, 0, 30, 0},
		{"top", 250, 2, 0, -30, 0, 30},
		{"bottom", 250, 418, 0, 30, 0, 30},
		{"top-left", 4, 4, -30, -30, 30, 30},
		{"top-right", 496, 4, 30, -30, 30, 30},
		{"bottom-left", 4, 416, -30, 30, 30, 30},
		{"bottom-right", 496, 416, 30, 30, 30, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "integration-" + tc.name
			t.Cleanup(func() { modalDragReset(key) })
			open := true
			tt := ui.NewTester(func(c *ui.Context) {
				testDraggableSheet(c, key, &open, "标题", 500, true, true, nil)
			}, 1200, 1000)
			tt.Frame()
			before, ok := tt.Find("dialog-card-" + key)
			if !ok { t.Fatal("未找到弹窗") }
			x, y := before.X+tc.x, before.Y+tc.y
			tt.Press(x, y)
			tt.Move(x+tc.dx, y+tc.dy)
			tt.Release(x+tc.dx, y+tc.dy)
			tt.Frame()
			after, ok := tt.Find("dialog-card-" + key)
			if !ok || after.W != before.W+tc.dw || after.H != before.H+tc.dh {
				t.Fatalf("缩放前 %v，缩放后 %v", before, after)
			}
			if tc.dw == 0 && after.X != before.X || tc.dh == 0 && after.Y != before.Y {
				t.Fatalf("缩放误触标题拖动：前 %v，后 %v", before, after)
			}
		})
	}

	t.Run("state", func(t *testing.T) {
		const first, second = "integration-first", "integration-second"
		t.Cleanup(func() { modalDragReset(first); modalDragReset(second) })
		key, open := first, true
		tt := ui.NewTester(func(c *ui.Context) {
			testDraggableSheet(c, key, &open, "标题", 500, true, true, nil)
		}, 1200, 1000)
		tt.Frame()
		initial, ok := tt.Find("dialog-card-" + key)
		if !ok { t.Fatal("未找到弹窗") }
		// 标题栏只改变位置，不改变尺寸。
		x, y := initial.X+250, initial.Y+30
		tt.Press(x, y); tt.Move(x+40, y+30); tt.Release(x+40, y+30); tt.Frame()
		moved, ok := tt.Find("dialog-card-" + key)
		if !ok || moved.X == initial.X || moved.Y == initial.Y || moved.W != initial.W || moved.H != initial.H {
			t.Fatalf("标题拖动前 %v，拖动后 %v", initial, moved)
		}
		x, y = moved.X+moved.W-4, moved.Y+moved.H-4
		tt.Press(x, y); tt.Move(x-1000, y-1000); tt.Release(x-1000, y-1000); tt.Frame()
		small, ok := tt.Find("dialog-card-" + key)
		if !ok || small.W != 360 || small.H != 240 { t.Fatalf("最小尺寸：%v", small) }
		x, y = small.X+small.W-4, small.Y+small.H-4
		tt.Press(x, y); tt.Move(x+2000, y+2000); tt.Release(x+2000, y+2000); tt.Frame(); tt.Frame()
		large, ok := tt.Find("dialog-card-" + key)
		if !ok || large.W > 1160 || large.H > 960 || large.X < 0 || large.Y < 0 || large.X+large.W > 1200 || large.Y+large.H > 1000 {
			t.Fatalf("弹窗超出窗口：%v", large)
		}
		key = second
		tt.Frame()
		other, ok := tt.Find("dialog-card-" + key)
		if !ok || other.W != initial.W || other.H != initial.H { t.Fatalf("不同 key 互相影响：%v", other) }
		key = first
		tt.Frame()
		restored, ok := tt.Find("dialog-card-" + key)
		if !ok || restored.W != large.W || restored.H != large.H { t.Fatalf("切换 key 后尺寸丢失：%v", restored) }
		open = false
		tt.Frame()
		open = true
		tt.Frame()
		reopened, ok := tt.Find("dialog-card-" + key)
		if !ok || reopened.W != initial.W || reopened.H != initial.H { t.Fatalf("关闭后尺寸未复位：%v", reopened) }
		x, y = reopened.X+reopened.W-4, reopened.Y+reopened.H-4
		tt.Press(x, y); tt.Move(x+30, y+30); tt.Release(x+30, y+30); tt.Frame()
		resized, ok := tt.Find("dialog-card-" + key)
		if !ok || resized.W == initial.W || resized.H == initial.H { t.Fatalf("双击前未缩放：%v", resized) }
		x, y = resized.X+resized.W-4, resized.Y+resized.H-4
		tt.ClickAt(x, y); tt.Frame(); tt.ClickAt(x, y); tt.Frame()
		reset, ok := tt.Find("dialog-card-" + key)
		if !ok || reset.W != initial.W || reset.H != initial.H { t.Fatalf("双击角后未复位：%v", reset) }
	})
}
