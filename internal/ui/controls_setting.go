package mcodeui

import "github.com/egoist/mygo/ui"

func SettingRow(c *ui.Context, key, title string, control func()) ui.Element {
	return settingRow(c, key, title, false, nil, control)
}

func StackedSettingRow(c *ui.Context, key, title string, control func()) ui.Element {
	return settingRow(c, key, title, true, nil, control)
}

func settingRow(c *ui.Context, key, title string, stacked bool, decorate func(ui.Element) ui.Element, control func()) ui.Element {
	var row ui.Element
	if stacked {
		row = ui.Column(c.Key(key)).Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight).Gap(Space4)
	} else {
		row = ui.Row(c.Key(key)).Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight).Gap(Space4).AlignItems(ui.Center)
	}
	if decorate != nil {
		row = decorate(row)
	}
	row.Children(func() {
		if !stacked {
			settingRowContent(c, key, title, control)
			return
		}
		ui.Column(c.Key(key + "-header")).Gap(Space1).Children(func() {
			titleEl := ui.Text(c, title).FontSize(uiFont(TypeBody)).FontWeight(400).TextColor(ThemePrimaryText(c)).SingleLine()
			if h := settingHint(key); h != "" {
				titleEl.Tooltip(h)
			}
		})
		ui.Row(c.Key(key + "-control")).AlignItems(ui.Center).Children(control)
	})
	return row
}

func settingRowContent(c *ui.Context, key, title string, control func()) {
	ui.Column(c.Key(key + "-left")).MinWidth(0).Grow(1).Gap(Space1).Children(func() {
		titleEl := ui.Text(c, title).FontSize(uiFont(TypeBody)).FontWeight(400).TextColor(ThemePrimaryText(c))
		if h := settingHint(key); h != "" {
			titleEl.Tooltip(h)
		}
	})
	settingRowRight(c, key+"-right", control)
}

func settingRowRight(c *ui.Context, key string, control func()) {
	ui.Row(c.Key(key)).MinWidth(0).Shrink(1).Wrap().Justify(ui.End).AlignItems(ui.Center).Gap(Space3).Children(control)
}
