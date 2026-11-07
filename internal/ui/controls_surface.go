package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// SectionCard 渲染设置分组及其内容。
func SectionCard(c *ui.Context, key any, title string, body func()) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	return ui.Column(ctx).FillWidth().Radius(RadiusLG).Background(ThemeSurfaceCard(c)).
		Border(Space1/4, ThemeBorder(c)).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(TypeFootnote).FontWeight(600).
				TextColor(ThemeSecondaryText(c)).Margin(Space4, Space4, 0, Space4)
		}
		if body != nil {
			body()
		}
	})
}

// PanelProps 定义工作区面板的标题和操作。
type PanelProps struct {
	Key      any
	Title    string
	Subtitle string
	Toolbar  func()
	Footer   func()
	Scroll   bool
	OnClose  func()
}

// WorkspacePanel 渲染带标题栏和可选底栏的工作区。
func WorkspacePanel(c *ui.Context, p PanelProps, body func()) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	return ui.Column(ctx).Fill().Background(ThemeSurfaceMain(c)).Children(func() {
		ui.Row(c).FillWidth().MinHeight(ControlLG+Space3).Padding(0, Space4).AlignItems(ui.Center).Gap(Space3).Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, p.Title).FontSize(TypeTitle).TextColor(ThemePrimaryText(c))
				if p.Subtitle != "" {
					ui.Text(c, p.Subtitle).FontSize(TypeFootnote).TextColor(ThemeTertiaryText(c))
				}
			})
			if p.Toolbar != nil {
				p.Toolbar()
			}
			if p.OnClose != nil {
				IconButton(c, IconButtonProps{Icon: IconX(), Label: "关闭", Variant: ButtonGhost, OnClick: p.OnClose})
			}
		})
		ui.Box(c).FillWidth().Height(Hairline).Background(ThemeHairline(c))
		content := ui.Column(c).FillWidth().Grow(1).Padding(Space5)
		if p.Scroll {
			content = ui.Scroll(c).FillWidth().Grow(1).Padding(Space5)
		}
		if body != nil {
			content.Children(body)
		}
		if p.Footer != nil {
			ui.Box(c).FillWidth().Height(Hairline).Background(ThemeHairline(c))
			ui.Row(c).FillWidth().Padding(Space4).Children(p.Footer)
		}
	})
}

// BannerProps 定义提示条内容和可选操作。
type BannerProps struct {
	Key      any
	Tone     Tone
	Text     string
	Action   string
	OnAction func()
	OnClose  func()
}

type bannerPalette struct{ info, warning, danger, success ui.Color }

func bannerToneColor(tone Tone, colors bannerPalette) ui.Color {
	switch tone {
	case ToneWarning:
		return colors.warning
	case ToneDanger:
		return colors.danger
	case ToneSuccess:
		return colors.success
	default:
		return colors.info
	}
}

// Banner 渲染语义提示及其可选操作。
func Banner(c *ui.Context, p BannerProps) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	color := bannerToneColor(p.Tone, bannerPalette{ThemeAccent(c), ThemeWarning(c), ThemeDanger(c), ThemeSuccess(c)})
	icon := IconInfo()
	if p.Tone == ToneSuccess {
		icon = IconCheck()
	}
	return ui.Row(ctx).FillWidth().Radius(RadiusMD).Padding(Space3, Space4).Gap(Space3).
		AlignItems(ui.Center).Background(color.Alpha(0.1)).Border(Space1/4, color.Alpha(0.2)).Children(func() {
		ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color)
		ui.Text(c, p.Text).Grow(1).FontSize(TypeBody).TextColor(ThemePrimaryText(c))
		if p.Action != "" {
			Button(c, ButtonOpts{Text: p.Action, Variant: ButtonGhost, Size: ControlSM, OnClick: p.OnAction})
		}
		if p.OnClose != nil {
			IconButton(c, IconButtonProps{Icon: IconX(), Label: "关闭", Variant: ButtonGhost, Size: ControlSM, OnClick: p.OnClose})
		}
	})
}

func skeletonFill(progress float32, reduced bool) float32 {
	if reduced {
		return 0.35
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	return 0.35 * (0.55 + 0.45*progress)
}

func skeleton(c *ui.Context, key any, width, height float32) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	el := ui.Box(ctx).Height(height).Radius(RadiusMD)
	if width > 0 {
		el = el.Width(width)
	} else {
		el = el.FillWidth()
	}
	period := animateDuration(c, motionSweep)
	progress := float32(0)
	if period > 0 {
		progress = el.Keyframes("skeleton-pulse", []ui.Keyframe{
			{At: 0, Value: 0, Ease: EaseSnap},
			{At: period, Value: 1, Ease: EaseSnap},
		}, ui.KeyframeOptions{Repeat: ui.RepeatForever, Alternate: true})
	}
	fill := ThemeOverlayHover(c).Alpha(skeletonFill(progress, period == 0))
	return el.Background(buttonOverlay(ThemeSurfaceCard(c), fill))
}

// SkeletonLine 渲染指定宽高的骨架行。
func SkeletonLine(c *ui.Context, key any, width, height float32) ui.Element {
	return skeleton(c, key, width, height)
}

// SkeletonBlock 渲染占满宽度的骨架块。
func SkeletonBlock(c *ui.Context, key any, height float32) ui.Element {
	return skeleton(c, key, 0, height)
}

func tooltipText(text, shortcut string) string {
	return strings.TrimSpace(strings.TrimSpace(text) + " " + strings.TrimSpace(shortcut))
}

// Tooltip 统一原生提示文字和快捷键的拼接方式。
func Tooltip(el ui.Element, text string, shortcut string) ui.Element {
	return el.Tooltip(tooltipText(text, shortcut))
}
