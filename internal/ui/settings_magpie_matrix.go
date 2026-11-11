package mcodeui

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

// magpieBuiltinAgent 是内置引擎的 agent id：矩阵列始终存在且始终可勾选，
// 没有安装状态，也没有 inject、global、off 的模式区分。
const magpieBuiltinAgent = "mcode"

// magpieMatrixAgents 是模型矩阵的列顺序：内置引擎固定在第一列。
var magpieMatrixAgents = []string{magpieBuiltinAgent, "claude", "codex", "pi"}

func magpieInstalledAgents(st *SettingsState) []string {
	st.mu.Lock()
	cfg := st.cfg
	providerRows := make(map[string]ProviderSetting, len(st.providerRows.settings))
	for id, row := range st.providerRows.settings {
		providerRows[id] = row
	}
	st.mu.Unlock()
	enabled := providerEnabledMap(cfg, providerRows, nil)

	agents := make([]string, 0, len(magpieMatrixAgents))
	for _, agent := range magpieMatrixAgents {
		// 内置引擎不来自供应商列表，providerStatusFor 对它的结果不可靠，固定保留。
		if agent == magpieBuiltinAgent {
			agents = append(agents, agent)
			continue
		}
		if providerDisabled(enabled, agent) {
			continue
		}
		st.mu.Lock()
		_, known := st.providerRows.available[agent]
		st.mu.Unlock()
		status := providerStatusFor(st, agent)
		if !known || status.Ready || !strings.HasPrefix(status.Summary, "未安装") {
			agents = append(agents, agent)
		}
	}
	return agents
}

// magpieMatrixAgentTitle 返回矩阵列头与无障碍名用的 agent 名：
// 内置引擎直接用小写 id，其余沿用 Magpie 页的行标题。
func magpieMatrixAgentTitle(agent string) string {
	if agent == magpieBuiltinAgent {
		return magpieBuiltinAgent
	}
	return magpieAgentTitle(agent)
}

// magpieMatrixColWidth 是每个智能体列的固定宽度：表头、家族批量勾选与模型行共用，
// 保证各列勾选框严格竖直对齐。
const magpieMatrixColWidth float32 = 72

// magpieMatrixModel 是矩阵里的一行：模型本身与各 agent 是否勾选。
type magpieMatrixModel struct {
	model    PiMagpieModel
	selected map[string]bool
}

// magpieMatrixFamily 是同一家族的模型分组，标题显示家族名。
type magpieMatrixFamily struct {
	title  string
	models []magpieMatrixModel
}

// magpieMatrixGateway 是一个网关在矩阵里的数据：按 agent 保存端点快照与模型分组。
type magpieMatrixGateway struct {
	id        string
	name      string
	endpoints map[string]PiMagpieEndpoint
	families  []magpieMatrixFamily
}

