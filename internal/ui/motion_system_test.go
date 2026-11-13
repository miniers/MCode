package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionSystemReduceMotion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reduced bool
		period  time.Duration
		active  bool
	}{
		{"reduced", true, time.Second, false},
		{"normal", false, time.Second, true},
		{"zero period", false, 0, false},
		{"negative period", false, -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setAppReduceMotion(t, tc.reduced)
			var phase float32
			tester := ui.NewTesterWith(func(c *ui.Context) {
				phase = motionLoop(c, ui.Box(c.Key("loop")), "phase", tc.period, ui.Linear)
			}, 100, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: tc.reduced}})
			tester.Frame()
			start := phase
			if tester.FramePending() != tc.active {
				t.Fatalf("待处理帧 = %v，期望 %v", tester.FramePending(), tc.active)
			}
			tester.Advance(200 * time.Millisecond)
			if tc.active {
				if phase == start || !tester.FramePending() {
					t.Fatalf("循环未推进或未续帧：%v -> %v", start, phase)
				}
			} else if phase != 0 || tester.FramePending() {
				t.Fatalf("静态循环不应产生进度或帧：%v", phase)
			}
		})
	}
}

func TestMotionSystemPresets(t *testing.T) {
	setAppReduceMotion(t, true)
	presets := []struct {
		name string
		make func(*ui.Context) ui.ElementTransition
	}{
		{"popover", func(c *ui.Context) ui.ElementTransition { return motionPopover(c, -motionRiseOffset) }},
		{"dialog", motionDialog},
		{"side left", func(c *ui.Context) ui.ElementTransition { return motionSidePanel(c, true) }},
		{"side right", func(c *ui.Context) ui.ElementTransition { return motionSidePanel(c, false) }},
		{"collapse", motionCollapse},
		{"list", motionListItem},
		{"toast", motionToast},
		{"feedback", motionFeedback},
	}
	for _, tc := range presets {
		t.Run(tc.name, func(t *testing.T) {
			show, width := true, float32(40)
			var preset ui.ElementTransition
			tester := ui.NewTesterWith(func(c *ui.Context) {
				preset = tc.make(c)
				ui.Column(c.Key("shell")).Fill().Children(func() {
					if show {
						ui.Box(c.Key("item")).Width(width).Height(20).Label("item").Transition(preset)
					}
				})
			}, 200, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
			if preset.Duration >= 0 || preset.ExitDuration >= 0 || !preset.Colors || preset.Animation != nil || preset.Enter != nil || preset.Exit != nil || preset.Position || preset.Size || preset.Shadows || preset.Width || preset.Height {
				t.Fatalf("减少动效过渡未关闭：%+v", preset)
			}
			if rect, ok := tester.Find("item"); !ok || rect.W != width || rect.H != 20 {
				t.Fatalf("进入时几何不正确：%+v，存在=%v", rect, ok)
			}
			width = 80
			tester.Frame()
			if rect, ok := tester.Find("item"); !ok || rect.W != width || rect.H != 20 {
				t.Fatalf("更新时几何不正确：%+v，存在=%v", rect, ok)
			}
			show = false
			tester.Frame()
			if _, ok := tester.Find("item"); ok {
				t.Fatal("移除后仍存在退出快照")
			}
			tester.Frame()
			if tester.FramePending() {
				t.Fatal("减少动效后仍有待处理帧")
			}
		})
	}
}

func TestMotionSystemPresetFields(t *testing.T) {
	ui.NewTester(func(c *ui.Context) {
		if springSnappy.Duration != 0 || springSnappy.Spring.Duration != DurSheet || springSnappy.Spring.Bounce != 0.15 || springSnappy.NonMotion || springGentle.Duration != 0 || springGentle.Spring.Duration != DurSlow || springGentle.Spring.Bounce != 0 || springGentle.NonMotion {
			t.Error("弹簧预设与约定不符")
		}
		cases := []struct {
			name           string
			got            ui.ElementTransition
			duration, exit time.Duration
			enter, leave   ui.Motion
		}{
			{"popover", motionPopover(c, -4), DurBase, DurFast, ui.Motion{Y: -4, Scale: 0.98}, ui.Motion{Y: -4, Scale: 0.99}},
			{"dialog", motionDialog(c), 0, DurSnap, ui.Motion{Y: 4, Scale: 0.98}, ui.Motion{Y: 4, Scale: 0.99}},
			{"left", motionSidePanel(c, true), DurSheet, DurBase, ui.Motion{XPercent: -100, Opacity: 1}, ui.Motion{XPercent: -100, Opacity: 1}},
			{"right", motionSidePanel(c, false), DurSheet, DurBase, ui.Motion{XPercent: 100, Opacity: 1}, ui.Motion{XPercent: 100, Opacity: 1}},
			{"collapse", motionCollapse(c), DurBase, DurSnap, ui.Motion{Collapse: true}, ui.Motion{Collapse: true}},
			{"list", motionListItem(c), DurBase, DurSnap, ui.Motion{Y: 4, Collapse: true}, ui.Motion{Y: -4, Collapse: true}},
			{"toast", motionToast(c), DurSheet, DurSnap, ui.Motion{Y: 8}, ui.Motion{Y: 4}},
		}
		for _, tc := range cases {
			p := tc.got
			if p.Duration != tc.duration || p.ExitDuration != tc.exit || !p.Colors || p.Position || p.Size || p.Shadows || p.Width || p.Height || p.Enter == nil || p.Exit == nil || *p.Enter != tc.enter || *p.Exit != tc.leave {
				t.Errorf("%s 预设不符：%+v", tc.name, p)
			}
		}
		dialog := motionDialog(c)
		if dialog.Animation == nil || dialog.Animation.Spring.Duration != DurSlow {
			t.Error("dialog 应使用 gentle 弹簧")
		}
		feedback := motionFeedback(c)
		if feedback.Duration != DurSnap || !feedback.Colors || feedback.Enter != nil || feedback.Exit != nil || feedback.Position || feedback.Size || feedback.Width || feedback.Height || feedback.Shadows {
			t.Errorf("feedback 预设不符：%+v", feedback)
		}
		first, second := motionPopover(c, 4), motionPopover(c, 4)
		first.Enter.Y = 99
		if second.Enter.Y != 4 {
			t.Error("预设共享了 Motion 指针")
		}
	}, 100, 100)
}

func TestMotionSystemPreferenceChange(t *testing.T) {
	var phase, enter float32
	tester := ui.NewTester(func(c *ui.Context) {
		e := ui.Box(c.Key("item"))
		phase = motionLoop(c, e, "loop", time.Second, ui.Linear)
		enter = motionEnter(c, e, "enter", staggerLimit)
	}, 100, 100)
	tester.Advance(50 * time.Millisecond)
	if !tester.FramePending() {
		t.Fatal("普通动效应请求帧")
	}
	setAppReduceMotion(t, true)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Frame()
	if phase != 0 || enter != 1 {
		t.Fatalf("切换后未直接到终态：循环=%v，入场=%v", phase, enter)
	}
	tester.Advance(50 * time.Millisecond)
	if tester.FramePending() {
		t.Fatal("切换后仍持续请求帧")
	}
	appReduceMotion.Store(false)
	tester.SetPreferences(ui.Preferences{})
	tester.Frame()
	if !tester.FramePending() {
		t.Fatal("恢复动效后循环应重新请求帧")
	}
}

func TestMotionSystemEnterStagger(t *testing.T) {
	var first, negative, limited, capped float32
	tester := ui.NewTester(func(c *ui.Context) {
		first = motionEnter(c, ui.Box(c.Key("first")), "enter", 0)
		negative = motionEnter(c, ui.Box(c.Key("negative")), "enter", -1)
		limited = motionEnter(c, ui.Box(c.Key("limited")), "enter", staggerLimit)
		capped = motionEnter(c, ui.Box(c.Key("capped")), "enter", staggerLimit+10)
	}, 100, 100)
	tester.Advance(DurBase / 2)
	if first <= 0 || first >= 1 || negative != first || limited != capped || limited != 0 {
		t.Fatalf("交错入场进度不符：首项=%v，负数=%v，上限=%v，超限=%v", first, negative, limited, capped)
	}
	tester.Advance(time.Duration(staggerLimit)*staggerStep + DurBase)
	if first != 1 || negative != 1 || limited != 1 || capped != 1 || tester.FramePending() {
		t.Fatalf("交错入场未结束：%v, %v, %v, %v", first, negative, limited, capped)
	}
}

func TestMotionSystemCustomTransitions(t *testing.T) {
	ui.NewTester(func(c *ui.Context) {
		for _, original := range []ui.ElementTransition{
			{Duration: DurSnap, Colors: true},
			{ExitDuration: DurBase, Colors: true, Exit: &ui.Motion{Y: 4}},
		} {
			got := motionTransition(c, original)
			if got.Duration != original.Duration || got.ExitDuration != original.ExitDuration || got.Colors != original.Colors || got.Exit != original.Exit {
				t.Errorf("正常模式应保留自定义过渡：%+v", got)
			}
		}
	}, 100, 100)
	setAppReduceMotion(t, true)
	ui.NewTesterWith(func(c *ui.Context) {
		original := ui.ElementTransition{Animation: &springSnappy, Position: true, Exit: &ui.Motion{Opacity: 0}}
		got := motionTransition(c, original)
		if got.Duration >= 0 || got.ExitDuration >= 0 || !got.Colors || got.Position || got.Animation != nil || got.Enter != nil || got.Exit != nil {
			t.Errorf("减少动效应清除自定义过渡：%+v", got)
		}
	}, 100, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
}

// 退出快照在移除那一帧按当时的过渡创建，退出途中切换偏好由 mygo 运行时处理，不在本测试覆盖范围内。
func TestMotionSystemTransitionPreferenceChange(t *testing.T) {
	show := false
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("shell")).Fill().Children(func() {
			if show {
				ui.Box(c.Key("item")).Width(40).Height(20).Label("item").Transition(motionPopover(c, 4))
			}
		})
	}, 200, 100)
	show = true
	tester.Frame()
	tester.Advance(DurBase + 50*time.Millisecond)
	if rect, ok := tester.Find("item"); !ok || rect.W != 40 || rect.H != 20 {
		t.Fatalf("进入后几何不正确：%+v，存在=%v", rect, ok)
	}
	setAppReduceMotion(t, true)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Frame()
	show = false
	tester.Frame()
	tester.Advance(50 * time.Millisecond)
	if _, ok := tester.Find("item"); ok {
		t.Fatal("切换减少动效后退出快照仍存在")
	}
	if tester.FramePending() {
		t.Fatal("切换减少动效后退出动画仍持续排帧")
	}
}

