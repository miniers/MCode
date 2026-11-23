package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// magpiePageConnected 报告网关是否已连上且状态已知；未知时先按未连接处理。
func magpiePageConnected(st *SettingsState) bool {
	connected, known := magpieConnected(st)
	return known && connected
}

func magpiePageHasGateways(st *SettingsState) bool {
	gateways, _ := magpieGateways(st)
	return len(gateways) > 0
}

func magpiePageHasInstalledAgents(st *SettingsState) bool {
	return magpiePageHasGateways(st) && len(magpieInstalledAgents(st)) > 0
}

// retryPiMagpieLoad 清除 pi 端点的加载错误并重新发起拉取。
func retryPiMagpieLoad(st *SettingsState) {
	st.mu.Lock()
	st.piMagpie.err = ""
	st.piMagpie.loaded = false
	st.mu.Unlock()
	magpieLoadFor(st, "pi")()
	st.invalidate()
}

var pageMagpie = settingsPage{
	ID: "magpie", Title: "Magpie", Icon: IconNetwork(),
	Sections: []settingsSection{
		{ID: "sec-magpie-gateways", Title: "网关", Rows: []setting{{ID: "row-magpie-gateways", Control: customFullWidth(func(c *ui.Context, st *SettingsState) {
			connected, known := magpieConnected(st)
			if !known {
				_, _, _, errText, _ := magpieEndpointsFor(st, "pi")
				if errText != "" {
					ui.Row(c.Key("row-magpie-gateways-load-err")).
						FillWidth().
						Padding(settingsRowPadY, settingsRowPadX).
						AlignItems(ui.Center).
						Gap(Space3).
						Children(func() {
							ui.Text(c, "加载失败："+errText).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeDanger(c)).
								Grow(1)
							Button(c, ButtonOpts{
								Key:     "btn-magpie-gateways-retry",
								Text:    "重试",
								Label:   "重试",
								Variant: ButtonSecondary,
								Size:    ControlSM,
								OnClick: func() { retryPiMagpieLoad(st) },
							})
						})
					return
				}
				ui.Row(c.Key("row-magpie-gateways-loading")).
					Padding(settingsRowPadY, settingsRowPadX).
					Children(func() {
						LoadingIndicator(c, "magpie-gateways-loading-indicator", "正在加载…", IconSM)
					})
				return
			}
			if !connected {
				renderMagpieConnectForm(c, st)
				return
			}
			gateways, _ := magpieGateways(st)
			for i, gateway := range gateways {
				gw := gateway
				if i > 0 {
					renderSettingsDivider(c, "div-magpie-gateway-"+gw.ID)
				}
				renderMagpieGatewayListRow(c, st, gw, func() { openMagpieGatewaySheet(st, gw.ID) })
			}
			if len(gateways) > 0 {
				renderSettingsDivider(c, "div-magpie-gateway-add")
			}
			button := ui.ButtonBase(c.Key("row-magpie-gateway-add")).Label("添加网关").FillWidth().
				Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight).Cursor(ui.CursorPointer)
			if button.Hovered() {
				button.Background(ThemeHover(c))
			}
			focusRing(c, button)
			if button.Clicked() {
				openMagpieGatewaySheet(st, "")
			}
			button.Children(func() {
				ui.Row(c.Key("row-magpie-gateway-add-content")).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, "添加网关").FontSize(uiFont(TypeBody)).TextColor(ThemeAccent(c))
					ui.Box(c.Key("row-magpie-gateway-add-spacer")).Grow(1)
				})
			})
		})}, {ID: "row-magpie-personas", Title: "人设", Visible: magpiePageConnected, Control: link(func(st *SettingsState) string {
			if count, known := magpiePersonaCount(st); known {
				return fmt.Sprintf("%d", count)
			}
			return ""
		}, "magpie-personas")}}},
		{ID: "sec-magpie-agents", Title: "智能体路由", Visible: magpiePageHasInstalledAgents, Rows: []setting{
			magpieAgentRouteRow("claude"),
			magpieAgentRouteRow("codex"),
			magpieAgentRouteRow("pi"),
		}},
		{ID: "sec-magpie-models", Title: "模型矩阵", Visible: magpiePageHasInstalledAgents, Rows: []setting{
			{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)},
		}},
	},
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageMagpie) }

func renderPageMagpie(c *ui.Context, st *SettingsState) { renderSettingsTreePage(c, st, pageMagpie) }
