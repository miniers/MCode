package remote

import (
	"context"
	"strings"
	"sync"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// handoffMarker 是交接段开头的固定标记。
const handoffMarker = "[mcode: history from before the agent switch]"

// sessionCheckerHarness 是能判断会话上下文是否还在的假 harness。
type sessionCheckerHarness struct {
	mu     sync.Mutex
	exists bool
	turns  []harness.Turn
}

func (h *sessionCheckerHarness) Kind() harness.Kind { return "session-checker" }

func (h *sessionCheckerHarness) Run(_ context.Context, turn harness.Turn, emit func(harness.Event)) error {
	h.mu.Lock()
	h.turns = append(h.turns, turn)
	h.mu.Unlock()
	if turn.SessionID == "" {
		emit(harness.Event{Type: harness.EventSession, SessionID: "session-1"})
	}
	emit(harness.Event{Type: harness.EventText, Text: "回复"})
	return nil
}

func (h *sessionCheckerHarness) SessionExists(string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exists
}

func (h *sessionCheckerHarness) setExists(exists bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.exists = exists
}

func (h *sessionCheckerHarness) prompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	prompts := make([]string, 0, len(h.turns))
	for _, turn := range h.turns {
		prompts = append(prompts, turn.Prompt)
	}
	return prompts
}

// sendTurn 发一条消息并等待线程空闲。
func sendTurn(t *testing.T, b *MemoryBackend, ctx context.Context, threadID, text string) {
	t.Helper()
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: text}); err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, b, ctx, threadID)
}

// TestSessionHandoff_OnMissingSession 会话缺失时把可见历史交接进下一轮提示词。
func TestSessionHandoff_OnMissingSession(t *testing.T) {
	ctx := context.Background()
	h := &sessionCheckerHarness{exists: true}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)
	sendTurn(t, b, ctx, id, "第一轮问题")
	h.setExists(false)
	sendTurn(t, b, ctx, id, "第二轮问题")

	prompts := h.prompts()
	if len(prompts) != 2 {
		t.Fatalf("got %d turns, want 2", len(prompts))
	}
	if strings.Contains(prompts[0], handoffMarker) {
		t.Fatalf("first prompt unexpectedly contains handoff: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], handoffMarker) {
		t.Fatalf("second prompt missing handoff: %q", prompts[1])
	}
	if !strings.Contains(prompts[1], "第一轮问题") {
		t.Fatalf("handoff missing prior history: %q", prompts[1])
	}
	if !strings.HasSuffix(prompts[1], "第二轮问题") {
		t.Fatalf("handoff should precede current prompt: %q", prompts[1])
	}

	// 会话被重建后不再交接。
	h.setExists(true)
	sendTurn(t, b, ctx, id, "第三轮问题")
	if prompt := h.prompts()[2]; strings.Contains(prompt, handoffMarker) {
		t.Fatalf("third prompt unexpectedly contains handoff: %q", prompt)
	}
}

// TestSessionHandoff_SkippedWhenSessionAlive 会话仍在时不注入交接段。
func TestSessionHandoff_SkippedWhenSessionAlive(t *testing.T) {
	ctx := context.Background()
	h := &sessionCheckerHarness{exists: true}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)
	sendTurn(t, b, ctx, id, "第一轮问题")
	sendTurn(t, b, ctx, id, "第二轮问题")

	prompts := h.prompts()
	if len(prompts) != 2 {
		t.Fatalf("got %d turns, want 2", len(prompts))
	}
	if prompt := prompts[1]; strings.Contains(prompt, handoffMarker) || prompt != "第二轮问题" {
		t.Fatalf("second prompt = %q, want plain prompt", prompt)
	}
}

// TestSessionHandoff_IgnoredWithoutChecker 不实现 SessionChecker 的 harness 行为不变。
func TestSessionHandoff_IgnoredWithoutChecker(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var prompts []string
	h := &customRunHarness{runFn: func(_ context.Context, turn harness.Turn, emit func(harness.Event)) error {
		mu.Lock()
		prompts = append(prompts, turn.Prompt)
		mu.Unlock()
		if turn.SessionID == "" {
			emit(harness.Event{Type: harness.EventSession, SessionID: "session-1"})
		}
		emit(harness.Event{Type: harness.EventText, Text: "回复"})
		return nil
	}}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)
	sendTurn(t, b, ctx, id, "第一轮问题")
	sendTurn(t, b, ctx, id, "第二轮问题")

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("got %d turns, want 2", len(prompts))
	}
	if prompt := prompts[1]; strings.Contains(prompt, handoffMarker) || prompt != "第二轮问题" {
		t.Fatalf("second prompt = %q, want plain prompt", prompt)
	}
}
