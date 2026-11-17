package mcodeui

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// modelPickerSideFromTab 把分段 ID 换算成侧别；空串按主导侧处理（与默认分段一致）。
func modelPickerSideFromTab(tab string) ModelPickerSide {
	if tab == "bees" {
		return ModelPickerSideBees
	}
	return ModelPickerSideLead
}

// listOpen 判断指定侧别的二级模型列表页是否展开。
// modelPickerAgentModels 返回竖栏某个 Agent 页要列出的全部模型：配置里的精选在前，
// 其余按目录顺序补齐，保证该 Agent 的每个模型都能在一级页点到。
func modelPickerAgentModels(agent ProviderInfo, pinned []modelPickerPinnedEntry) []modelPickerPinnedEntry {
	out := make([]modelPickerPinnedEntry, 0, len(agent.Models))
	seen := make(map[string]bool, len(agent.Models))
	for _, entry := range pinned {
		if entry.Provider.ID != agent.ID || seen[entry.Model.ID] {
			continue
		}
		seen[entry.Model.ID] = true
		out = append(out, modelPickerPinnedEntry{Provider: agent, Model: entry.Model})
	}
	for _, m := range agent.Models {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, modelPickerPinnedEntry{Provider: agent, Model: m})
	}
	return out
}

// providerByID 在供应商列表里按 ID 查找；找不到返回 false。
// providerByID 在供应商列表里按 ID 查找；找不到返回 false。
func providerByID(providers []ProviderInfo, id string) (ProviderInfo, bool) {
	for _, prov := range providers {
		if prov.ID == id {
			return prov, true
		}
	}
	return ProviderInfo{}, false
}

