package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
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

func TestCompactionBeforeRequest(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "compact",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	client := &scriptClient{steps: []scriptStep{{response: summaryResponse("earlier work summary")}, {response: reply("stop")}}}
	var compactions []harness.Event
	err := Run(context.Background(), Options{Client: client, Model: "test", Store: store, SessionID: "compact", ContextWindow: 21000, Emit: func(event harness.Event) {
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
	if len(compactions) != 2 || compactions[0].CompactTokensBefore == 0 || compactions[0].CompactTokensAfter != 0 || compactions[1].CompactTokensAfter == 0 {
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
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "tools", ContextWindow: 21000}, "continue", nil); err != nil {
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
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "nocut", ContextWindow: 17000}, strings.Repeat("a", 40000), nil); err != nil {
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
	if compactions != 2 {
		t.Fatalf("compactions = %d, want 2", compactions)
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

func TestReloadUsesSummary(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "reload",
		textMessage("user", strings.Repeat("a", 40000)),
		textMessage("assistant", "done"),
	)
	first := &scriptClient{steps: []scriptStep{{response: summaryResponse("first summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: first, Store: store, SessionID: "reload", ContextWindow: 21000}, "first prompt", nil); err != nil {
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
	if err := Run(context.Background(), Options{Client: first, Store: store, SessionID: "multi", ContextWindow: 21000}, "first prompt", nil); err != nil {
		t.Fatal(err)
	}
	second := &scriptClient{steps: []scriptStep{{response: summaryResponse("new summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: second, Store: store, SessionID: "multi", ContextWindow: 21000}, "second prompt", nil); err != nil {
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
