package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
)

// ModelPickerMode 决定模型选择组件的形态。
type ModelPickerMode int

const (
	// ModelPickerSimple 只选择单个 provider + model，并配置推理强度与快速模式。
	ModelPickerSimple ModelPickerMode = iota
	// ModelPickerSwarm 在单模型之上支持 Swarm 主导/执行配对、Swarm 开关与并发上限。
	ModelPickerSwarm
)

// ModelPickerSide 标识模型选择作用于哪一侧（主导 Lead 或执行 Bees）。
type ModelPickerSide int

const (
	ModelPickerSideLead ModelPickerSide = iota
	ModelPickerSideBees
)

// modelSideDisabledReason 返回某一侧在选择器内被禁用时的原因文案；未禁用返回空串。
func modelSideDisabledReason(overallDisabled bool, sideDisabled bool) string {
	if overallDisabled {
		return "操作进行中，暂不可修改模型设置"
	}
	if sideDisabled {
		return "正在保存该侧模型偏好，暂不可修改"
	}
	return ""
}

// ModelSelection 是一次模型选择的完整结果，可直接序列化到配置或界面状态。
type ModelSelection struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Fast     bool   `json:"fast,omitempty"`
}

// ModelPickerState 保存模型选择组件的展开状态，由调用方持有以便跨帧保持。
// State 为 nil 时组件按 KeyPrefix+ScopeKey 在包内维护状态。
type ModelPickerState struct {
	MenuOpen    bool            // 触发胶囊的 Popover 展开状态
	Tab         string          // Swarm 模式当前分段："lead"（默认）或 "bees"
	ListOpen    map[string]bool // 二级模型列表页展开状态，key 为 ScopeKey+"/"+side
	MaxBeesOpen bool            // 并发 Bee 上限下拉菜单展开状态
	PersonaOpen bool            // 人设子菜单展开状态
	TierEditing string          // 正在为其选择模型的档位 ID，空表示未编辑档位
	tiersOpen   bool            // 列表页档位区是否展开；默认折叠
	openCard    string          // 列表页当前展开的卡片；"" 表示全部收起；未设置时按默认
	openCardSet bool
	searchQuery string // 列表页搜索框内容，按名称筛选模型

	// pageDepth 是上一帧渲染的二级页深度（0 强度卡片、1 模型列表、2 更多模型），
	// pageDir 是最近一次换页的方向（1 进入、-1 返回）；两者共同驱动换页的滑入淡入动画。
	pageDepth int
	pageDir   float32

	// reopenAfterSwarm 记录「点了执行段、编排还在异步开启」：开关生效后要把弹层拉回来并停在执行侧，
	// 否则宿主重建造成的浮层关闭会被当成用户主动关闭，第一次点击看起来就是列表自己关掉了。
	reopenAfterSwarm bool
	reopenDeadline   time.Time
}

// Reset 清空全部选择器展开、分段与临时编辑状态。
func (st *ModelPickerState) Reset() {
	if st == nil {
		return
	}
	st.MenuOpen = false
	st.Tab = ""
	st.ListOpen = nil
	st.MaxBeesOpen = false
	st.PersonaOpen = false
	st.TierEditing = ""
	st.tiersOpen = false
	st.openCard = ""
	st.openCardSet = false
	st.searchQuery = ""
	st.pageDepth = 0
	st.pageDir = 0
	st.reopenAfterSwarm = false
	st.reopenDeadline = time.Time{}
}

// ModelPickerProps 定义模型选择组件的入参。
type ModelPickerProps struct {
	Mode      ModelPickerMode // 组件形态：Simple 或 Swarm
	KeyPrefix string          // 组件内控件 key 前缀，需在同一界面内唯一
	ScopeKey  string          // 展开状态与胶囊文案快照的键（会话 ID 或设置项 ID）
	State     *ModelPickerState

	Providers    []ProviderInfo // 可选 provider 范围
	MagpieFilter modelPickerMagpieFilter
	Selection    ModelSelection // 主导侧（Simple 模式即唯一一侧）的当前选择
	Bees         ModelSelection // Swarm 执行侧选择
	SwarmOn      bool
	HasMessages  bool
	Disabled     bool // 整体禁用（触发胶囊）
	LeadDisabled bool // 主导侧卡片的额外禁用（偏好写入中）
	BeesDisabled bool // 执行侧卡片的额外禁用（偏好写入中）

	Pins            []ModelPin      // 一级页精选模型；为空时直接展示按 provider 分组的全部模型
	ProviderEnabled map[string]bool // 各 provider 的启用状态（false 为已关闭）；列表页据此隐藏其模型，为空表示不过滤
	Pairs           []SwarmPair     // 已完成就绪检查的原始配对，展示在主导侧列表顶部
	PairID          string          // 会话选中的配对 ID，选定则优先高亮这一条
	MaxBees         int             // 并发 Bee 上限

	Tiers             []config.SwarmTier                  // Swarm Bee 档位列表
	TierOverrides     map[string]config.SwarmTierOverride // 会话级档位覆盖
	PairTierOverrides map[string]config.SwarmTierOverride // 当前配对的档位覆盖配置

	CapsuleLabel   string // 触发胶囊的主名称，不含推理强度
	NoEffortSignal bool   // 胶囊不显示推理强度信号格（跟随默认、未选模型时用）
	ListOnly       bool   // 没有强度可调：打开即是模型列表，没有卡片页与返回按钮
	HideBeeName    bool   // 窄宽度时子代理区只留图标、不显示短名
	Tooltip        string
	TriggerLabel   string // 触发按钮的无障碍名，同一界面有多个胶囊时用来区分；为空时使用“选择模型”
	Compact        bool   // 胶囊只显示供应商图标与箭头，完整文案放进提示，供浮动面板这类窄处使用
	Inline         bool   // 只渲染推理强度卡片（供设置页在自有弹层内复用），不渲染触发胶囊与 Popover
	NativeMenu     func(ui.Rect, float32, bool)

	QuotaUsed            func(providerID string) (float64, bool) // 读取 provider 配额用量（可选）
	OnPickModel          func(side ModelPickerSide, prov ProviderInfo, m ModelInfo)
	OnEffort             func(side ModelPickerSide, effort string)
	OnToggleFast         func(side ModelPickerSide)
	OnReset              func(side ModelPickerSide)
	OnFollowLead         func()
	OnToggleSwarm        func(on bool)
	OnApplyPair          func(pair SwarmPair)
	OnSetMaxBees         func(n int)
	OnSetTierOverride    func(tierID string, ov *config.SwarmTierOverride)
	OnResetTierOverrides func()
	// OnRefreshCatalog 请求重新拉取供应商目录；只有传了它，目录为空的供应商才画出“重新探测”按钮。
	OnRefreshCatalog func()
	// OnOpenList 用户进入模型列表页时调用，用于按需加载限额等数据。
	OnOpenList        func()
	OnOpen            func() // 打开模型浮层时调用（可选）
	Personas          []MagpiePersona
	Persona           string
	OnPersona         func(id string)
	CatalogRefreshing bool
	Invalidate        func()
}

type modelPickerMagpieFilter struct{}

func newModelPickerMagpieFilter(_ config.Config) modelPickerMagpieFilter {
	return modelPickerMagpieFilter{}
}

func filterModelPickerMagpie(providers []ProviderInfo, _ modelPickerMagpieFilter) []ProviderInfo {
	filtered := make([]ProviderInfo, 0, len(providers))
	for _, provider := range providers {
		if provider.ID == "mcode" {
			continue
		}
		filtered = append(filtered, provider)
	}
	return filtered
}

// modelPickerStates 保存调用方未持有状态时组件内部按 key 分配的状态。
var (
	modelPickerStatesMu sync.Mutex
	modelPickerStates   = make(map[string]*ModelPickerState)
)

// modelPickerStateFor 返回按 key 复用的组件状态。
func modelPickerStateFor(key string) *ModelPickerState {
	modelPickerStatesMu.Lock()
	defer modelPickerStatesMu.Unlock()
	st, ok := modelPickerStates[key]
	if !ok {
		st = &ModelPickerState{}
		modelPickerStates[key] = st
	}
	return st
}

// modelPickerSideKey 返回侧别在状态键中的名称。
func modelPickerSideKey(side ModelPickerSide) string {
	if side == ModelPickerSideBees {
		return "bees"
	}
	return "lead"
}

func (st *ModelPickerState) listOpen(scopeKey string, side ModelPickerSide) bool {
	if st == nil || st.ListOpen == nil {
		return false
	}
	return st.ListOpen[scopeKey+"/"+modelPickerSideKey(side)]
}

// setListOpen 设置指定侧别二级模型列表页的展开状态。
func (st *ModelPickerState) setListOpen(scopeKey string, side ModelPickerSide, open bool) {
	if st == nil {
		return
	}
	key := scopeKey + "/" + modelPickerSideKey(side)
	if open {
		if st.ListOpen == nil {
			st.ListOpen = make(map[string]bool)
		}
		st.ListOpen[key] = true
		return
	}
	delete(st.ListOpen, key)
}

// modelPickerInvalidate 请求调用方重绘。
func modelPickerInvalidate(p ModelPickerProps) {
	if p.Invalidate != nil {
		p.Invalidate()
	}
}

// 弹层展开期间冻结胶囊文案的包级快照（展开宽度锁定）。
// ui 框架不提供控件宽度测量，改为在弹层打开那一刻快照胶囊文案、关闭时清除，
// 让切换模型/配对后胶囊文字与宽度保持不变，避免弹层随之漂移。
var (
	modelCapsuleSnapMu    sync.Mutex
	modelCapsuleSnapshots = make(map[string]string)
)

func getModelCapsuleSnapshot(scopeKey string) (string, bool) {
	modelCapsuleSnapMu.Lock()
	defer modelCapsuleSnapMu.Unlock()
	label, ok := modelCapsuleSnapshots[scopeKey]
	return label, ok
}

func setModelCapsuleSnapshot(scopeKey, label string) {
	modelCapsuleSnapMu.Lock()
	defer modelCapsuleSnapMu.Unlock()
	modelCapsuleSnapshots[scopeKey] = label
}

func clearModelCapsuleSnapshot(scopeKey string) {
	modelCapsuleSnapMu.Lock()
	defer modelCapsuleSnapMu.Unlock()
	delete(modelCapsuleSnapshots, scopeKey)
	delete(modelCapsuleSnapshots, scopeKey+modelCapsuleBeeSnapshotSuffix)
}

// modelCapsuleBeeSnapshotSuffix 是子代理短名快照的键后缀：与主名称同开同关，保持胶囊宽度锁定。
const modelCapsuleBeeSnapshotSuffix = "/bee"

// modelCapsuleTwoLine 让编组胶囊改用双行布局：主代理在上、子代理在下；
// 改为 false 即整体退回单行。
const modelCapsuleTwoLine = true

