package mcodeui

import (
	"github.com/egoist/mygo/ui"
)

const (
	// onboardingNodeSize 是插画节点与完成页勾选圆的直径。
	onboardingNodeSize = Space8 + Space5
	// onboardingCelebrationSize 是完成页勾选外框的尺寸。
	onboardingCelebrationSize = Space8 + Space7
	// onboardingLinkHeight 是插画节点连线的粗细。
	onboardingLinkHeight = Space0
)

// onboardingEntry 为列表项提供交错淡入和升起效果。
func onboardingEntry(c *ui.Context, el ui.Element, index int) {
	k := motionEnter(c, el, "entry", index)
	el.Translate(0, (1-k)*motionRiseOffset).Opacity(k)
}

// onboardingPulse 用框架关键帧实现只在元素可见时运行的循环动效。
func onboardingPulse(c *ui.Context, el ui.Element, alternate bool) float32 {
	if reduceMotion(c) {
		return 0
	}
	if !alternate {
		return motionLoop(c, el, "pulse", motionSweep, ui.Linear)
	}
	return el.Keyframes("pulse", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, motionSweep), Value: 1, Ease: ui.Linear}}, ui.KeyframeOptions{Repeat: ui.RepeatForever, Alternate: alternate})
}

// renderOnboardingHero 展示引导首页的两个连接节点。
func renderOnboardingHero(c *ui.Context) {
	ui.Row(c.Key("onboarding-illustration")).FillWidth().AlignItems(ui.Center).Children(func() {
		for i, label := range []string{"mcode", "Magpie"} {
			if i > 0 {
				line := ui.Box(c.Key("onboarding-link-" + label)).Grow(1).Height(onboardingLinkHeight).Background(ThemeBorder(c))
				line.Children(func() {
					dot := ui.Box(c.Key("onboarding-light-" + label)).Width(Space2).Height(Space2).Radius(RadiusPill).Background(ThemeAccent(c))
					k := onboardingPulse(c, dot, false)
					width := line.LayoutBounds().W
					dot.Translate(k*max(float32(0), width-Space2), -Space2/2)
				})
			}
			ui.Column(c.Key("onboarding-node-" + label)).AlignItems(ui.Center).Gap(Space2).Children(func() {
				node := ui.Row(c.Key("onboarding-node-icon-" + label)).Width(onboardingNodeSize).Height(onboardingNodeSize).Radius(RadiusPill).AlignItems(ui.Center).Justify(ui.Center)
				glow := onboardingPulse(c, node, true)
				node.Background(ThemeAccent(c).Alpha(0.08+0.1*glow)).Border(Hairline, ThemeAccent(c).Alpha(0.25+0.3*glow)).Children(func() {
					if label == "mcode" {
						renderProviderRowIcon(c, "mcode", IconMD)
					} else if label == "Magpie" {
						ui.Icon(c, IconNetwork()).Size(IconMD, IconMD).TextColor(ThemeAccent(c))
					} else {
						ui.Icon(c, IconSparkles()).Size(IconMD, IconMD).TextColor(ThemeAccent(c))
					}
				})
				ui.Text(c, label).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			})
		}
	})
}

// onboardingBadge 为状态变化提供淡入，并为检查中状态显示脉冲。
func onboardingBadge(c *ui.Context, key, text string, tone Tone) {
	row := ui.Row(c.Key(key + "-" + text)).Gap(Space1).AlignItems(ui.Center)
	if !reduceMotion(c) {
		k := row.Keyframes("fade", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, motionIconFade), Value: 1, Ease: ui.Linear}})
		row.Opacity(k)
	}
	row.Children(func() {
		if text == "检查中" {
			dot := ui.Box(c.Key(key + "-checking")).Width(Space2).Height(Space2).Radius(RadiusPill).Background(ThemeAccent(c))
			dot.Opacity(0.4 + 0.6*onboardingPulse(c, dot, true))
		}
		Badge(c, nil, text, tone)
	})
}
