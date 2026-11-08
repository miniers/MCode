package config

import "strings"

// MagpieMode 是 agent 使用 magpie 模型的方式。
type MagpieMode string

const (
	// MagpieModeInject 只在 mcode 启动子进程时注入，不改 agent 自己的配置。
	MagpieModeInject MagpieMode = "inject"
	// MagpieModeGlobal 写 agent 的全局配置文件。
	MagpieModeGlobal MagpieMode = "global"
	// MagpieModeOff 不接管 agent 的配置或路由。
	MagpieModeOff MagpieMode = "off"
)

// MagpieSelection 是一个端点上勾选的模型。
type MagpieSelection struct {
	EndpointID string   `json:"endpointID"`
	Models     []string `json:"models,omitempty"`
}

// MagpieAgentConfig 是单个 agent 的 magpie 设置；零值为默认模式且无勾选。
// DefaultEndpointID / DefaultModel 是用户手动指定的主模型；两者要么同时有值、
// 要么同时为空，生效条件由使用方判断（端点有 key 且模型仍在勾选里）。
type MagpieAgentConfig struct {
	Mode              MagpieMode        `json:"mode,omitempty"`
	Selections        []MagpieSelection `json:"selections,omitempty"`
	DefaultEndpointID string            `json:"defaultEndpointID,omitempty"`
	DefaultModel      string            `json:"defaultModel,omitempty"`
}

// MagpieAgentSupportsGlobal 报告该 agent 是否支持 global 模式（写全局配置）。
// claude、codex 与 pi 各有一份可接管的全局配置。
func MagpieAgentSupportsGlobal(agent string) bool {
	switch agent {
	case ProviderClaude, ProviderCodex, ProviderPi:
		return true
	}
	return false
}

// MagpieAgentSupportsInject 报告该 agent 是否有运行时注入通道（mcode 启动
// 子进程时注入模型）；claude、pi 支持注入，codex 的注入正在实现中。
func MagpieAgentSupportsInject(agent string) bool {
	switch agent {
	case ProviderClaude, ProviderCodex, ProviderPi:
		return true
	}
	return false
}

// MagpieAgentSetting 返回某 agent 的 magpie 设置；空模式按 agent 的默认模式处理。
// 返回值是副本，调用方修改它不会影响配置。
// 内置引擎（mcode）没有模式概念：键存在时只用自己的勾选与默认模型，键不存在时沿用
// pi 的勾选，让老用户第一次就看到与 pi 相同的列表。模式固定报告为 inject，避免
// 调用方把它当成关闭而忽略勾选。
func MagpieAgentSetting(cfg Config, agent string) MagpieAgentConfig {
	if agent == ProviderMcode {
		setting, ok := cfg.Providers.MagpieAgents[ProviderMcode]
		if !ok {
			setting = cfg.Providers.MagpieAgents[ProviderPi]
		}
		setting.Mode = MagpieModeInject
		setting.Selections = copyMagpieSelections(setting.Selections)
		return setting
	}
	setting := cfg.Providers.MagpieAgents[agent]
	setting.Mode = normalizeMagpieMode(agent, setting.Mode, len(setting.Selections) > 0)
	setting.Selections = copyMagpieSelections(setting.Selections)
	return setting
}

// SetMagpieAgentModels 设置某 agent 在某端点上勾选的模型；
// models 为空（去空后没有模型）时删除这条勾选。agent 为 pi 时同步写对应端点的 PiModels。
func SetMagpieAgentModels(cfg *Config, agent, endpointID string, models []string) {
	if cfg == nil {
		return
	}
	agent = strings.TrimSpace(agent)
	endpointID = strings.TrimSpace(endpointID)
	if agent == "" || endpointID == "" {
		return
	}
	if cfg.Providers.MagpieAgents == nil {
		cfg.Providers.MagpieAgents = make(map[string]MagpieAgentConfig, 1)
	}
	setting := cfg.Providers.MagpieAgents[agent]
	if setting.Mode == "" {
		setting.Mode = MagpieModeOff
	}
	setting.Mode = normalizeMagpieMode(agent, setting.Mode, len(setting.Selections) > 0)
	setting.Selections = setMagpieSelection(normalizeMagpieSelections(setting.Selections), endpointID, normalizePiModels(models))
	cfg.Providers.MagpieAgents[agent] = setting
	if agent == ProviderPi {
		mirrorPiModels(cfg, endpointID, magpieModelsFor(setting.Selections, endpointID))
	}
	cleanupMagpieAgents(cfg)
}