// 双行模型胶囊的行高：上行放主模型名与品牌图标，下行放子代理名。
// 两行行高加 Space0 行距正好等于发送按钮的 ControlMD，胶囊不会把底栏撑高。
const (
	modelCapsuleLeadLineHeight = IconSM
	modelCapsuleLineGap        = Space0
)

// 模型列表里各 provider 分组的次要模型展开状态（key: ScopeKey+"/"+side+"/"+providerID）。
var (
	modelPanelProviderMoreMu sync.Mutex
	modelPanelProviderMore   = make(map[string]bool)
)

func isModelPanelProviderMoreOpen(scopeKey, side, providerID string) bool {
	modelPanelProviderMoreMu.Lock()
	defer modelPanelProviderMoreMu.Unlock()
	return modelPanelProviderMore[scopeKey+"/"+side+"/"+providerID]
}

func setModelPanelProviderMoreOpen(scopeKey, side, providerID string, open bool) {
	modelPanelProviderMoreMu.Lock()
	defer modelPanelProviderMoreMu.Unlock()
	if open {
		modelPanelProviderMore[scopeKey+"/"+side+"/"+providerID] = true
	} else {
		delete(modelPanelProviderMore, scopeKey+"/"+side+"/"+providerID)
	}
}

// 二级模型列表里各 provider 分组的折叠状态（key: ScopeKey+"/"+side+"/"+providerID）。
// 关闭弹层时不清除，折叠偏好保留到本次进程结束。
var (
	modelPanelProviderCollapsedMu sync.Mutex
	modelPanelProviderCollapsed   = make(map[string]bool)
)

func isModelPanelProviderCollapsed(scopeKey, side, providerID string) bool {
	modelPanelProviderCollapsedMu.Lock()
	defer modelPanelProviderCollapsedMu.Unlock()
	return modelPanelProviderCollapsed[scopeKey+"/"+side+"/"+providerID]
}

func setModelPanelProviderCollapsed(scopeKey, side, providerID string, collapsed bool) {
	modelPanelProviderCollapsedMu.Lock()
	defer modelPanelProviderCollapsedMu.Unlock()
	if collapsed {
		modelPanelProviderCollapsed[scopeKey+"/"+side+"/"+providerID] = true
	} else {
		delete(modelPanelProviderCollapsed, scopeKey+"/"+side+"/"+providerID)
	}
}

// modelPickerQueryMatch 判断模型是否命中列表页搜索词；查询为空时一律命中。
// 规则与设置页 filterProviderModels 一致：大小写不敏感，命中模型名、ID、说明、Agent 名称/ID
// 或模型行的实际显示名之一即可。
func modelPickerQueryMatch(prov ProviderInfo, m ModelInfo, query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return true
	}
	q := strings.ToLower(trimmed)
	for _, field := range []string{m.Name, m.ID, m.Detail, prov.Name, prov.ID, defaultModelRowDisplayName(prov, m)} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// hasDefaultModel 判断模型列表中是否存在标记为默认的模型。
func hasDefaultModel(models []ModelInfo) bool {
	for _, m := range models {
		if m.IsDefault {
			return true
		}
	}
	return false
}

// currentModelDisplayName 计算模型胶囊上应当显示的名称。
// 继承默认时显示实际解析出的模型名，
// 解析不出模型名时显示“默认模型”；侧边栏徽章等“有指定非默认模型才展示”的场景也沿用该判定。
func currentModelDisplayName(providers []ProviderInfo, currentProvider, modelID string) string {
	name, _, _ := modelDisplayIdentity(providers, currentProvider, modelID)
	return name
}

// effectiveModelName 解析 provider 下实际生效的模型名：
// modelID 为空或为默认模型时取该 provider 的默认模型（无默认则取首个），解析不出时返回空串。
func effectiveModelName(providers []ProviderInfo, providerID, modelID string) string {
	prov, ok := findProviderInfo(providers, providerID)
	if !ok {
		return ""
	}
	var picked ModelInfo
	if modelID == "" {
		picked = resolveModelInfo(prov.Models, "")
	} else {
		for _, m := range prov.Models {
			if m.ID == modelID {
				picked = m
				break
			}
		}
		if picked.ID == "" {
			return ""
		}
		if picked.IsDefault {
			picked = resolveModelInfo(prov.Models, "")
		}
	}
	if picked.Name != "" {
		return picked.Name
	}
	return picked.ID
}

// modelDisplayIdentity 汇总模型身份：Name 为显示名（默认时为实际解析出的模型名），
// NameIsDefault 标记显示名是否表示“继承默认配置”，Provider 为 provider 显示名。
// 卡片标题与胶囊共用，保证两处文案一致。
func modelDisplayIdentity(providers []ProviderInfo, providerID, modelID string) (name string, nameIsDefault bool, provName string) {
	effective := effectiveModelName(providers, providerID, modelID)
	isDefault := strings.TrimSpace(modelID) == ""
	if !isDefault {
		for _, p := range providers {
			if p.ID != providerID {
				continue
			}
			for _, m := range p.Models {
				if m.ID == modelID && m.IsDefault {
					isDefault = true
				}
			}
		}
	}
	if isDefault {
		if effective != "" {
			return effective, true, modelProviderDisplayName(providers, providerID)
		}
		return "默认模型", true, modelProviderDisplayName(providers, providerID)
	}
	if modelID != "" {
		if effective == "" {
			effective = modelID
		}
		return effective, false, modelProviderDisplayName(providers, providerID)
	}
	return "默认模型", true, modelProviderDisplayName(providers, providerID)
}

// modelProviderDisplayName 返回 provider 的展示名：优先 ProviderInfo.Name，其次内置映射，最后原 ID。
func modelProviderDisplayName(providers []ProviderInfo, providerID string) string {
	if prov, ok := findProviderInfo(providers, providerID); ok && prov.Name != "" {
		return prov.Name
	}
	if strings.TrimSpace(providerID) == "" {
		return ""
	}
	return providerDisplayName(providerID)
}

// modelPickerDisplayName 返回模型选择组件使用的显示名：继承默认时显示实际解析出的模型名，否则显示真实模型名或 ID。
func modelPickerDisplayName(providers []ProviderInfo, providerID, modelID string) string {
	name, _, _ := modelDisplayIdentity(providers, providerID, modelID)
	return name
}

// modelCapsuleLabel 在模型名后追加推理强度，格式为空格分隔、首字母大写，
// 例如「Opus 5.5 Medium」；强度为空时不追加。
func modelCapsuleLabel(name, effort string) string {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return name
	}
	capEffort := modelEffortTitle(effort)
	if name == "" {
		return capEffort
	}
	return name + " " + capEffort
}

// modelEffortTitle 把推理强度档位转成标题格式：先转小写再归一化，首字母大写，
// xhigh/extra-high → Extra High；“HIGH”也得到 High。
func modelEffortTitle(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "":
		return "Default"
	case "xhigh", "extra-high", "extra_high", "extra high":
		return "Extra High"
	}
	r := []rune(effort)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// modelPickerModelInfo 解析指定 provider 下某个模型的元数据；未指定时回退默认模型或首个模型。
func modelPickerModelInfo(providers []ProviderInfo, providerID, modelID string) ModelInfo {
	prov, ok := findProviderInfo(providers, providerID)
	if !ok {
		if modelID == "" {
			return ModelInfo{}
		}
		return ModelInfo{ID: modelID, Name: modelID}
	}
	return resolveModelInfo(prov.Models, modelID)
}

// resolveModelPickerSelection 解析组件实际使用的模型选择：已指定模型时原样返回；
// 未指定时优先使用上次记录的模型（provider 与模型都仍可用），其次使用该 provider 模型列表的第一个。
// provider 也为空时同样优先采用上次记录的 provider。
func resolveModelPickerSelection(providers []ProviderInfo, sel ModelSelection) ModelSelection {
	last, hasLast := loadLastModelSelection()
	return resolveModelPickerSelectionWith(providers, sel, last, hasLast, nil)
}

// resolveModelPickerSelectionForProject 在 resolveModelPickerSelection 的基础上按项目恢复：
// 项目桶里有记录时以它作为「上次使用」，没有记录时退回全局记忆（行为与旧版完全一致）。
func resolveModelPickerSelectionForProject(providers []ProviderInfo, sel ModelSelection, projectID string, pins []ModelPin) ModelSelection {
	last, hasLast := lastSelectionForProject(projectID)
	return resolveModelPickerSelectionWith(providers, sel, last, hasLast, pins)
}

// lastSelectionForProject 返回某个项目上次使用的模型选择：项目桶优先，
// 项目桶里没有完整的 provider+model 时退回全局最近一次选择。
func lastSelectionForProject(projectID string) (ModelSelection, bool) {
	if proj, ok := loadProjectModelSelection(projectID); ok && proj.Provider != "" && proj.Model != "" {
		return ModelSelection{Provider: proj.Provider, Model: proj.Model, Effort: proj.Effort, Fast: proj.Fast}, true
	}
	return loadLastModelSelection()
}

// resolveModelPickerSelectionWith 是 resolveModelPickerSelection 的实现体：
// last/hasLast 是「上次使用」的快照，由调用方按项目或全局选取。
func resolveModelPickerSelectionWith(providers []ProviderInfo, sel ModelSelection, last ModelSelection, hasLast bool, pins []ModelPin) ModelSelection {
	if strings.TrimSpace(sel.Model) != "" {
		if strings.TrimSpace(sel.Provider) != "" {
			if prov, ok := findProviderInfo(providers, sel.Provider); ok {
				if m, found := findModelInfo(prov.Models, sel.Model); found {
					sel.Model = m.ID
				}
				return sel
			}
		}
		for _, prov := range providers {
			if m, found := findModelInfo(prov.Models, sel.Model); found {
				sel.Provider = prov.ID
				sel.Model = m.ID
				break
			}
		}
		return sel
	}
	if strings.TrimSpace(sel.Provider) == "" {
		if hasLast {
			sel.Provider = last.Provider
		} else if len(providers) > 0 {
			sel.Provider = providers[0].ID
		}
	}
	prov, ok := findProviderInfo(providers, sel.Provider)
	if !ok {
		return sel
	}
	if hasLast && last.Provider == sel.Provider {
		if modelUsable(prov, last.Model, pins) {
			sel.Model = last.Model
			return sel
		}
	}
	if len(prov.Models) > 0 {
		sel.Model = prov.Models[0].ID
	}
	return sel
}

// currentSwarmPair 在会话 Swarm 开启时按 Lead 与 Bees 配置匹配第一个配对模板。
// th.PairID 非空时优先认定这条配对（仍需与当前模型一致）。
func currentSwarmPair(pairs []SwarmPair, th Thread, leadProvider, leadModel string) (SwarmPair, bool) {
	return currentSwarmPairFor(pairs, th.PairID, leadProvider, leadModel, th.BeesProvider, th.BeesModel)
}

