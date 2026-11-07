package mcodeui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// piMagpieState 维护 pi 的 Magpie 端点加载与保存状态。
type piMagpieState struct {
	loading   bool
	loaded    bool
	err       string             // 最近一次加载失败信息
	endpoints []PiMagpieEndpoint // 后端返回的 Magpie 端点快照
	saveErr   map[string]string  // 端点 ID → 最近一次保存失败信息
}

// loadPiMagpieEndpoints 异步拉取 Magpie 端点与已勾选模型；重复调用时保持单次在途。
func (st *SettingsState) loadPiMagpieEndpoints() {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.piMagpie.loading {
		st.mu.Unlock()
		return
	}
	st.piMagpie.loading = true
	st.mu.Unlock()
	st.invalidate()

	go func() {
		endpoints, err := client.PiMagpieEndpoints(context.Background())

		st.mu.Lock()
		st.piMagpie.loading = false
		if err != nil {
			if errors.Is(err, ErrNotImplemented) {
				st.piMagpie.err = "暂不支持读取 Magpie 端点"
			} else {
				st.piMagpie.err = err.Error()
			}
		} else {
			st.piMagpie.endpoints = endpoints
			st.piMagpie.loaded = true
			st.piMagpie.err = ""
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// refreshPiMagpieSnapshots 在勾选保存结束后同步 pi 的两份快照：piMagpie 端点用于模型
// 面板与路由可用性，agentMagpie["pi"] 用于路由、档位与供应商行状态。
func refreshPiMagpieSnapshots(st *SettingsState) {
	st.loadPiMagpieEndpoints()
	st.reloadAgentMagpie("pi")
}

// piMagpieNextSelection 纯函数：先按端点模型顺序列出选中的模型 ID，
// 再按原 Selected 顺序追加网关没列出但仍在选中的 ID，避免保存时丢掉它们。
func piMagpieNextSelection(ep PiMagpieEndpoint, modelID string, selected bool) []string {
	picked := make(map[string]bool, len(ep.Selected)+1)
	for _, id := range ep.Selected {
		picked[id] = true
	}
	if selected {
		picked[modelID] = true
	} else {
		delete(picked, modelID)
	}

	ids := make([]string, 0, len(picked))
	seen := make(map[string]bool, len(picked))
	for _, m := range ep.Models {
		if picked[m.ID] && !seen[m.ID] {
			ids = append(ids, m.ID)
			seen[m.ID] = true
		}
	}
	for _, id := range ep.Selected {
		if picked[id] && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	// 网关没列出的模型也要能新勾选，保证不会静默丢失这次勾选。
	if selected && picked[modelID] && !seen[modelID] {
		ids = append(ids, modelID)
	}
	return ids
}

// togglePiMagpieModel 切换单个模型的勾选：先乐观更新本地 Selected 并重绘，
// 再异步保存；保存失败时把该端点的 Selected 回滚到切换前的取值并记录错误。
func (st *SettingsState) togglePiMagpieModel(endpointID, modelID string, selected bool) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	idx := -1
	for i := range st.piMagpie.endpoints {
		if st.piMagpie.endpoints[i].ID == endpointID {
			idx = i
			break
		}
	}
	if idx < 0 {
		st.mu.Unlock()
		return
	}
	ep := &st.piMagpie.endpoints[idx]
	prev := append([]string(nil), ep.Selected...)

	// 按 Models 原顺序生成已选 ID 列表，后端据此保存勾选结果；
	// 网关没列出的已选模型按原顺序追加在后面。
	ids := piMagpieNextSelection(*ep, modelID, selected)
	ep.Selected = ids
	if st.piMagpie.saveErr == nil {
		st.piMagpie.saveErr = make(map[string]string)
	}
	st.piMagpie.saveErr[endpointID] = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.SetPiMagpieModels(endpointID, ids)

		st.mu.Lock()
		for i := range st.piMagpie.endpoints {
			if st.piMagpie.endpoints[i].ID != endpointID {
				continue
			}
			if err != nil {
				st.piMagpie.endpoints[i].Selected = prev
				if errors.Is(err, ErrNotImplemented) {
					st.piMagpie.saveErr[endpointID] = "暂不支持保存 Magpie 模型"
				} else {
					st.piMagpie.saveErr[endpointID] = err.Error()
				}
			} else {
				st.piMagpie.saveErr[endpointID] = ""
			}
			break
		}
		st.mu.Unlock()
		st.invalidate()
		// 勾选变化同时影响 pi 自己的端点快照与 agentMagpie["pi"]，两份都要重读。
		refreshPiMagpieSnapshots(st)
		if err == nil {
			// 勾选变化会让 pi 的模型目录重建（后端已作废缓存），重读目录让各选择器同步。
			refreshCatalogAfterPiMagpieSave(st, client)
		}
	}()
}

// injectPiMagpieModel 勾选并注入单个模型（来自模型列表的「+」）：已选中时直接返回；
// 保存成功后才更新本地状态，失败返回错误且不改本地状态。
func (st *SettingsState) injectPiMagpieModel(endpointID, modelID string) error {
	client := st.agyClient()
	if client == nil {
		return errors.New("客户端不可用，无法注入 Magpie 模型")
	}

	st.mu.Lock()
	idx := -1
	for i := range st.piMagpie.endpoints {
		if st.piMagpie.endpoints[i].ID == endpointID {
			idx = i
			break
		}
	}
	if idx < 0 {
		st.mu.Unlock()
		return errors.New("找不到该 Magpie 端点")
	}
	ep := st.piMagpie.endpoints[idx]
	for _, id := range ep.Selected {
		if id == modelID {
			st.mu.Unlock()
			return nil
		}
	}
	ids := piMagpieNextSelection(ep, modelID, true)
	st.mu.Unlock()

	if err := client.SetPiMagpieModels(endpointID, ids); err != nil {
		if errors.Is(err, ErrNotImplemented) {
			return errors.New("暂不支持保存 Magpie 模型")
		}
		return err
	}

	st.mu.Lock()
	for i := range st.piMagpie.endpoints {
		if st.piMagpie.endpoints[i].ID != endpointID {
			continue
		}
		st.piMagpie.endpoints[i].Selected = ids
		if st.piMagpie.saveErr != nil {
			st.piMagpie.saveErr[endpointID] = ""
		}
		break
	}
	st.mu.Unlock()
	st.invalidate()
	refreshPiMagpieSnapshots(st)
	refreshCatalogAfterPiMagpieSave(st, client)
	return nil
}

// piMagpieCatalogRefreshDelays 是保存勾选后重读供应商目录的时间点：pi 目录由后台子进程
// 重新拉取（最长 30 秒），立刻读一次往往还是旧目录，隔几秒再读才能拿到注入的模型。
// 改成包级变量便于测试缩短。
var piMagpieCatalogRefreshDelays = []time.Duration{3 * time.Second, 10 * time.Second}

// refreshCatalogAfterPiMagpieSave 在保存勾选后重读供应商目录，让所有模型选择器
// （Composer、Swarm 默认模型与档位/配对、提交信息模型等）都拿到同一份新目录。
func refreshCatalogAfterPiMagpieSave(st *SettingsState, client Client) {
	refreshCatalogProviders(st, client)
	go func() {
		for _, delay := range piMagpieCatalogRefreshDelays {
			time.Sleep(delay)
			refreshCatalogProviders(st, client)
		}
	}()
}

// refreshCatalogProviders 重读一次供应商目录：替换会话缓存（Composer、Swarm、提交信息模型等都读它），
// 并标记模型库页的快照已作废，让那里也换成同一份目录。
func refreshCatalogProviders(st *SettingsState, client Client) {
	st.mu.Lock()
	st.catalogStale = true
	st.mu.Unlock()
	refreshCustomEndpointProviders(st, client)
}

// magpieAgentOwnModelTitle 返回自身配置分组的标题：<agent> 自身配置。
func magpieAgentOwnModelTitle(agent string) string {
	return agent + " 自身配置"
}

// magpieAgentGlobalName 返回写全局配置文案里使用的 agent 名称。
func magpieAgentGlobalName(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cline":
		return "Cline"
	}
	return agent
}

// magpieAgentMode 返回指定 agent 当前的 Magpie 使用方式；未加载时按运行时注入处理。
func magpieAgentMode(st *SettingsState, agent string) string {
	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	mode := agentMagpieModeOf(state.setting)
	needsLoad := !state.loaded && !state.loading && state.err == ""
	st.mu.Unlock()
	if needsLoad {
		st.loadAgentMagpie(agent)
	}
	return mode
}

// magpieEndpointsFor 返回指定 agent 的 Magpie 端点快照、加载状态与保存错误。
// pi 用独立的 piMagpie 状态，其它 agent 用 agentMagpie 状态。
func magpieEndpointsFor(st *SettingsState, agent string) (endpoints []PiMagpieEndpoint, loading, loaded bool, errMsg string, saveErr map[string]string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if agent == "pi" {
		loading = st.piMagpie.loading
		loaded = st.piMagpie.loaded
		errMsg = st.piMagpie.err
		endpoints = append([]PiMagpieEndpoint(nil), st.piMagpie.endpoints...)
		saveErr = make(map[string]string, len(st.piMagpie.saveErr))
		for id, msg := range st.piMagpie.saveErr {
			saveErr[id] = msg
		}
		return
	}

	state := st.agentMagpieFor(agent)
	loading = state.loading
	loaded = state.loaded
	errMsg = state.err
	// 深拷贝端点：渲染期间后台保存会改 Selected，不能共享底层数组。
	endpoints = make([]PiMagpieEndpoint, len(state.setting.Endpoints))
	for i, ep := range state.setting.Endpoints {
		ep.Models = append([]PiMagpieModel(nil), ep.Models...)
		ep.Selected = append([]string(nil), ep.Selected...)
		endpoints[i] = ep
	}
	saveErr = make(map[string]string, len(state.saveErr))
	for id, msg := range state.saveErr {
		saveErr[id] = msg
	}
	return
}

// magpieLoadFor 返回指定 agent 的端点加载函数。
func magpieLoadFor(st *SettingsState, agent string) func() {
	if agent == "pi" {
		return st.loadPiMagpieEndpoints
	}
	return func() { st.loadAgentMagpie(agent) }
}

// magpieToggleFor 返回指定 agent 的模型勾选切换函数。
func magpieToggleFor(st *SettingsState, agent string) func(endpointID, modelID string, selected bool) {
	if agent == "pi" {
		return st.togglePiMagpieModel
	}
	return func(endpointID, modelID string, selected bool) {
		st.toggleAgentMagpieModel(agent, endpointID, modelID, selected)
	}
}

// magpieInjectFor 返回指定 agent 的「+」注入函数：勾选模型并加入你的模型。
func magpieInjectFor(st *SettingsState, agent string) func(endpointID, modelID string) error {
	if agent == "pi" {
		return st.injectPiMagpieModel
	}
	return func(endpointID, modelID string) error {
		return st.injectAgentMagpieModel(agent, endpointID, modelID)
	}
}

// piModelRow 是 pi 模型列表里的一行：Model 承载展示信息，EndpointModelID 是端点侧模型 ID，
// Injected 表示该模型已勾选（会注入 pi）。
type piModelRow struct {
	Model           ModelInfo
	EndpointModelID string
	Injected        bool
}

// piModelGroup 是一组 pi 模型：Endpoint 为 nil 表示 pi 自身配置里的模型。
type piModelGroup struct {
	Endpoint *PiMagpieEndpoint
	Title    string
	Rows     []piModelRow
}

// piMagpieEndpointTitle 返回端点分组标题：<Name> · <ProviderID>。
func piMagpieEndpointTitle(ep PiMagpieEndpoint) string {
	name := strings.TrimSpace(ep.Name)
	if name == "" {
		name = ep.ID
	}
	providerID := strings.TrimSpace(ep.ProviderID)
	if providerID != "" && providerID != name {
		return name + " · " + providerID
	}
	return name
}

// buildMagpieModelGroups 纯函数：把 Magpie 端点与 agent 的模型目录合并成分组。
// 每个端点一组，先按 ep.Models 顺序生成行（目录里有同 ID 条目时并进来并消耗掉），
// 再追加 ep.Selected 里网关没列出的已选模型；剩余目录条目放进最后一组「<agent> 自身配置」。
func buildMagpieModelGroups(catalog []ModelInfo, endpoints []PiMagpieEndpoint, agent string) []piModelGroup {
	remaining := make([]ModelInfo, len(catalog))
	copy(remaining, catalog)

	// consume 从剩余目录里取出同 ID 条目，保证每个目录条目只进一个分组。
	consume := func(fullID string) (ModelInfo, bool) {
		for i, m := range remaining {
			if m.ID != fullID {
				continue
			}
			remaining = append(remaining[:i], remaining[i+1:]...)
			return m, true
		}
		return ModelInfo{}, false
	}

	groups := make([]piModelGroup, 0, len(endpoints)+1)
	for i := range endpoints {
		ep := endpoints[i]
		providerID := strings.TrimSpace(ep.ProviderID)
		group := piModelGroup{Endpoint: &ep, Title: piMagpieEndpointTitle(ep)}

		selected := make(map[string]bool, len(ep.Selected))
		for _, id := range ep.Selected {
			selected[id] = true
		}
		listed := make(map[string]bool, len(ep.Models))
		group.Rows = make([]piModelRow, 0, len(ep.Models)+len(ep.Selected))

		for _, m := range ep.Models {
			listed[m.ID] = true
			fullID := providerID + "/" + m.ID
			model, ok := consume(fullID)
			if !ok {
				// 目录里没有同 ID 条目时用网关侧名称，避免丢掉网关给出的模型名。
				model = ModelInfo{ID: fullID, Name: m.Name}
			}
			// 行里展示的说明固定用端点侧模型 ID（即 pi 目录里 ID 的最后一段）。
			model.Detail = m.ID
			if strings.TrimSpace(model.Name) == "" {
				model.Name = m.ID
			}
			group.Rows = append(group.Rows, piModelRow{
				Model:           model,
				EndpointModelID: m.ID,
				Injected:        selected[m.ID],
			})
		}

		for _, id := range ep.Selected {
			if listed[id] {
				continue
			}
			fullID := providerID + "/" + id
			model, ok := consume(fullID)
			if !ok {
				model = ModelInfo{ID: fullID}
			}
			model.Detail = id
			if strings.TrimSpace(model.Name) == "" {
				model.Name = id
			}
			group.Rows = append(group.Rows, piModelRow{
				Model:           model,
				EndpointModelID: id,
				Injected:        true,
			})
		}

		groups = append(groups, group)
	}

	if len(remaining) > 0 {
		rows := make([]piModelRow, 0, len(remaining))
		for _, m := range remaining {
			rows = append(rows, piModelRow{Model: m})
		}
		groups = append(groups, piModelGroup{Title: magpieAgentOwnModelTitle(agent), Rows: rows})
	}
	return groups
}

// buildPiModelGroups 是 pi 的薄包装：保留给既有调用点与测试使用。
func buildPiModelGroups(catalog []ModelInfo, endpoints []PiMagpieEndpoint) []piModelGroup {
	return buildMagpieModelGroups(catalog, endpoints, "pi")
}

// dropMagpieOwnGroup 去掉 agent 自身配置分组（Endpoint == nil 的那组）。
// 写全局配置时，分组里的目录模型只是全局配置的回显，不再重复列在模型列表里。
func dropMagpieOwnGroup(groups []piModelGroup) []piModelGroup {
	res := make([]piModelGroup, 0, len(groups))
	for _, g := range groups {
		if g.Endpoint == nil {
			continue
		}
		res = append(res, g)
	}
	return res
}

// filterPiModelGroups 纯函数：行须命中全部 query（名称、ID、说明任一命中即算命中该 query）。
// 查询全为空时原样返回；有查询时去掉零命中的组，但端点出错的组保留（好让用户看到错误）。
func filterPiModelGroups(groups []piModelGroup, queries ...string) []piModelGroup {
	var active []string
	for _, q := range queries {
		if trimmed := strings.TrimSpace(q); trimmed != "" {
			active = append(active, trimmed)
		}
	}
	if len(active) == 0 {
		return groups
	}

	res := make([]piModelGroup, 0, len(groups))
	for _, g := range groups {
		rows := make([]piModelRow, 0, len(g.Rows))
		for _, r := range g.Rows {
			hit := true
			for _, q := range active {
				if !modelMatchesQuery(r.Model.Name, r.Model.ID, r.Model.Detail, q) {
					hit = false
					break
				}
			}
			if hit {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			if g.Endpoint != nil && g.Endpoint.Err != "" {
				// 出错端点的组保留标题和错误提示，但不带未命中的行。
				g.Rows = nil
				res = append(res, g)
			}
			continue
		}
		g.Rows = rows
		res = append(res, g)
	}
	return res
}

// piMagpieSearchHit 判断 pi 的全局搜索词是否命中某个 Magpie 端点模型；
// 命中时即使供应商行处于折叠状态也要展开，让结果直接可见。
func piMagpieSearchHit(st *SettingsState, search string) bool {
	st.mu.Lock()
	endpoints := append([]PiMagpieEndpoint(nil), st.piMagpie.endpoints...)
	st.mu.Unlock()
	if len(endpoints) == 0 {
		return false
	}
	for _, g := range filterPiModelGroups(buildPiModelGroups(nil, endpoints), search) {
		if len(g.Rows) > 0 {
			return true
		}
	}
	return false
}

// renderMagpieAgentModels 渲染某个 agent 的模型列表：已启用模型 + agent 自身配置的模型，
// 由公共的 Magpie 模型面板统一渲染。首次渲染时异步加载端点。
func renderMagpieAgentModels(c *ui.Context, st *SettingsState, lib *modelsLibraryView, catalog ProviderInfo, agent string) {
	opts := magpieModelPanelOptions{}
	if agent == "pi" {
		opts.Footer = "已启用的模型会注入 mcode 内启动的 pi"
	} else {
		opts.ShowDefault = true
		// 只有 claude 写全局配置时后端会给出各档位实际指向的模型；codex 只写一个默认模型。
		opts.ShowTiers = agent == "claude"
		if agent == "codex" {
			opts.Footer = "写全局配置后，codex 使用这里选定的默认模型"
		}
	}
	renderMagpieModelPanel(c, st, lib, catalog, agent, opts)
}

// renderPiModels 是 pi 的薄包装：保留给既有调用点与测试使用。
func renderPiModels(c *ui.Context, st *SettingsState, lib *modelsLibraryView, catalog ProviderInfo) {
	renderMagpieAgentModels(c, st, lib, catalog, "pi")
}

// renderMagpieRefreshButton 渲染端点卡片头部右侧的刷新按钮；刷新期间禁用并显示进度文案。
func renderMagpieRefreshButton(c *ui.Context, st *SettingsState, agent, key string, loading bool) {
	btn := ui.ButtonBase(c.Key(key)).
		Label("刷新").
		Radius(RadiusXS).
		Padding(0, Space0)
	if loading {
		btn.Disabled(true)
	}
	if btn.Clicked() && !loading {
		magpieLoadFor(st, agent)()
	}
	btn.Children(func() {
		label := "刷新"
		if loading {
			label = "正在刷新…"
		}
		color := ThemeSecondaryText(c)
		if btn.Hovered() && !loading {
			color = ThemeAccent(c)
		}
		ui.Text(c, label).
			FontSize(TypeFootnote).
			TextColor(color)
	})
}

// renderPiMagpieNotice 渲染区块内的一行提示文字（加载中或加载失败）。
func renderPiMagpieNotice(c *ui.Context, key, text string, color ui.Color) {
	ui.Row(c.Key("row-"+key)).
		Padding(0, Space5, Space4, Space5).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeFootnote).
				TextColor(color)
		})
}
