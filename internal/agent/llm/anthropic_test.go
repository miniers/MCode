package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicUserAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{name: "default", want: UserAgent},
		{name: "override", headers: map[string]string{"User-Agent": "custom-agent"}, want: "custom-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("User-Agent"); got != tc.want {
					t.Errorf("User-Agent = %q, want %q", got, tc.want)
				}
				fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			_, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL, Headers: tc.headers}).Stream(context.Background(), Request{Model: "test"}, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnthropicStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":30,\"cache_creation_input_tokens\":40}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"想\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"法\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"run\",\"input\":{}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\":\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1}\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	var deltas []Delta
	response, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "test"}, func(delta Delta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Reasoning != "想法" || response.Message.ReasoningSignature != "sig" || len(response.Message.Content) != 1 || response.Message.Content[0].Text != "你好" {
		t.Fatalf("message: %+v", response.Message)
	}
	if len(deltas) != 3 || deltas[0] != (Delta{Type: "reasoning", Text: "想"}) || deltas[2] != (Delta{Type: "text", Text: "你好"}) {
		t.Fatalf("deltas: %+v", deltas)
	}
	calls := response.Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "t1" || calls[0].Name != "run" || string(calls[0].Arguments) != `{"x":1}` {
		t.Fatalf("calls: %+v", calls)
	}
	if response.StopReason != "tool_use" || response.Usage != (Usage{Input: 12, Output: 7, CacheRead: 30, CacheWrite: 40}) {
		t.Fatalf("response: %+v", response)
	}
}

func TestAnthropicAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name          string
		bearer        bool
		authorization string
		apiKey        string
	}{
		{name: "bearer", bearer: true, authorization: "Bearer key"},
		{name: "api-key", apiKey: "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != tc.authorization || r.Header.Get("x-api-key") != tc.apiKey {
					t.Errorf("authentication headers: %v", r.Header)
				}
				fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			_, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL, APIKey: "key", BearerAuth: tc.bearer}).Stream(context.Background(), Request{Model: "test"}, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnthropicRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("X-Extra") != "value" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		system := body["system"].([]any)[0].(map[string]any)
		if system["text"] != "system" || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
			t.Errorf("system: %+v", system)
		}
		thinking := body["thinking"].(map[string]any)
		if thinking["budget_tokens"] != float64(32000) || body["max_tokens"] != float64(40192) || body["stream"] != true || body["temperature"] != nil {
			t.Errorf("body: %+v", body)
		}
		messages := body["messages"].([]any)
		if len(messages) != 3 {
			t.Fatalf("messages: %+v", messages)
		}
		user := messages[0].(map[string]any)["content"].([]any)
		image := user[1].(map[string]any)["source"].(map[string]any)
		if image["type"] != "base64" || image["media_type"] != "image/png" || image["data"] != "YQ==" {
			t.Errorf("image: %+v", image)
		}
		assistant := messages[1].(map[string]any)["content"].([]any)
		if assistant[0].(map[string]any)["signature"] != "sig" || assistant[0].(map[string]any)["thinking"] != "想" || assistant[2].(map[string]any)["input"].(map[string]any)["x"] != float64(1) {
			t.Errorf("assistant: %+v", assistant)
		}
		last := messages[2].(map[string]any)
		blocks := last["content"].([]any)
		if last["role"] != "user" || len(blocks) != 2 || blocks[0].(map[string]any)["tool_use_id"] != "t1" || blocks[1].(map[string]any)["is_error"] != true || blocks[1].(map[string]any)["cache_control"].(map[string]any)["type"] != "ephemeral" {
			t.Errorf("tool results: %+v", last)
		}
		tools := body["tools"].([]any)
		if tools[0].(map[string]any)["input_schema"].(map[string]any)["type"] != "object" {
			t.Errorf("tools: %+v", tools)
		}
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := NewAnthropic(AnthropicConfig{BaseURL: server.URL + "/v1", APIKey: "key", Headers: map[string]string{"X-Extra": "value"}})
	response, err := client.Stream(context.Background(), Request{Model: "test", System: "system", Effort: "xhigh", MaxTokens: 100, Messages: []Message{
		{Role: "user", Content: []Part{{Type: "text", Text: "看"}, {Type: "image", MimeType: "image/png", Data: "YQ=="}}},
		{Role: "assistant", Reasoning: "想", ReasoningSignature: "sig", Content: []Part{{Type: "text", Text: "好"}}, ToolCalls: []ToolCall{{ID: "t1", Name: "run", Arguments: json.RawMessage(`{"x":1}`)}, {ID: "t2", Name: "run"}}},
		{Role: "tool", ToolCallID: "t1", Content: []Part{{Type: "text", Text: "done"}}},
		{Role: "tool", ToolCallID: "t2", IsError: true, Content: []Part{{Type: "text", Text: "failed"}}},
	}, Tools: []ToolSpec{{Name: "run", Parameters: json.RawMessage(`{"type":"object"}`)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != "length" || len(response.Message.Content) != 1 || response.Message.Content[0].Text != "ok" {
		t.Fatalf("response: %+v", response)
	}
}

func TestDefaultAnthropicMaxTokens(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int
	}{
		{"claude-3-opus-20240229", 4096},
		{"anthropic/claude-3-sonnet-20240229", 4096},
		{"claude-3-haiku-20240307", 4096},
		{"claude-3-5-sonnet-20241022", 8192},
		{"anthropic/claude-3-5-haiku-20241022", 8192},
		{"claude-sonnet-4-5", 32000},
		{"test", 32000},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := defaultAnthropicMaxTokens(tc.model); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAnthropicDefaultMaxTokensInRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["max_tokens"] != float64(8192) {
			t.Errorf("max_tokens: %+v", body["max_tokens"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	_, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "anthropic/claude-3-5-sonnet-20241022"}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicTruncatedStreamRetryable(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"eof", "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"},
		{"invalid-json", "data: {bad}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "test"}, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !apiErr.Retryable {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestAnthropicErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "http", status: 529, body: strings.Repeat("x", 3000)},
		{name: "stream", status: 200, body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL}).Stream(context.Background(), Request{}, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !apiErr.Retryable || (tc.status == 529 && (apiErr.Status != 529 || len(apiErr.Body) != 2048)) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestAnthropicStreamErrorStatus(t *testing.T) {
	for kind, status := range map[string]int{"rate_limit_error": 429, "overloaded_error": 529} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":%q,\"message\":\"busy\"}}\n\n", kind)
		}))
		_, err := NewAnthropic(AnthropicConfig{BaseURL: server.URL}).Stream(context.Background(), Request{}, nil)
		server.Close()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status || !apiErr.Retryable {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
