package mcodeui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
)

// mcpCatalogUIState 维护「预设」分区的交互状态：搜索关键词、当前展开填表的条目与用户已填的值。
// values 以「条目ID.字段名」为键保存字符串指针，保证多次重绘后输入框绑定同一个地址；
// plan 缓存当前搜索词与服务列表下的分组结果，只在两者之一变化时重建。
type mcpCatalogUIState struct {
	mu sync.Mutex

	search    string
	openEntry string
	values    map[string]*string
	errs      map[string]string
	plan      *mcpCatalogPlan
}

func getMCPCatalogUIState(st *SettingsState) *mcpCatalogUIState {
	if st == nil {
		return &mcpCatalogUIState{
			values: make(map[string]*string),
			errs:   make(map[string]string),
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.mcpCatalogPage == nil {
		st.mcpCatalogPage = &mcpCatalogUIState{
			values: make(map[string]*string),
			errs:   make(map[string]string),
		}
	}
	return st.mcpCatalogPage
}

// valuePtrLocked 返回某字段的输入值指针，首次使用时按字段默认值预填；调用方需持有锁。
func (s *mcpCatalogUIState) valuePtrLocked(entryID, fieldKey, def string) *string {
	key := entryID + "." + fieldKey
	ptr, ok := s.values[key]
	if !ok {
		val := def
		ptr = &val
		s.values[key] = ptr
	}
	return ptr
}

// mcpCatalogNormalizeName 归一化服务名，用于判断某预设是否已安装。
func mcpCatalogNormalizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, "_", "-")
	return strings.ReplaceAll(n, " ", "-")
}

// mcpCatalogOptions 构造写配置用的 Options，与自定义服务表单保持一致。
func mcpCatalogOptions(st *SettingsState) agentconf.Options {
	home, _ := os.UserHomeDir()
	return agentconf.Options{
		Home:       home,
		ProjectDir: st.currentProjectDir(),
		DataDir:    st.dataDir,
	}
}

// renderMCPCatalogSection 渲染「预设」分区：搜索框 + 按分类分组的可一键添加服务卡片。
func renderMCPCatalogBody(c *ui.Context, st *SettingsState) {
	snap := mcpPageSnapshot(st)
	servers, client := snap.servers, snap.client
	catSt := getMCPCatalogUIState(st)
	uiSt := getMCPUIState(st)

	// 分组、搜索与已安装判定全部走缓存计划，逐帧渲染只读结果。
	catSt.mu.Lock()
	plan := catSt.planLocked(catSt.search, servers)
	catSt.mu.Unlock()

	// 登录状态与首次探测都在渲染前补齐，卡片渲染只读缓存。
	mcpCatalogPageLoad(st, uiSt, plan, client)

	func() {
		rowKey := "row-mcp-catalog-search"
		renderSettingRow(c, rowKey, "搜索预设", func() {
			textFunc(
				func(*SettingsState) (string, bool) {
					catSt.mu.Lock()
					defer catSt.mu.Unlock()
					return catSt.search, false
				},
				func(st *SettingsState, text string) {
					catSt.mu.Lock()
					catSt.search = text
					catSt.mu.Unlock()
					st.invalidate()
				},
				"搜索名称、厂商、功能或示例工具",
			).render(c, st, setting{ID: rowKey, Title: "搜索预设"})
		})

		for _, group := range plan.groups {
			renderSettingsDivider(c, "div-mcp-catalog-cat-"+group.category)
			ui.Row(c.Key("row-mcp-catalog-cat-"+group.category)).
				Padding(Space1, Space5).
				Children(func() {
					ui.Text(c, group.category).
						FontSize(TypeFootnote).
						FontWeight(600).
						TextColor(ThemeSecondaryText(c))
				})

			for i, row := range group.rows {
				if i > 0 {
					renderSettingsDivider(c, "div-mcp-catalog-"+group.category+"-"+strconv.Itoa(i))
				}
				renderMCPCatalogCard(c, st, catSt, uiSt, row, client)
			}
		}

		if len(plan.groups) == 0 {
			renderSettingsDivider(c, "div-mcp-catalog-empty-sep")
			ui.Row(c.Key("mcp-catalog-empty")).
				Padding(Space5, Space5).
				Children(func() {
					ui.Text(c, "没有匹配的预设").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})
		}
	}()
}

