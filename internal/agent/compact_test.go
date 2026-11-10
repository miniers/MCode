package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

func appendHistory(t *testing.T, store *session.Store, id string, messages ...llm.Message) {
	t.Helper()
	for _, message := range messages {
		if err := store.Append(id, messageEntry{Type: "message", Message: message}); err != nil {
			t.Fatal(err)
		}
	}
}

func summaryResponse(text string) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.Part{{Type: "text", Text: text}}}, StopReason: "stop"}
}

func textMessage(role, text string) llm.Message {
	return llm.Message{Role: role, Content: []llm.Part{{Type: "text", Text: text}}}
}

func compactionEntries(t *testing.T, store *session.Store, id string) []compactionEntry {
	t.Helper()
	entries, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	var found []compactionEntry
	for _, raw := range entries {
		var entry compactionEntry
		if json.Unmarshal(raw, &entry) == nil && entry.Type == compactionEntryType {
			found = append(found, entry)
		}
	}
	return found
}

func TestCompactThreshold(t *testing.T) {
	for _, tc := range []struct{ window, want int }{
		{8192, 6144},
		{200000, 183616},
		{4096, 3072},
		{128000, 111616},
	} {
		if got := compactThreshold(tc.window); got != tc.want {
			t.Fatalf("compactThreshold(%d) = %d, want %d", tc.window, got, tc.want)
		}
	}
}

func TestCompactionTriggerGrowthAndPressure(t *testing.T) {
	for _, tc := range []struct {
		window, growth, total int
		normal, emergency     bool
	}{
		{128000, 49999, 95999, false, false},
		{128000, 50000, 0, true, false},
		{128000, 0, 96000, true, false},
		{128000, 0, 111616, true, true},
		{200000, 0, 150000, true, false},
		{8192, 0, 6143, false, false},
		{8192, 0, 6144, true, true},
	} {
		normal, emergency := compactionPressure(tc.window, tc.growth, tc.total)
		if normal != tc.normal || emergency != tc.emergency {
			t.Errorf("compactionPressure(%d, %d, %d) = %v, %v; want %v, %v", tc.window, tc.growth, tc.total, normal, emergency, tc.normal, tc.emergency)
		}
	}
}

func TestGrowthSinceSummary(t *testing.T) {
	a, b, c := textMessage("user", "a"), textMessage("assistant", "bb"), textMessage("user", "ccc")
	for _, tc := range []struct {
		messages []llm.Message
		want     int
	}{
		{[]llm.Message{a, b, c}, tokensForMessages([]llm.Message{b, c})},
		{[]llm.Message{summaryMessage("old"), b, c}, tokensForMessages([]llm.Message{b, c})},
		{[]llm.Message{a, b, summaryMessage("block"), c}, tokensForMessage(c)},
	} {
		if got := growthSinceSummary(tc.messages); got != tc.want {
			t.Fatalf("growth = %d, want %d", got, tc.want)
		}
	}
}

func TestPlanBlock(t *testing.T) {
	large := textMessage("assistant", strings.Repeat("x", 20000))
	items := []replayItem{{message: textMessage("user", "first"), seq: 1}, {message: summaryMessage("old")}, {message: large, seq: 3}, {message: large, seq: 4}, {message: textMessage("user", strings.Repeat("r", 20000)), seq: 5}}
	start, end, ok := planBlock(items, 20000)
	if !ok || start != 2 || end != 4 {
		t.Fatalf("plan = %d %d %v", start, end, ok)
	}
	short := append([]replayItem(nil), items...)
	short[2].message, short[3].message = textMessage("assistant", "a"), textMessage("assistant", "b")
	if _, _, ok := planBlock(short, 20000); ok {
		t.Fatal("短区间不应压缩")
	}
	synthetic := append([]replayItem(nil), items...)
	synthetic[2].seq = 0
	if _, _, ok := planBlock(synthetic, 20000); ok {
		t.Fatal("合成消息不应压缩")
	}
	split := []replayItem{{message: textMessage("user", "first"), seq: 1}, {message: llm.Message{Role: "assistant", Content: large.Content, ToolCalls: []llm.ToolCall{call("id", "read", `{}`)}}, seq: 2}, {message: textMessage("user", strings.Repeat("r", 20000)), seq: 3}, {message: toolMessage("id", "result", false), seq: 4}}
	if _, _, ok := planBlock(split, 20000); ok {
		t.Fatal("不能拆分工具调用")
	}
}

