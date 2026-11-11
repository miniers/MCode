package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// magpieModelPanelOptions 是公共 Magpie 模型面板的显示选项。
type magpieModelPanelOptions struct {
	// ShowDefault 表示显示默认模型下拉框；只有能保存默认模型的 agent 打开。
	ShowDefault bool
	// ShowTiers 表示渲染「档位映射」区块；只有写全局配置的 claude 打开。
	ShowTiers bool
	// Footer 是默认模型行或已启用标题下方的说明文字，为空时不渲染。
	Footer string
}

// magpieEnabledRow 是「已启用」区块里的一行：端点、端点侧模型 ID 与展示用名称。
type magpieEnabledRow struct {
	Endpoint        PiMagpieEndpoint
	EndpointModelID string
	ModelName       string
}

// renderMagpieModelPanel 渲染 agent 的 Magpie 模型面板：
// 「已启用」列表、默认模型与 agent 自身配置的折叠分组。首次渲染时异步加载端点。
func renderMagpieModelPanel(c *ui.Context, st *SettingsState, lib *modelsLibraryView, catalog ProviderInfo, agent string, opts magpieModelPanelOptions) {
	ns := agent
	endpoints, loading, loaded, errMsg, saveErr := magpieEndpointsFor(st, agent)

	if !loaded && !loading && errMsg == "" {
		magpieLoadFor(st, agent)()
	}

	var ps *modelsPageState
	var client Client
	if lib != nil {
		ps = lib.ps
		client = lib.client
	}
	hasGroups := len(endpoints) > 0

	// pi 以外的 agent 没有 Magpie 端点时就是普通供应商：不渲染刷新行与加载状态，
	// 直接用通用列表渲染，保证加载完成前后布局不变。
	if agent != "pi" && !hasGroups && ps != nil {
		renderProviderModelsList(c, st, ps, client, catalog)
		return
	}

	if loading && !loaded {
		renderPiMagpieNotice(c, ns+"-magpie-loading", "正在加载 Magpie 端点…", ThemeSecondaryText(c))
	}
	if errMsg != "" {
		renderPiMagpieNotice(c, ns+"-magpie-load-err", "加载 Magpie 端点失败: "+errMsg, ThemeDanger(c))
	}

	if ps == nil {
		// 没有模型库（例如非模型页的供应商卡片）时只显示状态，不渲染列表。
		return
	}

	if !hasGroups {
		// pi 没有 Magpie 端点：交给通用列表样式渲染。
		renderProviderModelsList(c, st, ps, client, catalog)
		return
	}

	allGroups := buildMagpieModelGroups(catalog.Models, endpoints, agent)
	// 写全局配置时 agent 自身配置组只是全局配置文件的回显，隐藏它。
	if agent != "pi" && magpieAgentMode(st, agent) == agentMagpieModeGlobal {
		allGroups = dropMagpieOwnGroup(allGroups)
	}

	searchWord := ""
	if lib != nil {
		searchWord = lib.search
	}
	var ownGroup *piModelGroup
	if len(allGroups) > 0 && allGroups[len(allGroups)-1].Endpoint == nil {
		g := allGroups[len(allGroups)-1]
		if groups := filterPiModelGroups([]piModelGroup{g}, searchWord); len(groups) > 0 {
			ownGroup = &groups[0]
		}
	}

	ui.Column(c.Key(agent + "-magpie-model-panel")).
		FillWidth().
		Children(func() {
			renderMagpieEnabledSection(c, st, ps, client, catalog, agent, endpoints, opts, loading, saveErr)
			// 档位映射区块插在这里：位于「已启用」与 agent 自身配置分组之间。
			if opts.ShowTiers {
				renderMagpieTierSection(c, st, agent, endpoints)
			}
			renderMagpieOwnModelGroup(c, st, ps, client, catalog, agent, endpoints, ownGroup, strings.TrimSpace(searchWord) != "")
		})
}

