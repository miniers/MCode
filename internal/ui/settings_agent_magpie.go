package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// agent 的 magpie 使用方式：inject 只在启动的会话里注入，global 写进 agent 的全局配置。
const (
	agentMagpieModeInject = "inject"
	agentMagpieModeGlobal = "global"
	agentMagpieModeOff    = "off"
)

// agentMagpieState 维护某个 agent（claude 或 codex）的 Magpie 设置加载与保存状态。
type agentMagpieState struct {
	agent          string // 该状态归属的 agent id
	loading        bool
	refreshPending bool
	generation     uint64
	loaded         bool
	err            string // 最近一次加载失败信息

	setting     AgentMagpieSetting // 后端返回的设置快照
	saveErr     map[string]string  // 端点 ID → 最近一次保存失败信息
	modeErr     string             // 最近一次切换使用方式失败信息
	defaultErr  string             // 最近一次保存默认模型失败信息
	globalErr   string             // 最近一次全局配置操作失败信息
	matrixErr   string             // 最近一次保存模型矩阵勾选失败信息
	modelSaving map[string]bool    // 端点 ID → 模型勾选保存是否在途

	// modeSaving 表示切换使用方式的保存还在路上，期间路由控件禁用。
	modeSaving bool

	// confirmGlobal 表示「切换到写全局配置」确认框是否打开。
	confirmGlobal bool
}

// agentMagpieFor 返回某个 agent 的 Magpie 状态，没有时懒创建。
// 调用方需持有 st.mu。
func (st *SettingsState) agentMagpieFor(agent string) *agentMagpieState {
	if st.agentMagpie == nil {
		st.agentMagpie = make(map[string]*agentMagpieState)
	}
	state := st.agentMagpie[agent]
	if state == nil {
		state = &agentMagpieState{agent: agent}
		st.agentMagpie[agent] = state
	}
	return state
}

// agentMagpieModeOf 返回规范化的使用方式：空值与未知值都按运行时注入处理。
func agentMagpieModeOf(setting AgentMagpieSetting) string {
	switch strings.TrimSpace(setting.Mode) {
	case agentMagpieModeGlobal:
		return agentMagpieModeGlobal
	case agentMagpieModeOff:
		return agentMagpieModeOff
	default:
		return agentMagpieModeInject
	}
}

// loadAgentMagpie 异步拉取指定 agent 的 magpie 设置；同一 agent 重复调用时保持单次在途，
// 各 agent 的缓存互相独立。
func (st *SettingsState) loadAgentMagpie(agent string) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	state.generation++
	if state.loading {
		state.refreshPending = true
		st.mu.Unlock()
		return
	}
	state.loading = true
	generation := state.generation
	st.mu.Unlock()
	st.invalidate()

	go func() {
		for {
			setting, err := client.AgentMagpie(context.Background(), agent)

			st.mu.Lock()
			state := st.agentMagpieFor(agent)
			if generation == state.generation {
				if err != nil {
					if errors.Is(err, ErrNotImplemented) {
						state.err = "暂不支持读取 Magpie 设置"
					} else {
						state.err = err.Error()
					}
				} else {
					// 端点与勾选列表不跟后端返回的快照共享底层数组。
					endpoints := make([]PiMagpieEndpoint, len(setting.Endpoints))
					for i, ep := range setting.Endpoints {
						ep.Models = append([]PiMagpieModel(nil), ep.Models...)
						ep.Selected = append([]string(nil), ep.Selected...)
						endpoints[i] = ep
					}
					setting.Agent = agent
					setting.Mode = agentMagpieModeOf(setting)
					setting.Endpoints = endpoints
					state.setting = setting
					state.loaded = true
					state.err = ""
				}
			}
			if state.refreshPending {
				state.refreshPending = false
				generation = state.generation
				st.mu.Unlock()
				continue
			}
			state.loading = false
			st.mu.Unlock()
			st.invalidate()
			return
		}
	}()
}

// reloadAgentMagpie 保留当前缓存并重新读取指定 agent 的 magpie 设置。
func (st *SettingsState) reloadAgentMagpie(agent string) {
	st.loadAgentMagpie(agent)
}

