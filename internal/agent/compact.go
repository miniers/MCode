package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mcode/internal/agent/llm"
	"mcode/internal/harness"
)

const (
	// defaultContextWindow 是 Options.ContextWindow 为 0 时采用的上下文窗口大小。
	defaultContextWindow = 128000
	// reserveTokens 是留给下一轮输出和工具结果的余量。
	reserveTokens = 16384
)

// summaryPrefix 是压缩后摘要消息的固定前缀，界面和后续压缩靠它识别旧摘要。
const summaryPrefix = "The earlier conversation was compacted to save context. Summary of that conversation:"

// summaryInstruction 是压缩请求使用的系统提示。
const summaryInstruction = `You are compacting a coding agent conversation so it can continue in a smaller context window. Write a concise summary of the conversation so far. Include the user's goals, decisions, files changed and why, open work, and key facts.
KEEP VERBATIM: file paths, function signatures and identifiers, errors, commands, exact numbers, the user's original requests, decisions and reasons, constraints, and unfinished work.
DROP: verbose logs, repeatedly read file contents, and completed exploration; retain only a one-line lesson for dead ends.
PRIORITY when space is limited: user goals > decisions and reasons > exact artifacts > conclusions > lessons.
Output only the summary, with no preamble and no markdown fence.`

// compactionEntry 是写入会话 JSONL 的压缩边界。
// KeepFrom 是压缩后保留的第一条消息在重建后的消息列表中的下标；该列表可能以旧摘要消息开头。
type compactionEntry struct {
	Type     string `json:"type"`
	Summary  string `json:"summary"`
	KeepFrom int    `json:"keep_from"`
}

const compactionEntryType = "compaction"

// compactionResult 是一次压缩的结果。
type compactionResult struct {
	Messages []llm.Message
	Entry    compactionEntry
	Before   int
	After    int
	// Usage 是总结请求本身的用量，调用方并入本轮累计用量。
	Usage llm.Usage
}

// compactThreshold 返回触发自动压缩的上下文 token 数。
// 预留输出余量，但窗口较小时最多只留四分之一，避免阈值变成负数后每轮都压缩。
func compactThreshold(window int) int {
	return window - min(reserveTokens, window/4)
}

func keepRecentTokens(window int) int { return min(20000, window/4) }

// historyFromEntries 按文件顺序重建会话消息。
// compaction entry 把此前的消息替换成摘要加其后的保留消息，后写的压缩边界自然覆盖早先的。
func historyFromEntries(entries []json.RawMessage) []llm.Message {
	messages, _ := replayEntries(entries)
	return messages
}

func foldedFromEntries(entries []json.RawMessage) []llm.Message {
	_, folded := replayEntries(entries)
	return folded
}

func replayEntries(entries []json.RawMessage) ([]llm.Message, []llm.Message) {
	var messages []llm.Message
	var folded []llm.Message
	for _, raw := range entries {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		switch head.Type {
		case "message":
			var entry messageEntry
			if json.Unmarshal(raw, &entry) != nil {
				continue
			}
			if entry.Message.Role == "user" || entry.Message.Role == "assistant" || entry.Message.Role == "tool" {
				messages = append(messages, entry.Message)
			}
		case compactionEntryType:
			var entry compactionEntry
			if json.Unmarshal(raw, &entry) != nil || entry.Summary == "" {
				continue
			}
			messages = repairToolPairs(messages)
			if len(messages) == 0 && entry.KeepFrom == 1 {
				messages = []llm.Message{summaryMessage(entry.Summary)}
				continue
			}
			if entry.KeepFrom <= 0 || entry.KeepFrom >= len(messages) {
				continue
			}
			for _, message := range messages[:entry.KeepFrom] {
				if !isSummaryMessage(message) {
					folded = append(folded, message)
				}
			}
			messages = append([]llm.Message{summaryMessage(entry.Summary)}, messages[entry.KeepFrom:]...)
		}
	}
	return repairToolPairs(messages), folded
}

