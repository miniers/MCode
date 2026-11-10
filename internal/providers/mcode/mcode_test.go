package mcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/sandbox"
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
	opts.ResolveClient = func(_ context.Context, model, _ string) (llm.Client, string, error) { return client, model, nil }
	return New(opts)
}

type fakeBashSandbox struct{}

func (*fakeBashSandbox) Command(context.Context, string, harness.PermissionMode, string, ...string) (*exec.Cmd, func(), error) {
	return nil, nil, nil
}

func TestBashSandbox(t *testing.T) {
	explicit := &fakeBashSandbox{}
	for _, tt := range []struct {
		name     string
		turn     harness.Turn
		explicit bool
		readOnly bool
	}{
		{name: "explicit", turn: harness.Turn{Sandbox: explicit}, explicit: true},
		{name: "explicit read only plan", turn: harness.Turn{Sandbox: explicit, Mode: harness.ModeReadOnly, InteractionMode: "plan"}, explicit: true},
		{name: "auto", turn: harness.Turn{Mode: harness.ModeAuto}},
		{name: "read only", turn: harness.Turn{Mode: harness.ModeReadOnly}, readOnly: true},
		{name: "plan", turn: harness.Turn{Mode: harness.ModeAuto, InteractionMode: "plan"}, readOnly: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := bashSandbox(tt.turn)
			if tt.explicit {
				if got != explicit {
					t.Fatalf("explicit sandbox replaced: %T", got)
				}
			} else if tt.readOnly {
				if sandbox.IsReadOnly(got) != (sandbox.ReadOnly() != nil) {
					t.Fatalf("read-only sandbox = %T", got)
				}
			} else if got != nil {
				t.Fatalf("unexpected sandbox: %T", got)
			}
		})
	}
}

func TestKind(t *testing.T) {
	if got := New(Options{}).Kind(); got != harness.Kind("mcode") {
		t.Fatalf("Kind() = %q", got)
	}
}

