package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// sseEvent 表示单条 SSE 事件数据。
type sseEvent struct {
	Event string
	Data  string
	ID    string
}

// sseReader 基于 bufio.Reader 实现标准 Server-Sent Events 流式解析器。
// 支持 data: 多行拼接、event:、id:、注释行 (:)、空行分隔、[DONE] 结束标记、CRLF 与超长行。
type sseReader struct {
	reader *bufio.Reader
}

// newSSEReader 创建并返回一个 sseReader 实例。
func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{
		reader: bufio.NewReader(r),
	}
}

// readEvent 读取并返回下一条完整的 SSE 事件。
// 遇到 EOF 且无残留数据时返回 io.EOF。
func (r *sseReader) readEvent() (*sseEvent, error) {
	var cur sseEvent
	hasData := false

	for {
		lineBytes, err := r.reader.ReadBytes('\n')
		if err != nil && len(lineBytes) == 0 {
			if hasData {
				return &cur, nil
			}
			return nil, err
		}

		// 去除行末的 CRLF 或 LF
		line := string(bytes.TrimRight(lineBytes, "\r\n"))

		// 空行：触发当前事件派发
		if line == "" {
			if hasData || cur.Event != "" || cur.ID != "" {
				return &cur, nil
			}
			if err != nil {
				return nil, err
			}
			continue
		}

		// 注释行：以 ':' 开头，忽略
		if strings.HasPrefix(line, ":") {
			if err != nil {
				if hasData {
					return &cur, nil
				}
				return nil, err
			}
			continue
		}

		// 解析 field 与 value（字段以首个冒号分隔，若冒号后紧跟单个空格则去除）
		var field, value string
		if colonIdx := strings.IndexByte(line, ':'); colonIdx >= 0 {
			field = line[:colonIdx]
			value = line[colonIdx+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		} else {
			field = line
			value = ""
		}

		switch field {
		case "data":
			if hasData {
				cur.Data += "\n" + value
			} else {
				cur.Data = value
				hasData = true
			}
		case "event":
			cur.Event = value
		case "id":
			cur.ID = value
		}

		if err != nil {
			if hasData || cur.Event != "" || cur.ID != "" {
				return &cur, nil
			}
			return nil, err
		}
	}
}

// chatCompletionChunk 表示 OpenAI Chat Completions 流式响应块。
type chatCompletionChunk struct {
	ID      string                 `json:"id"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *chatUsage             `json:"usage"`
	Error   *chatError             `json:"error"`
}

type chatError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

type chatCompletionChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	Reasoning        string              `json:"reasoning"`
	ToolCalls        []chatDeltaToolCall `json:"tool_calls"`
}

type chatDeltaToolCall struct {
	Index    int               `json:"index"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function chatDeltaFunction `json:"function"`
}

type chatDeltaFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatUsage struct {
	PromptTokens            int64                    `json:"prompt_tokens"`
	CompletionTokens        int64                    `json:"completion_tokens"`
	TotalTokens             int64                    `json:"total_tokens"`
	PromptTokensDetails     *promptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details"`
}

type promptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type completionTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// parseChatChunk 解析一个 SSE 数据负载为 chatCompletionChunk。
func parseChatChunk(data string) (*chatCompletionChunk, error) {
	var chunk chatCompletionChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, err
	}
	return &chunk, nil
}

// maxNonStreamBody 限制非流式响应体的读取上限。
const maxNonStreamBody = 8 << 20

// chatMessagePayload 表示非流式响应中 choices[].message 的字段。
type chatMessagePayload struct {
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	Reasoning        string              `json:"reasoning"`
	ToolCalls        []chatDeltaToolCall `json:"tool_calls"`
}

