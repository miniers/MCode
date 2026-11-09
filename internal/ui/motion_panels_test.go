package mcodeui

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionPanelsQuotaBounds(t *testing.T) {
	color := ui.Color{R: 1, G: 2, B: 3, A: 255}
	for _, tc := range []struct {
		outer, inner float64
		wantOuter, wantInner float64
	}{
		{0, 1, 0, 1},
		{-1, 2, 0, 1},
		{2, -1, 1, 0},
	} {
		got := makeQuotaRingSVG(false, tc.outer, tc.inner, true, true, color, color)
		want := makeQuotaRingSVG(false, tc.wantOuter, tc.wantInner, true, true, color, color)
		if got != want {
			t.Fatalf("额度圆环未限制在 0 到 1：%v, %v", tc.outer, tc.inner)
		}
	}
}

func TestMotionPanelsQuotaReducedJump(t *testing.T) {
	used := float64(0)
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	actual := ui.NewTesterWith(func(c *ui.Context) {
		renderComposerQuotaRing(c, "motion-test", "额度", &quotaRingWindow{used: used}, nil)
	}, 40, 40, options)
	for _, used = range []float64{0, 100, 0, 100} {
		actual.Frame()
		expected := ui.NewTesterWith(func(c *ui.Context) {
			color := quotaLevelColor(c, used)
			ui.Box(c.Key("expected")).Size(IconMD, IconMD).Children(func() {
				ui.Image(c, makeQuotaRingSVG(isDark(c), used/100, 0, true, false, color, ThemeSecondaryText(c))).Size(IconMD, IconMD).Fit(ui.Contain)
			})
		}, 40, 40, options)
		expected.Frame()
		if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
			t.Fatalf("额度 %v%% 未立即显示终值", used)
		}
		if actual.FramePending() {
			t.Fatal("减少动效时额度圆环仍请求帧")
		}
	}
}

func TestMotionPanelsReducedFeedback(t *testing.T) {
	x := float32(0)
	color := ui.Color{R: 255, A: 255}
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	actual := ui.NewTesterWith(func(c *ui.Context) {
		ui.Box(c.Key("item")).Absolute().Left(x).Top(0).Size(20, 20).Background(color).Label("item").Transition(motionFeedback(c))
	}, 100, 40, options)
	x, color = 50, ui.Color{B: 255, A: 255}
	actual.Frame()
	expected := ui.NewTesterWith(func(c *ui.Context) {
		ui.Box(c.Key("item")).Absolute().Left(x).Top(0).Size(20, 20).Background(color).Label("item")
	}, 100, 40, options)
	expected.Frame()
	if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
		t.Fatal("颜色或位置未立即到达终态")
	}
	if actual.FramePending() {
		t.Fatal("减少动效时反馈仍请求帧")
	}
}

func TestMotionPanelsModelPageReduced(t *testing.T) {
	depth, selected := 0, float32(0)
	state := &ModelPickerState{}
	var slide float32
	tester := ui.NewTesterWith(func(c *ui.Context) {
		page := ui.Column(c.Key("page"))
		modelPickerPageTransition(c, page, state, depth)
		page.Children(func() { ui.Text(c, "模型页") })
		slide = modelPickerTabSlide(c, ui.Box(c.Key("tab")), selected)
	}, 200, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	for _, d := range []int{1, 2, 1, 0} {
		depth = d
		selected = 1 - selected
		tester.Frame()
		if _, ok := tester.Find("模型页"); !ok || slide != selected {
			t.Fatalf("模型页或标签未到终态：depth=%d, slide=%v", depth, slide)
		}
		if tester.FramePending() {
			t.Fatal("减少动效时模型页仍请求帧")
		}
	}
}

func TestMotionPanelsModelPageRapidReverse(t *testing.T) {
	depth, selected := 0, float32(0)
	state := &ModelPickerState{}
	var slide float32
	tester := ui.NewTester(func(c *ui.Context) {
		page := ui.Column(c.Key("page"))
		modelPickerPageTransition(c, page, state, depth)
		page.Children(func() { ui.Text(c, "模型页") })
		slide = modelPickerTabSlide(c, ui.Box(c.Key("tab")), selected)
	}, 200, 100)
	depth, selected = 1, 1
	tester.Frame()
	tester.Advance(DurFast)
	depth, selected = 0, 0
	tester.Frame()
	tester.Advance(time.Second)
	if _, ok := tester.Find("模型页"); !ok || slide < -0.001 || slide > 0.001 {
		t.Fatalf("快速返回后模型页或标签未到终态：slide=%v", slide)
	}
	if tester.FramePending() {
		t.Fatal("模型页动效结束后仍请求帧")
	}
}

func TestMotionPanelsSwarmReduced(t *testing.T) {
	const lead = "motion-panels-test"
	key := lead + "|" + string(teamPanelID)
	defer func() {
		swarmFloatMu.Lock()
		delete(swarmDragPos, key)
		swarmFloatMu.Unlock()
	}()
	layout := panelLayout{paneW: 800, paneH: 600}
	frame := swarmFloatFrame{leadID: lead, scene: swarmScene{
		layout: layout,
		slots: map[panelID]panelSlot{teamPanelID: {corner: cornerTopLeading}},
		slotsOnSide: map[panelSide]int{panelSideLeading: 1},
		docks: newPanelDocks(layout, panelReserve{}),
	}}
	show := true
	s := &chatState{}
	tester := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c.Key("shell")).Fill().Children(func() {
			if show {
				renderSwarmPanelCard(c, s, frame, teamPanelID, nil)
			}
		})
	}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	initial, ok := tester.Find("编排")
	if !ok {
		t.Fatal("浮窗未显示")
	}
	swarmFloatMu.Lock()
	swarmDragPos[key] = panelPoint{x: 100, y: 150}
	swarmFloatMu.Unlock()
	tester.Frame()
	dragged, ok := tester.Find("编排")
	if !ok || dragged.X == initial.X || dragged.Y == initial.Y {
		t.Fatal("拖动时浮窗未跟随位置")
	}
	swarmFloatMu.Lock()
	delete(swarmDragPos, key)
	swarmFloatMu.Unlock()
	tester.Frame()
	rest, ok := tester.Find("编排")
	if !ok || rest.X != initial.X || rest.Y != initial.Y {
		t.Fatal("松手后浮窗未立即回到停靠位置")
	}
	show = false
	tester.Frame()
	tester.Advance(time.Millisecond)
	if _, ok := tester.Find("编排"); ok {
		t.Fatal("关闭后浮窗仍然可见")
	}
	if tester.FramePending() {
		t.Fatal("关闭浮窗后仍请求帧")
	}
}
