package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/config"
	"mcode/internal/magpieinject"
)

func mcodeTestConfig(endpoints ...config.CustomEndpointConfig) config.Config {
	cfg := config.Defaults()
	cfg.Providers.Custom = endpoints
	return cfg
}

func mcodeTestSecrets(values map[string]string) config.SecretStore {
	store := config.NewMemorySecretStore()
	for account, value := range values {
		_ = store.Set(account, value)
	}
	return store
}

func TestMcodeResolveClientMagpieRequest(t *testing.T) {
	var path, auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	client, model, err := resolveMcodeClient(cfg, secrets, "magpie-mcode/claude-sonnet")
	if err != nil || model != "claude-sonnet" {
		t.Fatalf("resolve = %q, %v", model, err)
	}
	_, err = client.Stream(context.Background(), llm.Request{Model: model, Messages: []llm.Message{{Role: "user", Content: []llm.Part{{Type: "text", Text: "hello"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/chat/completions" || auth != "Bearer gateway-key" {
		t.Fatalf("request = %q %q", path, auth)
	}
}

func TestMcodeResolveClientAnthropicAndErrors(t *testing.T) {
	cfg := config.Defaults()
	store := mcodeTestSecrets(map[string]string{mcodeAnthropicAPIKeyAccount: "secret-key"})
	client, model, err := resolveMcodeClient(cfg, store, "anthropic/claude-sonnet-5-5")
	if err != nil || model != "claude-sonnet-5-5" {
		t.Fatalf("secret key resolve = %q, %v", model, err)
	}
	if _, ok := client.(interface {
		Stream(context.Context, llm.Request, func(llm.Delta)) (*llm.Response, error)
	}); !ok {
		t.Fatal("Anthropic client missing Stream")
	}
	store = mcodeTestSecrets(nil)
	t.Setenv("ANTHROPIC_API_KEY", "environment-key")
	if _, _, err := resolveMcodeClient(cfg, store, "anthropic/claude-opus-5-5"); err != nil {
		t.Fatalf("environment fallback: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, _, err := resolveMcodeClient(cfg, store, "anthropic/claude-opus-5-5"); err == nil || !strings.Contains(err.Error(), "未配置 Anthropic API Key") {
		t.Fatalf("missing key error = %v", err)
	}
	if _, _, err := resolveMcodeClient(cfg, store, "unknown/model"); err == nil || !strings.Contains(err.Error(), "Magpie 端点") {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestMcodeResolveClientDefaultAndAnthropicProviderPrecedence(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "anthropic", Name: "Anthropic", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("anthropic"): "gateway-key"})
	_, model, err := resolveMcodeClient(cfg, secrets, "anthropic/model-x")
	if err != nil || model != "model-x" {
		t.Fatalf("endpoint precedence = %q, %v", model, err)
	}
	if _, ok := magpieinject.ProviderOf("anthropic/model-x", magpieinject.Targets(cfg, secrets, nil)); !ok {
		t.Fatal("Magpie target should match")
	}
	cfg = mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	_, model, err = resolveMcodeClient(cfg, secrets, "")
	if err != nil || model != magpieinject.DefaultModel().ID {
		t.Fatalf("empty model = %q, %v", model, err)
	}
}

func TestMcodeModelCatalogAnthropicConditional(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {Selections: []config.MagpieSelection{{EndpointID: "edge", Models: []string{"model-a"}}}}}
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	got := mcodeModelsForConfig(cfg, secrets)
	if len(got) != 1 || got[0].ID != "edge/model-a" {
		t.Fatalf("Magpie catalog = %#v", got)
	}
	if !got[0].IsDefault {
		t.Fatal("first row should be default")
	}
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key", mcodeAnthropicAPIKeyAccount: "anthropic-key"})
	got = mcodeModelsForConfig(cfg, secrets)
	if len(got) != 4 || got[1].ID != "anthropic/claude-opus-5-5" || got[3].ID != "anthropic/claude-haiku-5-5" {
		t.Fatalf("Anthropic catalog = %#v", got)
	}
}

func TestMcodeModelCatalogNoAnthropicKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := config.Defaults()
	if got := mcodeModelsForConfig(cfg, mcodeTestSecrets(nil)); len(got) != 0 {
		t.Fatalf("catalog without key = %#v", got)
	}
}