// currentSwarmPairFor 先按会话选中的配对 ID 认定，再回退到按配置推断：
// 选中的配对存在且仍与当前 Lead 和 Bees 的 provider/model 一致时用它，
// 否则按当前配置推断。推断分两轮扫描：第一轮只取 LeadModel 非空且完全一致的配对，
// 第二轮才接受 LeadModel 为空的通配配对，同一轮内按列表顺序取第一条（与后端 MatchPair 一致）。
func currentSwarmPairFor(pairs []SwarmPair, pairID, leadProvider, leadModel, beesProvider, beesModel string) (SwarmPair, bool) {
	if pairID != "" {
		for _, p := range pairs {
			// 模型已经被换掉时这条配对不再代表当前会话，回退到按模型推断。
			if p.ID == pairID && swarmPairMatches(p, leadProvider, leadModel, beesProvider, beesModel) {
				return p, true
			}
		}
	}
	for pass := 0; pass < 2; pass++ {
		for _, p := range pairs {
			if pass == 0 {
				if p.LeadModel == "" {
					continue
				}
			} else if p.LeadModel != "" {
				continue
			}
			if swarmPairMatches(p, leadProvider, leadModel, beesProvider, beesModel) {
				return p, true
			}
		}
	}
	return SwarmPair{}, false
}

// swarmPairMatches 判断单条配对是否与当前 Lead 与 Bees 的 provider/model 一致。
// Bees 为空视作跟随 Lead；配对 WorkerProvider 为空时回退 LeadProvider（swarmWorkerProvider 语义），
// WorkerModel 为空时跟随配对 LeadModel。配对一侧的 LeadModel 为空视为通配：只要求 LeadProvider 相等，
// Worker 的比较同样回落到这条通配规则（模型为空即不再比较）。
func swarmPairMatches(p SwarmPair, leadProvider, leadModel, beesProvider, beesModel string) bool {
	if p.LeadProvider != leadProvider {
		return false
	}
	if p.LeadModel != "" && p.LeadModel != leadModel {
		return false
	}
	if beesProvider == "" {
		beesProvider = leadProvider
	}
	if beesModel == "" {
		beesModel = leadModel
	}
	if swarmWorkerProvider(p) != beesProvider {
		return false
	}
	workerModel := p.WorkerModel
	if workerModel == "" {
		workerModel = p.LeadModel
	}
	return workerModel == "" || workerModel == beesModel
}

