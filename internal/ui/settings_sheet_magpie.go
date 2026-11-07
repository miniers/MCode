package mcodeui

import "github.com/egoist/mygo/ui"

var sheetMagpieGateway = settingsSheet{
	ID: "magpie-gateway", Title: "Magpie 网关",
	Body: func(c *ui.Context, st *SettingsState) {
		st.mu.Lock()
		id := st.magpieHub.sheetGatewayID
		st.mu.Unlock()
		renderMagpieGatewaySheetBody(c, st, id)
	},
}

var sheetMagpiePersonas = settingsSheet{
	ID: "magpie-personas", Title: "人设",
	Body: renderMagpiePersonasSheetBody,
}

func init() {
	settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetMagpieGateway, sheetMagpiePersonas)
}
