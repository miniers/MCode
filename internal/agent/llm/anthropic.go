package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// AnthropicConfig 配置 Anthropic Messages 客户端。
type AnthropicConfig struct {
	BaseURL    string
	APIKey     string
	Headers    map[string]string
	HTTPClient *http.Client
	// BearerAuth 为真时用 Authorization: Bearer 发送密钥（与 Magpie 网关和 claude CLI 的 ANTHROPIC_AUTH_TOKEN 一致），否则用 x-api-key（官方 Anthropic）。
	BearerAuth bool
}

type anthropicClient struct {
	cfg AnthropicConfig
}

// NewAnthropic 创建 Anthropic Messages 客户端。
func NewAnthropic(cfg AnthropicConfig) Client {
	return &anthropicClient{cfg: cfg}
}

// defaultAnthropicMaxTokens 按模型返回默认输出上限。
// 老模型（claude-3-opus / sonnet / haiku）上限是 4096，
// claude-3-5 系列是 8192，其余新模型用 32000；超出上限会让请求直接 400。
func defaultAnthropicMaxTokens(model string) int {
	name := strings.ToLower(model)
	// 去掉 anthropic/ 这类 provider 前缀。
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	switch {
	case strings.HasPrefix(name, "claude-3-opus"), strings.HasPrefix(name, "claude-3-sonnet"), strings.HasPrefix(name, "claude-3-haiku"):
		return 4096
	case strings.HasPrefix(name, "claude-3-5-"):
		return 8192
	default:
		return 32000
	}
}

var anthropicModelVersion = regexp.MustCompile(`(?i)^claude-(opus|sonnet|haiku)-([0-9]+)-([0-9]+)($|-|\[)`)

func anthropicParseModelVersion(model string) (int, int, bool) {
	model = model[strings.LastIndex(model, "/")+1:]
	match := anthropicModelVersion.FindStringSubmatch(model)
	if match == nil {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(match[2])
	minor, errMinor := strconv.Atoi(match[3])
	return major, minor, errMajor == nil && errMinor == nil
}

func anthropicAdaptiveThinking(model string) bool {
	major, minor, ok := anthropicParseModelVersion(model)
	return ok && (major >= 5 || major == 4 && minor >= 6)
}

func anthropicRequestBody(req Request) (map[string]any, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultAnthropicMaxTokens(req.Model)
	}
	body := map[string]any{"model": req.Model, "stream": true}
	if req.System != "" {
		body["system"] = []any{map[string]any{"type": "text", "text": req.System, "cache_control": map[string]string{"type": "ephemeral"}}}
	}
	budget := map[string]int{"minimal": 1024, "low": 4096, "medium": 10000, "high": 24000, "xhigh": 32000, "max": 32000}[req.Effort]
	if anthropicAdaptiveThinking(req.Model) && req.Effort != "" {
		effort := req.Effort
		if effort == "minimal" {
			effort = "low"
		} else if major, minor, _ := anthropicParseModelVersion(req.Model); effort == "xhigh" && major == 4 && minor == 6 {
			effort = "high"
		}
		body["thinking"] = map[string]any{"type": "adaptive"}
		body["output_config"] = map[string]any{"effort": effort}
	} else if budget > 0 {
		if maxTokens <= budget {
			maxTokens = budget + 8192
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	body["max_tokens"] = maxTokens

	messages := make([]map[string]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			var text strings.Builder
			for _, part := range msg.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
			block := map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": text.String(), "is_error": msg.IsError}
			if len(messages) > 0 && messages[len(messages)-1]["role"] == "user" {
				last := messages[len(messages)-1]
				last["content"] = append(last["content"].([]map[string]any), block)
			} else {
				messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{block}})
			}
			continue
		}
		blocks := make([]map[string]any, 0, len(msg.Content)+len(msg.ToolCalls)+1)
		if msg.Role == "assistant" {
			if len(msg.ThinkingBlocks) > 0 {
				for _, block := range msg.ThinkingBlocks {
					if block.Type == "redacted_thinking" {
						blocks = append(blocks, map[string]any{"type": block.Type, "data": block.Data})
					} else if block.Type == "thinking" {
						blocks = append(blocks, map[string]any{"type": block.Type, "thinking": block.Thinking, "signature": block.Signature})
					}
				}
			} else if msg.ReasoningSignature != "" {
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": msg.Reasoning, "signature": msg.ReasoningSignature})
			}
		}
		for _, part := range msg.Content {
			switch part.Type {
			case "text":
				if part.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
				}
			case "image":
				if msg.Role == "user" {
					blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": part.MimeType, "data": part.Data}})
				}
			}
		}
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				input := map[string]any{}
				if len(bytes.TrimSpace(call.Arguments)) > 0 {
					if err := json.Unmarshal(call.Arguments, &input); err != nil || input == nil {
						return nil, fmt.Errorf("anthropic: invalid tool input for %q", call.Name)
					}
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
			}
		}
		if msg.Role == "user" && len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": ""})
		}
		messages = append(messages, map[string]any{"role": msg.Role, "content": blocks})
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			blocks := messages[i]["content"].([]map[string]any)
			if len(blocks) > 0 {
				blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			break
		}
	}
	body["messages"] = messages
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
		}
		body["tools"] = tools
	}
	return body, nil
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