// 判定条件与 renderModelPickerCard 中的 disabled 保持一致。
// renderModelPickerListPage 渲染模型列表选择页（步骤 2~8：一级精选模型平铺，二级更多模型按 provider 分组）。
// renderModelPickerListPage 渲染模型列表选择页：顶部搜索框，其下左侧 Agent 图标竖栏与右侧所选 Agent 的模型列表。
func renderModelPickerListPage(c *ui.Context, p ModelPickerProps, state *ModelPickerState, side ModelPickerSide) {
	scopeKey := p.ScopeKey
	// 分组保留当前会话正在使用的供应商，精选仅显示已启用供应商的模型。
	// 只影响展示，配置里的 ModelList 不做任何改动。
	disabledProviders := modelPickerDisabledProviders(p, p.Selection.Provider, p.Bees.Provider)
	providers := p.Providers
	if len(disabledProviders) > 0 {
		providers = filterProvidersByEnabled(providers, disabledProviders)
	}
	providers = filterModelPickerMagpie(providers, p.MagpieFilter)
	pinned := modelPickerPinnedEntries(p.Pins, filterProvidersByEnabled(providers, p.ProviderEnabled))
	// state 为 nil 时沿用包内按 key 复用的状态，避免搜索框绑定空指针。
	if state == nil {
		state = modelPickerStateFor(p.KeyPrefix + p.ScopeKey)
	}
	query := state.searchQuery
	searching := strings.TrimSpace(query) != ""
	// 正在为某个档位选模型：编辑状态记在面板状态里，选中模型后落到该档位而不是会话子代理模型。
	editingTier := ""
	if side == ModelPickerSideBees {
		editingTier = state.TierEditing
	}

	onBack := func() {
		state.setListOpen(scopeKey, side, false)
		// 返回后下次打开不残留旧查询与旧 Agent。
		state.searchQuery = ""
		state.activeAgent = ""
		modelPickerInvalidate(p)
	}

	onPick := func(prov ProviderInfo, m ModelInfo) {
		// 偏好保存期间模型列表整体不可选，给出原因而不是静默忽略点击。
		if effortCardLockedReason(p, side) != "" {
			return
		}
		if side == ModelPickerSideBees && editingTier != "" {
			tierID := editingTier
			state.searchQuery = ""
			state.activeAgent = ""
			if p.OnSetTierOverride != nil {
				p.OnSetTierOverride(tierID, &config.SwarmTierOverride{
					Provider: prov.ID,
					Model:    m.ID,
				})
			}
			modelPickerInvalidate(p)
			return
		}
		state.setListOpen(scopeKey, side, false)
		// 选中后清空查询与所选 Agent，下次打开列表不残留旧状态。
		state.searchQuery = ""
		state.activeAgent = ""
		if p.OnPickModel != nil {
			p.OnPickModel(side, prov, m)
		}
	}

	selection := p.Selection
	if side == ModelPickerSideBees {
		selection = p.Bees
	}
	// 正在为档位选模型：竖栏初始落在该档位当前模型所属 Agent，而不是会话正在用的 Agent。
	if side == ModelPickerSideBees && editingTier != "" {
		for _, tier := range p.Tiers {
			if tier.ID != editingTier {
				continue
			}
			if prov, _, _, _ := effectiveTierModel(tier, p.PairTierOverrides, p.TierOverrides); prov != "" {
				selection.Provider = prov
			}
			break
		}
	}
	activeAgent := modelPickerActiveAgent(state, providers, selection.Provider)

	// 竖栏与正文都从顶部开始排：竖栏只有几枚图标，居中会在顶部留出空白。
	page := ui.Row(c.Key(p.KeyPrefix + "model-list-page")).
		Gap(Space0).
		AlignItems(ui.Start).
		FillWidth()
	page.Children(func() {
		// 竖栏常驻左侧，只有右侧列表滚动：滚动后 Agent 仍可一键切换。
		if !searching {
			renderModelPickerAgentRail(c, p, state, side, selection.Provider, providers, activeAgent, side == ModelPickerSideLead && p.HasMessages)
		}
		// 换页过渡只作用在右侧正文上：竖栏常驻不动，也不和卡片共用同一动画通道。
		body := ui.Column(c.Key(p.KeyPrefix + "model-list-column")).
			MinWidth(0).
			Grow(1)
		body.Children(func() {
			// 搜索框固定在正文顶部：列表滚动时返回按钮与搜索框都不动。
			ui.Row(c.Key(p.KeyPrefix+"model-list-search-row")).
				AlignItems(ui.Center).
				Gap(Space2).
				Padding(Space2, Space3, Space1, Space1).
				FillWidth().
				Children(func() {
					renderModelPickerBackButton(c, p.KeyPrefix, onBack)
					SearchField(c, TextFieldProps{Key: p.KeyPrefix + "model-list-search", Value: &state.searchQuery, Label: "model-list-search-input", Placeholder: "搜索模型", FillWidth: true, AutoFocus: true, OnChange: func(string) { modelPickerInvalidate(p) }})
				})
			renderModelPickerListRegion(c, p, state, side, selection, activeAgent, searching, query, providers, pinned, onPick)
		})
		modelPickerPageTransition(c, body, state, modelPickerPageDepth(p, state, side))
	})
}

// renderModelPickerListRegion 渲染正文下方的列表：搜索态跨 Agent 列出命中项，
// 否则列出竖栏所选 Agent 的全部模型。滚动只发生在这里，竖栏与搜索框保持可见。
// renderModelPickerListRegion 渲染正文下方的列表：搜索态跨 Agent 列出命中项，
// 否则列出竖栏所选 Agent 的全部模型。滚动只发生在这里，竖栏与搜索框保持可见。
func renderModelPickerListRegion(c *ui.Context, p ModelPickerProps, state *ModelPickerState, side ModelPickerSide,
	selection ModelSelection, activeAgent string, searching bool, query string,
	providers []ProviderInfo, pinned []modelPickerPinnedEntry, onPick func(ProviderInfo, ModelInfo)) {
	var list ui.Element
	if isAppFlyoutServices(c.Services()) && p.NativeMenu == nil {
		list = ui.Column(c.Key(p.KeyPrefix + "model-list-rows"))
	} else {
		list = ui.Scroll(c.Key(p.KeyPrefix+"model-list-rows")).MaxHeight(400).ScrollbarInsets(4, 0, 4, 0)
	}
	list.MinWidth(0).Children(func() {
		if searching || activeAgent == "" {
			renderModelPickerSearchResults(c, p, side, providers, query, onPick)
			return
		}
		agent, ok := providerByID(providers, activeAgent)
		if !ok {
			return
		}
		renderModelPickerAgentModels(c, p, state, selection, side, agent, pinned, onPick)
	})
}