// toggleAgentMagpieModel 切换单个模型的勾选：先乐观更新本地 Selected 并重绘，
// 再异步保存；保存失败时把该端点的 Selected 回滚到切换前的取值并记录错误。
func (st *SettingsState) toggleAgentMagpieModel(agent, endpointID, modelID string, selected bool) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	idx := -1
	for i := range state.setting.Endpoints {
		if state.setting.Endpoints[i].ID == endpointID {
			idx = i
			break
		}
	}
	if idx < 0 {
		st.mu.Unlock()
		return
	}
	ep := &state.setting.Endpoints[idx]
	prev := append([]string(nil), ep.Selected...)
	ids := piMagpieNextSelection(*ep, modelID, selected)
	ep.Selected = ids
	if state.saveErr == nil {
		state.saveErr = make(map[string]string)
	}
	state.saveErr[endpointID] = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.SetAgentMagpieModels(context.Background(), agent, endpointID, ids)

		st.mu.Lock()
		state := st.agentMagpieFor(agent)
		for i := range state.setting.Endpoints {
			if state.setting.Endpoints[i].ID != endpointID {
				continue
			}
			if err != nil {
				state.setting.Endpoints[i].Selected = prev
				if errors.Is(err, ErrNotImplemented) {
					state.saveErr[endpointID] = "暂不支持保存 Magpie 模型"
				} else {
					state.saveErr[endpointID] = err.Error()
				}
			} else {
				state.saveErr[endpointID] = ""
			}
			break
		}
		st.mu.Unlock()
		st.invalidate()
		if err != nil {
			// 保存失败时后端可能已经改了配置：重读设置，界面以服务端为准。
			st.reloadAgentMagpie(agent)
			return
		}
		// 勾选变化会重建该 agent 的模型目录，重读目录让各选择器同步。
		refreshCatalogAfterPiMagpieSave(st, client)
		// 写全局配置时档位映射按勾选推导，勾选变了要重读设置让 Tiers 跟着变。
		st.mu.Lock()
		global := agentMagpieModeOf(st.agentMagpieFor(agent).setting) == agentMagpieModeGlobal
		st.mu.Unlock()
		if global {
			st.reloadAgentMagpie(agent)
		}
		st.invalidate()
	}()
}

// setAgentMagpieEndpointModels 一次保存某 agent 在一个端点上的完整勾选列表。
func (st *SettingsState) setAgentMagpieEndpointModels(agent, endpointID string, ids []string) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	if state.modelSaving == nil {
		state.modelSaving = make(map[string]bool)
	}
	if state.modelSaving[endpointID] {
		st.mu.Unlock()
		return
	}
	found := false
	for i := range state.setting.Endpoints {
		ep := &state.setting.Endpoints[i]
		if ep.ID != endpointID {
			continue
		}
		ep.Selected = append([]string(nil), ids...)
		found = true
		break
	}
	if !found {
		st.mu.Unlock()
		return
	}
	state.modelSaving[endpointID] = true
	state.matrixErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.SetAgentMagpieModels(context.Background(), agent, endpointID, ids)

		st.mu.Lock()
		state := st.agentMagpieFor(agent)
		state.modelSaving[endpointID] = false
		if err != nil {
			if errors.Is(err, ErrNotImplemented) {
				state.matrixErr = "暂不支持保存 Magpie 模型"
			} else {
				state.matrixErr = err.Error()
			}
		}
		st.mu.Unlock()
		st.invalidate()
		if err != nil {
			st.reloadAgentMagpie(agent)
			return
		}
		refreshCatalogAfterPiMagpieSave(st, client)
		if agent == "pi" {
			refreshPiMagpieSnapshots(st)
		} else {
			st.mu.Lock()
			global := agentMagpieModeOf(st.agentMagpieFor(agent).setting) == agentMagpieModeGlobal
			st.mu.Unlock()
			if global {
				st.reloadAgentMagpie(agent)
			}
		}
		st.invalidate()
	}()
}

