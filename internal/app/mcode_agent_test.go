package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/config"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
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
	var path, auth, affinity, clientRequest, session string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		affinity = r.Header.Get("x-session-affinity")
		clientRequest = r.Header.Get("x-client-request-id")
		session = r.Header.Get("session_id")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	client, model, err := resolveMcodeClient(cfg, secrets, "magpie-mcode/claude-sonnet", "thread-1")
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
	if affinity != "thread-1" || clientRequest != "thread-1" || session != "thread-1" {
		t.Fatalf("session affinity = %q %q %q", affinity, clientRequest, session)
	}
}

func TestMcodeResolveClientDefaultPlaceholder(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	// 模型目录为空时界面显示的占位模型 default（含大小写与空白变化）应落到默认模型。
	for _, model := range []string{"", "default", " DEFAULT "} {
		client, modelID, err := resolveMcodeClient(cfg, secrets, model, "")
		if err != nil {
			t.Fatalf("模型 %q：%v", model, err)
		}
		if client == nil || modelID != magpieinject.DefaultModel().ID {
			t.Fatalf("模型 %q 解析为 %q，期望 %q", model, modelID, magpieinject.DefaultModel().ID)
		}
	}
}

func TestMcodeResolveClientAnthropicSkipsAffinityHeaders(t *testing.T) {
	var affinity, clientRequest, session string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		affinity = r.Header.Get("x-session-affinity")
		clientRequest = r.Header.Get("x-client-request-id")
		session = r.Header.Get("session_id")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	cfg := config.Defaults()
	store := mcodeTestSecrets(map[string]string{mcodeAnthropicAPIKeyAccount: "secret-key"})
	client, model, err := resolveMcodeClient(cfg, store, "anthropic/claude-sonnet-5-5", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stream(context.Background(), llm.Request{Model: model, Messages: []llm.Message{{Role: "user", Content: []llm.Part{{Type: "text", Text: "hello"}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if affinity != "" || clientRequest != "" || session != "" {
		t.Fatalf("Anthropic request carried session affinity: %q %q %q", affinity, clientRequest, session)
	}
}

func TestMcodeResolveClientEchoesDeepSeekReasoning(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	client, model, err := resolveMcodeClient(cfg, secrets, "magpie-mcode/DeepSeek-V3", "thread-1")
	if err != nil || model != "DeepSeek-V3" {
		t.Fatalf("resolve = %q, %v", model, err)
	}
	_, err = client.Stream(context.Background(), llm.Request{Model: model, Messages: []llm.Message{
		{Role: "user", Content: []llm.Part{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Reasoning: "先想", ToolCalls: []llm.ToolCall{{ID: "call1", Name: "run", Arguments: json.RawMessage(`{}`)}}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	assistant := messages[1].(map[string]any)
	if assistant["reasoning_content"] != "先想" {
		t.Fatalf("assistant reasoning = %+v", assistant)
	}
}

func TestMcodeResolveClientAnthropicAndErrors(t *testing.T) {
	cfg := config.Defaults()
	store := mcodeTestSecrets(map[string]string{mcodeAnthropicAPIKeyAccount: "secret-key"})
	client, model, err := resolveMcodeClient(cfg, store, "anthropic/claude-sonnet-5-5", "")
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
	if _, _, err := resolveMcodeClient(cfg, store, "anthropic/claude-opus-5-5", ""); err != nil {
		t.Fatalf("environment fallback: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, _, err := resolveMcodeClient(cfg, store, "anthropic/claude-opus-5-5", ""); err == nil || !strings.Contains(err.Error(), "未配置 Anthropic API Key") {
		t.Fatalf("missing key error = %v", err)
	}
	if _, _, err := resolveMcodeClient(cfg, store, "unknown/model", ""); err == nil || !strings.Contains(err.Error(), "Magpie 端点") {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestMcodeResolveClientDefaultAndAnthropicProviderPrecedence(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "anthropic", Name: "Anthropic", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("anthropic"): "gateway-key"})
	_, model, err := resolveMcodeClient(cfg, secrets, "anthropic/model-x", "")
	if err != nil || model != "model-x" {
		t.Fatalf("endpoint precedence = %q, %v", model, err)
	}
	target, modelID, ok := magpieinject.ProviderOf("anthropic/model-x", magpieinject.Targets(cfg, secrets, nil))
	if !ok || target.ProviderID != "anthropic" || modelID != "model-x" {
		t.Fatal("Magpie target should match")
	}
	cfg = mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	_, model, err = resolveMcodeClient(cfg, secrets, "", "")
	if err != nil || model != magpieinject.DefaultModel().ID {
		t.Fatalf("empty model = %q, %v", model, err)
	}
}

func TestMcodeModelCatalogUsesPiSelections(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderPi: {Selections: []config.MagpieSelection{{EndpointID: "edge", Models: []string{"model-a", "model-b"}}}}}
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	got := mcodeModelsForConfig(cfg, secrets)
	piTargets := piMagpieTargets(cfg, secrets)
	if len(piTargets) != 1 || len(got) != 2 || got[0].ID != piTargets[0].providerID+"/model-a" ||
		got[0].Name != magpie.ShortName(magpie.Model{ID: "model-a"}) || got[1].ID != piTargets[0].providerID+"/model-b" {
		t.Fatalf("Pi-compatible catalog = %#v", got)
	}
	if !got[0].IsDefault || got[1].IsDefault {
		t.Fatalf("default flags = %#v", got)
	}
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key", mcodeAnthropicAPIKeyAccount: "anthropic-key"})
	got = mcodeModelsForConfig(cfg, secrets)
	if len(got) != 5 || got[2].ID != "anthropic/claude-opus-5-5" || got[4].ID != "anthropic/claude-haiku-5-5" {
		t.Fatalf("Anthropic catalog = %#v", got)
	}
}

func TestMcodeModelCatalogDefaultsPerKeyedEndpoint(t *testing.T) {
	cfg := mcodeTestConfig(
		config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: "https://edge.example/v1"},
		config.CustomEndpointConfig{ID: "other", Name: "Other", Enabled: true, Magpie: true, BaseURL: "https://other.example/v1"},
	)
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "edge-key", config.CustomEndpointSecretAccount("other"): "other-key"})
	got := mcodeModelsForConfig(cfg, secrets)
	if len(got) != 2 || got[0].ID != "edge/"+magpieinject.DefaultModel().ID || got[1].ID != "other/"+magpieinject.DefaultModel().ID || !got[0].IsDefault {
		t.Fatalf("default catalog = %#v", got)
	}
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "edge-key"})
	got = mcodeModelsForConfig(cfg, secrets)
	if len(got) != 1 || got[0].ID != "edge/"+magpieinject.DefaultModel().ID {
		t.Fatalf("catalog should include only endpoints with keys: %#v", got)
	}
}

func TestMcodeModelCatalogAnthropicRequiresKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := config.Defaults()
	if got := mcodeModelsForConfig(cfg, mcodeTestSecrets(nil)); len(got) != 0 {
		t.Fatalf("catalog without key = %#v", got)
	}
}

func TestMcodeModelCatalogLoadFailureReturnsNil(t *testing.T) {
	invalidPath := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(invalidPath, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	setMcodeAgentRuntime(invalidPath, nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	if got := mcodeModelCatalog(); got != nil {
		t.Fatalf("catalog after config load failure = %#v, want nil", got)
	}
}

func TestMcodeCurrentConfigLoadFailureUsesFallback(t *testing.T) {
	fallback := config.Defaults()
	fallback.Providers.Pi.Bin = "fallback-pi"
	invalidPath := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(invalidPath, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	setMcodeAgentRuntime(invalidPath, nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	if got := mcodeCurrentConfig(fallback, nil); got.Providers.Pi.Bin != "fallback-pi" {
		t.Fatalf("config load failure lost fallback: %+v", got.Providers.Pi)
	}
}

func TestMcodeContextWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","context_window":1000000}]}`)
	}))
	t.Cleanup(server.Close)
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "jp", Name: "Magpie JP", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderPi: {
		Selections: []config.MagpieSelection{{EndpointID: "jp", Models: []string{"claude-sonnet-4.5", "no-metadata"}}},
	}}
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("jp"): "gw-key"})
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gw-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	targets := piMagpieAllTargets(cfg, secrets)
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	prefix := targets[0].providerID
	if got := mcodeContextWindow(cfg, secrets, prefix+"/claude-sonnet-4.5"); got != 1000000 {
		t.Fatalf("网关元数据的窗口 = %d", got)
	}
	if got := mcodeContextWindow(cfg, secrets, prefix+"/no-metadata"); got != magpieinject.DefaultModel().ContextWindow {
		t.Fatalf("缺少元数据的窗口 = %d", got)
	}
	if got := mcodeContextWindow(cfg, secrets, "anthropic/claude-sonnet-5-5"); got != mcodeAnthropicContextWindow {
		t.Fatalf("Anthropic 的窗口 = %d", got)
	}
	if got := mcodeContextWindow(cfg, secrets, "ghost/model"); got != 0 {
		t.Fatalf("未知模型的窗口 = %d", got)
	}
	if got := mcodeContextWindow(cfg, secrets, ""); got != 0 {
		t.Fatalf("空模型的窗口 = %d", got)
	}
}