// renderModelPickerAgentModels 渲染所选 Agent 的一页：为档位选模型时先给 banner，
// 主导侧顶部保留配对分组，执行侧保留档位分组，其后是该 Agent 的全部模型。
// renderModelPickerAgentModels 渲染所选 Agent 的一页：为档位选模型时先给 banner，
// 主导侧顶部保留配对分组，执行侧保留档位分组，其后是该 Agent 的全部模型。
func renderModelPickerAgentModels(c *ui.Context, p ModelPickerProps, state *ModelPickerState, selection ModelSelection,
	side ModelPickerSide, agent ProviderInfo, pinned []modelPickerPinnedEntry,
	onPick func(ProviderInfo, ModelInfo)) {
	ui.Column(c.Key(p.KeyPrefix+"model-list-items")).
		Gap(Space0).
		MinWidth(0).
		Padding(0, 0, Space2, 0).
		FillWidth().
		Children(func() {
			editingTier := side == ModelPickerSideBees && state != nil && state.TierEditing != ""
			if editingTier {
				renderTierEditingBanner(c, p, state)
			}
			// 主导侧列表顶部保留就绪的 Swarm 配对模板：点击一键套用并关闭整个弹层。
			if side == ModelPickerSideLead {
				renderModelPickerPairsSection(c, p, state)
			}
			// 执行侧先给出档位分组：「跟随主导模型」入口在竖栏顶部，不再占列表行。
			if side == ModelPickerSideBees && len(p.Tiers) > 0 {
				renderModelPickerTiersSection(c, p, state)
			}
			renderModelPickerAgentRows(c, p, selection, side, agent, pinned, onPick)
		})
}

// renderModelPickerPairsSection 渲染主导侧列表顶部的 Swarm 配对分组：
// 只有存在就绪配对模板时出现，选中项按当前两侧选择解析。
// renderModelPickerPairsSection 渲染主导侧列表顶部的 Swarm 配对分组：
// 只有存在就绪配对模板时出现，选中项按当前两侧选择解析。
func renderModelPickerPairsSection(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	if (p.Mode != ModelPickerSwarm && p.OnApplyPair == nil) || len(p.Pairs) == 0 {
		return
	}
	curPair, hasCurPair := SwarmPair{}, false
	if p.Mode != ModelPickerSwarm {
		curPair, hasCurPair = SwarmPair{ID: p.PairID}, p.PairID != ""
	} else if p.SwarmOn {
		curPair, hasCurPair = currentSwarmPairFor(p.Pairs, p.PairID, p.Selection.Provider, p.Selection.Model, p.Bees.Provider, p.Bees.Model)
	}
	renderProviderGroupHeader(c, p.KeyPrefix+"pair-group-header", "配对")
	for idx, pair := range p.Pairs {
		pair := pair
		selected := hasCurPair && curPair.ID == pair.ID
		onEnter := func() {
			// 应用配对后关闭整个弹层。
			state.Reset()
			clearModelCapsuleSnapshot(p.ScopeKey)
			if p.OnApplyPair != nil {
				p.OnApplyPair(pair)
			}
		}
		renderModelListPairRow(c, p, fmt.Sprintf("%spair-row-%d", p.KeyPrefix, idx), pair, selected, onEnter)
	}
	renderProviderGroupHeader(c, p.KeyPrefix+"pair-models-header", "模型")
}

// renderModelPickerBackButton 渲染搜索框左侧的返回按钮：回到该侧的推理强度卡片。
// renderModelPickerBackButton 渲染搜索框左侧的返回按钮：回到该侧的推理强度卡片。
func renderModelPickerBackButton(c *ui.Context, keyPrefix string, onBack func()) {
	btn := ui.ButtonBase(c.Key(keyPrefix+"model-list-back")).
		Label("返回").
		Tooltip("返回推理强度").
		Size(ControlMD, ControlMD).
		Radius(RadiusSM).
		Shrink(0).
		Center().
		Background(ui.Transparent)
	if btn.Hovered() {
		btn.Background(ColorHoverDark)
	}
	btn.OnClick(onBack)
	btn.Children(func() {
		ui.Icon(c, iconChevronLeft).Size(IconSM, IconSM).TextColor(modelPickerTextSecondary)
	})
}