func TestRunTwoBlockCompactions(t *testing.T) {
	store := session.NewStore(t.TempDir())
	id := "two-blocks"
	appendHistory(t, store, id, textMessage("user", "first"))
	for i := 0; i < 6; i++ {
		appendHistory(t, store, id, textMessage("assistant", strings.Repeat("a", 16000)))
	}
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("first summary")}, {response: reply("stop")}, {response: summaryResponse("second summary")}, {response: reply("stop")}}}
	opts := Options{Client: client, Store: store, SessionID: id, ContextWindow: 40000}
	if err := Run(context.Background(), opts, "continue one", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendHistory(t, store, id, textMessage("assistant", strings.Repeat("b", 16000)))
	}
	if err := Run(context.Background(), opts, "continue two", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 4 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	first, second := client.requests[1], client.requests[3]
	if strings.Contains(messageText(client.requests[2].Messages[0]), "first summary") {
		t.Fatal("第二次摘要包含旧摘要")
	}
	for _, msg := range client.requests[2].Messages {
		if strings.Contains(messageText(msg), "first summary") {
			t.Fatal("第二次摘要包含旧摘要")
		}
	}
	if !reflect.DeepEqual(second.CacheBreakpoints, []int{1, 2}) || !reflect.DeepEqual(first.CacheBreakpoints, []int{1}) {
		t.Fatalf("breakpoints = %v, %v", first.CacheBreakpoints, second.CacheBreakpoints)
	}
	if !reflect.DeepEqual(first.Messages[:2], second.Messages[:2]) {
		t.Fatal("第二块之前的前缀发生变化")
	}
	entries, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	history := historyFromEntries(entries)
	if !reflect.DeepEqual(history[:len(second.Messages)], second.Messages) {
		t.Fatal("重载消息与请求不一致")
	}
	blocks := compactionEntries(t, store, id)
	if len(blocks) != 2 || blocks[0].FromSeq <= 0 || blocks[1].FromSeq <= blocks[0].ToSeq {
		t.Fatalf("blocks = %+v", blocks)
	}
	for _, block := range blocks {
		result, err := recallTool(store, id).Run(context.Background(), tools.Env{}, json.RawMessage(fmt.Sprintf(`{"index":%d}`, block.FromSeq)))
		if err != nil || strings.Contains(result.Text, "No message") {
			t.Fatalf("recall = %+v, %v", result, err)
		}
	}
}

func TestBlockWithoutSavingsCountsUsage(t *testing.T) {
	store := session.NewStore(t.TempDir())
	id := "no-savings"
	appendHistory(t, store, id, textMessage("user", "first"))
	for i := 0; i < 6; i++ {
		appendHistory(t, store, id, textMessage("assistant", strings.Repeat("a", 16000)))
	}
	summary := summaryResponse(strings.Repeat("s", 50000))
	summary.Usage = llm.Usage{Input: 123, Output: 45}
	client := &scriptClient{steps: []scriptStep{{response: summary}, {response: reply("stop")}}}
	var usage harness.Usage
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: id, ContextWindow: 40000, Emit: func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = *event.Usage
		}
	}}, "continue", nil)
	if err != nil || len(compactionEntries(t, store, id)) != 0 || usage.InputTokens != 123 || usage.OutputTokens != 45 {
		t.Fatalf("err=%v usage=%+v", err, usage)
	}
}

type noSavingsClient struct {
	requests []llm.Request
	steps    int
}

func (c *noSavingsClient) Stream(_ context.Context, req llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	c.requests = append(c.requests, req)
	if req.System == summaryInstruction {
		return summaryResponse(strings.Repeat("s", 50000)), nil
	}
	c.steps++
	if c.steps <= 2 {
		return reply("tool_use", call(fmt.Sprint(c.steps), "read", `{}`)), nil
	}
	return reply("stop"), nil
}

