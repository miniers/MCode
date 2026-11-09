package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
)

func TestWarmMagpieModelsRecoversPanic(t *testing.T) {
	previous := warmMagpieFetch
	t.Cleanup(func() { warmMagpieFetch = previous })
	called := make(chan struct{}, 2)
	warmMagpieFetch = func(context.Context, string, string) ([]magpie.Model, error) { called <- struct{}{}; panic("fetch") }
	target := magpieinject.Target{BaseURL: "panic-app", APIKey: "panic-key"}
	key := target.BaseURL + "\x00" + target.APIKey
	for i := 0; i < 2; i++ {
		warmMagpieModels(target)
		select { case <-called: case <-time.After(time.Second): t.Fatal("预热未执行") }
		deadline := time.Now().Add(time.Second)
		for {
			magpieWarm.Lock()
			running := magpieWarm.inFlight[key]
			magpieWarm.Unlock()
			if !running { break }
			if time.Now().After(deadline) { t.Fatal("预热状态未清除") }
			time.Sleep(time.Millisecond)
		}
	}
}

// agentMagpieTestEndpoint 返回一个启用的 Magpie 端点，BaseURL 故意带末尾斜杠，
// 用来验证路由保留配置原值、注入时才归一化。
func agentMagpieTestEndpoint() config.CustomEndpointConfig {
	return config.CustomEndpointConfig{
		ID:      "jp",
		Name:    "Magpie JP",
		BaseURL: "http://jp.example/v1/",
		Enabled: true,
		Magpie:  true,
	}
}

// saveAgentMagpieConfig 写盘并让 claude 注入读这份数据目录。
func saveAgentMagpieConfig(t *testing.T, cfg config.Config, secrets config.SecretStore) {
	t.Helper()
	dataDir := t.TempDir()
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("保存测试配置失败: %v", err)
	}
	setPiMagpieRuntime(dataDir, secrets)
}

// setClaudeMagpieSetting 直接写 claude 的 Magpie 设置（测试不经过设置页）。
func setClaudeMagpieSetting(cfg *config.Config, mode config.MagpieMode, endpointID string, models []string) {
	if cfg.Providers.MagpieAgents == nil {
		cfg.Providers.MagpieAgents = make(map[string]config.MagpieAgentConfig, 1)
	}
	cfg.Providers.MagpieAgents[config.ProviderClaude] = config.MagpieAgentConfig{
		Mode:       mode,
		Selections: []config.MagpieSelection{{EndpointID: endpointID, Models: models}},
	}
}

func TestClaudeMagpieRoutesSkipUnusableEndpoints(t *testing.T) {
	t.Run("停用端点", func(t *testing.T) {
		ep := agentMagpieTestEndpoint()
		ep.Enabled = false
		cfg := magpieTestConfig(ep)
		setClaudeMagpieSetting(&cfg, config.MagpieModeInject, ep.ID, []string{"auto/claude-sonnet-4.5"})
		saveAgentMagpieConfig(t, cfg, magpieTestSecrets(t, map[string]string{ep.ID: "gw-key"}))

		if routes := claudeMagpieRoutes(); len(routes) != 0 {
			t.Fatalf("停用端点不应生成路由，得到 %#v", routes)
		}
	})

	t.Run("没有 key", func(t *testing.T) {
		ep := agentMagpieTestEndpoint()
		cfg := magpieTestConfig(ep)
		setClaudeMagpieSetting(&cfg, config.MagpieModeInject, ep.ID, []string{"auto/claude-sonnet-4.5"})
		saveAgentMagpieConfig(t, cfg, magpieTestSecrets(t, nil))

		if routes := claudeMagpieRoutes(); len(routes) != 0 {
			t.Fatalf("没有 key 的端点不应生成路由，得到 %#v", routes)
		}
		if rows, _ := magpieCatalogRows(config.ProviderClaude); len(rows) != 0 {
			t.Fatalf("没有 key 的端点不应生成目录行，得到 %#v", rows)
		}
	})

	t.Run("global 模式", func(t *testing.T) {
		ep := agentMagpieTestEndpoint()
		cfg := magpieTestConfig(ep)
		setClaudeMagpieSetting(&cfg, config.MagpieModeGlobal, ep.ID, []string{"auto/claude-sonnet-4.5"})
		saveAgentMagpieConfig(t, cfg, magpieTestSecrets(t, map[string]string{ep.ID: "gw-key"}))

		if routes := claudeMagpieRoutes(); len(routes) != 0 {
			t.Fatalf("global 模式不应生成路由: %#v", routes)
		}
		rows, active := magpieCatalogRows(config.ProviderClaude)
		if !active || len(rows) != 1 || rows[0].ID != "auto/claude-sonnet-4.5" {
			t.Fatalf("global 目录不符: %#v", rows)
		}
	})
}

