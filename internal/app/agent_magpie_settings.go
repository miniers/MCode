package app

// 本文件实现 app 层的 agent magpie 设置接口：读取某 agent 的端点、勾选、模式与
// 支持 global 的 agent（claude、codex、cline、pi）的全局配置接管状态，保存勾选，在 inject
// 与 global 之间切换，并处理 global 模式的 reapply / keep。写全局配置走
// internal/magpiecfg，状态文件放在 <数据目录>/agentcfg/<agent>/；API Key 只在
// 写全局配置文件（claude 的 settings.json、codex 的 config.toml、cline 的
// providers.json 与 models.json、pi 的 models.json 与 settings.json）时明文落盘，
// 读取接口不返回它。

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/magpiecfg"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// agentMagpieFetchTimeout 是读取端点模型目录的超时。设置页只需要模型名，
// 取不到就把错误写进该端点、不阻塞整个列表。
var agentMagpieFetchTimeout = 5 * time.Second

// AgentMagpie 返回某 agent 的 magpie 设置：各端点的可选模型与已勾选模型、
// 当前模式，以及支持 global 的 agent 的全局配置接管状态。
func (c *Client) AgentMagpie(ctx context.Context, agent string) (mcodeui.AgentMagpieSetting, error) {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return mcodeui.AgentMagpieSetting{}, errors.New("agent 不能为空")
	}
	cfg, ok := c.loadConfig()
	if !ok {
		return mcodeui.AgentMagpieSetting{}, errors.New("dataDir 未初始化")
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()

	setting := config.MagpieAgentSetting(cfg, agent)
	targets := magpieinject.Targets(cfg, secrets, nil)
	names := agentMagpieEndpointNames(cfg)
	out := mcodeui.AgentMagpieSetting{
		Agent:     agent,
		Mode:      string(setting.Mode),
		Endpoints: make([]mcodeui.PiMagpieEndpoint, 0, len(targets)),
		// 默认模型可能指向已删除的端点或已取消勾选的模型，原样带出供 UI 判断。
		DefaultEndpointID: setting.DefaultEndpointID,
		DefaultModel:      setting.DefaultModel,
		// cline 是 global-only，没有运行时注入通道。
		InjectSupported: config.MagpieAgentSupportsInject(agent),
	}
	for _, target := range targets {
		ep := mcodeui.PiMagpieEndpoint{
			ID:         target.Endpoint,
			Name:       names[target.Endpoint],
			ProviderID: target.ProviderID,
			Selected:   agentMagpieSelectedModels(setting, target.Endpoint),
		}
		if !target.HasKey() {
			ep.Err = "未设置 API Key"
			out.Endpoints = append(out.Endpoints, ep)
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, agentMagpieFetchTimeout)
		models, err := magpie.FetchModels(fetchCtx, target.BaseURL, target.APIKey)
		cancel()
		if err != nil {
			ep.Err = err.Error()
			out.Endpoints = append(out.Endpoints, ep)
			continue
		}
		ep.Models = make([]mcodeui.PiMagpieModel, 0, len(models))
		for _, m := range models {
			ep.Models = append(ep.Models, mcodeui.PiMagpieModel{ID: m.ID, Name: magpie.ShortName(m)})
		}
		out.Endpoints = append(out.Endpoints, ep)
	}

	if !config.MagpieAgentSupportsGlobal(agent) {
		return out, nil
	}
	// 档位只对 claude 有意义：claude 的 settings.json 有 ANTHROPIC_DEFAULT_*
	// 档位映射，codex 的 config.toml 没有。
	if agent == config.ProviderClaude {
		// 档位映射与 globalPlan 用同一套选端点、选主模型规则，只读配置不请求网关。
		if _, endpointID, ids, main, ok := claudeGlobalPick(setting, targets); ok {
			selected := false
			for _, id := range ids {
				if id == main {
					selected = true
					break
				}
			}
			// 与 magpiecfg.ClaudePlan 一致：主模型为空或不在勾选里时用第一条。
			if main == "" || !selected {
				main = ids[0]
			}
			selections := make([]magpiecfg.ClaudeSelection, 0, len(ids))
			for _, id := range ids {
				selections = append(selections, magpiecfg.ClaudeSelection{ID: id})
			}
			tiers := magpiecfg.ClaudeTierModels(main, selections)
			out.Tiers = make([]mcodeui.AgentMagpieTier, 0, len(magpiecfg.ClaudeTiers))
			for _, tier := range magpiecfg.ClaudeTiers {
				out.Tiers = append(out.Tiers, mcodeui.AgentMagpieTier{
					Tier:       tier,
					EndpointID: endpointID,
					ModelID:    tiers[tier],
				})
			}
		}
	}
	global := mcodeui.AgentMagpieGlobal{Supported: true}
	switch agent {
	case config.ProviderClaude:
		if path, err := magpiecfg.ClaudeSettingsPath(); err == nil {
			global.Path = path
		} else {
			global.Err = err.Error()
		}
		if path, ok := magpiecfg.ClaudeManagedSettingsPresent(); ok {
			global.ManagedPath = path
		}
	case config.ProviderCodex:
		if path, err := magpiecfg.CodexConfigPath(); err == nil {
			global.Path = path
		} else {
			global.Err = err.Error()
		}
	case config.ProviderCline:
		if path, _, err := magpiecfg.ClinePaths(); err == nil {
			global.Path = path
		} else {
			global.Err = err.Error()
		}
	case config.ProviderPi:
		if path, _, err := magpiecfg.PiPaths(); err == nil {
			global.Path = path
		} else {
			global.Err = err.Error()
		}
	}
	status := magpiecfg.Drift(c.effectiveDataDir(), agent)
	global.State = status.State
	global.Drifted = append([]string(nil), status.Drifted...)
	// 正常情况两个路径一致；CLAUDE_CONFIG_DIR 改过时以实际接管的文件为准。
	if global.Path == "" && status.Path != "" {
		global.Path = status.Path
	}
	if status.Err != "" {
		global.Err = status.Err
	}
	out.Global = global
	return out, nil
}

