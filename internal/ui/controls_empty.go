package mcodeui

import "github.com/egoist/mygo/ui"

type EmptyStateProps struct {
	Key                any
	Icon               *ui.SVG
	Title, Description string
	Compact            bool
	Action             func()
}

func EmptyState(c *ui.Context, p EmptyStateProps) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	col := ui.Column(ctx).AlignItems(ui.Center).Gap(Space3)
	col.Children(func() {
		if p.Icon != nil {
			ui.Icon(c, p.Icon).
				Size(diffStatusIconSize, diffStatusIconSize).
				TextColor(ThemeSecondaryText(c).Alpha(0.40))
		}
		font := TypeTitle
		if p.Compact {
			font = TypeFootnote
		}
		ui.Text(c, p.Title).FontSize(font).FontWeight(500).TextColor(ThemePrimaryText(c))
		if p.Description != "" {
			ui.Text(c, p.Description).
				FontSize(TypeFootnote).
				MaxWidth(diffStatusSubtitleMaxWidth).
				TextAlign(ui.Center).
				Wrap().
				TextColor(ThemeSecondaryText(c))
		}
		if p.Action != nil {
			p.Action()
		}
	})
	return col
}