// renderMagpieEnabledSection 渲染「已启用」区块：标题行、已勾选的模型行与默认模型说明。
func renderMagpieEnabledSection(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, catalog ProviderInfo, agent string, endpoints []PiMagpieEndpoint, opts magpieModelPanelOptions, loading bool, saveErr map[string]string) {
	rows := magpieEnabledRows(catalog.Models, endpoints)
	addKey := "magpie-add-" + agent

	var addAnchor ui.Element
	ui.Column(c.Key("magpie-enabled-" + agent)).
		FillWidth().
		Gap(Space1).
		Children(func() {
			ui.Row(c.Key("magpie-enabled-head-"+agent)).
				FillWidth().
				Padding(Space4, Space5, Space2, Space5).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, fmt.Sprintf("已启用（%d）", len(rows))).
						FontSize(uiFont(TypeBody)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					ui.Row(c.Key("magpie-enabled-head-gap-" + agent)).Grow(1)
					renderMagpieRefreshButton(c, st, agent, "magpie-enabled-refresh-"+agent, loading)
					addAnchor = renderModelPickerAddButton(c, st, ps, addKey, "添加模型")
				})
			if !opts.ShowDefault && strings.TrimSpace(opts.Footer) != "" {
				ui.Text(c, opts.Footer).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Padding(0, Space5, Space3, Space5)
			}
			if len(rows) == 0 {
				ui.Text(c, "还没有启用模型，点「添加模型」从网关里选择").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					Padding(0, Space5, Space3, Space5)
			}
			for _, ep := range endpoints {
				// 端点出错时保留提示：已启用的行照旧渲染，用户仍能停用或取消收藏。
				if ep.Err != "" {
					ui.Text(c, "该端点不可用: "+ep.Err).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeDanger(c)).
						Padding(0, Space5, Space1, Space5)
				}
				if errText := saveErr[ep.ID]; errText != "" {
					ui.Text(c, "保存失败: "+errText).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeDanger(c)).
						Padding(0, Space5, Space1, Space5)
				}
			}
			for _, row := range rows {
				renderMagpieEnabledRow(c, st, ps, client, agent, row, opts.ShowDefault, len(endpoints) > 1)
			}
			if opts.ShowDefault {
				renderMagpieDefaultSummary(c, st, catalog, agent, endpoints, opts.Footer)
			}
			items, targets := magpieModelPickerItems(endpoints)
			onToggle := func(item modelPickerItem, checked bool) {
				target, ok := targets[item.Key]
				if !ok {
					return
				}
				if !checked {
					// 取消勾选一个已固定的模型时先移除固定，避免留下无法解析的模型。
					pin := ModelPin{Provider: agent, ModelID: target.providerID + "/" + item.ID}
					if modelPinPresent(ps.pins, pin) {
						ps.removePin(st, client, pin)
						// removePin 会关闭固定浮层；这里重新打开「添加模型」，让勾选可以连续操作。
						ps.openPopoverKey = addKey
					}
				}
				magpieToggleFor(st, agent)(target.endpointID, item.ID, checked)
			}
			renderModelPickerPopover(c, st, ps, addKey, addAnchor, items, "暂无可添加的模型", onToggle, modelPickerCustom{})
		})
}

// magpieEnabledRows 按端点顺序、端点内按 Selected 顺序列出已启用模型。
func magpieEnabledRows(catalog []ModelInfo, endpoints []PiMagpieEndpoint) []magpieEnabledRow {
	var rows []magpieEnabledRow
	for _, ep := range endpoints {
		for _, id := range ep.Selected {
			rows = append(rows, magpieEnabledRow{
				Endpoint:        ep,
				EndpointModelID: id,
				ModelName:       magpieEnabledModelName(catalog, ep, id),
			})
		}
	}
	return rows
}

// magpieEnabledModelName 返回已启用模型的展示名：端点名称优先，其次目录名称，最后用 ID。
func magpieEnabledModelName(catalog []ModelInfo, ep PiMagpieEndpoint, modelID string) string {
	for _, m := range ep.Models {
		if m.ID != modelID {
			continue
		}
		if name := strings.TrimSpace(m.Name); name != "" {
			return name
		}
		break
	}
	fullID := ep.ProviderID + "/" + modelID
	for _, m := range catalog {
		if m.ID != fullID {
			continue
		}
		if name := strings.TrimSpace(m.Name); name != "" {
			return name
		}
		break
	}
	return modelID
}

