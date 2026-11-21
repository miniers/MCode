package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	// onboardingNodeSize 是插画节点的直径。
	onboardingNodeSize = Space8 + Space5
	// onboardingLinkHeight 是插画节点连线的粗细。
	onboardingLinkHeight = Space0
	// onboardingHeroFrameInterval 是欢迎页 Draw 重画的帧间隔（约 30fps）。
	onboardingHeroFrameInterval = 33 * time.Millisecond
)

// onboardingEntry 为列表项提供交错淡入和升起效果。
func onboardingEntry(c *ui.Context, el ui.Element, index int) {
	k := motionEnter(c, el, "entry", index)
	el.Translate(0, (1-k)*motionRiseOffset).Opacity(k)
}

// onboardingPulsePhase 按绘制时间计算循环动效相位：单向在 [0, 1) 内按 motionSweep 线性前进，
// alternate 为真时在 0→1→0 之间往返，每个半程时长为 motionSweep。
func onboardingPulsePhase(now time.Time, alternate bool) float32 {
	if motionSweep <= 0 {
		return 0
	}
	period := int64(motionSweep)
	if alternate {
		phase := now.UnixNano() % (2 * period)
		if phase < 0 {
			phase += 2 * period
		}
		k := float32(phase) / float32(period)
		if k > 1 {
			return 2 - k
		}
		return k
	}
	phase := now.UnixNano() % period
	if phase < 0 {
		phase += period
	}
	return float32(phase) / float32(period)
}

// renderOnboardingHero 展示引导首页的两个连接节点。
func renderOnboardingHero(c *ui.Context) {
	accent := ThemeAccent(c)
	border := ThemeBorder(c)
	reduced := reduceMotion(c)
	ui.Row(c.Key("onboarding-illustration")).FillWidth().AlignItems(ui.Center).Children(func() {
		for i, label := range []string{"mcode", "Magpie"} {
			if i > 0 {
				ui.Box(c.Key("onboarding-link-" + label)).Grow(1).Height(Space2).Draw(func(p *ui.Painter, r ui.Rect) {
					lineRect := ui.Rect{X: r.X, Y: r.Y + (r.H-onboardingLinkHeight)/2, W: r.W, H: onboardingLinkHeight}
					p.Fill(lineRect, border, 0)
					k := float32(0)
					if !reduced {
						k = onboardingPulsePhase(p.Now(), false)
						p.After(onboardingHeroFrameInterval)
					}
					dotRect := ui.Rect{
						X: r.X + k*max(float32(0), r.W-Space2),
						Y: r.Y + (r.H-Space2)/2,
						W: Space2,
						H: Space2,
					}
					p.Fill(dotRect, accent, Space2/2)
				})
			}
			ui.Column(c.Key("onboarding-node-" + label)).AlignItems(ui.Center).Gap(Space2).Children(func() {
				node := ui.Row(c.Key("onboarding-node-icon-" + label)).Width(onboardingNodeSize).Height(onboardingNodeSize).Radius(RadiusPill).AlignItems(ui.Center).Justify(ui.Center)
				node.Draw(func(p *ui.Painter, r ui.Rect) {
					glow := float32(0)
					if !reduced {
						glow = onboardingPulsePhase(p.Now(), true)
						p.After(onboardingHeroFrameInterval)
					}
					p.Fill(r, accent.Alpha(0.08+0.1*glow), r.H/2)
					p.Stroke(r, accent.Alpha(0.25+0.3*glow), r.H/2, Hairline)
				})
				node.Children(func() {
					if label == "mcode" {
						renderProviderRowIcon(c, "mcode", IconMD)
					} else if label == "Magpie" {
						ui.Icon(c, IconNetwork()).Size(IconMD, IconMD).TextColor(accent)
					} else {
						ui.Icon(c, IconSparkles()).Size(IconMD, IconMD).TextColor(accent)
					}
				})
				ui.Text(c, label).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
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
			accent := ThemeAccent(c)
			reduced := reduceMotion(c)
			ui.Box(c.Key(key + "-checking")).Width(Space2).Height(Space2).Radius(RadiusPill).Draw(func(p *ui.Painter, r ui.Rect) {
				glow := float32(0)
				if !reduced {
					glow = onboardingPulsePhase(p.Now(), true)
					p.After(onboardingHeroFrameInterval)
				}
				p.Fill(r, accent.Alpha(0.4+0.6*glow), r.H/2)
			})
		}
		Badge(c, nil, text, tone)
	})
}
