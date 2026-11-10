package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/config"
	"mcode/internal/magpiecfg"
	mcodeui "mcode/internal/ui"
)

// agentMagpieModelsServer 是一个只列出给定模型的 magpie 网关假服务。
func agentMagpieModelsServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf(`{"id":%q,"name":%q}`, id, id))
	}
	body := `{"data":[` + strings.Join(rows, ",") + `]}`
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

type agentMagpieTestEnv struct {
	client         *Client
	dataDir        string
	claudeDir      string
	codexDir       string
	clineDir       string
	piDir          string
	settings       string
	clineProviders string
	clineModels    string
}

// newAgentMagpieTestEnv 准备临时数据目录与临时 claude / codex / cline 配置目录，并把一个
// 带 key 的 Magpie 端点写进配置。CLAUDE_CONFIG_DIR、CODEX_HOME 与 CLINE_DATA_DIR 都指向临时
// 目录，测试不会碰到真实配置。
func newAgentMagpieTestEnv(t *testing.T, baseURL string) agentMagpieTestEnv {
	t.Helper()
	dataDir := t.TempDir()
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	clineDir := t.TempDir()
	piDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexDir)
	t.Setenv("CLINE_DATA_DIR", clineDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", piDir)

	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{{
		ID:      "jp",
		Name:    "Magpie JP",
		BaseURL: baseURL + "/v1",
		Enabled: true,
		Magpie:  true,
	}}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(config.CustomEndpointSecretAccount("jp"), "gw-key"); err != nil {
		t.Fatalf("写入端点密钥失败: %v", err)
	}
	client := NewClient(nil)
	client.SetProviderStore(dataDir, secrets)
	return agentMagpieTestEnv{
		client:         client,
		dataDir:        dataDir,
		claudeDir:      claudeDir,
		codexDir:       codexDir,
		clineDir:       clineDir,
		piDir:          piDir,
		settings:       filepath.Join(claudeDir, "settings.json"),
		clineProviders: filepath.Join(clineDir, "settings", "providers.json"),
		clineModels:    filepath.Join(clineDir, "settings", "models.json"),
	}
}

func agentMagpieMode(t *testing.T, dataDir, agent string) config.MagpieMode {
	t.Helper()
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	return config.MagpieAgentSetting(cfg, agent).Mode
}

func readAgentMagpieSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 %s 失败: %v\n%s", path, err, raw)
	}
	return doc
}

func writeAgentMagpieSettings(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("编码配置失败: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

func agentMagpieSettingsEnv(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	env, ok := doc["env"].(map[string]any)
	if !ok {
		t.Fatalf("settings.json 缺少 env 对象: %#v", doc)
	}
	return env
}

func agentMagpieSettingsModel(t *testing.T, path string) string {
	t.Helper()
	model, _ := readAgentMagpieSettings(t, path)["model"].(string)
	return model
}

func TestAgentMagpieOffRoundTrip(t *testing.T) {
	srv := agentMagpieModelsServer(t, "model")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()
	for _, agent := range []string{"claude", "codex", "cline", "pi"} {
		want := config.MagpieModeOff
		if got := config.MagpieAgentSetting(config.Config{}, agent).Mode; got != want {
			t.Errorf("%s 空模式 = %q，想要 %q", agent, got, want)
		}
		cfg := config.Config{}
		config.SetMagpieAgentMode(&cfg, agent, "invalid")
		if got := config.MagpieAgentSetting(cfg, agent).Mode; got != config.MagpieModeOff {
			t.Errorf("%s 非法模式 = %q，想要 off", agent, got)
		}
		if err := env.client.SetAgentMagpieMode(ctx, agent, "off"); err != nil {
			t.Fatalf("%s 切 off: %v", agent, err)
		}
		if got := agentMagpieMode(t, env.dataDir, agent); got != config.MagpieModeOff {
			t.Fatalf("%s 模式 = %q，想要 off", agent, got)
		}
		if err := env.client.SetAgentMagpieModels(ctx, agent, "jp", []string{"model"}); err != nil {
			t.Fatalf("%s 保存勾选: %v", agent, err)
		}
		if err := env.client.SetAgentMagpieDefaultModel(ctx, agent, "jp", "model"); err != nil {
			t.Fatalf("%s 保存默认模型: %v", agent, err)
		}
		got, err := env.client.AgentMagpie(ctx, agent)
		if err != nil || got.Mode != "off" || len(got.Endpoints) != 1 || !reflect.DeepEqual(got.Endpoints[0].Selected, []string{"model"}) || got.DefaultModel != "model" {
			t.Fatalf("%s 读取设置 = %+v，错误 = %v", agent, got, err)
		}
	}
	setPiMagpieRuntime(env.dataDir, env.client.secrets)
	t.Cleanup(func() { setPiMagpieRuntime("", nil) })
	rows, _ := magpieCatalogRows(config.ProviderClaude)
	if len(claudeMagpieRoutes()) != 0 || len(rows) != 0 {
		t.Fatal("claude off 不应生成路由或模型目录")
	}
}

// 内置引擎列没有模式设置，只读写自己的勾选；清空后不回退到 pi。
func TestAgentMagpieMcodeSelectionRoundTrip(t *testing.T) {
	srv := agentMagpieModelsServer(t, "sonnet-model", "opus-model")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieModels(ctx, config.ProviderPi, "jp", []string{"opus-model"}); err != nil {
		t.Fatal(err)
	}
	// 第一次读取沿用 pi 的勾选。
	got, err := env.client.AgentMagpie(ctx, config.ProviderMcode)
	if err != nil || len(got.Endpoints) != 1 || !reflect.DeepEqual(got.Endpoints[0].Selected, []string{"opus-model"}) {
		t.Fatalf("mcode 初始设置 = %+v，错误 = %v", got, err)
	}
	if got.Mode != "inject" || got.Global.Supported {
		t.Fatalf("mcode 模式 = %q，global = %v", got.Mode, got.Global.Supported)
	}

	if err := env.client.SetAgentMagpieModels(ctx, config.ProviderMcode, "jp", []string{"opus-model", "sonnet-model"}); err != nil {
		t.Fatal(err)
	}
	if err := env.client.SetAgentMagpieDefaultModel(ctx, config.ProviderMcode, "jp", "sonnet-model"); err != nil {
		t.Fatal(err)
	}
	got, err = env.client.AgentMagpie(ctx, config.ProviderMcode)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Endpoints[0].Selected, []string{"opus-model", "sonnet-model"}) || got.DefaultEndpointID != "jp" || got.DefaultModel != "sonnet-model" {
		t.Fatalf("mcode 读取设置 = %+v", got)
	}
	piSetting := config.MagpieAgentSetting(mustLoadAgentMagpieConfig(t, env.dataDir), config.ProviderPi)
	if !reflect.DeepEqual(piSetting.Selections, []config.MagpieSelection{{EndpointID: "jp", Models: []string{"opus-model"}}}) {
		t.Fatalf("pi 的勾选被影响 = %+v", piSetting.Selections)
	}
	if got := mustLoadAgentMagpieConfig(t, env.dataDir).Providers.Custom[0].PiModels; !reflect.DeepEqual(got, []string{"opus-model"}) {
		t.Fatalf("mcode 的写入被镜像到 piModels = %+v", got)
	}

	if err := env.client.SetAgentMagpieMode(ctx, config.ProviderMcode, "off"); err == nil || !strings.Contains(err.Error(), "内置引擎没有模式设置") {
		t.Fatalf("mcode 设置模式错误 = %v", err)
	}
	if err := env.client.AgentMagpieGlobalAction(ctx, config.ProviderMcode, "keep"); err == nil || !strings.Contains(err.Error(), "内置引擎没有模式设置") {
		t.Fatalf("mcode 全局操作错误 = %v", err)
	}

	// 清空勾选后 mcode 键保留，不回到 pi 的勾选。
	if err := env.client.SetAgentMagpieModels(ctx, config.ProviderMcode, "jp", nil); err != nil {
		t.Fatal(err)
	}
	got, err = env.client.AgentMagpie(ctx, config.ProviderMcode)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Endpoints[0].Selected) != 0 {
		t.Fatalf("清空后 mcode 的勾选 = %+v", got.Endpoints[0].Selected)
	}
	if _, ok := mustLoadAgentMagpieConfig(t, env.dataDir).Providers.MagpieAgents[config.ProviderMcode]; !ok {
		t.Fatal("清空后 mcode 键不应被删除")
	}
}