// agentMagpieEndpointNames 按端点 ID 取展示名，只用 Magpie 端点。
func agentMagpieEndpointNames(cfg config.Config) map[string]string {
	names := make(map[string]string, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		if ep.Magpie {
			names[ep.ID] = ep.Name
		}
	}
	return names
}

// agentMagpieSelectedModels 返回某 agent 在某个端点上勾选的模型副本。
func agentMagpieSelectedModels(setting config.MagpieAgentConfig, endpointID string) []string {
	for _, selection := range setting.Selections {
		if strings.TrimSpace(selection.EndpointID) == endpointID {
			return append([]string(nil), selection.Models...)
		}
	}
	return nil
}

// globalPlan 按 agent 生成写全局配置的计划。每个 agent 有各自的选主模型规则
// 与文件格式，写成 switch 便于后续增加支持的 agent。
func globalPlan(ctx context.Context, cfg config.Config, secrets config.SecretStore, agent string) (magpiecfg.Plan, error) {
	setting := config.MagpieAgentSetting(cfg, agent)
	targets := magpieinject.Targets(cfg, secrets, nil)
	switch agent {
	case config.ProviderClaude:
		return claudeGlobalPlan(ctx, setting, targets)
	case config.ProviderCodex:
		return codexGlobalPlan(setting, targets)
	case config.ProviderCline:
		return clineGlobalPlan(ctx, setting, targets)
	case config.ProviderPi:
		return piGlobalPlan(ctx, setting, targets)
	}
	return magpiecfg.Plan{}, fmt.Errorf("agent %q 暂不支持写全局配置", agent)
}

