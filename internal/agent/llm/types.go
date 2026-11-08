package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Message struct {
	Role string `json:"role"`
	Content []Part `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	IsError bool `json:"is_error,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
}

type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data string `json:"data,omitempty"`
}

type ToolCall struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolSpec struct {
	Name string `json:"name"`
	Description string `json:"description"`
	Parameters json.RawMessage `json:"parameters"`
}

type Request struct {
	Model string `json:"model"`
	System string `json:"system,omitempty"`
	Messages []Message `json:"messages"`
	Tools []ToolSpec `json:"tools,omitempty"`
	Effort string `json:"effort,omitempty"`
	MaxTokens int `json:"max_tokens,omitempty"`
}

type Usage struct {
	Input int `json:"input"`
	Output int `json:"output"`
	CacheRead int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

type Response struct {
	Message Message `json:"message"`
	Usage Usage `json:"usage"`
	StopReason string `json:"stop_reason"`
}

type Delta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Client interface {
	Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
}

type APIError struct {
	Status int `json:"status"`
	Body string `json:"body"`
	Retryable bool `json:"retryable"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: API error (status %d): %s", e.Status, e.Body)
}

func IsContextOverflow(err error) bool {
	if err == nil { return false }
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"context_length_exceeded", "maximum context length", "prompt is too long", "too many tokens", "context window exceeded", "context length exceeded"} {
		if strings.Contains(text, marker) { return true }
	}
	return false
}
