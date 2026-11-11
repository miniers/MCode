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
	// 搜索命中高级行时先展开，确保目标行在本帧即可渲染并消费滚动标记。
	if row := consumeProviderSheetJumpRow(st); row != "" {
		section := providerSheetSectionForRow(kind, id, row)
		for _, def := range defs {
			if def.ID == section && def.Advanced {
				st.mu.Lock()
				setProviderSheetAdvancedOpenLocked(st, kind, id, true)
				st.mu.Unlock()
				break
			}
		}
	}
	open := providerSheetAdvancedOpen(st, kind, id)
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
				renderSection := func(def providerSheetSection) {
					ui.Column(c.Key("provider-sheet-section-" + def.ID)).FillWidth().Gap(Space2).Children(func() {
						// agy 的账号、策略、会话保持和调度由原有渲染函数画标题。
						if !(kind == "cli" && id == "agy" && def.ID != "sec-agy-advanced-login") {
							ui.Text(c, def.Title).
								FontSize(uiFont(TypeCaption)).
								FontWeight(500).
								TextColor(ThemeTertiaryText(c)).
								Margin(0, 0, 0, settingsRowPadX)
						}
						def.Render(c, st)
					})
				}
				var advanced []providerSheetSection
				var titles []string
				for _, def := range defs {
					if def.Advanced {
						advanced = append(advanced, def)
						titles = append(titles, def.Title)
					} else {
						renderSection(def)
					}
				}
				if len(advanced) == 0 {
					return
				}
				label, icon := "展开高级", IconChevronRight()
				if open {
					label, icon = "收起高级", IconChevronDown()
				}
				btn := ui.ButtonBase(c.Key("provider-sheet-advanced-toggle")).
					Label(label).Radius(RadiusSM).Padding(Space2, Space2)
				if btn.Hovered() {
					btn.Background(ThemeHover(c))
				}
				if btn.Clicked() {
					setProviderSheetAdvancedOpen(st, kind, id, !open)
				}
				btn.Children(func() {
					ui.Row(c.Key("provider-advanced-content")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
						ui.Text(c, "高级").FontSize(uiFont(TypeBody)).FontWeight(500).TextColor(ThemeSecondaryText(c))
						ui.Text(c, strings.Join(titles, "、")).FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c)).SingleLine().Shrink(1)
						ui.Box(c.Key("provider-advanced-spacer")).Grow(1)
						ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					})
				})
				if open {
					for _, def := range advanced {
						renderSection(def)
					}
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

// renderProviderSheetHero 画面板身份区：图标、名称、状态胶囊、副标题与主操作。
func renderProviderSheetHero(c *ui.Context, st *SettingsState) {
	kind, id := currentProviderSheet(st)
	if id == "" {
		ui.Box(c.Key("settings-sheet-provider-hero")).Grow(1)
		return
	}
	entry := providerSheetListEntry(c, st, kind, id)
	name := entry.Name
	if name == "" {
		name = providerDisplayName(id)
	}
	ui.Row(c.Key("settings-sheet-provider-hero")).
		Grow(1).
		AlignItems(ui.Center).
		Gap(Space4).
		Children(func() {
			ui.Box(c.Key("settings-sheet-provider-hero-icon")).
				Width(IconLG).
				Height(IconLG).
				Shrink(0).
				Children(func() { renderProviderRowIcon(c, id, IconLG) })
			ui.Column(c.Key("settings-sheet-provider-hero-titles")).
				Grow(1).
				Shrink(1).
				Gap(Space1).
				Children(func() {
					ui.Row(c.Key("settings-sheet-provider-hero-name")).
						Gap(Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, name).
								FontSize(uiFont(TypeTitle)).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
							renderProviderSheetStatusPill(c, entry.Status)
						})
					if subtitle := providerSheetSubtitle(st, kind, id); subtitle != "" {
						ui.Text(c, subtitle).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
					}
				})
			if label, action, ok := providerSheetPrimaryAction(st, id); ok {
				btn := ui.ButtonBase(c.Key("provider-action-"+id)).
					Label(label).
					Padding(settingsPillPadX, settingsRowPadX).
					Shrink(0)
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
	var entries []providerListEntry
	if kind == "cli" {
		entries = cliProviderListEntries(st, modelsPageLibrary(c, st))
	} else {
		entries = apiProviderListEntries(st)
	}
	for _, entry := range entries {
		if entry.Kind == kind && entry.ID == id {
			return entry
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