func TestResolveErrorEmitsErrorAndDone(t *testing.T) {
	wantErr := errors.New("model not found")
	h := New(Options{ResolveClient: func(context.Context, string, string) (llm.Client, string, error) { return nil, "", wantErr }})
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
	h := New(Options{DefaultModel: "default-model", ResolveClient: func(_ context.Context, model, _ string) (llm.Client, string, error) {
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
	h := New(Options{ResolveClient: func(_ context.Context, model, key string) (llm.Client, string, error) {
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

func TestResolveClientReceivesRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{})
	done := make(chan error, 1)
	h := New(Options{ResolveClient: func(ctx context.Context, _, _ string) (llm.Client, string, error) {
		close(called)
		<-ctx.Done()
		return nil, "", ctx.Err()
	}})
	go func() { done <- h.Run(ctx, harness.Turn{}, nil) }()
	<-called
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
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

func TestResolvedModelMetadataForEmptyModel(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	var contextModel, maxModel, costModel string
	h := New(Options{
		ResolveClient: func(context.Context, string, string) (llm.Client, string, error) {
			return client, "resolved-model", nil
		},
		ContextWindow: func(model string) int { contextModel = model; return 321000 },
		MaxTokens:     func(model string) int { maxModel = model; return 4096 },
		CostUSD:       func(model string, _ harness.Usage) (float64, bool) { costModel = model; return 0.25, true },
	})
	var usage *harness.Usage
	if err := h.Run(context.Background(), harness.Turn{}, func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	}); err != nil {
		t.Fatal(err)
	}
	if contextModel != "resolved-model" || maxModel != "resolved-model" || costModel != "resolved-model" || usage == nil || usage.ContextWindow != 321000 || usage.CostUSD != 0.25 {
		t.Fatalf("context=%q max=%q cost=%q usage=%+v", contextModel, maxModel, costModel, usage)
	}
}

func TestQualifiedModelMetadata(t *testing.T) {
	for _, model := range []string{"", "default", "anthropic/claude"} {
		client := &fakeClient{responses: []*llm.Response{answer()}}
		var got string
		h := New(Options{
			QualifyModel:  func(context.Context, string) string { return "magpie/model" },
			ResolveClient: func(context.Context, string, string) (llm.Client, string, error) { return client, "model", nil },
			ContextWindow: func(model string) int { got = model; return 200000 },
		})
		if err := h.Run(context.Background(), harness.Turn{Model: model}, nil); err != nil {
			t.Fatal(err)
		}
		want := "magpie/model"
		if model != "" && model != "default" {
			want = model
		}
		if got != want {
			t.Fatalf("模型 %q：得到 %q，期望 %q", model, got, want)
		}
	}
}

func TestCostUSDReachesUsage(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer()}}
	var gotModel string
	h := harnessFor(client, Options{CostUSD: func(model string, _ harness.Usage) (float64, bool) {
		gotModel = model
		return 0.25, true
	}})
	var usage *harness.Usage
	err := h.Run(context.Background(), harness.Turn{Model: "magpie-jp/claude-sonnet-5-5"}, func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "magpie-jp/claude-sonnet-5-5" || usage == nil || usage.CostUSD != 0.25 {
		t.Fatalf("model=%q usage=%+v", gotModel, usage)
	}
}

func TestCostUSDLeftEmptyWithoutPrice(t *testing.T) {
	for name, cost := range map[string]func(string, harness.Usage) (float64, bool){
		"nil":     nil,
		"decline": func(string, harness.Usage) (float64, bool) { return 0, false },
	} {
		client := &fakeClient{responses: []*llm.Response{answer()}}
		h := harnessFor(client, Options{CostUSD: cost})
		var usage *harness.Usage
		err := h.Run(context.Background(), harness.Turn{Model: "magpie-jp/plain-model"}, func(event harness.Event) {
			if event.Type == harness.EventUsage {
				usage = event.Usage
			}
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if usage == nil || usage.CostUSD != 0 {
			t.Fatalf("%s: usage = %+v", name, usage)
		}
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

// TestSteerLandsAfterFinalDrain 验证插话正好落在最后一次 drain 之后时不会被悄悄丢掉：
// 要么 Steer 明确返回 ErrNotRunning，要么这段文本进入续跑的模型请求。
// EventDone 在最后一次 drain 之后发出，正好卡在收尾窗口里。
func TestSteerLandsAfterFinalDrain(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{answer(), answer()}}
	h := harnessFor(client, Options{})
	var (
		steered  bool
		steerErr error
	)
	err := h.Run(context.Background(), harness.Turn{SessionID: "late", Prompt: "start"}, func(event harness.Event) {
		if event.Type != harness.EventDone || steered {
			return
		}
		steered = true
		steerErr = h.Steer(context.Background(), "late", "later")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !steered {
		t.Fatal("回调没有触发插话")
	}
	if steerErr != nil {
		if !errors.Is(steerErr, harness.ErrNotRunning) {
			t.Fatalf("Steer() error = %v", steerErr)
		}
		return
	}
	for _, request := range client.requests[1:] {
		for _, message := range request.Messages {
			for _, part := range message.Content {
				if part.Text == "later" {
					return
				}
			}
		}
	}
	t.Fatalf("Steer() 返回 nil，但插话没有进入模型请求：%+v", client.requests)
}

func TestSteerContinuationAccumulatesUsage(t *testing.T) {
	first, second := answer(), answer()
	first.Usage = llm.Usage{Input: 10, Output: 3, CacheRead: 2}
	second.Usage = llm.Usage{Input: 20, Output: 5, CacheRead: 4}
	client := &fakeClient{responses: []*llm.Response{first, second}}
	h := harnessFor(client, Options{CostUSD: func(_ string, usage harness.Usage) (float64, bool) {
		return float64(usage.InputTokens), true
	}})
	var usage *harness.Usage
	steered := false
	err := h.Run(context.Background(), harness.Turn{SessionID: "usage", Prompt: "start"}, func(event harness.Event) {
		if event.Type == harness.EventUsage {
			usage = event.Usage
		}
		if event.Type == harness.EventDone && !steered {
			steered = true
			if err := h.Steer(context.Background(), "usage", "later"); err != nil {
				t.Errorf("Steer() error = %v", err)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	// InputTokens 含缓存读取，两次分别为 12 和 24。
	if len(client.requests) != 2 || usage == nil || usage.InputTokens != 36 || usage.OutputTokens != 8 || usage.CachedTokens != 6 || usage.CostUSD != 36 {
		t.Fatalf("requests=%d usage=%+v", len(client.requests), usage)
	}
}

func TestSteerPreservedAfterRunError(t *testing.T) {
	client := &fakeClient{}
	h := harnessFor(client, Options{})
	turn := harness.Turn{SessionID: "retry", Prompt: "start"}
	steered := false
	err := h.Run(context.Background(), turn, func(event harness.Event) {
		if event.Type == harness.EventError && !steered {
			steered = true
			if err := h.Steer(context.Background(), "retry", "please remember this"); err != nil {
				t.Errorf("Steer() error = %v", err)
			}
		}
	})
	if err == nil || !steered {
		t.Fatalf("Run() error = %v, steered = %v", err, steered)
	}
	client.responses = []*llm.Response{answer()}
	turn.Prompt = "next"
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}
	for _, message := range client.requests[1].Messages {
		for _, part := range message.Content {
			if message.Role == "user" && part.Text == "please remember this\n\nnext" {
				return
			}
		}
	}
	t.Fatalf("插话未送达：%+v", client.requests[1].Messages)
}

// panicOnceClient 第一次请求时 panic，之后正常回答，用于验证兜底后 Harness 仍可复用。
type panicOnceClient struct {
	mu    sync.Mutex
	calls int
}

func (c *panicOnceClient) Stream(context.Context, llm.Request, func(llm.Delta)) (*llm.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		panic("stream exploded")
	}
	return answer(), nil
}

func TestRunRecoversStreamPanic(t *testing.T) {
	h := harnessFor(&panicOnceClient{}, Options{})
	err := h.Run(context.Background(), harness.Turn{SessionID: "panicked", Prompt: "hello"}, nil)
	if err == nil || !strings.Contains(err.Error(), "内置引擎内部错误") || !strings.Contains(err.Error(), "stream exploded") {
		t.Fatalf("Run() error = %v", err)
	}
	if steerErr := h.Steer(context.Background(), "panicked", "text"); !errors.Is(steerErr, harness.ErrNotRunning) {
		t.Fatalf("panic 之后 Steer() error = %v", steerErr)
	}
	if err := h.Run(context.Background(), harness.Turn{SessionID: "after-panic", Prompt: "again"}, nil); err != nil {
		t.Fatalf("panic 之后重新 Run() error = %v", err)
	}
}

func TestRunRecoversToolPanic(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{toolCall("explode", "call-1", `{}`), answer(), answer()}}
	h := harnessFor(client, Options{ExtraTools: func(context.Context, harness.Turn) []tools.Tool {
		return []tools.Tool{{Name: "explode", Description: "panic on purpose", Run: func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
			panic("tool exploded")
		}}}
	}})
	var toolEnd *harness.Event
	err := h.Run(context.Background(), harness.Turn{SessionID: "tool-panicked", Prompt: "go", Mode: harness.ModeFullAccess}, func(event harness.Event) {
		if event.Type == harness.EventToolEnd && event.ToolName == "explode" {
			e := event
			toolEnd = &e
		}
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if toolEnd == nil || !toolEnd.IsError || !strings.Contains(toolEnd.Output, "tool exploded") {
		t.Fatalf("tool_end = %+v", toolEnd)
	}
	if len(client.requests) < 2 {
		t.Fatalf("requests = %d, want >= 2", len(client.requests))
	}
	if steerErr := h.Steer(context.Background(), "tool-panicked", "text"); !errors.Is(steerErr, harness.ErrNotRunning) {
		t.Fatalf("panic 之后 Steer() error = %v", steerErr)
	}
	if err := h.Run(context.Background(), harness.Turn{Prompt: "again", Mode: harness.ModeFullAccess}, nil); err != nil {
		t.Fatalf("panic 之后重新 Run() error = %v", err)
	}
}