// readySwarmPairs 过滤出在当前提供方目录中两侧都能解析的配对模板：
// Lead 与 Worker 的 provider 都存在，且各自指定的模型在该 provider 的模型列表中存在；
// 模型为空表示不指定，不做校验。
func readySwarmPairs(cfg config.Config, pairs []SwarmPair, providers []ProviderInfo, pins []ModelPin) []SwarmPair {
	var out []SwarmPair
	for _, p := range pairs {
		leadProvider, leadModel, leadMapped := apisource.NormalizeBeeModel(cfg, nil, p.LeadProvider, p.LeadModel)
		if p.LeadProvider == "mcode" {
			if _, ok := findProviderInfo(providers, p.LeadProvider); !ok || !leadMapped {
				continue
			}
		}
		leadProv, ok := findProviderInfo(providers, leadProvider)
		if !ok || (!modelUsable(leadProv, leadModel, pins) && !providerHasPinnedModel(p.LeadProvider, p.LeadModel, pins)) {
			continue
		}
		originalWorkerProvider, originalWorkerModel := swarmWorkerProvider(p), p.WorkerModel
		if originalWorkerModel == "" {
			originalWorkerModel = p.LeadModel
		}
		workerProvider, workerModel, workerMapped := apisource.NormalizeBeeModel(cfg, nil, originalWorkerProvider, originalWorkerModel)
		if originalWorkerProvider == "mcode" && !workerMapped {
			continue
		}
		workerProv, ok := findProviderInfo(providers, workerProvider)
		if !ok || (!modelUsable(workerProv, workerModel, pins) && !providerHasPinnedModel(originalWorkerProvider, originalWorkerModel, pins)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// providerHasPlaceholderCatalog 判断 provider 目录是否为空或仅有占位模型。
func providerHasPlaceholderCatalog(prov ProviderInfo) bool {
	if len(prov.Models) == 0 {
		return true
	}
	return len(prov.Models) == 1 && prov.Models[0].ID == "default"
}

// providerHasModel 判断 provider 的模型目录中是否存在指定模型；modelID 为空视为通过。
// 目录为空、仅有占位行，或属于 CLI 动态目录的 provider 放行任意非空模型名（对齐后端 IsValidModel）。
func providerHasModel(prov ProviderInfo, modelID string) bool {
	if modelID == "" {
		return true
	}
	for _, m := range prov.Models {
		if m.ID == modelID {
			return true
		}
	}
	if providerHasPlaceholderCatalog(prov) || harness.IsCLICatalogProvider(prov.ID) {
		return true
	}
	return false
}

// providerHasPinnedModel 判断模型是否在用户固定的模型列表里（跨目录的自定义模型 ID）。
func providerHasPinnedModel(provider, modelID string, pins []ModelPin) bool {
	for _, pin := range pins {
		if pin.Provider == provider && pin.ModelID == modelID {
			return true
		}
	}
	return false
}

// modelUsable 在目录校验之外放行已固定的模型，避免恢复记忆时把自定义模型换成目录首项。
func modelUsable(prov ProviderInfo, modelID string, pins []ModelPin) bool {
	return providerHasModel(prov, modelID) || providerHasPinnedModel(prov.ID, modelID, pins)
}

// swarmPairModelLabel 返回配对一侧模型的显示名；modelID 为空时返回 fallback。
func swarmPairModelLabel(providers []ProviderInfo, providerID, modelID, fallback string) string {
	if modelID == "" {
		return fallback
	}
	for _, p := range providers {
		if p.ID == providerID {
			for _, m := range p.Models {
				if m.ID == modelID {
					if m.Name != "" {
						return m.Name
					}
					return m.ID
				}
			}
		}
	}
	return modelID
}

// swarmPairModelShortLabel 与 swarmPairModelLabel 规则相同，但返回胶囊用的模型短名。
func swarmPairModelShortLabel(providers []ProviderInfo, providerID, modelID, fallback string) string {
	if modelID == "" {
		return fallback
	}
	return modelShortDisplayName(providers, providerID, modelID)
}

// swarmPairTooltip 生成配对胶囊的悬停提示：「名称 · 强度 · Lead 模型 + Bee 模型」。
func swarmPairTooltip(p SwarmPair, effort string, providers []ProviderInfo) string {
	name := p.Name
	if name == "" {
		name = "编排配对"
	}
	parts := []string{name}
	if label := effortLabel(effort); label != "" {
		parts = append(parts, label)
	}
	lead := swarmPairModelLabel(providers, p.LeadProvider, p.LeadModel, "任意模型")
	worker := swarmPairModelLabel(providers, swarmWorkerProvider(p), p.WorkerModel, "跟随主代理")
	parts = append(parts, lead+" + "+worker)
	return strings.Join(parts, " · ")
}

// trimModelIDPrefix 去掉模型 id 的第一段（`<provider>/`），没有可去掉的前缀时原样返回。
// 只用于匹配兜底：pi 这类 CLI 的模型 id 自带斜杠（`jp/group/x`），会话里存下的可能是
// 去掉 provider 前缀的裸 id，而目录里是带前缀的完整 id。
func trimModelIDPrefix(modelID string) string {
	if index := strings.IndexByte(modelID, '/'); index > 0 && index < len(modelID)-1 {
		return modelID[index+1:]
	}
	return modelID
}

// findModelInfo 在模型列表里查模型：先精确匹配完整 id，再认“带 / 不带 provider 前缀”的
// 另一种形状（两种形状指同一个模型，不跨 provider 比较）。查不到时返回 false。
func findModelInfo(models []ModelInfo, modelID string) (ModelInfo, bool) {
	wanted := strings.TrimSpace(modelID)
	if wanted == "" {
		return ModelInfo{}, false
	}
	for _, m := range models {
		if m.ID == wanted {
			return m, true
		}
	}
	trimmed := trimModelIDPrefix(wanted)
	for _, m := range models {
		if m.ID == trimmed || trimModelIDPrefix(m.ID) == wanted || trimModelIDPrefix(m.ID) == trimmed {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// modelMatches 判断列表中第 mIdx 个模型是否对应已选模型 ID（为空时取默认模型，没有默认则取第一个）。
func modelMatches(models []ModelInfo, selected string, mIdx int, m ModelInfo) bool {
	if _, ok := findModelInfo([]ModelInfo{m}, selected); ok {
		return true
	}
	return (selected == "" && m.IsDefault) ||
		(selected == "" && !hasDefaultModel(models) && mIdx == 0)
}

// resolveModelInfo 在模型列表中查找指定 ID 的模型；找不到时优先回退至默认模型，再找不到返回首个模型。
func resolveModelInfo(models []ModelInfo, modelID string) ModelInfo {
	if modelID != "" {
		if m, ok := findModelInfo(models, modelID); ok {
			return m
		}
		return ModelInfo{ID: modelID, Name: modelID}
	}
	if len(models) == 0 {
		return ModelInfo{}
	}
	for _, m := range models {
		if m.IsDefault {
			return m
		}
	}
	return models[0]
}

// modelPickerPinnedEntry 是一级页展示的精选模型条目。
type modelPickerPinnedEntry struct {
	Provider ProviderInfo
	Model    ModelInfo
}

// modelPickerPinnedEntries 把设置中固定的模型解析为当前可用的条目。
func modelPickerPinnedEntries(pins []ModelPin, providers []ProviderInfo) []modelPickerPinnedEntry {
	seen := make(map[string]bool)
	var list []modelPickerPinnedEntry
	for _, pin := range pins {
		prov, ok := findProviderInfo(providers, pin.Provider)
		if !ok {
			continue
		}
		var m ModelInfo
		for _, model := range prov.Models {
			if model.ID == pin.ModelID && model.ID != "" {
				m = model
				break
			}
		}
		if m.ID == "" {
			continue
		}
		key := prov.ID + "/" + m.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		list = append(list, modelPickerPinnedEntry{Provider: prov, Model: m})
	}
	return list
}

// modelPickerDisabledProviders 把启用状态表转成 filterPinsByEnabledProviders /
// filterProvidersByEnabled 使用的过滤表：只保留已关闭、且不在 keep 中的 provider（值为 false）。
// keep 传入当前正在使用的 provider，保证已有会话仍能看到自己的模型。
func modelPickerDisabledProviders(p ModelPickerProps, keep ...string) map[string]bool {
	if len(p.ProviderEnabled) == 0 {
		return nil
	}
	disabled := make(map[string]bool)
	for id, on := range p.ProviderEnabled {
		if on || id == "" {
			continue
		}
		inUse := false
		for _, k := range keep {
			if k != "" && k == id {
				inUse = true
				break
			}
		}
		if !inUse {
			disabled[id] = false
		}
	}
	return disabled
}

// modelPickerSide 汇总某一侧渲染所需的 provider 与模型信息。
type modelPickerSide struct {
	Side        ModelPickerSide
	Provider    ProviderInfo
	Model       ModelInfo
	Selection   ModelSelection
	FollowsLead bool // 执行侧未指定 provider/model，跟随主导
}

// modelPickerSideInfo 解析指定侧别的 provider 与模型；执行侧未指定时跟随主导。
func modelPickerSideInfo(p ModelPickerProps, side ModelPickerSide) modelPickerSide {
	lead := p.Selection
	sel := lead
	followsLead := false
	if side == ModelPickerSideBees {
		sel = p.Bees
		if sel.Provider == "" && sel.Model == "" {
			followsLead = true
			sel.Provider = lead.Provider
			sel.Model = lead.Model
		}
	}
	if strings.TrimSpace(sel.Provider) == "" && strings.TrimSpace(sel.Model) != "" {
		for _, prov := range p.Providers {
			if _, ok := findModelInfo(prov.Models, sel.Model); ok {
				sel.Provider = prov.ID
				break
			}
		}
	}
	prov, ok := findProviderInfo(p.Providers, sel.Provider)
	if !ok {
		prov = ProviderInfo{ID: sel.Provider}
	}
	return modelPickerSide{
		Side:        side,
		Provider:    prov,
		Model:       resolveModelInfo(prov.Models, sel.Model),
		Selection:   sel,
		FollowsLead: followsLead,
	}
}

// modelPickerModelTitle 返回某一侧的模型名，继承默认时显示实际解析出的模型名。
// name 为空时退回 provName。
func modelPickerModelTitle(providers []ProviderInfo, s modelPickerSide) string {
	name, _, provName := modelDisplayIdentity(providers, s.Selection.Provider, s.Selection.Model)
	if name == "" {
		return provName
	}
	return name
}

// modelPickerSideCaption 返回档位标题下方的模型说明行（模型名）：
// 主导侧返回主导模型名；执行侧跟随主导时注明跟随关系，否则返回执行模型名。
func modelPickerSideCaption(p ModelPickerProps, side ModelPickerSide) string {
	lead := modelPickerSideInfo(p, ModelPickerSideLead)
	bees := modelPickerSideInfo(p, ModelPickerSideBees)
	leadTitle := modelPickerModelTitle(p.Providers, lead)
	beesTitle := modelPickerModelTitle(p.Providers, bees)

	if side == ModelPickerSideBees {
		if bees.FollowsLead {
			return fmt.Sprintf("跟随主导: %s", leadTitle)
		}
		return beesTitle
	}
	return leadTitle
}

// renderModelPicker 渲染公共模型选择组件。
// Swarm 模式提供主导/执行两侧的模型、推理强度、快速模式、配对模板与并发上限（即 Composer 的完整功能）；
// Simple 模式只提供单侧 provider + model、推理强度与快速模式。
func renderModelPicker(c *ui.Context, p ModelPickerProps) {
	state := p.State
	if state == nil {
		state = modelPickerStateFor(p.KeyPrefix + p.ScopeKey)
	}
	if p.Inline {
		renderModelPickerCard(c, p, state, ModelPickerSideLead)
		return
	}
	// 点执行段会异步开启编排：开关生效前宿主已重建，浮层那次关闭会被当成用户主动关掉面板。
	// 开关一到就把弹层拉回来并停在执行侧，这一次点击才算真正落在「子代理」上。
	if state.reopenAfterSwarm && p.SwarmOn && time.Now().Before(state.reopenDeadline) {
		state.reopenAfterSwarm = false
		state.MenuOpen = true
		state.Tab = "bees"
	}

	lead := modelPickerSideInfo(p, ModelPickerSideLead)
	bees := modelPickerSideInfo(p, ModelPickerSideBees)
	paired := p.Mode == ModelPickerSwarm && p.SwarmOn && !bees.FollowsLead
	// 子代理与主代理的 provider 和 model 都相同时保持单图标；同服务商不同模型仍需区分。
	showBee := paired && (providerIconKey(lead.Selection.Provider) != providerIconKey(bees.Selection.Provider) || lead.Selection.Model != bees.Selection.Model)
	// 双行只有编组且子代理另配模型时才成立；窄宽度隐藏子代理名时第二行没有文字，
	// 以及 Compact 时不显示文字，都退回单行只留图标。
	twoLine := modelCapsuleTwoLine && showBee && !p.Compact && !p.HideBeeName
	leadBrand := modelBrandForModel(p.Providers, lead.Selection.Provider, lead.Selection.Model)
	beeBrand := modelBrandForModel(p.Providers, bees.Selection.Provider, bees.Selection.Model)
	beeLabel := modelShortDisplayName(p.Providers, bees.Selection.Provider, bees.Selection.Model)

	// 展开宽度锁定：弹层打开期间胶囊使用打开那一刻的文案快照，关闭时清除。
	capsuleLabel := p.CapsuleLabel
	if state.MenuOpen {
		if snap, ok := getModelCapsuleSnapshot(p.ScopeKey); ok {
			capsuleLabel = snap
		} else {
			setModelCapsuleSnapshot(p.ScopeKey, capsuleLabel)
		}
		if snap, ok := getModelCapsuleSnapshot(p.ScopeKey + modelCapsuleBeeSnapshotSuffix); ok {
			beeLabel = snap
		} else {
			setModelCapsuleSnapshot(p.ScopeKey+modelCapsuleBeeSnapshotSuffix, beeLabel)
		}
	} else {
		clearModelCapsuleSnapshot(p.ScopeKey)
		if state != nil {
			state.PersonaOpen = false
			state.TierEditing = ""
			state.Tab = ""
			state.setListOpen(p.ScopeKey, ModelPickerSideLead, false)
			state.setListOpen(p.ScopeKey, ModelPickerSideBees, false)
			// 关闭时把换页动画状态归零：下次打开直接显示强度卡片，不再补播上一轮的换页动效。
			state.pageDepth = 0
			state.pageDir = 0
		}
	}

	// 触发按钮的无障碍名：调用方传了 TriggerLabel 就原样使用（如“选择主导模型”），
	// 否则固定为“选择模型”；不再拼接模型名或强度，胶囊上的可见文案由内部 Text 呈现。
	triggerLabel := p.TriggerLabel
	if triggerLabel == "" {
		triggerLabel = "选择模型"
	}

	// 双行胶囊按固定行高排满（IconSM + Space0 + Space4 = ControlMD），纵向内边距归零，
	// 整体高度正好等于右侧发送按钮；单行胶囊保留 Space1 的上下内距。
	triggerPadY := Space1
	if twoLine {
		triggerPadY = 0
	}

	renderSelectorRow := func() {
		ui.Row(c.Key(p.KeyPrefix + "model-selector-row")).AlignItems(ui.Center).Children(func() {
			trigger := ui.ButtonBase(c.Key(p.KeyPrefix+"model-trigger")).
				Label(triggerLabel).
				Disabled(p.Disabled).
				Tooltip(p.Tooltip).
				Padding(triggerPadY, Space3).
				Radius(RadiusXL)
			scale := motionPressScale(c, trigger, "button-press-scale", !p.Disabled && trigger.Pressed())
			trigger.Scale(scale, scale)
			if p.KeyPrefix != "composer-" {
				trigger.Margin(0, Space2, 0, 0)
			}
			composerHoverBg(c, trigger, state.MenuOpen || trigger.Hovered())
			if p.Disabled {
				trigger.Tooltip("操作进行中，暂不可修改模型设置")
			}

			trigger.OnClick(func() {
				state.MenuOpen = !state.MenuOpen
				// 用户自己动手开关弹层：此前的「等编排生效再拉回弹层」意图作废。
				state.reopenAfterSwarm = false
				state.reopenDeadline = time.Time{}
				if state.MenuOpen {
					// 打开那一刻快照胶囊文案（展开宽度锁定），子代理短名同开同关。
					setModelCapsuleSnapshot(p.ScopeKey, p.CapsuleLabel)
					setModelCapsuleSnapshot(p.ScopeKey+modelCapsuleBeeSnapshotSuffix, beeLabel)
					state.PersonaOpen = false
					state.setListOpen(p.ScopeKey, ModelPickerSideLead, false)
					state.setListOpen(p.ScopeKey, ModelPickerSideBees, false)
					state.TierEditing = ""
					state.Tab = ""
					if p.OnOpen != nil {
						p.OnOpen()
					}
				} else {
					clearModelCapsuleSnapshot(p.ScopeKey)
					state.PersonaOpen = false
					state.TierEditing = ""
					state.Tab = ""
					state.setListOpen(p.ScopeKey, ModelPickerSideLead, false)
					state.setListOpen(p.ScopeKey, ModelPickerSideBees, false)
				}
				modelPickerInvalidate(p)
			})

			// 单行布局：未启用双行时原样保留旧的单行渲染。
			renderSingleLine := func() {
				ui.Row(c.Key(p.KeyPrefix + "model-trigger-content")).
					Gap(Space1).
					AlignItems(ui.Center).
					Children(func() {
						renderModelBrandIcon(c, lead.Selection.Provider, leadBrand, 14)
						if !p.Compact {
							ui.Text(c, capsuleLabel).
								FontSize(uiFont(FontSizeCapsuleText)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine().Ellipsis("…").MaxWidth(140)
						}
						if !p.Compact && !p.NoEffortSignal {
							renderEffortSignal(c, p.KeyPrefix+"model-effort-signal", EffortSliderCardProps{
								Efforts:        lead.Model.Efforts,
								Current:        lead.Selection.Effort,
								DefaultEffort:  lead.Model.DefaultEffort,
								SupportsFast:   lead.Model.SupportsFast,
								FastMode:       lead.Selection.Fast,
								ProviderID:     lead.Selection.Provider,
								BrandID:        leadBrand,
								PairProviderID: bees.Selection.Provider,
								PairBrandID:    beeBrand,
								Paired:         showBee,
							})
						}
						if lead.Model.SupportsFast && lead.Selection.Fast {
							ui.Icon(c, IconByName("zap")).Size(12, 12).TextColor(ThemeWarning(c))
						}
						if showBee {
							ui.Text(c, "·").
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c).Alpha(0.5))
							beeIconAlpha := float32(0.7)
							if trigger.Hovered() || state.MenuOpen {
								beeIconAlpha = 1
							}
							ui.Box(c.Key(p.KeyPrefix+"model-bee-icon")).
								Size(13, 13).
								Center().
								Opacity(beeIconAlpha).
								Children(func() {
									renderModelBrandIcon(c, bees.Selection.Provider, beeBrand, 13)
								})
							if !p.Compact && !p.HideBeeName {
								ui.Text(c, beeLabel).
									FontSize(uiFont(FontSizeProjectName)).
									TextColor(ThemeSecondaryText(c).Alpha(0.75)).
									SingleLine().Ellipsis("…").MaxWidth(110)
							}
						}
						ui.Icon(c, IconChevronDown()).
							Size(10, 10).
							TextColor(ThemeSecondaryText(c))
					})
			}

			// 双行布局：上行主代理，下行子代理；两行的图标盒左对齐。
			renderTwoLine := func() {
				beeIconAlpha := float32(0.7)
				if trigger.Hovered() || state.MenuOpen {
					beeIconAlpha = 1
				}
				ui.Row(c.Key(p.KeyPrefix + "model-trigger-content")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Column(c.Key(p.KeyPrefix + "model-trigger-lines")).
							Gap(modelCapsuleLineGap).
							AlignItems(ui.Start).
							Children(func() {
								// 两行都用固定行高：单行文字的测量高度不受 LineHeight 影响，固定行高才能压住总高。
								ui.Row(c.Key(p.KeyPrefix + "model-trigger-lead-line")).
									Height(modelCapsuleLeadLineHeight).
									Gap(Space1).
									AlignItems(ui.Center).
									Children(func() {
										renderModelBrandIcon(c, lead.Selection.Provider, leadBrand, 13)
										ui.Text(c, capsuleLabel).
											FontSize(uiFont(FontSizeCapsuleText)).
											FontWeight(500).
											TextColor(ThemePrimaryText(c).Alpha(0.9)).
											LineHeight(1).
											SingleLine().Ellipsis("…").MaxWidth(150)
										if !p.NoEffortSignal {
											renderEffortSignal(c, p.KeyPrefix+"model-effort-signal", EffortSliderCardProps{
												Efforts:        lead.Model.Efforts,
												Current:        lead.Selection.Effort,
												DefaultEffort:  lead.Model.DefaultEffort,
												SupportsFast:   lead.Model.SupportsFast,
												FastMode:       lead.Selection.Fast,
												ProviderID:     lead.Selection.Provider,
												BrandID:        leadBrand,
												PairProviderID: bees.Selection.Provider,
												PairBrandID:    beeBrand,
												Paired:         showBee,
											})
										}
										if lead.Model.SupportsFast && lead.Selection.Fast {
											ui.Icon(c, IconByName("zap")).Size(12, 12).TextColor(ThemeWarning(c))
										}
									})
								ui.Row(c.Key(p.KeyPrefix + "model-trigger-bee-line")).
									Height(modelPickerBeeLineHeight).
									Gap(Space1).
									AlignItems(ui.Center).
									Children(func() {
										// 盒子宽 13，与上一行的主代理图标左对齐。
										ui.Box(c.Key(p.KeyPrefix+"model-bee-icon")).
											Size(13, 10).
											Center().
											Opacity(beeIconAlpha).
											Children(func() {
												renderModelBrandIcon(c, bees.Selection.Provider, beeBrand, 10)
											})
										ui.Text(c, beeLabel).
											FontSize(uiFont(FontSizeProjectName)).
											TextColor(ThemeSecondaryText(c).Alpha(0.75)).
											LineHeight(1).
											SingleLine().Ellipsis("…").MaxWidth(150)
									})
							})
						ui.Icon(c, IconChevronDown()).
							Size(10, 10).
							TextColor(ThemeSecondaryText(c))
					})
			}

			trigger.Children(func() {
				if twoLine {
					renderTwoLine()
					return
				}
				renderSingleLine()
			})

			// 面板锚点：始终按触发胶囊的 Bounds 定位——面板水平居中于胶囊，PopoverBase 路径下箭头对准胶囊中心
			// （见下方 DrawOver：用胶囊中心的绝对 x 减去面板左边缘得到偏移，再限制在圆角内）。
			// 读不到 Bounds 时 PopoverBase 自身的居中兜底会让箭头保持正中。
			anchor := trigger

			if p.NativeMenu != nil {
				p.NativeMenu(anchor.Bounds(), c.Zoom(), state.MenuOpen)
				return
			}
			if flyoutHostWindow(c.Services()) != nil {
				appFlyoutPopover(c, p.KeyPrefix+"model-popover", anchor, &state.MenuOpen, nil, appFlyoutProps{Width: modelPickerPanelWidth, Placement: mygo.PlacementTop, Background: &modelPickerPanelBg, RiseY: Space4}, func(fc *ui.Context) {
					renderModelPickerMenuBody(fc, p, state)
				})
				return
			}
			ui.PopoverBase(c.Key(p.KeyPrefix+"model-popover"), anchor, &state.MenuOpen, func(panel ui.Element) {
				panelBg := modelPickerPanelBg
				borderColor := ColorBorderDark
				// DrawOver 可能在只重绘的 pass 执行，那时旧 Element 已过期：构建期先读出锚点位置。
				anchorBounds := anchor.Bounds()
				// 面板是固定深色面（与 effort_slider.go 共用 modelPickerPanelBg），阴影取
				// 固定深色面一致的 ElevDialog，不再写死一档 rgba 阴影。
				panel = applyElevation(c, panel, ElevDialog)
				panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).
					Transition(motionPopover(c, Space4)).
					Margin(0, 0, Space3, 0).
					Radius(modelPickerPanelRadius).
					Background(panelBg).
					Border(1, borderColor).
					DrawOver(func(painter *ui.Painter, r ui.Rect) {
						// 底部箭头指向锚点（模型胶囊）的水平中心：用锚点 Bounds 的中心与面板左边缘的差值
						// 算出偏移，并限制在面板圆角区域之内，避免箭头压到圆角上。
						tipOffset := r.W / 2
						if anchorBounds.W > 0 {
							tipOffset = anchorBounds.X + anchorBounds.W/2 - r.X
						}
						// 面板圆角 22、箭头半宽 7，再留 4 的安全边距：箭头尖端始终落在圆角之外。
						const arrowInset = float32(33)
						if r.W > arrowInset*2 {
							tipOffset = min(max(tipOffset, arrowInset), r.W-arrowInset)
						}
						tipX := r.X + tipOffset
						baseY := r.Y + r.H
						var arrow ui.Path
						arrow.MoveTo(tipX-7, baseY-1)
						arrow.LineTo(tipX, baseY+6)
						arrow.LineTo(tipX+7, baseY-1)
						arrow.Close()
						painter.FillPath(&arrow, panelBg)

						var stroke ui.Path
						stroke.MoveTo(tipX-7, baseY-1)
						stroke.LineTo(tipX, baseY+6)
						stroke.LineTo(tipX+7, baseY-1)
						painter.StrokePath(&stroke, 1, borderColor)

						painter.Line(tipX-6, baseY, tipX+6, baseY, 2, panelBg)
					})

				renderModelPickerMenuBody(c, p, state)
			})
		})
	}

	renderSelectorRow()
}

// modelPickerPanelBg 是模型面板的底色；Swarm 开关的镂空环内侧也用它盖住彩虹底。
// 这是固定深色面、不随主题翻转：effort_slider.go 在该面板上画深色 token，面板必须一起保持深色。
var (
	modelPickerPanelBg = ColorSurfaceMainDark.Alpha(0.95)
	// modelPickerCardBg 是列表页分组卡片的底色，比面板底色更亮一层以区分群组。
	modelPickerCardBg = ColorSurfacePopoverDark
	// modelPickerDivider 是分组卡片内标题与模型行之间的细分隔线颜色。
	modelPickerDivider       = ColorBorderDark
	modelPickerTextPrimary   = ColorTextPrimaryDark
	modelPickerTextSecondary = ColorTextSecondaryDark
)

// modelPickerPanelWidth 是模型选择面板的宽度。
const modelPickerPanelWidth = 368

// modelPickerPanelRadius 是模型选择面板的圆角：面板宽 368、内容成组，取比浮层常规圆角更大的一档。
const modelPickerPanelRadius = float32(22)

// modelPickerSwarmReopenWindow 是「点执行段后等编排开关生效」的时间窗：超过这段时间还没开成，
// 不再把弹层拉回来，避免用户已经去做别的事时面板突然弹出。
const modelPickerSwarmReopenWindow = 5 * time.Second

// modelPickerRowHeight 是模型列表单行（只有模型名）的行高。
const modelPickerRowHeight = float32(30)

// modelPickerRowHeightTwoLine 是模型列表两行内容（名称 + 说明）的行高。
const modelPickerRowHeightTwoLine = float32(46)

// modelPickerBeeLineHeight 是模型胶囊里子代理行的行高：11pt 字号的子代理名留出余量，
// 与上行 IconSM、行距 Space0 相加正好等于发送按钮的 ControlMD。
const modelPickerBeeLineHeight = Space4

// renderModelPickerMenuBody 渲染模型选择面板的主体内容（宽 368）：
// 卡片页由双槽配对卡直接承载主/子模型切换与编排开关；二级模型列表页上方保留主导/执行分段栏。
func renderModelPickerMenuBody(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	menu := ui.Column(c.Key(p.KeyPrefix + "model-menu-list"))
	if p.NativeMenu != nil {
		menu.MaxWidth(modelPickerPanelWidth).FillWidth()
	} else {
		menu.Width(modelPickerPanelWidth)
	}
	menu.Children(func() {
		side := ModelPickerSideLead
		if p.Mode == ModelPickerSwarm {
			// Swarm 开关是异步生效的：点执行段时 SwarmOn 可能还没变为 true，保留 Tab 等开关生效后再切过去。
			if p.SwarmOn && state.Tab == "bees" {
				side = ModelPickerSideBees
			}
		}

		depth := modelPickerPageDepth(p, state, side)
		if p.Mode == ModelPickerSwarm && depth > 0 {
			ui.Row(c.Key(p.KeyPrefix+"model-tabs-wrap")).
				Padding(Space3, Space4, Space0, Space4).
				AlignSelf(ui.Stretch).
				Children(func() {
					renderModelPickerSideTabs(c, p, state)
				})
		}
		if depth > 0 {
			// 换页过渡由列表页自己的容器负责，这里只给内边距，避免同一页动画挂在两层上。
			wrap := ui.Box(c.Key(p.KeyPrefix + "model-list-wrap")).Padding(Space2)
			wrap.Children(func() { renderModelPickerListPage(c, p, state, side) })
			return
		}
		wrap := ui.Column(c.Key(p.KeyPrefix + "model-slider-wrap"))
		modelPickerPageTransition(c, wrap, state, depth)
		wrap.Children(func() { renderModelPickerCard(c, p, state, side) })
	})
}

// modelPickerSideEffort 返回某一侧分段标签用的档位：当前选择的档位，为空时回退模型默认档位
// （与卡片大标题一致，取自模型档位列表中实际生效的一档）；模型没有档位列表时取选择或默认值。
func modelPickerSideEffort(s modelPickerSide) string {
	if len(s.Model.Efforts) > 0 {
		return s.Model.Efforts[effortIndex(s.Model.Efforts, s.Selection.Effort, s.Model.DefaultEffort)]
	}
	if e := strings.TrimSpace(s.Selection.Effort); e != "" {
		return e
	}
	return s.Model.DefaultEffort
}

// renderModelPickerSideTabs 渲染顶部主导/执行分段控件：占满宽度的
// 胶囊轨道（白 8% 底、高 28、内边距 2、段间距 2），两段等宽；选中段是 accent 实心胶囊配白字，
// 未选中段为主文字 85%；字号 13、medium。未开启 Swarm 时执行段未选中即半透明，点击会先开启 Swarm。
func renderModelPickerSideTabs(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	lead := modelPickerSideInfo(p, ModelPickerSideLead)
	bees := modelPickerSideInfo(p, ModelPickerSideBees)
	leadLabel := swarmSideTabLabel("lead", modelPickerSideEffort(lead), false)
	beesLabel := swarmSideTabLabel("bees", modelPickerSideEffort(bees), !p.SwarmOn)
	tab := state.Tab
	if !p.SwarmOn || tab != "bees" {
		tab = "lead"
	}

	// 选中指示块画在轨道自己的背景之上、两段按钮之下：位置按 tabSel 在轨道内插值，
	// 因此切换分段时是整块胶囊滑动，而不是两段各自淡入淡出。
	tabSel := float32(0)
	if tab == "bees" {
		tabSel = 1
	}
	track := ui.Row(c.Key(p.KeyPrefix + "model-tabs")).
		// 轨道内边距与段间距都是 2：两段高 24 加上下各 2 刚好铺满 28 的轨道，不能换成 Space1。
		Gap(Space0).
		Padding(Space0).
		Height(ControlMD).
		Radius(RadiusXL).
		Background(ColorHoverDark).
		Grow(1)
	tabSel = modelPickerTabSlide(c, track, tabSel)
	track.Draw(func(painter *ui.Painter, r ui.Rect) {
		// 轨道内宽扣掉左右各 2 的内边距与 2 的段间距后，两段等宽；
		// 指示块沿这个宽度从 0（主导段）滑到 1（执行段）。
		cell := (r.W - 6) / 2
		painter.Fill(ui.Rect{
			X: r.X + 2 + tabSel*(cell+2),
			Y: r.Y + 2,
			W: cell,
			H: r.H - 4,
		}, ThemeAccent(c), RadiusLG)
	})
	track.Children(func() {
		for _, t := range []struct{ id, label string }{{"lead", leadLabel}, {"bees", beesLabel}} {
			item := t
			isSel := (tab == item.id)
			// 选中胶囊撑满轨道内高（28 减上下各 2 的内边距），不随文字高度收缩。
			tabBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"model-tab-"+item.id)).
				Label(item.label).
				Height(ControlSM).
				Padding(0, Space2).
				Radius(RadiusLG).
				Grow(1).
				Basis(0).
				Center().
				OnClick(func() {
					// 列表页展开时切换分段要留在列表页：把展开状态一并带到目标分段，
					// 否则第一次点击只是把列表关掉，要再点一次才回到列表。
					if state.listOpen(p.ScopeKey, modelPickerSideFromTab(state.Tab)) {
						state.setListOpen(p.ScopeKey, modelPickerSideFromTab(state.Tab), false)
						state.setListOpen(p.ScopeKey, modelPickerSideFromTab(item.id), true)
						state.openCard = ""
						state.openCardSet = false
					}
					state.Tab = item.id
					state.TierEditing = ""
					if item.id == "bees" && !p.SwarmOn && p.OnToggleSwarm != nil {
						// 开启编排是异步的，宿主重建会让浮层关闭；记下意图，开关生效时把弹层拉回来。
						state.reopenAfterSwarm = true
						state.reopenDeadline = time.Now().Add(modelPickerSwarmReopenWindow)
						p.OnToggleSwarm(true)
					}
					modelPickerInvalidate(p)
				})
			// 选中态的实心胶囊由上面的滑块层绘制，按钮自身始终透明。
			scale := motionPressScale(c, tabBtn, "button-press-scale", true && tabBtn.Pressed())
			tabBtn.Scale(scale, scale)
			tabBtn.Background(ui.Transparent)
			if item.id == "bees" && !p.SwarmOn {
				tabBtn.Tooltip("点击开启编排")
			}
			// 未开启 Swarm 时执行段整体半透明。
			opacity := 1.0
			if item.id == "bees" && !p.SwarmOn && !isSel {
				opacity = 0.5
			}
			tabBtn.Opacity(float32(opacity))
			tabBtn.Children(func() {
				color := modelPickerTextPrimary.Alpha(0.85)
				if isSel {
					color = ThemeOnAccent(c)
				}
				ui.Text(c, item.label).
					FontSize(uiFont(TypeBody)).
					FontWeight(500).
					SingleLine().
					Ellipsis("…").
					TextColor(color)
			})
		}
	})
}

// renderModelPickerCard 渲染某一侧的推理强度卡片（含快速模式、重置与进入模型列表的入口）。
func renderModelPickerCard(c *ui.Context, p ModelPickerProps, state *ModelPickerState, side ModelPickerSide) {
	lead := modelPickerSideInfo(p, ModelPickerSideLead)
	bees := modelPickerSideInfo(p, ModelPickerSideBees)
	cur := lead
	if side == ModelPickerSideBees {
		cur = bees
	}
	paired := p.Mode == ModelPickerSwarm && p.SwarmOn && !bees.FollowsLead

	modelTitle := modelPickerModelTitle(p.Providers, cur)
	var subtitle string
	if side == ModelPickerSideBees && bees.FollowsLead {
		subtitle = "跟随主"
	}
	var subtitlePair string
	if side == ModelPickerSideLead && paired {
		subtitlePair = modelPickerModelTitle(p.Providers, bees)
	}
	var subtitleAccessory func()
	if side == ModelPickerSideBees && p.Mode == ModelPickerSwarm {
		subtitleAccessory = func() {
			renderModelPickerMaxBeesCapsule(c, p, state)
		}
	}
	// 调整强度后会短暂进入保存中的禁用态：原因只放进各控件的 Tooltip，不改说明行，
	// 否则说明行在保存期间变长换行、保存完又缩回，弹层高度会连跳两次。
	disabled := p.Disabled || p.LeadDisabled
	if side == ModelPickerSideBees {
		disabled = p.Disabled || p.BeesDisabled
	}

	// Inline 模式（设置页自有弹层内）不提供进入模型列表的入口，只保留推理强度与快速模式。
	var onOpenModelList func()
	if !p.Inline {
		onOpenModelList = func() {
			state.setListOpen(p.ScopeKey, side, true)
			if p.OnOpenList != nil {
				p.OnOpenList()
			}
			modelPickerInvalidate(p)
		}
	}

	props := EffortSliderCardProps{
		KeyPrefix:         p.KeyPrefix,
		ProviderID:        cur.Selection.Provider,
		BrandID:           modelBrandKey(cur.Selection.Provider, cur.Selection.Model, modelPickerDisplayName(p.Providers, cur.Selection.Provider, cur.Selection.Model)),
		PairProviderID:    bees.Selection.Provider,
		PairBrandID:       modelBrandKey(bees.Selection.Provider, bees.Selection.Model, modelPickerDisplayName(p.Providers, bees.Selection.Provider, bees.Selection.Model)),
		Paired:            paired,
		IsBees:            side == ModelPickerSideBees,
		SwarmOn:           p.SwarmOn,
		ModelTitle:        modelTitle,
		Subtitle:          subtitle,
		SubtitlePair:      subtitlePair,
		SubtitleAccessory: subtitleAccessory,
		LockedReason:      effortCardLockedReason(p, side),
		Efforts:           cur.Model.Efforts,
		Current:           cur.Selection.Effort,
		DefaultEffort:     cur.Model.DefaultEffort,
		SupportsFast:      cur.Model.SupportsFast,
		FastMode:          cur.Selection.Fast,
		Disabled:          disabled,
		OnEffort: func(effort string) {
			if p.OnEffort != nil {
				p.OnEffort(side, effort)
			}
			// 独立弹层闭包捕获宿主帧 props，切换档位后需主动请求重绘。
			modelPickerInvalidate(p)
		},
		OnToggleFast: func() {
			if p.OnToggleFast != nil {
				p.OnToggleFast(side)
			}
			modelPickerInvalidate(p)
		},
		OnReset: func() {
			if p.OnReset != nil {
				p.OnReset(side)
			}
			modelPickerInvalidate(p)
		},
		OnOpenModelList: onOpenModelList,
		Personas:        p.Personas,
		Persona:         p.Persona,
		OnPersona:       p.OnPersona,
		PersonaOpen:     &state.PersonaOpen,
		Invalidate:      p.Invalidate,
	}

	// 只在能带团队的选择器里给卡片顶栏放编排开关，并同步异步开启的拉回窗口。
	if p.Mode == ModelPickerSwarm && p.OnToggleSwarm != nil {
		props.OnToggleSwarm = func() {
			if p.SwarmOn {
				state.Tab = "lead"
			} else {
				state.reopenAfterSwarm = true
				state.reopenDeadline = time.Now().Add(modelPickerSwarmReopenWindow)
			}
			p.OnToggleSwarm(!p.SwarmOn)
			modelPickerInvalidate(p)
		}
	}

	// 双槽卡只用于 Composer 主面板（ModelPickerSwarm 且非 Inline）；Simple 模式只有一个槽，走单卡。
	if !p.Inline && p.Mode == ModelPickerSwarm {
		openListFor := func(targetSide ModelPickerSide, targetTab string) func() {
			return func() {
				state.Tab = targetTab
				state.setListOpen(p.ScopeKey, targetSide, true)
				if p.OnOpenList != nil {
					p.OnOpenList()
				}
				modelPickerInvalidate(p)
			}
		}
		leadRole := ""
		if p.Mode == ModelPickerSwarm {
			leadRole = "主"
		}
		leadEffort := ""
		if len(lead.Model.Efforts) > 0 {
			leadEffort = effortThumbLabel(modelPickerSideEffort(lead))
		}
		leadBrand := modelBrandKey(lead.Selection.Provider, lead.Selection.Model, modelPickerDisplayName(p.Providers, lead.Selection.Provider, lead.Selection.Model))
		roster := []EffortRosterSlot{{
			Key:         "lead",
			Role:        leadRole,
			BrandID:     leadBrand,
			ProviderID:  lead.Selection.Provider,
			ModelTitle:  modelPickerModelTitle(p.Providers, lead),
			EffortLabel: leadEffort,
			Active:      side == ModelPickerSideLead,
			Disabled:    p.Disabled || p.LeadDisabled,
			LockedTip:   effortCardLockedReason(p, ModelPickerSideLead),
			OnSelect: func() {
				state.Tab = "lead"
				state.TierEditing = ""
				modelPickerInvalidate(p)
			},
			OnOpenList: openListFor(ModelPickerSideLead, "lead"),
		}}
		if p.Mode == ModelPickerSwarm && p.SwarmOn {
			beesEffort := ""
			if len(bees.Model.Efforts) > 0 {
				beesEffort = effortThumbLabel(modelPickerSideEffort(bees))
			}
			beesPlaceholder := ""
			if bees.FollowsLead {
				beesPlaceholder = "跟随主"
			}
			beesBrand := modelBrandKey(bees.Selection.Provider, bees.Selection.Model, modelPickerDisplayName(p.Providers, bees.Selection.Provider, bees.Selection.Model))
			roster = append(roster, EffortRosterSlot{
				Key:         "bees",
				Role:        "子",
				BrandID:     beesBrand,
				ProviderID:  bees.Selection.Provider,
				ModelTitle:  modelPickerModelTitle(p.Providers, bees),
				Placeholder: beesPlaceholder,
				EffortLabel: beesEffort,
				Active:      side == ModelPickerSideBees,
				Disabled:    p.Disabled || p.BeesDisabled,
				LockedTip:   effortCardLockedReason(p, ModelPickerSideBees),
				Accessory: func() {
					renderModelPickerMaxBeesCapsule(c, p, state)
				},
				OnSelect: func() {
					state.Tab = "bees"
					state.TierEditing = ""
					modelPickerInvalidate(p)
				},
				OnOpenList: openListFor(ModelPickerSideBees, "bees"),
			})
		}
		props.Roster = roster
	}

	// 配对时顶栏右侧渲染收藏按钮：无论当前激活主代理还是子代理，收藏星均稳定可见。
	if paired {
		props.PairFav = swarmPairFavProps(p, lead, bees)
	}

	renderEffortSliderCard(c, props)
}

func effortCardLockedReason(p ModelPickerProps, side ModelPickerSide) string {
	sideDisabled := p.LeadDisabled
	if side == ModelPickerSideBees {
		sideDisabled = p.BeesDisabled
	}
	return modelSideDisabledReason(p.Disabled, sideDisabled)
}

// renderModelPickerMaxBeesCapsule 渲染执行侧“最多 N 个 ⌄”并发 Bee 上限选择胶囊。
func renderModelPickerMaxBeesCapsule(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	curVal := p.MaxBees
	if curVal < 0 || curVal > 8 {
		curVal = 4
	}

	label := fmt.Sprintf("%d 个", curVal)
	if curVal == 0 {
		// 0 实际按每个主代理最多 8 个执行（见 swarm.Manager 的 defaultMaxBees），不再是“不限”。
		label = "自动"
	}

	btn := ui.ButtonBase(c.Key(p.KeyPrefix+"bees-max-capsule")).
		Label(label).
		Tooltip("并发上限").
		// 纵向 2 与卡片行内高度对齐，左右收紧为 Space2 使并发胶囊更紧凑。
		Padding(Space0, Space2).
		Radius(RadiusLG).
		Center()

	if btn.Hovered() || state.MaxBeesOpen {
		btn.Background(ColorSelectedDark)
	} else {
		btn.Background(ColorHoverDark)
	}

	btn.OnClick(func() {
		state.MaxBeesOpen = !state.MaxBeesOpen
		modelPickerInvalidate(p)
	})

	btn.Children(func() {
		ui.Row(c.Key(p.KeyPrefix + "bees-max-content")).
			AlignItems(ui.Center).
			Gap(Space1).
			Children(func() {
				ui.Text(c, label).
					FontSize(uiFont(TypeCaption)).
					Bold().
					TextColor(ColorTextPrimaryDark)
				ui.Icon(c, IconChevronDown()).
					Size(8, 8).
					TextColor(ColorTextSecondaryDark)
			})
	})

	appFlyoutPopover(c, p.KeyPrefix+"bees-max-popover", btn, &state.MaxBeesOpen, func() {
		state.MaxBeesOpen = false
	}, appFlyoutProps{Width: 230, Background: &ColorSurfaceRaisedDark}, func(fc *ui.Context) {
		c := fc
		optionsCol := ui.Column(c.Key(p.KeyPrefix + "bees-max-options")).
			Padding(Space1).
			FocusGroup(ui.Vertical)
		// 选项面板是固定深色面上的第二层浮层，阴影取 ElevPopover。
		if flyoutHostWindow(c.Services()) == nil {
			optionsCol = applyElevation(c, optionsCol.Radius(RadiusMD).Background(ColorSurfaceRaisedDark).Border(1, ColorBorderDark), ElevPopover)
		}
		optionsCol.Children(func() {
			options := []struct {
				val   int
				label string
			}{
				{0, "自动（8）"},
				{1, "1 个"},
				{2, "2 个"},
				{3, "3 个"},
				{4, "4 个"},
				{6, "6 个"},
				{8, "8 个"},
			}
			for _, opt := range options {
				o := opt
				isSel := (curVal == o.val)
				menuRow(c, menuRowProps{
					Key:         fmt.Sprintf("%smax-bees-opt-%d", p.KeyPrefix, o.val),
					Label:       o.label,
					Title:       o.label,
					Checked:     isSel,
					Highlighted: isSel,
					OnClick: func() {
						state.MaxBeesOpen = false
						if p.OnSetMaxBees != nil {
							p.OnSetMaxBees(o.val)
						}
						modelPickerInvalidate(p)
					},
				})
			}
		})
	})
}

// effectiveTierModel 按 session override > pair override > global tier 的优先级解析档位最终模型配置。
// 当所有层级均未指定 Model 时返回空字符串，调用方据此回退至跟随会话 Bee 模型。
func effectiveTierModel(tier config.SwarmTier, pairOverrides, sessionOverrides map[string]config.SwarmTierOverride) (provider, model, effort string, fast bool) {
	provider = tier.Provider
	model = tier.Model
	effort = tier.Effort
	fast = tier.FastMode

	if pairOverrides != nil {
		if pov, ok := pairOverrides[tier.ID]; ok {
			if pov.Provider != "" {
				provider = pov.Provider
			}
			if pov.Model != "" {
				model = pov.Model
			}
			if pov.Effort != "" {
				effort = pov.Effort
			}
			if pov.FastMode != nil {
				fast = *pov.FastMode
			}
		}
	}

	if sessionOverrides != nil {
		if sov, ok := sessionOverrides[tier.ID]; ok {
			if sov.Provider != "" {
				provider = sov.Provider
			}
			if sov.Model != "" {
				model = sov.Model
			}
			if sov.Effort != "" {
				effort = sov.Effort
			}
			if sov.FastMode != nil {
				fast = *sov.FastMode
			}
		}
	}

	return provider, model, effort, fast
}

func renderTierEditingBanner(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	tierID := state.TierEditing
	tierName := tierID
	for _, t := range p.Tiers {
		if t.ID == tierID {
			if t.Name != "" {
				tierName = t.Name
			}
			break
		}
	}
	ui.Row(c.Key(p.KeyPrefix+"tier-editing-banner")).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(Space2).
		Padding(Space2, Space3).
		Radius(RadiusSM).
		Background(ThemeAccent(c).Alpha(0.12)).
		FillWidth().
		Children(func() {
			ui.Text(c, fmt.Sprintf("正在为「%s」档选择模型", tierName)).
				FontSize(uiFont(TypeFootnote)).
				TextColor(modelPickerTextPrimary).
				SingleLine().
				MinWidth(0).
				Shrink(1).
				Ellipsis("…")
			cancelBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"tier-editing-cancel")).
				Label("取消").
				Tooltip("取消选择").
				Padding(Space0, Space2).
				Radius(RadiusXS).
				Center().
				Shrink(0).
				Background(ui.Transparent)
			if cancelBtn.Hovered() {
				cancelBtn.Background(ColorHoverDark)
			}
			cancelBtn.OnClick(func() {
				state.TierEditing = ""
				modelPickerInvalidate(p)
			})
			cancelBtn.Children(func() {
				ui.Text(c, "取消").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeAccent(c))
			})
		})
}