// modelPickerAgentEntries 返回竖栏要列出的 Agent：有模型的供应商在前，目录为空的供应商排在末尾
// （竖栏里它们不可点，Tooltip 给出原因）。当前 Agent 不在启用范围内时也保留，便于看到正在使用的模型。
// modelPickerAgentEntries 返回竖栏要列出的 Agent：有模型的供应商在前，目录为空的供应商排在末尾
// （竖栏里它们不可点，Tooltip 给出原因）。当前 Agent 不在启用范围内时也保留，便于看到正在使用的模型。
func modelPickerAgentEntries(providers []ProviderInfo, current string) []ProviderInfo {
	withModels := make([]ProviderInfo, 0, len(providers))
	empty := make([]ProviderInfo, 0, 4)
	hasCurrent := false
	for _, prov := range providers {
		if prov.ID == current {
			hasCurrent = true
		}
		if len(prov.Models) == 0 {
			empty = append(empty, prov)
			continue
		}
		withModels = append(withModels, prov)
	}
	if hasCurrent || strings.TrimSpace(current) == "" {
		return append(withModels, empty...)
	}
	return append(withModels, append([]ProviderInfo{{ID: current, Name: providerDisplayName(current)}}, empty...)...)
}

// modelPickerActiveAgent 返回竖栏选中的 Agent：上次点选的若仍可用则沿用，
// 否则回退到当前选中的模型所属 Agent，再回退到第一个带模型的 Agent。
// modelPickerActiveAgent 返回竖栏选中的 Agent：上次点选的若仍可用则沿用，
// 否则回退到当前选中的模型所属 Agent，再回退到第一个带模型的 Agent。
func modelPickerActiveAgent(state *ModelPickerState, providers []ProviderInfo, current string) string {
	entries := modelPickerAgentEntries(providers, current)
	for _, prov := range entries {
		if prov.ID == state.activeAgent {
			return prov.ID
		}
	}
	for _, prov := range entries {
		if prov.ID == current {
			return prov.ID
		}
	}
	if len(entries) > 0 {
		return entries[0].ID
	}
	return ""
}

// renderModelPickerAgentRail 渲染左侧 Agent 竖栏：每个 Agent 一枚彩色图标，选中项为圆角实底，
// 悬停有淡色底；点击有模型的 Agent 让右侧列表切到它的模型，目录为空的 Agent 不可点。
// renderModelPickerAgentRail 渲染左侧 Agent 竖栏：每个 Agent 一枚彩色图标，选中项为圆角实底，
// 悬停有淡色底；点击有模型的 Agent 让右侧列表切到它的模型，目录为空的 Agent 不可点。
func renderModelPickerAgentRail(c *ui.Context, p ModelPickerProps, state *ModelPickerState, side ModelPickerSide, current string, providers []ProviderInfo, active string, lockAgents bool) {
	rail := ui.Column(c.Key(p.KeyPrefix+"agent-rail")).
		Padding(Space2, Space2).
		Gap(Space0).
		Shrink(0)
	if !isAppFlyoutServices(c.Services()) || p.NativeMenu != nil {
		rail = ui.Scroll(c.Key(p.KeyPrefix+"agent-rail-scroll")).
			MaxHeight(400).
			ScrollbarInsets(4, 0, 4, 0).
			Shrink(0)
	} else {
		rail.MaxHeight(400)
	}
	rail.Children(func() {
		// 竖栏顶部先给「跟随主导模型」，再用一条分隔线把「不指定模型」与具体 Agent 分开。
		// 只有执行侧有「跟随主导」这一选项，主导侧不出现。
		if p.Mode == ModelPickerSwarm && side == ModelPickerSideBees {
			renderModelPickerFollowLeadRailItem(c, p, state)
			ui.Box(c.Key(p.KeyPrefix+"agent-rail-divider")).
				Height(Hairline).
				Margin(Space2, Space2).
				FillWidth().
				Background(ThemeBorder(c))
		}
		entries := modelPickerAgentEntries(providers, current)
		ui.Column(c.Key(p.KeyPrefix + "agent-rail-items")).
			Gap(Space0).
			Children(func() {
				for _, prov := range entries {
					prov := prov
					name := modelPickerAgentName(prov)
					btn := ui.ButtonBase(c.Key(p.KeyPrefix+"agent-rail-"+prov.ID)).
						Label(name).
						Tooltip(name).
						Size(modelPickerAgentButtonSize, modelPickerAgentButtonSize).
						Radius(RadiusMD).
						Shrink(0).
						Center().
						Background(ui.Transparent)
					btn.OnClick(func() {
						state.activeAgent = prov.ID
						modelPickerInvalidate(p)
					})
					disabled := len(prov.Models) == 0 || (lockAgents && prov.ID != current)
					btn.Children(func() {
						// 面板是固定深色面：品牌图标缺失时用深色面文字色兜底，避免浅色主题下图标看不见。
						renderProviderIconColor(c, prov.ID, 24, modelPickerTextSecondary)
						if disabled {
							return
						}
						if prov.ID == active {
							btn.Background(ColorSelectedDark)
						} else if btn.Hovered() {
							btn.Background(ColorHoverDark)
						}
					})
					if disabled {
						btn.Disabled(true).Opacity(0.35)
						if len(prov.Models) == 0 && prov.UnavailableReason != "" {
							btn.Tooltip(prov.UnavailableReason)
						} else if lockAgents && prov.ID != current {
							btn.Tooltip("已有消息后只能切换到当前 Agent 的模型")
						}
					}
				}
			})
	})
}