// repairToolPairs 修补残缺的工具调用配对：
// 为缺少结果的 assistant 工具调用补一条错误结果，并丢掉找不到对应调用的孤立 tool 消息。
func repairToolPairs(messages []llm.Message) []llm.Message {
	repaired := make([]llm.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			// 前面没有工具调用的 tool 消息是孤立结果，丢掉。
			if message.Role == "tool" {
				continue
			}
			repaired = append(repaired, message)
			continue
		}
		repaired = append(repaired, message)
		expected := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			expected[call.ID] = true
		}
		answered := make(map[string]bool, len(message.ToolCalls))
		var rest []llm.Message
		j := i + 1
		// 工具结果只属于紧接着的 assistant 消息，直到下一条 assistant 为止。
		for ; j < len(messages) && messages[j].Role != "assistant"; j++ {
			next := messages[j]
			if next.Role != "tool" {
				rest = append(rest, next)
				continue
			}
			if !expected[next.ToolCallID] || answered[next.ToolCallID] {
				continue
			}
			answered[next.ToolCallID] = true
			repaired = append(repaired, next)
		}
		for _, call := range message.ToolCalls {
			if !answered[call.ID] {
				repaired = append(repaired, toolMessage(call.ID, "Tool call was interrupted.", true))
			}
		}
		repaired = append(repaired, rest...)
		i = j - 1
	}
	return repaired
}

// estimateContextTokens 估算当前上下文大小：以最近一次响应的用量为基线，之后追加的消息按字符数估算。
func estimateContextTokens(messages []llm.Message, baselineTokens, baselineMessages int) int {
	if baselineMessages < 0 || baselineMessages > len(messages) {
		baselineMessages = 0
		baselineTokens = 0
	}
	return baselineTokens + tokensForMessages(messages[baselineMessages:])
}

// tokensForMessages 按字符数粗略估算一组消息的 token 数。
func tokensForMessages(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += tokensForMessage(message)
	}
	return total
}

func tokensForMessage(message llm.Message) int {
	chars := len(message.ToolCallID) + len(message.Reasoning)
	images := 0
	for _, part := range message.Content {
		chars += len(part.Text)
		if part.Type == "image" {
			images++
		}
	}
	for _, call := range message.ToolCalls {
		chars += len(call.ID) + len(call.Name) + len(call.Arguments)
	}
	return (chars+3)/4 + 4 + images*1200
}

// summaryMessage 把摘要包装成一条 user 消息。
func summaryMessage(summary string) llm.Message {
	return llm.Message{Role: "user", Content: []llm.Part{{Type: "text", Text: summaryPrefix + "\n\n" + summary}}}
}

// isSummaryMessage 判断消息是否是压缩产生的摘要消息。
func isSummaryMessage(message llm.Message) bool {
	if message.Role != "user" || len(message.Content) == 0 {
		return false
	}
	return strings.HasPrefix(message.Content[0].Text, summaryPrefix)
}

// isUserTurn 判断消息是否是一个用户回合的开头（而不是工具结果）。
func isUserTurn(message llm.Message) bool {
	return message.Role == "user" && message.ToolCallID == "" && !isSummaryMessage(message)
}

// findCut 返回压缩切点：保留该下标起的最近消息，返回 0 表示没有合法切点。
// 切点落在用户回合或 assistant 消息开头，不拆开工具调用及其结果。
func findCut(messages []llm.Message, window int) int {
	keepRecentTokens := keepRecentTokens(window)
	total := 0
	for i := len(messages) - 1; i >= 1; i-- {
		total += tokensForMessage(messages[i])
		if total < keepRecentTokens {
			continue
		}
		if cut := userCutAtOrBefore(messages, i); cut > 0 {
			return cut
		}
		break
	}
	// 整体不足 keepRecentTokens（或目标区间之前没有用户回合）时，退回到最近的用户回合开头。
	for i := len(messages) - 1; i >= 1; i-- {
		if (isUserTurn(messages[i]) || messages[i].Role == "assistant") && hasSummarizablePrefix(messages, i) {
			return i
		}
	}
	return 0
}

// userCutAtOrBefore 从 i 往前找最近的用户回合或 assistant 消息开头。
func userCutAtOrBefore(messages []llm.Message, i int) int {
	for j := i; j >= 1; j-- {
		if (isUserTurn(messages[j]) || messages[j].Role == "assistant") && hasSummarizablePrefix(messages, j) {
			return j
		}
	}
	return 0
}