func renderModelPickerTiersSection(c *ui.Context, p ModelPickerProps, state *ModelPickerState) {
	open := state != nil && (state.tiersOpen || state.TierEditing != "")
	renderProviderGroupHeaderToggle(c, p.KeyPrefix+"tier-group-header", "档位", !open, func() {
		if state != nil {
			state.tiersOpen = !open
		}
		modelPickerInvalidate(p)
	})
	if !open {
		return
	}

	ui.Column(c.Key(p.KeyPrefix + "tier-rows")).
		Gap(Space0).
		FillWidth().
		Transition(motionCollapse(c)).
		Children(func() {
			for _, t := range p.Tiers {
				tier := t
				prov, mdl, eff, fast := effectiveTierModel(tier, p.PairTierOverrides, p.TierOverrides)
				effectiveText := swarmModelCapsuleText(p.Providers, prov, mdl, eff, fast)

				_, hasSessionOverride := p.TierOverrides[tier.ID]
				isEditing := (state != nil && state.TierEditing == tier.ID)

				ui.Row(c.Key(p.KeyPrefix + "tier-row-container-" + tier.ID)).
					AlignItems(ui.Center).
					FillWidth().
					Children(func() {
						rowBtn := ui.ButtonBase(c.Key(p.KeyPrefix+"tier-row-"+tier.ID)).
							Label(tier.Name).
							Tooltip(tier.Name).
							Height(ControlLG).
							Padding(0, Space3).
							Radius(RadiusMD).
							Grow(1).
							MinWidth(0)
						if rowBtn.Hovered() || isEditing {
							rowBtn.Background(ColorHoverDark)
						} else {
							rowBtn.Background(ui.Transparent)
						}
						rowBtn.OnClick(func() {
							if state != nil {
								state.TierEditing = tier.ID
								modelPickerInvalidate(p)
							}
						})
						rowBtn.Children(func() {
							ui.Row(c.Key(p.KeyPrefix + "tier-row-content-" + tier.ID)).
								AlignItems(ui.Center).
								Justify(ui.SpaceBetween).
								Gap(Space2).
								Grow(1).
								MinWidth(0).
								Children(func() {
									renderSwarmTierChip(c, p.KeyPrefix+"tier-chip-"+tier.ID, tier.Name, tier.ID, tier.ReadOnly)
									ui.Row(c.Key(p.KeyPrefix + "tier-row-right-" + tier.ID)).
										AlignItems(ui.Center).
										Gap(Space2).
										MinWidth(0).
										Shrink(1).
										Children(func() {
											ui.Text(c, effectiveText).
												FontSize(uiFont(TypeFootnote)).
												TextColor(modelPickerTextSecondary).
												SingleLine().
												MinWidth(0).
												Shrink(1).
												Ellipsis("…")
											if hasSessionOverride {
												ui.Box(c.Key(p.KeyPrefix+"tier-dot-"+tier.ID)).
													Size(6, 6).
													Radius(RadiusPill).
													Background(ThemeAccent(c)).
													Tooltip("会话覆盖").
													Shrink(0)
											}
										})
								})
						})

						if hasSessionOverride {
							btnReset := ui.ButtonBase(c.Key(p.KeyPrefix+"tier-reset-"+tier.ID)).
								Label("恢复").
								Tooltip("恢复").
								Size(26, 26).
								Radius(RadiusPill).
								Center().
								Shrink(0)
							if btnReset.Hovered() {
								btnReset.Background(ColorHoverDark)
							} else {
								btnReset.Background(ui.Transparent)
							}
							btnReset.OnClick(func() {
								if state != nil && state.TierEditing == tier.ID {
									state.TierEditing = ""
								}
								if p.OnSetTierOverride != nil {
									p.OnSetTierOverride(tier.ID, nil)
								}
								modelPickerInvalidate(p)
							})
							btnReset.Children(func() {
								textColor := ColorTextSecondaryDark
								if btnReset.Hovered() {
									textColor = ColorTextPrimaryDark
								}
								ui.Icon(c, iconRotateCcw).
									Size(13, 13).
									TextColor(textColor)
							})
						}
					})
			}

			if len(p.TierOverrides) > 0 {
				ui.Row(c.Key(p.KeyPrefix+"tier-footer-row")).
					AlignItems(ui.Center).
					Justify(ui.End).
					Padding(Space1, Space3).
					FillWidth().
					Children(func() {
						btnResetAll := ui.ButtonBase(c.Key(p.KeyPrefix+"tier-reset-all")).
							Label("全部恢复").
							Tooltip("全部恢复").
							Padding(Space0, Space2).
							Radius(RadiusXS).
							Center().
							Background(ui.Transparent)
						if btnResetAll.Hovered() {
							btnResetAll.Background(ColorHoverDark)
						}
						btnResetAll.OnClick(func() {
							if state != nil {
								state.TierEditing = ""
							}
							if p.OnResetTierOverrides != nil {
								p.OnResetTierOverrides()
							}
							modelPickerInvalidate(p)
						})
						btnResetAll.Children(func() {
							ui.Text(c, "全部恢复").
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeAccent(c))
						})
					})
			}
		})
}

