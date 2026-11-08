package app

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

func harnessKinds(hs []harness.Harness) []string {
	kinds := make([]string, 0, len(hs))
	for _, h := range hs {
		kinds = append(kinds, string(h.Kind()))
	}
	return kinds
}

func harnessModelIDs(t *testing.T, h harness.Harness) []string {
	t.Helper()
	mi, ok := h.(interface{ ModelIDs() []string })
	if !ok {
		t.Fatalf("harness %T 不暴露 ModelIDs", h)
	}
	return mi.ModelIDs()
}

func TestBuildExtraHarnesses_AllDisabled(t *testing.T) {
	secrets := config.NewMemorySecretStore()
	if got := buildExtraHarnesses(config.Config{}, secrets, providerMCP{}); len(got) != 0 {
		t.Fatalf("全部关闭时应返回空列表，实际: %v", harnessKinds(got))
	}
}

func TestBuildExtraHarnesses_EnabledKinds(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers.Copilot.Enabled = true
	cfg.Providers.Pi.Enabled = true
	cfg.Providers.Omp.Enabled = true
	cfg.Providers.CommandCode.Enabled = true
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true},
	}

	got := harnessKinds(buildExtraHarnesses(cfg, config.NewMemorySecretStore(), providerMCP{}))
	want := []string{"copilot", "pi", "mcode", "omp", "commandcode", "deepseek"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("harness Kind 集合不匹配: got %v, want %v", got, want)
	}
}

func TestBuildProviderHarness_PresetModelOverride(t *testing.T) {
	secrets := config.NewMemorySecretStore()

	cfg := config.Config{}
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true},
	}
	h, ok := buildProviderHarness(cfg, secrets, "deepseek", providerMCP{})
	if !ok {
		t.Fatalf("deepseek 预设启用时应返回 harness")
	}
	if models := harnessModelIDs(t, h); !reflect.DeepEqual(models, []string{"deepseek-v4-pro"}) {
		t.Fatalf("未覆盖时模型应为预设默认值，got %v", models)
	}

	cfg.Providers.Presets["deepseek"] = config.PresetProviderConfig{Enabled: true, Model: "deepseek-custom"}
	h, ok = buildProviderHarness(cfg, secrets, "deepseek", providerMCP{})
	if !ok {
		t.Fatalf("deepseek 预设启用时应返回 harness")
	}
	if models := harnessModelIDs(t, h); !reflect.DeepEqual(models, []string{"deepseek-custom"}) {
		t.Fatalf("预设 Model 覆盖未生效，got %v", models)
	}
}

// newProviderSettingsClient 构造使用临时 dataDir 与内存 SecretStore 的 Client。
func newProviderSettingsClient(t *testing.T) (*Client, string, config.SecretStore) {
	t.Helper()
	dataDir := t.TempDir()
	secrets := config.NewMemorySecretStore()

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	client := NewClient(backend)
	client.SetProviderStore(dataDir, secrets)
	return client, dataDir, secrets
}

