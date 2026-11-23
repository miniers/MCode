package mcodeui

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
)

// providerSheetSection 定义供应商面板的小节标题与内容。
type providerSheetSection struct {
	ID    string
	Title string
	// Advanced 为真时这一节放进页面底部默认收起的「高级」折叠区。
	Advanced bool
	Render   func(c *ui.Context, st *SettingsState)
}

// providerSheetSections 按供应商类型分发正文的分节集合。
func providerSheetSections(st *SettingsState, kind, id string, lib *modelsLibraryView) []providerSheetSection {
	switch kind {
	case "cli":
		return cliProviderSheetSections(st, id, lib)
	case "preset", "custom":
		return apiProviderSheetSections(st, kind, id)
	}
	return nil
}

// renderProviderSheetBody 画供应商面板的单页内容与高级折叠区。
func renderProviderSheetBody(c *ui.Context, st *SettingsState) {
	kind, id := currentProviderSheet(st)
	if id == "" {
		return
	}
	var lib *modelsLibraryView
	if kind == "cli" {
		lib = modelsPageLibrary(c, st)
	}
	defs := providerSheetSections(st, kind, id, lib)
	if len(defs) == 0 {
		return
	}
	// 高级设置已全部平铺展开，直接消费一次性跳转行标记即可。
	consumeProviderSheetJumpRow(st)
	ui.Scroll(c.Key("settings-sheet-provider-scroll")).
		Grow(1).
		Shrink(1).
		FillWidth().
		Clip().
		Children(func() {
			content := ui.Column(c.Key("settings-sheet-provider-content")).
				FillWidth().
				Gap(settingsSectionGap).
				Padding(0, Space5, Space5, Space5)
			content.Children(func() {
				if kind == "cli" && providerSheetHasStatusRow(c, st, kind, id) {
					renderSettingsSection(c, "sec-provider-status", "", func() {
						renderProviderSheetStatusRow(c, st, kind, id)
					})
				}
				renderSection := func(def providerSheetSection) {
					ui.Column(c.Key("provider-sheet-section-" + def.ID)).FillWidth().Gap(Space2).Children(func() {
						// agy 与 API 供应商卡片不重复画小节外标题；仅普通 CLI 保留节标题以便区分账号/模型/命令行。
						if kind == "cli" && id != "agy" {
							ui.Text(c, def.Title).
								FontSize(uiFont(TypeCaption)).
								FontWeight(500).
								TextColor(ThemeTertiaryText(c)).
								Margin(0, 0, 0, settingsRowPadX)
						}
						def.Render(c, st)
					})
				}
				for _, def := range defs {
					renderSection(def)
				}
			})
			reportSettingsSheetContentHeight("provider", content.Bounds().H)
		})
}

// consumeProviderSheetJumpRow 取走面板内的一次性跳转行（读到即清空），空串表示没有待跳转的行。
func consumeProviderSheetJumpRow(st *SettingsState) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	row := st.sheetJumpRow
	st.sheetJumpRow = ""
	return row
}

// providerSheetAdvancedOpen 返回高级折叠区状态；供应商变化时收起。
func providerSheetAdvancedOpen(st *SettingsState, kind, id string) bool {
	key := kind + "/" + id
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.providerAdvancedKey != key {
		st.providerAdvancedOpen = false
		st.providerAdvancedKey = key
	}
	return st.providerAdvancedOpen
}

// setProviderSheetAdvancedOpenLocked 记录高级折叠状态；调用方必须已经持有 st.mu。
func setProviderSheetAdvancedOpenLocked(st *SettingsState, kind, id string, open bool) {
	st.providerAdvancedOpen = open
	st.providerAdvancedKey = kind + "/" + id
}

// setProviderSheetAdvancedOpen 记录高级折叠状态并安排重绘。
func setProviderSheetAdvancedOpen(st *SettingsState, kind, id string, open bool) {
	st.mu.Lock()
	setProviderSheetAdvancedOpenLocked(st, kind, id, open)
	st.mu.Unlock()
	st.invalidate()
}

