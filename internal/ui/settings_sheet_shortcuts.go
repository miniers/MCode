package mcodeui

import "github.com/egoist/mygo/ui"

// sheetShortcuts 是「快捷键」面板：各功能分组的快捷键表放在面板里，页面只留一行入口。
var sheetShortcuts = settingsSheet{
	Width: 720,
	ID:    "shortcuts",
	Title: "快捷键",
	Body: func(c *ui.Context, st *SettingsState) {
		renderShortcutTable(c, st, "")
	},
}

// 注册到面板树放在 init 里：包级变量不能引用面板的初始值，否则会和渲染链形成初始化循环。
func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetShortcuts) }
