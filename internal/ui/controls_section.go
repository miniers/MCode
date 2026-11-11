package mcodeui

import "github.com/egoist/mygo/ui"

func SectionHeading(c *ui.Context, key any, title string) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	return ui.Text(ctx, title).FontSize(uiFont(TypeCaption)).FontWeight(500).TextColor(ThemeTertiaryText(c))
}
