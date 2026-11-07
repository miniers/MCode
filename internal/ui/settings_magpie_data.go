package mcodeui

import (
	"encoding/json"
	"strings"

	"github.com/egoist/mygo/ui"
)

func magpieGateways(st *SettingsState) (gateways []CustomEndpoint, loaded bool) {
	st.mu.Lock()
	loaded = st.endpoints.loaded && !st.endpoints.loading && st.endpoints.err == ""
	loading := st.endpoints.loading
	for _, ep := range st.endpoints.endpoints {
		if ep.Magpie {
			gateways = append(gateways, ep)
		}
	}
	st.mu.Unlock()
	if !loaded && !loading {
		loadCustomEndpoints(st)
	}
	return
}

func magpieConnected(st *SettingsState) (connected bool, known bool) {
	gateways, loaded := magpieGateways(st)
	if !loaded {
		return false, false
	}
	eligible := false
	for _, gw := range gateways {
		if gw.Enabled && gw.HasAPIKey {
			eligible = true
			break
		}
	}
	if !eligible {
		return false, true
	}
	endpoints, loading, loaded, errText, _ := magpieEndpointsFor(st, "pi")
	if !loaded || loading {
		if !loading && errText == "" {
			magpieLoadFor(st, "pi")()
		}
		return false, false
	}
	if errText != "" {
		return false, false
	}
	unknown := false
	for _, gw := range gateways {
		if !gw.Enabled || !gw.HasAPIKey {
			continue
		}
		found := false
		for _, ep := range endpoints {
			if ep.ID == gw.ID {
				found = true
				if ep.Err == "" {
					return true, true
				}
				break
			}
		}
		if !found {
			unknown = true
		}
	}
	return false, !unknown
}

func magpieNavStatus(st *SettingsState) string {
	connected, known := magpieConnected(st)
	if !known {
		return "Magpie"
	}
	if connected {
		return "Magpie · 已连接"
	}
	return "连接 Magpie"
}

func magpieModelSource(st *SettingsState, agent, modelID string) (endpointID string, ok bool) {
	endpoints, loading, loaded, errText, _ := magpieEndpointsFor(st, agent)
	if !loaded || loading {
		if !loading && errText == "" {
			magpieLoadFor(st, agent)()
		}
		return "", false
	}
	if errText != "" {
		return "", false
	}
	for _, ep := range endpoints {
		for _, selected := range ep.Selected {
			if ep.ProviderID != "" && ep.ProviderID+"/"+selected == modelID {
				return ep.ID, true
			}
		}
	}
	return "", false
}

func magpiePersonaCount(st *SettingsState) (n int, known bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, ep := range st.endpoints.endpoints {
		if !ep.Magpie || !ep.Enabled {
			continue
		}
		if text := strings.TrimSpace(ep.PersonasJSON); text != "" {
			var personas []json.RawMessage
			if json.Unmarshal([]byte(text), &personas) == nil && personas != nil {
				return len(personas), true
			}
		}
		if st.endpoints.editID == ep.ID && st.endpoints.personaTestShown && st.endpoints.personaTestResult.OK {
			return st.endpoints.personaTestResult.Count, true
		}
		break
	}
	return 0, false
}

func openMagpieGatewaySheet(st *SettingsState, gatewayID string) {
	st.mu.Lock()
	if st.endpoints.editing {
		closeCustomEndpointForm(st)
	}
	st.magpieHub.sheetGatewayID = gatewayID
	st.magpieHub.sheetSaving = false
	st.mu.Unlock()
	openSettingsSheet(st, "magpie-gateway", "")
}

func magpieOpenGatewayDraft(st *SettingsState, gatewayID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.endpoints.editing && st.endpoints.editScope == endpointFormScopeMagpie && st.endpoints.editID == gatewayID {
		return true
	}
	if st.endpoints.editing {
		return false
	}
	if gatewayID == "" {
		openCustomEndpointForm(st, nil, endpointFormScopeMagpie)
		st.endpoints.editName = "Magpie"
		st.endpoints.editBaseURL = magpieDefaultBaseURL
		return true
	}
	for _, gw := range st.endpoints.endpoints {
		if gw.Magpie && gw.ID == gatewayID {
			openCustomEndpointForm(st, &gw, endpointFormScopeMagpie)
			return true
		}
	}
	return false
}