func TestBlockWithoutSavingsStopsRetrying(t *testing.T) {
	store := session.NewStore(t.TempDir())
	id := "no-savings-retry"
	appendHistory(t, store, id, textMessage("user", "first"))
	for i := 0; i < 6; i++ {
		appendHistory(t, store, id, textMessage("assistant", strings.Repeat("a", 16000)))
	}
	client := &noSavingsClient{}
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: id, ContextWindow: 40000, Tools: []tools.Tool{testTool("read")}}, "continue", nil)
	if err != nil {
		t.Fatal(err)
	}
	var summaries int
	for _, request := range client.requests {
		if request.System == summaryInstruction {
			summaries++
		}
	}
	if summaries != 1 || client.steps != 3 {
		t.Fatalf("summary requests = %d, model steps = %d", summaries, client.steps)
	}
	for _, entry := range compactionEntries(t, store, id) {
		if entry.FromSeq > 0 {
			t.Fatalf("unexpected block entry = %+v", entry)
		}
	}
}

type permissionClient struct {
	path     string
	requests []llm.Request
}

func (c *permissionClient) Stream(_ context.Context, req llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	c.requests = append(c.requests, req)
	if req.System == summaryInstruction {
		if err := os.Chmod(c.path, 0400); err != nil {
			return nil, err
		}
		return summaryResponse("summary"), nil
	}
	if err := os.Chmod(c.path, 0600); err != nil {
		return nil, err
	}
	return reply("stop"), nil
}

func TestBlockAppendFailureDoesNotChangeMessages(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	id := "append-failure"
	appendHistory(t, store, id, textMessage("user", "first"))
	for i := 0; i < 6; i++ {
		appendHistory(t, store, id, textMessage("assistant", strings.Repeat("a", 16000)))
	}
	client := &permissionClient{path: filepath.Join(dir, id+".jsonl")}
	var compacted bool
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: id, ContextWindow: 40000, Emit: func(event harness.Event) {
		compacted = compacted || event.Type == harness.EventContextCompacted
	}}, "continue", nil)
	if err != nil || compacted || len(client.requests) != 2 || len(compactionEntries(t, store, id)) != 0 {
		t.Fatalf("err=%v compacted=%v requests=%d", err, compacted, len(client.requests))
	}
	for _, message := range client.requests[1].Messages {
		if isSummaryMessage(message) {
			t.Fatal("写入失败后内存消息被压缩")
		}
	}
}

func TestNoStoreSkipsGrowthCompaction(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}, {response: reply("stop")}}}
	calls := 0
	err := Run(context.Background(), Options{Client: client, ContextWindow: 40000, Steer: func() []string {
		calls++
		if calls == 2 {
			return []string{strings.Repeat("x", 84000)}
		}
		return nil
	}}, "first", nil)
	if err != nil || len(client.requests) != 2 || client.requests[1].System == summaryInstruction {
		t.Fatalf("err=%v requests=%d", err, len(client.requests))
	}
}

func TestFindCutCanStartAfterInitialUser(t *testing.T) {
	messages := []llm.Message{textMessage("user", "start")}
	for i := 0; i < 40; i++ {
		messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call("id", "read", `{}`)}, Content: []llm.Part{{Type: "text", Text: strings.Repeat("a", 1000)}}}, toolMessage("id", strings.Repeat("x", 2000), false))
	}
	cut := findCut(messages, 12000)
	if cut <= 0 || messages[cut].Role != "assistant" {
		t.Fatalf("cut = %d", cut)
	}
	if keep := keepRecentTokens(12000); keep > 3000 {
		t.Fatalf("keep = %d, want <= 3000", keep)
	}
}

func TestCompactionTruncatesLargeToolResult(t *testing.T) {
	messages := []llm.Message{textMessage("user", strings.Repeat("u", 50000)), {Role: "assistant", ToolCalls: []llm.ToolCall{call("id", "read", `{}`)}}, toolMessage("id", strings.Repeat("x", 10000), false), textMessage("user", "next"), textMessage("assistant", strings.Repeat("recent", 2000)), textMessage("user", "current")}
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}}}
	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {
		t.Fatal(err)
	}
	for _, message := range client.requests[0].Messages {
		if message.Role == "tool" {
			text := message.Content[0].Text
			if len([]rune(text)) >= 4000 || !strings.Contains(text, "已截断") {
				t.Fatalf("tool result length=%d", len([]rune(text)))
			}
			return
		}
	}
	t.Fatalf("summary request omitted tool result: %+v", client.requests[0].Messages)
}