func TestProviderSettings_ListOrderAndDefaults(t *testing.T) {
	client, _, _ := newProviderSettingsClient(t)

	list, err := client.ProviderSettings(context.Background())
	if err != nil {
		t.Fatalf("ProviderSettings failed: %v", err)
	}

	wantIDs := []string{
		"copilot", "mcode", "pi", "omp", "commandcode", "droid", "cline",
		"cursor", "opencode", "grok", "devin",
		"deepseek", "zai", "meta",
	}
	gotIDs := make([]string, 0, len(list))
	for _, s := range list {
		gotIDs = append(gotIDs, s.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("列表顺序不匹配: got %v, want %v", gotIDs, wantIDs)
	}

	byID := make(map[string]mcodeui.ProviderSetting, len(list))
	for _, s := range list {
		byID[s.ID] = s
	}

	// Pi 与 mcode 默认开启；ACP droid/cline/cursor/opencode/grok/devin 在配置缺省时视为启用。
	if !byID["pi"].Enabled {
		t.Error("pi 应默认开启")
	}
	if !byID["mcode"].Enabled || byID["mcode"].Kind == "cli" || byID["mcode"].Bin != "" {
		t.Errorf("mcode 设置不匹配: %+v", byID["mcode"])
	}
	for _, id := range []string{"copilot", "omp", "commandcode", "deepseek", "zai", "meta"} {
		if byID[id].Enabled {
			t.Errorf("%s 应默认关闭", id)
		}
	}
	for _, id := range []string{"droid", "cline", "cursor", "opencode", "grok", "devin"} {
		if !byID[id].Enabled {
			t.Errorf("%s 在 ACP 配置缺省时应视为启用", id)
		}
		if byID[id].Kind != "cli" {
			t.Errorf("%s 的 Kind 不匹配: %q", id, byID[id].Kind)
		}
	}
	if byID["copilot"].Kind != "cli" || byID["deepseek"].Kind != "api" {
		t.Errorf("Kind 不匹配: copilot=%q deepseek=%q", byID["copilot"].Kind, byID["deepseek"].Kind)
	}
	if byID["deepseek"].DefaultBaseURL != "https://api.deepseek.com" || byID["deepseek"].DefaultModel != "deepseek-v4-pro" {
		t.Errorf("deepseek 默认值不匹配: %+v", byID["deepseek"])
	}
	if byID["zai"].DefaultBaseURL != "https://api.z.ai/api/coding/paas/v4" {
		t.Errorf("zai 默认 BaseURL 不匹配: %q", byID["zai"].DefaultBaseURL)
	}
	if byID["deepseek"].HasAPIKey || byID["droid"].HasAPIKey {
		t.Errorf("未保存 Key 时 HasAPIKey 应为 false")
	}
	if !byID["copilot"].UpdateMonitor || !byID["droid"].UpdateMonitor {
		t.Errorf("UpdateMonitor 默认应为开启")
	}
}

// pi 的两个专有设置（长缓存、视觉模型）能在设置页视图与配置文件之间双向映射，
// 且只对 pi 生效：给其他 Provider 带这两个字段不会污染 pi 的配置。
func TestSaveProviderSetting_PiTuningFields(t *testing.T) {
	client, dataDir, _ := newProviderSettingsClient(t)
	ctx := context.Background()

	saved, err := client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:            "pi",
		PiCacheLong:   true,
		PiVisionModel: "  anthropic/claude-haiku-4-5  ",
	}, "")
	if err != nil {
		t.Fatalf("SaveProviderSetting(pi) failed: %v", err)
	}
	if !saved.PiCacheLong || saved.PiVisionModel != "anthropic/claude-haiku-4-5" {
		t.Fatalf("保存后的 pi 设置不匹配: %+v", saved)
	}

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Providers.Pi.PiCacheLong || cfg.Providers.Pi.PiVisionModel != "anthropic/claude-haiku-4-5" {
		t.Fatalf("配置文件中的 pi 设置不匹配: %+v", cfg.Providers.Pi)
	}

	// ProviderSettings 回读时带上这两项。
	list, err := client.ProviderSettings(ctx)
	if err != nil {
		t.Fatalf("ProviderSettings failed: %v", err)
	}
	var pi mcodeui.ProviderSetting
	for _, s := range list {
		if s.ID == "pi" {
			pi = s
		}
	}
	if !pi.PiCacheLong || pi.PiVisionModel != "anthropic/claude-haiku-4-5" {
		t.Fatalf("ProviderSettings 回读的 pi 设置不匹配: %+v", pi)
	}

	// 给 copilot 带上这两个字段不应写进 pi 的配置。
	if _, err := client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:            "copilot",
		PiCacheLong:   true,
		PiVisionModel: "openai/gpt-4o-mini",
	}, ""); err != nil {
		t.Fatalf("SaveProviderSetting(copilot) failed: %v", err)
	}
	cfg, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Providers.Pi.PiCacheLong || cfg.Providers.Pi.PiVisionModel != "anthropic/claude-haiku-4-5" {
		t.Fatalf("保存其他 Provider 不应改动 pi 的设置: %+v", cfg.Providers.Pi)
	}

	// 清空后回到默认。
	saved, err = client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{ID: "pi", PiVisionModel: "   "}, "")
	if err != nil {
		t.Fatalf("SaveProviderSetting(pi clear) failed: %v", err)
	}
	if saved.PiCacheLong || saved.PiVisionModel != "" {
		t.Fatalf("清空后的 pi 设置不匹配: %+v", saved)
	}
}

