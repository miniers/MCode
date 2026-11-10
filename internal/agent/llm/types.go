package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Message 表示一条对话消息。
type Message struct {
	Role               string          `json:"role"`
	Content            []Part          `json:"content,omitempty"`
	ToolCalls          []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID         string          `json:"tool_call_id,omitempty"`
	IsError            bool            `json:"is_error,omitempty"`
	Reasoning          string          `json:"reasoning,omitempty"`
	ReasoningSignature string          `json:"reasoning_signature,omitempty"`
	ThinkingBlocks     []ThinkingBlock `json:"thinking_blocks,omitempty"`
}

// ThinkingBlock 保存 Anthropic 返回的原始思考块。
type ThinkingBlock struct {
	Type      string `json:"type,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

// Part 表示消息中的文本或图片。
type Part struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

// ToolCall 表示模型发起的工具调用。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolSpec 描述可供模型调用的工具。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request 描述一次模型请求。
type Request struct {
	Model     string     `json:"model"`
	System    string     `json:"system,omitempty"`
	Messages  []Message  `json:"messages"`
	Tools     []ToolSpec `json:"tools,omitempty"`
	Effort    string     `json:"effort,omitempty"`
	MaxTokens int        `json:"max_tokens,omitempty"`
	// CacheBreakpoints 是 Messages 的下标，在消息末尾为不再变化的摘要块打缓存断点；不支持的 provider 忽略。
	CacheBreakpoints []int `json:"-"`
}

// Usage 记录模型请求的 token 用量。
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Response 包含模型回复和用量。
type Response struct {
	Message    Message `json:"message"`
	Usage      Usage   `json:"usage"`
	StopReason string  `json:"stop_reason"`
}

// Delta 表示流式输出的增量。
type Delta struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Reasoning 是思考增量的原文；Type 为 "reasoning" 时与 Text 相同。
	Reasoning string `json:"reasoning,omitempty"`
}

// Client 定义流式模型调用接口。
type Client interface {
	Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
}

// APIError 描述模型服务返回的错误。
type APIError struct {
	Status     int           `json:"status"`
	Body       string        `json:"body"`
	Retryable  bool          `json:"retryable"`
	RetryAfter time.Duration `json:"-"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: API error (status %d): %s", e.Status, e.Body)
}

// RetryAfterOf 返回服务端建议的重试等待时间。
func RetryAfterOf(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

// IsContextOverflow 判断错误是否表示上下文超出限制。
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 429 || apiErr.Status == 529) {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"rate limit", "per minute", "per min", "tpm", "rpm"} {
		if strings.Contains(text, marker) {
			return false
		}
	}
	for _, marker := range []string{"context_length_exceeded", "maximum context length", "prompt is too long", "too many tokens", "context window exceeded", "context length exceeded", "input is too long", "exceeds the context window", "context_window_exceeded", "prompt too long"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
