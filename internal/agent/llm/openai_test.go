package llm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你\",\"reasoning_content\":\"想\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\",\"reasoning\":\"法\",\"tool_calls\":[{\"index\":1,\"id\":\"b\",\"function\":{\"name\":\"second\",\"arguments\":\"{\\\"y\\\":\"}},{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"first\",\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}},{\"index\":1,\"function\":{\"arguments\":\"2}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":30}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	var deltas []Delta
	response, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "test"}, func(d Delta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Message.Content) != 1 || response.Message.Content[0].Text != "你好" || response.Message.Reasoning != "想法" {
		t.Fatalf("message: %+v", response.Message)
	}
	if len(deltas) != 4 || deltas[0] != (Delta{Type: "text", Text: "你"}) || deltas[1] != (Delta{Type: "reasoning", Text: "想", Reasoning: "想"}) || deltas[3] != (Delta{Type: "reasoning", Text: "法", Reasoning: "法"}) {
		t.Fatalf("deltas: %+v", deltas)
	}
	calls := response.Message.ToolCalls
	if len(calls) != 2 || calls[0].ID != "a" || calls[0].Name != "first" || string(calls[0].Arguments) != `{"x":1}` || calls[1].ID != "b" || string(calls[1].Arguments) != `{"y":2}` {
		t.Fatalf("calls: %+v", calls)
	}
	if response.StopReason != "tool_use" || response.Usage != (Usage{Input: 70, Output: 7, CacheRead: 30}) {
		t.Fatalf("response: %+v", response)
	}
}

func TestOpenAIResponseAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, stop string
		status                        int
	}{
		{"length", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", "length", 200},
		{"json", "application/json", `{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`, "stop", 200},
		{"rate", "application/json", `rate limited`, "", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			response, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL}).Stream(context.Background(), Request{}, nil)
			if tc.status == 429 {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || !apiErr.Retryable || apiErr.Status != 429 {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.StopReason != tc.stop || len(response.Message.Content) != 1 || response.Message.Content[0].Text != "x" {
				t.Fatalf("response: %+v", response)
			}
		})
	}
}

func TestOpenAIRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Session") != "session" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] != "high" || body["stream"] != true || body["stream_options"].(map[string]any)["include_usage"] != true {
			t.Errorf("body: %+v", body)
		}
		messages := body["messages"].([]any)
		user := messages[1].(map[string]any)["content"].([]any)
		image := user[1].(map[string]any)["image_url"].(map[string]any)["url"]
		if image != "data:image/png;base64,YQ==" || messages[3].(map[string]any)["tool_call_id"] != "call1" {
			t.Errorf("messages: %+v", messages)
		}
		calls := messages[2].(map[string]any)["tool_calls"].([]any)
		if calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"x":1}` || len(body["tools"].([]any)) != 1 {
			t.Errorf("body: %+v", body)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client := NewOpenAI(OpenAIConfig{BaseURL: server.URL + "/v1", APIKey: "key", Headers: map[string]string{"X-Session": "session"}})
	_, err := client.Stream(context.Background(), Request{Model: "test", System: "system", Effort: "xhigh", Messages: []Message{
		{Role: "user", Content: []Part{{Type: "text", Text: "看"}, {Type: "image", MimeType: "image/png", Data: "YQ=="}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call1", Name: "run", Arguments: json.RawMessage(`{"x":1}`)}}},
		{Role: "tool", ToolCallID: "call1", Content: []Part{{Type: "text", Text: "done"}}},
	}, Tools: []ToolSpec{{Name: "run", Parameters: json.RawMessage(`{"type":"object"}`)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIRequestReasoningEcho(t *testing.T) {
	for _, echo := range []bool{true, false} {
		t.Run(fmt.Sprintf("echo=%t", echo), func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			client := NewOpenAI(OpenAIConfig{BaseURL: server.URL, EchoReasoning: echo})
			// 模型名不带 deepseek，验证回传只由 EchoReasoning 决定。
			_, err := client.Stream(context.Background(), Request{Model: "test", Messages: []Message{
				{Role: "assistant", Content: []Part{{Type: "text", Text: "done"}}, Reasoning: "先想"},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "call1", Name: "run", Arguments: json.RawMessage(`{}`)}}},
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			messages := body["messages"].([]any)
			first := messages[0].(map[string]any)
			second := messages[1].(map[string]any)
			if echo {
				if first["reasoning_content"] != "先想" || second["reasoning_content"] != "" {
					t.Fatalf("echoed messages: %+v", messages)
				}
				return
			}
			if _, ok := first["reasoning_content"]; ok {
				t.Fatalf("reasoning_content should be omitted: %+v", first)
			}
			if _, ok := second["reasoning_content"]; ok {
				t.Fatalf("reasoning_content should be omitted: %+v", second)
			}
		})
	}
}

func TestOpenAIMissingToolCallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()
	response, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL}).Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("calls: %+v", response.Message.ToolCalls)
	}
	id := response.Message.ToolCalls[0].ID
	if !strings.HasPrefix(id, "call_") {
		t.Fatalf("ID: %q", id)
	}
	if decoded, err := hex.DecodeString(strings.TrimPrefix(id, "call_")); err != nil || len(decoded) != 16 {
		t.Fatalf("ID: %q", id)
	}
}

func TestIsContextOverflow(t *testing.T) {
	for _, text := range []string{"context_length_exceeded", "Maximum context length", "prompt is too long", "too many tokens"} {
		if !IsContextOverflow(errors.New(text)) {
			t.Errorf("not recognized: %s", text)
		}
	}
	if IsContextOverflow(nil) || IsContextOverflow(errors.New(strings.Repeat("x", 4))) {
		t.Fatal("false positive")
	}
}