func TestCompactionPreservesSummaryWhenDroppingOldMessages(t *testing.T) {
	oldSummary := "old detail " + strings.Repeat("s", 20000)
	messages := []llm.Message{summaryMessage(oldSummary)}
	for i := 0; i < 30; i++ {
		messages = append(messages,
			textMessage("user", strings.Repeat("u", 2500)),
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call("id", "read", `{}`)}},
			toolMessage("id", strings.Repeat("t", 2500), false),
		)
	}
	messages = append(messages, textMessage("assistant", strings.Repeat("recent", 4000)), textMessage("user", "continue"))
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}}}
	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {
		t.Fatal(err)
	}
	summarized := client.requests[0].Messages
	if len(summarized) >= 91 || !isSummaryMessage(summarized[0]) || !strings.Contains(messageText(summarized[0]), "old detail") || !strings.Contains(messageText(summarized[0]), "已截断") {
		t.Fatalf("summary request did not preserve and truncate the old summary: %+v", summarized)
	}
	if messageText(messages[0]) != summaryPrefix+"\n\n"+oldSummary {
		t.Fatal("compaction modified the original summary")
	}
	for i, message := range summarized {
		if message.Role == "tool" && (i == 0 || len(summarized[i-1].ToolCalls) != 1 || summarized[i-1].ToolCalls[0].ID != message.ToolCallID) {
			t.Fatalf("orphaned tool message at %d", i)
		}
		if len(message.ToolCalls) > 0 && (i+1 == len(summarized) || summarized[i+1].Role != "tool" || summarized[i+1].ToolCallID != message.ToolCalls[0].ID) {
			t.Fatalf("tool call without result at %d", i)
		}
	}
}

func TestCompactionPrependsUserBeforeAssistant(t *testing.T) {
	messages := []llm.Message{textMessage("assistant", "earlier"), textMessage("user", "continue")}
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}}}
	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {
		t.Fatal(err)
	}
	summarized := client.requests[0].Messages
	if len(summarized) != 3 || summarized[0].Role != "user" || messageText(summarized[0]) != "（更早的对话已省略）" || summarized[1].Role != "assistant" || summarized[2].Role != "user" || !strings.Contains(messageText(summarized[2]), "总结以上对话") {
		t.Fatalf("summary request = %+v", summarized)
	}
}

func TestCompactionAppendsInstructionToUser(t *testing.T) {
	messages := []llm.Message{textMessage("user", "old"), textMessage("assistant", "recent")}
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}}}
	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {
		t.Fatal(err)
	}
	got := client.requests[0].Messages
	if len(got) != 1 || got[0].Role != "user" || len(got[0].Content) != 2 || !strings.Contains(messageText(got[0]), "总结以上对话") {
		t.Fatalf("summary request = %+v", got)
	}
	if len(messages[0].Content) != 1 {
		t.Fatal("original message was modified")
	}
}

func TestCompactionReplayRepairsBeforeKeepFrom(t *testing.T) {
	callMessage := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call("missing", "read", `{}`)}}
	encode := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	entries := []json.RawMessage{
		encode(messageEntry{Type: "message", Message: textMessage("user", "old")}),
		encode(messageEntry{Type: "message", Message: callMessage}),
		encode(messageEntry{Type: "message", Message: textMessage("assistant", "after compaction")}),
		encode(compactionEntry{Type: compactionEntryType, Summary: "summary", KeepFrom: 3}),
		encode(messageEntry{Type: "message", Message: textMessage("assistant", "new")}),
	}
	got := historyFromEntries(entries)
	if len(got) != 3 || !isSummaryMessage(got[0]) || got[1].Content[0].Text != "after compaction" || got[2].Content[0].Text != "new" {
		t.Fatalf("history = %+v", got)
	}
}

