package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/providers/magpie"
)

func TestAPISourceCatalogMagpieEndpoints(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"shared","display_name":"First","reasoning":true,"supported_reasoning_levels":[{"effort":"high"},{"effort":"low"}],"context_window":12345,"magpie_price":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}},{"id":"plain","reasoning":false}]}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"shared","display_name":"Second","context_window":54321},{"id":"two"},{"id":"three"}]}`)
	}))
	defer second.Close()
	cfg := mcodeTestConfig(
		config.CustomEndpointConfig{ID: "a", Enabled: true, Magpie: true, BaseURL: first.URL, Model: "plain"},
		config.CustomEndpointConfig{ID: "b", Enabled: true, Magpie: true, BaseURL: second.URL},
		config.CustomEndpointConfig{ID: "disabled", Enabled: false, Magpie: true, BaseURL: first.URL},
		config.CustomEndpointConfig{ID: "nokey", Enabled: true, Magpie: true, BaseURL: first.URL},
	)
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {DefaultEndpointID: "b", DefaultModel: "three"}}
	secrets := mcodeTestSecrets(map[string]string{
		config.CustomEndpointSecretAccount("a"):        "key-a",
		config.CustomEndpointSecretAccount("b"):        "key-b",
		config.CustomEndpointSecretAccount("disabled"): "key-a",
	})
	for _, item := range []struct{ url, key string }{{first.URL, "key-a"}, {second.URL, "key-b"}} {
		if _, err := magpie.FetchModels(context.Background(), item.url, item.key); err != nil {
			t.Fatal(err)
		}
	}
	a := apiSourceCatalog(cfg, secrets, "custom-a", nil)
	b := apiSourceCatalog(cfg, secrets, "custom-b", nil)
	if len(a) != 2 || a[0].ID != "plain" || !a[0].IsDefault || a[1].ID != "shared" || a[1].Name != "First" {
		t.Fatalf("端点 a 目录 = %#v", a)
	}
	if len(a[0].Efforts) != 0 || strings.Join(a[1].Efforts, ",") != "high,low" || a[1].ContextLimit != 12345 || a[1].Cost == nil || a[1].Cost.Input != 3 || a[1].Cost.Output != 15 || a[1].Cost.CacheRead != 0.3 || a[1].Cost.CacheWrite != 3.75 {
		t.Fatalf("端点 a 元数据 = %#v", a)
	}
	if len(b) != 3 || b[0].ID != "shared" || b[0].Name != "Second" || b[0].ContextLimit != 54321 {
		t.Fatalf("端点 b 目录 = %#v", b)
	}
	for _, kind := range []string{"custom-disabled", "custom-nokey", "custom-deleted"} {
		if got := apiSourceCatalog(cfg, secrets, kind, []string{"fake"}); len(got) != 0 {
			t.Fatalf("%s 目录 = %#v", kind, got)
		}
	}
	targets := apisource.MagpieTargets(cfg, secrets)
	prefixes := make(map[string]string)
	for _, target := range targets {
		prefixes[target.Endpoint] = target.ProviderID
	}
	rows := mcodeModelsForConfig(cfg, secrets)
	if len(rows) != 5 || rows[0].ID != prefixes["b"]+"/three" || !rows[0].IsDefault {
		t.Fatalf("聚合目录 = %#v", rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if !seen[prefixes["a"]+"/shared"] || !seen[prefixes["b"]+"/shared"] {
		t.Fatalf("同名模型缺少端点前缀：%#v", rows)
	}
	cfg.Providers.Custom = cfg.Providers.Custom[1:]
	if got := apiSourceCatalog(cfg, secrets, "custom-a", nil); len(got) != 0 {
		t.Fatalf("删除后目录 = %#v", got)
	}
	if got := mcodeModelsForConfig(cfg, secrets); len(got) != 3 {
		t.Fatalf("删除后聚合目录 = %#v", got)
	}
}

func TestAPISourceCatalogWarmsColdCache(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	requested := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"data":[{"id":"warmed"}]}`)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "cold", Enabled: true, Magpie: true, BaseURL: server.URL})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("cold"): "cold-key"})
	if got := apiSourceCatalog(cfg, secrets, "custom-cold", nil); len(got) != 0 {
		close(release)
		t.Fatalf("冷缓存目录 = %#v", got)
	}
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("未触发预热")
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rows := apiSourceCatalog(cfg, secrets, "custom-cold", nil); len(rows) == 1 && rows[0].ID == "warmed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("预热后未读到缓存")
}

func TestAPISourceCatalogStaticSources(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "plain", Enabled: true, Model: "default"})
	secrets := mcodeTestSecrets(map[string]string{"anthropic.apiKey": "key"})
	rows := apiSourceCatalog(cfg, secrets, "custom-plain", []string{"other", "default", "other"})
	if len(rows) != 2 || rows[0].ID != "default" || !rows[0].IsDefault || rows[1].ID != "other" {
		t.Fatalf("自定义端点目录 = %#v", rows)
	}
	rows = apiSourceCatalog(cfg, secrets, "anthropic", []string{"ignored"})
	if len(rows) != 3 || rows[0].ID != "claude-opus-5-5" {
		t.Fatalf("Anthropic 目录 = %#v", rows)
	}
	cfg.Providers.Presets = map[string]config.PresetProviderConfig{"deepseek": {Enabled: true, Model: "configured"}}
	rows = apiSourceCatalog(cfg, secrets, "deepseek", []string{"legacy", "configured", "legacy"})
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.ID] {
			t.Fatalf("预设目录包含重复模型：%#v", rows)
		}
		seen[row.ID] = true
	}
	if len(rows) < 3 || rows[0].ID != "configured" || !seen["legacy"] {
		t.Fatalf("预设目录 = %#v", rows)
	}
}
