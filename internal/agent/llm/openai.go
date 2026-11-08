package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// OpenAIConfig 配置 OpenAI 兼容客户端。
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
	// Headers 可设置 Magpie 的会话亲和请求头。
	Headers    map[string]string
	HTTPClient *http.Client
	// EchoReasoning 为 true 时在 assistant 消息上回传 reasoning_content，
	// DeepSeek 系思考模型的工具轮次需要它。
	EchoReasoning bool
	// Effort 可定制 reasoning_effort 的取值：返回假表示不发送 effort，为 nil 时使用内置规则。
	// 钩子返回的值原样写入 reasoning_effort，不再归一化，便于网关透传 xhigh、max。
	Effort func(model, effort string) (string, bool)
}

type openAIClient struct{ cfg OpenAIConfig }

// NewOpenAI 创建 OpenAI 兼容客户端。
func NewOpenAI(cfg OpenAIConfig) Client {
	return &openAIClient{cfg: cfg}
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type chatToolCall struct {
	Index    int          `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function chatFunction `json:"function"`
}
type chatDelta struct {
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	Reasoning        string         `json:"reasoning"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
}
type chatPayload struct {
	Choices []struct {
		Index        int       `json:"index"`
		Delta        chatDelta `json:"delta"`
		Message      chatDelta `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Details    struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type toolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// feedToolCalls 按 index 拼接工具调用的参数片段。
func feedToolCalls(acc map[int]*toolCallAccumulator, calls []chatToolCall) {
	for _, call := range calls {
		a := acc[call.Index]
		if a == nil {
			a = &toolCallAccumulator{}
			acc[call.Index] = a
		}
		if call.ID != "" {
			a.ID = call.ID
		}
		if call.Function.Name != "" {
			a.Name = call.Function.Name
		}
		a.Arguments.WriteString(call.Function.Arguments)
	}
}

func sortedToolCalls(acc map[int]*toolCallAccumulator) []ToolCall {
	indices := make([]int, 0, len(acc))
	for i := range acc {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	var result []ToolCall
	for _, i := range indices {
		a := acc[i]
		result = append(result, ToolCall{ID: a.ID, Name: a.Name, Arguments: json.RawMessage(a.Arguments.String())})
	}
	return result
}

// chatCompletionsURL 构造 Chat Completions 地址：
// BaseURL 已经指向 /chat/completions 时直接使用，避免重复拼接路径。
func chatCompletionsURL(baseURL string) string {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(u, "/chat/completions") {
		return u
	}
	return u + "/chat/completions"
}

// defaultEffort 是内置的推理强度规则：
// deepseek-reasoner 不接受 reasoning_effort；off 不下发；
// minimal 归一到 low，xhigh 与 max 归一到 high，其余原样。
func defaultEffort(model, effort string) (string, bool) {
	if effort == "" || effort == "off" || strings.Contains(strings.ToLower(model), "deepseek-reasoner") {
		return "", false
	}
	if effort == "minimal" {
		effort = "low"
	}
	if effort == "xhigh" || effort == "max" {
		effort = "high"
	}
	return effort, true
}

// resolveEffort 优先使用配置的钩子，未配置时走内置规则。
func (c *openAIClient) resolveEffort(model, effort string) (string, bool) {
	if c.cfg.Effort != nil {
		return c.cfg.Effort(model, effort)
	}
	return defaultEffort(model, effort)
}

func requestBody(req Request, echoReasoning bool, resolveEffort func(model, effort string) (string, bool)) map[string]any {
	messages := make([]any, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	lowerModel := strings.ToLower(req.Model)
	deepseek := strings.Contains(lowerModel, "deepseek")
	// deepseek-reasoner 既不接受 reasoning_effort，也不接受 thinking。
	reasoner := strings.Contains(lowerModel, "deepseek-reasoner")
	for _, msg := range req.Messages {
		m := map[string]any{"role": msg.Role}
		var text strings.Builder
		var parts []any
		for _, p := range msg.Content {
			switch p.Type {
			case "text":
				text.WriteString(p.Text)
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			case "image":
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + p.MimeType + ";base64," + p.Data}})
			}
		}
		if msg.Role == "user" && len(parts) > 0 {
			hasImage := false
			for _, p := range msg.Content {
				if p.Type == "image" {
					hasImage = true
					break
				}
			}
			if hasImage {
				m["content"] = parts
			} else {
				m["content"] = text.String()
			}
		} else if text.Len() > 0 || len(msg.ToolCalls) == 0 {
			m["content"] = text.String()
		} else {
			m["content"] = nil
		}
		if msg.Role == "assistant" {
			if echoReasoning {
				m["reasoning_content"] = msg.Reasoning
			}
			if len(msg.ToolCalls) > 0 {
				calls := make([]any, 0, len(msg.ToolCalls))
				for _, call := range msg.ToolCalls {
					calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": string(call.Arguments)}})
				}
				m["tool_calls"] = calls
			}
		}
		if msg.Role == "tool" {
			m["tool_call_id"] = msg.ToolCallID
		}
		messages = append(messages, m)
	}
	body := map[string]any{"model": req.Model, "messages": messages, "stream": true, "stream_options": map[string]bool{"include_usage": true}}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if deepseek && !reasoner {
		// 对应 Magpie 桥的 thinkingFormat 与 assistant reasoning_content 方言。
		if req.Effort != "" {
			typ := "enabled"
			if req.Effort == "off" {
				typ = "disabled"
			}
			body["thinking"] = map[string]string{"type": typ}
		}
	}
	if req.Effort != "" {
		if resolveEffort == nil {
			resolveEffort = defaultEffort
		}
		if effort, ok := resolveEffort(req.Model, req.Effort); ok && effort != "" {
			body["reasoning_effort"] = effort
		}
	}
	return body
}

func (c *openAIClient) Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	encoded, err := json.Marshal(requestBody(req, c.cfg.EchoReasoning, c.resolveEffort))
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(c.cfg.BaseURL), bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for key, value := range c.cfg.Headers {
		httpReq.Header.Set(key, value)
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
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 2048))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, &APIError{Status: httpResp.StatusCode, Body: readErr.Error(), Retryable: true}
		}
		return nil, &APIError{Status: httpResp.StatusCode, Body: string(body), Retryable: httpResp.StatusCode == 429 || httpResp.StatusCode >= 500}
	}
	result := &Response{Message: Message{Role: "assistant"}, StopReason: "stop"}
	acc := map[int]*toolCallAccumulator{}
	finished := false
	consume := func(data []byte, nonstream bool) error {
		var payload chatPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			if nonstream {
				return fmt.Errorf("openai: invalid response: %w", err)
			}
			// 流式模式下单个事件解析失败通常是传输被截断，交给 agent 重试。
			return &APIError{Body: "openai: invalid stream event: " + err.Error(), Retryable: true}
		}
		if len(payload.Error) > 0 && string(payload.Error) != "null" {
			return &APIError{Body: string(payload.Error)}
		}
		if nonstream && len(payload.Choices) == 0 {
			return fmt.Errorf("openai: response has no choices")
		}
		if payload.Usage != nil {
			result.Usage = Usage{Input: payload.Usage.Prompt - payload.Usage.Details.Cached, Output: payload.Usage.Completion, CacheRead: payload.Usage.Details.Cached}
		}
		for _, choice := range payload.Choices {
			if choice.Index != 0 {
				continue
			}
			d := choice.Delta
			if nonstream {
				d = choice.Message
				for i := range d.ToolCalls {
					d.ToolCalls[i].Index = i
				}
			}
			if d.Content != "" {
				result.Message.Content = appendText(result.Message.Content, d.Content)
				if onDelta != nil {
					onDelta(Delta{Type: "text", Text: d.Content})
				}
			}
			reasoning := d.ReasoningContent
			if reasoning == "" {
				reasoning = d.Reasoning
			}
			if reasoning != "" {
				result.Message.Reasoning += reasoning
				if onDelta != nil {
					onDelta(Delta{Type: "reasoning", Text: reasoning, Reasoning: reasoning})
				}
			}
			feedToolCalls(acc, d.ToolCalls)
			if choice.FinishReason != "" {
				finished = true
				switch choice.FinishReason {
				case "tool_calls", "function_call":
					result.StopReason = "tool_use"
				case "length":
					result.StopReason = "length"
				case "stop":
					result.StopReason = "stop"
				default:
					result.StopReason = "error"
				}
			}
		}
		return nil
	}
	reader := bufio.NewReader(httpResp.Body)
	// 未声明 SSE 的兼容服务也可能返回事件流。
	for {
		b, peekErr := reader.ReadByte()
		if peekErr != nil {
			break
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			_ = reader.UnreadByte()
			break
		}
	}
	prefix, _ := reader.Peek(1)
	if strings.Contains(strings.ToLower(httpResp.Header.Get("Content-Type")), "text/event-stream") || (len(prefix) > 0 && prefix[0] != '{' && prefix[0] != '[') {
		sse := newSSEReader(reader)
		for {
			event, readErr := sse.readEvent()
			if readErr != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if readErr == io.EOF {
					if finished {
						break
					}
					// 既没有 finish_reason 也没有 [DONE]，这一轮被截断了。
					return nil, &APIError{Body: "openai: stream ended unexpectedly", Retryable: true}
				}
				return nil, &APIError{Body: readErr.Error(), Retryable: true}
			}
			if strings.TrimSpace(event.Data) == "[DONE]" {
				break
			}
			if event.Data == "" {
				continue
			}
			if err := consume([]byte(event.Data), false); err != nil {
				return nil, err
			}
		}
	} else {
		body, readErr := io.ReadAll(io.LimitReader(reader, 8<<20))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, &APIError{Body: readErr.Error(), Retryable: true}
		}
		if err := consume(body, true); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result.Message.ToolCalls = sortedToolCalls(acc)
	for i := range result.Message.ToolCalls {
		if result.Message.ToolCalls[i].ID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return nil, fmt.Errorf("openai: generate tool call ID: %w", err)
			}
			result.Message.ToolCalls[i].ID = "call_" + hex.EncodeToString(id[:])
		}
	}
	return result, nil
}

func appendText(parts []Part, text string) []Part {
	if len(parts) > 0 && parts[len(parts)-1].Type == "text" {
		parts[len(parts)-1].Text += text
		return parts
	}
	return append(parts, Part{Type: "text", Text: text})
}
