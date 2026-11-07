package app

import (
	"strings"

	"mcode/internal/config"
	"mcode/internal/magpieinject"
)

// AgentRoutingModel 标识一个端点上可用的模型。
type AgentRoutingModel struct {
	EndpointID string
	ProviderID string
	ModelID    string
}

// Ref 返回带端点前缀的模型 ID。
func (m AgentRoutingModel) Ref() string {
	return m.ProviderID + "/" + m.ModelID
}

// AgentRouting 是某个 agent 当前可用的 Magpie 路由。
type AgentRouting struct {
	Agent   string
	Mode    config.MagpieMode
	Models  []AgentRoutingModel
	Default AgentRoutingModel
	Targets []magpieinject.Target
}

// ResolveAgentRouting 根据配置和密钥解析当前可用的路由。
func ResolveAgentRouting(cfg config.Config, secrets config.SecretStore, agent string) AgentRouting {
	routing := AgentRouting{Agent: agent, Mode: config.MagpieModeOff}
	if agent != config.ProviderClaude && agent != config.ProviderCodex && agent != config.ProviderPi {
		return routing
	}
	if !config.MagpieAgentSupportsInject(agent) && !config.MagpieAgentSupportsGlobal(agent) {
		return routing
	}
	setting := config.MagpieAgentSetting(cfg, agent)
	routing.Mode = setting.Mode
	if setting.Mode == config.MagpieModeOff || len(setting.Selections) == 0 {
		return routing
	}
	for _, target := range magpieinject.Targets(cfg, secrets, setting.Selections) {
		if !target.HasKey() {
			continue
		}
		routing.Targets = append(routing.Targets, target)
		seen := make(map[string]bool)
		for _, selection := range setting.Selections {
			if strings.TrimSpace(selection.EndpointID) != strings.TrimSpace(target.Endpoint) {
				continue
			}
			for _, id := range selection.Models {
				id = strings.TrimSpace(id)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				routing.Models = append(routing.Models, AgentRoutingModel{
					EndpointID: target.Endpoint, ProviderID: target.ProviderID, ModelID: id,
				})
			}
		}
	}
	if len(routing.Models) > 0 {
		routing.Default = routing.Models[0]
		endpointID := strings.TrimSpace(setting.DefaultEndpointID)
		modelID := strings.TrimSpace(setting.DefaultModel)
		if endpointID != "" && modelID != "" {
			for _, model := range routing.Models {
				if model.EndpointID == endpointID && model.ModelID == modelID {
					routing.Default = model
					break
				}
			}
		}
	}
	return routing
}

// Lookup 查找带端点前缀且已勾选的模型。
func (r AgentRouting) Lookup(ref string) (AgentRoutingModel, magpieinject.Target, bool) {
	target, modelID, ok := magpieinject.ProviderOf(ref, r.Targets)
	if ok {
		for _, model := range r.Models {
			if model.EndpointID == target.Endpoint && model.ProviderID == target.ProviderID && model.ModelID == modelID {
				return model, target, true
			}
		}
	}
	return AgentRoutingModel{}, magpieinject.Target{}, false
}

// Active 报告路由是否包含可用模型。
func (r AgentRouting) Active() bool {
	return (r.Mode == config.MagpieModeInject || r.Mode == config.MagpieModeGlobal) && len(r.Models) > 0
}