// renderMagpieEnabledRow 渲染一行已启用模型：名称、默认标签、ID 与停用按钮。
func renderMagpieEnabledRow(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, agent string, row magpieEnabledRow, showDefault, showEndpoint bool) {
	ep := row.Endpoint
	modelID := row.EndpointModelID
	name := row.ModelName
	pin := ModelPin{Provider: agent, ModelID: ep.ProviderID + "/" + modelID}
	key := "magpie-enabled-row-" + agent + "-" + ep.ID + "-" + modelID

	ui.Row(c.Key(key)).
		FillWidth().
		Padding(Space1, Space5).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Column(c.Key(key + "-text")).
				Grow(1).
				Gap(Space0).
				Children(func() {
					ui.Row(c.Key(key + "-name")).Gap(Space2).AlignItems(ui.Center).Children(func() {
						ui.Text(c, name).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
						if showDefault {
							if defEP, defModel, ok := agentMagpieCurrentDefault(st, agent); ok && defEP == ep.ID && defModel == modelID {
								ui.Text(c, "默认").FontSize(uiFont(TypeCaption)).TextColor(ThemeAccent(c))
							}
						}
					})
					ui.Text(c, modelID).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				})
			if showEndpoint {
				ui.Text(c, agentMagpieEndpointName(ep)).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}
			renderMagpieDisableButton(c, st, ps, client, agent, ep.ID, modelID, pin, name)
		})
}

// renderMagpieDisableButton 渲染停用按钮：已收藏的模型先取消收藏，再取消勾选。
func renderMagpieDisableButton(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, agent, endpointID, modelID string, pin ModelPin, name string) {
	btn := ui.ButtonBase(c.Key("magpie-disable-"+agent+"-"+endpointID+"-"+modelID)).
		Label("停用 "+name).
		Radius(RadiusXS).
		Padding(Space1, Space1)
	btn.OnClick(func() {
		if modelPinPresent(ps.pins, pin) {
			ps.removePin(st, client, pin)
		}
		magpieToggleFor(st, agent)(endpointID, modelID, false)
	})
	btn.Children(func() {
		color := ThemeSecondaryText(c)
		if btn.Hovered() {
			color = ThemeDanger(c)
		}
		ui.Icon(c, IconX()).Size(16, 16).TextColor(color)
	})
}

// magpieAddModelPin 把模型加入「你的模型」：与通用模型行的「+」行为保持一致。
// 这段逻辑来自 settings_page_models.go 的行内「+」，改成公共函数供新面板调用。
func magpieAddModelPin(st *SettingsState, ps *modelsPageState, client Client, pin ModelPin) {
	if client == nil || ps == nil || !ps.canEdit() || !canAddModel(ps.pins, ps.limit, pin) {
		return
	}
	pref, err := client.ModelPreferences(context.Background(), pin)
	if err == nil {
		var added bool
		added, err = client.AddModelToList(context.Background(), pin)
		if err == nil && added {
			ps.pins = append(ps.pins, pin)
			ps.preferences[pin.Key()] = pref
			ps.syncSettings(st)
		}
	}
	if err != nil {
		ps.opErr = "添加模型失败：" + err.Error()
	} else {
		ps.opErr = ""
		ps.refreshPins(st, client)
	}
	st.invalidate()
}

// renderMagpieDefaultSummary 渲染默认模型下拉框、说明与失效警示。
func renderMagpieDefaultSummary(c *ui.Context, st *SettingsState, catalog ProviderInfo, agent string, endpoints []PiMagpieEndpoint, footer string) {
	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	setting := state.setting
	setting.Agent = agent
	// 深拷贝端点，避免解锁后与后台保存结果时的字段写入共享底层数组。
	endpointsCopy := make([]PiMagpieEndpoint, len(setting.Endpoints))
	for i, ep := range setting.Endpoints {
		ep.Models = append([]PiMagpieModel(nil), ep.Models...)
		ep.Selected = append([]string(nil), ep.Selected...)
		endpointsCopy[i] = ep
	}
	setting.Endpoints = endpointsCopy
	st.mu.Unlock()

	endpointID, modelID, manual := agentMagpieEffectiveDefault(setting)
	stale := strings.TrimSpace(setting.DefaultModel) != "" && !manual

	autoLabel := "自动"
	if agentMagpieModeOf(setting) == agentMagpieModeInject {
		autoLabel = "使用 " + agent + " 自己的默认"
	} else if autoEP, autoModel := agentMagpieAutoDefault(setting); autoModel != "" {
		for _, ep := range endpoints {
			if ep.ID == autoEP {
				autoLabel = "自动（" + magpieEnabledModelName(catalog.Models, ep, autoModel) + "）"
				break
			}
		}
	}
	rows := magpieEnabledRows(catalog.Models, endpoints)
	labels := []string{autoLabel}
	counts := map[string]int{autoLabel: 1}
	for _, row := range rows {
		label := row.ModelName
		if len(endpoints) > 1 {
			label += " · " + agentMagpieEndpointName(row.Endpoint)
		}
		labels = append(labels, label)
		counts[label]++
	}
	sel := autoLabel
	for i, row := range rows {
		if counts[labels[i+1]] > 1 {
			labels[i+1] += " · " + row.EndpointModelID
		}
		if manual && row.Endpoint.ID == endpointID && row.EndpointModelID == modelID {
			sel = labels[i+1]
		}
	}
	ui.Row(c.Key("magpie-default-row-"+agent)).
		FillWidth().
		Padding(Space2, Space5).
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			ui.Text(c, "默认模型").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
			ui.Row(c.Key("magpie-default-gap-" + agent)).Grow(1)
			pick := ui.Select(c.Key("magpie-default-select-"+agent), &sel, labels).
				Label("选择默认模型").Width(220).Height(RowHeight)
			if pick.Changed() {
				if sel == labels[0] {
					st.setAgentMagpieDefault(agent, "", "")
				} else {
					for i, row := range rows {
						if sel == labels[i+1] {
							st.setAgentMagpieDefault(agent, row.Endpoint.ID, row.EndpointModelID)
							break
						}
					}
				}
			}
		})
	if strings.TrimSpace(footer) != "" {
		ui.Text(c, footer).
			FontSize(uiFont(TypeCaption)).
			TextColor(ThemeSecondaryText(c)).
			Padding(0, Space5, Space3, Space5)
	}
	if stale {
		ui.Text(c, "默认模型 "+strings.TrimSpace(setting.DefaultModel)+" 已停用，当前按自动规则选择").
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeWarning(c)).
			Padding(0, Space5, Space3, Space5)
	}
}