// magpieMatrixData 汇总各 agent 的 Magpie 端点，按网关配置顺序返回模型矩阵。
func magpieMatrixData(st *SettingsState, query string) []magpieMatrixGateway {
	gateways, _ := magpieGateways(st)
	names := make(map[string]string, len(gateways))
	order := make(map[string]int, len(gateways))
	for i, gw := range gateways {
		names[gw.ID] = gw.Name
		order[gw.ID] = i
	}

	agents := magpieInstalledAgents(st)
	byID := make(map[string]*magpieMatrixGateway)
	for _, agent := range agents {
		st.mu.Lock()
		state := st.agentMagpieFor(agent)
		loaded, loading, loadErr := state.loaded, state.loading, state.err
		endpoints := make([]PiMagpieEndpoint, len(state.setting.Endpoints))
		for i, ep := range state.setting.Endpoints {
			ep.Models = append([]PiMagpieModel(nil), ep.Models...)
			ep.Selected = append([]string(nil), ep.Selected...)
			endpoints[i] = ep
		}
		st.mu.Unlock()
		if !loaded && !loading && loadErr == "" {
			st.loadAgentMagpie(agent)
		}
		for _, ep := range endpoints {
			gateway := byID[ep.ID]
			if gateway == nil {
				name := strings.TrimSpace(names[ep.ID])
				if name == "" {
					name = agentMagpieEndpointName(ep)
				}
				gateway = &magpieMatrixGateway{id: ep.ID, name: name, endpoints: make(map[string]PiMagpieEndpoint)}
				byID[ep.ID] = gateway
			}
			gateway.endpoints[agent] = ep
		}
	}

	var result []magpieMatrixGateway
	for _, gateway := range byID {
		models := make(map[string]*magpieMatrixModel)
		for _, agent := range agents {
			ep, ok := gateway.endpoints[agent]
			if !ok {
				continue
			}
			selected := make(map[string]bool, len(ep.Selected))
			for _, id := range ep.Selected {
				selected[id] = true
			}
			for _, model := range ep.Models {
				row := models[model.ID]
				if row == nil {
					row = &magpieMatrixModel{model: model, selected: make(map[string]bool)}
					models[model.ID] = row
				}
				if row.model.Name == "" {
					row.model.Name = model.Name
				}
				row.selected[agent] = selected[model.ID]
			}
		}

		familyMap := make(map[string][]magpieMatrixModel)
		for _, row := range models {
			name := strings.TrimSpace(row.model.Name)
			if name == "" {
				name = row.model.ID
			}
			if !modelMatchesQuery(name, row.model.ID, gateway.name, query) {
				continue
			}
			row.model.Name = name
			family := magpieModelFamily(row.model.ID)
			familyMap[family] = append(familyMap[family], *row)
		}
		for title, rows := range familyMap {
			// 组内按原始模型 ID 排序：很多模型的显示名重复，按名称排会显得无序。
			sort.Slice(rows, func(i, j int) bool {
				a := strings.ToLower(rows[i].model.ID)
				b := strings.ToLower(rows[j].model.ID)
				if a != b {
					return a < b
				}
				return rows[i].model.ID < rows[j].model.ID
			})
			gateway.families = append(gateway.families, magpieMatrixFamily{title: title, models: rows})
		}
		sort.Slice(gateway.families, func(i, j int) bool {
			a := magpieModelFamilyRank(gateway.families[i].title)
			b := magpieModelFamilyRank(gateway.families[j].title)
			if a != b {
				return a < b
			}
			return gateway.families[i].title < gateway.families[j].title
		})
		if len(gateway.families) > 0 || matrixGatewayHasError(*gateway) {
			result = append(result, *gateway)
		}
	}

	// 网关按配置顺序排列；没有配置记录的网关（例如只存在于某 agent 设置里的端点）排在最后。
	sort.Slice(result, func(i, j int) bool {
		oi, iConfigured := order[result[i].id]
		oj, jConfigured := order[result[j].id]
		if iConfigured && jConfigured {
			return oi < oj
		}
		if iConfigured != jConfigured {
			return iConfigured
		}
		return result[i].id < result[j].id
	})
	return result
}

// magpieMatrixCell 画矩阵里一个智能体列：固定宽度、内容水平居中，空单元格也占位。
func magpieMatrixCell(c *ui.Context, key string, children func()) ui.Element {
	cell := ui.Row(c.Key(key)).
		Width(magpieMatrixColWidth).
		Shrink(0).
		Justify(ui.Center).
		AlignItems(ui.Center)
	cell.Children(children)
	return cell
}

// magpieMatrixDefaultBadge 把默认模型所属的智能体合并成一段胶囊文字，
// 例如「Claude · pi 默认」；没有默认模型时返回空串。
func magpieMatrixDefaultBadge(agents []string) string {
	if len(agents) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(agents))
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		if seen[agent] {
			continue
		}
		seen[agent] = true
		names = append(names, magpieMatrixAgentTitle(agent))
	}
	return strings.Join(names, " · ") + " 默认"
}

// magpieMatrixModelIDLabel 返回模型名后要显示的原始 ID。
// 名称为空或与 ID 相同时（矩阵已把名称回退成 ID）返回空串，避免重复显示。
func magpieMatrixModelIDLabel(m PiMagpieModel) string {
	id := strings.TrimSpace(m.ID)
	name := strings.TrimSpace(m.Name)
	if id == "" || name == "" || id == name {
		return ""
	}
	return id
}

// magpieMatrixErrorLines 把各 agent 的错误按文字去重：同一段文字只输出一行，
// 只有部分 agent 出错时在行首标出 agent 名；输出顺序按 magpieMatrixAgents。
func magpieMatrixErrorLines(agents []string, errs map[string]string) []string {
	groups := make(map[string][]string, len(errs))
	order := make([]string, 0, len(errs))
	for _, agent := range agents {
		text := strings.TrimSpace(errs[agent])
		if text == "" {
			continue
		}
		if _, ok := groups[text]; !ok {
			order = append(order, text)
		}
		groups[text] = append(groups[text], magpieMatrixAgentTitle(agent))
	}
	lines := make([]string, 0, len(order))
	for _, text := range order {
		names := groups[text]
		if len(names) == len(agents) {
			lines = append(lines, text)
			continue
		}
		lines = append(lines, strings.Join(names, "、")+"："+text)
	}
	return lines
}

