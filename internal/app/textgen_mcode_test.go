package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/textgen"
)

// mcodeTextgenStream 返回一段 OpenAI 流式 SSE，内容是两段文本。
const mcodeTextgenStream = "data: {\"choices\":[{\"delta\":{\"content\":\" 会话\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"标题 \"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func TestMcodeGenerateTextViaDispatch(t *testing.T) {
	var path, auth, affinity string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		affinity = r.Header.Get("session_id")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, mcodeTextgenStream)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	// 非 Claude 模型保留 OpenAI chat completions 的超时与 effort 透传。
	out, err := generateText(context.Background(), cfg, secrets, config.ProviderMcode, "magpie-mcode/plain-model", "high", "给这段对话起标题")
	if err != nil {
		t.Fatal(err)
	}
	if out != "会话标题" {
		t.Fatalf("输出 = %q，期望 %q", out, "会话标题")
	}
	if path != "/chat/completions" || auth != "Bearer gateway-key" || body["model"] != "plain-model" {
		t.Fatalf("请求 = %q %q %v", path, auth, body["model"])
	}
	if affinity != "" {
		t.Fatalf("一次性文本生成不应带会话亲和头：%q", affinity)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("请求不应带 tools：%v", body["tools"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("请求不应设置 max_tokens：%v", body["max_tokens"])
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v，期望 high", body["reasoning_effort"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "给这段对话起标题" {
		t.Fatalf("消息 = %v", msg)
	}
}

func TestMcodeGenerateTextDefaultModel(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, mcodeTextgenStream)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {
		DefaultEndpointID: "edge",
		DefaultModel:      "plain-model",
	}}
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	for _, model := range []string{"", "default"} {
		out, err := generateText(context.Background(), cfg, secrets, config.ProviderMcode, model, "", "给这段对话起标题")
		if err != nil {
			t.Fatalf("模型 %q：%v", model, err)
		}
		if out != "会话标题" {
			t.Fatalf("模型 %q 的输出 = %q", model, out)
		}
		if body["model"] != "plain-model" {
			t.Fatalf("模型 %q 请求的模型 = %v", model, body["model"])
		}
		if _, ok := body["reasoning_effort"]; ok {
			t.Fatalf("模型 %q 未指定 effort 时不应带 reasoning_effort：%v", model, body["reasoning_effort"])
		}
	}
}

func TestMcodeGenerateTextUnknownModel(t *testing.T) {
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: "https://gateway.example/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	_, err := generateText(context.Background(), cfg, secrets, config.ProviderMcode, "ghost/model", "", "给这段对话起标题")
	if err == nil || !strings.Contains(err.Error(), "没有找到模型 ghost/model 对应的 Magpie 端点") {
		t.Fatalf("未知模型错误 = %v", err)
	}
}

func TestMcodeGenerateTextWithoutEndpoint(t *testing.T) {
	_, err := mcodeGenerateText(context.Background(), config.Defaults(), nil, "", "", "给这段对话起标题")
	if err == nil || !strings.Contains(err.Error(), "没有配置可用的 Magpie 端点") {
		t.Fatalf("未配置端点错误 = %v", err)
	}
}

// withoutCLI 模拟没装 claude、codex、pi 的环境。
func withoutCLI(t *testing.T) {
	t.Helper()
	restore := textgen.SetLookPathForTest(func(string) (string, error) {
		return "", errors.New("not found")
	})
	t.Cleanup(restore)
}

func TestGenerateTextWithoutCLIFallsBackToMcode(t *testing.T) {
	withoutCLI(t)
	var body map[string]any
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, mcodeTextgenStream)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	cfg.Providers.Mcode.Enabled = true
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {
		DefaultEndpointID: "edge",
		DefaultModel:      "plain-model",
	}}
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	// provider 为空时默认按 claude 处理，与 claude / codex 走同一条回退规则。
	for _, provider := range []string{"claude", "codex", ""} {
		out, err := generateText(context.Background(), cfg, secrets, provider, "", "", "给这段对话起标题")
		if err != nil {
			t.Fatalf("provider %q：%v", provider, err)
		}
		if out != "会话标题" {
			t.Fatalf("provider %q 的输出 = %q", provider, out)
		}
		if body["model"] != "plain-model" {
			t.Fatalf("provider %q 请求的模型 = %v，期望 mcode 默认模型", provider, body["model"])
		}
	}
	if hits != 3 {
		t.Fatalf("mcode 请求次数 = %d，期望 3", hits)
	}
}

func TestGenerateTextThirdLayerFallsBackToMcode(t *testing.T) {
	withoutCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, mcodeTextgenStream)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	cfg.Providers.Mcode.Enabled = true
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{config.ProviderMcode: {
		DefaultEndpointID: "edge",
		DefaultModel:      "plain-model",
	}}
	cfg.Providers.Claude.Bin = "/nonexistent/claude"
	cfg.Providers.Codex.Bin = "/nonexistent/codex"
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	out, err := generateText(context.Background(), cfg, secrets, "custom-unknown", "", "", "给这段对话起标题")
	if err != nil {
		t.Fatal(err)
	}
	if out != "会话标题" {
		t.Fatalf("输出 = %q", out)
	}
}

func TestGenerateTextWithoutMcodeKeepsCLIError(t *testing.T) {
	withoutCLI(t)
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, mcodeTextgenStream)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL})
	cfg.Providers.Mcode.Enabled = false
	cfg.Providers.Claude.Bin = "/nonexistent/claude"
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	_, err := generateText(context.Background(), cfg, secrets, "claude", "", "", "给这段对话起标题")
	if err == nil {
		t.Fatalf("mcode 停用时不应回退到 mcode")
	}
	if hits != 0 {
		t.Fatalf("mcode 停用时不应请求 Magpie，实际 %d 次", hits)
	}
}

func TestGenerateTextAllFallbacksFailIncludesErrors(t *testing.T) {
	withoutCLI(t)
	cfg := mcodeTestConfig()
	cfg.Providers.Mcode.Enabled = true
	cfg.Providers.Claude.Bin = "/nonexistent/claude"
	cfg.Providers.Codex.Bin = "/nonexistent/codex"

	_, err := generateText(context.Background(), cfg, nil, "custom-unknown", "", "", "给这段对话起标题")
	if err == nil {
		t.Fatal("三个后端都失败时应返回错误")
	}
	for _, want := range []string{"claude / codex / mcode 也失败", "/nonexistent/claude", "/nonexistent/codex", "没有配置可用的 Magpie 端点"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误 %q 缺少 %q", err, want)
		}
	}
}