func renderMagpieGatewayListRow(c *ui.Context, st *SettingsState, gw CustomEndpoint, onOpen func()) {
	endpoints, _, _, _, _ := magpieEndpointsFor(st, "pi")
	status := magpieGatewayStatusOf(gw, endpoints)
	pill := status
	if status.Kind == magpieGatewayStatusDanger {
		pill.Text = "连接失败"
	}
	row := ui.ButtonBase(c.Key("row-magpie-gateway-"+gw.ID)).Label("打开 "+gw.Name).FillWidth().
		Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight)
	if row.Hovered() {
		row.Background(ThemeHover(c))
	}
	if onOpen != nil {
		row.OnClick(onOpen)
	}
	row.Children(func() {
		ui.Column(c.Key("content-magpie-gateway-"+gw.ID)).FillWidth().Gap(Space1).Children(func() {
			ui.Row(c.Key("head-magpie-gateway-"+gw.ID)).FillWidth().AlignItems(ui.Center).Gap(Space4).Children(func() {
				ui.Column(c.Key("name-magpie-gateway-"+gw.ID)).Grow(1).Children(func() {
					ui.Text(c, gw.Name).FontSize(TypeBody).TextColor(ThemePrimaryText(c)).SingleLine()
				})
				ui.Row(c.Key("actions-magpie-gateway-"+gw.ID)).Shrink(0).AlignItems(ui.Center).Gap(Space4).Children(func() {
					renderMagpieHubStatusPill(c, "pill-magpie-gateway-"+gw.ID, pill)
					ui.Icon(c.Key("chevron-magpie-gateway-"+gw.ID), IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				})
			})
			if status.Kind == magpieGatewayStatusDanger {
				ui.Text(c, status.Text).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			}
		})
	})
}

func renderMagpieGatewaySheetBody(c *ui.Context, st *SettingsState, gatewayID string) {
	st.mu.Lock()
	finished := st.magpieHub.sheetSaving && !st.endpoints.saving && !st.endpoints.editing
	if finished || (!st.endpoints.saving && st.endpoints.editErr != "") {
		st.magpieHub.sheetSaving = false
	}
	st.mu.Unlock()
	if finished {
		closeSettingsSheet(st)
		return
	}
	if !magpieOpenGatewayDraft(st, gatewayID) {
		return
	}
	st.mu.Lock()
	shouldLoad := gatewayID != "" && st.endpoints.quotaProviderLoadedFor != gatewayID
	st.mu.Unlock()
	if shouldLoad {
		loadMagpieQuotaProviders(st, gatewayID)
	}
	if gatewayID != "" {
		st.mu.Lock()
		var gateway CustomEndpoint
		for _, ep := range st.endpoints.endpoints {
			if ep.ID == gatewayID {
				gateway = ep
				break
			}
		}
		busy := st.endpoints.saving || st.endpoints.testing || st.endpoints.personaTestBusy || st.endpoints.toggling || st.endpoints.deletingID != ""
		confirm := st.endpoints.deleteConfirmID == gatewayID
		st.mu.Unlock()
		renderSettingsSection(c, "sec-magpie-gateway-operations", "", func() {
			renderSettingRow(c, "row-magpie-gateway-enabled", "启用", func() {
				// 开关的无障碍标签带上网关名，行内标题保持「启用」。
				enabledSetting := setting{ID: "row-magpie-gateway-enabled", Title: "启用 " + gateway.Name}
				toggleFunc(
					func(*SettingsState) (bool, bool) { return gateway.Enabled, busy },
					func(st *SettingsState, on bool) { toggleCustomEndpointEnabled(st, gateway, on) },
				).render(c, st, enabledSetting)
			})
			renderSettingsDivider(c, "div-magpie-gateway-operations")
			renderSettingRow(c, "row-magpie-gateway-operations", "操作", func() {
				ui.Row(c.Key("magpie-sheet-operations")).Gap(settingsCompactPadX).Children(func() {
					refresh := ui.ButtonBase(c.Key("magpie-sheet-refresh")).Label("刷新").Disabled(busy)
					if refresh.Clicked() {
						st.loadPiMagpieEndpoints()
						markMagpieHubClaudeStale(st)
					}
					refresh.Children(func() { ui.Text(c, "刷新") })
					label := "删除"
					if confirm {
						label = "确认删除"
					}
					del := ui.ButtonBase(c.Key("magpie-sheet-delete")).Label(label).Disabled(busy)
					if del.Clicked() {
						if confirm {
							deleteCustomEndpoint(st, gatewayID)
							closeSettingsSheet(st)
						} else {
							st.mu.Lock()
							st.endpoints.deleteConfirmID = gatewayID
							st.mu.Unlock()
							st.invalidate()
						}
					}
					del.Children(func() { ui.Text(c, label) })
				})
			})
		})
	}
	renderMagpieFormSnapshot(c, st, gatewayID, "gateway")
}