// claudeGlobalPlan 使用唯一勾选网关的全部模型，主模型优先使用有效默认模型，
// 否则取第一项。切换模式和保存勾选时保证 claude 只使用一个网关。
// 模型用网关的原始模型 ID（勾选里存的就是它），不加 ProviderID 前缀；地址
// 按 Claude Code 的要求去掉结尾 /v1。显示名与上下文窗口来自网关的
// /v1/models：取不到时 Label 留空（picker 退化为 ID 去前缀）、ContextWindow
// 为 0（不加 [1m]），仍照常生成计划，不让网关抖动挡住模式切换。
func claudeGlobalPlan(ctx context.Context, setting config.MagpieAgentConfig, targets []magpieinject.Target) (magpiecfg.Plan, error) {
	target, _, ids, main, ok := claudeGlobalPick(setting, targets)
	if !ok {
		return magpiecfg.Plan{}, errors.New("请先勾选一个可用的 magpie 模型")
	}
	opts := magpiecfg.ClaudeOptions{
		BaseURL:    magpieinject.AnthropicBaseURL(target.BaseURL),
		APIKey:     target.APIKey,
		Main:       main,
		Selections: make([]magpiecfg.ClaudeSelection, 0, len(ids)),
	}
	for _, id := range ids {
		opts.Selections = append(opts.Selections, magpiecfg.ClaudeSelection{ID: id})
	}
	fetchCtx, cancel := context.WithTimeout(ctx, agentMagpieFetchTimeout)
	models, err := magpie.FetchModels(fetchCtx, target.BaseURL, target.APIKey)
	cancel()
	if err == nil {
		byID := make(map[string]magpie.Model, len(models))
		opts.Served = make([]string, 0, len(models))
		for _, m := range models {
			if m.ID == "" {
				continue
			}
			byID[m.ID] = m
			opts.Served = append(opts.Served, m.ID)
		}
		for i := range opts.Selections {
			if m, ok := byID[opts.Selections[i].ID]; ok {
				opts.Selections[i].Label = m.Name
				opts.Selections[i].ContextWindow = m.ContextWindow
			}
		}
	}
	return magpiecfg.ClaudePlan(opts)
}

// codexGlobalPlan 生成 codex 的 config.toml 计划。codex 的全局配置只能指向
// 一个 provider，所以与 claude 共用选端点、选主模型的规则；不请求
// /v1/models 补显示名（codex 的配置里没有这些字段），主模型为空或不在勾选里
// 时取第一条。
func codexGlobalPlan(setting config.MagpieAgentConfig, targets []magpieinject.Target) (magpiecfg.Plan, error) {
	target, _, ids, main, ok := claudeGlobalPick(setting, targets)
	if !ok {
		return magpiecfg.Plan{}, errors.New("请先勾选一个可用的 magpie 模型")
	}
	selected := false
	for _, id := range ids {
		if id == main {
			selected = true
			break
		}
	}
	if main == "" || !selected {
		main = ids[0]
	}
	return magpiecfg.CodexPlan(magpiecfg.CodexOptions{
		BaseURL: target.BaseURL,
		APIKey:  target.APIKey,
		Model:   main,
	})
}

// clineGlobalPlan 生成 cline 的 providers.json + models.json 计划。cline 与
// claude、codex 共用选端点、选主模型的规则；provider 只能指向一个网关。
// /v1/models 只用来补显示名与模型元数据，取不到时留零值，仍照常生成计划。
func clineGlobalPlan(ctx context.Context, setting config.MagpieAgentConfig, targets []magpieinject.Target) (magpiecfg.Plan, error) {
	target, _, ids, main, ok := claudeGlobalPick(setting, targets)
	if !ok {
		return magpiecfg.Plan{}, errors.New("请先勾选一个可用的 magpie 模型")
	}
	byID := make(map[string]magpie.Model, len(ids))
	fetchCtx, cancel := context.WithTimeout(ctx, agentMagpieFetchTimeout)
	models, err := magpie.FetchModels(fetchCtx, target.BaseURL, target.APIKey)
	cancel()
	if err == nil {
		for _, m := range models {
			if m.ID != "" {
				byID[m.ID] = m
			}
		}
	}
	rows := make([]magpiecfg.ClineModel, 0, len(ids))
	for _, id := range ids {
		row := magpiecfg.ClineModel{ID: id}
		if m, ok := byID[id]; ok {
			row.Name = m.Name
			row.ContextWindow = m.ContextWindow
			row.MaxTokens = m.MaxTokens
			row.Vision = slices.Contains(m.Input, "image")
			row.Reasoning = m.Reasoning
		}
		rows = append(rows, row)
	}
	return magpiecfg.ClinePlan(magpiecfg.ClineOptions{
		BaseURL: target.BaseURL,
		APIKey:  target.APIKey,
		Main:    main,
		Models:  rows,
	})
}