// SetMagpieAgentDefault 设置某 agent 手动指定的主模型（端点上勾选的一个模型）。
// 两个参数去空白后任一为空都表示恢复自动：两个字段一起清空。写入时不校验
// 端点是否存在、模型是否已勾选；生效条件留给使用方判断，配置值保留不删。
func SetMagpieAgentDefault(cfg *Config, agent, endpointID, modelID string) {
	if cfg == nil {
		return
	}
	agent = strings.TrimSpace(agent)
	endpointID = strings.TrimSpace(endpointID)
	modelID = strings.TrimSpace(modelID)
	if agent == "" {
		return
	}
	if cfg.Providers.MagpieAgents == nil {
		cfg.Providers.MagpieAgents = make(map[string]MagpieAgentConfig, 1)
	}
	setting := cfg.Providers.MagpieAgents[agent]
	if setting.Mode == "" {
		setting.Mode = MagpieModeOff
	}
	setting.Mode = normalizeMagpieMode(agent, setting.Mode, len(setting.Selections) > 0)
	if endpointID == "" || modelID == "" {
		setting.DefaultEndpointID = ""
		setting.DefaultModel = ""
	} else {
		setting.DefaultEndpointID = endpointID
		setting.DefaultModel = modelID
	}
	cfg.Providers.MagpieAgents[agent] = setting
	cleanupMagpieAgents(cfg)
}

// SetMagpieAgentMode 设置 agent 使用 magpie 模型的方式；
// 不支持的模式按直连或兼容旧勾选的规则处理。
func SetMagpieAgentMode(cfg *Config, agent string, mode MagpieMode) {
	if cfg == nil {
		return
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return
	}
	if cfg.Providers.MagpieAgents == nil {
		cfg.Providers.MagpieAgents = make(map[string]MagpieAgentConfig, 1)
	}
	setting := cfg.Providers.MagpieAgents[agent]
	setting.Mode = normalizeMagpieMode(agent, mode, len(setting.Selections) > 0)
	cfg.Providers.MagpieAgents[agent] = setting
	cleanupMagpieAgents(cfg)
}

// normalizeMagpieAgents 把旧的 PiModels 并入 MagpieAgents["pi"]，并归一模式、勾选与空白项。
// 它在加载与保存时都会跑，必须幂等：连续调用两次结果不变。
func normalizeMagpieAgents(cfg *Config) {
	if cfg == nil {
		return
	}
	// 复制一份 map，避免改到调用方看到的同一个 map。
	agents := make(map[string]MagpieAgentConfig, len(cfg.Providers.MagpieAgents))
	for agent, setting := range cfg.Providers.MagpieAgents {
		agents[agent] = setting
	}

	// 旧字段 PiModels 并入 pi 的勾选：已有模型在前，新模型按 PiModels 顺序接在后面。
	if len(cfg.Providers.Custom) > 0 {
		pi := agents[ProviderPi]
		for _, ep := range cfg.Providers.Custom {
			if !ep.Magpie || len(ep.PiModels) == 0 {
				continue
			}
			pi.Selections = mergeMagpieModels(pi.Selections, ep.ID, ep.PiModels)
		}
		agents[ProviderPi] = pi
	}

	// 逐 agent 归一模式与勾选；已删除端点的勾选保留，只在取用时跳过。
	normalized := make(map[string]MagpieAgentConfig, len(agents))
	for agent, setting := range agents {
		if strings.TrimSpace(agent) == "" {
			continue
		}
		setting.Selections = normalizeMagpieSelections(setting.Selections)
		setting.Mode = normalizeMagpieMode(agent, setting.Mode, len(setting.Selections) > 0)
		// 默认端点与默认模型必须成对出现：任一为空都清掉两个，保证幂等。
		setting.DefaultEndpointID = strings.TrimSpace(setting.DefaultEndpointID)
		setting.DefaultModel = strings.TrimSpace(setting.DefaultModel)
		if setting.DefaultEndpointID == "" || setting.DefaultModel == "" {
			setting.DefaultEndpointID, setting.DefaultModel = "", ""
		}
		normalized[agent] = setting
	}
	cfg.Providers.MagpieAgents = normalized

	// 反向镜像：pi 的勾选写回对应端点的 PiModels，让旧版本 mcode 仍看到同样的列表。
	if len(cfg.Providers.Custom) > 0 {
		pi := cfg.Providers.MagpieAgents[ProviderPi]
		custom := make([]CustomEndpointConfig, 0, len(cfg.Providers.Custom))
		for _, ep := range cfg.Providers.Custom {
			if ep.Magpie {
				ep.PiModels = magpieModelsFor(pi.Selections, ep.ID)
			}
			custom = append(custom, ep)
		}
		cfg.Providers.Custom = custom
	}

	cleanupMagpieAgents(cfg)
}

// normalizeMagpieMode 保留支持的模式，并兼容 claude 和 pi 的旧勾选。
func normalizeMagpieMode(agent string, mode MagpieMode, hasSelections bool) MagpieMode {
	if !MagpieAgentSupportsInject(agent) && !MagpieAgentSupportsGlobal(agent) {
		return MagpieModeOff
	}
	if mode == MagpieModeOff {
		return MagpieModeOff
	}
	if mode == MagpieModeGlobal && MagpieAgentSupportsGlobal(agent) {
		return MagpieModeGlobal
	}
	if mode == MagpieModeInject && MagpieAgentSupportsInject(agent) {
		return MagpieModeInject
	}
	if hasSelections && (agent == ProviderClaude || agent == ProviderPi) {
		return MagpieModeInject
	}
	return MagpieModeOff
}