func renderMagpiePersonasSheetBody(c *ui.Context, st *SettingsState) {
	gateways, loaded := magpieGateways(st)
	if !loaded {
		return
	}
	st.mu.Lock()
	id := ""
	if st.endpoints.editing && st.endpoints.editScope == endpointFormScopeMagpie {
		id = st.endpoints.editID
	}
	st.mu.Unlock()
	if id == "" {
		for _, gw := range gateways {
			if gw.Enabled {
				id = gw.ID
				break
			}
		}
	}
	if id == "" || !magpieOpenGatewayDraft(st, id) {
		return
	}
	if len(gateways) > 1 {
		for _, gw := range gateways {
			gw := gw
			btn := ui.ButtonBase(c.Key("persona-gateway-" + gw.ID)).Label(gw.Name).Disabled(gw.ID == id)
			if btn.Clicked() {
				st.mu.Lock()
				current := customEndpointFormEndpoint(st)
				var original CustomEndpoint
				for _, ep := range st.endpoints.endpoints {
					if ep.ID == id {
						original = ep
						break
					}
				}
				dirty := current.PersonaWebURL != strings.TrimSpace(original.PersonaWebURL) || current.PersonasJSON != strings.TrimSpace(original.PersonasJSON) || current.PersonaRules != original.PersonaRules || st.endpoints.editWebKey != "" || current.Name != strings.TrimSpace(original.Name) || current.BaseURL != strings.TrimSpace(original.BaseURL) || current.Model != strings.TrimSpace(original.Model) || st.endpoints.editAPIKey != ""
				if !dirty {
					closeCustomEndpointForm(st)
					openCustomEndpointForm(st, &gw, endpointFormScopeMagpie)
				} else {
					st.endpoints.editErr = "请先保存或取消更改"
				}
				st.mu.Unlock()
				st.invalidate()
			}
			btn.Children(func() { ui.Text(c, gw.Name) })
		}
	}
	renderMagpieFormSnapshot(c, st, id, "persona")
}

func renderMagpieConnectForm(c *ui.Context, st *SettingsState) {
	if !magpieOpenGatewayDraft(st, "") {
		return
	}
	renderMagpieFormSnapshot(c, st, "", "connect")
}

func renderMagpieFormSnapshot(c *ui.Context, st *SettingsState, id, mode string) {
	st.mu.Lock()
	e := st.endpoints
	quota := make(map[string]bool, len(e.editQuotaProviders))
	for k, v := range e.editQuotaProviders {
		quota[k] = v
	}
	list := append([]string(nil), e.quotaProviderList...)
	var result *CustomEndpointTestResult
	if e.testResult != nil {
		copy := *e.testResult
		result = &copy
	}
	busy := e.saving || e.testing || e.personaTestBusy || e.toggling || e.deletingID != ""
	st.mu.Unlock()
	renderMagpieGatewayForm(c, st, id, e.editAPIKey, e.editWebKey, e.editPersonaWebURL, e.editPersonasJSON,
		e.editHasWebKey, e.editHasAPIKey, e.personaTestBusy, e.personaTestShown, e.personaTestErr, e.personaTestResult,
		e.quotaProviderLoading, e.quotaProviderErr, list, quota, busy, e.testing, e.saving, e.editErr, e.testErr, result, mode)
}
