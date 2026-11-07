package mcodeui

import "github.com/egoist/mygo/ui"

var sheetOpenIn = settingsSheet{
	ID:    "open-in",
	Title: "打开方式",
	Body: func(c *ui.Context, st *SettingsState) {
		renderPageOpenIn(c, st)
	},
}

func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetOpenIn) }