// setAgentMagpieMode 切换 agent 的 magpie 使用方式：先乐观更新界面，
// 保存失败时回滚到切换前的取值并记录错误，成功后重新加载设置。
func (st *SettingsState) setAgentMagpieMode(agent, mode string) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	if state.modeSaving {
		st.mu.Unlock()
		return
	}
	prev := state.setting.Mode
	state.setting.Mode = mode
	state.modeErr = ""
	state.modeSaving = true
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.SetAgentMagpieMode(context.Background(), agent, mode)

		st.mu.Lock()
		state := st.agentMagpieFor(agent)
		state.modeSaving = false
		if err != nil {
			state.setting.Mode = prev
			if errors.Is(err, ErrNotImplemented) {
				state.modeErr = "暂不支持切换 Magpie 使用方式"
			} else {
				state.modeErr = err.Error()
			}
		}
		st.mu.Unlock()
		st.invalidate()
		if err == nil {
			st.reloadAgentMagpie(agent)
			st.invalidate()
		}
	}()
}

// setAgentMagpieDefault 保存用户选定的默认模型（两个参数都为空表示恢复自动）：
// 先乐观更新本地字段并重绘，再异步保存；失败时回滚并记录错误。
// 写全局配置时 settings.json 的 model 与档位映射都跟着变，成功后重读设置。
func (st *SettingsState) setAgentMagpieDefault(agent, endpointID, modelID string) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	prevEndpointID := state.setting.DefaultEndpointID
	prevModelID := state.setting.DefaultModel
	state.setting.DefaultEndpointID = endpointID
	state.setting.DefaultModel = modelID
	state.defaultErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.SetAgentMagpieDefaultModel(context.Background(), agent, endpointID, modelID)

		st.mu.Lock()
		state := st.agentMagpieFor(agent)
		if err != nil {
			state.setting.DefaultEndpointID = prevEndpointID
			state.setting.DefaultModel = prevModelID
			if errors.Is(err, ErrNotImplemented) {
				state.defaultErr = "暂不支持保存默认模型"
			} else {
				state.defaultErr = err.Error()
			}
		}
		st.mu.Unlock()
		st.invalidate()
		if err == nil {
			// 写全局配置时主模型写在 settings.json 的 model 里，重读让状态行与勾选同步。
			st.mu.Lock()
			global := agentMagpieModeOf(st.agentMagpieFor(agent).setting) == agentMagpieModeGlobal
			st.mu.Unlock()
			if global {
				st.reloadAgentMagpie(agent)
			}
			st.invalidate()
		}
	}()
}

// runAgentMagpieGlobalAction 对全局配置执行 reapply 或 keep：
// 失败时记录错误，成功后重新加载设置让状态行与按钮同步。
func (st *SettingsState) runAgentMagpieGlobalAction(agent, action string) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	state.globalErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.AgentMagpieGlobalAction(context.Background(), agent, action)

		st.mu.Lock()
		state := st.agentMagpieFor(agent)
		if err != nil {
			if errors.Is(err, ErrNotImplemented) {
				state.globalErr = "暂不支持修改全局配置"
			} else {
				state.globalErr = err.Error()
			}
		}
		st.mu.Unlock()
		st.invalidate()
		if err == nil {
			st.reloadAgentMagpie(agent)
			st.invalidate()
		}
	}()
}

// setAgentMagpieConfirm 打开或关闭「切换到写全局配置」确认框。
func (st *SettingsState) setAgentMagpieConfirm(agent string, open bool) {
	st.mu.Lock()
	st.agentMagpieFor(agent).confirmGlobal = open
	st.mu.Unlock()
	st.invalidate()
}

// agentMagpieAutoDefault 按自动规则挑默认模型：取第一个勾选非空的端点的第一项。
// 没有任何勾选时返回空串。
func agentMagpieAutoDefault(setting AgentMagpieSetting) (endpointID, modelID string) {
	for _, ep := range setting.Endpoints {
		if len(ep.Selected) > 0 {
			return ep.ID, ep.Selected[0]
		}
	}
	return "", ""
}