// retryMagpieMatrixAgent 重新加载某个 agent 的 Magpie 端点，沿用各 agent 原有的加载入口。
func retryMagpieMatrixAgent(st *SettingsState, agent string) {
	if agent == "pi" {
		st.mu.Lock()
		st.piMagpie.err = ""
		st.piMagpie.loaded = false
		st.mu.Unlock()
		st.loadPiMagpieEndpoints()
	}
	st.mu.Lock()
	st.agentMagpieFor(agent).err = ""
	st.agentMagpieFor(agent).loaded = false
	st.mu.Unlock()
	st.loadAgentMagpie(agent)
}

// renderMagpieModelMatrix 画 Magpie 页的模型矩阵：搜索框、每个网关的模型家族与逐列勾选。
func renderMagpieModelMatrix(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	query := &st.magpieMatrixSearch
	st.mu.Unlock()

	agents := magpieInstalledAgents(st)
	for _, agent := range agents {
		ensureAgentMagpieLoaded(st, agent)
	}
	ui.Column(c.Key("magpie-model-matrix")).FillWidth().Gap(Space2).Padding(Space0, Space0, Space3, Space0).Children(func() {
		ui.Row(c.Key("magpie-matrix-search")).FillWidth().AlignItems(ui.Center).Gap(Space2).Padding(Space2, Space3).Children(func() {
			// 复用供应商页的搜索框，避免在 settings_controls.go 之外直接调用 ui.TextInput。
			renderProviderModelSearchBox(c, st, "magpie-matrix", query).Grow(1)
		})
		loadErrs := make(map[string]string, len(agents))
		matrixErrs := make(map[string]string, len(agents))
		allLoaded := true
		for _, agent := range agents {
			_, loading, loaded, loadErr, _ := magpieEndpointsFor(st, agent)
			st.mu.Lock()
			state := st.agentMagpieFor(agent)
			matrixErr := state.matrixErr
			if state.loading || !state.loaded || state.err != "" {
				allLoaded = false
			}
			st.mu.Unlock()
			if loading || !loaded {
				allLoaded = false
			}
			if !loaded {
				ui.Text(c, "正在加载…").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					PaddingX(settingsRowPadX)
			}
			if loadErr != "" {
				loadErrs[agent] = "加载失败：" + loadErr
			}
			if matrixErr != "" {
				matrixErrs[agent] = matrixErr
			}
		}
		if lines := magpieMatrixErrorLines(agents, loadErrs); len(lines) > 0 {
			ui.Row(c.Key("magpie-matrix-load-err")).Gap(Space2).PaddingX(settingsRowPadX).Children(func() {
				ui.Column(c.Key("magpie-matrix-load-err-lines")).Gap(Space1).Children(func() {
					for _, line := range lines {
						ui.Text(c, line).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
					}
				})
				retry := ui.ButtonBase(c.Key("retry-magpie-matrix")).Label("重试")
				if retry.Clicked() {
					for _, agent := range agents {
						if loadErrs[agent] == "" {
							continue
						}
						retryMagpieMatrixAgent(st, agent)
					}
				}
				retry.Children(func() {
					ui.Text(c, "重试").FontSize(uiFont(TypeFootnote)).TextColor(ThemeAccent(c))
				})
			})
		}
		for _, line := range magpieMatrixErrorLines(agents, matrixErrs) {
			ui.Text(c, line).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeDanger(c)).
				PaddingX(settingsRowPadX)
		}
		ui.Row(c.Key("magpie-matrix-head")).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center).
			Padding(Space2, settingsRowPadX).
			Children(func() {
				ui.Text(c, "模型").
					Grow(1).
					FontSize(uiFont(TypeFootnote)).
					FontWeight(600).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
				for _, agent := range agents {
					magpieMatrixCell(c, "magpie-matrix-head-"+agent, func() {
						ui.Text(c, magpieMatrixAgentTitle(agent)).
							FontSize(uiFont(TypeFootnote)).
							FontWeight(600).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
					})
				}
			})
		renderSettingsDivider(c, "magpie-matrix-head-divider")
		gateways := magpieMatrixData(st, *query)
		if len(gateways) == 0 {
			if len(agents) > 0 && allLoaded && len(loadErrs) == 0 && strings.TrimSpace(*query) == "" {
				ui.Text(c, "网关没有返回模型").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					PaddingX(settingsRowPadX)
			}
			return
		}
		showGateway := len(gateways) > 1
		firstFamily := true
		ui.Scroll(c.Key("magpie-matrix-scroll")).MaxHeight(640).FillWidth().Children(func() {
			ui.Column(c.Key("magpie-matrix-groups")).FillWidth().Gap(Space1).Children(func() {
				for _, gateway := range gateways {
					if showGateway {
						ui.Text(c, gateway.name).
							FontSize(uiFont(TypeFootnote)).
							FontWeight(600).
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Padding(Space2, settingsRowPadX)
					}
					epErrs := make(map[string]string, len(gateway.endpoints))
					for _, agent := range agents {
						if ep, ok := gateway.endpoints[agent]; ok && ep.Err != "" {
							epErrs[agent] = ep.Err
						}
					}
					for _, line := range magpieMatrixErrorLines(agents, epErrs) {
						ui.Text(c, line).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeDanger(c)).
							PaddingX(settingsRowPadX)
					}
					for _, family := range gateway.families {
						renderMagpieMatrixFamily(c, st, gateway, family, *query, !firstFamily)
						firstFamily = false
					}
				}
			})
		})
	})
}