// renderMagpieTierSection 渲染「档位映射」区块：写全局配置时 Claude Code 各档位实际指向的模型。
// 这些档位由后端按默认模型自动推导，inject 模式与不支持写全局配置的 agent 不渲染。
func renderMagpieTierSection(c *ui.Context, st *SettingsState, agent string, endpoints []PiMagpieEndpoint) {
	var tiers []AgentMagpieTier
	st.mu.Lock()
	setting := st.agentMagpieFor(agent).setting
	if setting.Global.Supported && agentMagpieModeOf(setting) == agentMagpieModeGlobal && len(setting.Tiers) > 0 {
		tiers = append([]AgentMagpieTier(nil), setting.Tiers...)
	}
	st.mu.Unlock()
	if len(tiers) == 0 {
		return
	}

	ui.Column(c.Key("magpie-tiers-"+agent)).
		FillWidth().
		Padding(Space3, Space5, Space1, Space5).
		Gap(Space1).
		Children(func() {
			ui.Row(c.Key("magpie-tiers-head-" + agent)).
				FillWidth().
				Gap(Space1).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "档位映射").
						FontSize(uiFont(TypeBody)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, "（按默认模型自动推导）").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c))
				})
			for _, tier := range tiers {
				renderMagpieTierRow(c, agent, tier, endpoints)
			}
		})
}

// renderMagpieTierRow 渲染一行档位映射：/model 档位 → 指向的模型名。
func renderMagpieTierRow(c *ui.Context, agent string, tier AgentMagpieTier, endpoints []PiMagpieEndpoint) {
	modelID := strings.TrimSpace(tier.ModelID)
	name := modelID
	for _, ep := range endpoints {
		if ep.ID == tier.EndpointID {
			name = agentMagpieModelName(ep, modelID)
			break
		}
	}

	ui.Row(c.Key("magpie-tier-" + agent + "-" + tier.Tier)).
		FillWidth().
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			secondary := ThemeSecondaryText(c)
			ui.Text(c, "/model "+tier.Tier).
				Width(110).
				FontSize(uiFont(TypeFootnote)).
				TextColor(secondary)
			ui.Text(c, "→").
				FontSize(uiFont(TypeFootnote)).
				TextColor(secondary)
			if modelID == "" {
				ui.Text(c, "—（用 Claude Code 自带）").
					FontSize(uiFont(TypeFootnote)).
					TextColor(secondary)
				return
			}
			ui.Text(c, name).
				FontSize(uiFont(TypeBody)).
				TextColor(ThemePrimaryText(c)).
				SingleLine()
			if tier.Tier == "haiku" {
				ui.Text(c, "也用于后台小任务").
					FontSize(uiFont(TypeCaption)).
					TextColor(secondary)
			}
		})
}

// magpiePickerTarget 记录浮层里一行模型对应的端点，供勾选回调查回。
type magpiePickerTarget struct {
	endpointID string
	providerID string
}