// agentMagpieEffectiveDefault 计算某个 agent 当前生效的默认模型（主模型）：
// 用户指定的端点存在且 DefaultModel 在该端点的勾选里时用它（manual=true）；
// 否则回退到自动规则（manual=false）。都没有时返回空串。
func agentMagpieEffectiveDefault(setting AgentMagpieSetting) (endpointID, modelID string, manual bool) {
	wantEndpointID := strings.TrimSpace(setting.DefaultEndpointID)
	wantModelID := strings.TrimSpace(setting.DefaultModel)
	if wantEndpointID != "" && wantModelID != "" {
		for _, ep := range setting.Endpoints {
			if ep.ID != wantEndpointID {
				continue
			}
			for _, id := range ep.Selected {
				if id == wantModelID {
					return ep.ID, wantModelID, true
				}
			}
			break
		}
	}
	autoEndpointID, autoModelID := agentMagpieAutoDefault(setting)
	return autoEndpointID, autoModelID, false
}

// agentMagpieCurrentDefault 返回某个 agent 当前生效的默认模型；没有端点或没有勾选时 ok 为 false。
func agentMagpieCurrentDefault(st *SettingsState, agent string) (endpointID, modelID string, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	setting := st.agentMagpieFor(agent).setting
	endpointID, modelID, manual := agentMagpieEffectiveDefault(setting)
	// 运行时注入只在用户手设了默认模型时才把它填进路由的 Default；没手设时
	// 交给 agent 自己的默认模型，界面也不该给它标默认徽标。
	if agentMagpieModeOf(setting) == agentMagpieModeInject && !manual {
		return "", "", false
	}
	if endpointID == "" || modelID == "" {
		return "", "", false
	}
	return endpointID, modelID, true
}

// agentMagpieModelName 返回端点上某个模型的展示名：优先网关给出的名称，找不到用 ID。
func agentMagpieModelName(ep PiMagpieEndpoint, modelID string) string {
	for _, m := range ep.Models {
		if m.ID != modelID {
			continue
		}
		if name := strings.TrimSpace(m.Name); name != "" {
			return name
		}
		break
	}
	return modelID
}

// agentMagpieEndpointName 返回端点在选择框里的名称：没有名称时用端点 ID。
func agentMagpieEndpointName(ep PiMagpieEndpoint) string {
	if name := strings.TrimSpace(ep.Name); name != "" {
		return name
	}
	return ep.ID
}

// ensureAgentMagpieLoaded 在需要时触发一次 agent 设置加载：已加载、加载中或已出错时不动。
// 供应商行在决定是否渲染模型小节前先用它拿端点快照，没有目录模型的 agent 也能从网关拿到模型。
func ensureAgentMagpieLoaded(st *SettingsState, agent string) {
	_, loading, loaded, errMsg, _ := magpieEndpointsFor(st, agent)
	if loading || loaded || errMsg != "" {
		return
	}
	magpieLoadFor(st, agent)()
}

// agentMagpieRouteConfigured 报告该 agent 的 Magpie 设置里是否已有可路由的内容：
// 已加载且（有端点或已写全局配置）。不触发加载。
func agentMagpieRouteConfigured(st *SettingsState, agent string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	state := st.agentMagpieFor(agent)
	if !state.loaded {
		return false
	}
	return agentMagpieModeOf(state.setting) == agentMagpieModeGlobal || len(state.setting.Endpoints) > 0
}

// magpieAgentRouteValue 返回已加载的路由值，首次读取时异步加载。
func magpieAgentRouteValue(st *SettingsState, agent string) (value string, known bool) {
	switch agent {
	case "claude", "codex", "pi":
	default:
		return "", false
	}
	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	loading, loaded, errMsg := state.loading, state.loaded, state.err
	mode := agentMagpieModeOf(state.setting)
	st.mu.Unlock()
	if !loaded && !loading && errMsg == "" {
		st.loadAgentMagpie(agent)
	}
	if loading || !loaded || errMsg != "" {
		return "", false
	}
	switch mode {
	case agentMagpieModeGlobal:
		return "全局", true
	case agentMagpieModeInject:
		return "仅 mcode", true
	}
	return "直连", true
}

// magpieAgentRouteControl 在 Magpie 页内编辑路由和默认模型。
type magpieAgentRouteControl struct{ agent string }

