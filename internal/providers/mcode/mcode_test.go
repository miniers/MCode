package mcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
