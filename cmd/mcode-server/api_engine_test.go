package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
)

func TestWarmServerMagpieRecoversPanic(t *testing.T) {
	previous := serverMagpieFetch
	t.Cleanup(func() { serverMagpieFetch = previous })
	called := make(chan struct{}, 2)
	serverMagpieFetch = func(context.Context, string, string) ([]magpie.Model, error) { called <- struct{}{}; panic("fetch") }
	const url, key = "panic-server", "panic-key"
	for i := 0; i < 2; i++ {
		warmServerMagpie(url, key)
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("预热未执行")
		}
		deadline := time.Now().Add(time.Second)
		for {
			if _, running := serverMagpieWarm.Load(url + "\x00" + key); !running {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("预热状态未清除")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestAPIEngineDiscoversDefaultModel(t *testing.T) {
	var modelRequests int
	var requestedModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			modelRequests++
			_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		requestedModels = append(requestedModels, request.Model)
		http.Error(w, "结束测试", http.StatusBadRequest)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{{ID: "discovery", Enabled: true, BaseURL: server.URL + "/v1"}}
	dir := t.TempDir()
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	backend := remote.NewMemoryBackend(remote.NewStreamHub(remote.StreamHubOptions{}))
	defer backend.Close()
	registerAPIEngines(backend, cfg, config.NewMemorySecretStore(), dir)
	t.Cleanup(func() { remote.SetAPICatalog("custom-discovery", nil) })
	h := backend.RegisteredHarness("custom-discovery").(*providermcode.Harness)
	if ok, reason := h.Available(); !ok {
		t.Fatalf("自定义来源不可用：%s", reason)
	}
	for i := 0; i < 2; i++ {
		_ = h.Run(context.Background(), harness.Turn{Prompt: "hi", WorkDir: t.TempDir()}, nil)
	}
	if modelRequests != 1 || !reflect.DeepEqual(requestedModels, []string{"alpha", "alpha"}) {
		t.Fatalf("目录请求 %d 次，模型 %v", modelRequests, requestedModels)
	}
}

func TestAPIEngineMissingAPIKeyUnavailable(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	cfg := config.Defaults()
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{"deepseek": {Enabled: true}}
	dir := t.TempDir()
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	backend := remote.NewMemoryBackend(remote.NewStreamHub(remote.StreamHubOptions{}))
	defer backend.Close()
	registerAPIEngines(backend, cfg, config.NewMemorySecretStore(), dir)
	t.Cleanup(func() { remote.SetAPICatalog("deepseek", nil) })
	h := backend.RegisteredHarness("deepseek").(*providermcode.Harness)
	if ok, reason := h.Available(); ok || reason != "未配置 API Key" {
		t.Fatalf("Available() = %v, %q", ok, reason)
	}
}

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
