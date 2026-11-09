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

func TestAnchoredPopoverPlacementAndWidth(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		align                     PopoverAlign
		width, minWidth, maxWidth float32
	}{
		{"下方左对齐与最小宽度", BelowStart, 40, 100, 180},
		{"下方右对齐与最大宽度", BelowEnd, 240, 100, 180},
		{"上方左对齐", AboveStart, 120, 100, 180},
		{"上方右对齐", AboveEnd, 120, 100, 180},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := true
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Fill().Children(func() {
					anchor := ui.Box(c).Label("锚点").Absolute().Left(200).Top(150).Width(80).Height(24)
					anchoredPopover(c, anchoredPopoverProps{Anchor: anchor, Open: &open, Align: tc.align, MinWidth: tc.minWidth, MaxWidth: tc.maxWidth, MaxHeight: 90}, func(panel ui.Element) {
						panel.Label("浮层面板")
						ui.Box(c).Width(tc.width).Height(60)
					})
				})
			}, 600, 400)
			tt.Frame()
			tt.Advance(motionPop + 60*time.Millisecond)
			anchor, ok := tt.Find("锚点")
			if !ok {
				t.Fatal("未找到锚点")
			}
			panel, ok := tt.Find("浮层面板")
			if !ok {
				t.Fatal("未找到浮层面板")
			}
			if panel.W < tc.minWidth || panel.W > tc.maxWidth || panel.H > 90 {
				t.Fatalf("浮层尺寸超出限制：%+v", panel)
			}
			if tc.align == AboveStart || tc.align == AboveEnd {
				if panel.Y+panel.H > anchor.Y {
					t.Fatalf("浮层不在锚点上方：%+v %+v", anchor, panel)
				}
			} else if panel.Y < anchor.Y+anchor.H {
				t.Fatalf("浮层不在锚点下方：%+v %+v", anchor, panel)
			}
			if tc.align == BelowEnd || tc.align == AboveEnd {
				if math.Abs(float64(panel.X+panel.W-anchor.X-anchor.W)) > 1 {
					t.Fatalf("浮层未右对齐：%+v %+v", anchor, panel)
				}
			} else if math.Abs(float64(panel.X-anchor.X)) > 1 {
				t.Fatalf("浮层未左对齐：%+v %+v", anchor, panel)
			}
		})
	}
}

func TestAnchoredPopoverDismiss(t *testing.T) {
	for _, escape := range []bool{false, true} {
		t.Run(map[bool]string{false: "外部点击", true: "Escape"}[escape], func(t *testing.T) {
			open, dismissed, rendered := true, 0, 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Fill().Children(func() {
					anchor := ui.Box(c).Absolute().Left(20).Top(20).Width(80).Height(24)
					appPopover(c, anchor, &open, func() { dismissed++ }, func() { rendered++; ui.Text(c, "浮层内容") })
				})
			}, 400, 300)
			tt.Frame()
			if escape {
				tt.Key(0, ui.KeyEscape)
			} else {
				tt.ClickAt(350, 250)
			}
			tt.Frame()
			if open || dismissed != 1 {
				t.Fatalf("open=%v，关闭回调=%d", open, dismissed)
			}
			before := rendered
			tt.Frame()
			if rendered != before || dismissed != 1 {
				t.Fatalf("关闭后仍渲染或重复回调：渲染=%d，回调=%d", rendered-before, dismissed)
			}
		})
	}
}
