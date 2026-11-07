package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/config"
)

// piProviderDoc 返回 pi 写入的 models.json 里某个 provider 的对象。
func piProviderDoc(t *testing.T, path, providerID string) map[string]any {
	t.Helper()
	doc := readAgentMagpieSettings(t, path)
	providers, ok := doc["providers"].(map[string]any)
	if !ok {
		t.Fatalf("models.json 缺少 providers: %#v", doc)
	}
	provider, ok := providers[providerID].(map[string]any)
	if !ok {
		t.Fatalf("models.json 缺少 providers.%s: %#v", providerID, providers)
	}
	return provider
}

// piModelIDs 返回 pi provider 的模型 ID 列表。
func piModelIDs(t *testing.T, provider map[string]any) []string {
	t.Helper()
	rows, ok := provider["models"].([]any)
	if !ok {
		t.Fatalf("provider.models 类型 = %T", provider["models"])
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		m, _ := row.(map[string]any)
		id, _ := m["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// TestAgentMagpiePiGlobalLifecycle 校验 pi 的 global 流程：写 models.json 与
// settings.json、勾选与默认模型变化自动重写、切回 inject / off 恢复原文件。
func TestAgentMagpiePiGlobalLifecycle(t *testing.T) {
	srv := agentMagpieModelsServer(t, "glm-4.6", "glm-4.5")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()

	modelsPath := filepath.Join(env.piDir, "models.json")
	settingsPath := filepath.Join(env.piDir, "settings.json")
	modelsOrig := `{"providers":{"anthropic":{"baseUrl":"https://api.anthropic.com","models":[{"id":"claude-sonnet-4-5"}]}}}`
	settingsOrig := `{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-4-5","theme":"dark"}`
	if err := os.WriteFile(modelsPath, []byte(modelsOrig), 0o600); err != nil {
		t.Fatalf("写 models.json 失败: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(settingsOrig), 0o600); err != nil {
		t.Fatalf("写 settings.json 失败: %v", err)
	}

	if err := env.client.SetPiMagpieModels("jp", []string{"glm-4.6", "glm-4.5"}); err != nil {
		t.Fatalf("SetPiMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieDefaultModel(ctx, "pi", "jp", "glm-4.6"); err != nil {
		t.Fatalf("保存默认模型失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "global"); err != nil {
		t.Fatalf("切 global 失败: %v", err)
	}

	provider := piProviderDoc(t, modelsPath, "magpie-jp")
	if got := provider["baseUrl"]; got != srv.URL+"/v1" {
		t.Errorf("baseUrl = %v，想要 %q", got, srv.URL+"/v1")
	}
	if got := provider["apiKey"]; got != "gw-key" {
		t.Errorf("apiKey = %v，想要端点 key", got)
	}
	if got := provider["api"]; got != "openai-completions" {
		t.Errorf("api = %v", got)
	}
	if got := piModelIDs(t, provider); len(got) != 2 || got[0] != "glm-4.6" || got[1] != "glm-4.5" {
		t.Errorf("models = %v，想要 [glm-4.6 glm-4.5]", got)
	}
	settings := readAgentMagpieSettings(t, settingsPath)
	if got := settings["defaultProvider"]; got != "magpie-jp" {
		t.Errorf("defaultProvider = %v，想要 magpie-jp", got)
	}
	if got := settings["defaultModel"]; got != "glm-4.6" {
		t.Errorf("defaultModel = %v，想要 glm-4.6", got)
	}
	if got := settings["theme"]; got != "dark" {
		t.Errorf("原有 theme 被覆盖: %v", got)
	}
	if _, ok := readAgentMagpieSettings(t, modelsPath)["providers"].(map[string]any)["anthropic"]; !ok {
		t.Error("原有 anthropic provider 应保留")
	}

	setting, err := env.client.AgentMagpie(ctx, "pi")
	if err != nil {
		t.Fatalf("AgentMagpie 失败: %v", err)
	}
	if !setting.Global.Supported || setting.Global.Path != modelsPath {
		t.Errorf("Global = %+v，想要 Supported 且 Path = %q", setting.Global, modelsPath)
	}
	if setting.Mode != string(config.MagpieModeGlobal) || setting.Global.State != "applied" {
		t.Fatalf("setting = %+v，想要 global / applied", setting)
	}

	// global 下勾选变化：勾选只剩 glm-4.5，默认模型回退到它并重写两份文件。
	if err := env.client.SetPiMagpieModels("jp", []string{"glm-4.5"}); err != nil {
		t.Fatalf("global 下改勾选失败: %v", err)
	}
	provider = piProviderDoc(t, modelsPath, "magpie-jp")
	if got := piModelIDs(t, provider); len(got) != 1 || got[0] != "glm-4.5" {
		t.Errorf("改勾选后 models = %v，想要 [glm-4.5]", got)
	}
	settings = readAgentMagpieSettings(t, settingsPath)
	if got := settings["defaultModel"]; got != "glm-4.5" {
		t.Errorf("改勾选后 defaultModel = %v，想要 glm-4.5", got)
	}

	// global 下改默认模型：恢复两个勾选后把默认模型改成 glm-4.5，应立刻重写。
	if err := env.client.SetPiMagpieModels("jp", []string{"glm-4.6", "glm-4.5"}); err != nil {
		t.Fatalf("恢复勾选失败: %v", err)
	}
	if err := env.client.SetAgentMagpieDefaultModel(ctx, "pi", "jp", "glm-4.5"); err != nil {
		t.Fatalf("global 下改默认模型失败: %v", err)
	}
	if got := readAgentMagpieSettings(t, settingsPath)["defaultModel"]; got != "glm-4.5" {
		t.Errorf("改默认模型后 defaultModel = %v，想要 glm-4.5", got)
	}

	// 切回 inject：恢复 connect 前的两份文件。
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "inject"); err != nil {
		t.Fatalf("切回 inject 失败: %v", err)
	}
	if got, err := os.ReadFile(modelsPath); err != nil || string(got) != modelsOrig {
		t.Errorf("models.json 未恢复: %v\n%s", err, got)
	}
	if got, err := os.ReadFile(settingsPath); err != nil || string(got) != settingsOrig {
		t.Errorf("settings.json 未恢复: %v\n%s", err, got)
	}

	// 再进 global 后切 off，也应恢复原文件。
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "global"); err != nil {
		t.Fatalf("重新切 global 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "off"); err != nil {
		t.Fatalf("切 off 失败: %v", err)
	}
	if got, err := os.ReadFile(settingsPath); err != nil || string(got) != settingsOrig {
		t.Errorf("off 后 settings.json 未恢复: %v\n%s", err, got)
	}
}

// TestPiGlobalPlanWritesAllEndpoints 校验 pi 把每个有 key 且勾了模型的端点
// 都写进 models.json，默认指向第一个。
func TestPiGlobalPlanWritesAllEndpoints(t *testing.T) {
	old := agentMagpieFetchTimeout
	agentMagpieFetchTimeout = 50 * time.Millisecond
	defer func() { agentMagpieFetchTimeout = old }()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "jp", Name: "Magpie JP", BaseURL: "http://127.0.0.1:1/v1", Enabled: true, Magpie: true},
		{ID: "backup", Name: "Magpie Backup", BaseURL: "http://127.0.0.1:1/v1", Enabled: true, Magpie: true},
		{ID: "nokey", Name: "Magpie NoKey", BaseURL: "http://127.0.0.1:1/v1", Enabled: true, Magpie: true},
	}
	secrets := magpieTestSecrets(t, map[string]string{"jp": "k1", "backup": "k2"})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{
		config.ProviderPi: {Selections: []config.MagpieSelection{
			{EndpointID: "jp", Models: []string{"glm-4.6"}},
			{EndpointID: "backup", Models: []string{"acme/model-x"}},
			{EndpointID: "nokey", Models: []string{"ghost"}},
		}},
	}

	plan, err := globalPlan(context.Background(), cfg, secrets, config.ProviderPi)
	if err != nil {
		t.Fatalf("globalPlan: %v", err)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("文件数 = %d，想要 2", len(plan.Files))
	}
	var paths []string
	for _, f := range plan.Files[0].Fields {
		paths = append(paths, f.Path)
	}
	if len(paths) != 2 || paths[0] != "providers.magpie-jp" || paths[1] != "providers.magpie-backup" {
		t.Errorf("provider 路径 = %v，想要 jp 与 backup（跳过无 key 的端点）", paths)
	}
	var defaultProvider, defaultModel any
	for _, f := range plan.Files[1].Fields {
		switch f.Path {
		case "defaultProvider":
			defaultProvider = f.Value
		case "defaultModel":
			defaultModel = f.Value
		}
	}
	if defaultProvider != "magpie-jp" || defaultModel != "glm-4.6" {
		t.Errorf("默认 = %v / %v，想要 magpie-jp / glm-4.6", defaultProvider, defaultModel)
	}
}

// TestBuildPiMagpieEnvModeGate 校验只有 inject 模式才注入；global 与 off 返回空。
func TestBuildPiMagpieEnvModeGate(t *testing.T) {
	old := piMagpieFetchTimeout
	piMagpieFetchTimeout = 50 * time.Millisecond
	defer func() { piMagpieFetchTimeout = old }()

	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: "http://127.0.0.1:1/v1", Enabled: true, Magpie: true,
		PiModels: []string{"glm-4.6"},
	})
	secrets := magpieTestSecrets(t, map[string]string{"jp": "gw-key"})

	cfg = config.Normalize(cfg)
	if raw := buildPiMagpieEnv(context.Background(), cfg, secrets); raw == "" {
		t.Fatal("inject 模式应生成注入值")
	}
	config.SetMagpieAgentMode(&cfg, config.ProviderPi, config.MagpieModeGlobal)
	if raw := buildPiMagpieEnv(context.Background(), cfg, secrets); raw != "" {
		t.Fatalf("global 模式不应注入，得到 %q", raw)
	}
	config.SetMagpieAgentMode(&cfg, config.ProviderPi, config.MagpieModeOff)
	if raw := buildPiMagpieEnv(context.Background(), cfg, secrets); raw != "" {
		t.Fatalf("off 模式不应注入，得到 %q", raw)
	}
}

// TestPiStaleModelGlobalApplied 校验 global 已接管时模型不算失效，off 时仍判失效。
func TestPiStaleModelGlobalApplied(t *testing.T) {
	srv := agentMagpieModelsServer(t, "glm-4.6")
	env := newAgentMagpieTestEnv(t, srv.URL)
	ctx := context.Background()
	setPiMagpieRuntime(env.dataDir, env.client.secrets)
	t.Cleanup(func() { setPiMagpieRuntime("", nil) })

	if err := env.client.SetPiMagpieModels("jp", []string{"glm-4.6"}); err != nil {
		t.Fatalf("SetPiMagpieModels 失败: %v", err)
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "global"); err != nil {
		t.Fatalf("切 global 失败: %v", err)
	}
	if piStaleModel("magpie-jp/glm-4.6") {
		t.Error("global 已接管时不应判为失效")
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "off"); err != nil {
		t.Fatalf("切 off 失败: %v", err)
	}
	if !piStaleModel("magpie-jp/glm-4.6") {
		t.Error("off 时 Magpie provider 的模型应判为失效")
	}
	if err := env.client.SetAgentMagpieMode(ctx, "pi", "inject"); err != nil {
		t.Fatalf("切 inject 失败: %v", err)
	}
	if piStaleModel("magpie-jp/glm-4.6") {
		t.Error("inject 且有可注入端点时不应判为失效")
	}
}
