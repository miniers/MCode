package app

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote/wire"
)

// currentAgentMagpieRouting 每次读取最新配置与密钥。
func currentAgentMagpieRouting(agent string) (config.Config, AgentRouting) {
	dataDir, secrets := piMagpieRuntimeState()
	if dataDir == "" {
		return config.Config{}, AgentRouting{}
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		log.Printf("%s magpie: 读取配置失败: %v", agent, err)
		return config.Config{}, AgentRouting{}
	}
	return cfg, ResolveAgentRouting(cfg, secrets, agent)
}

func claudeMagpieRoutes() []claude.MagpieRoute {
	cfg, routing := currentAgentMagpieRouting(config.ProviderClaude)
	if routing.Mode != config.MagpieModeInject || !routing.Active() {
		return nil
	}
	routes := make([]claude.MagpieRoute, 0, len(routing.Targets))
	for _, target := range routing.Targets {
		route := claude.MagpieRoute{ProviderID: target.ProviderID, BaseURL: claudeMagpieRouteBaseURL(cfg, target), APIKey: target.APIKey}
		for _, model := range routing.Models {
			if model.EndpointID == target.Endpoint {
				route.Models = append(route.Models, model.ModelID)
			}
		}
		if target.Endpoint == routing.Default.EndpointID {
			route.Default = routing.Default.ModelID
		}
		if len(route.Models) > 0 {
			routes = append(routes, route)
		}
	}
	return routes
}

func codexMagpieRoutes() []codex.MagpieRoute {
	_, routing := currentAgentMagpieRouting(config.ProviderCodex)
	if routing.Mode != config.MagpieModeInject || !routing.Active() {
		return nil
	}
	routes := make([]codex.MagpieRoute, 0, len(routing.Targets))
	for _, target := range routing.Targets {
		var selected bool
		for _, model := range routing.Models {
			if model.EndpointID == target.Endpoint {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		route := codex.MagpieRoute{ProviderID: target.ProviderID, BaseURL: target.BaseURL, APIKey: target.APIKey}
		if target.Endpoint == routing.Default.EndpointID {
			route.Default = routing.Default.ModelID
		}
		routes = append(routes, route)
	}
	return routes
}

func claudeMagpieRouteBaseURL(cfg config.Config, target magpieinject.Target) string {
	for _, ep := range cfg.Providers.Custom {
		if ep.ID == target.Endpoint && strings.TrimSpace(ep.BaseURL) != "" {
			return ep.BaseURL
		}
	}
	return target.BaseURL
}

// magpieEffortBackends 是各 agent 后端能用的思考档位；不在表里的 agent 不填档位。
var magpieEffortBackends = map[string]map[string]bool{
	config.ProviderClaude: {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	config.ProviderPi:     {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	config.ProviderMcode:  {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	config.ProviderCodex:  {"low": true, "medium": true, "high": true, "xhigh": true},
}

// magpieEffortsFor 把网关档位过滤成该 agent 后端允许的档位：保序、去重，
// none 与 minimal 对所有 agent 都丢弃，过滤后为空时返回 nil。
func magpieEffortsFor(agent string, levels []string) []string {
	allowed := magpieEffortBackends[agent]
	if allowed == nil {
		return nil
	}
	var efforts []string
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if !allowed[level] || seen[level] {
			continue
		}
		seen[level] = true
		efforts = append(efforts, level)
	}
	return efforts
}

// magpieWarm 记录正在预热的端点，键与 magpie 的模型缓存键一致。
var magpieWarm = struct {
	sync.Mutex
	inFlight map[string]bool
}{inFlight: map[string]bool{}}

var warmMagpieFetch = magpie.FetchModels

// warmMagpieModels 在后台拉一次模型目录，让下次打开模型选择器时能读到档位。
// 同一端点同时只跑一个请求；失败交给 magpie 包的失败缓存处理，这里不重试也不上报。
func warmMagpieModels(target magpieinject.Target) {
	key := target.BaseURL + "\x00" + target.APIKey
	magpieWarm.Lock()
	if magpieWarm.inFlight[key] {
		magpieWarm.Unlock()
		return
	}
	magpieWarm.inFlight[key] = true
	magpieWarm.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Magpie 模型目录预热 panic：%v", r)
			}
			magpieWarm.Lock()
			delete(magpieWarm.inFlight, key)
			magpieWarm.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = warmMagpieFetch(ctx, target.BaseURL, target.APIKey)
	}()
}

// magpieCatalogRows 返回当前模式下勾选的模型目录。
func magpieCatalogRows(agent string) ([]wire.ProviderModel, bool) {
	cfg, routing := currentAgentMagpieRouting(agent)
	if !routing.Active() {
		return nil, false
	}
	labels := make(map[string]string, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		labels[ep.ID] = strings.TrimSpace(ep.Name)
	}
	targets := make(map[string]magpieinject.Target, len(routing.Targets))
	for _, target := range routing.Targets {
		targets[target.Endpoint] = target
	}
	warmed := make(map[string]bool, len(routing.Targets))
	rows := make([]wire.ProviderModel, 0, len(routing.Models))
	for _, model := range routing.Models {
		label := labels[model.EndpointID]
		if label == "" {
			label = model.ProviderID
		}
		detail := label + " · Magpie"
		id := model.Ref()
		if routing.Mode == config.MagpieModeGlobal && agent != config.ProviderPi {
			id = model.ModelID
		}
		row := wire.ProviderModel{ID: id, Name: magpie.ShortName(magpie.Model{ID: model.ModelID}), Detail: &detail, IsDefault: model == routing.Default}
		// pi 的档位来自注入目录，这里保持现状不填。
		if agent != config.ProviderPi {
			if target, ok := targets[model.EndpointID]; ok {
				if cached, hit := magpie.CachedModel(target.BaseURL, target.APIKey, model.ModelID); hit {
					row.Efforts = magpieEffortsFor(agent, cached.Levels)
				} else if !warmed[target.Endpoint] {
					warmed[target.Endpoint] = true
					warmMagpieModels(target)
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, true
}