// providerSheetSectionForRow 把搜索命中的行映射到它所在的节；映射不到时返回空串。
func providerSheetSectionForRow(kind, id, rowID string) string {
	switch kind {
	case "preset", "custom":
		if strings.Contains(rowID, "form-model") {
			return "sec-provider-models"
		}
		return "sec-provider-account"
	case "cli":
		if id == "agy" {
			// strategy / affinity 也带 sec-agy-routing- 前缀，必须先于通用的路由判断。
			switch {
			case rowID == "row-agy-terminal-login":
				return "sec-agy-advanced-login"
			case strings.HasPrefix(rowID, "sec-agy-routing-strategy"):
				return "sec-agy-strategy"
			case strings.HasPrefix(rowID, "sec-agy-routing-affinity"):
				return "sec-agy-affinity"
			case strings.HasPrefix(rowID, "sec-agy-routing-"), rowID == "sec-agy-auto-warmup":
				return "sec-agy-routing"
			case rowID == "row-agy-channel", rowID == "row-agy-channel-update":
				return "sec-agy-channel"
			default:
				return "sec-agy-accounts"
			}
		}
		switch {
		case rowID == "row-provider-magpie":
			return ""
		case rowID == "row-provider-bin", rowID == "row-claude-bin", rowID == "row-codex-bin":
			return "sec-provider-cli"
		case strings.HasPrefix(rowID, "provider-model-"):
			return "sec-provider-models"
		default:
			return "sec-provider-account"
		}
	}
	return ""
}

// renderProviderSheetHero 画面板头部标题：仅显示当前供应商名称。
func renderProviderSheetHero(c *ui.Context, st *SettingsState) {
	kind, id := currentProviderSheet(st)
	name := "供应商"
	if id != "" {
		entry := providerSheetListEntry(c, st, kind, id)
		name = entry.Name
		if name == "" {
			name = providerDisplayName(id)
		}
	}
	ui.Text(c, name).
		FontSize(uiFont(TypeTitle)).
		FontWeight(600).
		TextColor(ThemePrimaryText(c)).
		Grow(1).
		SingleLine()
}

// providerSheetHasStatusRow 判断供应商面板正文第一行是否需要渲染状态与主操作。
func providerSheetHasStatusRow(c *ui.Context, st *SettingsState, kind, id string) bool {
	entry := providerSheetListEntry(c, st, kind, id)
	subtitle := providerSheetSubtitle(st, kind, id)
	_, _, ok := providerSheetPrimaryAction(st, id)
	return entry.Status != "" || subtitle != "" || ok
}

// renderProviderSheetStatusRow 在供应商面板正文第一行渲染状态胶囊、副标题与主操作按钮。
func renderProviderSheetStatusRow(c *ui.Context, st *SettingsState, kind, id string) {
	entry := providerSheetListEntry(c, st, kind, id)
	subtitle := providerSheetSubtitle(st, kind, id)
	label, action, ok := providerSheetPrimaryAction(st, id)
	if entry.Status == "" && subtitle == "" && !ok {
		return
	}
	ui.Row(c.Key("row-provider-status")).
		Padding(settingsRowPadY, settingsRowPadX).
		MinHeight(settingsRowMinHeight).
		Gap(Space4).
		AlignItems(ui.Center).
		Children(func() {
			ui.Row(c.Key("row-provider-status-left")).
				MinWidth(0).
				Grow(1).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					renderProviderSheetStatusPill(c, entry.Status)
					if subtitle != "" {
						ui.Text(c, subtitle).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Shrink(1)
					}
				})
			if ok {
				btn := ui.ButtonBase(c.Key("provider-action-"+id)).
					Label(label).
					Radius(RadiusSM).
					Padding(settingsPillPadX, Space4).
					Background(ThemeHover(c)).
					Cursor(ui.CursorPointer).
					Shrink(0)
				if btn.Hovered() {
					btn.Background(ThemeOverlay(c, 0.15))
				}
				focusRing(c, btn)
				if btn.Clicked() {
					launchProviderTerminalAction(st, id, action)
				}
				btn.Children(func() {
					ui.Text(c, label).
						FontSize(uiFont(TypeBody)).
						TextColor(ThemeAccent(c))
				})
			}
		})
}

