package mcodeui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionControlsReducedFeedback(t *testing.T) {
	setAppReduceMotion(t, true)
	copied, active, ratio, selected, count := false, false, float32(0), 0, 4
	tester := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c).Width(200).Children(func() {
			CopyButton(c, CopyButtonProps{Key: "copy", Label: "复制按钮", Copied: copied})
			ProgressBar(c, ProgressBarProps{Key: "progress", Label: "进度", Ratio: ratio, Height: Space1})
			TabButton(c, TabButtonProps{Key: "tab", Label: "标签", Active: active}, func() {
				ui.Box(c).Width(100).Height(30)
			})
			Segmented(c, "segments", &selected, []Segment{{Label: "甲", Count: 3}, {Label: "乙", Count: count}}, nil)
			Button(c, ButtonOpts{Key: "loading", Text: "忙碌", Loading: true})
		})
	}, 300, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tester.Frame()
	tab, ok := tester.Find("标签")
	if !ok {
		t.Fatal("找不到标签")
	}
	x, y := int(tab.X+tab.W/2), int(tab.Y+tab.H/2)
	before := tester.Image().At(x, y)
	copied, active, ratio, selected, count = true, true, 0.75, 1, 7
	tester.Frame()
	if _, ok := tester.Find("复制成功"); !ok {
		t.Fatal("复制成功图标未立即显示")
	}
	if _, ok := tester.Find("复制图标"); ok {
		t.Fatal("复制成功后仍显示原图标")
	}
	track, trackOK := tester.Find("进度 75%")
	fill, fillOK := tester.Find("进度 填充")
	if !trackOK || !fillOK || math.Abs(float64(fill.W-track.W*ratio)) > 0.01 {
		t.Fatalf("进度未立即到终值：轨道=%+v 填充=%+v", track, fill)
	}
	if after := tester.Image().At(x, y); before == after {
		t.Fatal("标签选中背景未立即变化")
	}
	if _, ok := tester.Find("7"); !ok {
		t.Fatal("分段数量未显示")
	}
	if _, ok := tester.Find("加载中"); !ok {
		t.Fatal("加载按钮缺少静态指示器")
	}
	tester.Frame()
	if tester.FramePending() {
		t.Fatal("减少动效时仍有待处理动画帧")
	}
}

func TestMotionControlsFeedbackStops(t *testing.T) {
	copied, active, ratio := false, false, float32(0)
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(200).Children(func() {
			CopyButton(c, CopyButtonProps{Key: "copy", Label: "复制", Copied: copied})
			ProgressBar(c, ProgressBarProps{Key: "progress", Label: "进度", Ratio: ratio, Height: Space1})
			TabButton(c, TabButtonProps{Key: "tab", Label: "标签", Active: active}, nil)
		})
	}, 300, 200)
	tester.Advance(time.Second)
	copied, active, ratio = true, true, 1
	tester.Frame()
	tester.Advance(time.Second)
	tester.Frame()
	if tester.FramePending() {
		t.Fatal("有限反馈完成后仍请求帧")
	}
	track, trackOK := tester.Find("进度 100%")
	fill, fillOK := tester.Find("进度 填充")
	if !trackOK || !fillOK || math.Abs(float64(fill.W-track.W)) > 0.01 {
		t.Fatalf("进度未到终值：轨道=%+v 填充=%+v", track, fill)
	}
}

func TestMotionControlsPopoverDismissOnce(t *testing.T) {
	for _, escape := range []bool{false, true} {
		t.Run(map[bool]string{false: "外部点击", true: "Escape"}[escape], func(t *testing.T) {
			open, dismissed := true, 0
			tester := ui.NewTesterWith(func(c *ui.Context) {
				ui.Box(c).Fill().Children(func() {
					anchor := ui.Box(c).Absolute().Left(20).Top(20).Width(80).Height(24)
					appPopover(c, anchor, &open, func() { dismissed++ }, func() { ui.Text(c, "浮层内容") })
				})
			}, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
			tester.Frame()
			if escape {
				tester.Key(0, ui.KeyEscape)
			} else {
				tester.ClickAt(350, 250)
			}
			tester.Frame()
			tester.Frame()
			if open || dismissed != 1 {
				t.Fatalf("浮层打开=%v，关闭回调=%d", open, dismissed)
			}
		})
	}
}

func TestMotionControlsDisabledAndLoadingButtons(t *testing.T) {
	for _, loading := range []bool{false, true} {
		t.Run(map[bool]string{false: "禁用", true: "加载"}[loading], func(t *testing.T) {
			clicked := 0
			tester := ui.NewTesterWith(func(c *ui.Context) {
				Button(c, ButtonOpts{Text: "操作", Disabled: !loading, Loading: loading, OnClick: func() { clicked++ }})
			}, 200, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
			if err := tester.Click("操作"); err != nil {
				t.Fatal(err)
			}
			if clicked != 0 {
				t.Fatal("不可操作的按钮触发了点击")
			}
			tester.Frame()
			if tester.FramePending() {
				t.Fatal("不可操作的按钮持续请求帧")
			}
		})
	}
}

func TestMotionControlsSettingTitleWithWideControl(t *testing.T) {
	tester := ui.NewTesterWith(func(c *ui.Context) {
		SettingRow(c, "wide-setting", "设置标题", func() {
			ui.Box(c).Width(300).Height(ControlMD)
		}).FillWidth()
	}, 400, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	title, ok := tester.Find("设置标题")
	if !ok || title.W <= 0 {
		t.Fatalf("宽控件挤没了标题：%+v，存在=%v", title, ok)
	}
}

func TestMotionControlsSheetDragAndResize(t *testing.T) {
	const key = "motion-controls-sheet"
	t.Cleanup(func() { modalDragReset(key) })
	open := true
	tester := ui.NewTester(func(c *ui.Context) {
		testDraggableSheet(c, key, &open, "标题", 500, true, true, nil)
	}, 1200, 1000)
	tester.Advance(time.Second)
	initial, ok := tester.Find("dialog-card-" + key)
	if !ok {
		t.Fatal("未找到弹窗")
	}
	x, y := initial.X+250, initial.Y+30
	tester.Press(x, y)
	tester.Move(x+40, y+30)
	tester.Frame()
	moved, ok := tester.Find("dialog-card-" + key)
	if !ok || moved.X == initial.X || moved.Y == initial.Y || moved.W != initial.W || moved.H != initial.H {
		t.Fatalf("拖动未立即跟随：拖动前=%+v，拖动后=%+v", initial, moved)
	}
	tester.Release(x+40, y+30)
	tester.Frame()
	x, y = moved.X+moved.W-4, moved.Y+moved.H-4
	tester.Press(x, y)
	tester.Move(x+30, y+30)
	tester.Frame()
	resized, ok := tester.Find("dialog-card-" + key)
	if !ok || resized.W != moved.W+30 || resized.H != moved.H+30 {
		t.Fatalf("缩放未立即跟随：缩放前=%+v，缩放后=%+v", moved, resized)
	}
	tester.Release(x+30, y+30)
}
