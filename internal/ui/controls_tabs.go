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
	background := ui.Transparent
	if p.Active {
		background = ThemeSelected(c)
	} else if btn.Hovered() {
		background = ThemeHover(c)
	}
	feedback, list := motionFeedback(c), motionListItem(c)
	feedback.Enter, feedback.Exit = list.Enter, list.Exit
	feedback.ExitDuration, feedback.ExitEase = list.ExitDuration, list.ExitEase
	btn.Background(background).Transition(feedback)
	btn.Children(func() {
		if body != nil {
			body()
			return
		}
		ui.Text(c, p.Label).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c)).SingleLine()
	})
	return btn
}
