package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mcode/internal/agent/llm"
)

const (
	// defaultContextWindow 是 Options.ContextWindow 为 0 时采用的上下文窗口大小。
	defaultContextWindow = 128000
	// 压缩判定使用的窗口上限，超过 200k 的输入费用很高，大窗口模型也按此上限压缩。
	compactionBudget = 200000
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
	FromSeq  int    `json:"from_seq,omitempty"`
	ToSeq    int    `json:"to_seq,omitempty"`
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

// compactionPressure 只按总量触发：摘要输出和尾部缓存重付有固定成本，一次多压才划算。
func compactionPressure(window, total int) (normal, emergency bool) {
	normalPressure := min(window*3/4, compactThreshold(window))
	emergencyPressure := min(window*95/100, compactThreshold(window))
	return total >= normalPressure, total >= emergencyPressure
}

func planBlock(items []replayItem, window int) (start, end int, ok bool) {
	start = 1
	for i := len(items) - 1; i >= 0; i-- {
		if isSummaryMessage(items[i].message) {
			start = max(1, i+1)
			break
		}
	}
	total := 0
	for i := len(items) - 1; i >= start; i-- {
		total += tokensForMessage(items[i].message)
		if total >= keepRecentTokens(window) {
			for j := i; j >= start; j-- {
				if isUserTurn(items[j].message) || items[j].message.Role == "assistant" {
					end = j
					break
				}
			}
			break
		}
	}
	if end <= start {
		return 0, 0, false
	}
	total = 0
	maxSeq := 0
	for _, item := range items {
		maxSeq = max(maxSeq, item.seq)
	}
	entered := make([]bool, maxSeq+1)
	for _, item := range items {
		if item.seq > 0 {
			entered[item.seq] = true
		}
	}
	for _, item := range items[start:end] {
		if item.seq == 0 {
			return 0, 0, false
		}
		total += tokensForMessage(item.message)
	}
	if total < 4000 {
		return 0, 0, false
	}
	a, b, valid := blockRange(items, entered, items[start].seq, items[end-1].seq)
	return start, end, valid && a == start && b == end
}

func compactBlock(ctx context.Context, client llm.Client, base llm.Request, entries []json.RawMessage, window, overhead int) (*compactionResult, error) {
	items, _ := replayEntries(entries)
	start, end, ok := planBlock(items, window)
	if !ok {
		return nil, nil
	}
	messages := replayMessages(items)
	summary, usage, err := summarize(ctx, client, base, start, end, window, overhead)
	result := &compactionResult{Usage: usage}
	if err != nil {
		return result, err
	}
	if tokensForMessage(summaryMessage(summary))*2 >= tokensForMessages(messages[start:end]) {
		return result, nil
	}
	result.Before = tokensForMessages(messages)
	result.Messages = append(append(append([]llm.Message(nil), messages[:start]...), summaryMessage(summary)), messages[end:]...)
	result.After = tokensForMessages(result.Messages)
	result.Entry = compactionEntry{Type: compactionEntryType, Summary: summary, FromSeq: items[start].seq, ToSeq: items[end-1].seq}
	return result, nil
}

// historyFromEntries 按文件顺序重建会话消息。
// compaction entry 把此前的消息替换成摘要加其后的保留消息，后写的压缩边界自然覆盖早先的。
func historyFromEntries(entries []json.RawMessage) []llm.Message {
	items, _ := replayEntries(entries)
	return replayMessages(items)
}

func foldedFromEntries(entries []json.RawMessage) []llm.Message {
	var messages []llm.Message
	for _, item := range foldedItemsFromEntries(entries) {
		messages = append(messages, item.Message)
	}
	return messages
}

type replayItem struct {
	message llm.Message
	seq     int
}

type foldedMessage struct {
	Seq     int
	Message llm.Message
}

func foldedItemsFromEntries(entries []json.RawMessage) []foldedMessage {
	_, folded := replayEntries(entries)
	return folded
}

func replayMessages(items []replayItem) []llm.Message {
	messages := make([]llm.Message, 0, len(items))
	for _, item := range items {
		messages = append(messages, item.message)
	}
	return messages
}

func replayEntries(entries []json.RawMessage) ([]replayItem, []foldedMessage) {
	var messages []replayItem
	var folded []foldedMessage
	entered := []bool{false}
	seq := 0
	for _, raw := range entries {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		switch head.Type {
		case "message":
			seq++
			entered = append(entered, false)
			var entry messageEntry
			if json.Unmarshal(raw, &entry) != nil {
				continue
			}
			if entry.Message.Role == "user" || entry.Message.Role == "assistant" || entry.Message.Role == "tool" {
				messages = append(messages, replayItem{message: entry.Message, seq: seq})
				entered[seq] = true
			}
		case compactionEntryType:
			var entry compactionEntry
			if json.Unmarshal(raw, &entry) != nil || entry.Summary == "" {
				continue
			}
			if entry.FromSeq > 0 {
				start, end, ok := blockRange(messages, entered, entry.FromSeq, entry.ToSeq)
				if !ok {
					continue
				}
				for _, item := range messages[start:end] {
					folded = append(folded, foldedMessage{Seq: item.seq, Message: item.message})
				}
				replacement := replayItem{message: summaryMessage(entry.Summary)}
				messages = append(append(append([]replayItem(nil), messages[:start]...), replacement), messages[end:]...)
				continue
			}
			messages = repairReplayToolPairs(messages)
			if len(messages) == 0 && entry.KeepFrom == 1 {
				messages = []replayItem{{message: summaryMessage(entry.Summary)}}
				continue
			}
			if entry.KeepFrom <= 0 || entry.KeepFrom >= len(messages) {
				continue
			}
			for _, item := range messages[:entry.KeepFrom] {
				if !isSummaryMessage(item.message) {
					folded = append(folded, foldedMessage{Seq: item.seq, Message: item.message})
				}
			}
			messages = append([]replayItem{{message: summaryMessage(entry.Summary)}}, messages[entry.KeepFrom:]...)
		}
	}
	return repairReplayToolPairs(messages), folded
}

func blockRange(items []replayItem, entered []bool, from, to int) (int, int, bool) {
	if from > to || to >= len(entered) {
		return 0, 0, false
	}
	start, end, count := -1, -1, 0
	for i, item := range items {
		if item.seq >= from && item.seq <= to {
			if start < 0 {
				start = i
			}
			end = i + 1
			count++
		}
	}
	want := 0
	for _, exists := range entered[from : to+1] {
		if exists {
			want++
		}
	}
	if start <= 0 || count != want || end-start != count {
		return 0, 0, false
	}
	// 配对规则与修复逻辑相同：结果属于前一条 assistant。
	matched := make(map[int]bool)
	for i, item := range items {
		if item.message.Role != "assistant" {
			continue
		}
		for _, call := range item.message.ToolCalls {
			result := -1
			for j := i + 1; j < len(items) && items[j].message.Role != "assistant"; j++ {
				if items[j].message.Role == "tool" && items[j].message.ToolCallID == call.ID && !matched[j] {
					result = j
					break
				}
			}
			inside := i >= start && i < end
			if result >= 0 {
				matched[result] = true
			}
			if inside != (result >= start && result < end) {
				return 0, 0, false
			}
		}
	}
	for i := start; i < end; i++ {
		if items[i].message.Role == "tool" && !matched[i] {
			return 0, 0, false
		}
	}
	return start, end, true
}

// repairToolPairs 修补残缺的工具调用配对：
// 为缺少结果的 assistant 工具调用补一条错误结果，并丢掉找不到对应调用的孤立 tool 消息。
func repairToolPairs(messages []llm.Message) []llm.Message {
	items := make([]replayItem, len(messages))
	for i, message := range messages {
		items[i].message = message
	}
	return replayMessages(repairReplayToolPairs(items))
}

func repairReplayToolPairs(messages []replayItem) []replayItem {
	repaired := make([]replayItem, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		item := messages[i]
		message := item.message
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			// 前面没有工具调用的 tool 消息是孤立结果，丢掉。
			if message.Role == "tool" {
				continue
			}
			repaired = append(repaired, item)
			continue
		}
		repaired = append(repaired, item)
		expected := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			expected[call.ID] = true
		}
		answered := make(map[string]bool, len(message.ToolCalls))
		var rest []replayItem
		j := i + 1
		// 工具结果只属于紧接着的 assistant 消息，直到下一条 assistant 为止。
		for ; j < len(messages) && messages[j].message.Role != "assistant"; j++ {
			next := messages[j]
			if next.message.Role != "tool" {
				rest = append(rest, next)
				continue
			}
			if !expected[next.message.ToolCallID] || answered[next.message.ToolCallID] {
				continue
			}
			answered[next.message.ToolCallID] = true
			repaired = append(repaired, next)
		}
		for _, call := range message.ToolCalls {
			if !answered[call.ID] {
				repaired = append(repaired, replayItem{message: toolMessage(call.ID, "Tool call was interrupted.", true)})
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
func compactMessages(ctx context.Context, client llm.Client, base llm.Request, window, overhead int) (*compactionResult, error) {
	messages := base.Messages
	cut := findCut(messages, window)
	if cut <= 0 {
		return nil, nil
	}
	before := tokensForMessages(messages) + overhead
	summary, usage, err := summarize(ctx, client, base, 0, cut, window, overhead)
	result := &compactionResult{Usage: usage}
	if err != nil {
		return result, err
	}
	compacted := append([]llm.Message{summaryMessage(summary)}, messages[cut:]...)
	result.Messages = compacted
	result.Entry = compactionEntry{Type: compactionEntryType, Summary: summary, KeepFrom: cut}
	result.Before = before
	result.After = tokensForMessages(compacted) + overhead
	return result, nil
}

func summarize(ctx context.Context, client llm.Client, base llm.Request, start, end, window, overhead int) (string, llm.Usage, error) {
	request := base
	request.Messages = append([]llm.Message(nil), base.Messages[:end]...)
	instruction := fmt.Sprintf("你正在为当前会话生成压缩摘要。只总结本指令之前的会话消息中，零基下标 [%d,%d) 的消息，即第 %d 条至第 %d 条。计数不含 System、Tools 和本条指令。前面的消息仅供理解语境，不要再次总结；本条指令也不属于摘要内容。目标之后的近期消息未随此请求发送，不要补写其内容。只输出摘要正文，不调用任何工具，不继续执行原任务。\n\n%s", start, end, start+1, end, strings.Replace(summaryInstruction, "the conversation so far", "the specified message range", 1))
	request.Messages = append(request.Messages, llm.Message{Role: "user", Content: []llm.Part{{Type: "text", Text: instruction}}})
	request.CacheBreakpoints = nil
	for _, index := range base.CacheBreakpoints {
		if index < end {
			request.CacheBreakpoints = append(request.CacheBreakpoints, index)
		}
	}
	chars := len(base.System)
	for _, tool := range base.Tools {
		chars += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
	}
	fallback := func(usage llm.Usage) (string, llm.Usage, error) {
		summary, extra, err := summarizeTruncated(ctx, client, base.Model, base.Messages[start:end], window)
		usage.Input += extra.Input
		usage.Output += extra.Output
		usage.CacheRead += extra.CacheRead
		usage.CacheWrite += extra.CacheWrite
		return summary, usage, err
	}
	if end == 0 || base.Messages[0].Role != "user" || tokensForMessages(request.Messages)+max(overhead, (chars+3)/4) > compactThreshold(window) {
		return fallback(llm.Usage{})
	}
	response, err := client.Stream(ctx, request, func(llm.Delta) {})
	var usage llm.Usage
	if response != nil {
		usage = response.Usage
	}
	if err != nil {
		var apiErr *llm.APIError
		if llm.IsContextOverflow(err) || (errors.As(err, &apiErr) && apiErr.Status == 413) {
			return fallback(usage)
		}
		return "", usage, err
	}
	if response == nil {
		return "", usage, errors.New("agent: empty compaction response")
	}
	if len(response.Message.ToolCalls) > 0 {
		return fallback(usage)
	}
	summary := strings.TrimSpace(messageText(response.Message))
	if summary == "" {
		return "", usage, errors.New("agent: empty compaction summary")
	}
	return summary, usage, nil
}

func summarizeTruncated(ctx context.Context, client llm.Client, model string, messages []llm.Message, window int) (string, llm.Usage, error) {
	prefix := append([]llm.Message(nil), messages...)
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
	instruction := llm.Part{Type: "text", Text: "请按系统提示的要求，总结以上对话，用于在新的上下文中继续工作。只输出摘要。"}
	for len(prefix) > 0 {
		tokens := tokensForMessages(prefix) + (len(summaryInstruction)+3)/4 + tokensForMessage(llm.Message{Role: "user", Content: []llm.Part{instruction}})
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
			return "", llm.Usage{}, errors.New("agent: no messages fit in compaction request")
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
		return "", llm.Usage{}, errors.New("agent: no messages fit in compaction request")
	}
	if prefix[0].Role != "user" {
		prefix = append([]llm.Message{omitted}, prefix...)
	}
	last := &prefix[len(prefix)-1]
	if last.Role == "user" && last.ToolCallID == "" {
		last.Content = append(append([]llm.Part(nil), last.Content...), instruction)
	} else {
		prefix = append(prefix, llm.Message{Role: "user", Content: []llm.Part{instruction}})
	}
	request := llm.Request{Model: model, System: summaryInstruction, Messages: prefix}
	response, err := client.Stream(ctx, request, func(llm.Delta) {})
	if err != nil {
		if response != nil {
			return "", response.Usage, err
		}
		return "", llm.Usage{}, err
	}
	if response == nil {
		return "", llm.Usage{}, errors.New("agent: empty compaction response")
	}
	if len(response.Message.ToolCalls) > 0 {
		return "", response.Usage, errors.New("agent: compaction response contains tool calls")
	}
	summary := strings.TrimSpace(messageText(response.Message))
	if summary == "" {
		return "", response.Usage, errors.New("agent: empty compaction summary")
	}
	return summary, response.Usage, nil
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