// renderModelPickerFollowLeadRailItem 渲染竖栏底部的“跟随主导模型”入口：
// 与 Agent 图标同尺寸的圆角按钮，选中时实底，点击回到跟随主导模型。
// renderModelPickerFollowLeadRailItem 渲染竖栏底部的“跟随主导模型”入口：
// 与 Agent 图标同尺寸的圆角按钮，选中时实底，点击回到跟随主导模型。
func renderModelPickerFollowLeadRailItem(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	selected := modelPickerSideInfo(p, ModelPickerSideBees).FollowsLead
	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"agent-rail-follow-lead")).
		Label("跟随主导模型").
		Tooltip("跟随主导模型").
		Size(modelPickerAgentButtonSize, modelPickerAgentButtonSize).
		Radius(RadiusMD).
		Shrink(0).
		Center().
		Background(ui.Transparent)
	if p.Disabled || p.BeesDisabled {
		btn.Disabled(true).
			Opacity(0.35).
			Tooltip(modelSideDisabledReason(p.Disabled, p.BeesDisabled))
	}
	btn.OnClick(func() {
		state.setListOpen(p.ScopeKey, ModelPickerSideBees, false)
		state.searchQuery = ""
		state.activeAgent = ""
		if p.OnFollowLead != nil {
			p.OnFollowLead()
		}
		modelPickerInvalidate(p)
	})
	btn.Children(func() {
		if selected {
			btn.Background(ThemeHover(c))
		} else if btn.Hovered() {
			btn.Background(ThemeHover(c).Alpha(0.5))
		}
		ui.Icon(c, IconByName("arrow-up-right")).
			Size(IconMD, IconMD).
			TextColor(modelPickerTextPrimary)
	})
}

// renderModelPickerSearchResults 渲染搜索态：跨 Agent 列出命中模型，每个 Agent 一组。
// renderModelPickerSearchResults 渲染搜索态：跨 Agent 列出命中模型，每个 Agent 一组。
func renderModelPickerSearchResults(c *ui.Context, p ModelPickerProps, side ModelPickerSide,
	providers []ProviderInfo, query string, onPick func(ProviderInfo, ModelInfo)) {
	isChecked := func(prov ProviderInfo, mIdx int, m ModelInfo) bool {
		return prov.ID == p.Selection.Provider && modelMatches(prov.Models, p.Selection.Model, mIdx, m)
	}
	isDisabled := func(prov ProviderInfo) bool { return p.HasMessages && prov.ID != p.Selection.Provider }
	if side == ModelPickerSideBees {
		isChecked = func(prov ProviderInfo, mIdx int, m ModelInfo) bool {
			return prov.ID == p.Bees.Provider && modelMatches(prov.Models, p.Bees.Model, mIdx, m)
		}
		isDisabled = func(ProviderInfo) bool { return false }
	}
	rendered := renderModelList(c, p, "search-", modelPickerSideKey(side), providers, query, isChecked, isDisabled, onPick)
	if !rendered {
		ui.Text(c, "没有匹配的模型").
			FontSize(uiFont(TypeFootnote)).
			TextColor(modelPickerTextSecondary).
			Padding(Space2, Space3, Space1, Space3)
	}
}

