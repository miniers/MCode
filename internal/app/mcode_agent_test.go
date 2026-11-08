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
	client, model, err := resolveMcodeClient(cfg, secrets, "magpie-mcode/plain-model", "thread-1")
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
	client, model, err := resolveMcodeClient(cfg, secrets, "magpie-mcode/claude-sonnet", "thread-1")
	if err != nil || model != "claude-sonnet" {
		t.Fatalf("resolve = %q, %v", model, err)
	}
	_, err = client.Stream(context.Background(), llm.Request{Model: model, Messages: []llm.Message{{Role: "user", Content: []llm.Part{{Type: "text", Text: "hello"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" || apiKey != "gateway-key" || auth != "" {
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

func TestMcodeModelCatalogUsesMcodeSelection(t *testing.T) {
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

	// 没有 mcode 勾选时沿用 pi 的勾选。
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderPi: {
		Selections: []config.MagpieSelection{{EndpointID: "edge", Models: []string{"claude-sonnet-4.5"}}},
	}}
	got := mcodeModelsForConfig(cfg, secrets)
	if len(got) != 1 || got[0].ID != prefix+"/claude-sonnet-4.5" || !got[0].IsDefault {
		t.Fatalf("沿用 pi 的目录 = %#v", got)
	}

	// 有 mcode 勾选时只显示勾选的模型，默认模型排在最前。
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
	_, modelID, err := resolveMcodeClient(cfg, secrets, got[1].ID, "")
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

func TestMcodeModelCatalogDefaultsOnCacheMiss(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"edge-model"}]}`)
	}))
	t.Cleanup(edge.Close)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"other-model"}]}`)
	}))
	t.Cleanup(other.Close)
	cfg := mcodeTestConfig(
		config.CustomEndpointConfig{ID: "edge", Name: "Edge", Enabled: true, Magpie: true, BaseURL: edge.URL + "/v1"},
		config.CustomEndpointConfig{ID: "other", Name: "Other", Enabled: true, Magpie: true, BaseURL: other.URL + "/v1"},
		config.CustomEndpointConfig{ID: "unused", Name: "Unused", Enabled: true, Magpie: true, BaseURL: other.URL + "/v1"},
	)
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {
		Selections:        []config.MagpieSelection{{EndpointID: "edge", Models: []string{"plain-model", "other-model"}}},
		DefaultEndpointID: "edge",
		DefaultModel:      "plain-model",
	}}
	secrets := mcodeTestSecrets(map[string]string{
		config.CustomEndpointSecretAccount("edge"):   "edge-key",
		config.CustomEndpointSecretAccount("other"):  "other-key",
		config.CustomEndpointSecretAccount("unused"): "unused-key",
	})
	got := mcodeModelsForConfig(cfg, secrets)
	// unused 端点一个都没勾选，不出行。
	if len(got) != 2 || got[0].ID != "edge/plain-model" || got[1].ID != "edge/other-model" {
		t.Fatalf("缓存未命中的目录 = %#v", got)
	}
	if !got[0].IsDefault || got[1].IsDefault {
		t.Fatalf("默认标记 = %#v", got)
	}
	if strings.Join(got[0].Efforts, ",") != "low,medium,high,xhigh,max" {
		t.Fatalf("缺元数据时的档位 = %v", got[0].Efforts)
	}
	if got[0].ContextLimit != magpieinject.DefaultModel().ContextWindow {
		t.Fatalf("缺元数据时的窗口 = %d", got[0].ContextLimit)
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