// chatCompletionResponse 表示一次性的（非流式）Chat Completions 响应。
type chatCompletionResponse struct {
	Choices []struct {
		Message      chatMessagePayload `json:"message"`
		FinishReason *string            `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *chatError `json:"error"`
}

// toChunk 把一次性响应归一化成单个流式块，让非流式响应也能走同一套增量处理。
// 非流式响应不返回 tool_calls[].index，按出现顺序补上。
func (r *chatCompletionResponse) toChunk() *chatCompletionChunk {
	chunk := &chatCompletionChunk{Usage: r.Usage, Error: r.Error}
	for i, choice := range r.Choices {
		delta := chatDelta{
			Content:          choice.Message.Content,
			ReasoningContent: choice.Message.ReasoningContent,
			Reasoning:        choice.Message.Reasoning,
		}
		for idx, tc := range choice.Message.ToolCalls {
			tc.Index = idx
			delta.ToolCalls = append(delta.ToolCalls, tc)
		}
		chunk.Choices = append(chunk.Choices, chatCompletionChoice{
			Index:        i,
			Delta:        delta,
			FinishReason: choice.FinishReason,
		})
	}
	return chunk
}

// normalizeNonStreamResponse 把非 SSE 的 200 响应归一化成一条流式块 JSON。
// 服务端返回错误体时保留 error 字段，由流式处理统一报错；
// 无法识别为 chat completion 时返回带响应开头的错误，而不是静默当成空回答。
func normalizeNonStreamResponse(contentType string, raw []byte) (string, error) {
	var resp chatCompletionResponse
	trimmed := bytes.TrimSpace(raw)
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return "", fmt.Errorf("openai: unexpected response (content-type %q, err=%v): %s", contentType, err, bodyExcerpt(trimmed))
	}
	if resp.Error == nil && len(resp.Choices) == 0 {
		return "", fmt.Errorf("openai: unexpected response (content-type %q): %s", contentType, bodyExcerpt(trimmed))
	}
	chunk, err := json.Marshal(resp.toChunk())
	if err != nil {
		return "", fmt.Errorf("openai: marshal non-stream response: %w", err)
	}
	return string(chunk), nil
}

// looksLikeSSE 判断 body 是否看起来是 SSE 流（用于服务端漏发 Content-Type 的情况）。
func looksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("data:")) ||
		bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte(":"))
}

// bodyExcerpt 返回用于错误显示的响应开头片段。
func bodyExcerpt(body []byte) string {
	const limit = 200
	if len(body) <= limit {
		return string(body)
	}
	return strings.ToValidUTF8(string(body[:limit]), "") + "..."
}

// toolCallAccumulator 用于在流式响应中增量拼接单条工具调用。
type toolCallAccumulator struct {
	Index     int
	ID        string
	Name      string
	Arguments strings.Builder
}

// toolCall 表示拼接完毕的工具调用。
type toolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// feedToolCalls 将 chunk 中的 delta.tool_calls 按 index 增量拼接到 accumulators 映射中。
func feedToolCalls(accMap map[int]*toolCallAccumulator, calls []chatDeltaToolCall) {
	for _, c := range calls {
		acc, ok := accMap[c.Index]
		if !ok {
			acc = &toolCallAccumulator{Index: c.Index}
			accMap[c.Index] = acc
		}
		if c.ID != "" {
			acc.ID = c.ID
		}
		if c.Function.Name != "" {
			acc.Name = c.Function.Name
		}
		if c.Function.Arguments != "" {
			acc.Arguments.WriteString(c.Function.Arguments)
		}
	}
}

// sortedToolCalls 将累积的工具调用按 index 升序排列导出。
func sortedToolCalls(accMap map[int]*toolCallAccumulator) []toolCall {
	if len(accMap) == 0 {
		return nil
	}
	indices := make([]int, 0, len(accMap))
	for idx := range accMap {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	result := make([]toolCall, 0, len(indices))
	for _, idx := range indices {
		acc := accMap[idx]
		tc := toolCall{
			Index:     acc.Index,
			ID:        acc.ID,
			Name:      acc.Name,
			Arguments: acc.Arguments.String(),
		}
		if tc.ID == "" {
			// 部分 OpenAI 兼容服务不返回工具调用 ID；补一个本地 ID，
			// 否则后续 assistant.tool_calls 与 tool 结果都会缺少 ID 而被服务拒绝。
			tc.ID = "call_" + randomHex(8)
		}
		result = append(result, tc)
	}
	return result
}

// isDoneChunk 检查 SSE 数据行是否为 OpenAI 的流结束标记 [DONE]。
func isDoneChunk(data string) bool {
	return strings.TrimSpace(data) == "[DONE]"
}

// errStreamDone 用于在流读取结束时标识。
var errStreamDone = errors.New("stream done")