// hasSummarizablePrefix 判断切点之前是否有值得总结的内容；只剩旧摘要时不再压缩。
func hasSummarizablePrefix(messages []llm.Message, cut int) bool {
	if cut <= 0 {
		return false
	}
	return cut > 1 || !isSummaryMessage(messages[0])
}

// compactMessages 把切点之前的消息总结成一条摘要消息。
// 返回 nil 表示没有合法切点，调用方应保持原消息不变。
func compactMessages(ctx context.Context, client llm.Client, model string, messages []llm.Message, window, overhead int, emit func(harness.Event)) (*compactionResult, error) {
	cut := findCut(messages, window)
	if cut <= 0 {
		return nil, nil
	}
	before := tokensForMessages(messages) + overhead
	prefix := append([]llm.Message(nil), messages[:cut]...)
	truncate := func(message *llm.Message) {
		message.Content = append([]llm.Part(nil), message.Content...)
		for j := range message.Content {
			part := &message.Content[j]
			if part.Type == "text" {
				text := []rune(part.Text)
				if len(text) > 4000 {
					part.Text = string(text[:2000]) + fmt.Sprintf("…（已截断 %d 字符）…", len(text)-3000) + string(text[len(text)-1000:])
				}
			}
		}
	}
	for i := range prefix {
		if prefix[i].Role == "tool" {
			truncate(&prefix[i])
		}
	}
	if tokensForMessages(prefix) > compactThreshold(window) {
		for i := range prefix {
			truncate(&prefix[i])
		}
	}
	omitted := llm.Message{Role: "user", Content: []llm.Part{{Type: "text", Text: "（更早的对话已省略）"}}}
	for len(prefix) > 0 {
		tokens := tokensForMessages(prefix)
		if prefix[0].Role != "user" {
			tokens += tokensForMessage(omitted)
		}
		if tokens <= compactThreshold(window) {
			break
		}
		start := 0
		if isSummaryMessage(prefix[0]) {
			start = 1
		}
		if start == len(prefix) {
			return nil, errors.New("agent: no messages fit in compaction request")
		}
		next := start + 1
		if prefix[start].Role == "assistant" && len(prefix[start].ToolCalls) > 0 {
			for i := next; i < len(prefix) && prefix[i].Role != "assistant"; i++ {
				if prefix[i].Role == "tool" {
					next = i + 1
				}
			}
		}
		prefix = append(prefix[:start], prefix[next:]...)
	}
	if len(prefix) == 0 {
		return nil, errors.New("agent: no messages fit in compaction request")
	}
	if prefix[0].Role != "user" {
		prefix = append([]llm.Message{omitted}, prefix...)
	}
	instruction := llm.Part{Type: "text", Text: "请按系统提示的要求，总结以上对话，用于在新的上下文中继续工作。只输出摘要。"}
	last := &prefix[len(prefix)-1]
	if last.Role == "user" && last.ToolCallID == "" {
		last.Content = append(append([]llm.Part(nil), last.Content...), instruction)
	} else {
		prefix = append(prefix, llm.Message{Role: "user", Content: []llm.Part{instruction}})
	}
	request := llm.Request{Model: model, System: summaryInstruction, Messages: prefix}
	response, err := client.Stream(ctx, request, func(llm.Delta) {})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("agent: empty compaction response")
	}
	summary := strings.TrimSpace(messageText(response.Message))
	if summary == "" {
		return nil, errors.New("agent: empty compaction summary")
	}
	compacted := append([]llm.Message{summaryMessage(summary)}, messages[cut:]...)
	after := tokensForMessages(compacted) + overhead
	emit(harness.Event{Type: harness.EventContextCompacted, CompactTokensBefore: int64(before), CompactTokensAfter: int64(after)})
	return &compactionResult{
		Messages: compacted,
		Entry:    compactionEntry{Type: compactionEntryType, Summary: summary, KeepFrom: cut},
		Before:   before,
		After:    after,
		Usage:    response.Usage,
	}, nil
}

// messageText 连接消息里的文本片段。
func messageText(message llm.Message) string {
	var text strings.Builder
	for _, part := range message.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}
