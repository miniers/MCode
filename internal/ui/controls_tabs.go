package mcodeui

import "github.com/egoist/mygo/ui"

type TabButtonProps struct {
	Key     any
	Label   string
	Tooltip string
	Active  bool
}

func TabButton(c *ui.Context, p TabButtonProps, body func()) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	btn := ui.ButtonBase(ctx).Label(p.Label).Radius(RadiusSM).Padding(Space1, Space2).Shrink(0)
	if p.Tooltip != "" {
		btn.Tooltip(p.Tooltip)
	}
	selected, hovered := float32(0), float32(0)
	if p.Active {
		selected = 1
	} else if btn.Hovered() {
		hovered = 1
	}
	animation := ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap}
	selected = btn.AnimateTo("tab-selected", selected, animation)
	hovered = btn.AnimateTo("tab-hover", hovered, animation)
	btn.Background(ui.Transparent.Mix(ThemeHover(c), hovered).Mix(ThemeSelected(c), selected))
	btn.Children(func() {
		if body != nil {
			body()
			return
		}
		ui.Text(c, p.Label).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c)).SingleLine()
	})
	return btn
}