// renderModelPickerAgentRows 渲染所选 Agent 的全部模型行：精选在前、其余按目录顺序补齐；
// 每行只有模型名，Agent 由竖栏表达，行高固定为常规单行。
// renderModelPickerAgentRows 渲染所选 Agent 的全部模型行：精选在前、其余按目录顺序补齐；
// 每行只有模型名，Agent 由竖栏表达，行高固定为常规单行。
func renderModelPickerAgentRows(c *ui.Context, p ModelPickerProps, selection ModelSelection, side ModelPickerSide,
	agent ProviderInfo, pinned []modelPickerPinnedEntry, onPick func(ProviderInfo, ModelInfo)) {
	entries := modelPickerAgentModels(agent, pinned)
	if len(entries) == 0 {
		ui.Text(c, "这个 Agent 没有可用的模型").
			FontSize(uiFont(TypeFootnote)).
			TextColor(modelPickerTextSecondary).
			Padding(Space2, Space3)
		return
	}
	seenQuota := false
	for _, entry := range entries {
		renderModelPickerListRow(c, p, modelRowOptions{
			KeyPrefix: "model-",
			Provider:  entry.Provider,
			Model:     entry.Model,
			IsChecked: selection.Provider == entry.Provider.ID && modelMatches(entry.Provider.Models, selection.Model, 0, entry.Model),
			ShowQuota: !seenQuota,
			OnPick:    onPick,
		})
		seenQuota = true
	}
}

// modelPickerAgentName 返回 Agent 的显示名，供竖栏图标按钮的 Label 与 Tooltip 使用。
// modelPickerAgentName 返回 Agent 的显示名，供竖栏图标按钮的 Label 与 Tooltip 使用。
func modelPickerAgentName(prov ProviderInfo) string {
	if name := strings.TrimSpace(prov.Name); name != "" {
		return name
	}
	if name := providerDisplayName(prov.ID); name != "" {
		return name
	}
	return prov.ID
}