func defaultModelRowDisplayName(prov ProviderInfo, m ModelInfo) string {
	name := m.Name
	if name == "" {
		name = m.ID
	}
	if name == "" {
		return "默认模型"
	}
	if eff := effectiveModelName([]ProviderInfo{prov}, prov.ID, m.ID); eff != "" {
		return eff
	}
	return name
}

// renderProviderGroupHeader 渲染模型分组标题（步骤 6：高 22，Padding 上 6、右 8、下 3、左 8；11px 粗体，ThemeSecondaryText；文字是 provider 显示名）。
func renderProviderGroupHeader(c *ui.Context, key, title string) {
	ui.Row(c.Key(key)).
		Height(ControlXS).
		Padding(Space2, Space3, Space1, Space3).
		AlignItems(ui.Center).
		FillWidth().
		Children(func() {
			ui.Text(c, title).
				FontSize(uiFont(TypeCaption)).
				Bold().
				TextColor(modelPickerTextSecondary)
		})
}

// renderProviderGroupHeaderToggle 渲染可点击折叠的 Agent 分组标题：外观与 renderProviderGroupHeader 一致
// （高 22，Padding 上 6、右 8、下 3、左 8；11px 粗体次要色），整行可点击，
// 右侧 10 号箭头指示展开（向下）或折叠（向右），悬停时用与模型行一致的淡色底。
func renderProviderGroupHeaderToggle(c *ui.Context, key, title string, collapsed bool, onToggle func()) {
	label := "折叠 " + title
	if collapsed {
		label = "展开 " + title
	}
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(label).
		Height(ControlXS).
		Padding(Space2, Space3, Space1, Space3).
		Radius(RadiusXS)
	if btn.Hovered() {
		btn.Background(ColorHoverDark)
	} else {
		btn.Background(ui.Transparent)
	}
	btn.OnClick(func() {
		if onToggle != nil {
			onToggle()
		}
	})
	btn.Children(func() {
		ui.Row(c.Key(key + "-content")).
			AlignItems(ui.Center).
			Gap(Space1).
			FillWidth().
			Children(func() {
				ui.Text(c, title).
					FontSize(uiFont(TypeCaption)).
					Bold().
					TextColor(modelPickerTextSecondary).
					Grow(1)
				arrow := IconChevronDown()
				if collapsed {
					arrow = IconChevronRight()
				}
				ui.Icon(c, arrow).
					Size(10, 10).
					TextColor(modelPickerTextSecondary)
			})
	})
}

