package mcodeui

import "github.com/egoist/mygo/ui"

var sheetSteward = settingsSheet{
	ID:    "steward",
	Title: "总管",
	Body:  func(c *ui.Context, st *SettingsState) { renderPageSteward(c, st) },
}

func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetSteward) }