// piGlobalPlan 生成 pi 的 models.json + settings.json 计划。与 claude、codex、
// cline 不同，pi 的 models.json 可以放多个 provider，所以每个有 key 且勾了模型
// 的端点都写进去，provider id 沿用注入时的 ProviderID，切换模式前后模型引用
// 不变。默认 provider / 主模型按 claudeGlobalPick 的规则选；它选中的端点一定
// 也在写入列表里。/v1/models 只用来补模型元数据，取不到时用 Selected 补默认，
// 不让网关抖动挡住模式切换。
func piGlobalPlan(ctx context.Context, setting config.MagpieAgentConfig, targets []magpieinject.Target) (magpiecfg.Plan, error) {
	target, _, _, main, ok := claudeGlobalPick(setting, targets)
	if !ok {
		return magpiecfg.Plan{}, errors.New("请先勾选一个可用的 magpie 模型")
	}
	byEndpoint := make(map[string]magpieinject.Target, len(targets))
	for _, t := range targets {
		byEndpoint[t.Endpoint] = t
	}
	providers := make([]magpie.PiProvider, 0, len(setting.Selections))
	written := make(map[string]bool, len(setting.Selections))
	for _, selection := range setting.Selections {
		id := strings.TrimSpace(selection.EndpointID)
		t, found := byEndpoint[id]
		if !found || !t.HasKey() || written[t.ProviderID] {
			continue
		}
		ids := make([]string, 0, len(selection.Models))
		for _, modelID := range selection.Models {
			if modelID = strings.TrimSpace(modelID); modelID != "" {
				ids = append(ids, modelID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		providers = append(providers, magpie.PiProvider{
			Provider: t.ProviderID,
			Endpoint: t.Endpoint,
			BaseURL:  t.BaseURL,
			APIKey:   t.APIKey,
			Models:   magpieinject.Selected(ids, piGlobalFetchedModels(ctx, t)),
		})
		written[t.ProviderID] = true
	}
	if len(providers) == 0 {
		return magpiecfg.Plan{}, errors.New("请先勾选一个可用的 magpie 模型")
	}
	defaultProvider := target.ProviderID
	defaultModel := main
	if !written[defaultProvider] {
		defaultProvider = providers[0].Provider
		defaultModel = providers[0].Models[0].ID
	} else if strings.TrimSpace(defaultModel) == "" {
		for _, provider := range providers {
			if provider.Provider == defaultProvider {
				defaultModel = provider.Models[0].ID
				break
			}
		}
	}
	return magpiecfg.PiPlan(magpiecfg.PiOptions{
		Providers:       providers,
		DefaultProvider: defaultProvider,
		DefaultModel:    defaultModel,
	})
}

// piGlobalFetchedModels 读取端点的模型目录，只用于补显示名与元数据；失败时
// 返回 nil，由 magpieinject.Selected 填默认值。
func piGlobalFetchedModels(ctx context.Context, target magpieinject.Target) []magpie.Model {
	fetchCtx, cancel := context.WithTimeout(ctx, agentMagpieFetchTimeout)
	defer cancel()
	models, err := magpie.FetchModels(fetchCtx, target.BaseURL, target.APIKey)
	if err != nil {
		return nil
	}
	return models
}

// claudeGlobalPick 是写全局配置时选端点、选主模型的规则：把用户默认端点对应
// 的勾选排到最前，取第一个既有 target（即有 API Key）又勾了模型的端点，返回
// 该端点、去空后的勾选模型 ID 与主模型（仅默认端点生效时非空）。都不满足时
// ok 为 false。
func claudeGlobalPick(setting config.MagpieAgentConfig, targets []magpieinject.Target) (target magpieinject.Target, endpointID string, ids []string, main string, ok bool) {
	defaultEndpointID := agentMagpieDefaultEndpoint(setting, targets)
	selections := agentMagpieDefaultFirst(setting.Selections, defaultEndpointID)
	for _, selection := range selections {
		id := strings.TrimSpace(selection.EndpointID)
		var found magpieinject.Target
		hasTarget := false
		for _, t := range targets {
			if t.Endpoint == id {
				found, hasTarget = t, true
				break
			}
		}
		if !hasTarget || !found.HasKey() {
			continue
		}
		picked := make([]string, 0, len(selection.Models))
		for _, model := range selection.Models {
			if model = strings.TrimSpace(model); model != "" {
				picked = append(picked, model)
			}
		}
		if len(picked) == 0 {
			continue
		}
		main = ""
		if id == defaultEndpointID {
			main = strings.TrimSpace(setting.DefaultModel)
		}
		return found, id, picked, main, true
	}
	return magpieinject.Target{}, "", nil, "", false
}

// agentMagpieDefaultEndpoint 返回默认模型生效的端点 ID：默认端点必须在
// targets 里（即有 API Key），默认模型仍在该端点的勾选里。任一条件不满足都
// 返回空，调用方回到原规则：第一个有 key 且有勾选的端点的第一项。
func agentMagpieDefaultEndpoint(setting config.MagpieAgentConfig, targets []magpieinject.Target) string {
	endpointID := strings.TrimSpace(setting.DefaultEndpointID)
	modelID := strings.TrimSpace(setting.DefaultModel)
	if endpointID == "" || modelID == "" {
		return ""
	}
	found := false
	for _, target := range targets {
		if target.Endpoint == endpointID {
			found = target.HasKey()
			break
		}
	}
	if !found {
		return ""
	}
	for _, model := range agentMagpieSelectedModels(setting, endpointID) {
		if strings.TrimSpace(model) == modelID {
			return endpointID
		}
	}
	return ""
}

// agentMagpieDefaultFirst 把默认端点对应的勾选排到最前，其余保持原顺序；
// endpointID 为空时原样返回。
func agentMagpieDefaultFirst(selections []config.MagpieSelection, endpointID string) []config.MagpieSelection {
	if endpointID == "" {
		return selections
	}
	out := make([]config.MagpieSelection, 0, len(selections))
	for _, selection := range selections {
		if strings.TrimSpace(selection.EndpointID) == endpointID {
			out = append(out, selection)
		}
	}
	for _, selection := range selections {
		if strings.TrimSpace(selection.EndpointID) != endpointID {
			out = append(out, selection)
		}
	}
	return out
}

var (
	errAgentMagpieNoModels         = errors.New("请先在 Magpie 模型列表里为该智能体勾选模型")
	errAgentMagpieMultipleGateways = errors.New("全局模式只能使用一个网关的模型，请先取消其它网关上的勾选")
	errAgentMagpieGlobalEmpty      = errors.New("全局模式下至少保留一个模型，或先切回直连")
)

func agentMagpieSelectedGatewayCount(setting config.MagpieAgentConfig) int {
	selected := make(map[string]bool)
	for _, selection := range setting.Selections {
		id := strings.TrimSpace(selection.EndpointID)
		for _, model := range selection.Models {
			if id != "" && strings.TrimSpace(model) != "" {
				selected[id] = true
				break
			}
		}
	}
	return len(selected)
}

// SetAgentMagpieModels 保存某 agent 在某端点上勾选的模型。若该 agent 当前是
// global 模式且全局配置仍被接管（applied / drifted），用新配置重新写入一次，
// 让换模型立即生效；重新写入失败时勾选已保存，错误文案会说明这一点。
func (c *Client) SetAgentMagpieModels(ctx context.Context, agent, endpointID string, modelIDs []string) error {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	agent = strings.TrimSpace(agent)
	id := strings.TrimSpace(endpointID)
	switch {
	case agent == "":
		return errors.New("agent 不能为空")
	case id == "":
		return errors.New("端点 ID 不能为空")
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return errors.New("dataDir 未初始化")
	}
	secrets := c.secrets

	if err := config.Update(dataDir, func(latest *config.Config) error {
		found := false
		for _, ep := range latest.Providers.Custom {
			if ep.ID != id {
				continue
			}
			if !ep.Magpie {
				return fmt.Errorf("端点 %q 不是 Magpie 端点", id)
			}
			found = true
			break
		}
		if !found {
			return fmt.Errorf("自定义端点 %q 不存在", id)
		}
		config.SetMagpieAgentModels(latest, agent, id, modelIDs)
		setting := config.MagpieAgentSetting(*latest, agent)
		if (agent == config.ProviderClaude || agent == config.ProviderCodex) && setting.Mode == config.MagpieModeGlobal {
			switch agentMagpieSelectedGatewayCount(setting) {
			case 0:
				return errAgentMagpieGlobalEmpty
			case 1:
			default:
				return errAgentMagpieMultipleGateways
			}
		}
		return nil
	}); err != nil {
		if errors.Is(err, errAgentMagpieGlobalEmpty) || errors.Is(err, errAgentMagpieMultipleGateways) {
			return err
		}
		return fmt.Errorf("save config: %w", err)
	}

	return c.agentMagpieReapplyGlobal(ctx, dataDir, agent, "勾选已保存", secrets)
}

// SetAgentMagpieDefaultModel 保存某 agent 手动指定的主模型（端点上勾选的一个
// 模型）。endpointID 与 modelID 任一为空都表示恢复自动（按勾选顺序取第一个
// 可用端点的第一项）；配置值不校验是否已勾选。若该 agent 处于 global 模式且
// 全局配置仍被接管，按与 SetAgentMagpieModels 相同的路径重新写入一次。
func (c *Client) SetAgentMagpieDefaultModel(ctx context.Context, agent, endpointID, modelID string) error {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	agent = strings.TrimSpace(agent)
	if agent == "" {
		return errors.New("agent 不能为空")
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return errors.New("dataDir 未初始化")
	}
	secrets := c.secrets

	if err := config.Update(dataDir, func(latest *config.Config) error {
		config.SetMagpieAgentDefault(latest, agent, endpointID, modelID)
		return nil
	}); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	return c.agentMagpieReapplyGlobal(ctx, dataDir, agent, "默认模型已保存", secrets)
}

// agentMagpieReapplyGlobal 在 agent 处于 global 模式且全局配置仍被接管
// （applied / drifted）时，按最新配置重算计划并重写；saved 描述已保存内容，
// 拼进失败文案。其它情况下什么都不做。
func (c *Client) agentMagpieReapplyGlobal(ctx context.Context, dataDir, agent, saved string, secrets config.SecretStore) error {
	cfg, ok := c.loadConfig()
	if !ok {
		return fmt.Errorf("%s，但重新读取配置失败", saved)
	}
	if config.MagpieAgentSetting(cfg, agent).Mode != config.MagpieModeGlobal {
		return nil
	}
	status := magpiecfg.Drift(dataDir, agent)
	if status.State != magpiecfg.StatusApplied && status.State != magpiecfg.StatusDrifted {
		return nil
	}
	plan, err := globalPlan(ctx, cfg, secrets, agent)
	if err == nil {
		err = magpiecfg.Reapply(dataDir, plan)
	}
	if err != nil {
		return fmt.Errorf("%s，但重新写入全局配置失败：%w", saved, err)
	}
	return nil
}

// agentMagpieSaveMode 只把模式写进配置，不碰全局配置文件。
func agentMagpieSaveMode(dataDir, agent string, mode config.MagpieMode) error {
	return config.Update(dataDir, func(latest *config.Config) error {
		config.SetMagpieAgentMode(latest, agent, mode)
		return nil
	})
}

// SetAgentMagpieMode 切换 agent 使用 magpie 模型的方式（inject / global / off）。
// 切到 global 先写全局配置，成功后才落盘模式；写失败时配置保持原样。
// 离开 global 先按 stash 恢复全局配置，恢复失败时模式不变。
func (c *Client) SetAgentMagpieMode(ctx context.Context, agent, mode string) error {
	agent = strings.TrimSpace(agent)
	mode = strings.TrimSpace(mode)
	if agent == "" {
		return errors.New("agent 不能为空")
	}
	if mode != string(config.MagpieModeInject) && mode != string(config.MagpieModeGlobal) && mode != string(config.MagpieModeOff) {
		return fmt.Errorf("未知的 magpie 模式 %q", mode)
	}

	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return errors.New("dataDir 未初始化")
	}
	cfg, ok := c.loadConfig()
	if !ok {
		return errors.New("dataDir 未初始化")
	}
	current := config.MagpieAgentSetting(cfg, agent).Mode
	if string(current) == mode {
		if current != config.MagpieModeGlobal {
			return nil
		}
		applied, err := magpiecfg.Applied(dataDir, agent)
		if err != nil {
			return fmt.Errorf("读取全局配置接管状态失败：%w", err)
		}
		if applied {
			return nil
		}
	}
	if mode != string(config.MagpieModeOff) && (agent == config.ProviderClaude || agent == config.ProviderCodex || agent == config.ProviderPi) {
		config.SetMagpieAgentMode(&cfg, agent, config.MagpieMode(mode))
		if len(ResolveAgentRouting(cfg, c.secrets, agent).Models) == 0 {
			return errAgentMagpieNoModels
		}
		if mode == string(config.MagpieModeGlobal) && (agent == config.ProviderClaude || agent == config.ProviderCodex) && agentMagpieSelectedGatewayCount(config.MagpieAgentSetting(cfg, agent)) > 1 {
			return errAgentMagpieMultipleGateways
		}
	}

	if mode == string(config.MagpieModeGlobal) {
		if !config.MagpieAgentSupportsGlobal(agent) {
			return fmt.Errorf("agent %q 暂不支持写全局配置", agent)
		}
		plan, err := globalPlan(ctx, cfg, c.secrets, agent)
		if err != nil {
			return err
		}
		if err := magpiecfg.Connect(dataDir, plan); err != nil {
			return fmt.Errorf("写入全局配置失败：%w", err)
		}
		if err := agentMagpieSaveMode(dataDir, agent, config.MagpieModeGlobal); err != nil {
			return fmt.Errorf("全局配置已写入，但保存模式失败：%w", err)
		}
		return nil
	}

	if current == config.MagpieModeGlobal {
		if err := magpiecfg.Disconnect(dataDir, agent); err != nil {
			return fmt.Errorf("恢复全局配置失败：%w", err)
		}
	}
	if err := agentMagpieSaveMode(dataDir, agent, config.MagpieMode(mode)); err != nil {
		return fmt.Errorf("全局配置已恢复，但保存模式失败：%w", err)
	}
	return nil
}

// AgentMagpieGlobalAction 处理 global 模式的状态操作：
// reapply 按当前勾选重新写入，keep 表示保留用户的修改、不再跟踪。
func (c *Client) AgentMagpieGlobalAction(ctx context.Context, agent, action string) error {
	agent = strings.TrimSpace(agent)
	action = strings.TrimSpace(action)
	if agent == "" {
		return errors.New("agent 不能为空")
	}

	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return errors.New("dataDir 未初始化")
	}
	switch action {
	case "reapply":
		cfg, ok := c.loadConfig()
		if !ok {
			return errors.New("dataDir 未初始化")
		}
		plan, err := globalPlan(ctx, cfg, c.secrets, agent)
		if err != nil {
			return err
		}
		return magpiecfg.Reapply(dataDir, plan)
	case "keep":
		return magpiecfg.Keep(dataDir, agent)
	default:
		return fmt.Errorf("未知的全局配置操作 %q", action)
	}
}
