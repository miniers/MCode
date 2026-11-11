package remote

import (
	"context"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeadPlansOnlyTurns(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	turns := make(chan harness.Turn, 4)
	b.SetHarness(&mockHarness{runFunc: func(_ context.Context, turn harness.Turn, emit func(harness.Event)) error {
		turns <- turn
		emit(harness.Event{Type: harness.EventSession, SessionID: "session"})
		return nil
	}}, t.TempDir(), harness.ModeAgent)
	const id = "lead-plans-only"
	b.AddThread(wire.ThreadSummary{ID: id, ProjectID: "p", InteractionMode: "build", RuntimeMode: string(harness.ModeAgent)}, nil)
	var enabled atomic.Bool
	b.SetSwarmLeadPlansOnlyFunc(func(threadID string) bool { _, _ = b.GetThread(ctx, threadID); return enabled.Load() })
	check := func(want bool) {
		t.Helper()
		select {
		case turn := <-turns:
			if turn.LeadPlansOnly != want || turn.Mode != harness.ModeAgent || turn.InteractionMode != "build" {
				t.Fatalf("turn = %+v", turn)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("turn timeout")
		}
		waitThreadIdle(t, b, ctx, id)
	}
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "first"}); err != nil {
		t.Fatal(err)
	}
	check(false)
	enabled.Store(true)
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "second"}); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := b.SendSwarmReport(ctx, id, "report"); err != nil {
		t.Fatal(err)
	}
	check(true)
	enabled.Store(false)
	if err := b.SendHostEvent(ctx, id, "notice"); err != nil {
		t.Fatal(err)
	}
	check(false)
}
