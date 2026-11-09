package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSheetDismissAndBounds(t *testing.T) {
	for _, escape := range []bool{false, true} {
		t.Run(map[bool]string{false: "遮罩", true: "Escape"}[escape], func(t *testing.T) {
			open := true
			tt := ui.NewTester(func(c *ui.Context) {
				Sheet(c, SheetProps{Key: "sheet-test", Label: "测试卡片", Open: &open, Width: 800, Height: 600}, func() {
					ui.ButtonBase(c).Label("卡片内容").Width(80).Height(40)
				})
			}, 400, 300)
			t.Cleanup(func() { modalDragReset("sheet-test") })
			tt.Frame()
			card, ok := tt.Find("测试卡片")
			if !ok || card.W > 400 || card.H > 300 || card.X < 0 || card.Y < 0 || card.X+card.W > 400 || card.Y+card.H > 300 {
				t.Fatalf("卡片超出窗口：%+v，找到=%v", card, ok)
			}
			if err := tt.Click("卡片内容"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !open {
				t.Fatal("点击卡片内部关闭了弹窗")
			}
			if escape {
				tt.Key(0, ui.KeyEscape)
			} else {
				tt.ClickAt(1, 1)
			}
			tt.Frame()
			if open {
				t.Fatal("弹窗未关闭")
			}
		})
	}
}

func TestSheetReduceMotionDoesNotScheduleFrames(t *testing.T) {
	open := true
	tt := ui.NewTesterWith(func(c *ui.Context) {
		Sheet(c, SheetProps{Key: "sheet-reduce-motion", Label: "测试卡片", Open: &open, Width: 300, Height: 200}, nil)
	}, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	t.Cleanup(func() { modalDragReset("sheet-reduce-motion") })
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("减少动效时仍有待播放帧")
	}
}