func TestMotionSystemScalars(t *testing.T) {
	changed := false
	var press, hover, progress, fade float32
	tester := ui.NewTester(func(c *ui.Context) {
		e := ui.Box(c.Key("scalar"))
		press = motionPressScale(c, e, "press", true)
		hover = motionHoverScale(c, e, "hover", true)
		progress, fade = motionNumberProgress(c, e, changed)
	}, 100, 100)
	tester.Advance(time.Second)
	changed = true
	tester.Frame()
	if progress != 0 || fade != 0 {
		t.Fatalf("数字变化应重置通道：%v, %v", progress, fade)
	}
	changed = false
	tester.Advance(motionRoll / 2)
	if progress <= 0 || progress >= 1 || fade <= 0 {
		t.Fatalf("数字应继续滚动：%v, %v", progress, fade)
	}
	setAppReduceMotion(t, true)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	if press != 1 || hover != 1 || progress != 1 || fade != 1 {
		t.Fatalf("减少动效标量未到终态：%v, %v, %v, %v", press, hover, progress, fade)
	}
	appReduceMotion.Store(false)
	tester.SetPreferences(ui.Preferences{})
	if progress != 1 || fade != 1 {
		t.Fatalf("恢复偏好后旧数字不应重播：%v, %v", progress, fade)
	}
}

func TestMotionSystemReducedUnfold(t *testing.T) {
	setAppReduceMotion(t, true)
	open := false
	tester := ui.NewTesterWith(func(c *ui.Context) {
		motionUnfoldBody(c, "unfold", open, func() { ui.Text(c, "body") })
	}, 200, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	open = true
	tester.Frame()
	if rect, ok := tester.Find("body"); !ok || rect.H <= 0 {
		t.Fatalf("展开应立即显示内容：%+v，存在=%v", rect, ok)
	}
	open = false
	tester.Frame()
	if _, ok := tester.Find("body"); ok {
		t.Fatal("收起应立即销毁内容")
	}
}
