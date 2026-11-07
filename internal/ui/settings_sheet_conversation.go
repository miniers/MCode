package mcodeui

import "github.com/egoist/mygo/ui"

var sheetQuickReplies = settingsSheet{
	ID:    "quick-replies",
	Title: "快捷回复",
	Body: func(c *ui.Context, st *SettingsState) {
		renderPageQuickReplies(c, st)
	},
}

var sheetArchive = settingsSheet{
	ID:    "archive",
	Title: "已归档会话",
	Body: func(c *ui.Context, st *SettingsState) {
		renderPageArchive(c, st)
	},
}

func init() {
	settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetQuickReplies, sheetArchive)
}
