package mcodeui

import (
	"github.com/egoist/mygo/ui"
)

type visualPickerOption struct {
	value string
	label string
	icon  *ui.SVG
}

// renderVisualPicker 渲染图标方块选项：每个选项约 64×44 圆角方块，内含图标，下方是文字；选中时描强调色 2px 边框、文字加粗
func renderVisualPicker(c *ui.Context, key string, selected *string, options []visualPickerOption, disabled bool, onSelect func(val string)) {
	ui.Row(c.Key(key)).Gap(Space3).AlignItems(ui.Start).Children(func() {
		for _, opt := range options {
			optVal := opt
			isSelected := *selected == optVal.value

			col := ui.ButtonBase(c.Key(key + "-tile-" + optVal.value)).
				Label(optVal.value)

			if disabled {
				col.Disabled(true)
			} else {
				col.OnClick(func() {
					if *selected != optVal.value {
						*selected = optVal.value
						if onSelect != nil {
							onSelect(optVal.value)
						}
					}
				})
			}

			col.Children(func() {
				ui.Column(c.Key(key + "-col-" + optVal.value)).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						// 上方约 64x44 的圆角方块，内含图标
						tile := ui.Box(c.Key(key+"-box-"+optVal.value)).
							Size(settingsPreviewTileW, settingsPreviewTileH).
							Radius(RadiusMD).
							Center()

						iconColor := ThemeSecondaryText(c)
						if disabled {
							tile.Background(ThemeOverlay(c, 0.03)).Border(Hairline, ThemeBorder(c).Alpha(0.5))
							iconColor = iconColor.Alpha(0.35)
						} else if isSelected {
							tile.Background(ThemeAccent(c).Alpha(0.12)).
								Border(settingsSelectedBorder, ThemeAccent(c))
							iconColor = ThemeAccent(c)
						} else if col.Hovered() {
							tile.Background(ThemeHover(c)).
								Border(Hairline, ThemeBorder(c))
							iconColor = ThemePrimaryText(c)
						} else {
							tile.Background(ThemeOverlay(c, 0.05)).
								Border(Hairline, ThemeBorder(c))
						}

						tile.Children(func() {
							if optVal.icon != nil {
								ui.Icon(c, optVal.icon).Size(settingsSwatchIcon, settingsSwatchIcon).TextColor(iconColor)
							}
						})

						// 下方文字：单行居中，选中时主色加粗
						txt := ui.Text(c, optVal.label).
							FontSize(uiFont(TypeFootnote)).
							SingleLine()
						if isSelected {
							txt.TextColor(ThemePrimaryText(c)).FontWeight(600)
						} else if disabled {
							txt.TextColor(ThemeSecondaryText(c).Alpha(0.40))
						} else {
							txt.TextColor(ThemeSecondaryText(c))
						}
					})
			})
		}
	})
}
