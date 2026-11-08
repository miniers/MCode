package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/magpieinject"
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

	out, err := generateText(context.Background(), cfg, secrets, config.ProviderMcode, "magpie-mcode/claude-sonnet", "high", "给这段对话起标题")
	if err != nil {
		t.Fatal(err)
	}
	if out != "会话标题" {
		t.Fatalf("输出 = %q，期望 %q", out, "会话标题")
	}
	if path != "/chat/completions" || auth != "Bearer gateway-key" || body["model"] != "claude-sonnet" {
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
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	for _, model := range []string{"", "default"} {
		out, err := generateText(context.Background(), cfg, secrets, config.ProviderMcode, model, "", "给这段对话起标题")
		if err != nil {
			t.Fatalf("模型 %q：%v", model, err)
		}
		if out != "会话标题" {
			t.Fatalf("模型 %q 的输出 = %q", model, out)
		}
		if body["model"] != magpieinject.DefaultModel().ID {
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
