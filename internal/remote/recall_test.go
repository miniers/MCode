package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestRecallTurn_PersistsRemovalAndRebuildsContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "project"})
	b.AddThread(wire.ThreadSummary{ID: "t", ProjectID: "p", Provider: "mock"}, []wire.ThreadMessage{
		{ID: "prior", Role: "user", Text: "earlier conversation", TurnIndex: 0},
	})
	started := make(chan struct{})
	prompts := make(chan harness.Turn, 2)
	b.RegisterHarness(&mockHarness{kind: "mock", runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		prompts <- turn
		if strings.Contains(turn.Prompt, "withdraw this") {
			emit(harness.Event{Type: harness.EventSession, SessionID: "session-with-withdrawn-prompt"})
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		emit(harness.Event{Type: harness.EventText, Text: "done"})
		return nil
	}})
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "t", Text: "withdraw this"}); err != nil {
		t.Fatal(err)
	}
	<-started
	prompt, err := b.RecallDraft(ctx, "t")
	if err != nil || prompt != "withdraw this" {
		t.Fatalf("take back: prompt=%q err=%v", prompt, err)
	}
	history, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "t"})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != "prior" {
		t.Fatalf("history after take back: %+v, %v", history, err)
	}
	// 从同一数据库恢复，确认撤回不只作用于内存。
	reloadedHub := NewStreamHub(StreamHubOptions{})
	defer reloadedHub.Close()
	reloaded := NewMemoryBackend(reloadedHub)
	defer reloaded.Close()
	if err := reloaded.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	history, err = reloaded.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "t"})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("reloaded history: %+v, %v", history, err)
	}
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "t", Text: "replacement"}); err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, b, ctx, "t")
	<-prompts
	next := <-prompts
	if next.SessionID != "" || !strings.Contains(next.Prompt, "earlier conversation") || strings.Contains(next.Prompt, "withdraw this") {
		t.Fatalf("invalid context after take back: %+v", next)
	}
}

func TestRecallTurn_RejectsOutputAndCancellationRace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, lateOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "already_output", true: "output_during_cancel"}[lateOutput], func(t *testing.T) {
			ctx := context.Background()
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			b := NewMemoryBackend(hub)
			defer b.Close()
			ready := make(chan struct{})
			b.RegisterHarness(&mockHarness{kind: "mock", runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
				if !lateOutput {
					emit(harness.Event{Type: harness.EventText, Text: "already replied"})
				}
				close(ready)
				<-ctx.Done()
				if lateOutput {
					emit(harness.Event{Type: harness.EventText, Text: "last buffered output"})
				}
				return ctx.Err()
			}})
			b.AddThread(wire.ThreadSummary{ID: "t", Provider: "mock"}, nil)
			if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "t", Text: "keep me"}); err != nil {
				t.Fatal(err)
			}
			<-ready
			if _, err := b.RecallDraft(ctx, "t"); err == nil {
				t.Fatal("must not retract a turn with output")
			}
			history, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "t"})
			if err != nil || len(history.Messages) != 2 || history.Messages[0].Text != "keep me" {
				t.Fatalf("turn lost on rejected take back: %+v, %v", history, err)
			}
		})
	}
}
