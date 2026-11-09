package mcodeui

import "github.com/egoist/mygo/ui"

type SheetProps struct {
	Key, PanelKey, PanelLabel, CardKey, Label string
	Open                                      *bool
	Width, Height                             float32
}

func Sheet(c *ui.Context, p SheetProps, body func()) ui.Element {
	if p.Open == nil || !*p.Open {
		return ui.Element{}
	}
	var card ui.Element
	panel := ui.DialogBase(c, p.Open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity)).Transition(motionTransition(c, ui.ElementTransition{
			Duration: DurBase, ExitDuration: DurSnap, Colors: true,
			Enter: &ui.Motion{Opacity: 0}, Exit: &ui.Motion{Opacity: 0},
		}))
		winW, winH := c.Size()
		maxW, maxH := max(0, winW-modalDragPanelInset), max(0, winH-modalDragPanelInset)
		panel.MaxWidth(maxW).MaxHeight(maxH)
		if p.PanelKey != "" {
			panel.Key(p.PanelKey)
		}
		if p.PanelLabel != "" {
			panel.Label(p.PanelLabel)
		}
		ctx := c
		if p.CardKey != "" {
			ctx = c.Key(p.CardKey)
		}
		card = ui.Column(ctx).Radius(RadiusLG).Background(ThemeWindowBg(c)).Border(Hairline, ThemeHairline(c)).MaxWidth(maxW).MaxHeight(maxH).Transition(motionDialog(c))
		if p.Label != "" {
			card.Label(p.Label)
		}
		if p.Width > 0 {
			card.Width(p.Width)
		}
		if p.Height > 0 {
			card.Height(p.Height)
		}
		applyElevation(c, card, ElevDialog).Children(func() {
			if body != nil {
				body()
			}
		})
	})
	modalDragPanel(c, p.Key, panel)
	return card
}
