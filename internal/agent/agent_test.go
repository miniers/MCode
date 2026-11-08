package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/agent/gate"
	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

type scriptStep struct {
	response *llm.Response
	err      error
	delta    llm.Delta
}

type scriptClient struct {
	steps    []scriptStep
	requests []llm.Request
}

func (c *scriptClient) Stream(_ context.Context, request llm.Request, emit func(llm.Delta)) (*llm.Response, error) {
	c.requests = append(c.requests, request)
	if len(c.steps) == 0 {
		return nil, errors.New("script exhausted")
	}
	step := c.steps[0]
	c.steps = c.steps[1:]
	if step.delta.Text != "" {
		emit(step.delta)
	}
	return step.response, step.err
}

type testApprover struct {
	response harness.ApprovalResponse
	requests []harness.ApprovalRequest
}

func (a *testApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	a.requests = append(a.requests, req)
	return a.response.Decision, nil
}

func (a *testApprover) Interact(_ context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.requests = append(a.requests, req)
	return a.response, nil
}

func reply(reason string, calls ...llm.ToolCall) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.Part{{Type: "text", Text: "answer"}}, ToolCalls: calls}, StopReason: reason}
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func testTool(name string) tools.Tool {
	return tools.Tool{Name: name, Parameters: json.RawMessage(`{"type":"object"}`), Run: func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "result"}, nil
	}}
}

func loadMessages(t *testing.T, store *session.Store, id string) []llm.Message {
	t.Helper()
	entries, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	var messages []llm.Message
	for _, raw := range entries {
		var entry messageEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Type != "message" {
			t.Fatalf("unexpected entry type %q", entry.Type)
		}
		messages = append(messages, entry.Message)
	}
	return messages
}

func TestTextAndToolConversation(t *testing.T) {
	for _, withTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "tool"}[withTool], func(t *testing.T) {
			store := session.NewStore(t.TempDir())
			client := &scriptClient{steps: []scriptStep{{response: reply("stop"), delta: llm.Delta{Type: "text", Text: "answer"}}}}
			want := []harness.EventType{harness.EventSession, harness.EventTurnAccepted, harness.EventText, harness.EventUsage, harness.EventDone}
			if withTool {
				client.steps = []scriptStep{{response: reply("tool_use", call("1", "read", `{}`))}, {response: reply("stop"), delta: llm.Delta{Type: "text", Text: "answer"}}}
				want = []harness.EventType{harness.EventSession, harness.EventTurnAccepted, harness.EventToolStart, harness.EventToolEnd, harness.EventText, harness.EventUsage, harness.EventDone}
			}
			var events []harness.EventType
			err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "test", Tools: []tools.Tool{testTool("read")}, Emit: func(event harness.Event) {
				events = append(events, event.Type)
			}}, "hello", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
			messages := loadMessages(t, store, "test")
			if !withTool && (len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant") {
				t.Fatalf("messages = %+v", messages)
			}
			if withTool && (len(messages) != 4 || messages[2].Role != "tool" || messages[2].ToolCallID != "1" || messages[2].Content[0].Text != "result") {
				t.Fatalf("messages = %+v", messages)
			}
		})
	}
}

func TestGateDecisions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		plan         bool
		response     harness.ApprovalResponse
		wantError    bool
		wantRequests int
	}{
		{name: "deny plan", plan: true, wantError: true},
		{name: "rejected", response: harness.ApprovalResponse{Decision: harness.DecisionDeny}, wantError: true, wantRequests: 2},
		{name: "session allow", response: harness.ApprovalResponse{Answers: map[string][]string{"choice": {"Allow for session"}}}, wantRequests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approver := &testApprover{response: tc.response}
			client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", call("1", "write", `{"path":"a"}`), call("2", "write", `{"path":"a"}`))}, {response: reply("stop")}}}
			err := Run(context.Background(), Options{Client: client, Tools: []tools.Tool{testTool("write")}, Mode: harness.ModeSupervised, Plan: tc.plan, Approver: approver}, "hello", nil)
			if err != nil {
				t.Fatal(err)
			}
			messages := client.requests[1].Messages
			if messages[2].IsError != tc.wantError || messages[3].IsError != tc.wantError {
				t.Fatalf("results = %+v", messages)
			}
			if len(approver.requests) != tc.wantRequests {
				t.Fatalf("approval requests = %d, want %d", len(approver.requests), tc.wantRequests)
			}
		})
	}
}

