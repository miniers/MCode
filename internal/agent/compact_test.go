package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	} {
		if got := compactThreshold(tc.window); got != tc.want {
			t.Fatalf("compactThreshold(%d) = %d, want %d", tc.window, got, tc.want)
		}
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
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "compact",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("earlier work summary")}, {response: reply("stop")}}}
	var compactions []harness.Event
	err := Run(context.Background(), Options{Client: client, Model: "test", Store: store, SessionID: "compact", ContextWindow: 12000, Emit: func(event harness.Event) {
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
	if summaryRequest.System != summaryInstruction || len(summaryRequest.Tools) != 0 || len(summaryRequest.Messages) != 2 {
		t.Fatalf("summary request = %+v", summaryRequest)
	}
	messages := client.requests[1].Messages
	if len(messages) != 2 || !isSummaryMessage(messages[0]) || !strings.Contains(messages[0].Content[0].Text, "earlier work summary") || messages[1].Content[0].Text != "continue" {
		t.Fatalf("messages = %+v", messages)
	}
	if len(compactions) != 1 || compactions[0].CompactTokensBefore == 0 || compactions[0].CompactTokensAfter == 0 {
		t.Fatalf("compaction events = %+v", compactions)
	}
	entries := compactionEntries(t, store, "compact")
	if len(entries) != 1 || entries[0].Summary != "earlier work summary" || entries[0].KeepFrom != 2 {
		t.Fatalf("compaction entries = %+v", entries)
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
	if len(summarized) != 3 || len(summarized[1].ToolCalls) != 1 || summarized[2].Role != "tool" || summarized[2].ToolCallID != "call-1" {
		t.Fatalf("summarized messages = %+v", summarized)
	}
	messages := client.requests[1].Messages
	if len(messages) != 4 || !isSummaryMessage(messages[0]) || messages[1].Content[0].Text != "next step" {
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
	if usage == nil || usage.InputTokens != 151 || usage.OutputTokens != 12 || usage.CachedTokens != 50 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestReloadUsesSummary(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "reload",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	first := &scriptClient{steps: []scriptStep{{response: summaryResponse("first summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: first, Store: store, SessionID: "reload", ContextWindow: 12000}, "first prompt", nil); err != nil {
		t.Fatal(err)
	}
	second := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: second, Store: store, SessionID: "reload"}, "second prompt", nil); err != nil {
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
	if len(summarized) != 3 || !isSummaryMessage(summarized[0]) || !strings.Contains(summarized[0].Content[0].Text, "old detail") {
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
