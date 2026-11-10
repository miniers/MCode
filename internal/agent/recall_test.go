package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

func TestFoldedFromEntries(t *testing.T) {
	encode := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	a := textMessage("user", "first")
	b := textMessage("assistant", "second")
	c := textMessage("user", "third")
	entries := []json.RawMessage{encode(messageEntry{Type: "message", Message: a}), encode(messageEntry{Type: "message", Message: b}), encode(compactionEntry{Type: compactionEntryType, Summary: "one", KeepFrom: 1})}
	if got := foldedFromEntries(entries); !reflect.DeepEqual(got, []llm.Message{a}) {
		t.Fatalf("first: %+v", got)
	}
	entries = append(entries, encode(messageEntry{Type: "message", Message: c}), encode(compactionEntry{Type: compactionEntryType, Summary: "two", KeepFrom: 2}))
	if got := foldedFromEntries(entries); !reflect.DeepEqual(got, []llm.Message{a, b}) {
		t.Fatalf("second: %+v", got)
	}
}

func TestRecall(t *testing.T) {
	store := session.NewStore(t.TempDir())
	appendHistory(t, store, "recall", textMessage("user", "Needle one"), llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call("id", "read", `{"path":"Needle"}`)}}, toolMessage("id", "NEEDLE result", false), textMessage("user", strings.Repeat("界", 20001)), textMessage("assistant", "recent"))
	if err := store.Append("recall", compactionEntry{Type: compactionEntryType, Summary: "summary", KeepFrom: 4}); err != nil {
		t.Fatal(err)
	}
	tool := recallTool(store, "recall")
	run := func(args string) string {
		t.Helper()
		r, err := tool.Run(context.Background(), tools.Env{}, json.RawMessage(args))
		if err != nil || r.IsError {
			t.Fatalf("recall: %+v %v", r, err)
		}
		return r.Text
	}
	got := run(`{"query":"needle","limit":2}`)
	if !strings.Contains(got, "Total matches: 3") || !strings.Contains(got, "#1 user") || !strings.Contains(got, "#2 assistant") || strings.Contains(got, "#3") {
		t.Fatal(got)
	}
	got = run(`{"query":"result"}`)
	if !strings.Contains(got, "#3 tool (id)") || !strings.Contains(got, "NEEDLE result") {
		t.Fatal(got)
	}
	if got = run(`{"index":1}`); !strings.Contains(got, "Needle one") {
		t.Fatal(got)
	}
	if got = run(`{"index":4}`); !strings.Contains(got, "[truncated]") || strings.Count(got, "界") != 20000 {
		t.Fatal("truncation failed")
	}
	if got = run(`{"query":"absent"}`); !strings.Contains(got, "No matches") {
		t.Fatal(got)
	}
	if got = run(`{}`); !strings.Contains(got, "query or index") {
		t.Fatal(got)
	}
}

func TestRecallAvailability(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "available"}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	for _, tool := range client.requests[0].Tools {
		if tool.Name == "recall" {
			t.Fatal("premature recall")
		}
	}
	appendHistory(t, store, "available", textMessage("user", strings.Repeat("a", 40000)), textMessage("assistant", "done"))
	client = &scriptClient{steps: []scriptStep{{response: summaryResponse("summary")}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "available", ContextWindow: 12000}, "continue", nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range client.requests[1].Tools {
		if tool.Name == "recall" {
			found = true
		}
	}
	if !found {
		t.Fatal("recall missing after compaction")
	}
	client = &scriptClient{steps: []scriptStep{
		{response: reply("tool_use", call("recall-id", "recall", `{"query":"hello"}`))},
		{response: reply("stop")},
	}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "available", Mode: harness.ModeFullAccess}, "again", nil); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, tool := range client.requests[0].Tools {
		if tool.Name == "recall" {
			found = true
		}
	}
	if !found {
		t.Fatal("recall missing after reload")
	}
	last := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if last.Role != "tool" || !strings.Contains(messageText(last), "hello") {
		t.Fatalf("recall result = %+v", last)
	}
}
