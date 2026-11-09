package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
	steps        []scriptStep
	requests     []llm.Request
	requestTimes []time.Time
}

func (c *scriptClient) Stream(_ context.Context, request llm.Request, emit func(llm.Delta)) (*llm.Response, error) {
	c.requestTimes = append(c.requestTimes, time.Now())
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

func TestToolChangesEmitFileChangeEvents(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{
		{response: reply("tool_use", call("1", "write", `{}`))},
		{response: reply("stop")},
	}}
	tool := testTool("write")
	tool.Run = func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "done", Changes: []tools.FileChange{
			{Path: "a.go", Patch: "-old\n+new\n"},
			{Path: "b.go", Patch: "-x\n+y\n"},
		}}, nil
	}
	var changes []harness.Event
	opts := Options{Client: client, Store: store, SessionID: "changes", Mode: harness.ModeFullAccess, Tools: []tools.Tool{tool}, Emit: func(event harness.Event) {
		if event.Type == harness.EventFileChange {
			changes = append(changes, event)
		}
	}}
	if err := Run(context.Background(), opts, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].Path != "a.go" || changes[1].Path != "b.go" {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Patch != "-old\n+new\n" {
		t.Fatalf("patch = %q", changes[0].Patch)
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

func TestRetryAfter(t *testing.T) {
	previous := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = previous })

	client := &scriptClient{steps: []scriptStep{
		{err: &llm.APIError{Status: 429, Retryable: true, RetryAfter: 50 * time.Millisecond}},
		{response: reply("stop")},
	}}
	if err := Run(context.Background(), Options{Client: client}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.requestTimes) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requestTimes))
	}
	if elapsed := client.requestTimes[1].Sub(client.requestTimes[0]); elapsed < 50*time.Millisecond {
		t.Fatalf("retry delay = %v, want at least 50ms", elapsed)
	}
}

func TestRetryAfterExceedsMaximum(t *testing.T) {
	previous := maxRetryDelay
	maxRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { maxRetryDelay = previous })

	apiErr := &llm.APIError{Status: 429, Retryable: true, RetryAfter: 50 * time.Millisecond}
	client := &scriptClient{steps: []scriptStep{
		{err: apiErr, delta: llm.Delta{Type: "text", Text: "partial"}},
		{response: reply("stop")},
	}}
	var retracted string
	err := Run(context.Background(), Options{Client: client, Emit: func(event harness.Event) {
		if event.Type == harness.EventTextRetract {
			retracted += event.Text
		}
	}}, "hello", nil)
	if !errors.Is(err, apiErr) || len(client.requests) != 1 || retracted != "partial" {
		t.Fatalf("error = %v, requests = %d, retracted = %q", err, len(client.requests), retracted)
	}
}

func TestRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client := &scriptClient{steps: []scriptStep{
		{err: &llm.APIError{Status: 429, Retryable: true, RetryAfter: 5 * time.Second}},
		{response: reply("stop")},
	}}
	err := Run(ctx, Options{Client: client}, "hello", nil)
	if !errors.Is(err, context.DeadlineExceeded) || len(client.requests) != 1 {
		t.Fatalf("error = %v, requests = %d", err, len(client.requests))
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
	var ends []harness.Event
	err := Run(ctx, Options{Client: client, Store: store, SessionID: "cancel", Tools: []tools.Tool{tool}, Emit: func(e harness.Event) {
		if e.Type == harness.EventToolEnd {
			ends = append(ends, e)
		}
	}}, "hello", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	messages := loadMessages(t, store, "cancel")
	if len(messages) != 4 || messages[2].ToolCallID != "1" || messages[3].ToolCallID != "2" || !messages[2].IsError || !messages[3].IsError {
		t.Fatalf("messages = %+v", messages)
	}
	if len(ends) != 2 || ends[0].ToolID != "1" || ends[1].ToolID != "2" || ends[0].ToolName != "read" || ends[1].Output != "Cancelled" || !ends[0].IsError || !ends[1].IsError {
		t.Fatalf("ends = %+v", ends)
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

func TestToolCallsWithStopReasonAreExecuted(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{{response: reply("stop", call("1", "read", `{}`))}, {response: reply("stop")}}}
	executed := 0
	tool := testTool("read")
	run := tool.Run
	tool.Run = func(ctx context.Context, env tools.Env, args json.RawMessage) (tools.Result, error) {
		executed++
		return run(ctx, env, args)
	}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "stop-calls", Tools: []tools.Tool{tool}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("executed = %d, want 1", executed)
	}
	messages := loadMessages(t, store, "stop-calls")
	if len(messages) != 4 || messages[2].Role != "tool" || messages[2].ToolCallID != "1" {
		t.Fatalf("messages = %+v", messages)
	}
	if len(client.requests) != 2 || len(client.requests[1].Messages) != 3 || client.requests[1].Messages[2].Role != "tool" {
		t.Fatalf("requests = %+v", client.requests)
	}
}

func TestMaxTokensFromOptions(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	err := Run(context.Background(), Options{Client: client, Model: "test", MaxTokens: func(model string) int {
		if model != "test" {
			t.Fatalf("model = %q", model)
		}
		return 4096
	}}, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.requests[0].MaxTokens != 4096 {
		t.Fatalf("max tokens = %d, want 4096", client.requests[0].MaxTokens)
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
	if usage == nil || usage.InputTokens != 24 || usage.OutputTokens != 3 || usage.CachedTokens != 5 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestUsageReportsContextWindow(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	var usage *harness.Usage
	if err := Run(context.Background(), Options{Client: client, ContextWindow: 321000, Emit: func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.ContextWindow != 321000 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestUsageUnknownContextWindow(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}}}
	var usage *harness.Usage
	if err := Run(context.Background(), Options{Client: client, Emit: func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.ContextWindow != 0 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestResumeDoesNotDuplicatePrompt(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{{err: errors.New("request failed")}, {response: reply("stop")}}}
	opts := Options{Client: client, Store: store, SessionID: "retry"}
	if err := Run(context.Background(), opts, "hello", nil); err == nil {
		t.Fatal("expected failure")
	}
	if err := Run(context.Background(), opts, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := client.requests[1].Messages; len(got) != 1 || messageText(got[0]) != "hello" {
		t.Fatalf("request messages = %+v", got)
	}
	if got := loadMessages(t, store, "retry"); len(got) != 2 || messageText(got[0]) != "hello" || got[1].Role != "assistant" {
		t.Fatalf("stored messages = %+v", got)
	}
}

func TestUsageReportsLastRequestContextTokens(t *testing.T) {
	first := reply("tool_use", call("1", "read", `{}`))
	first.Usage = llm.Usage{Input: 1000, CacheRead: 500, CacheWrite: 10, Output: 50}
	second := reply("stop")
	second.Usage = llm.Usage{Input: 1200, CacheRead: 600, CacheWrite: 20, Output: 80}
	client := &scriptClient{steps: []scriptStep{{response: first}, {response: second}}}
	var usage *harness.Usage
	if err := Run(context.Background(), Options{Client: client, Tools: []tools.Tool{testTool("read")}, Emit: func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.ContextTokens != 1900 || usage.InputTokens != 3330 || usage.OutputTokens != 130 || usage.CachedTokens != 1100 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestStopReasonsEmitErrors(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{
		{"refusal", "模型拒绝回答或内容被过滤（refusal）"},
		{"content_filter", "模型拒绝回答或内容被过滤（content_filter）"},
		{"length", "回复达到输出上限被截断"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var got *harness.Event
			response := reply(tc.reason)
			client := &scriptClient{steps: []scriptStep{{response: response}}}
			if err := Run(context.Background(), Options{Client: client, Emit: func(event harness.Event) {
				if event.Type == harness.EventError {
					copy := event
					got = &copy
				}
			}}, "hello", nil); err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Err != tc.want || got.ErrKind != harness.ErrKindAPIError {
				t.Fatalf("error event = %+v", got)
			}
		})
	}
}

func TestModelRequestErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{429, harness.ErrKindRateLimited}, {529, harness.ErrKindRateLimited}, {401, harness.ErrKindAPIKeyRejected}, {403, harness.ErrKindAPIKeyRejected}, {500, harness.ErrKindAPIError}} {
		client := &scriptClient{steps: []scriptStep{{err: &llm.APIError{Status: tc.status}}}}
		var got harness.Event
		_ = Run(context.Background(), Options{Client: client, Emit: func(event harness.Event) {
			if event.Type == harness.EventError {
				got = event
			}
		}}, "hello", nil)
		if got.ErrKind != tc.want {
			t.Errorf("status %d kind = %q, want %q", tc.status, got.ErrKind, tc.want)
		}
	}
}

func TestSteerInjectedBeforeRequest(t *testing.T) {
	store := session.NewStore(t.TempDir())
	client := &scriptClient{steps: []scriptStep{
		{response: reply("tool_use", call("1", "read", `{}`))},
		{response: reply("stop")},
	}}
	pending := []string{"change course"}
	err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "steer", Tools: []tools.Tool{testTool("read")}, Steer: func() []string {
		texts := pending
		pending = nil
		return texts
	}}, "start", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, request := range client.requests {
		count := 0
		for _, message := range request.Messages {
			if message.Role == "user" && len(message.Content) == 1 && message.Content[0].Text == "change course" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("request %d 中的插话消息数 = %d，想要 1", i, count)
		}
	}
	messages := loadMessages(t, store, "steer")
	found := false
	for _, message := range messages {
		if message.Role == "user" && message.Content[0].Text == "change course" {
			found = true
		}
	}
	if !found {
		t.Fatalf("持久化的消息里没有插话：%+v", messages)
	}
}

func TestSteerAfterFinalReplyContinuesTurn(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{{response: reply("stop")}, {response: reply("stop")}}}
	calls := 0
	err := Run(context.Background(), Options{Client: client, SessionID: "steer-final", Steer: func() []string {
		calls++
		if calls == 2 {
			return []string{"one more thing"}
		}
		return nil
	}}, "start", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	last := client.requests[1].Messages
	if len(last) != 3 || last[2].Role != "user" || last[2].Content[0].Text != "one more thing" {
		t.Fatalf("第二次请求 = %+v", last)
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

func readOnlyTestTool(name string) tools.Tool {
	tool := testTool(name)
	tool.ReadOnly = true
	return tool
}

func TestParallelReadOnlyToolCalls(t *testing.T) {
	store := session.NewStore(t.TempDir())
	calls := []llm.ToolCall{call("1", "read", `{"n":1}`), call("2", "read", `{"n":2}`), call("3", "read", `{"n":3}`)}
	client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", calls...)}, {response: reply("stop")}}}
	var barrier sync.WaitGroup
	barrier.Add(len(calls))
	tool := readOnlyTestTool("read")
	tool.Run = func(_ context.Context, _ tools.Env, args json.RawMessage) (tools.Result, error) {
		barrier.Done()
		barrier.Wait()
		return tools.Result{Text: string(args)}, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{Client: client, Store: store, SessionID: "parallel", Tools: []tools.Tool{tool}, Mode: harness.ModeFullAccess}, "hello", nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parallel read-only tool calls did not run concurrently")
	}
	messages := loadMessages(t, store, "parallel")
	if len(messages) != 6 {
		t.Fatalf("messages = %+v", messages)
	}
	for i, c := range calls {
		message := messages[2+i]
		if message.Role != "tool" || message.ToolCallID != c.ID || message.IsError || message.Content[0].Text != string(c.Arguments) {
			t.Fatalf("tool message %d = %+v, want call %s", i, message, c.ID)
		}
	}
}

func TestMixedToolCallsPreserveOrder(t *testing.T) {
	store := session.NewStore(t.TempDir())
	calls := []llm.ToolCall{call("1", "read", `{}`), call("2", "write", `{}`), call("3", "grep", `{}`)}
	client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", calls...)}, {response: reply("stop")}}}
	opts := Options{Client: client, Store: store, SessionID: "mixed", Mode: harness.ModeFullAccess, Tools: []tools.Tool{readOnlyTestTool("read"), testTool("write"), readOnlyTestTool("grep")}}
	if err := Run(context.Background(), opts, "hello", nil); err != nil {
		t.Fatal(err)
	}
	messages := loadMessages(t, store, "mixed")
	if len(messages) != 6 {
		t.Fatalf("messages = %+v", messages)
	}
	for i, c := range calls {
		message := messages[2+i]
		if message.Role != "tool" || message.ToolCallID != c.ID || message.IsError || message.Content[0].Text != "result" {
			t.Fatalf("tool message %d = %+v, want call %s", i, message, c.ID)
		}
	}
}

func TestParallelToolPanicIsRecovered(t *testing.T) {
	store := session.NewStore(t.TempDir())
	calls := []llm.ToolCall{call("1", "read", `{}`), call("2", "read", `{"panic":true}`), call("3", "read", `{}`)}
	client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", calls...)}, {response: reply("stop")}}}
	tool := readOnlyTestTool("read")
	tool.Run = func(_ context.Context, _ tools.Env, args json.RawMessage) (tools.Result, error) {
		if strings.Contains(string(args), "panic") {
			panic("boom")
		}
		return tools.Result{Text: "result"}, nil
	}
	opts := Options{Client: client, Store: store, SessionID: "panic", Mode: harness.ModeFullAccess, Tools: []tools.Tool{tool}}
	if err := Run(context.Background(), opts, "hello", nil); err != nil {
		t.Fatal(err)
	}
	messages := loadMessages(t, store, "panic")
	if len(messages) != 6 {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[3].ToolCallID != "2" || !messages[3].IsError || !strings.Contains(messages[3].Content[0].Text, "tool panicked") {
		t.Fatalf("panicked message = %+v", messages[3])
	}
	for _, i := range []int{2, 4} {
		if messages[i].IsError || messages[i].Content[0].Text != "result" {
			t.Fatalf("message %d = %+v", i, messages[i])
		}
	}
}

func TestSerialToolPanicIsRecovered(t *testing.T) {
	store := session.NewStore(t.TempDir())
	tool := testTool("read")
	tool.Run = func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) { panic("serial boom") }
	client := &scriptClient{steps: []scriptStep{{response: reply("tool_use", call("1", "read", `{}`))}, {response: reply("stop")}}}
	if err := Run(context.Background(), Options{Client: client, Store: store, SessionID: "serial-panic", Tools: []tools.Tool{tool}}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	messages := loadMessages(t, store, "serial-panic")
	if len(messages) != 4 || !messages[2].IsError || messages[2].Content[0].Text != "tool panicked: serial boom" {
		t.Fatalf("messages = %+v", messages)
	}
}