// providerSheetListEntry 在供应商列表快照里找当前供应商，拿名称与状态；找不到时返回零值。
func providerSheetListEntry(c *ui.Context, st *SettingsState, kind, id string) providerListEntry {
	if kind == "cli" {
		for _, entry := range cliProviderListEntries(st, modelsPageLibrary(c, st)) {
			if entry.Kind == kind && entry.ID == id {
				return entry
			}
		}
		return providerListEntry{}
	}
	// API 供应商只读当前状态快照，避免在面板渲染中触发后台拉取覆盖已注入或未标记 loaded 的列表。
	st.mu.Lock()
	defer st.mu.Unlock()
	switch kind {
	case "preset":
		for _, p := range st.presets.items {
			if p.ID == id && p.Kind == "api" {
				return providerListEntry{
					Kind:    "preset",
					ID:      p.ID,
					Name:    p.Name,
					Status:  apiProviderStatusText(p.Enabled, presetProviderHasAPIKey(p)),
					Enabled: p.Enabled,
				}
			}
		}
	case "custom":
		for _, ep := range st.endpoints.endpoints {
			if ep.ID == id && !ep.Magpie {
				return providerListEntry{
					Kind:    "custom",
					ID:      ep.ID,
					Name:    ep.Name,
					Status:  apiProviderStatusText(ep.Enabled, ep.HasAPIKey),
					Enabled: ep.Enabled,
				}
			}
		}
	}
	return providerListEntry{}
}

// renderProviderSheetStatusPill 画状态胶囊：健康状态用成功色，需要处理的状态用危险色。
func renderProviderSheetStatusPill(c *ui.Context, status string) {
	if status == "" {
		return
	}
	color := ThemeSecondaryText(c)
	switch {
	case strings.HasPrefix(status, "已连接"), strings.HasPrefix(status, "已配置"), strings.HasPrefix(status, "已登录"):
		color = ThemeSuccess(c)
	case strings.HasPrefix(status, "版本错误"), strings.HasPrefix(status, "未安装"), strings.HasPrefix(status, "需登录"):
		color = ThemeDanger(c)
	}
	ui.Box(c.Key("settings-sheet-provider-status")).
		Shrink(0).
		Background(ThemeHover(c)).
		Radius(RadiusPill).
		Padding(settingsPillPadX, Space2).
		Children(func() {
			ui.Text(c, status).
				FontSize(uiFont(TypeCaption)).
				TextColor(color).
				SingleLine()
		})
}

// providerSheetSubtitle 让身份区第二行只回答「装的是哪个版本、在哪」或「连的是哪个主机」。
func providerSheetSubtitle(st *SettingsState, kind, id string) string {
	if kind == "cli" {
		st.mu.Lock()
		installed := strings.TrimSpace(st.providerRows.versions[id].Installed)
		st.mu.Unlock()
		if installed == "" {
			installed = "…"
		}
		return installed + " · " + providerRowBin(st, id)
	}
	baseURL := ""
	st.mu.Lock()
	if kind == "preset" {
		for _, item := range st.presets.items {
			if item.ID == id {
				baseURL = strings.TrimSpace(item.BaseURL)
				break
			}
		}
	} else if kind == "custom" {
		for _, item := range st.endpoints.endpoints {
			if item.ID == id {
				baseURL = strings.TrimSpace(item.BaseURL)
				break
			}
		}
	}
	st.mu.Unlock()
	if baseURL == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Host
}