// renderMCPCatalogCard 渲染单条预设卡片：名称、厂商、说明、示例工具、文档/密钥链接与添加表单。
// 已安装的条目在右侧展示连接状态与工具数，并允许手动重新检测。
func renderMCPCatalogCard(
	c *ui.Context,
	st *SettingsState,
	catSt *mcpCatalogUIState,
	uiSt *mcpPageUIState,
	row mcpCatalogRow,
	client Client,
) {
	entryVal := row.view.entry
	key := row.view.key

	catSt.mu.Lock()
	isOpen := catSt.openEntry == entryVal.ID
	errMsg := catSt.errs[entryVal.ID]
	catSt.mu.Unlock()

	// 已安装时汇总连接状态（必要时触发首次自动探测）
	var (
		statusLabel string
		statusColor ui.Color
		probeErr    string
		canReprobe  bool
	)
	saved := row.saved
	installed := row.installed && saved.Name != ""
	if installed {
		statusLabel, statusColor, probeErr, canReprobe = resolveMCPCatalogStatus(c, st, uiSt, saved, client)
	}

	ui.Column(c.Key(key)).
		Padding(settingsRowPadY, settingsRowPadX).
		Gap(Space1).
		Children(func() {
			ui.Row(c.Key(key + "-header")).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Text(c, entryVal.Name).
						FontSize(TypeProse).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine()

					renderBadge(c, key+"-badge-vendor", entryVal.Vendor)

					ui.Spacer(c)

					if installed {
						renderMCPCatalogStatusBadge(c, key+"-badge-status", statusLabel, statusColor)

						if canReprobe {
							reprobeBtn := ui.ButtonBase(c.Key(key+"-btn-reprobe")).
								Radius(RadiusSM).
								Padding(Space0, Space3)
							if reprobeBtn.Hovered() {
								reprobeBtn.Background(ThemeHover(c))
							}
							if reprobeBtn.Clicked() {
								resetMCPToolProbe(uiSt, mcp.EndpointKey(saved.Agent, saved.Scope, saved.Name))
								startMCPToolProbe(st, uiSt, saved)
								st.invalidate()
							}
							reprobeBtn.Children(func() {
								ui.Text(c, "重新检测").
									FontSize(TypeCaption).
									TextColor(ThemeSecondaryText(c))
							})
						}
						return
					}

					addBtn := ui.ButtonBase(c.Key(key+"-btn-add")).
						Radius(RadiusSM).
						Padding(settingsBadgePadY, settingsCompactPadX).
						Background(ThemeOverlay(c, 0.06))
					if addBtn.Hovered() {
						addBtn.Background(ThemeHover(c))
					}
					if addBtn.Clicked() {
						if len(entryVal.Fields) == 0 {
							addMCPCatalogEntry(st, catSt, entryVal, client)
						} else {
							catSt.mu.Lock()
							if catSt.openEntry == entryVal.ID {
								catSt.openEntry = ""
							} else {
								catSt.openEntry = entryVal.ID
							}
							delete(catSt.errs, entryVal.ID)
							catSt.mu.Unlock()
							st.invalidate()
						}
					}
					addBtn.Children(func() {
						label := "添加"
						if len(entryVal.Fields) > 0 && isOpen {
							label = "收起"
						}
						ui.Text(c, label).
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemeAccent(c))
					})
				})

			ui.Text(c, entryVal.Summary).
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				MaxLines(2)

			// 探测失败时在卡片内补充一行错误详情，过长截断
			if installed && probeErr != "" {
				ui.Text(c, probeErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c)).
					SingleLine()
			}

			if len(entryVal.SampleTools) > 0 {
				ui.Text(c, row.view.sampleLine).
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c).Alpha(0.8)).
					SingleLine()
			}

			if entryVal.DocsURL != "" || entryVal.KeysURL != "" {
				ui.Row(c.Key(key+"-links")).
					AlignItems(ui.Center).
					Gap(Space4).
					Margin(Space0, 0, 0, 0).
					Children(func() {
						if entryVal.DocsURL != "" {
							docsBtn := ui.ButtonBase(c.Key(key+"-btn-docs")).
								Radius(RadiusSM).
								Padding(Space0, Space2)
							if docsBtn.Hovered() {
								docsBtn.Background(ThemeHover(c))
							}
							if docsBtn.Clicked() {
								c.OpenURL(entryVal.DocsURL)
							}
							docsBtn.Children(func() {
								ui.Text(c, "文档").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							})
						}
						if entryVal.KeysURL != "" {
							keysBtn := ui.ButtonBase(c.Key(key+"-btn-keys")).
								Radius(RadiusSM).
								Padding(Space0, Space2)
							if keysBtn.Hovered() {
								keysBtn.Background(ThemeHover(c))
							}
							if keysBtn.Clicked() {
								c.OpenURL(entryVal.KeysURL)
							}
							keysBtn.Children(func() {
								ui.Text(c, "获取密钥").
									FontSize(TypeFootnote).
									TextColor(ThemeAccent(c))
							})
						}
					})
			}

			if !isOpen || len(entryVal.Fields) == 0 {
				return
			}

			ui.Column(c.Key(key+"-form")).
				Padding(settingsCompactPadX, Space4).
				Margin(Space1, 0, 0, 0).
				Radius(RadiusSM).
				Gap(Space3).
				Background(ThemeOverlay(c, 0.04)).
				Children(func() {
					for _, field := range entryVal.Fields {
						fieldVal := field
						renderStackedSettingRow(c, key+"-row-"+fieldVal.Key, fieldVal.Label, func() {
							get := func(*SettingsState) (string, bool) {
								catSt.mu.Lock()
								defer catSt.mu.Unlock()
								return *catSt.valuePtrLocked(entryVal.ID, fieldVal.Key, fieldVal.Default), false
							}
							set := func(st *SettingsState, text string) {
								catSt.mu.Lock()
								*catSt.valuePtrLocked(entryVal.ID, fieldVal.Key, fieldVal.Default) = text
								catSt.mu.Unlock()
								st.invalidate()
							}
							ctl := textFunc(get, set, fieldVal.Placeholder)
							if fieldVal.Kind == agentconf.MCPCatalogFieldSecret {
								ctl = ctl.withSecret()
							}
							ctl.render(c, st, setting{ID: key + "-row-" + fieldVal.Key, Title: fieldVal.Label})
						})

						if fieldVal.Help != "" {
							ui.Text(c, fieldVal.Help).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c).Alpha(0.8))
						}
					}

					if errMsg != "" {
						ui.Text(c, errMsg).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					}

					ui.Row(c.Key(key+"-actions")).
						AlignItems(ui.Center).
						Gap(settingsCompactPadX).
						Margin(Space0, 0, 0, 0).
						Children(func() {
							saveBtn := ui.ButtonBase(c.Key(key+"-btn-save")).
								Radius(RadiusSM).
								Padding(Space1, Space4).
								Background(ThemeAccent(c))
							if saveBtn.Clicked() {
								addMCPCatalogEntry(st, catSt, entryVal, client)
							}
							saveBtn.Children(func() {
								ui.Text(c, "保存").
									FontSize(TypeFootnote).
									FontWeight(500).
									TextColor(ThemeOnAccent(c))
							})

							cancelBtn := ui.ButtonBase(c.Key(key+"-btn-cancel")).
								Radius(RadiusSM).
								Padding(Space1, Space4).
								Background(ThemeOverlay(c, 0.06))
							if cancelBtn.Clicked() {
								catSt.mu.Lock()
								catSt.openEntry = ""
								delete(catSt.errs, entryVal.ID)
								catSt.mu.Unlock()
								st.invalidate()
							}
							cancelBtn.Children(func() {
								ui.Text(c, "取消").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							})
						})
				})
		})
}