// renderModelListPairRow 渲染模型列表里的一个 Swarm 配对：行高 46，
// 左侧 34 宽的盒子里并排两个 16 号完整品牌图标（主导 + 执行），中间是 14 号配对标题与次要色说明行，
// 右侧对勾标出会话正在使用的配对；悬停白 10% 圆角底，点击套用该配对。
func renderModelListPairRow(c *ui.Context, p ModelPickerProps, key string, pair SwarmPair, selected bool, onEnter func()) {
	title := swarmPairTitle(pair, p.Providers)
	detail := swarmPairListDetail(pair, p.Providers)

	row := ui.ButtonBase(c.Key(key)).
		Label(title).
		Tooltip(swarmPairTooltip(pair, "", p.Providers)).
		Checked(selected).
		Height(modelPickerRowHeightTwoLine).
		Padding(0, Space3).
		Radius(RadiusMD).
		Grow(1)
	if row.Hovered() {
		row.Background(ColorHoverSubtleDark)
	} else {
		row.Background(ui.Transparent)
	}
	row.OnClick(onEnter)

	row.Children(func() {
		ui.Row(c.Key(key + "-content")).
			AlignItems(ui.Center).
			Gap(Space3).
			Grow(1).
			Children(func() {
				ui.Box(c.Key(key + "-icon-box")).
					Width(34).
					AlignItems(ui.Start).
					Children(func() {
						renderModelPairIcons(c, key+"-icon", p.Providers, pair.LeadProvider, pair.LeadModel, swarmWorkerProvider(pair), pair.WorkerModel, 16)
					})
				ui.Column(c.Key(key + "-col")).
					Gap(Space0).
					Grow(1).
					MinWidth(0).
					Children(func() {
						ui.Text(c, title).
							FontSize(uiFont(TypeProse)).
							TextColor(modelPickerTextPrimary).
							SingleLine().Ellipsis("…")
						if detail != "" {
							ui.Text(c, detail).
								FontSize(uiFont(TypeFootnote)).
								TextColor(modelPickerTextSecondary).
								SingleLine().Ellipsis("…")
						}
					})
				if selected {
					ui.Icon(c, IconCheck()).
						Size(13, 13).
						TextColor(modelPickerTextPrimary)
				} else {
					ui.Box(c.Key(key+"-check-placeholder")).
						Size(13, 13)
				}
			})
	})
}

