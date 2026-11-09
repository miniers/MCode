package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

var (
	springSnappy = ui.Animation{Spring: ui.SpringOptions{Duration: DurSheet, Bounce: 0.15}}
	springGentle = ui.Animation{Spring: ui.SpringOptions{Duration: DurSlow, Bounce: 0}}
)

func motionTransition(c *ui.Context, t ui.ElementTransition) ui.ElementTransition {
	if reduceMotion(c) {
		return ui.ElementTransition{Duration: -time.Nanosecond, ExitDuration: -time.Nanosecond, Colors: true}
	}
	return t
}

func motionPressScale(c *ui.Context, e ui.Element, key any, pressed bool) float32 {
	if reduceMotion(c) {
		return 1
	}
	target := float32(1)
	if pressed {
		target = 0.975
	}
	return e.AnimateTo(key, target, springSnappy)
}

func motionHoverScale(c *ui.Context, e ui.Element, key any, hovered bool) float32 {
	if reduceMotion(c) {
		return 1
	}
	target := float32(1)
	if hovered {
		target = 1.015
	}
	return e.AnimateTo(key, target, springGentle)
}

func motionNumberProgress(c *ui.Context, e ui.Element, changed bool) (progress, fade float32) {
	if reduceMotion(c) {
		e.Jump("roll", 1)
		e.Jump("fade", 1)
		return 1, 1
	}
	if changed {
		e.Jump("roll", 0)
		e.Jump("fade", 0)
	}
	return e.AnimateTo("roll", 1, ui.Animation{Duration: motionRoll, Ease: motionEaseEmphasis}),
		e.AnimateTo("fade", 1, ui.Animation{Duration: motionRollFade, Ease: ui.Linear})
}

func motionLoop(c *ui.Context, e ui.Element, key any, period time.Duration, ease ui.Easing) float32 {
	if reduceMotion(c) || period <= 0 {
		return 0
	}
	return e.Loop(key, period, ease)
}

func motionEnter(c *ui.Context, e ui.Element, key any, index int) float32 {
	if reduceMotion(c) {
		return 1
	}
	return e.Keyframes(key, []ui.Keyframe{{Value: 0}, {At: DurBase, Value: 1, Ease: EaseSnap}},
		ui.KeyframeOptions{Delay: staggerDelayFor(c, min(max(index, 0), staggerLimit), staggerStep)})
}

func motionPopover(c *ui.Context, offsetY float32) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{
		Duration: DurBase, Ease: EaseSnap, ExitDuration: DurFast, ExitEase: EaseOut, Colors: true,
		Enter: &ui.Motion{Y: offsetY, Opacity: 0, Scale: 0.98},
		Exit:  &ui.Motion{Y: offsetY, Opacity: 0, Scale: 0.99},
	})
}

func motionDialog(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{
		Animation: &springGentle, ExitDuration: DurSnap, ExitEase: EaseOut, Colors: true,
		Enter: &ui.Motion{Y: motionRiseOffset, Opacity: 0, Scale: 0.98},
		Exit:  &ui.Motion{Y: motionRiseOffset, Opacity: 0, Scale: 0.99},
	})
}

func motionSidePanel(c *ui.Context, fromLeft bool) ui.ElementTransition {
	x := float32(100)
	if fromLeft {
		x = -100
	}
	return motionTransition(c, ui.ElementTransition{
		Duration: DurSheet, Ease: EaseSnap, ExitDuration: DurBase, ExitEase: EaseOut, Colors: true,
		Enter: &ui.Motion{XPercent: x, Opacity: 1},
		Exit:  &ui.Motion{XPercent: x, Opacity: 1},
	})
}

func motionCollapse(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{
		Duration: DurBase, Ease: EaseOut, ExitDuration: DurSnap, ExitEase: EaseOut, Colors: true,
		Enter: &ui.Motion{Collapse: true, Opacity: 0},
		Exit:  &ui.Motion{Collapse: true, Opacity: 0},
	})
}

func motionListItem(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{
		Duration: DurBase, Ease: EaseSnap, ExitDuration: DurSnap, ExitEase: EaseOut, Colors: true,
		Enter: &ui.Motion{Y: motionRiseOffset, Opacity: 0, Collapse: true},
		Exit:  &ui.Motion{Y: -motionRiseOffset, Opacity: 0, Collapse: true},
	})
}

func motionToast(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{
		Duration: DurSheet, Ease: EaseOut, ExitDuration: DurSnap, ExitEase: EaseOut, Colors: true,
		Enter: &ui.Motion{Y: 2 * motionRiseOffset, Opacity: 0},
		Exit:  &ui.Motion{Y: motionRiseOffset, Opacity: 0},
	})
}

func motionFeedback(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{Duration: DurSnap, Ease: EaseSnap, Colors: true})
}