// renderMagpieMatrixFamily 画一个模型家族：家族标题行带批量勾选，下面逐行列出模型。
// separated 为真时在家族上方留出间距，把不同模型组隔开。
func renderMagpieMatrixFamily(c *ui.Context, st *SettingsState, gateway magpieMatrixGateway, family magpieMatrixFamily, query string, separated bool) {
	agents := magpieInstalledAgents(st)
	ui.Column(c.Key("magpie-family-" + gateway.id + "-" + family.title)).FillWidth().Children(func() {
		if separated {
			ui.Box(c.Key("magpie-family-gap-" + gateway.id + "-" + family.title)).Height(Space2)
		}
		ui.Row(c.Key("magpie-family-head-"+gateway.id+"-"+family.title)).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center).
			Padding(Space1, settingsRowPadX).
			Children(func() {
				ui.Text(c, family.title).
					Grow(1).
					FontSize(uiFont(TypeFootnote)).
					FontWeight(600).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
				for _, agent := range agents {
					checked, total, picked := true, 0, 0
					for _, row := range family.models {
						ep, ok := gateway.endpoints[agent]
						if !ok {
							continue
						}
						if !matrixEndpointHasModel(ep, row.model.ID) {
							continue
						}
						total++
						if row.selected[agent] {
							picked++
						} else {
							checked = false
						}
					}
					checked = checked && total > 0
					mixed := picked > 0 && picked < total
					magpieMatrixCell(c, "magpie-family-bulk-"+gateway.id+"-"+family.title+"-"+agent, func() {
						value := checked
						if total == 0 {
							return
						}
						label := "批量切换 " + magpieMatrixAgentTitle(agent) + " " + family.title
						if mixed {
							label += "（部分选中）"
						}
						box := ui.Checkbox(c.Key("magpie-bulk-"+gateway.id+"-"+family.title+"-"+agent), &value, "").
							Label(label)
						if mixed {
							box = box.Mixed()
						}
						if box.Changed() {
							saveMagpieMatrixFamily(st, agent, gateway, family, value, query)
						}
					})
				}
			})
		for _, row := range family.models {
			modelRow := ui.Row(c.Key("magpie-model-"+gateway.id+"-"+row.model.ID)).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Padding(Space1, settingsRowPadX).
				MinHeight(RowHeight)
			if modelRow.Hovered() {
				modelRow.Background(ThemeHover(c)).Radius(RadiusSM)
			}
			modelRow.Children(func() {
				ui.Row(c.Key("magpie-model-name-" + gateway.id + "-" + row.model.ID)).
					Grow(1).
					Shrink(1).
					MinWidth(0).
					AlignItems(ui.Center).
					Gap(Space1).
					Children(func() {
						ui.Text(c, row.model.Name).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemePrimaryText(c)).
							SingleLine().
							Shrink(1).
							MinWidth(0)
						if id := magpieMatrixModelIDLabel(row.model); id != "" {
							ui.Text(c, id).
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeTertiaryText(c)).
								SingleLine().
								Shrink(1).
								MinWidth(0)
						}
						var defaults []string
						for _, agent := range agents {
							ep, ok := gateway.endpoints[agent]
							if ok && matrixDefaultMatches(st, agent, ep, row.model.ID) {
								defaults = append(defaults, agent)
							}
						}
						if badge := magpieMatrixDefaultBadge(defaults); badge != "" {
							ui.Text(c, badge).
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeAccent(c)).
								Background(ThemeAccent(c).Alpha(0.12)).
								Radius(RadiusPill).
								Padding(Space0, settingsPillPadX).
								SingleLine().
								Shrink(0)
						}
					})
				for _, agent := range agents {
					magpieMatrixCell(c, "magpie-cell-"+gateway.id+"-"+row.model.ID+"-"+agent, func() {
						ep, ok := gateway.endpoints[agent]
						if !ok || !matrixEndpointHasModel(ep, row.model.ID) {
							return
						}
						st.mu.Lock()
						saving := st.agentMagpieFor(agent).modelSaving[ep.ID]
						st.mu.Unlock()
						value := row.selected[agent]
						box := ui.Checkbox(c.Key("magpie-checkbox-"+gateway.id+"-"+row.model.ID+"-"+agent), &value, "").
							Label("切换 " + magpieMatrixAgentTitle(agent) + " " + row.model.Name)
						if saving {
							box.Disabled(true)
						}
						if box.Changed() {
							magpieToggleFor(st, agent)(ep.ID, row.model.ID, value)
						}
					})
				}
			})
		}
	})
}

