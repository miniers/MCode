package mcodeui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 浮层出现时从下方 4 DIP 升起并淡入：打开的那一帧比静止位置低 4 DIP。
func TestPopoverRisesFromFourDips(t *testing.T) {
	open := false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("pop-shell")).Fill().Children(func() {
			anchor := ui.Box(c.Key("pop-anchor")).Absolute().Left(20).Top(20).Width(80).Height(24)
			appPopover(c, anchor, &open, nil, func() {
				popoverCard(c, ui.Column(c).Children(func() {
					ui.Text(c, "浮层内容")
				}))
			})
		})
	}, 400, 300)

	if _, ok := tt.Find("浮层内容"); ok {
		t.Fatal("关闭状态下不应渲染浮层")
	}

	open = true
	// 打开后只重建视图、不推进时间：面板落点稳定下来时升起进度仍是 0。
	tt.Frame()
	tt.Frame()
	start, ok := tt.Find("浮层内容")
	if !ok {
		t.Fatalf("打开后应渲染浮层，texts: %q", tt.Texts())
	}

	tt.Advance(motionPop + 60*time.Millisecond)
	settled, ok := tt.Find("浮层内容")
	if !ok {
		t.Fatalf("动画结束后应保留浮层，texts: %q", tt.Texts())
	}
	if got := start.Y - settled.Y; math.Abs(float64(got-motionRiseOffset)) > 0.6 {
		t.Fatalf("浮层应从下方 %v DIP 升起，got %v（%v -> %v）", motionRiseOffset, got, start.Y, settled.Y)
	}
}

// 升起方向随落点翻转：面板落在锚点下方时从下方升起，翻到上方时从上方落下。
func TestPopoverRiseDirectionFollowsPlacement(t *testing.T) {
	cases := []struct {
		name      string
		anchorTop float32
		contentH  float32
		want      float32
	}{
		{"落在锚点下方", 20, 60, motionRiseOffset},
		{"翻到锚点上方", 250, 120, -motionRiseOffset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open := true
			var offset float32
			ready := false
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c.Key("rise-shell")).Fill().Children(func() {
					anchor := ui.Box(c.Key("rise-anchor")).Absolute().Left(20).Top(tc.anchorTop).Width(80).Height(24)
					ui.PopoverBase(c, anchor, &open, func(panel ui.Element) {
						panel.MinWidth(100)
						ui.Box(c).Height(tc.contentH)
						// 首帧面板还没有尺寸，从第二帧起才能判断落点。
						if panel.Bounds().H > 0 {
							offset, ready = popoverRiseOffset(panel, anchor), true
						}
					})
				})
			}, 400, 300)

			// 面板尺寸与翻转都要下一帧才生效，多跑几帧让落点稳定下来。
			for i := 0; i < 4; i++ {
				tt.Frame()
			}
			if !ready {
				t.Fatal("第二帧起面板尺寸应可用")
			}
			if offset != tc.want {
				t.Fatalf("升起偏移应为 %v，got %v", tc.want, offset)
			}
		})
	}
}
