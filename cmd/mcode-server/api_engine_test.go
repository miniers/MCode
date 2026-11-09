package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mcode/internal/config"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
)

func TestRegisterAPIEngines(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.OpenAI.Enabled = false
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{
		"deepseek": {Enabled: true, Model: "first"},
		"magpie2":  {Enabled: true, BaseURL: "https://legacy.example/v1", Model: "legacy"},
		"zai":      {Enabled: false},
	}
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "example", Name: "Example", BaseURL: "https://example.com/v1", Model: "custom-model", Enabled: true},
		{ID: "gateway", Name: "Gateway", BaseURL: "https://gateway.example/v1", Magpie: true, Enabled: true},
		{ID: "disabled", Enabled: false},
	}
	writeConfig := func() {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.ConfigPath(dir), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set("magpie2.apiKey", "legacy-key"); err != nil {
		t.Fatal(err)
	}
	backend := remote.NewMemoryBackend(remote.NewStreamHub(remote.StreamHubOptions{}))
	defer backend.Close()
	kinds := registerAPIEngines(backend, cfg, secrets, dir)
	for _, kind := range kinds {
		t.Cleanup(func() { remote.SetAPICatalog(kind, nil) })
	}
	for _, kind := range []string{"deepseek", "custom-example", "custom-gateway", "magpie2"} {
		h, ok := backend.RegisteredHarness(kind).(*providermcode.Harness)
		if !ok {
			t.Fatalf("%s 不是内置引擎", kind)
		}
		storeDir := reflect.ValueOf(h).Elem().FieldByName("opts").FieldByName("StoreDir").String()
		if storeDir != filepath.Join(dir, "mcode", "sessions") {
			t.Fatalf("%s: StoreDir = %q", kind, storeDir)
		}
	}
	if backend.RegisteredHarness("zai") != nil || backend.RegisteredHarness("custom-disabled") != nil {
		t.Fatal("禁用来源不应注册")
	}
	for _, s := range serverAPISources(cfg, secrets) {
		if s.Kind == "magpie2" && s.APIKey(secrets) != "legacy-key" {
			t.Fatal("旧凭据没有回退")
		}
	}
	pc := cfg.Providers.Presets["deepseek"]
	pc.Model = "second"
	cfg.Providers.Presets["deepseek"] = pc
	writeConfig()
	rows := remote.ProviderModels("deepseek")
	if len(rows) == 0 || rows[0].ID != "second" {
		t.Fatalf("目录未更新：%v", rows)
	}
	if _, ok := cfg.Providers.Presets["magpie2"]; !ok {
		t.Fatal("旧配置被修改")
	}
}