// matrixEndpointHasModel 报告该端点是否提供某个模型（已选列表也算，兼容模型列表不全的情况）。
func matrixEndpointHasModel(ep PiMagpieEndpoint, modelID string) bool {
	for _, model := range ep.Models {
		if model.ID == modelID {
			return true
		}
	}
	for _, id := range ep.Selected {
		if id == modelID {
			return true
		}
	}
	return false
}

// matrixGatewayHasError 报告网关是否有某个 agent 的端点加载失败，失败时仍要展示错误。
func matrixGatewayHasError(gateway magpieMatrixGateway) bool {
	for _, ep := range gateway.endpoints {
		if ep.Err != "" {
			return true
		}
	}
	return false
}

// matrixDefaultMatches 报告某端点的模型是否为该 agent 当前生效的默认模型。
func matrixDefaultMatches(st *SettingsState, agent string, ep PiMagpieEndpoint, modelID string) bool {
	st.mu.Lock()
	setting := st.agentMagpieFor(agent).setting
	st.mu.Unlock()
	defaultEndpointID, defaultModelID, _ := agentMagpieEffectiveDefault(setting)
	return defaultEndpointID == ep.ID && defaultModelID == modelID
}

// saveMagpieMatrixFamily 把家族内可见模型的勾选整体设为 selected，并保留家族外的已有勾选。
func saveMagpieMatrixFamily(st *SettingsState, agent string, gateway magpieMatrixGateway, family magpieMatrixFamily, selected bool, query string) {
	ep, ok := gateway.endpoints[agent]
	if !ok {
		return
	}
	visible := make(map[string]bool)
	for _, row := range family.models {
		visible[row.model.ID] = true
	}
	picked := make(map[string]bool)
	for _, id := range ep.Selected {
		picked[id] = true
	}
	for id := range visible {
		if selected {
			picked[id] = true
		} else {
			delete(picked, id)
		}
	}
	ids := make([]string, 0, len(picked))
	seen := make(map[string]bool)
	for _, model := range ep.Models {
		if picked[model.ID] && !seen[model.ID] {
			ids = append(ids, model.ID)
			seen[model.ID] = true
		}
	}
	for _, id := range ep.Selected {
		if picked[id] && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	st.setAgentMagpieEndpointModels(agent, ep.ID, ids)
}

// renderMagpieAgentDefaultPicker 画某 agent 的默认模型选择器：自动或手选某个端点的模型。
func renderMagpieAgentDefaultPicker(c *ui.Context, st *SettingsState, agent string, endpoints []PiMagpieEndpoint, setting AgentMagpieSetting) {
	rows := magpieEnabledRows(nil, endpoints)
	labels := []string{"自动"}
	for _, row := range rows {
		name := row.ModelName
		if len(endpoints) > 1 {
			name += " · " + agentMagpieEndpointName(row.Endpoint)
		}
		labels = append(labels, name)
	}
	selected := labels[0]
	endpointID, modelID, manual := agentMagpieEffectiveDefault(setting)
	if manual {
		for i, row := range rows {
			if row.Endpoint.ID == endpointID && row.EndpointModelID == modelID {
				selected = labels[i+1]
				break
			}
		}
	}
	ui.Row(c.Key("magpie-default-" + agent)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space2).
		MinHeight(RowHeight).
		Children(func() {
			ui.Text(c, "默认模型").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
			pick := ui.Select(c.Key("magpie-default-select-"+agent), &selected, labels).
				Label("选择默认模型").
				Width(260).
				Height(RowHeight)
			if !pick.Changed() {
				return
			}
			if selected == labels[0] {
				st.setAgentMagpieDefault(agent, "", "")
				return
			}
			for i, row := range rows {
				if selected == labels[i+1] {
					st.setAgentMagpieDefault(agent, row.Endpoint.ID, row.EndpointModelID)
					break
				}
			}
		})
}
