package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
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

var pageMagpie = settingsPage{
	ID: "magpie", Title: "Magpie", Icon: IconSettings(),
	Sections: []settingsSection{
		{ID: "sec-magpie-gateways", Title: "网关", Rows: []setting{{ID: "row-magpie-gateways", Control: customFullWidth(func(c *ui.Context, st *SettingsState) {
			connected, known := magpieConnected(st)
			if !known {
				ui.Text(c, "正在加载…")
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
				Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight)
			if button.Hovered() {
				button.Background(ThemeHover(c))
			}
			if button.Clicked() {
				openMagpieGatewaySheet(st, "")
			}
			button.Children(func() {
				ui.Row(c.Key("row-magpie-gateway-add-content")).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, "添加网关").FontSize(TypeBody).TextColor(ThemeAccent(c))
					ui.Box(c.Key("row-magpie-gateway-add-spacer")).Grow(1)
				})
			})
		})}, {ID: "row-magpie-personas", Title: "人设", Visible: magpiePageConnected, Control: link(func(st *SettingsState) string {
			if count, known := magpiePersonaCount(st); known {
				return fmt.Sprintf("%d", count)
			}
			return ""
		}, "magpie-personas")}}},
		{ID: "sec-magpie-agents", Title: "智能体", Visible: magpiePageHasInstalledAgents, Rows: []setting{
			magpieAgentRouteRow("claude"),
			magpieAgentRouteRow("codex"),
			magpieAgentRouteRow("pi"),
		}},
		{ID: "sec-magpie-models", Title: "模型", Visible: magpiePageHasInstalledAgents, Rows: []setting{
			{ID: "row-magpie-model-picker-visible", Title: "在模型选择器中显示", Aliases: []string{"Magpie", "模型选择器", "显示"}, Control: toggle(inverted(val(func(cfg *config.Config) *bool { return &cfg.Providers.HideMagpieModelsInPicker }, true)))},
			{ID: "row-magpie-model-matrix", Control: customFullWidth(renderMagpieModelMatrix)},
		}},
	},
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageMagpie) }

func renderPageMagpie(c *ui.Context, st *SettingsState) { renderSettingsTreePage(c, st, pageMagpie) }