func TestRepairToolPairs(t *testing.T) {
	messages := []llm.Message{
		textMessage("user", "start"),
		toolMessage("stale", "old result", false),
		{Role: "assistant", ToolCalls: []llm.ToolCall{call("1", "read", `{}`), call("2", "read", `{}`)}},
		toolMessage("1", "ok", false),
		{Role: "assistant", ToolCalls: []llm.ToolCall{call("3", "read", `{}`)}},
		toolMessage("3", "ok", false),
		toolMessage("gone", "orphan", true),
	}
	repaired := repairToolPairs(messages)
	if len(repaired) != 6 {
		t.Fatalf("repaired = %+v", repaired)
	}
	if repaired[0].Role != "user" || repaired[1].Role != "assistant" || repaired[2].ToolCallID != "1" {
		t.Fatalf("repaired = %+v", repaired)
	}
	interrupted := repaired[3]
	if interrupted.Role != "tool" || interrupted.ToolCallID != "2" || !interrupted.IsError || interrupted.Content[0].Text != "Tool call was interrupted." {
		t.Fatalf("interrupted = %+v", interrupted)
	}
	if repaired[4].Role != "assistant" || repaired[5].ToolCallID != "3" {
		t.Fatalf("repaired = %+v", repaired)
	}
}

func replayTestEntries(values ...any) []json.RawMessage {
	var entries []json.RawMessage
	for _, value := range values {
		b, _ := json.Marshal(value)
		entries = append(entries, b)
	}
	return entries
}

func TestReplayBlocks(t *testing.T) {
	a, b, c, d, e, f := textMessage("user", "a"), textMessage("assistant", "b"), textMessage("user", "c"), textMessage("assistant", "d"), textMessage("user", "e"), textMessage("assistant", "f")
	entries := replayTestEntries(messageEntry{Type: "message", Message: a}, messageEntry{Type: "message", Message: b}, messageEntry{Type: "message", Message: c}, messageEntry{Type: "message", Message: d}, messageEntry{Type: "message", Message: e}, messageEntry{Type: "message", Message: f}, compactionEntry{Type: compactionEntryType, Summary: "B1", FromSeq: 2, ToSeq: 3}, compactionEntry{Type: compactionEntryType, Summary: "B2", FromSeq: 5, ToSeq: 5})
	want := []llm.Message{a, summaryMessage("B1"), d, summaryMessage("B2"), f}
	if got := historyFromEntries(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v", got)
	}
	if got := foldedItemsFromEntries(entries); !reflect.DeepEqual(got, []foldedMessage{{2, b}, {3, c}, {5, e}}) {
		t.Fatalf("folded = %+v", got)
	}
	if history := historyFromEntries(entries); !isSummaryMessage(history[1]) || !isSummaryMessage(history[3]) {
		t.Fatal("块摘要位置错误")
	}
	x, y := replayEntries(entries)
	x2, y2 := replayEntries(entries)
	if !reflect.DeepEqual(x, x2) || !reflect.DeepEqual(y, y2) {
		t.Fatal("重放结果不一致")
	}
}

func TestReplayLegacyThenBlock(t *testing.T) {
	a, b, c, d := textMessage("user", "a"), textMessage("assistant", "b"), textMessage("user", "c"), textMessage("assistant", "d")
	entries := replayTestEntries(messageEntry{Type: "message", Message: a}, messageEntry{Type: "message", Message: b}, messageEntry{Type: "message", Message: c}, messageEntry{Type: "message", Message: d}, compactionEntry{Type: compactionEntryType, Summary: "old", KeepFrom: 2})
	if got := historyFromEntries(entries); !reflect.DeepEqual(got, []llm.Message{summaryMessage("old"), c, d}) {
		t.Fatalf("legacy history = %+v", got)
	}
	if got := foldedFromEntries(entries); !reflect.DeepEqual(got, []llm.Message{a, b}) {
		t.Fatalf("legacy folded = %+v", got)
	}
	entries = append(entries, replayTestEntries(compactionEntry{Type: compactionEntryType, Summary: "block", FromSeq: 3, ToSeq: 3}, compactionEntry{Type: compactionEntryType, Summary: "new", KeepFrom: 2})...)
	if got := historyFromEntries(entries); !reflect.DeepEqual(got, []llm.Message{summaryMessage("new"), d}) {
		t.Fatalf("history = %+v", got)
	}
	if got := foldedItemsFromEntries(entries); !reflect.DeepEqual(got, []foldedMessage{{1, a}, {2, b}, {3, c}}) {
		t.Fatalf("folded = %+v", got)
	}
	if history := historyFromEntries(entries); !isSummaryMessage(history[0]) || isSummaryMessage(history[1]) {
		t.Fatal("整段摘要位置错误")
	}
}

