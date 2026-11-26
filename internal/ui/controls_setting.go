package mcodeui

import "github.com/egoist/mygo/ui"

func SettingRow(c *ui.Context, key, title string, control func()) ui.Element {
	return settingRow(c, key, title, false, nil, control)
}

func StackedSettingRow(c *ui.Context, key, title string, control func()) ui.Element {
	return settingRow(c, key, title, true, nil, control)
}

// SettingRowKeyed 与 SettingRow 同布局，但行自身的 key 与内部子控件前缀分开传：
// 设置搜索用行 key（如 row-theme-color-card）定位，控件前缀不进入设置索引。
func SettingRowKeyed(c *ui.Context, rowKey, controlKey, title string, control func()) ui.Element {
	return settingRowParts(c, rowKey, controlKey, title, false, nil, control)
}

func settingRow(c *ui.Context, key, title string, stacked bool, decorate func(ui.Element) ui.Element, control func()) ui.Element {
	return settingRowParts(c, key, key, title, stacked, decorate, control)
}

// SettingRowKeyedTall 与 SettingRowKeyed 同布局，但控件可以高出常规行（如颜色行里的色块）：
// 左侧标题列垂直居中并跟随行高，行高由右侧最高的控件决定。
func SettingRowKeyedTall(c *ui.Context, rowKey, controlKey, title string, control func()) ui.Element {
	return settingRowPartsTall(c, rowKey, controlKey, title, func(row ui.Element) ui.Element { return row }, control)
}

func settingRowParts(c *ui.Context, rowKey, controlKey, title string, stacked bool, decorate func(ui.Element) ui.Element, control func()) ui.Element {
	return settingRowPartsOf(c, rowKey, controlKey, title, stacked, decorate, control, false)
}

// settingRowPartsTall 与 settingRowParts 相同，但不 stacked 的行里左侧标题列垂直居中，行高交给控件。
func settingRowPartsTall(c *ui.Context, rowKey, controlKey, title string, decorate func(ui.Element) ui.Element, control func()) ui.Element {
	return settingRowPartsOf(c, rowKey, controlKey, title, false, decorate, control, true)
}

func settingRowPartsOf(c *ui.Context, rowKey, controlKey, title string, stacked bool, decorate func(ui.Element) ui.Element, control func(), tall bool) ui.Element {
	var row ui.Element
	if stacked {
		row = ui.Column(c.Key(rowKey)).Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight).Gap(Space4)
	} else {
		row = ui.Row(c.Key(rowKey)).Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight).Gap(Space4).AlignItems(ui.Center)
	}
	if decorate != nil {
		row = decorate(row)
	}
	row.Children(func() {
		if !stacked {
			settingRowContentOf(c, controlKey, title, tall, control)
			return
		}
		ui.Column(c.Key(controlKey + "-header")).Gap(Space1).Children(func() {
			titleEl := ui.Text(c, title).FontSize(uiFont(TypeBody)).FontWeight(400).TextColor(ThemePrimaryText(c)).SingleLine()
			if h := settingHint(controlKey); h != "" {
				titleEl.Tooltip(h)
			}
		})
		ui.Row(c.Key(controlKey + "-control")).AlignItems(ui.Center).Children(control)
	})
	return row
}

func settingRowContent(c *ui.Context, key, title string, control func()) {
	settingRowContentOf(c, key, title, false, control)
}

func settingRowContentOf(c *ui.Context, key, title string, tall bool, control func()) {
	left := ui.Column(c.Key(key + "-left")).MinWidth(0).Grow(1).Gap(Space1)
	if tall {
		left.Justify(ui.Center)
	}
	left.Children(func() {
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