func TestResumeAndPlan(t *testing.T) {
	store := session.NewStore(t.TempDir())
	if err := store.Append("old", messageEntry{Type: "message", Message: llm.Message{Role: "user", Content: []llm.Part{{Type: "text", Text: "old"}}}}, map[string]string{"type": "unknown"}); err != nil {
		t.Fatal(err)
	}
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "old", Plan: true, System: "system"}, "new", nil); err != nil {
		t.Fatal(err)
	}
	req := client.requests[0]
	if len(req.Messages) != 2 || req.Messages[0].Content[0].Text != "old" || req.Messages[1].Content[0].Text != "new" {
		t.Fatalf("history = %+v", req.Messages)
	}
	if !strings.Contains(req.System, gate.PlanModePrompt()) {
		t.Fatalf("system = %q", req.System)
	}
}

func TestRetryAndRetract(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{err: &llm.APIError{Retryable: true}, delta: llm.Delta{Type: "text", Text: "partial"}}, {response: reply("stop")}}}
	var events []harness.Event
	if err := Run(context.Background(), Options{Client: client, Emit: func(event harness.Event) {
		events = append(events, event)
	}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == harness.EventTextRetract && event.Text == "partial" {
			found = true
		}
	}
	if !found || len(client.requests) != 2 {
		t.Fatalf("events = %+v, requests = %d", events, len(client.requests))
	}
}

func TestMaxRounds(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", call("1", "read", `{}`))}}}
	var events []harness.Event
	err := Run(context.Background(), Options{Client: client, MaxRounds: 1, Tools: []tools.Tool{testTool("read")}, Emit: func(event harness.Event) {
		events = append(events, event)
	}}, "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "maximum rounds") {
		t.Fatalf("error = %v", err)
	}
	if events[len(events)-2].Type != harness.EventUsage || events[len(events)-1].Type != harness.EventDone {
		t.Fatalf("events = %+v", events)
	}
}

func TestCancellationClosesToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", call("1", "read", `{}`), call("2", "read", `{}`))}}}
	tool := testTool("read")
	tool.Run = func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
		cancel()
		return tools.Result{}, context.Canceled
	}
	err := Run(ctx, Options{Client: client, Store: store, SessionID: "cancel", Tools: []tools.Tool{tool}}, "hello", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	messages := loadMessages(t, store, "cancel")
	if len(messages) != 4 || messages[2].ToolCallID != "1" || messages[3].ToolCallID != "2" || !messages[2].IsError || !messages[3].IsError {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestIncompleteToolArguments(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{{response: reply("length", call("1", "write", `{"path":`))}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "incomplete", Tools: []tools.Tool{testTool("write")}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	messages := loadMessages(t, store, "incomplete")
	if len(messages) != 4 || !messages[2].IsError || !strings.Contains(messages[2].Content[0].Text, "incomplete") {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestUsageAccumulation(t *testing.T) {
	first := reply("tool_use", call("1", "read", `{}`))
	first.Usage = llm.Usage{Input: 10, Output: 2, CacheRead: 3, CacheWrite: 4}
	second := reply("stop")
	second.Usage = llm.Usage{Input: 5, Output: 1, CacheRead: 2}
	client := &scriptClient{steps: []scriptStep{{response: first}, {response: second}}}
	var usage *harness.Usage
	if err := Run(context.Background(), Options{Client: client, Tools: []tools.Tool{testTool("read")}, Emit: func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.InputTokens != 24 || usage.OutputTokens != 3 || usage.CachedTokens != 9 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAskUserQuestion(t *testing.T) {
	approver := &testApprover{response: harness.ApprovalResponse{Answers: map[string][]string{"question": {"yes"}}}}
	builtin := BuiltinTools(approver)
	result, err := builtin[0].Run(context.Background(), tools.Env{}, json.RawMessage(`{"question":"Continue?","options":["yes","no"]}`))
	if err != nil || result.IsError || result.Text != "yes" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if len(approver.requests) != 1 || approver.requests[0].Kind != harness.RequestKindQuestion || len(approver.requests[0].Questions[0].Choices) != 2 {
		t.Fatalf("requests = %+v", approver.requests)
	}
}
