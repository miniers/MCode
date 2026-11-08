package mcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

type fakeClient struct {
	responses []*llm.Response
	requests  []llm.Request
}

func (f *fakeClient) Stream(_ context.Context, request llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	f.requests = append(f.requests, request)
	if len(f.responses) == 0 {
		return nil, errors.New("no scripted response")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func answer() *llm.Response {
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.Part{{Type: "text", Text: "done"}}}, StopReason: "end_turn"}
}

func toolCall(name, id, args string) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{Name: name, ID: id, Arguments: json.RawMessage(args)}}}, StopReason: "tool_use"}
}

func harnessFor(client llm.Client, opts Options) *Harness {
	opts.ResolveClient = func(model, _ string) (llm.Client, string, error) { return client, model, nil }
	return New(opts)
}

func TestKind(t *testing.T) {
	if got := New(Options{}).Kind(); got != harness.Kind("mcode") {
		t.Fatalf("Kind() = %q", got)
	}
}

func TestResolveErrorEmitsErrorAndDone(t *testing.T) {
	wantErr := errors.New("model not found")
	h := New(Options{ResolveClient: func(string, string) (llm.Client, string, error) { return nil, "", wantErr }})
	var events []harness.Event
	err := h.Run(context.Background(), harness.Turn{Model: "missing"}, func(event harness.Event) { events = append(events, event) })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v", err)
	}
	if len(events) != 2 || events[0].Type != harness.EventError || events[1].Type != harness.EventDone || !strings.Contains(events[0].Err, "missing") {
		t.Fatalf("events = %#v", events)
	}
}

func TestDefaultModel(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	var resolved string
	h := New(Options{DefaultModel: "default-model", ResolveClient: func(model, _ string) (llm.Client, string, error) {
		resolved = model
		return client, "upstream-model", nil
	}})
	if err := h.Run(context.Background(), harness.Turn{Prompt: "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if resolved != "default-model" || client.requests[0].Model != "upstream-model" {
		t.Fatalf("resolved=%q request model=%q", resolved, client.requests[0].Model)
	}
}

func TestResolveClientAffinityKey(t *testing.T) {
	var affinityKey string
	h := New(Options{ResolveClient: func(model, key string) (llm.Client, string, error) {
		affinityKey = key
		return &fakeClient{responses: []*llm.Response{answer()}}, model, nil
	}})
	turn := harness.Turn{ThreadID: "thread", SessionID: "session"}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	if affinityKey != "thread" {
		t.Fatalf("affinity key = %q, want thread", affinityKey)
	}
	turn.ThreadID = ""
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	if affinityKey != "session" {
		t.Fatalf("affinity key without thread = %q, want session", affinityKey)
	}
}

func TestExtraToolDoesNotOverrideBuiltIn(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	h := harnessFor(client, Options{ExtraTools: func(context.Context, harness.Turn) []tools.Tool {
		return []tools.Tool{{Name: "bash", Description: "replacement"}, {Name: "custom", Description: "custom"}}
	}})
	if err := h.Run(context.Background(), harness.Turn{}, nil); err != nil {
		t.Fatal(err)
	}
	specs := client.requests[0].Tools
	var bashDescription string
	customCount := 0
	for _, spec := range specs {
		if spec.Name == "bash" {
			bashDescription = spec.Description
		}
		if spec.Name == "custom" {
			customCount++
		}
	}
	if bashDescription == "replacement" || bashDescription == "" || customCount != 1 {
		t.Fatalf("bash description=%q custom count=%d", bashDescription, customCount)
	}
}

type allowingInteractor struct{ calls int }

func (a *allowingInteractor) Approve(context.Context, harness.ApprovalRequest) (harness.Decision, error) {
	return harness.DecisionDeny, nil
}

func (a *allowingInteractor) Interact(context.Context, harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.calls++
	return harness.ApprovalResponse{Answers: map[string][]string{"permission": {"Allow for session"}}}, nil
}

func TestGateSessionSharedBetweenTurns(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCall("bash", "one", `{"command":"touch created"}`), answer(),
		toolCall("bash", "two", `{"command":"touch created"}`), answer(),
	}}
	approver := &allowingInteractor{}
	h := harnessFor(client, Options{})
	turn := harness.Turn{ThreadID: "thread", WorkDir: t.TempDir(), Mode: harness.ModeSupervised, Approver: approver}
	for range 2 {
		if err := h.Run(context.Background(), turn, nil); err != nil {
			t.Fatal(err)
		}
	}
	if approver.calls != 1 {
		t.Fatalf("approval requests = %d, want 1", approver.calls)
	}
}

func TestImagesBecomeParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	data := []byte("image bytes")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{responses: []*llm.Response{answer()}}
	h := harnessFor(client, Options{})
	if err := h.Run(context.Background(), harness.Turn{Images: []string{path}}, nil); err != nil {
		t.Fatal(err)
	}
	parts := client.requests[0].Messages[0].Content
	if len(parts) != 2 || parts[1].Type != "image" || parts[1].MimeType != "image/png" || parts[1].Data != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("image part = %#v", parts)
	}
}

func TestSessionHistoryRestored(t *testing.T) {
	storeDir := t.TempDir()
	client := &fakeClient{responses: []*llm.Response{answer(), answer()}}
	h := harnessFor(client, Options{StoreDir: storeDir})
	turn := harness.Turn{Prompt: "first", SessionID: "history-test"}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	turn.Prompt = "second"
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	messages := client.requests[1].Messages
	if len(messages) != 3 || messages[0].Content[0].Text != "first" || messages[2].Content[0].Text != "second" {
		t.Fatalf("restored history = %#v", messages)
	}
}

func TestTurnEnvAndPlanArePassedThrough(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	var system string
	h := harnessFor(client, Options{SystemPrompt: func(_ harness.Turn, names []string) string {
		system = strings.Join(names, ",")
		return "system"
	}})
	turn := harness.Turn{InteractionMode: "plan", Env: []string{"CUSTOM=value"}}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	if client.requests[0].Tools[0].Name != "read" || !strings.Contains(system, "bash") || !strings.Contains(client.requests[0].System, "Plan mode is on") {
		t.Fatal("expected tools, prompt callback and plan mode to be applied")
	}
}

// blockingClient 在第一次请求时阻塞，供测试在轮次运行中调用 Steer。
type blockingClient struct {
	mu       sync.Mutex
	requests []llm.Request
	started  chan struct{}
	release  chan struct{}
}

func (c *blockingClient) Stream(ctx context.Context, request llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	count := len(c.requests)
	c.mu.Unlock()
	if count > 1 {
		return answer(), nil
	}
	close(c.started)
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return toolCall("read", "call-1", `{}`), nil
}

func TestContextWindowReachesUsage(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	var resolved string
	h := harnessFor(client, Options{ContextWindow: func(model string) int {
		resolved = model
		return 321000
	}})
	var usage *harness.Usage
	err := h.Run(context.Background(), harness.Turn{Model: "magpie-jp/auto/gpt-6.1-terra"}, func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "magpie-jp/auto/gpt-6.1-terra" || usage == nil || usage.ContextWindow != 321000 {
		t.Fatalf("resolved=%q usage=%+v", resolved, usage)
	}
}

func TestSteerInjectsIntoRunningSession(t *testing.T) {
	client := &blockingClient{started: make(chan struct{}), release: make(chan struct{})}
	h := harnessFor(client, Options{})
	done := make(chan error, 1)
	go func() {
		done <- h.Run(context.Background(), harness.Turn{SessionID: "live", Prompt: "start"}, nil)
	}()
	<-client.started
	if err := h.Steer(context.Background(), "live", "please check the tests"); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	close(client.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	steered := false
	for _, message := range client.requests[1].Messages {
		if message.Role == "user" && len(message.Content) == 1 && message.Content[0].Text == "please check the tests" {
			steered = true
		}
	}
	if !steered {
		t.Fatalf("第二次请求 = %+v", client.requests[1].Messages)
	}
}

func TestSteerNotRunning(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	h := harnessFor(client, Options{})
	if err := h.Steer(context.Background(), "missing", "text"); !errors.Is(err, harness.ErrNotRunning) {
		t.Fatalf("未运行会话的 Steer() error = %v", err)
	}
	if err := h.Steer(context.Background(), "", "text"); !errors.Is(err, harness.ErrNotRunning) {
		t.Fatalf("空会话 ID 的 Steer() error = %v", err)
	}
	if err := h.Steer(context.Background(), "missing", "  "); err != nil {
		t.Fatalf("空文本的 Steer() error = %v", err)
	}
	if err := h.Run(context.Background(), harness.Turn{SessionID: "live"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.Steer(context.Background(), "live", "text"); !errors.Is(err, harness.ErrNotRunning) {
		t.Fatalf("已结束会话的 Steer() error = %v", err)
	}
}
