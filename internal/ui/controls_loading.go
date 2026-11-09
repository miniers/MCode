package mcodeui

import "github.com/egoist/mygo/ui"

func LoadingIndicator(c *ui.Context, key any, text string, size float32) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	row := ui.Row(ctx).AlignItems(ui.Center)
	if text != "" {
		row.Label(text)
		row.Gap(Space3)
	}
	row.Children(func() {
		if reduceMotion(c) {
			dot := ui.Box(c).Size(size, size).Justify(ui.Center).AlignItems(ui.Center)
			if text == "" {
				dot.Label("加载中")
			}
			dot.Children(func() {
				ui.Box(c).Size(size/3, size/3).Radius(size/6).Background(ThemeSecondaryText(c))
			})
		} else {
			ui.Spinner(c).Size(size, size).TextColor(ThemeAccent(c)).Label("加载中")
		}
		if text != "" {
			ui.Text(c, text).FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
		}
	})
	return row
}
