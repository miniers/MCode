package acp

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
)

func TestLiveACPRun(t *testing.T) {
	agentVal := os.Getenv("MCODE_ACP_LIVE")
	if agentVal == "" {
		t.Skip("skipping live acp test; set MCODE_ACP_LIVE=<agent名> to run")
	}

	h := New(Options{Agent: Agent(agentVal)})
	if ok, reason := h.Available(); !ok {
		t.Skipf("skipping live acp test: %s", reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var mu sync.Mutex
	var events []harness.Event

	emit := func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}

	turn := harness.Turn{
		WorkDir:  t.TempDir(),
		Mode:     harness.ModeFullAccess,
		Prompt:   "Reply with exactly the word: pong",
		Approver: nil,
	}

	if err := h.Run(ctx, turn, emit); err != nil {
		t.Fatalf("live acp run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var sawSession bool
	var textBuilder strings.Builder
	for _, e := range events {
		if e.Type == harness.EventSession && e.SessionID != "" {
			sawSession = true
		}
		if e.Type == harness.EventText {
			textBuilder.WriteString(e.Text)
		}
	}

	if !sawSession {
		t.Error("expected to see EventSession with non-empty SessionID")
	}

	concatenatedText := textBuilder.String()
	if concatenatedText == "" {
		t.Error("expected non-empty concatenated EventText")
	}

	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	lastEvent := events[len(events)-1]
	if lastEvent.Type != harness.EventDone {
		t.Errorf("expected last event to be %v, got %v", harness.EventDone, lastEvent.Type)
	}

	t.Logf("concatenated text: %q, stop reason: %s", concatenatedText, lastEvent.StopReason)
}

// TestLiveACPRunResume 在真实 Agent 上验证第二轮以同一 SessionID 经 session/load 恢复会话。
// 与 TestLiveACPRun 相同，需要 MCODE_ACP_LIVE=<agent名> 才会运行。
func TestLiveACPRunResume(t *testing.T) {
	agentVal := os.Getenv("MCODE_ACP_LIVE")
	if agentVal == "" {
		t.Skip("skipping live acp resume test; set MCODE_ACP_LIVE=<agent名> to run")
	}

	h := New(Options{Agent: Agent(agentVal)})
	if ok, reason := h.Available(); !ok {
		t.Skipf("skipping live acp test: %s", reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	workDir := t.TempDir()

	var (
		mu       sync.Mutex
		sessions []string
	)
	emit := func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.Type == harness.EventSession && e.SessionID != "" {
			sessions = append(sessions, e.SessionID)
		}
	}

	turn1 := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeFullAccess,
		Prompt:  "Reply with exactly the word: pong",
	}
	if err := h.Run(ctx, turn1, emit); err != nil {
		t.Fatalf("first live turn failed: %v", err)
	}

	mu.Lock()
	var sid string
	if len(sessions) > 0 {
		sid = sessions[len(sessions)-1]
	}
	mu.Unlock()
	if sid == "" {
		t.Fatal("expected a session ID from the first turn")
	}

	turn2 := harness.Turn{
		WorkDir:   workDir,
		Mode:      harness.ModeFullAccess,
		SessionID: sid,
		Prompt:    "Reply with exactly the word: ping",
	}
	if err := h.Run(ctx, turn2, emit); err != nil {
		t.Fatalf("resume live turn failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sessions) == 0 || sessions[len(sessions)-1] != sid {
		t.Fatalf("expected the second turn to keep session %q, got %v", sid, sessions)
	}
}