func (c *anthropicClient) Stream(ctx context.Context, req Request, onDelta func(Delta)) (response *Response, streamErr error) {
	body, err := anthropicRequestBody(req)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
	if strings.HasSuffix(baseURL, "/v1/messages") {
		baseURL = strings.TrimSuffix(baseURL, "/messages")
	}
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, baseURL+"/messages", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", UserAgent)
	if c.cfg.BearerAuth {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	} else {
		httpReq.Header.Set("x-api-key", c.cfg.APIKey)
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	for key, value := range c.cfg.Headers {
		httpReq.Header.Set(key, value)
	}
	if c.cfg.BearerAuth {
		httpReq.Header.Del("x-api-key")
	} else {
		httpReq.Header.Del("Authorization")
	}
	client := c.cfg.HTTPClient
	if client == nil {
		client = defaultHTTPClient
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &APIError{Body: err.Error(), Retryable: true}
	}
	defer httpResp.Body.Close()
	stopClose := context.AfterFunc(streamCtx, func() { httpResp.Body.Close() })
	defer stopClose()
	idle := newIdleReader(httpResp.Body, cancel)
	defer func() {
		timedOut := idle.stop()
		if ctx.Err() != nil {
			response, streamErr = nil, ctx.Err()
		} else if timedOut {
			response, streamErr = nil, &APIError{Body: "stream idle timeout", Retryable: true}
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(idle, 8192))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, &APIError{Status: httpResp.StatusCode, Body: readErr.Error(), Retryable: true, RetryAfter: retryAfter(httpResp.Header.Get("Retry-After"))}
		}
		status := httpResp.StatusCode
		return nil, &APIError{Status: status, Body: string(body), Retryable: status == 408 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504 || status == 529, RetryAfter: retryAfter(httpResp.Header.Get("Retry-After"))}
	}
	result := &Response{Message: Message{Role: "assistant"}, StopReason: "stop"}
	calls := map[int]*toolCallAccumulator{}
	thinkingBlocks := map[int]*ThinkingBlock{}
	stopped := false
	reader := newSSEReader(idle)
	for {
		event, readErr := reader.readEvent()
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if readErr == io.EOF {
				// 缺少 message_stop 说明流被截断，不能当作正常结束。
				if stopped {
					break
				}
				return nil, &APIError{Body: "anthropic: stream ended unexpectedly", Retryable: true}
			}
			// 中途断网等读取错误同样值得重试。
			return nil, &APIError{Body: readErr.Error(), Retryable: true}
		}
		if event.Data == "" {
			continue
		}
		var payload anthropicEvent
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			// 流式事件解析失败通常是传输被截断，交给 agent 重试。
			return nil, &APIError{Body: "anthropic: invalid stream event: " + err.Error(), Retryable: true}
		}
		eventType := payload.Type
		if eventType == "" {
			eventType = event.Event
		}
		switch eventType {
		case "message_start":
			usage := payload.Message.Usage
			result.Usage = Usage{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite}
		case "content_block_start":
			block := payload.ContentBlock
			switch block.Type {
			case "tool_use":
				calls[payload.Index] = &toolCallAccumulator{ID: block.ID, Name: block.Name}
				if len(block.Input) > 0 && string(block.Input) != "{}" {
					calls[payload.Index].Arguments.Write(block.Input)
				}
			case "text":
				if block.Text != "" {
					result.Message.Content = appendText(result.Message.Content, block.Text)
					if onDelta != nil {
						onDelta(Delta{Type: "text", Text: block.Text})
					}
				}
			case "thinking":
				thinkingBlocks[payload.Index] = &ThinkingBlock{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature}
				result.Message.Reasoning += block.Thinking
				result.Message.ReasoningSignature += block.Signature
				if block.Thinking != "" && onDelta != nil {
					onDelta(Delta{Type: "reasoning", Text: block.Thinking})
				}
			case "redacted_thinking":
				thinkingBlocks[payload.Index] = &ThinkingBlock{Type: block.Type, Data: block.Data}
			}
		case "content_block_delta":
			switch payload.Delta.Type {
			case "text_delta":
				result.Message.Content = appendText(result.Message.Content, payload.Delta.Text)
				if onDelta != nil {
					onDelta(Delta{Type: "text", Text: payload.Delta.Text})
				}
			case "thinking_delta":
				if block := thinkingBlocks[payload.Index]; block != nil {
					block.Thinking += payload.Delta.Thinking
				}
				result.Message.Reasoning += payload.Delta.Thinking
				if onDelta != nil {
					onDelta(Delta{Type: "reasoning", Text: payload.Delta.Thinking})
				}
			case "signature_delta":
				if block := thinkingBlocks[payload.Index]; block != nil {
					block.Signature += payload.Delta.Signature
				}
				result.Message.ReasoningSignature += payload.Delta.Signature
			case "input_json_delta":
				if call := calls[payload.Index]; call != nil {
					call.Arguments.WriteString(payload.Delta.PartialJSON)
				}
			}
		case "message_delta":
			result.Usage.Output = payload.Usage.Output
			switch payload.Delta.StopReason {
			case "end_turn", "stop_sequence", "pause_turn":
				result.StopReason = "stop"
			case "tool_use":
				result.StopReason = "tool_use"
			case "max_tokens":
				result.StopReason = "length"
			default:
				if payload.Delta.StopReason != "" {
					result.StopReason = payload.Delta.StopReason
				}
			}
		case "message_stop":
			stopped = true
		case "error":
			status := 0
			switch payload.Error.Type {
			case "rate_limit_error":
				status = 429
			case "overloaded_error":
				status = 529
			case "api_error":
				status = 500
			case "timeout_error":
				status = 408
			}
			return nil, &APIError{Status: status, Body: payload.Error.Type + ": " + payload.Error.Message, Retryable: status != 0}
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	indices := make([]int, 0, len(thinkingBlocks))
	for index := range thinkingBlocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		result.Message.ThinkingBlocks = append(result.Message.ThinkingBlocks, *thinkingBlocks[index])
	}
	result.Message.ToolCalls = sortedToolCalls(calls)
	for i := range result.Message.ToolCalls {
		if len(result.Message.ToolCalls[i].Arguments) == 0 {
			result.Message.ToolCalls[i].Arguments = json.RawMessage(`{}`)
		}
	}
	return result, nil
}
