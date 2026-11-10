package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 现有胶囊迁移对照：
// 头部工具区 → 多按钮容器，需保留组合布局，不能直接替换为单个 Capsule。
// internal/ui/composer_capsules.go:73 renderComposerCapsule → Capsule{Tone: ToneNeutral, Size: ControlMD, Interactive: true, Dismissible: true}；实时状态用 LeadingDot。
// internal/ui/kanban_modal.go:368 kanbanHeaderPill → Capsule{Tone: ToneAccent, Size: ControlSM, Interactive: true}；实心样式需另行处理。
// internal/ui/settings_status.go:16 settingBadge → Badge(..., ToneNeutral)。
// internal/ui/git_pr_ui.go:528 renderGitPRTag → Badge(..., ToneNeutral)。
// internal/ui/chat_header.go:757 renderGitBranchCapsule → Capsule{Tone: ToneNeutral, Size: ControlMD, Interactive: true}；独立无障碍名称需另行处理。
// internal/ui/timeline_rows.go:458 renderUserMessageFileCapsule → Capsule{Tone: ToneNeutral, Size: ControlXS}；原有描边和文件名截断需迁移时处理。

// Tone 定义胶囊的语义色。
type Tone int

const (
	ToneNeutral Tone = iota
	ToneAccent
	ToneSuccess
	ToneWarning
	ToneDanger
)

// CapsuleProps 定义胶囊内容和交互。
type CapsuleProps struct {
	Key         any
	Text        string
	Icon        *ui.SVG
	LeadingDot  *Status
	Tone        Tone
	Size        float32
	Interactive bool
	Selected    bool
	Dismissible bool
	Tooltip     string
	OnClick     func()
	OnDismiss   func()
}

func toneColors(c *ui.Context, tone Tone) (ui.Color, ui.Color, ui.Color) {
	border := ThemeHairline(c)
	switch tone {
	case ToneAccent:
		color := ThemeAccent(c)
		return color, color.Alpha(0), border
	case ToneSuccess:
		color := ThemeSuccess(c)
		return color, color.Alpha(0.1), border
	case ToneWarning:
		color := ThemeWarning(c)
		return color, color.Alpha(0.1), border
	case ToneDanger:
		color := ThemeDanger(c)
		return color, color.Alpha(0.1), border
	default:
		return ThemeSecondaryText(c), ThemeOverlayHover(c), border
	}
}

// capsuleSelectedColors 返回选中胶囊的前景与底色：底色是强调色的低透明叠加，
// 窗口失焦时随强调色一起降级；前景文字保持不变。
func capsuleSelectedColors(c *ui.Context, active bool) (ui.Color, ui.Color) {
	return ThemeAccent(c), inactiveAccentFor(active, ThemeAccentSubtle(c), ThemeTertiaryText(c))
}

// Capsule 渲染带可选图标、状态点和关闭按钮的胶囊。
func Capsule(c *ui.Context, p CapsuleProps) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	height := p.Size
	if height <= 0 {
		height = ControlSM
	}
	font := TypeCaption
	if height >= ControlMD {
		font = TypeFootnote
	}
	fg, bg, _ := toneColors(c, p.Tone)
	if p.Selected {
		fg, bg = capsuleSelectedColors(c, windowActiveFor(c))
	}
	var capsule ui.Element
	if p.Interactive {
		capsule = ui.ButtonBase(ctx).Label(p.Text).OnClick(p.OnClick)
		if capsule.Pressed() {
			bg = ThemeOverlayPressed(c)
		} else if capsule.Hovered() && !p.Selected {
			bg = ThemeOverlayHover(c)
		}
	} else {
		capsule = ui.Row(ctx).Label(p.Text)
	}
	capsule = capsule.Height(height).Radius(RadiusPill).Padding(0, Space2).
		Gap(Space1).AlignItems(ui.Center).Background(bg)
	if p.Tooltip != "" {
		capsule = capsule.Tooltip(p.Tooltip)
	}
	return capsule.Children(func() {
		if p.LeadingDot != nil {
			StatusDot(c, nil, *p.LeadingDot, p.Text)
		} else if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(IconSM, IconSM).TextColor(fg)
		}
		ui.Text(c, p.Text).FontSize(font).TextColor(fg).SingleLine()
		if p.Dismissible {
			close := ui.ButtonBase(c).Label("关闭 "+p.Text).
				Size(ControlSM, ControlSM).Radius(RadiusPill).OnClick(p.OnDismiss)
			if close.Hovered() {
				close.Background(ThemeOverlayHover(c))
			}
			close.Children(func() {
				ui.Icon(c, IconX()).Size(Space4, Space4).TextColor(fg)
			})
		}
	})
}

// Badge 渲染紧凑的方角标记。
func Badge(c *ui.Context, key any, text string, tone Tone) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	fg, bg, _ := toneColors(c, tone)
	return ui.Row(ctx).Label(text).Padding(Space0, Space2).Radius(RadiusXS).
		Background(bg).Children(func() {
		ui.Text(c, text).FontSize(TypeCaption).TextColor(fg).SingleLine()
	})
}

func statusDotColor(c *ui.Context, st Status) ui.Color {
	switch st {
	case StatusRunning:
		return ThemeAccent(c)
	case StatusAttention:
		return ThemeWarning(c)
	case StatusError:
		return ThemeDanger(c)
	default:
		return ThemeTertiaryText(c)
	}
}

// statusDotPulsePeriod 是状态点呼吸的完整周期。
const statusDotPulsePeriod = 1600 * time.Millisecond

// StatusDot 渲染带无障碍名称的四态状态点。
func StatusDot(c *ui.Context, key any, st Status, label string) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	color := statusDotColor(c, st)
	if st == StatusRunning {
		period := animateDuration(c, statusDotPulsePeriod)
		if period > 0 {
			const steps = 8
			step := c.Now().UnixNano() / int64(period/steps) % steps
			phase := float32(step) / steps
			if phase > 0.5 {
				phase = 1 - phase
			}
			color = color.Alpha(0.35 + 1.3*phase)
			c.After(period / steps)
		}
	}
	return ui.Box(ctx).Label(label).Size(Space2, Space2).Radius(RadiusPill).Background(color)
}
