package app

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote"
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
	client, model, err := resolveMcodeClient(context.Background(), cfg, secrets, "magpie-mcode/plain-model", "thread-1")
	if err != nil || model != "plain-model" {
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

func TestMcodeResolveClientMagpieClaudeMessages(t *testing.T) {
	var path, apiKey, auth, affinity, clientRequest, session string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		path, apiKey, auth = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("Authorization")
		affinity = r.Header.Get("x-session-affinity")
		clientRequest = r.Header.Get("x-client-request-id")
		session = r.Header.Get("session_id")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicMessagesSSE)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	client, model, err := resolveMcodeClient(context.Background(), cfg, secrets, "magpie-mcode/claude-sonnet", "thread-1")
	if err != nil || model != "claude-sonnet" {
		t.Fatalf("resolve = %q, %v", model, err)
	}
	_, err = client.Stream(context.Background(), llm.Request{Model: model, Messages: []llm.Message{{Role: "user", Content: []llm.Part{{Type: "text", Text: "hello"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" || apiKey != "" || auth != "Bearer gateway-key" {
		t.Fatalf("request = %q, x-api-key %q, Authorization %q", path, apiKey, auth)
	}
	if affinity != "thread-1" || clientRequest != "thread-1" || session != "thread-1" {
		t.Fatalf("session affinity = %q %q %q", affinity, clientRequest, session)
	}
}

func TestMcodeUsageCost(t *testing.T) {
	models := `{"data":[` +
		`{"id":"plain-model","magpie_price":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}},` +
		`{"id":"no-price"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, models)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gateway-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	// 未缓存输入 1、缓存读取 10、输出 5：(1*3 + 10*0.3 + 5*15) / 1e6。
	cost, ok := mcodeUsageCost(cfg, secrets, "magpie-mcode/plain-model", harness.Usage{InputTokens: 11, OutputTokens: 5, CachedTokens: 10})
	if !ok || math.Abs(cost-0.000081) > 1e-12 {
		t.Fatalf("cost = %v, %v", cost, ok)
	}
	// 空模型没有可计价的模型 ID，不填费用。
	if _, ok := mcodeUsageCost(cfg, secrets, "", harness.Usage{InputTokens: 1000}); ok {
		t.Fatal("空模型不应返回费用")
	}
	if _, ok := mcodeUsageCost(cfg, secrets, "magpie-mcode/no-price", harness.Usage{InputTokens: 1}); ok {
		t.Fatal("无价格的模型不应返回费用")
	}
	if _, ok := mcodeUsageCost(cfg, secrets, "anthropic/claude-sonnet-5-5", harness.Usage{InputTokens: 1}); ok {
		t.Fatal("非 Magpie 模型不应返回费用")
	}
}

func TestMcodeResolveClientDefaultPlaceholder(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {
		DefaultEndpointID: "edge",
		DefaultModel:      "configured-model",
	}}

	// 模型目录为空时界面显示的占位模型 default（含大小写与空白变化）应落到设置里的默认模型。
	for _, model := range []string{"", "default", " DEFAULT "} {
		client, modelID, err := resolveMcodeClient(context.Background(), cfg, secrets, model, "")
		if err != nil {
			t.Fatalf("模型 %q：%v", model, err)
		}
		if client == nil || modelID != "configured-model" {
			t.Fatalf("模型 %q 解析为 %q，期望 configured-model", model, modelID)
		}
	}
}

func TestMcodeHarnessQualifiedMetadata(t *testing.T) {
	setMcodeAgentRuntime("", nil)
	t.Cleanup(func() { setMcodeAgentRuntime("", nil); remote.SetMcodeModels(nil) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, anthropicMessagesSSE)
			return
		}
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Enabled: true, Magpie: true, BaseURL: server.URL})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {
		DefaultEndpointID: "edge",
		DefaultModel:      "plain-model",
	}}
	secrets := mcodeTestSecrets(map[string]string{
		config.CustomEndpointSecretAccount("edge"): "gateway-key",
		"anthropic.apiKey":                         "anthropic-key",
	})
	h := buildMcodeHarness(cfg, secrets, "", providerMCP{})
	for _, tc := range []struct {
		model string
		want  int64
	}{
		{"", int64(magpieinject.DefaultModel().ContextWindow)},
		{"anthropic/claude-test", 200000},
	} {
		var window int64
		err := h.Run(context.Background(), harness.Turn{Model: tc.model, Prompt: "hi", WorkDir: t.TempDir()}, func(event harness.Event) {
			if event.Type == harness.EventUsage && event.Usage != nil {
				window = event.Usage.ContextWindow
			}
		})
		if err != nil || window != tc.want || window == 0 {
			t.Fatalf("模型 %q: 窗口 %d，错误 %v", tc.model, window, err)
		}
	}
}

func TestMcodeResolveClientDefaultFromModelCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"cached-first"},{"id":"cached-second"}]}`)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gateway-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	_, modelID, err := resolveMcodeClient(context.Background(), cfg, secrets, "", "")
	if err != nil || modelID != "cached-first" {
		t.Fatalf("默认模型 = %q, %v", modelID, err)
	}
}

func TestMcodeResolveClientDefaultWithoutConfigOrCache(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	if _, _, err := resolveMcodeClient(context.Background(), cfg, secrets, "", ""); err == nil || !strings.Contains(err.Error(), "默认模型") {
		t.Fatalf("缺少默认模型时应报错，got %v", err)
	}
}

func TestMcodeAnthropicKeyAccountPrecedence(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	onlyNew := mcodeTestSecrets(map[string]string{"anthropic.apiKey": "new-key"})
	if got := mcodeAnthropicKey(onlyNew); got != "new-key" {
		t.Fatalf("只有新账号时 Key = %q", got)
	}
	both := mcodeTestSecrets(map[string]string{"anthropic.apiKey": "new-key", mcodeAnthropicAPIKeyAccount: "legacy-key"})
	if got := mcodeAnthropicKey(both); got != "new-key" {
		t.Fatalf("新旧账号都有时 Key = %q", got)
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
	client, model, err := resolveMcodeClient(context.Background(), cfg, store, "anthropic/claude-sonnet-5-5", "thread-1")
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
	client, model, err := resolveMcodeClient(context.Background(), cfg, secrets, "magpie-mcode/DeepSeek-V3", "thread-1")
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
	client, model, err := resolveMcodeClient(context.Background(), cfg, store, "anthropic/claude-sonnet-5-5", "")
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
	if _, _, err := resolveMcodeClient(context.Background(), cfg, store, "anthropic/claude-opus-5-5", ""); err != nil {
		t.Fatalf("environment fallback: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, _, err := resolveMcodeClient(context.Background(), cfg, store, "anthropic/claude-opus-5-5", ""); err == nil || !strings.Contains(err.Error(), "未配置 Anthropic API Key") {
		t.Fatalf("missing key error = %v", err)
	}
	if _, _, err := resolveMcodeClient(context.Background(), cfg, store, "unknown/model", ""); err == nil || !strings.Contains(err.Error(), "Magpie 端点") {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestMcodeResolveClientDefaultAndAnthropicProviderPrecedence(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "anthropic", Name: "Anthropic", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("anthropic"): "gateway-key"})
	_, model, err := resolveMcodeClient(context.Background(), cfg, secrets, "anthropic/model-x", "")
	if err != nil || model != "model-x" {
		t.Fatalf("endpoint precedence = %q, %v", model, err)
	}
	target, modelID, ok := magpieinject.ProviderOf("anthropic/model-x", magpieinject.Targets(cfg, secrets, nil))
	if !ok || target.ProviderID != "anthropic" || modelID != "model-x" {
		t.Fatal("Magpie target should match")
	}
	cfg = mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	if _, _, err = resolveMcodeClient(context.Background(), cfg, secrets, "", ""); err == nil || !strings.Contains(err.Error(), "默认模型") {
		t.Fatalf("空模型没有默认值时应报错，got %v", err)
	}
}

func TestMcodeModelCatalogUsesFullCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"id":"claude-sonnet-4.5","magpie_label":"Claude Sonnet 4.5 · Anthropic","reasoning":true,"supported_reasoning_levels":[{"effort":"minimal"},{"effort":"low"},{"effort":"high"},{"effort":"max"}],"context_window":400000,"magpie_price":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}},
			{"id":"plain-model","display_name":"Plain Model","reasoning":false,"context_window":128000}]}`)
	}))
	t.Cleanup(server.Close)
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gw-key"})
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gw-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	targets := mcodeTargets(cfg, secrets)
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	prefix := targets[0].ProviderID

	// pi 的勾选不影响完整目录。
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderPi: {
		Selections: []config.MagpieSelection{{EndpointID: "edge", Models: []string{"claude-sonnet-4.5"}}},
	}}
	got := mcodeModelsForConfig(cfg, secrets)
	if len(got) != 2 || got[0].ID != prefix+"/claude-sonnet-4.5" || !got[0].IsDefault {
		t.Fatalf("完整目录 = %#v", got)
	}

	// 默认模型排在最前。
	cfg.Providers.MagpieAgents[config.ProviderMcode] = config.MagpieAgentConfig{
		Selections:        []config.MagpieSelection{{EndpointID: "edge", Models: []string{"plain-model", "claude-sonnet-4.5"}}},
		DefaultEndpointID: "edge",
		DefaultModel:      "claude-sonnet-4.5",
	}
	got = mcodeModelsForConfig(cfg, secrets)
	if len(got) != 2 {
		t.Fatalf("catalog = %#v", got)
	}
	if got[0].ID != prefix+"/claude-sonnet-4.5" || got[1].ID != prefix+"/plain-model" {
		t.Fatalf("模型 ID = %q, %q", got[0].ID, got[1].ID)
	}
	if got[0].Name != "Claude Sonnet 4.5" || got[1].Name != "Plain Model" {
		t.Fatalf("模型名 = %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].ContextLimit != 400000 || got[1].ContextLimit != 128000 {
		t.Fatalf("上下文窗口 = %d, %d", got[0].ContextLimit, got[1].ContextLimit)
	}
	if strings.Join(got[0].Efforts, ",") != "low,high,max" {
		t.Fatalf("网关档位过滤 = %v", got[0].Efforts)
	}
	if len(got[1].Efforts) != 0 {
		t.Fatalf("无推理模型的档位 = %v", got[1].Efforts)
	}
	if !got[0].IsDefault || got[1].IsDefault {
		t.Fatalf("默认标记 = %#v", got)
	}
	if got[0].Cost == nil || got[0].Cost.Input != 3 || got[0].Cost.Output != 15 || got[0].Cost.CacheRead != 0.3 || got[0].Cost.CacheWrite != 3.75 {
		t.Fatalf("网关价格 = %#v", got[0].Cost)
	}
	if got[1].Cost != nil {
		t.Fatalf("没有价格的目录行 = %#v", got[1].Cost)
	}
	// 目录行 ID 能被请求解析到同一个端点。
	_, modelID, err := resolveMcodeClient(context.Background(), cfg, secrets, got[1].ID, "")
	if err != nil || modelID != "plain-model" {
		t.Fatalf("解析目录行 = %q, %v", modelID, err)
	}
	// Anthropic 行照旧排在 Magpie 行之后。
	secrets = mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gw-key", mcodeAnthropicAPIKeyAccount: "anthropic-key"})
	got = mcodeModelsForConfig(cfg, secrets)
	if len(got) != 5 || got[2].ID != "anthropic/claude-opus-5-5" || got[4].ID != "anthropic/claude-haiku-5-5" {
		t.Fatalf("Anthropic catalog = %#v", got)
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

func TestMcodeMaxTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"plain-model","max_output_tokens":64000}]}`)
	}))
	t.Cleanup(server.Close)
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gateway-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	if got := mcodeMaxTokens(cfg, secrets, "magpie-mcode/plain-model"); got != 64000 {
		t.Fatalf("MaxTokens = %d, want 64000", got)
	}
	if got := mcodeMaxTokens(cfg, secrets, "magpie-mcode/no-cache"); got != 0 {
		t.Fatalf("缓存未命中的 MaxTokens = %d, want 0", got)
	}
	if got := mcodeMaxTokens(cfg, secrets, "anthropic/claude-sonnet-5-5"); got != 0 {
		t.Fatalf("Anthropic 的 MaxTokens = %d, want 0", got)
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
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("jp"): "gw-key"})
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gw-key"); err != nil {
		t.Fatalf("填充模型缓存失败: %v", err)
	}
	targets := mcodeTargets(cfg, secrets)
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	prefix := targets[0].ProviderID
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