// renderModelPickerListRow 渲染模型列表页的一行：左侧模型名，右侧收藏星标与会话当前模型标记。
// 星标只反映模型是否在配置的精选列表里（见 ModelPickerProps.Pins），不可点击。
// renderModelPickerListRow 渲染模型列表页的一行：左侧模型名，右侧收藏星标与会话当前模型标记。
// 星标只反映模型是否在配置的精选列表里（见 ModelPickerProps.Pins），不可点击。
func renderModelPickerListRow(c *ui.Context, p ModelPickerProps, opt modelRowOptions) {
	prov := opt.Provider
	modelItem := opt.Model
	rowKey := p.KeyPrefix + opt.KeyPrefix + "model-opt-" + prov.ID + "-" + modelItem.ID

	name := modelItem.Name
	if name == "" {
		name = modelItem.ID
	}
	if name == "" {
		name = "默认模型"
	}
	if modelItem.IsDefault {
		name = defaultModelRowDisplayName(prov, modelItem)
	}
	// 行左侧是模型名，右侧是分组名（模型 ID 里 `/` 之前的部分）；上下文窗口等目录信息不占行高。
	// 模型 ID 不带网关前缀时，分组就是它所属的 Agent。
	groupName := prov.ID
	if i := strings.IndexByte(modelItem.ID, '/'); i > 0 {
		groupName = modelItem.ID[:i]
	}
	btn := ui.ButtonBase(c.Key(rowKey)).
		Label(name).
		Tooltip(name+"  "+groupName).
		Height(modelPickerRowHeight).
		Padding(Space2, Space3).
		Radius(RadiusMD).
		FillWidth()
	if opt.IsLocked {
		tip := opt.LockedReason
		if tip == "" {
			tip = "已有消息，只能切换到当前 Agent 的模型（下回合生效）"
		}
		btn.Disabled(true).Opacity(0.45).Background(ui.Transparent).Tooltip(tip)
	} else {
		if btn.Hovered() {
			btn.Background(ColorHoverDark)
		} else {
			btn.Background(ui.Transparent)
		}
		btn.OnClick(func() {
			if opt.OnPick != nil {
				opt.OnPick(prov, modelItem)
			}
		})
	}
	btn.Children(func() {
		ui.Row(c.Key(rowKey + "-content")).
			AlignItems(ui.Center).
			Gap(Space3).
			FillWidth().
			Children(func() {
				title := ui.Text(c, name).
					FontSize(uiFont(TypeProse)).
					TextColor(modelPickerTextPrimary).
					SingleLine().
					Shrink(0)
				if opt.IsChecked {
					title.FontWeight(600)
				}
				// 模型名优先占位，分组名贴右侧：跨 Agent 搜索与收藏混合时也能看出模型归属。
				ui.Row(c.Key(rowKey + "-name-gap")).Grow(1)
				ui.Text(c, groupName).
					FontSize(uiFont(TypeCaption)).
					TextColor(modelPickerTextSecondary).
					SingleLine().
					Ellipsis("…").
					Shrink(1)
				if opt.ShowQuota && p.QuotaUsed != nil {
					if usedPercent, ok := p.QuotaUsed(prov.ID); ok {
						clamped := math.Max(0, math.Min(100, usedPercent))
						ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(clamped)))).
							FontSize(uiFont(TypeCaption)).
							TextColor(quotaPercentColor(c, clamped))
					}
				}
				// 收藏（设置页固定的模型）在行右端给一枚实心星，未收藏的模型不显示星。
				if providerHasPinnedModel(prov.ID, modelItem.ID, p.Pins) {
					ui.Box(c).
						Label("已收藏").
						Tooltip("已收藏").
						Size(IconSM, IconSM).
						Shrink(0).
						Children(func() {
							ui.Icon(c, IconByName("star-filled")).
								Size(IconSM, IconSM).
								TextColor(modelPickerTextPrimary)
						})
				}
				if opt.IsChecked {
					ui.Icon(c, IconCheck()).
						Size(IconSM, IconSM).
						TextColor(modelPickerTextPrimary)
				} else if modelItem.SupportsFast {
					ui.Icon(c, IconByName("zap")).
						Size(12, 12).
						TextColor(modelPickerTextSecondary)
				}
			})
	})
}

type modelRowOptions struct {
	KeyPrefix    string
	Provider     ProviderInfo
	Model        ModelInfo
	IsChecked    bool
	IsLocked     bool
	LockedReason string // 行被禁用时 Tooltip 显示的说明；为空时按锁定语义给默认文案
	ShowSubtitle bool
	SubtitleText string
	ShowQuota    bool
	OnPick       func(prov ProviderInfo, m ModelInfo)
}

// modelPickerSideOf 返回行所属的分段：执行侧的行前缀是 bees-，其余归主导侧。
func modelPickerSideOf(opt modelRowOptions) ModelPickerSide {
	if strings.HasPrefix(opt.KeyPrefix, "bees-") {
		return ModelPickerSideBees
	}
	return ModelPickerSideLead
}

// renderModelRow 渲染单个模型行（步骤 4）。
// ui.ButtonBase，左右 Padding 10，Radius 9。只在 Hovered 时背景为 ColorHoverDark（固定深色面上的淡色悬停底）。
// 行高：有副标题 46，单行 34。内容 Row Gap 10：左侧宽 18 图标盒，居中 renderProviderIcon(c, providerID, 14)；
// 中间 Column Gap 1 Grow 1：主标题 14px ThemePrimaryText 单行截断，副标题 12px ThemeSecondaryText 单行截断；
// 右侧依次：配额文字、SupportsFast 10px zap 图标、最右边对勾（13px 纯白，未选中放 13x13 占位）。
// 锁定状态：透明度 0.45、Disabled，副标题换成“下回合生效”。