func TestReplayInvalidBlocks(t *testing.T) {
	entries := replayTestEntries(messageEntry{Type: "message", Message: textMessage("user", "a")}, messageEntry{Type: "message", Message: textMessage("assistant", "b")}, messageEntry{Type: "message", Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call("id", "read", `{}`)}}}, messageEntry{Type: "message", Message: toolMessage("id", "ok", false)}, messageEntry{Type: "message", Message: textMessage("user", "e")}, compactionEntry{Type: compactionEntryType, Summary: "existing", FromSeq: 2, ToSeq: 2})
	for _, tc := range []struct {
		name  string
		entry compactionEntry
	}{
		{"empty", compactionEntry{FromSeq: 3, ToSeq: 4}}, {"reversed", compactionEntry{Summary: "x", FromSeq: 4, ToSeq: 3}}, {"future", compactionEntry{Summary: "x", FromSeq: 3, ToSeq: 6}}, {"first", compactionEntry{Summary: "x", FromSeq: 1, ToSeq: 1}}, {"overlap", compactionEntry{Summary: "x", FromSeq: 2, ToSeq: 4}}, {"call only", compactionEntry{Summary: "x", FromSeq: 3, ToSeq: 3}}, {"result only", compactionEntry{Summary: "x", FromSeq: 4, ToSeq: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.entry.Type = compactionEntryType
			before, folded := replayEntries(entries)
			after, foldedAfter := replayEntries(append(append([]json.RawMessage(nil), entries...), replayTestEntries(tc.entry)...))
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(folded, foldedAfter) {
				t.Fatalf("invalid block changed replay: %+v", after)
			}
		})
	}
}

func TestAutoCompactionFailureStopsRetrying(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "compact-fail",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	client := &scriptClient{steps: []scriptStep{
		{err: errors.New("summary unavailable")},
		{response: reply("tool_use", call("1", "read", `{}`))},
		{response: reply("stop")},
	}}
	var errorEvents int
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "compact-fail", ContextWindow: 12000, Tools: []tools.Tool{testTool("read")}, Emit: func(event harness.Event) {
		if event.Type == harness.EventError {
			errorEvents++
		}
	}}, "continue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if errorEvents != 0 {
		t.Fatalf("error events = %d, want 0", errorEvents)
	}
	var summaryRequests int
	for _, request := range client.requests {
		if request.System == summaryInstruction {
			summaryRequests++
		}
	}
	if summaryRequests != 1 {
		t.Fatalf("summary requests = %d, want 1", summaryRequests)
	}
	if len(client.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(client.requests))
	}
}