func TestSaveProviderSetting_WritesConfigAndSecrets(t *testing.T) {
	client, dataDir, secrets := newProviderSettingsClient(t)
	ctx := context.Background()

	// 1. CLI Provider：启用 + 自定义 Bin + 关闭更新监控。
	saved, err := client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:            "copilot",
		Enabled:       true,
		Bin:           "/opt/copilot",
		UpdateMonitor: false,
	}, "")
	if err != nil {
		t.Fatalf("SaveProviderSetting(copilot) failed: %v", err)
	}
	if !saved.Enabled || saved.Bin != "/opt/copilot" || saved.UpdateMonitor {
		t.Fatalf("保存后的 copilot 设置不匹配: %+v", saved)
	}

	// 2. API 预设：Key 写入 fake secrets，配置文件中不含明文。
	saved, err = client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:      "deepseek",
		Enabled: true,
		BaseURL: "https://proxy.example.com/v1",
		Model:   "deepseek-custom",
	}, "sk-test-deepseek")
	if err != nil {
		t.Fatalf("SaveProviderSetting(deepseek) failed: %v", err)
	}
	if !saved.Enabled || saved.BaseURL != "https://proxy.example.com/v1" || saved.Model != "deepseek-custom" || !saved.HasAPIKey {
		t.Fatalf("保存后的 deepseek 设置不匹配: %+v", saved)
	}
	if saved.DefaultBaseURL != "https://api.deepseek.com" || saved.DefaultModel != "deepseek-v4-pro" {
		t.Fatalf("保存后应回填预设默认值: %+v", saved)
	}

	if key, err := secrets.Get("deepseek.apiKey"); err != nil || key != "sk-test-deepseek" {
		t.Fatalf("secrets 中的 Key 不匹配: val=%q, err=%v", key, err)
	}

	raw, err := os.ReadFile(config.ConfigPath(dataDir))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	configStr := string(raw)
	if !strings.Contains(configStr, "proxy.example.com") || !strings.Contains(configStr, "deepseek-custom") {
		t.Fatalf("配置文件中应包含 BaseURL 与 Model: %s", configStr)
	}
	if strings.Contains(configStr, "sk-test-deepseek") {
		t.Fatalf("配置文件中不得包含 API Key 明文")
	}

	// 保存后热更新注册：deepseek harness 应出现在 Provider 列表中。
	providers, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	foundDeepSeek := false
	for _, p := range providers {
		if p.ID == "deepseek" {
			foundDeepSeek = true
			break
		}
	}
	if !foundDeepSeek {
		t.Fatalf("启用 deepseek 后应出现在 Providers 列表: %+v", providers)
	}

	// 3. 再次保存且 apiKey 为空：保留已存 Key，HasAPIKey 回填。
	saved, err = client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:      "deepseek",
		Enabled: true,
		BaseURL: "https://proxy.example.com/v1",
		Model:   "deepseek-custom",
	}, "")
	if err != nil {
		t.Fatalf("SaveProviderSetting(deepseek) with empty key failed: %v", err)
	}
	if !saved.HasAPIKey {
		t.Fatalf("apiKey 为空时应保留已存 Key（HasAPIKey=true）")
	}

	// 4. droid：认证方式写入 ACP 配置，Key 写入 droid.apiKey 账号。
	saved, err = client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:            "droid",
		Enabled:       true,
		Auth:          "apiKey",
		UpdateMonitor: true,
	}, "factory-key")
	if err != nil {
		t.Fatalf("SaveProviderSetting(droid) failed: %v", err)
	}
	if saved.Auth != "apiKey" || !saved.HasAPIKey {
		t.Fatalf("保存后的 droid 设置不匹配: %+v", saved)
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Providers.ACP["droid"]; !got.Enabled || got.Auth != "apiKey" {
		t.Fatalf("ACP droid 配置不匹配: %+v", got)
	}
	if key, err := secrets.Get("droid.apiKey"); err != nil || key != "factory-key" {
		t.Fatalf("secrets 中的 droid Key 不匹配: val=%q, err=%v", key, err)
	}

	// 5. 停用后热更新注销：deepseek 从 Provider 列表消失。
	if _, err := client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{
		ID:      "deepseek",
		Enabled: false,
	}, ""); err != nil {
		t.Fatalf("SaveProviderSetting(deepseek disable) failed: %v", err)
	}
	providers, err = client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	for _, p := range providers {
		if p.ID == "deepseek" {
			t.Fatalf("停用 deepseek 后不应出现在 Providers 列表: %+v", providers)
		}
	}
}

func TestSaveProviderSetting_McodeRegistersAndUnregisters(t *testing.T) {
	client, _, _ := newProviderSettingsClient(t)
	ctx := context.Background()

	if _, err := client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{ID: "mcode", Enabled: true}, ""); err != nil {
		t.Fatalf("enable mcode: %v", err)
	}
	if client.backend.RegisteredHarness("mcode") == nil {
		t.Fatal("enabled mcode should register its harness")
	}
	if _, err := client.SaveProviderSetting(ctx, mcodeui.ProviderSetting{ID: "mcode", Enabled: false}, ""); err != nil {
		t.Fatalf("disable mcode: %v", err)
	}
	if client.backend.RegisteredHarness("mcode") != nil {
		t.Fatal("disabled mcode should unregister its harness")
	}
}

func TestSaveProviderSetting_UnknownID(t *testing.T) {
	client, _, _ := newProviderSettingsClient(t)
	if _, err := client.SaveProviderSetting(context.Background(), mcodeui.ProviderSetting{ID: "unknown"}, ""); err == nil {
		t.Fatalf("未知 ID 应返回错误")
	}
}