// addMCPCatalogEntry 套用用户填写的字段，保存为自定义服务并刷新列表。
// 条目声明了登录方式时，保存成功后自动发起一次登录；登录失败只写错误，不撤销安装。
func addMCPCatalogEntry(st *SettingsState, catSt *mcpCatalogUIState, entry agentconf.MCPCatalogEntry, client Client) {
	entryVal := entry

	catSt.mu.Lock()
	values := make(map[string]string, len(entryVal.Fields))
	for _, field := range entryVal.Fields {
		if ptr, ok := catSt.values[entryVal.ID+"."+field.Key]; ok && ptr != nil {
			values[field.Key] = *ptr
		}
	}
	catSt.mu.Unlock()

	srv, _, err := entryVal.Resolve(values)
	if err != nil {
		catSt.mu.Lock()
		catSt.errs[entryVal.ID] = err.Error()
		catSt.openEntry = entryVal.ID
		catSt.mu.Unlock()
		st.invalidate()
		return
	}

	if err := agentconf.SaveMCPServer(mcpCatalogOptions(st), srv); err != nil {
		catSt.mu.Lock()
		catSt.errs[entryVal.ID] = err.Error()
		catSt.openEntry = entryVal.ID
		catSt.mu.Unlock()
		st.invalidate()
		return
	}

	catSt.mu.Lock()
	delete(catSt.errs, entryVal.ID)
	catSt.openEntry = ""
	for _, field := range entryVal.Fields {
		delete(catSt.values, entryVal.ID+"."+field.Key)
	}
	catSt.mu.Unlock()

	st.loadAgentConf(st.currentProjectDir())

	// 目录声明了登录方式时自动发起登录：device 走设备流，auto/loopback 走授权码 + PKCE。
	// 登录与工具探测并发是已知现象：登录会作废登录态缓存，探测可能先拿到 401，
	// 不影响保存结果；登录失败只写 mcpAuthErr，不撤销安装。
	if srv.Auth != nil && srv.Auth.Mode != "" && srv.URL != "" && client != nil {
		authID := srv.AuthID()
		auth := srv.Auth
		st.startMCPAuth(authID, func() error {
			return client.MCPSignInWithAuth(context.Background(), authID, srv.URL, auth)
		})
	}

	// 保存成功后立即探测新服务的工具列表，预设卡片上直接显示连接状态与工具数。
	startMCPToolProbe(st, getMCPUIState(st), srv)
}