func TestCompactionBeforeRequest(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	appendHistory(t, store, "compact",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("earlier work summary")}, {response: reply("stop")}}}
	var compactions []harness.Event
	err := Run(context.Background(), Options{Client: client, Model: "test", System: strings.Repeat("s", 400), Tools: []tools.Tool{testTool("read")}, Store: store, SessionID: "compact", ContextWindow: 12000, Emit: func(event harness.Event) {
		if event.Type == harness.EventContextCompacted {
			compactions = append(compactions, event)
		}
	}}, "continue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	summaryRequest := client.requests[0]
	if summaryRequest.System != summaryInstruction || len(summaryRequest.Tools) != 0 || len(summaryRequest.Messages) != 3 {
		t.Fatalf("summary request = %+v", summaryRequest)
	}
	if summaryRequest.Messages[0].Role != "user" || !strings.Contains(messageText(summaryRequest.Messages[0]), "已截断") {
		t.Fatalf("first summary message = %+v", summaryRequest.Messages[0])
	}
	last := summaryRequest.Messages[len(summaryRequest.Messages)-1]
	if last.Role != "user" || !strings.Contains(messageText(last), "总结以上对话") {
		t.Fatalf("last summary message = %+v", last)
	}
	messages := client.requests[1].Messages
	if len(messages) != 2 || !isSummaryMessage(messages[0]) || !strings.Contains(messages[0].Content[0].Text, "earlier work summary") || messages[1].Content[0].Text != "continue" {
		t.Fatalf("messages = %+v", messages)
	}
	if len(compactions) != 1 || compactions[0].CompactTokensBefore == 0 || compactions[0].CompactTokensAfter == 0 {
		t.Fatalf("compaction events = %+v", compactions)
	}
	beforeMessages := []llm.Message{textMessage("user", strings.Repeat("a", 40000)), textMessage("assistant", "done"), textMessage("user", "continue")}
	if compactions[0].CompactTokensBefore <= int64(tokensForMessages(beforeMessages)) {
		t.Fatalf("compaction event lacks overhead: %+v", compactions[0])
	}
	entries := compactionEntries(t, store, "compact")
	if len(entries) != 1 || entries[0].Summary != "earlier work summary" || entries[0].KeepFrom <= 0 {
		t.Fatalf("compaction entries = %+v", entries)
	}
	data, err := os.ReadFile(filepath.Join(dir, "compact.jsonl"))
	if err != nil || !strings.Contains(string(data), strings.Repeat("a", 100)) {
		t.Fatalf("session lost old messages: %v", err)
	}
	raw, err := store.Load("compact")
	if err != nil {
		t.Fatal(err)
	}
	var lastEntry compactionEntry
	if err := json.Unmarshal(raw[len(raw)-2], &lastEntry); err != nil || lastEntry.Type != compactionEntryType || lastEntry.KeepFrom <= 0 {
		t.Fatalf("last entry before response = %+v, %v", lastEntry, err)
	}
	if got := historyFromEntries(raw[:len(raw)-1]); !reflect.DeepEqual(got, messages) {
		t.Fatalf("history at compaction = %+v, want %+v", got, messages)
	}
	want := append(append([]llm.Message(nil), messages...), reply("stop").Message)
	if got := historyFromEntries(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
}

func TestCompactionKeepsToolPairs(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "tools",
		textMessage("user", strings.Repeat("a", 60000)),
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call("call-1", "read", `{}`)}},
		llm.Message{Role: "tool", ToolCallID: "call-1", Content: []llm.Part{{Type: "text", Text: "file body"}}},
		textMessage("user", "next step"),
		textMessage("assistant", strings.Repeat("b", 100000)),
	)
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "tools", ContextWindow: 12000}, "continue", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	summarized := client.requests[0].Messages
	paired := false
	for i := 0; i+1 < len(summarized); i++ {
		if len(summarized[i].ToolCalls) == 1 && summarized[i+1].Role == "tool" && summarized[i+1].ToolCallID == "call-1" {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("summarized messages = %+v", summarized)
	}
	messages := client.requests[1].Messages
	if len(messages) != 3 || !isSummaryMessage(messages[0]) || messageText(messages[len(messages)-1]) != "continue" {
		t.Fatalf("messages = %+v", messages)
	}
	for _, message := range messages[1:] {
		if message.Role == "tool" {
			t.Fatalf("retained tool message without its call: %+v", messages)
		}
	}
}

func TestCompactionSkippedWithoutCut(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "nocut", ContextWindow: 8000}, strings.Repeat("a", 40000), nil); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(client.requests))
	}
	if entries := compactionEntries(t, store, "nocut"); len(entries) != 0 {
		t.Fatalf("compaction entries = %+v", entries)
	}
}

func TestOverflowCompactsAndRetries(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "overflow", textMessage("user", "earlier question"), textMessage("assistant", "earlier answer"))
	overflow := &llm.APIError{Status: 400, Body: "prompt is too long: 200000 tokens"}
	client := &scriptClient{steps: []scriptStep{
		{err: overflow},
		{response: summaryResponse("compact summary")},
		{response: reply("stop")},
	}}
	var compactions int
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "overflow", Emit: func(event harness.Event) {
		if event.Type == harness.EventContextCompacted {
			compactions++
		}
	}}, "next", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(client.requests))
	}
	if client.requests[1].System != summaryInstruction || !isSummaryMessage(client.requests[2].Messages[0]) {
		t.Fatalf("requests = %+v", client.requests)
	}
	if compactions != 1 {
		t.Fatalf("compactions = %d, want 1", compactions)
	}
	raw, err := store.Load("overflow")
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]llm.Message(nil), client.requests[2].Messages...), reply("stop").Message)
	if got := historyFromEntries(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
}

