package app

import (
	"reflect"
	"testing"

	"mcode/internal/config"
)

func TestResolveAgentRouting(t *testing.T) {
	cfg := magpieTestConfig(
		config.CustomEndpointConfig{ID: "first", Name: "Magpie First", Enabled: true, Magpie: true},
		config.CustomEndpointConfig{ID: "nokey", Name: "Magpie No Key", Enabled: true, Magpie: true},
		config.CustomEndpointConfig{ID: "disabled", Name: "Magpie Disabled", Enabled: false, Magpie: true},
		config.CustomEndpointConfig{ID: "second", Name: "Magpie Second", Enabled: true, Magpie: true},
	)
	secrets := magpieTestSecrets(t, map[string]string{"first": "key1", "disabled": "key3", "second": "key2"})
	setting := config.MagpieAgentConfig{
		Mode: config.MagpieModeInject,
		Selections: []config.MagpieSelection{
			{EndpointID: "second", Models: []string{"shared", "second-only"}},
			{EndpointID: "deleted", Models: []string{"deleted-model"}},
			{EndpointID: "first", Models: []string{"shared", "vendor/model", "shared"}},
			{EndpointID: "nokey", Models: []string{"no-key-model"}},
			{EndpointID: "disabled", Models: []string{"disabled-model"}},
			{EndpointID: "first", Models: []string{"vendor/model", "last"}},
		},
	}
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderClaude: setting}

	for _, mode := range []config.MagpieMode{config.MagpieModeInject, config.MagpieModeGlobal} {
		t.Run(string(mode), func(t *testing.T) {
			setting.Mode = mode
			cfg.Providers.MagpieAgents[config.ProviderClaude] = setting
			routing := ResolveAgentRouting(cfg, secrets, config.ProviderClaude)
			if routing.Agent != config.ProviderClaude || routing.Mode != mode || !routing.Active() {
				t.Fatalf("路由状态 = %+v", routing)
			}
			if len(routing.Targets) != 2 || routing.Targets[0].Endpoint != "first" || routing.Targets[1].Endpoint != "second" {
				t.Fatalf("端点 = %+v，想要 first、second", routing.Targets)
			}
			want := []AgentRoutingModel{
				{EndpointID: "first", ProviderID: routing.Targets[0].ProviderID, ModelID: "shared"},
				{EndpointID: "first", ProviderID: routing.Targets[0].ProviderID, ModelID: "vendor/model"},
				{EndpointID: "first", ProviderID: routing.Targets[0].ProviderID, ModelID: "last"},
				{EndpointID: "second", ProviderID: routing.Targets[1].ProviderID, ModelID: "shared"},
				{EndpointID: "second", ProviderID: routing.Targets[1].ProviderID, ModelID: "second-only"},
			}
			if !reflect.DeepEqual(routing.Models, want) {
				t.Fatalf("模型 = %+v，想要 %+v", routing.Models, want)
			}
			if routing.Models[0].Ref() == routing.Models[3].Ref() || routing.Default != want[0] {
				t.Errorf("同名模型引用或默认模型不正确: %+v", routing)
			}
			model, target, ok := routing.Lookup(want[1].Ref())
			if !ok || model != want[1] || target != routing.Targets[0] {
				t.Errorf("Lookup 命中 = %+v, %+v, %v", model, target, ok)
			}
			for _, ref := range []string{"unknown/shared", routing.Targets[0].ProviderID + "/unselected", "shared", routing.Targets[0].ProviderID + "/"} {
				if _, _, ok := routing.Lookup(ref); ok {
					t.Errorf("Lookup(%q) 不应命中", ref)
				}
			}
		})
	}
	for _, agent := range []string{config.ProviderCodex, config.ProviderPi} {
		t.Run(agent, func(t *testing.T) {
			setting.Mode = config.MagpieModeInject
			cfg.Providers.MagpieAgents[agent] = setting
			routing := ResolveAgentRouting(cfg, secrets, agent)
			if routing.Agent != agent || routing.Mode != config.MagpieModeInject || !routing.Active() || len(routing.Models) != 5 {
				t.Errorf("路由 = %+v", routing)
			}
		})
	}

	for _, tc := range []struct {
		name       string
		endpointID string
		modelID    string
		wantIndex  int
	}{
		{"有效默认值", "second", "shared", 3},
		{"模型未勾选", "second", "missing", 0},
		{"端点无密钥", "nokey", "no-key-model", 0},
		{"端点已禁用", "disabled", "disabled-model", 0},
		{"端点已删除", "deleted", "deleted-model", 0},
		{"默认值不完整", "second", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setting.DefaultEndpointID, setting.DefaultModel = tc.endpointID, tc.modelID
			cfg.Providers.MagpieAgents[config.ProviderClaude] = setting
			routing := ResolveAgentRouting(cfg, secrets, config.ProviderClaude)
			if routing.Default != routing.Models[tc.wantIndex] {
				t.Errorf("默认模型 = %+v，想要 %+v", routing.Default, routing.Models[tc.wantIndex])
			}
		})
	}

	t.Run("返回值独立", func(t *testing.T) {
		cfg.Providers.MagpieAgents[config.ProviderClaude] = setting
		routing := ResolveAgentRouting(cfg, secrets, config.ProviderClaude)
		routing.Models[0].ModelID = "changed"
		routing.Targets[0].Endpoint = "changed"
		if cfg.Providers.Custom[0].ID != "first" || cfg.Providers.MagpieAgents[config.ProviderClaude].Selections[2].Models[0] != "shared" {
			t.Fatal("修改返回值影响了配置")
		}
	})

	t.Run("空勾选", func(t *testing.T) {
		cfg.Providers.MagpieAgents[config.ProviderClaude] = config.MagpieAgentConfig{Mode: config.MagpieModeInject}
		routing := ResolveAgentRouting(cfg, secrets, config.ProviderClaude)
		if routing.Active() || len(routing.Targets) != 0 || len(routing.Models) != 0 || routing.Default != (AgentRoutingModel{}) {
			t.Errorf("空勾选的路由 = %+v", routing)
		}
	})
	t.Run("没有可用模型", func(t *testing.T) {
		cfg.Providers.MagpieAgents[config.ProviderClaude] = config.MagpieAgentConfig{
			Mode: config.MagpieModeGlobal,
			Selections: []config.MagpieSelection{
				{EndpointID: "nokey", Models: []string{"model"}},
			},
		}
		routing := ResolveAgentRouting(cfg, secrets, config.ProviderClaude)
		if routing.Active() || len(routing.Targets) != 0 || len(routing.Models) != 0 || routing.Default != (AgentRoutingModel{}) {
			t.Errorf("没有可用模型的路由 = %+v", routing)
		}
	})

	for _, agent := range []string{config.ProviderClaude, config.ProviderCline, "unknown"} {
		t.Run("关闭或不支持/"+agent, func(t *testing.T) {
			setting.Mode = config.MagpieModeOff
			if agent != config.ProviderClaude {
				setting.Mode = config.MagpieModeGlobal
			}
			cfg.Providers.MagpieAgents[agent] = setting
			routing := ResolveAgentRouting(cfg, secrets, agent)
			if routing.Agent != agent || routing.Mode != config.MagpieModeOff || routing.Active() || len(routing.Models) != 0 || len(routing.Targets) != 0 || routing.Default != (AgentRoutingModel{}) {
				t.Errorf("关闭或不支持的路由 = %+v", routing)
			}
		})
	}
}
