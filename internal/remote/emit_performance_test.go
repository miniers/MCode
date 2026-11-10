package remote

import (
	"context"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func TestEmitTextDeltaTiming(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	const threadID = "00000000-0000-0000-0000-000000000063"
	backend.AddThread(wire.ThreadSummary{ID: threadID}, nil)
	ready := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRun()
	backend.SetHarness(&mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "first"})
		emit(harness.Event{Type: harness.EventReasoning, Text: "think"})
		emit(harness.Event{Type: harness.EventText, Text: " second"})
		emit(harness.Event{Type: harness.EventReasoning, Text: " more"})
		close(ready)
		<-release
		return nil
	}}, t.TempDir(), harness.ModeAgent)
	_, err := backend.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: threadID, Text: "start", ActionID: "00000000-0000-0000-0000-000000000064"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for streamed events")
	}
	backend.mu.Lock()
	messages := append([]wire.ThreadMessage(nil), backend.threads[threadID].messages...)
	backend.mu.Unlock()
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(messages))
	}
	for i, want := range []string{"first second", "think more"} {
		msg := messages[i+1]
		if msg.Text != want || msg.TurnStatus == nil || *msg.TurnStatus != "running" || msg.TurnStartedAt == nil || msg.TurnCompletedAt != nil {
			t.Errorf("message %d = %+v, want text %q and running timing", i+1, msg, want)
		}
	}
	releaseRun()
	waitThreadIdle(t, backend, context.Background(), threadID)
	backend.mu.Lock()
	messages = append([]wire.ThreadMessage(nil), backend.threads[threadID].messages...)
	backend.mu.Unlock()
	for _, msg := range messages[1:] {
		if msg.TurnStatus == nil || *msg.TurnStatus != "completed" || msg.TurnStartedAt == nil || msg.TurnCompletedAt == nil {
			t.Errorf("message has incorrect completed timing: %+v", msg)
		}
	}
}

func TestSessionUsageToolCountCache(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	const threadID = "usage-cache"
	backend.AddThread(wire.ThreadSummary{ID: threadID}, []wire.ThreadMessage{{Role: "user"}})
	check := func(want int) {
		t.Helper()
		_, got, err := backend.SessionUsage(context.Background(), threadID)
		if err != nil || got != want {
			t.Fatalf("SessionUsage tool calls = %d, err = %v; want %d", got, err, want)
		}
	}
	check(0)
	check(0)
	tool := "tool"
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.messages = append(td.messages, wire.ThreadMessage{Role: "tool"}, wire.ThreadMessage{Role: "assistant", Kind: &tool})
	backend.mu.Unlock()
	check(2)
	check(2)
}

func BenchmarkEmitTextDelta(b *testing.B) {
	b.StopTimer()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	const threadID = "00000000-0000-0000-0000-000000000061"
	history := make([]wire.ThreadMessage, 5000)
	for i := range history {
		history[i] = wire.ThreadMessage{Role: "user", Text: "history"}
	}
	backend.AddThread(wire.ThreadSummary{ID: threadID}, history)
	done := make(chan struct{})
	backend.SetHarness(&mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "x"})
		b.ResetTimer()
		b.StartTimer()
		for i := 0; i < b.N; i++ {
			emit(harness.Event{Type: harness.EventText, Text: "x"})
			backend.mu.Lock()
			msgs := backend.threads[threadID].messages
			msgs[len(msgs)-1].Text = "x"
			backend.mu.Unlock()
		}
		b.StopTimer()
		close(done)
		return nil
	}}, b.TempDir(), harness.ModeAgent)
	_, err := backend.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: threadID, Text: "start", ActionID: "00000000-0000-0000-0000-000000000062"})
	if err != nil {
		b.Fatal(err)
	}
	<-done
}