func mustLoadAgentMagpieConfig(t *testing.T, dataDir string) config.Config {
	t.Helper()
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAgentMagpieOffGlobalRestore(t *testing.T) {
	srv := agentMagpieModelsServer(t, "model", "other")
	for _, scenario := range []string{"restore", "failure", "keep", "no-reapply"} {
		t.Run(scenario, func(t *testing.T) {
			env := newAgentMagpieTestEnv(t, srv.URL)
			ctx := context.Background()
			writeAgentMagpieSettings(t, env.settings, map[string]any{"model": "original"})
			if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"model"}); err != nil {
				t.Fatal(err)
			}
			if err := env.client.SetAgentMagpieMode(ctx, "claude", "off"); err != nil {
				t.Fatal(err)
			}
			if err := env.client.SetAgentMagpieMode(ctx, "claude", "global"); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "failure":
				if err := os.WriteFile(env.settings, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := env.client.SetAgentMagpieMode(ctx, "claude", "off"); err == nil {
					t.Fatal("恢复失败应返回错误")
				}
				if got := agentMagpieMode(t, env.dataDir, "claude"); got != config.MagpieModeGlobal {
					t.Fatalf("恢复失败后模式 = %q", got)
				}
				return
			case "keep":
				if err := env.client.AgentMagpieGlobalAction(ctx, "claude", "keep"); err != nil {
					t.Fatal(err)
				}
			case "no-reapply":
				if err := agentMagpieSaveMode(env.dataDir, "claude", config.MagpieModeOff); err != nil {
					t.Fatal(err)
				}
				before := readAgentMagpieBytes(t, env.settings)
				if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"other"}); err != nil {
					t.Fatal(err)
				}
				if err := env.client.SetAgentMagpieDefaultModel(ctx, "claude", "jp", "other"); err != nil {
					t.Fatal(err)
				}
				if got := readAgentMagpieBytes(t, env.settings); got != before {
					t.Fatal("off 修改模型不应重写全局配置")
				}
				if err := env.client.SetAgentMagpieMode(ctx, "claude", "inject"); err != nil {
					t.Fatal(err)
				}
				if got := readAgentMagpieBytes(t, env.settings); got != before {
					t.Fatal("off 切 inject 不应重写全局配置")
				}
				return
			}
			doc := readAgentMagpieSettings(t, env.settings)
			doc["user"] = "changed"
			writeAgentMagpieSettings(t, env.settings, doc)
			if err := env.client.SetAgentMagpieMode(ctx, "claude", "off"); err != nil {
				t.Fatal(err)
			}
			got := readAgentMagpieSettings(t, env.settings)
			if got["user"] != "changed" || (scenario == "restore" && got["model"] != "original") || (scenario == "keep" && got["model"] != "model") {
				t.Fatalf("恢复后的配置 = %#v", got)
			}
			if mode := agentMagpieMode(t, env.dataDir, "claude"); mode != config.MagpieModeOff {
				t.Fatalf("模式 = %q，想要 off", mode)
			}
		})
	}
}

