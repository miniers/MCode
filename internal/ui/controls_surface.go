package mcodeui

import "github.com/egoist/mygo/ui"

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

// BannerProps 定义提示条内容和可选操作。
type BannerProps struct {
	Key      any
	Tone     Tone
	Text     string
	Action   string
	OnAction func()
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
	})
}