// swarmPairListDetail 返回配对行标题下的说明：
// 有自定义名称时先列出“主导模型 + 执行模型”，之后是 provider（跨 provider 时为“X 主导，Y 执行”），
// 再是两侧设置过的强度与快速模式；都没设置的部分不出现。
func swarmPairListDetail(pair SwarmPair, providers []ProviderInfo) string {
	var parts []string
	if pair.Name != "" && !swarmPairIsAutoName(pair, providers) {
		unnamed := pair
		unnamed.Name = ""
		if modelPart := swarmPairTitle(unnamed, providers); modelPart != pair.Name {
			parts = append(parts, modelPart)
		}
	}
	leadProv := modelProviderDisplayName(providers, pair.LeadProvider)
	if worker := swarmWorkerProvider(pair); worker != pair.LeadProvider {
		parts = append(parts, leadProv+" 主导，"+modelProviderDisplayName(providers, worker)+" 执行")
	} else if leadProv != "" {
		parts = append(parts, leadProv)
	}
	if summary := swarmPairSummary(pair); summary != "默认强度" {
		parts = append(parts, summary)
	}
	return strings.Join(parts, " · ")
}

// renderProviderCatalogNotice 渲染“供应商可用但模型目录为空”的说明块：
// 第一行是“<Agent 名> · <原因>”，下面一行是“重新探测”按钮（仅在调用方给了 OnRefreshCatalog 时）。
// 说明块直接放在选择器已有的列表内容区里，不另开弹窗或浮层。
func renderProviderCatalogNotice(c *ui.Context, p ModelPickerProps, prefix string, prov ProviderInfo) {
	key := p.KeyPrefix + prefix + "model-provider-notice-" + prov.ID
	title := prov.Name
	if title == "" {
		title = providerDisplayName(prov.ID)
	}
	ui.Column(c.Key(key)).
		FillWidth().
		Gap(Space1).
		Padding(Space3, Space3, Space3, Space3).
		Children(func() {
			ui.Text(c, title+" · "+prov.UnavailableReason).
				FontSize(uiFont(TypeFootnote)).
				TextColor(modelPickerTextSecondary).
				FillWidth()
			if p.OnRefreshCatalog == nil {
				return
			}
			btn := ui.ButtonBase(c.Key(key+"-rescan-btn")).
				Label("重新探测").
				Height(ControlMD).
				Padding(0, Space3).
				Radius(RadiusMD)
			if btn.Hovered() {
				btn.Background(ColorHoverDark)
			} else {
				btn.Background(ui.Transparent)
			}
			btn.Disabled(p.CatalogRefreshing)
			scale := motionPressScale(c, btn, "button-press-scale", !p.CatalogRefreshing && btn.Pressed())
			btn.Scale(scale, scale)
			btn.OnClick(p.OnRefreshCatalog)
			btn.Children(func() {
				if p.CatalogRefreshing {
					LoadingIndicator(c, nil, "", IconSM)
					return
				}
				ui.Text(c, "重新探测").
					FontSize(uiFont(TypeBody)).
					TextColor(modelPickerTextPrimary).
					SingleLine()
			})
		})
}