func TestAgentMagpieOffPiInjection(t *testing.T) {
	srv := agentMagpieModelsServer(t, "model")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()
	if err := env.client.SetPiMagpieModels("jp", []string{"model"}); err != nil {
		t.Fatal(err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "off"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(env.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := buildPiMagpieEnv(ctx, cfg, env.client.secrets); got != "" {
		t.Fatalf("pi off 注入 = %q", got)
	}
	endpoints, err := env.client.PiMagpieEndpoints(ctx)
	if err != nil || len(endpoints) != 1 || !reflect.DeepEqual(endpoints[0].Selected, []string{"model"}) {
		t.Fatalf("pi 端点 = %+v，错误 = %v", endpoints, err)
	}
}

func TestRefreshPiMagpieEndpointsBypassesCache(t *testing.T) {
	var added atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if added.Load() {
			_, _ = io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"a"}]}`)
	}))
	t.Cleanup(srv.Close)
	env := newAgentMagpieTestEnv(t, srv.URL)
	if err := env.client.secrets.Set(config.CustomEndpointSecretAccount("jp"), "key-refresh-pi-endpoints"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func(endpoints []mcodeui.PiMagpieEndpoint, err error, want ...string) {
		t.Helper()
		if err != nil || len(endpoints) != 1 || endpoints[0].Err != "" {
			t.Fatalf("端点 = %+v，错误 = %v", endpoints, err)
		}
		var ids []string
		for _, model := range endpoints[0].Models {
			ids = append(ids, model.ID)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("模型 = %v，想要 %v", ids, want)
		}
	}
	endpoints, err := env.client.PiMagpieEndpoints(ctx)
	check(endpoints, err, "a")
	added.Store(true)
	endpoints, err = env.client.PiMagpieEndpoints(ctx)
	check(endpoints, err, "a")
	endpoints, err = env.client.RefreshPiMagpieEndpoints(ctx)
	check(endpoints, err, "a", "b")
	endpoints, err = env.client.PiMagpieEndpoints(ctx)
	check(endpoints, err, "a", "b")
}

// TestAgentMagpieEndpointSelection 校验读取接口返回端点、可选模型与已勾选模型，
// 以及 agent 为空时报错。
func TestAgentMagpieEndpointSelection(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-sonnet-4.5", "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if _, err := env.client.AgentMagpie(ctx, "  "); err == nil {
		t.Fatal("agent 为空时应报错")
	}

	got, err := env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if got.Agent != "claude" || got.Mode != string(config.MagpieModeOff) {
		t.Errorf("setting = %+v，想要 claude / off", got)
	}
	if !got.Global.Supported {
		t.Error("claude 应支持 global")
	}
	if got.Global.State != "none" {
		t.Errorf("初始 Global.State = %q，想要 none", got.Global.State)
	}
	if got.Global.Path != env.settings {
		t.Errorf("Global.Path = %q，想要 %q", got.Global.Path, env.settings)
	}
	if len(got.Endpoints) != 1 {
		t.Fatalf("端点数 = %d，想要 1: %+v", len(got.Endpoints), got.Endpoints)
	}
	ep := got.Endpoints[0]
	if ep.Name != "Magpie JP" || ep.ProviderID != "magpie-jp" {
		t.Errorf("端点 = %+v，想要 Magpie JP / magpie-jp", ep)
	}
	if len(ep.Models) != 2 || ep.Models[0].ID != "claude-sonnet-4.5" || ep.Models[1].ID != "claude-opus-4.5" {
		t.Errorf("模型目录 = %+v，想要网关列出的两个模型", ep.Models)
	}
	if len(ep.Selected) != 0 {
		t.Errorf("初始 Selected = %v，想要空", ep.Selected)
	}

	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	got, err = env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if !reflect.DeepEqual(got.Endpoints[0].Selected, []string{"claude-opus-4.5"}) {
		t.Errorf("勾选后 Selected = %v，想要 [claude-opus-4.5]", got.Endpoints[0].Selected)
	}
}

// TestAgentMagpieModeGuards 校验非法模式、没有勾选时切 global、不支持 global 的
// agent 都在写配置之前被拦住。
func TestAgentMagpieModeGuards(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieMode(ctx, "claude", "sometimes"); err == nil {
		t.Fatal("非法模式应报错")
	}
	if err := env.client.SetAgentMagpieMode(ctx, "claude", "global"); err == nil {
		t.Fatal("没有勾选模型时应拒绝切 global")
	}
	if mode := agentMagpieMode(t, env.dataDir, "claude"); mode != config.MagpieModeOff {
		t.Errorf("失败后 claude 模式 = %q，想要 off", mode)
	}
	if _, err := os.Stat(env.settings); !os.IsNotExist(err) {
		t.Fatalf("失败后 settings.json 不应被创建: %v", err)
	}

	if err := env.client.SetAgentMagpieMode(ctx, "omp", "global"); err == nil {
		t.Fatal("omp 不支持 global，应报错")
	}
	if mode := agentMagpieMode(t, env.dataDir, "omp"); mode != config.MagpieModeOff {
		t.Errorf("omp 模式 = %q，想要 off", mode)
	}

	// codex 支持 global，但没有勾选模型时仍然被拦住，也不会创建 config.toml。
	if err := env.client.SetAgentMagpieMode(ctx, "codex", "global"); err == nil {
		t.Fatal("codex 没有勾选模型时应拒绝切 global")
	}
	if mode := agentMagpieMode(t, env.dataDir, "codex"); mode != config.MagpieModeOff {
		t.Errorf("codex 模式 = %q，想要 off", mode)
	}
	if _, err := os.Stat(filepath.Join(env.codexDir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("失败后 config.toml 不应被创建: %v", err)
	}
}

// TestAgentMagpieGlobalLifecycle 校验 global 的完整流程：写入、换模型后自动重写、
// 手改后 drift、reapply 恢复、切回 inject 后原配置逐值还原。
func TestAgentMagpieGlobalLifecycle(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-sonnet-4.5", "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	// 用户原本的配置；connect 后应被接管，disconnect 后应逐值回来。
	original := map[string]any{
		"model": "original-model",
		"env": map[string]any{
			"NO_PROXY":        "corp",
			"ANTHROPIC_MODEL": "stale-model",
		},
	}
	writeAgentMagpieSettings(t, env.settings, original)

	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "claude", "global"); err != nil {
		t.Fatalf("切 global 失败: %v", err)
	}

	doc := readAgentMagpieSettings(t, env.settings)
	settingsEnv := agentMagpieSettingsEnv(t, doc)
	if got := settingsEnv["ANTHROPIC_BASE_URL"]; got != srv.URL {
		t.Errorf("ANTHROPIC_BASE_URL = %v，想要去掉 /v1 的 %q", got, srv.URL)
	}
	if got := settingsEnv["ANTHROPIC_AUTH_TOKEN"]; got != "gw-key" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %v，想要端点的 key", got)
	}
	if got := doc["model"]; got != "claude-opus-4-5" {
		// 网关只服务点号形式，Claude Code 只认横线形式，写配置时要换成横线。
		t.Errorf("model = %v，想要 Claude Code 读得懂的 claude-opus-4-5", got)
	}
	if _, ok := settingsEnv["ANTHROPIC_MODEL"]; ok {
		t.Error("env.ANTHROPIC_MODEL 应被删除")
	}
	if _, ok := settingsEnv["NO_PROXY"]; !ok {
		t.Error("env.NO_PROXY 应被合并写入")
	}
	if got := settingsEnv["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "claude-opus-4-5" {
		t.Errorf("opus 档 = %v，想要 claude-opus-4-5", got)
	}
	if got := settingsEnv["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "claude-opus-4-5" {
		t.Errorf("haiku 档没有同前缀模型时应跟随主模型，得到 %v", got)
	}
	if _, ok := settingsEnv["ANTHROPIC_DEFAULT_FABLE_MODEL"]; ok {
		t.Error("非 Fable 主模型时 fable 档应被删除")
	}
	picker, ok := doc["modelPicker"].(map[string]any)
	if !ok {
		t.Fatalf("settings.json 缺少 modelPicker: %#v", doc)
	}
	options, _ := picker["options"].([]any)
	if len(options) != 1 {
		t.Fatalf("picker 行数 = %d，想要 1", len(options))
	}
	row, _ := options[0].(map[string]any)
	if row["model"] != "claude-opus-4-5" || row["label"] != "claude-opus-4.5" {
		t.Errorf("picker 行 = %#v，想要横线 model 与网关显示名", row)
	}

	setting, err := env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.Mode != string(config.MagpieModeGlobal) || setting.Global.State != "applied" {
		t.Fatalf("setting = %+v，想要 global / applied", setting)
	}

	// global 模式换勾选：新模型应重新写进 settings.json。
	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-sonnet-4.5"}); err != nil {
		t.Fatalf("global 模式下换勾选失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-sonnet-4-5" {
		t.Errorf("换勾选后 model = %q，想要 claude-sonnet-4-5", got)
	}

	// 手改 model 后进入 drifted。
	doc = readAgentMagpieSettings(t, env.settings)
	doc["model"] = "user-edited"
	writeAgentMagpieSettings(t, env.settings, doc)
	setting, err = env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.Global.State != "drifted" || !reflect.DeepEqual(setting.Global.Drifted, []string{"model"}) {
		t.Fatalf("Global = %+v，想要 drifted / [model]", setting.Global)
	}

	if err := env.client.AgentMagpieGlobalAction(ctx, "claude", "reapply"); err != nil {
		t.Fatalf("reapply 失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-sonnet-4-5" {
		t.Errorf("reapply 后 model = %q，想要 claude-sonnet-4-5", got)
	}
	setting, err = env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.Global.State != "applied" {
		t.Errorf("reapply 后 State = %q，想要 applied", setting.Global.State)
	}

	// 切回 inject：按 stash 恢复 connect 前的配置。
	if err := env.client.SetAgentMagpieMode(ctx, "claude", "inject"); err != nil {
		t.Fatalf("切回 inject 失败: %v", err)
	}
	if mode := agentMagpieMode(t, env.dataDir, "claude"); mode != config.MagpieModeInject {
		t.Errorf("claude 模式 = %q，想要 inject", mode)
	}
	restored := readAgentMagpieSettings(t, env.settings)
	if !reflect.DeepEqual(restored, original) {
		t.Errorf("settings.json 未恢复原样\n得到: %#v\n想要: %#v", restored, original)
	}
	setting, err = env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.Global.State != "none" {
		t.Errorf("切回 inject 后 State = %q，想要 none", setting.Global.State)
	}
}

// TestAgentMagpieGlobalKeep 校验 keep 之后状态是 kept、reapply 被拒绝，
// 以及未知操作报错。
func TestAgentMagpieGlobalKeep(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "claude", "global"); err != nil {
		t.Fatalf("切 global 失败: %v", err)
	}
	if err := env.client.AgentMagpieGlobalAction(ctx, "claude", "keep"); err != nil {
		t.Fatalf("keep 失败: %v", err)
	}
	setting, err := env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.Global.State != "kept" {
		t.Errorf("keep 后 State = %q，想要 kept", setting.Global.State)
	}

	if err := env.client.AgentMagpieGlobalAction(ctx, "claude", "reapply"); err == nil {
		t.Fatal("kept 状态下 reapply 应报错")
	}
	setting, err = env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.Global.State != "kept" {
		t.Errorf("失败后 State = %q，想要 kept", setting.Global.State)
	}
	if err := env.client.AgentMagpieGlobalAction(ctx, "claude", "nope"); err == nil {
		t.Fatal("未知操作应报错")
	}
}

// agentMagpieCatalogModel 是假网关 /v1/models 返回的一行。
type agentMagpieCatalogModel struct {
	ID            string `json:"id"`
	Name          string `json:"display_name"`
	ContextWindow int    `json:"context_window"`
}

// agentMagpieCatalogServer 是一个返回给定模型元数据（含 contextWindow）的假网关。
func agentMagpieCatalogServer(t *testing.T, models ...agentMagpieCatalogModel) *httptest.Server {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"data": models})
	if err != nil {
		t.Fatalf("编码模型目录失败: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// agentMagpieFailingServer 是一个始终 500 的假网关，用来模拟取模型目录失败。
func agentMagpieFailingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func agentMagpiePlanField(t *testing.T, plan magpiecfg.Plan, path string) magpiecfg.Field {
	t.Helper()
	for _, f := range plan.Files[0].Fields {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("计划缺少字段 %s", path)
	return magpiecfg.Field{}
}

// agentMagpiePlanPickerRows 取出计划里 modelPicker.options 的行。
func agentMagpiePlanPickerRows(t *testing.T, plan magpiecfg.Plan) []map[string]any {
	t.Helper()
	f := agentMagpiePlanField(t, plan, "modelPicker")
	picker, ok := f.Value.(map[string]any)
	if !ok {
		t.Fatalf("modelPicker Value 类型 = %T，想要 map[string]any", f.Value)
	}
	raw, ok := picker["options"].([]any)
	if !ok {
		t.Fatalf("modelPicker.options 类型 = %T，想要 []any", picker["options"])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("modelPicker 行类型 = %T，想要 map[string]any", item)
		}
		rows = append(rows, row)
	}
	return rows
}

// TestAgentMagpieGlobalPlanCollectsSelections 校验 globalPlan 使用第一个既有
// key 又有勾选的端点，把全部勾选按顺序交给 ClaudePlan，并用网关元数据补
// label、上下文窗口与拼写集合。
func TestAgentMagpieGlobalPlanCollectsSelections(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	srv := agentMagpieCatalogServer(t,
		agentMagpieCatalogModel{ID: "auto/claude-sonnet-4.5", Name: "Claude Sonnet 4.5", ContextWindow: 200_000},
		agentMagpieCatalogModel{ID: "auto/claude-opus-4.5", Name: "Claude Opus 4.5", ContextWindow: 1_000_000},
		agentMagpieCatalogModel{ID: "auto/claude-haiku-4.5", Name: "Claude Haiku 4.5", ContextWindow: 200_000},
		agentMagpieCatalogModel{ID: "auto/deepseek-v3.2", Name: "DeepSeek V3.2"},
	)
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "nokey", Name: "没有 key", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true},
		{ID: "idle", Name: "没有勾选", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true},
		{ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true},
	}
	config.SetMagpieAgentModels(&cfg, "claude", "jp", []string{
		"auto/claude-sonnet-4.5",
		"auto/claude-opus-4.5",
		"auto/claude-haiku-4.5",
	})
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(config.CustomEndpointSecretAccount("idle"), "idle-key"); err != nil {
		t.Fatalf("写入端点密钥失败: %v", err)
	}
	if err := secrets.Set(config.CustomEndpointSecretAccount("jp"), "gw-key"); err != nil {
		t.Fatalf("写入端点密钥失败: %v", err)
	}

	plan, err := globalPlan(context.Background(), cfg, secrets, "claude")
	if err != nil {
		t.Fatalf("globalPlan 失败: %v", err)
	}
	if plan.Files[0].Path != filepath.Join(claudeDir, "settings.json") {
		t.Errorf("Path = %q，想要临时目录下的 settings.json", plan.Files[0].Path)
	}
	// 主模型是第一个勾选；网关只服务点号，写出去要是横线形式。
	if got := agentMagpiePlanField(t, plan, "model").Value; got != "auto/claude-sonnet-4-5" {
		t.Errorf("model = %v，想要 auto/claude-sonnet-4-5", got)
	}
	if got := agentMagpiePlanField(t, plan, "env.ANTHROPIC_SMALL_FAST_MODEL").Value; got != "auto/claude-haiku-4-5" {
		t.Errorf("SMALL_FAST = %v，想要 auto/claude-haiku-4-5", got)
	}
	if got := agentMagpiePlanField(t, plan, "env.ANTHROPIC_DEFAULT_OPUS_MODEL").Value; got != "auto/claude-opus-4-5[1m]" {
		t.Errorf("opus 档 = %v，想要勾选里的 opus 模型 auto/claude-opus-4-5[1m]", got)
	}

	rows := agentMagpiePlanPickerRows(t, plan)
	if len(rows) != 3 {
		t.Fatalf("picker 行数 = %d，想要 3", len(rows))
	}
	want := []struct{ model, label string }{
		{"auto/claude-sonnet-4-5", "Claude Sonnet 4.5"},
		{"auto/claude-opus-4-5[1m]", "Claude Opus 4.5"},
		{"auto/claude-haiku-4-5", "Claude Haiku 4.5"},
	}
	for i, w := range want {
		if rows[i]["model"] != w.model {
			t.Errorf("picker 第 %d 行 model = %v，想要 %q", i, rows[i]["model"], w.model)
		}
		if rows[i]["label"] != w.label {
			t.Errorf("picker 第 %d 行 label = %v，想要网关显示名 %q", i, rows[i]["label"], w.label)
		}
	}
}

// TestAgentMagpieGlobalPlanFetchFailure 校验取模型目录失败（500）时仍生成计划，
// label 退化为 ID 去前缀，且没有元数据就不加 [1m]。
func TestAgentMagpieGlobalPlanFetchFailure(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	srv := agentMagpieFailingServer(t)
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true},
	}
	config.SetMagpieAgentModels(&cfg, "claude", "jp", []string{
		"auto/claude-opus-5-5",
		"auto/claude-sonnet-5-5",
	})
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(config.CustomEndpointSecretAccount("jp"), "gw-key"); err != nil {
		t.Fatalf("写入端点密钥失败: %v", err)
	}

	plan, err := globalPlan(context.Background(), cfg, secrets, "claude")
	if err != nil {
		t.Fatalf("网关 500 时仍应生成计划，得到错误: %v", err)
	}
	if got := agentMagpiePlanField(t, plan, "model").Value; got != "auto/claude-opus-5-5" {
		t.Errorf("model = %v，想要 auto/claude-opus-5-5", got)
	}
	rows := agentMagpiePlanPickerRows(t, plan)
	if len(rows) != 2 {
		t.Fatalf("picker 行数 = %d，想要 2", len(rows))
	}
	if rows[0]["model"] != "auto/claude-opus-5-5" {
		t.Errorf("picker 第 0 行 model = %v，没有元数据时不应加 [1m]", rows[0]["model"])
	}
	if rows[0]["label"] != "claude-opus-5-5" {
		t.Errorf("picker 第 0 行 label = %v，想要去掉路由前缀的 ID", rows[0]["label"])
	}
}

// TestAgentMagpieCodexGlobalLifecycle 校验 codex 的 global 流程：写出计划里的
// config.toml、换模型后自动重写、切回 inject 后原文件逐字节还原。
func TestAgentMagpieCodexGlobalLifecycle(t *testing.T) {
	srv := agentMagpieModelsServer(t, "gpt-6.1-terra", "gpt-6.1-sol")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()
	path := filepath.Join(env.codexDir, "config.toml")

	original := "# 用户自己的 codex 配置\n" +
		`model = "gpt-5-codex"` + "\n" +
		`model_provider = "openai"` + "\n" +
		"\n" +
		"[model_providers.other]\n" +
		`name = "Other"` + "\n" +
		`base_url = "https://other.example/v1"` + "\n" +
		"\n" +
		"[other_table]\n" +
		"enabled = true\n" +
		"retries = 3\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("写入原始 config.toml 失败: %v", err)
	}

	if err := env.client.SetAgentMagpieModels(ctx, "codex", "jp", []string{"gpt-6.1-terra"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "codex", "global"); err != nil {
		t.Fatalf("切 codex global 失败: %v", err)
	}

	doc, err := magpiecfg.GetTOMLDocument(path)
	if err != nil {
		t.Fatalf("解析 config.toml 失败: %v", err)
	}
	for key, want := range map[string]any{
		"model":                                                  "gpt-6.1-terra",
		"model_provider":                                         magpiecfg.CodexProviderID,
		"model_providers.mcode-magpie.name":                      "Magpie",
		"model_providers.mcode-magpie.base_url":                  srv.URL + "/v1",
		"model_providers.mcode-magpie.wire_api":                  "responses",
		"model_providers.mcode-magpie.requires_openai_auth":      true,
		"model_providers.mcode-magpie.experimental_bearer_token": "gw-key",
	} {
		got, ok := magpiecfg.LookupTOMLValue(doc, key)
		if !ok || got != want {
			t.Errorf("%s = %#v（存在=%v），想要 %#v", key, got, ok, want)
		}
	}
	if got, ok := magpiecfg.LookupTOMLValue(doc, "model_providers.other.name"); !ok || got != "Other" {
		t.Errorf("其它 provider 表被改动: %#v（存在=%v）", got, ok)
	}

	got, err := env.client.AgentMagpie(ctx, "codex")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if !got.Global.Supported {
		t.Error("codex 应支持 global")
	}
	if got.Global.State != "applied" || len(got.Global.Drifted) != 0 {
		t.Errorf("Global = %+v，想要 applied 且无 drift", got.Global)
	}
	if got.Global.Path != path {
		t.Errorf("Global.Path = %q，想要 %q", got.Global.Path, path)
	}
	if len(got.Tiers) != 0 {
		t.Errorf("codex 不应有档位: %+v", got.Tiers)
	}

	// 换勾选后自动重写计划里的 model。
	if err := env.client.SetAgentMagpieModels(ctx, "codex", "jp", []string{"gpt-6.1-sol"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	doc, err = magpiecfg.GetTOMLDocument(path)
	if err != nil {
		t.Fatalf("解析 config.toml 失败: %v", err)
	}
	if got, _ := magpiecfg.LookupTOMLValue(doc, "model"); got != "gpt-6.1-sol" {
		t.Errorf("换勾选后 model = %#v，想要 gpt-6.1-sol", got)
	}

	// 切回 inject：原文件逐字节还原。
	if err := env.client.SetAgentMagpieMode(ctx, "codex", "inject"); err != nil {
		t.Fatalf("切回 inject 失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 config.toml 失败: %v", err)
	}
	if string(raw) != original {
		t.Fatalf("断开后未逐字节还原:\n得到 %q\n想要 %q", raw, original)
	}
}

// TestAgentMagpieCodexGlobalPlanRequiresSelection 校验 codex 没有可用端点/勾选
// 时不写 config.toml、直接报错。
func TestAgentMagpieCodexGlobalPlanRequiresSelection(t *testing.T) {
	codexDir := t.TempDir()
	t.Setenv("CODEX_HOME", codexDir)
	cfg := config.Defaults()
	secrets := config.NewMemorySecretStore()

	if _, err := globalPlan(context.Background(), cfg, secrets, "codex"); err == nil {
		t.Fatal("没有可用端点时应报错")
	}
	if _, err := os.Stat(filepath.Join(codexDir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("失败时不应写出 config.toml: %v", err)
	}
}

// TestAgentMagpieCodexTiersEmpty 校验 codex 不计算 claude 的档位。
func TestAgentMagpieCodexTiersEmpty(t *testing.T) {
	srv := agentMagpieModelsServer(t, "gpt-6.1-terra")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieModels(ctx, "codex", "jp", []string{"gpt-6.1-terra"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "codex", "global"); err != nil {
		t.Fatalf("切 codex global 失败: %v", err)
	}
	got, err := env.client.AgentMagpie(ctx, "codex")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if got.Global.State != "applied" {
		t.Errorf("Global = %+v，想要 applied", got.Global)
	}
	if len(got.Tiers) != 0 {
		t.Errorf("codex 不应有档位: %+v", got.Tiers)
	}
}

// TestAgentMagpieGlobalPlanRequiresSelection 校验没有勾选时不写文件、直接报错，
// 以及不支持的 agent 也被拦住。
func TestAgentMagpieGlobalPlanRequiresSelection(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	codexDir := t.TempDir()
	t.Setenv("CODEX_HOME", codexDir)
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "jp", Name: "Magpie JP", BaseURL: "http://jp.example/v1", Enabled: true, Magpie: true},
	}
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(config.CustomEndpointSecretAccount("jp"), "gw-key"); err != nil {
		t.Fatalf("写入端点密钥失败: %v", err)
	}

	if _, err := globalPlan(context.Background(), cfg, secrets, "claude"); err == nil {
		t.Fatal("没有勾选模型时应报错")
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("失败时不应写出 settings.json: %v", err)
	}
	if _, err := globalPlan(context.Background(), cfg, secrets, "codex"); err == nil {
		t.Fatal("codex 没有勾选模型时应报错")
	}
	if _, err := os.Stat(filepath.Join(codexDir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("失败时不应写出 codex 的 config.toml: %v", err)
	}
	if _, err := globalPlan(context.Background(), cfg, secrets, "omp"); err == nil {
		t.Fatal("omp 不支持写全局配置，应报错")
	}
}

// 用户指定的默认模型在 global 模式立即生效；清空后回到第一条勾选。
func TestAgentMagpieGlobalDefaultModel(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-sonnet-4.5", "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5", "claude-sonnet-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "claude", "global"); err != nil {
		t.Fatalf("切 global 失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-opus-4-5" {
		t.Fatalf("初始 model = %q，想要第一条勾选 claude-opus-4-5", got)
	}

	if err := env.client.SetAgentMagpieDefaultModel(ctx, "claude", "jp", "claude-sonnet-4.5"); err != nil {
		t.Fatalf("SetAgentMagpieDefaultModel 失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-sonnet-4-5" {
		t.Fatalf("设置默认后 model = %q，想要 claude-sonnet-4-5", got)
	}
	if got := agentMagpieSettingsEnv(t, readAgentMagpieSettings(t, env.settings))["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "claude-sonnet-4-5" {
		t.Fatalf("sonnet 档 = %v，想要跟随默认主模型", got)
	}
	setting, err := env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.DefaultEndpointID != "jp" || setting.DefaultModel != "claude-sonnet-4.5" {
		t.Fatalf("读接口默认模型 = %q/%q，想要 jp/claude-sonnet-4.5", setting.DefaultEndpointID, setting.DefaultModel)
	}

	// 清空默认模型：恢复为第一条勾选。
	if err := env.client.SetAgentMagpieDefaultModel(ctx, "claude", "", ""); err != nil {
		t.Fatalf("清空默认模型失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-opus-4-5" {
		t.Fatalf("清空后 model = %q，想要回到 claude-opus-4-5", got)
	}
}

// 默认模型失效（被取消勾选）时忽略它回到第一项，但配置值保留。
func TestAgentMagpieGlobalDefaultModelIgnoredWhenUnselected(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-sonnet-4.5", "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5", "claude-sonnet-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "claude", "global"); err != nil {
		t.Fatalf("切 global 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieDefaultModel(ctx, "claude", "jp", "claude-sonnet-4.5"); err != nil {
		t.Fatalf("SetAgentMagpieDefaultModel 失败: %v", err)
	}

	// 取消勾选默认模型：global 配置回到第一条，默认值仍在配置里。
	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-opus-4-5" {
		t.Fatalf("默认模型失效后 model = %q，想要回到 claude-opus-4-5", got)
	}
	setting, err := env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if setting.DefaultModel != "claude-sonnet-4.5" {
		t.Fatalf("配置里的默认值应保留，得到 %q", setting.DefaultModel)
	}

	// 重新勾选后自动恢复为默认主模型。
	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", []string{"claude-opus-4.5", "claude-sonnet-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if got := agentMagpieSettingsModel(t, env.settings); got != "claude-sonnet-4-5" {
		t.Fatalf("重新勾选后 model = %q，想要恢复 claude-sonnet-4-5", got)
	}
}

func TestAgentMagpieGlobalGatewayGuards(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-sonnet-4.5", "claude-opus-4.5")
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			env := newAgentMagpieTestEnv(t, srv.URL)
			addAgentMagpieTestGateway(t, env, srv.URL)
			ctx := context.Background()
			path := env.settings
			original := "{\"model\":\"original\"}"
			if agent == "codex" {
				path = filepath.Join(env.codexDir, "config.toml")
				original = "model = \"original\"\n"
			}
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"inject", "global"} {
				if err := env.client.SetAgentMagpieMode(ctx, agent, mode); err == nil || err.Error() != "请先在 Magpie 模型列表里为该智能体勾选模型" {
					t.Fatalf("空勾选切 %s 的错误 = %v", mode, err)
				}
			}
			for _, endpoint := range []string{"jp", "us"} {
				if err := env.client.SetAgentMagpieModels(ctx, agent, endpoint, []string{"claude-opus-4.5"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := env.client.SetAgentMagpieMode(ctx, agent, "global"); err == nil || err.Error() != "全局模式只能使用一个网关的模型，请先取消其它网关上的勾选" {
				t.Fatalf("多网关切 global 的错误 = %v", err)
			}
			if got := agentMagpieMode(t, env.dataDir, agent); got != config.MagpieModeOff {
				t.Fatalf("拒绝后模式 = %q", got)
			}
			if got := readAgentMagpieBytes(t, path); got != original {
				t.Fatalf("拒绝后配置发生变化: %q", got)
			}
			if err := env.client.SetAgentMagpieModels(ctx, agent, "us", nil); err != nil {
				t.Fatal(err)
			}
			if err := env.client.SetAgentMagpieMode(ctx, agent, "global"); err != nil {
				t.Fatal(err)
			}
			before := readAgentMagpieBytes(t, path)
			for _, test := range []struct {
				endpoint string
				models   []string
				want     string
			}{
				{"us", []string{"claude-sonnet-4.5"}, "全局模式只能使用一个网关的模型，请先取消其它网关上的勾选"},
				{"jp", nil, "全局模式下至少保留一个模型，或先切回直连"},
			} {
				if err := env.client.SetAgentMagpieModels(ctx, agent, test.endpoint, test.models); err == nil || err.Error() != test.want {
					t.Fatalf("保存 %s 的错误 = %v", test.endpoint, err)
				}
				cfg, err := config.Load(env.dataDir)
				if err != nil {
					t.Fatal(err)
				}
				setting := config.MagpieAgentSetting(cfg, agent)
				if !reflect.DeepEqual(agentMagpieSelectedModels(setting, "jp"), []string{"claude-opus-4.5"}) || len(agentMagpieSelectedModels(setting, "us")) != 0 || setting.Mode != config.MagpieModeGlobal {
					t.Fatalf("拒绝后设置发生变化: %+v", setting)
				}
				if got := readAgentMagpieBytes(t, path); got != before {
					t.Fatal("拒绝后全局配置发生变化")
				}
			}
		})
	}
}

func addAgentMagpieTestGateway(t *testing.T, env agentMagpieTestEnv, baseURL string) {
	t.Helper()
	cfg, err := config.Load(env.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers.Custom = append(cfg.Providers.Custom, config.CustomEndpointConfig{ID: "us", Name: "Magpie US", BaseURL: baseURL + "/v1", Enabled: true, Magpie: true})
	if err := config.Save(env.dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	secrets := config.NewMemorySecretStore()
	for _, endpoint := range []string{"jp", "us"} {
		if err := secrets.Set(config.CustomEndpointSecretAccount(endpoint), "gw-key"); err != nil {
			t.Fatal(err)
		}
	}
	env.client.SetProviderStore(env.dataDir, secrets)
}

func TestAgentMagpieGlobalSelectedModelsAndDefault(t *testing.T) {
	srv := agentMagpieModelsServer(t, "model-a", "model-b")
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			env := newAgentMagpieTestEnv(t, srv.URL)
			ctx := context.Background()
			if err := env.client.SetAgentMagpieModels(ctx, agent, "jp", []string{"model-a", "model-b"}); err != nil {
				t.Fatal(err)
			}
			if err := env.client.SetAgentMagpieDefaultModel(ctx, agent, "jp", "model-b"); err != nil {
				t.Fatal(err)
			}
			if err := env.client.SetAgentMagpieMode(ctx, agent, "global"); err != nil {
				t.Fatal(err)
			}
			if agent == "codex" {
				doc, err := magpiecfg.GetTOMLDocument(filepath.Join(env.codexDir, "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				if got, ok := magpiecfg.LookupTOMLValue(doc, "model"); !ok || got != "model-b" {
					t.Fatalf("codex 默认模型 = %v", got)
				}
				return
			}
			doc := readAgentMagpieSettings(t, env.settings)
			if doc["model"] != "model-b" {
				t.Fatalf("claude 默认模型 = %v", doc["model"])
			}
			picker, ok := doc["modelPicker"].(map[string]any)
			if !ok {
				t.Fatalf("缺少模型选择器: %#v", doc)
			}
			options, ok := picker["options"].([]any)
			if !ok || len(options) != 2 {
				t.Fatalf("模型选择器 = %#v", picker)
			}
			for i, want := range []string{"model-a", "model-b"} {
				row, ok := options[i].(map[string]any)
				if !ok || row["model"] != want {
					t.Fatalf("模型选择器第 %d 项 = %#v", i, options[i])
				}
			}
		})
	}
}

func TestAgentMagpiePiGlobalMultipleGateways(t *testing.T) {
	srv := agentMagpieModelsServer(t, "model")
	env := newAgentMagpieTestEnv(t, srv.URL)
	addAgentMagpieTestGateway(t, env, srv.URL)
	ctx := context.Background()
	for _, endpoint := range []string{"jp", "us"} {
		if err := env.client.SetAgentMagpieModels(ctx, "pi", endpoint, []string{"model"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "global"); err != nil {
		t.Fatal(err)
	}
	if got := agentMagpieMode(t, env.dataDir, "pi"); got != config.MagpieModeGlobal {
		t.Fatalf("pi 模式 = %q", got)
	}
	doc := readAgentMagpieSettings(t, filepath.Join(env.piDir, "models.json"))
	providers, ok := doc["providers"].(map[string]any)
	if !ok || providers["magpie-jp"] == nil || providers["magpie-us"] == nil {
		t.Fatalf("pi 网关 = %#v", doc["providers"])
	}
}

func TestAgentMagpieModeRequiresAvailableModel(t *testing.T) {
	env := newAgentMagpieTestEnv(t, "https://unused.test")
	env.client.SetProviderStore(env.dataDir, config.NewMemorySecretStore())
	ctx := context.Background()
	for _, agent := range []string{"claude", "codex", "pi"} {
		if err := env.client.SetAgentMagpieModels(ctx, agent, "jp", []string{"model"}); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"inject", "global"} {
			if err := env.client.SetAgentMagpieMode(ctx, agent, mode); err == nil || err.Error() != "请先在 Magpie 模型列表里为该智能体勾选模型" {
				t.Fatalf("%s 缺少密钥时切 %s 的错误 = %v", agent, mode, err)
			}
		}
	}
}

// 勾了同系列模型时 Tiers 按 opus、sonnet、haiku、fable 顺序给出各档指向的
// 模型，fable 档在非 Fable 主模型上留空。
func TestAgentMagpieTiers(t *testing.T) {
	srv := agentMagpieModelsServer(t, "auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-haiku-5-5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	models := []string{"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-haiku-5-5"}
	if err := env.client.SetAgentMagpieModels(ctx, "claude", "jp", models); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieDefaultModel(ctx, "claude", "jp", "auto/claude-opus-5-5"); err != nil {
		t.Fatalf("SetAgentMagpieDefaultModel 失败: %v", err)
	}

	got, err := env.client.AgentMagpie(ctx, "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	want := []mcodeui.AgentMagpieTier{
		{Tier: "opus", EndpointID: "jp", ModelID: "auto/claude-opus-5-5"},
		{Tier: "sonnet", EndpointID: "jp", ModelID: "auto/claude-sonnet-5-5"},
		{Tier: "haiku", EndpointID: "jp", ModelID: "auto/claude-haiku-5-5"},
		{Tier: "fable", EndpointID: "jp", ModelID: ""},
	}
	if !reflect.DeepEqual(got.Tiers, want) {
		t.Fatalf("Tiers = %+v，想要 %+v", got.Tiers, want)
	}
}

// 没有任何勾选时没有可用端点，Tiers 为空。
func TestAgentMagpieTiersWithoutSelection(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)

	got, err := env.client.AgentMagpie(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if len(got.Tiers) != 0 {
		t.Fatalf("没勾选时 Tiers = %+v，想要空", got.Tiers)
	}
}

// 不支持写全局配置的 agent 没有档位映射。
func TestAgentMagpieTiersUnsupportedAgent(t *testing.T) {
	srv := agentMagpieModelsServer(t, "claude-opus-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	if err := env.client.SetAgentMagpieModels(ctx, "pi", "jp", []string{"claude-opus-4.5"}); err != nil {
		t.Fatalf("SetAgentMagpieModels 失败: %v", err)
	}
	got, err := env.client.AgentMagpie(ctx, "pi")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if len(got.Tiers) != 0 {
		t.Fatalf("pi 的 Tiers = %+v，想要空", got.Tiers)
	}
}

// readAgentMagpieBytes 读取文件原始字节，用来断言逐字节还原。
func readAgentMagpieBytes(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(raw)
}

func TestAgentMagpieClineOnlyDirect(t *testing.T) {
	env := newAgentMagpieTestEnv(t, "https://unused.test")
	ctx := context.Background()
	if err := env.client.SetAgentMagpieMode(ctx, "cline", "global"); err == nil {
		t.Fatal("cline 不应支持 global")
	}
	if err := env.client.SetAgentMagpieMode(ctx, "cline", "inject"); err != nil {
		t.Fatal(err)
	}
	if got := agentMagpieMode(t, env.dataDir, "cline"); got != config.MagpieModeOff {
		t.Fatalf("cline 模式 = %q，想要 off", got)
	}
}

func TestAgentMagpieClineGlobalPlan(t *testing.T) {
	if _, err := globalPlan(context.Background(), config.Config{}, nil, "cline"); err == nil {
		t.Fatal("cline 不应支持全局配置")
	}
}