// magpieModelPickerItems 把 Magpie 端点模型转换成通用浮层选项：多端点时按端点分段，
// 同时返回以选项 Key 为键的端点映射。
func magpieModelPickerItems(endpoints []PiMagpieEndpoint) ([]modelPickerItem, map[string]magpiePickerTarget) {
	multi := len(endpoints) > 1
	targets := make(map[string]magpiePickerTarget, len(endpoints)*4)
	var items []modelPickerItem
	for _, ep := range endpoints {
		selected := make(map[string]bool, len(ep.Selected))
		for _, id := range ep.Selected {
			selected[id] = true
		}
		section := ""
		if multi {
			section = agentMagpieEndpointName(ep)
		}
		for _, m := range ep.Models {
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = m.ID
			}
			key := ep.ID + "-" + m.ID
			targets[key] = magpiePickerTarget{endpointID: ep.ID, providerID: ep.ProviderID}
			items = append(items, modelPickerItem{
				Section: section,
				Key:     key,
				Name:    name,
				ID:      m.ID,
				Checked: selected[m.ID],
			})
		}
	}
	return items, targets
}

// magpieModelFamilyRank 返回系列标题的排序权重：Claude 在前，其他在最后。
func magpieModelFamilyRank(title string) int {
	switch title {
	case "Claude":
		return 0
	case "其他":
		return 2
	}
	return 1
}

// magpieModelFamily 从模型 ID 推导展示用系列名：去掉路由前缀后按开头的字母识别，
// 识别不了的归入「其他」。
func magpieModelFamily(id string) string {
	rest := strings.TrimSpace(id)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[i+1:]
	}
	lower := strings.ToLower(rest)
	known := []struct {
		prefix string
		title  string
	}{
		{"claude", "Claude"},
		{"gpt", "GPT"},
		{"glm", "GLM"},
		{"gemini", "Gemini"},
		{"deepseek", "DeepSeek"},
		{"qwen", "Qwen"},
		{"mimo", "MiMo"},
		{"kimi", "Kimi"},
		{"grok", "Grok"},
	}
	for _, k := range known {
		if strings.HasPrefix(lower, k.prefix) {
			return k.title
		}
	}
	return "其他"
}

// magpieOwnModelGroups 按 agent 保存自身配置组的展开状态。
var magpieOwnModelGroups = struct {
	sync.Mutex
	expanded map[string]bool
}{expanded: make(map[string]bool)}

// renderMagpieOwnModelGroup 渲染默认折叠的自身配置组，搜索命中时自动展开。
func renderMagpieOwnModelGroup(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, catalog ProviderInfo, agent string, endpoints []PiMagpieEndpoint, g *piModelGroup, filtering bool) {
	if g == nil || len(g.Rows) == 0 {
		return
	}
	key := agent + "-magpie-own"
	magpieOwnModelGroups.Lock()
	expanded := magpieOwnModelGroups.expanded[agent] || filtering
	magpieOwnModelGroups.Unlock()
	toggleLabel := "展开 " + g.Title
	if expanded {
		toggleLabel = "收起 " + g.Title
	}
	ui.Column(c.Key(key)).
		FillWidth().
		Padding(0, 0, Space4, 0).
		Gap(Space3).
		Children(func() {
			btn := ui.ButtonBase(c.Key(key+"-toggle")).
				Label(toggleLabel).
				FillWidth().
				Padding(Space2, Space5)
			btn.OnClick(func() {
				magpieOwnModelGroups.Lock()
				magpieOwnModelGroups.expanded[agent] = !expanded
				magpieOwnModelGroups.Unlock()
				st.invalidate()
			})
			btn.Children(func() {
				ui.Row(c.Key(key + "-title")).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
					ui.Text(c, g.Title).
						FontSize(uiFont(TypeBody)).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					ui.Text(c, fmt.Sprintf("%d 个模型", len(g.Rows))).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
					ui.Row(c.Key(key + "-gap")).Grow(1)
					icon := IconChevronRight()
					if expanded {
						icon = IconChevronDown()
					}
					ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				})
			})
			if !expanded {
				return
			}
			ui.Column(c.Key(key + "-list")).FillWidth().Children(func() {
				for i, r := range g.Rows {
					if i > 0 {
						renderSettingsDivider(c, fmt.Sprintf("div-%s-%d", key, i))
					}
					name := strings.TrimSpace(r.Model.Name)
					if name == "" {
						name = r.Model.ID
					}
					ui.Text(c, name).
						FontSize(uiFont(TypeBody)).
						TextColor(ThemeSecondaryText(c)).
						Padding(Space2, Space5)
				}
			})
		})
}
