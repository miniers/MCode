package mcodeui

import "github.com/egoist/mygo/ui"

type ButtonVariant int

const (
	ButtonPrimary ButtonVariant = iota
	ButtonSecondary
	ButtonGhost
	ButtonDanger
)

type ButtonOpts struct {
	Key      any
	Text     string
	Icon     *ui.SVG
	Label    string
	Variant  ButtonVariant
	Size     float32
	Disabled bool
	Loading  bool
	OnClick  func()
	Tooltip  string
}

type ButtonProps = ButtonOpts

type IconButtonProps struct {
	Key      any
	Icon     *ui.SVG
	Label    string // 必填；为空时依次使用 Tooltip 和「按钮」。
	Size     float32
	Variant  ButtonVariant
	Active   bool
	Disabled bool
	Loading  bool
	OnClick  func()
	Tooltip  string
}

func (s *chatState) setWindowActive(active bool) {
	if s.windowActive.Swap(active) != active {
		s.invalidate()
	}
}

func windowActiveFor(c *ui.Context) bool {
	if c == nil {
		return true
	}
	services := c.Services()
	if services == (ui.Services{}) {
		return true
	}
	chatWindowsMu.RLock()
	defer chatWindowsMu.RUnlock()
	for _, state := range chatWindows {
		if state.uiServices() == services {
			return state.windowActive.Load()
		}
	}
	return true
}

func buttonSize(size float32) float32 {
	switch size {
	case ControlXS, ControlSM, ControlMD, ControlLG:
		return size
	default:
		return ControlMD
	}
}

// inactiveAccentAlpha 是窗口失焦时强调色的叠加比例。
const inactiveAccentAlpha float32 = 0.18

// inactiveAccentFor 按窗口聚焦状态降级强调色：失焦时退为三级文本色的低透明叠加。
func inactiveAccentFor(active bool, accent, tertiary ui.Color) ui.Color {
	if !active {
		return tertiary.Alpha(inactiveAccentAlpha)
	}
	return accent
}

// inactiveAccent 用当前窗口的聚焦状态降级强调色。
func inactiveAccent(c *ui.Context, accent ui.Color) ui.Color {
	return inactiveAccentFor(windowActiveFor(c), accent, ThemeTertiaryText(c))
}

func buttonPrimaryColor(active bool, accent, tertiary ui.Color) ui.Color {
	return inactiveAccentFor(active, accent, tertiary)
}

const (
	dangerBaseAlpha  = 0.10
	dangerHoverAlpha = 0.16
	dangerPressAlpha = 0.22
)

type buttonColors struct {
	accent, accentHover, accentPressed, tertiary  ui.Color
	overlaySelected, overlayHover, overlayPressed ui.Color
	danger                                        ui.Color
}

func buttonOverlay(base, overlay ui.Color) ui.Color {
	alpha := float32(overlay.A) / 255
	baseAlpha := float32(base.A) / 255
	combined := alpha + baseAlpha*(1-alpha)
	if combined == 0 {
		return ui.Transparent
	}
	color := base.Mix(overlay, alpha/combined)
	color.A = uint8(combined*255 + 0.5)
	return color
}

func buttonBackground(variant ButtonVariant, active, windowActive bool, hover, press float32, colors buttonColors) ui.Color {
	if variant == ButtonPrimary {
		bg := buttonPrimaryColor(windowActive, colors.accent, colors.tertiary)
		if windowActive {
			bg = bg.Mix(colors.accentHover, hover).Mix(colors.accentPressed, press)
		}
		return bg
	}
	bg := ui.Transparent
	if variant == ButtonDanger {
		bg = colors.danger.Alpha(dangerBaseAlpha)
	}
	if active {
		bg = colors.overlaySelected
	}
	if variant == ButtonDanger {
		hoverColor, pressColor := colors.danger.Alpha(dangerHoverAlpha), colors.danger.Alpha(dangerPressAlpha)
		if active {
			hoverColor = buttonOverlay(bg, hoverColor)
			pressColor = buttonOverlay(bg, pressColor)
		}
		return bg.Mix(hoverColor, hover).Mix(pressColor, press)
	}
	return bg.Mix(buttonOverlay(bg, colors.overlayHover), hover).Mix(buttonOverlay(bg, colors.overlayPressed), press)
}