func TestClaudeMagpieRoutesUseEndpointConfig(t *testing.T) {
	ep := agentMagpieTestEndpoint()
	cfg := magpieTestConfig(ep)
	setClaudeMagpieSetting(&cfg, config.MagpieModeInject, ep.ID, []string{"auto/claude-sonnet-4.5"})
	saveAgentMagpieConfig(t, cfg, magpieTestSecrets(t, map[string]string{ep.ID: "gw-key"}))

	routes := claudeMagpieRoutes()
	if len(routes) != 1 {
		t.Fatalf("路由数 = %d，想要 1: %#v", len(routes), routes)
	}
	route := routes[0]
	if route.ProviderID != "magpie-jp" {
		t.Errorf("ProviderID = %q，想要 magpie-jp", route.ProviderID)
	}
	if route.BaseURL != ep.BaseURL {
		t.Errorf("BaseURL = %q，想要端点原值 %q", route.BaseURL, ep.BaseURL)
	}
	if route.APIKey != "gw-key" {
		t.Errorf("APIKey = %q，想要 gw-key", route.APIKey)
	}
	if len(route.Models) != 1 || route.Models[0] != "auto/claude-sonnet-4.5" {
		t.Errorf("Models = %#v，想要该端点的勾选", route.Models)
	}
}

func TestClaudeMagpieModelRows(t *testing.T) {
	ep := agentMagpieTestEndpoint()
	cfg := magpieTestConfig(ep)
	setClaudeMagpieSetting(&cfg, config.MagpieModeInject, ep.ID, []string{"auto/claude-sonnet-4.5", "gpt-x"})
	saveAgentMagpieConfig(t, cfg, magpieTestSecrets(t, map[string]string{ep.ID: "gw-key"}))

	rows, _ := magpieCatalogRows(config.ProviderClaude)
	if len(rows) != 2 {
		t.Fatalf("目录行数 = %d，想要 2: %#v", len(rows), rows)
	}
	if rows[0].ID != "magpie-jp/auto/claude-sonnet-4.5" {
		t.Errorf("第一行 ID = %q，想要带 provider 前缀", rows[0].ID)
	}
	if rows[1].ID != "magpie-jp/gpt-x" {
		t.Errorf("第二行 ID = %q，想要带 provider 前缀", rows[1].ID)
	}
	if rows[0].Name != "claude-sonnet-4.5 (auto)" {
		t.Errorf("显示名 = %q，想要纯模型名、不带端点名", rows[0].Name)
	}
	if rows[0].Detail == nil || !strings.Contains(*rows[0].Detail, "Magpie JP · Magpie") {
		t.Errorf("说明 = %v，想要带端点名与 Magpie", rows[0].Detail)
	}
}

// TestPiMagpieTargetsLegacyPiModels 覆盖只有 PiModels 的旧配置：
// 没有新的 pi 勾选字段时仍要带出旧模型，不能升级后一启动就丢。
func TestPiMagpieTargetsLegacyPiModels(t *testing.T) {
	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: "http://jp.example/v1/", Enabled: true, Magpie: true,
		PiModels: []string{"cline/gpt-x"},
	})
	targets := piMagpieTargets(cfg, magpieTestSecrets(t, map[string]string{"jp": "gw-key"}))
	if len(targets) != 1 {
		t.Fatalf("端点数 = %d，想要 1", len(targets))
	}
	if !targets[0].injectable() {
		t.Fatal("旧配置的 PiModels 应让端点可注入")
	}
	if len(targets[0].models) != 1 || targets[0].models[0] != "cline/gpt-x" {
		t.Fatalf("models = %#v，想要沿用 PiModels", targets[0].models)
	}
}

