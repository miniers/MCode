package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/config"
	"mcode/internal/harness"
)

// anthropicTestStream 是最小的一条 Anthropic SSE 流，足以让客户端正常收尾。
const anthropicTestStream = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

func anthropicHarnessAvailable(h harness.Harness) (bool, string) {
	if a, ok := h.(interface{ Available() (bool, string) }); ok {
		return a.Available()
	}
	return true, ""
}

func anthropicHarnessModelIDs(h harness.Harness) []string {
	if m, ok := h.(interface{ ModelIDs() []string }); ok {
		return m.ModelIDs()
	}
	return nil
}

// 只在旧账号 mcode.anthropicApiKey 里放 Key 也应可用，请求打到 /v1/messages 并带 x-api-key。
func TestAnthropicEngineHarness_UsesLegacyKey(t *testing.T) {
	hits := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.URL.Path + " " + r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicTestStream)
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("ANTHROPIC_API_KEY", "")

	secrets := config.NewMemorySecretStore()
	if err := secrets.Set("mcode.anthropicApiKey", "sk-ant-legacy"); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	h := newAnthropicEngineHarness(config.Config{}, secrets, t.TempDir(), providerMCP{})
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	if ok, reason := anthropicHarnessAvailable(h); !ok {
		t.Fatalf("旧账号有 Key 时应可用: %s", reason)
	}
	wantModels := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}
	if got := anthropicHarnessModelIDs(h); !reflect.DeepEqual(got, wantModels) {
		t.Fatalf("模型列表不匹配: got %v, want %v", got, wantModels)
	}

	client, modelID, err := resolveAnthropicClient(config.Config{}, secrets, "default")
	if err != nil {
		t.Fatalf("resolveAnthropicClient: %v", err)
	}
	if modelID != "claude-opus-5-5" {
		t.Fatalf("default 应取来源默认模型，got %q", modelID)
	}
	req := llm.Request{Model: modelID, Messages: []llm.Message{{
		Role:    "user",
		Content: []llm.Part{{Type: "text", Text: "hi"}},
	}}}
	if _, err := client.Stream(context.Background(), req, func(llm.Delta) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := <-hits; got != "/v1/messages sk-ant-legacy" {
		t.Fatalf("请求不匹配: %q", got)
	}
}

// 带 anthropic/ 前缀的模型要按旧引擎的规则去掉前缀；空模型回落到默认模型。
func TestResolveAnthropicClient_ModelIDs(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set("mcode.anthropicApiKey", "sk-ant-legacy"); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	if _, modelID, err := resolveAnthropicClient(config.Config{}, secrets, " anthropic/claude-sonnet-5-5 "); err != nil || modelID != "claude-sonnet-5-5" {
		t.Fatalf("前缀未去除: modelID=%q, err=%v", modelID, err)
	}
	if _, modelID, err := resolveAnthropicClient(config.Config{}, secrets, ""); err != nil || modelID != "claude-opus-5-5" {
		t.Fatalf("空模型应回落到默认模型: modelID=%q, err=%v", modelID, err)
	}
}

// 新旧账号都没有 Key 时来源不可用。
func TestAnthropicEngineHarness_UnavailableWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	secrets := config.NewMemorySecretStore()

	h := newAnthropicEngineHarness(config.Config{}, secrets, t.TempDir(), providerMCP{})
	t.Cleanup(func() { setMcodeAgentRuntime("", nil) })
	ok, reason := anthropicHarnessAvailable(h)
	if ok || reason != "未配置 API Key" {
		t.Fatalf("无 Key 时应不可用: ok=%v, reason=%q", ok, reason)
	}
	if got := anthropicHarnessModelIDs(h); len(got) != 0 {
		t.Fatalf("无 Key 时不应有模型: %v", got)
	}
	if _, _, err := resolveAnthropicClient(config.Config{}, secrets, ""); err == nil {
		t.Fatal("无 Key 时应返回错误")
	}
}