func (r magpieAgentRouteControl) render(c *ui.Context, st *SettingsState, s setting) {
	_, known := magpieAgentRouteValue(st, r.agent)
	st.mu.Lock()
	state := st.agentMagpieFor(r.agent)
	snapshot := state.setting
	loadErr := state.err
	defaultErr := state.defaultErr
	st.mu.Unlock()
	endpoints, _, loaded, _, _ := magpieEndpointsFor(st, r.agent)
	ui.Column(c.Key(s.ID)).FillWidth().Padding(settingsRowPadY, settingsRowPadX).Gap(Space1).Children(func() {
		ui.Row(c.Key(s.ID + "-mode")).FillWidth().AlignItems(ui.Center).Gap(Space3).Children(func() {
			ui.Text(c, s.Title).
				FontSize(TypeBody).
				TextColor(ThemePrimaryText(c)).
				SingleLine().
				Grow(1)
			magpieAgentRouteChoice(r.agent).render(c, st, setting{ID: s.ID + "-choice", Title: magpieAgentRouteLabel(s.Title)})
		})
		if !loaded && !known {
			ui.Text(c, "正在加载…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
		}
		if loadErr != "" {
			ui.Text(c, "加载 Magpie 设置失败: "+loadErr).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
		}
		if loaded {
			renderMagpieAgentDefaultPicker(c, st, r.agent, endpoints, snapshot)
		}
		if defaultErr != "" {
			ui.Text(c, defaultErr).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
		}
		renderMagpieAgentRouteExtras(c, st, r.agent)
	})
}

// magpieAgentRouteHasModels 报告该 agent 在界面上是否有可用勾选：任一端点 Selected 非空
// 即视为可用；pi 读的是模型区展示的 piMagpie 端点，保证与界面上的勾选一致。
func magpieAgentRouteHasModels(st *SettingsState, agent string) bool {
	endpoints, _, loaded, errMsg, _ := magpieEndpointsFor(st, agent)
	if !loaded || errMsg != "" {
		return false
	}
	for _, ep := range endpoints {
		if len(ep.Selected) > 0 {
			return true
		}
	}
	return false
}

// renderMagpieAgentRouteExtras 画路由错误、全局配置状态与写配置确认框。
func renderMagpieAgentRouteExtras(c *ui.Context, st *SettingsState, agent string) {
	_, known := magpieAgentRouteValue(st, agent)
	st.mu.Lock()
	state := st.agentMagpieFor(agent)
	setting := state.setting
	setting.Agent = agent
	setting.Endpoints = append([]PiMagpieEndpoint(nil), setting.Endpoints...)
	setting.Global.Drifted = append([]string(nil), setting.Global.Drifted...)
	modeErr, loadErr, globalErr, confirmOpen := state.modeErr, state.err, state.globalErr, state.confirmGlobal
	st.mu.Unlock()

	if modeErr != "" {
		renderAgentMagpieNotice(c, agent+"-route-save-err", "切换路由失败: "+modeErr, ThemeDanger(c))
	}
	if loadErr != "" {
		renderAgentMagpieNotice(c, agent+"-route-load-err", "加载 Magpie 设置失败: "+loadErr, ThemeDanger(c))
	}
	if known && agentMagpieModeOf(setting) == agentMagpieModeGlobal {
		renderAgentMagpieGlobalStatus(c, st, agent, setting.Global, globalErr)
	}
	body, detail := agentMagpieGlobalConfirmCopy(setting)
	RenderConfirmDialog(c, ConfirmDialog{
		Key: "agent-magpie-route-global-" + agent, Open: confirmOpen, Tone: ConfirmSafe,
		Title: "切换到写全局配置？", Body: body, Detail: detail, ConfirmLabel: "写入全局配置",
		OnConfirm: func() {
			st.setAgentMagpieConfirm(agent, false)
			// 确认框可能与勾选变化交错：没有勾选时写全局配置没有意义。
			if !magpieAgentRouteHasModels(st, agent) {
				return
			}
			st.setAgentMagpieMode(agent, agentMagpieModeGlobal)
		},
		OnCancel: func() { st.setAgentMagpieConfirm(agent, false) },
	})
}

// magpieAgentRouteChoice 按智能体能力提供路由选项：保存未完成前整段禁用，
// 没有任何 Magpie 勾选时只能选直连。
func magpieAgentRouteChoice(agent string) choiceFuncControl {
	options := []string{"直连", "仅 mcode", "全局"}
	needsModels := func(idx int) bool { return idx > 0 }
	return choiceFunc(options,
		func(st *SettingsState) (int, bool) {
			value, known := magpieAgentRouteValue(st, agent)
			st.mu.Lock()
			state := st.agentMagpieFor(agent)
			saving := state.modeSaving
			st.mu.Unlock()
			for idx, option := range options {
				if value == option {
					return idx, !known || saving
				}
			}
			return 0, !known || saving
		},
		func(st *SettingsState, idx int) {
			if idx < 0 || idx >= len(options) {
				return
			}
			// 禁用的选项不写配置：避免后端拒绝产生无意义的错误提示。
			if needsModels(idx) && !magpieAgentRouteHasModels(st, agent) {
				return
			}
			switch options[idx] {
			case "直连":
				st.setAgentMagpieMode(agent, agentMagpieModeOff)
			case "全局":
				st.setAgentMagpieConfirm(agent, true)
			case "仅 mcode":
				st.setAgentMagpieMode(agent, agentMagpieModeInject)
			}
		}).withItemDisabled(func(st *SettingsState, idx int) bool {
		return needsModels(idx) && !magpieAgentRouteHasModels(st, agent)
	})
}

// magpieAgentRouteRow 保留标题供页面树与索引使用，由具体控件接管整行布局。
func magpieAgentRouteRow(agent string) setting {
	return setting{ID: "row-magpie-agent-" + agent, Title: magpieAgentTitle(agent), Visible: func(st *SettingsState) bool {
		for _, installed := range magpieInstalledAgents(st) {
			if installed == agent {
				return true
			}
		}
		return false
	}, Control: magpieAgentRouteControl{agent: agent}}
}

func magpieAgentRouteDescription(agent string, snapshot AgentMagpieSetting, known, saving bool) string {
	if saving {
		return "正在切换路由…"
	}
	if !known {
		return "正在加载路由…"
	}
	title := magpieAgentTitle(agent)
	switch agentMagpieModeOf(snapshot) {
	case agentMagpieModeOff:
		return "使用 " + title + " 自身的登录与配置"
	case agentMagpieModeGlobal:
		path := strings.TrimSpace(snapshot.Global.Path)
		if path == "" {
			path = title + " 全局配置"
		}
		return "已写入 " + path + "，mcode 外启动的 " + title + " 也经 Magpie"
	default:
		switch agent {
		case "claude":
			return "在 mcode 中选用 Magpie 模型时经网关；不改 ~/.claude 配置"
		case "pi":
			return "mcode 启动的 pi 可用 Magpie 模型；不改 pi 自身配置"
		case "codex":
			return "使用 Codex 自身的登录与配置"
		default:
			return "使用 " + title + " 自身的登录与配置"
		}
	}
}

// magpieAgentRouteLabel 返回路由分段的无障碍名：带上行标题，避免四行分段同名。
func magpieAgentRouteLabel(title string) string { return title + " 路由" }

// magpieAgentTitle 返回各 agent 在 Magpie 页的行标题。
func magpieAgentTitle(agent string) string {
	switch agent {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	default:
		return "pi"
	}
}

// agentMagpieGlobalConfirmCopy 生成「切换到写全局配置」确认框的文案。
func agentMagpieGlobalConfirmCopy(setting AgentMagpieSetting) (string, []string) {
	path := strings.TrimSpace(setting.Global.Path)
	if path == "" {
		path = "该 agent 的全局配置文件"
	}
	endpoint := ""
	for _, ep := range setting.Endpoints {
		if name := strings.TrimSpace(ep.Name); name != "" {
			endpoint = name
			break
		}
	}
	if endpoint == "" {
		endpoint = "该 Magpie 端点"
	}
	body := fmt.Sprintf("写全局配置会把 %s 的 API Key 以明文写入 %s。", endpoint, path)
	authDetail := "Claude Code 会改用该 key，退出 claude.ai 登录"
	switch setting.Agent {
	case "pi":
		authDetail = "mcode 内外的 pi 都会改用该 key 与 Magpie provider，不再使用原来的登录"
	case "codex":
		authDetail = "Codex 会改用该 key 与 mcode-magpie provider，不再使用 ChatGPT 登录"
	}
	detail := []string{
		fmt.Sprintf("会把该端点的 API Key 以明文写入 %s", path),
		authDetail,
		"关闭写全局配置时会恢复原配置，期间手改的字段不会被覆盖",
	}
	return body, detail
}

// renderAgentMagpieGlobalStatus 渲染写全局配置的状态行与操作按钮。
func renderAgentMagpieGlobalStatus(c *ui.Context, st *SettingsState, agent string, g AgentMagpieGlobal, actionErr string) {
	path := strings.TrimSpace(g.Path)
	state := strings.TrimSpace(g.State)
	statusText := ""
	statusColor := ThemeSecondaryText(c)
	switch state {
	case "drifted":
		statusText = "配置被改动"
		if drifted := strings.Join(g.Drifted, "、"); drifted != "" {
			statusText += "：" + drifted
		}
		statusColor = ThemeWarning(c)
	case "kept":
		statusText = "已保留当前配置；关闭时不会恢复原配置"
	case "missing":
		statusText = "配置文件不存在"
		if path != "" {
			statusText += "：" + path
		}
		statusColor = ThemeWarning(c)
	case "error":
		statusText = g.Err
		if statusText == "" {
			statusText = "写全局配置失败"
		}
		statusColor = ThemeDanger(c)
	}

	if statusText != "" {
		ui.Text(c, statusText).
			FontSize(TypeFootnote).
			TextColor(statusColor).
			Padding(Space0, settingsRowPadX)
	}
	if actionErr != "" {
		ui.Text(c, "操作失败: "+actionErr).
			FontSize(TypeFootnote).
			TextColor(ThemeDanger(c)).
			Padding(Space0, settingsRowPadX)
	}

	if state == "drifted" {
		ui.Row(c.Key("row-agent-magpie-global-actions-"+agent)).
			Padding(Space0, settingsRowPadX).
			Gap(Space3).
			AlignItems(ui.Center).
			Children(func() {
				reapply := ui.ButtonBase(c.Key("btn-agent-magpie-reapply-"+agent)).
					Label("重新写入").
					Radius(RadiusSM).
					Padding(Space1, Space4)
				if reapply.Hovered() {
					reapply.Background(ThemeHover(c))
				}
				if reapply.Clicked() {
					st.runAgentMagpieGlobalAction(agent, "reapply")
				}
				reapply.Children(func() {
					ui.Text(c, "重新写入").
						FontSize(TypeFootnote).
						TextColor(ThemeAccent(c))
				})

				keep := ui.ButtonBase(c.Key("btn-agent-magpie-keep-"+agent)).
					Label("保留我的修改").
					Radius(RadiusSM).
					Padding(Space1, Space4).
					Background(ThemeHover(c))
				if keep.Hovered() {
					keep.Background(ThemeOverlay(c, 0.12))
				}
				if keep.Clicked() {
					st.runAgentMagpieGlobalAction(agent, "keep")
				}
				keep.Children(func() {
					ui.Text(c, "保留我的修改").
						FontSize(TypeFootnote).
						TextColor(ThemePrimaryText(c))
				})
			})
	}

	if managed := strings.TrimSpace(g.ManagedPath); managed != "" {
		ui.Text(c, "系统管理员配置可能覆盖本设置："+managed).
			FontSize(TypeFootnote).
			TextColor(ThemeWarning(c)).
			Padding(Space0, settingsRowPadX)
	}
}

// renderAgentMagpieNotice 渲染区块内的一行提示文字（加载中、加载失败或没有端点）。
func renderAgentMagpieNotice(c *ui.Context, key, text string, color ui.Color) {
	ui.Row(c.Key("row-"+key)).
		Padding(Space0, settingsRowPadX).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeFootnote).
				TextColor(color)
		})
}