// TestSetPiMagpieModelsDoesNotResurrectUnchecked 回归 Normalize 把 PiModels 并回勾选的 bug：
// 先勾两个再改成一个，重新加载后必须只剩一个。
func TestSetPiMagpieModelsDoesNotResurrectUnchecked(t *testing.T) {
	dataDir := t.TempDir()
	cfg := magpieTestConfig(agentMagpieTestEndpoint())
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("保存测试配置失败: %v", err)
	}
	client := NewClient(nil)
	client.SetProviderStore(dataDir, config.NewMemorySecretStore())

	if err := client.SetPiMagpieModels("jp", []string{"m1", "m2"}); err != nil {
		t.Fatalf("第一次保存勾选失败: %v", err)
	}
	if err := client.SetPiMagpieModels("jp", []string{"m2"}); err != nil {
		t.Fatalf("第二次保存勾选失败: %v", err)
	}

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	setting := config.MagpieAgentSetting(loaded, config.ProviderPi)
	if len(setting.Selections) != 1 || len(setting.Selections[0].Models) != 1 || setting.Selections[0].Models[0] != "m2" {
		t.Fatalf("pi 勾选 = %#v，想要只剩 m2", setting.Selections)
	}
	if got := loaded.Providers.Custom[0].PiModels; len(got) != 1 || got[0] != "m2" {
		t.Fatalf("PiModels = %#v，想要只剩 m2", got)
	}
}

func TestAgentMagpieRoutesAndCatalogModes(t *testing.T) {
	first := agentMagpieTestEndpoint()
	second := config.CustomEndpointConfig{ID: "us", Name: "Magpie US", BaseURL: "http://us.example/v1", Enabled: true, Magpie: true}
	secrets := magpieTestSecrets(t, map[string]string{"jp": "jp-key", "us": "us-key"})
	for _, mode := range []config.MagpieMode{config.MagpieModeOff, config.MagpieModeInject, config.MagpieModeGlobal} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := magpieTestConfig(first, second)
			cfg.Providers.MagpieAgents = make(map[string]config.MagpieAgentConfig)
			for _, agent := range []string{config.ProviderClaude, config.ProviderCodex, config.ProviderPi} {
				cfg.Providers.MagpieAgents[agent] = config.MagpieAgentConfig{
					Mode: mode, Selections: []config.MagpieSelection{{EndpointID: "jp", Models: []string{"auto/a"}}, {EndpointID: "us", Models: []string{"auto/b"}}},
					DefaultEndpointID: "us", DefaultModel: "auto/b",
				}
			}
			saveAgentMagpieConfig(t, cfg, secrets)
			claudeRoutes, codexRoutes := claudeMagpieRoutes(), codexMagpieRoutes()
			if mode == config.MagpieModeInject {
				if len(claudeRoutes) != 2 || claudeRoutes[0].Default != "" || claudeRoutes[1].Default != "auto/b" {
					t.Fatalf("claude 默认路由不符: %#v", claudeRoutes)
				}
				if len(codexRoutes) != 2 || codexRoutes[0].Default != "" || codexRoutes[1].Default != "auto/b" || codexRoutes[1].APIKey != "us-key" {
					t.Fatalf("codex 默认路由不符: %#v", codexRoutes)
				}
			} else if len(claudeRoutes) != 0 || len(codexRoutes) != 0 {
				t.Fatalf("%s 不应注入: %#v %#v", mode, claudeRoutes, codexRoutes)
			}
			for _, agent := range []string{config.ProviderClaude, config.ProviderCodex, config.ProviderPi} {
				rows, active := magpieCatalogRows(agent)
				if mode == config.MagpieModeOff {
					if active || len(rows) != 0 {
						t.Fatalf("%s off 目录: %#v", agent, rows)
					}
					continue
				}
				want := "magpie-us/auto/b"
				if mode == config.MagpieModeGlobal && agent != config.ProviderPi {
					want = "auto/b"
				}
				if !active || len(rows) != 2 || rows[1].ID != want || rows[0].IsDefault || !rows[1].IsDefault {
					t.Fatalf("%s %s 目录不符: %#v", agent, mode, rows)
				}
			}
		})
	}
}