// magpieModelsFor 返回某端点勾选的模型副本；没有该端点时返回 nil。
func magpieModelsFor(selections []MagpieSelection, endpointID string) []string {
	for _, sel := range selections {
		if sel.EndpointID == endpointID {
			return append([]string(nil), sel.Models...)
		}
	}
	return nil
}

// mergeMagpieModels 把 models 并入 selections 中 EndpointID 等于 endpointID 的那一条；
// 没有该端点时追加一条，已有模型在前。
func mergeMagpieModels(selections []MagpieSelection, endpointID string, models []string) []MagpieSelection {
	if endpointID == "" {
		return selections
	}
	res := make([]MagpieSelection, len(selections), len(selections)+1)
	copy(res, selections)
	for i := range res {
		if res[i].EndpointID != endpointID {
			continue
		}
		merged := make([]string, 0, len(res[i].Models)+len(models))
		merged = append(merged, res[i].Models...)
		merged = append(merged, models...)
		res[i].Models = normalizePiModels(merged)
		return res
	}
	return append(res, MagpieSelection{EndpointID: endpointID, Models: normalizePiModels(models)})
}

// normalizeMagpieSelections 对勾选做 TrimSpace、去空、去重，并合并重复端点。
func normalizeMagpieSelections(selections []MagpieSelection) []MagpieSelection {
	if len(selections) == 0 {
		return nil
	}
	res := make([]MagpieSelection, 0, len(selections))
	index := make(map[string]int, len(selections))
	for _, sel := range selections {
		id := strings.TrimSpace(sel.EndpointID)
		if id == "" {
			continue
		}
		models := normalizePiModels(sel.Models)
		if i, ok := index[id]; ok {
			merged := make([]string, 0, len(res[i].Models)+len(models))
			merged = append(merged, res[i].Models...)
			merged = append(merged, models...)
			res[i].Models = normalizePiModels(merged)
			continue
		}
		index[id] = len(res)
		res = append(res, MagpieSelection{EndpointID: id, Models: models})
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

// setMagpieSelection 设置某端点的勾选：已有条目原位替换，没有则追加；models 为空时删除该条。
func setMagpieSelection(selections []MagpieSelection, endpointID string, models []string) []MagpieSelection {
	res := make([]MagpieSelection, 0, len(selections)+1)
	found := false
	for _, sel := range selections {
		if sel.EndpointID != endpointID {
			res = append(res, sel)
			continue
		}
		if found {
			continue
		}
		found = true
		if len(models) > 0 {
			res = append(res, MagpieSelection{EndpointID: endpointID, Models: append([]string(nil), models...)})
		}
	}
	if !found && len(models) > 0 {
		res = append(res, MagpieSelection{EndpointID: endpointID, Models: append([]string(nil), models...)})
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

// mirrorPiModels 把 pi 在某端点上的勾选写回该 Magpie 端点配置的 PiModels。
func mirrorPiModels(cfg *Config, endpointID string, models []string) {
	for i := range cfg.Providers.Custom {
		if cfg.Providers.Custom[i].ID != endpointID || !cfg.Providers.Custom[i].Magpie {
			continue
		}
		cfg.Providers.Custom[i].PiModels = append([]string(nil), models...)
		return
	}
}

// cleanupMagpieAgents 删除没有实际内容的 agent 项（off 且没有勾选），
// map 为空时设成 nil，避免 JSON 里出现空的 magpieAgents。只带默认模型的项
// 不算空：勾选被清空后还要保留默认值，用户重新勾选时自动恢复。
func cleanupMagpieAgents(cfg *Config) {
	for agent, setting := range cfg.Providers.MagpieAgents {
		// 内置引擎没有模式开关：键本身表示用户已为 mcode 单独勾选过，勾选清空后也要
		// 保留，否则读取时会回退到 pi 的勾选。
		if agent == ProviderMcode {
			continue
		}
		if setting.Mode == MagpieModeOff && len(setting.Selections) == 0 &&
			setting.DefaultEndpointID == "" && setting.DefaultModel == "" {
			delete(cfg.Providers.MagpieAgents, agent)
		}
	}
	if len(cfg.Providers.MagpieAgents) == 0 {
		cfg.Providers.MagpieAgents = nil
	}
}

// copyMagpieSelections 返回勾选列表的深拷贝。
func copyMagpieSelections(selections []MagpieSelection) []MagpieSelection {
	if len(selections) == 0 {
		return nil
	}
	res := make([]MagpieSelection, 0, len(selections))
	for _, sel := range selections {
		res = append(res, MagpieSelection{
			EndpointID: sel.EndpointID,
			Models:     append([]string(nil), sel.Models...),
		})
	}
	return res
}