// renderMCPCatalogStatusBadge 渲染预设卡片的连接状态徽标，按状态取主题色。
func renderMCPCatalogStatusBadge(c *ui.Context, key, text string, color ui.Color) {
	ui.Box(c.Key(key)).
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(color.Alpha(0.15)).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeCaption).
				FontWeight(500).
				TextColor(color)
		})
}

// mcpCatalogPageLoad 在渲染前为已安装预设补齐登录状态与首次工具探测，
// 让卡片渲染只读缓存，不在渲染路径里发起网络请求。
func mcpCatalogPageLoad(st *SettingsState, uiSt *mcpPageUIState, plan mcpCatalogPlan, client Client) {
	for _, group := range plan.groups {
		for _, row := range group.rows {
			if !row.installed || row.saved.Name == "" {
				continue
			}
			srv := row.saved
			if srv.URL != "" && (client == nil || !mcpServerSignedIn(st, uiSt, srv, client)) {
				continue
			}
			uiSt.mu.Lock()
			key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
			_, hasTools := uiSt.toolsCache[key]
			probing := uiSt.toolsLoading[key]
			probeErr := uiSt.toolsErr[key]
			uiSt.mu.Unlock()
			if !hasTools && !probing && probeErr == "" {
				startMCPToolProbe(st, uiSt, srv)
			}
		}
	}
}

// resolveMCPCatalogStatus 汇总已安装预设的连接状态，返回徽标文案、配色、错误详情与「重新检测」是否可用。
// 登录状态与工具探测都由 mcpCatalogPageLoad 在渲染前准备好，这里只读缓存。
func resolveMCPCatalogStatus(
	c *ui.Context,
	st *SettingsState,
	uiSt *mcpPageUIState,
	srv agentconf.MCPServer,
	client Client,
) (label string, color ui.Color, detail string, canReprobe bool) {
	if srv.URL != "" && client != nil && !mcpServerSignedIn(st, uiSt, srv, client) {
		return "未登录", ThemeWarning(c), "", false
	}

	uiSt.mu.Lock()
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	tools := uiSt.toolsCache[key]
	probing := uiSt.toolsLoading[key]
	probeErr := uiSt.toolsErr[key]
	uiSt.mu.Unlock()

	switch {
	case probing:
		return "连接中…", ThemeAccent(c), "", false
	case tools != nil:
		return fmt.Sprintf("已连接 · %d 个工具", len(tools)), ThemeSuccess(c), "", true
	case probeErr != "":
		return "连接失败", ThemeDanger(c), probeErr, true
	}

	return "连接中…", ThemeAccent(c), "", false
}