// agentMagpieLevelsServer 是一个只列出一个带思考档位模型的 magpie 网关假服务，
// 档位写在 supported_reasoning_levels 里，与原样返回的网关一致。
func agentMagpieLevelsServer(t *testing.T, id string, levels ...string) *httptest.Server {
	t.Helper()
	efforts := make([]string, 0, len(levels))
	for _, level := range levels {
		efforts = append(efforts, fmt.Sprintf(`{"effort":%q}`, level))
	}
	body := fmt.Sprintf(`{"data":[{"id":%q,"name":%q,"supported_reasoning_levels":[%s]}]}`, id, id, strings.Join(efforts, ","))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// saveAgentMagpieLevelsConfig 让 agent 勾选 httptest 端点上的一个模型。
func saveAgentMagpieLevelsConfig(t *testing.T, srv *httptest.Server, agent, modelID string) {
	t.Helper()
	ep := config.CustomEndpointConfig{ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true}
	cfg := magpieTestConfig(ep)
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{
		agent: {Mode: config.MagpieModeInject, Selections: []config.MagpieSelection{{EndpointID: ep.ID, Models: []string{modelID}}}},
	}
	saveAgentMagpieConfig(t, cfg, magpieTestSecrets(t, map[string]string{ep.ID: "gw-key"}))
}

// TestMagpieCatalogRowsFillGatewayLevels 校验缓存命中时勾选行带上该后端允许的档位。
func TestMagpieCatalogRowsFillGatewayLevels(t *testing.T) {
	srv := agentMagpieLevelsServer(t, "claude-sonnet-4.5", "none", "low", "high", "max")
	if _, err := magpie.FetchModels(context.Background(), srv.URL+"/v1", "gw-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}

	t.Run("claude", func(t *testing.T) {
		saveAgentMagpieLevelsConfig(t, srv, config.ProviderClaude, "claude-sonnet-4.5")
		rows, active := magpieCatalogRows(config.ProviderClaude)
		if !active || len(rows) != 1 {
			t.Fatalf("目录不符: %#v", rows)
		}
		if want := []string{"low", "high", "max"}; !reflect.DeepEqual(rows[0].Efforts, want) {
			t.Fatalf("Efforts = %#v，想要 %#v", rows[0].Efforts, want)
		}
	})

	t.Run("codex", func(t *testing.T) {
		saveAgentMagpieLevelsConfig(t, srv, config.ProviderCodex, "claude-sonnet-4.5")
		rows, active := magpieCatalogRows(config.ProviderCodex)
		if !active || len(rows) != 1 {
			t.Fatalf("目录不符: %#v", rows)
		}
		if want := []string{"low", "high"}; !reflect.DeepEqual(rows[0].Efforts, want) {
			t.Fatalf("Efforts = %#v，想要 %#v", rows[0].Efforts, want)
		}
	})

	t.Run("pi 不填档位", func(t *testing.T) {
		saveAgentMagpieLevelsConfig(t, srv, config.ProviderPi, "claude-sonnet-4.5")
		rows, active := magpieCatalogRows(config.ProviderPi)
		if !active || len(rows) != 1 {
			t.Fatalf("目录不符: %#v", rows)
		}
		if len(rows[0].Efforts) != 0 {
			t.Fatalf("pi 行不应带档位: %#v", rows[0].Efforts)
		}
	})
}

// TestMagpieCatalogRowsWarmColdCache 校验冷缓存时首次调用不阻塞、不带档位，
// 后台预热完成后再次调用能读到档位。
func TestMagpieCatalogRowsWarmColdCache(t *testing.T) {
	srv := agentMagpieLevelsServer(t, "claude-sonnet-4.5", "low", "high")
	saveAgentMagpieLevelsConfig(t, srv, config.ProviderClaude, "claude-sonnet-4.5")

	rows, active := magpieCatalogRows(config.ProviderClaude)
	if !active || len(rows) != 1 {
		t.Fatalf("目录不符: %#v", rows)
	}
	if len(rows[0].Efforts) != 0 {
		t.Fatalf("冷缓存首次调用不应带档位: %#v", rows[0].Efforts)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, _ = magpieCatalogRows(config.ProviderClaude)
		if len(rows) == 1 && reflect.DeepEqual(rows[0].Efforts, []string{"low", "high"}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("预热后仍未读到档位: %#v", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMagpieEffortsFor 覆盖档位过滤：丢弃 none/minimal、按后端白名单过滤、去重保序。
func TestMagpieEffortsFor(t *testing.T) {
	cases := []struct {
		name   string
		agent  string
		levels []string
		want   []string
	}{
		{"claude 丢弃 none", config.ProviderClaude, []string{"none", "low", "high", "max"}, []string{"low", "high", "max"}},
		{"codex 丢弃 max", config.ProviderCodex, []string{"low", "max", "xhigh"}, []string{"low", "xhigh"}},
		{"丢弃 minimal", config.ProviderClaude, []string{"minimal", "medium"}, []string{"medium"}},
		{"去重保序", config.ProviderClaude, []string{"high", "low", "high", "medium"}, []string{"high", "low", "medium"}},
		{"pi 允许 max", config.ProviderPi, []string{"none", "high", "max"}, []string{"high", "max"}},
		{"未知 agent", "other", []string{"low"}, nil},
		{"全部被过滤", config.ProviderCodex, []string{"none", "minimal"}, nil},
		{"没有档位", config.ProviderClaude, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := magpieEffortsFor(tc.agent, tc.levels); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("magpieEffortsFor(%q, %#v) = %#v，想要 %#v", tc.agent, tc.levels, got, tc.want)
			}
		})
	}
}