func Button(c *ui.Context, p ButtonOpts) ui.Element {
	return renderButton(c, p, false, false)
}

func IconButton(c *ui.Context, p IconButtonProps) ui.Element {
	if p.Label == "" {
		p.Label = p.Tooltip
		if p.Label == "" {
			p.Label = "按钮"
		}
	}
	return renderButton(c, ButtonOpts{
		Key: p.Key, Icon: p.Icon, Label: p.Label, Size: p.Size,
		Variant: p.Variant, Disabled: p.Disabled, Loading: p.Loading,
		OnClick: p.OnClick, Tooltip: p.Tooltip,
	}, true, p.Active)
}

func renderButton(c *ui.Context, p ButtonOpts, iconOnly, active bool) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	size := buttonSize(p.Size)
	pad, radius, font, iconSize := Space3, RadiusMD, TypeBody, IconMD
	switch size {
	case ControlXS:
		pad, radius, font, iconSize = Space2, RadiusSM, TypeCaption, IconSM
	case ControlSM:
		pad, radius, font, iconSize = Space3, RadiusSM, TypeFootnote, IconSM
	case ControlLG:
		pad = Space4
	}
	label := p.Label
	if label == "" {
		label = p.Text
	}
	// 布局尺寸等于视觉尺寸：底框自身就是绘制矩形，也不再撑大所在行。
	// mygo 没有不占布局的扩展命中区，所以命中区只能等于 Size。
	b := ui.ButtonBase(ctx).Height(size).Radius(radius).Gap(Space2).Label(label)
	if iconOnly {
		b.Width(size)
	} else {
		b.Padding(0, pad)
	}
	if p.Tooltip != "" {
		b.Tooltip(p.Tooltip)
	} else if iconOnly {
		b.Tooltip(label)
	}
	disabled := p.Disabled || p.Loading
	if disabled {
		b.Disabled(true)
	} else if p.OnClick != nil {
		b.OnClick(p.OnClick)
	}

	fg := ThemePrimaryText(c)
	switch p.Variant {
	case ButtonPrimary:
		fg = ThemeOnAccent(c)
	case ButtonDanger:
		fg = ThemeDanger(c)
	}
	hoverTarget, pressTarget := float32(0), float32(0)
	if !disabled && b.Hovered() {
		hoverTarget = 1
	}
	if !disabled && b.Pressed() {
		pressTarget = 1
	}
	hover := b.AnimateTo("button-hover", hoverTarget, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap})
	press := b.AnimateTo("button-press", pressTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseSnap})
	bg := buttonBackground(p.Variant, active, windowActiveFor(c), hover, press, buttonColors{
		accent: ThemeAccent(c), accentHover: ThemeAccentHover(c), accentPressed: ThemeAccentPressed(c),
		tertiary: ThemeTertiaryText(c), overlaySelected: ThemeOverlaySelected(c),
		overlayHover: ThemeOverlayHover(c), overlayPressed: ThemeOverlayPressed(c), danger: ThemeDanger(c),
	})
	if p.Disabled {
		fg = ThemeTertiaryText(c)
	}
	b.Background(bg)
	if p.Variant == ButtonSecondary {
		b.Border(1, ThemeBorder(c))
	}
	if b.FocusVisible() {
		b.Shadow(0, 0, 0, 2, ThemeAccent(c)).Shadow(0, 0, 0, 1, ThemeSurfaceCard(c))
	}
	scale := motionPressScale(c, b, "button-press-scale", !disabled && b.Pressed())
	b.Scale(scale, scale).Children(func() {
		if p.Loading {
			LoadingIndicator(c, nil, "", iconSize, fg)
		} else if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(iconSize, iconSize).TextColor(fg)
		}
		if p.Text != "" {
			ui.Text(c, p.Text).FontSize(font).TextColor(fg)
		}
	})
	return b
}