func TestOverflowTwiceReturnsError(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "overflow2", textMessage("user", "earlier question"), textMessage("assistant", "earlier answer"))
	overflow := &llm.APIError{Status: 400, Body: "prompt is too long: 200000 tokens"}
	client := &scriptClient{steps: []scriptStep{
		{err: overflow},
		{response: summaryResponse("compact summary")},
		{err: overflow},
	}}
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "overflow2"}, "next", nil)
	if !llm.IsContextOverflow(err) {
		t.Fatalf("error = %v", err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(client.requests))
	}
}

func TestCompactionUsageCountsTowardTurn(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "usage",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	summary := summaryResponse("earlier work summary")
	summary.Usage = llm.Usage{Input: 100, Output: 10, CacheRead: 20, CacheWrite: 30}
	final := reply("stop")
	final.Usage = llm.Usage{Input: 1, Output: 2}
	client := &scriptClient{steps: []scriptStep{{response: summary}, {response: final}}}
	var usage *harness.Usage
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "usage", ContextWindow: 12000, Emit: func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	}}, "continue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.InputTokens != 151 || usage.OutputTokens != 12 || usage.CachedTokens != 20 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestReloadUsesSummary(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	appendHistory(t, store, "reload",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	first := &scriptClient{steps: []scriptStep{{response: summaryResponse("first summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: first, Store: store, SessionID: "reload", ContextWindow: 12000}, "first prompt", nil); err != nil {
		t.Fatal(err)
	}
	second := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: second, Store: session.NewStore(dir), SessionID: "reload"}, "second prompt", nil); err != nil {
		t.Fatal(err)
	}
	messages := second.requests[0].Messages
	if len(messages) != 4 || !isSummaryMessage(messages[0]) || !strings.Contains(messages[0].Content[0].Text, "first summary") || messages[1].Content[0].Text != "first prompt" || messages[3].Content[0].Text != "second prompt" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestMultipleCompactionsKeepLatest(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "multi", textMessage("user", strings.Repeat("a", 40000)))
	oldSummary := "old summary " + strings.Repeat("old detail ", 5000)
	first := &scriptClient{steps: []scriptStep{{response: summaryResponse(oldSummary)}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: first, Store: store, SessionID: "multi", ContextWindow: 12000}, "first prompt", nil); err != nil {
		t.Fatal(err)
	}
	second := &scriptClient{steps: []scriptStep{{response: summaryResponse("new summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: second, Store: store, SessionID: "multi", ContextWindow: 12000}, "second prompt", nil); err != nil {
		t.Fatal(err)
	}
	if len(second.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(second.requests))
	}
	summarized := second.requests[0].Messages
	if len(summarized) != 4 || !isSummaryMessage(summarized[0]) || !strings.Contains(summarized[0].Content[0].Text, "old detail") {
		t.Fatalf("summarized messages = %+v", summarized)
	}
	if entries := compactionEntries(t, store, "multi"); len(entries) != 2 {
		t.Fatalf("compaction entries = %+v", entries)
	}
	entries, err := store.Load("multi")
	if err != nil {
		t.Fatal(err)
	}
	messages := historyFromEntries(entries)
	if len(messages) == 0 || !isSummaryMessage(messages[0]) || !strings.Contains(messages[0].Content[0].Text, "new summary") || strings.Contains(messages[0].Content[0].Text, "old summary") {
		t.Fatalf("history = %+v", messages)
	}
}

func TestDefaultContextWindow(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "default",
		textMessage("user", strings.Repeat("a", 500000)),
		textMessage("assistant", "ok"),
	)
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "default"}, "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || !isSummaryMessage(client.requests[1].Messages[0]) {
		t.Fatalf("requests = %+v", client.requests)
	}
}
