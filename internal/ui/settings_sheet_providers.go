package mcodeui

import "github.com/egoist/mygo/ui"

func init() {
	settingsTreeSheetDefs = append(settingsTreeSheetDefs,
		settingsSheet{
			ID:             "provider",
			Width:          720,
			MinWidth:       settingsProviderSheetMinWidth,
			Title:          "供应商",
			BodyOwnsScroll: true,
			FitContent:     true,
			FooterVisible: func(st *SettingsState) bool {
				kind, id := currentProviderSheet(st)
				return id != "" && (kind == "preset" || kind == "custom")
			},
			Header: renderProviderSheetHero,
			Body:   renderProviderSheetBody,
			Footer: func(c *ui.Context, st *SettingsState) {
				kind, id := currentProviderSheet(st)
				if id == "" || (kind != "preset" && kind != "custom") {
					return
				}
				renderAPIProviderSheetFooter(c, st, kind, id)
			},
		},
		settingsSheet{ID: "provider-add", Width: 720, Title: "添加供应商", Body: func(c *ui.Context, st *SettingsState) {
			renderProviderAddSheetBody(c, st, cliProviderListEntries(st, modelsPageLibrary(c, st)))
		}},
	)
	// 供应商面板用 Body 分发内容，Sections 只为搜索与跳转登记 agy 那几行的 ID。
	registerAgyProviderSheetSections()
}
