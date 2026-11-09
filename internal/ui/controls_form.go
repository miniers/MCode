package mcodeui

import "github.com/egoist/mygo/ui"

func FormActions(c *ui.Context, key any, body func()) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	return ui.Row(ctx).FillWidth().Justify(ui.End).AlignItems(ui.Center).Gap(Space3).Children(body)
}
