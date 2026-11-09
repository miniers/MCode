package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
)

func TestBeeSessionRelationBeforeHarnessRun(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	type observation struct {
		thread wire.ThreadSummary
		err    error
	}
	observed := make(chan observation, 1)
	h := &mockHarness{kind: "custom-test", runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		thread, err := b.GetThread(ctx, turn.ThreadID)
		observed <- observation{thread: thread, err: err}
		emit(harness.Event{Type: harness.EventText, Text: "完成"})
		return nil
	}}
	b.SetHarness(h, t.TempDir(), harness.ModeFullAccess)
	b.RegisterHarness(h)
	lead, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", LeadThreadID: lead.ThreadID, Provider: "custom-test", WorkDir: t.TempDir(), Prompt: "执行任务",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observed:
		if got.err != nil || got.thread.ID != threadID || derefString(got.thread.ParentThreadID) != lead.ThreadID || !got.thread.IsHelper || got.thread.Provider != "custom-test" {
			t.Fatalf("Run 时的会话关系不符合预期: %+v, %v", got.thread, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("假 harness 未执行")
	}
	thread := waitThreadIdle(t, b, ctx, threadID)
	state, err := b.SessionState(ctx, threadID)
	if err != nil || state.Failed || state.LastAssistantText != "完成" || thread.Provider != "custom-test" {
		t.Fatalf("自定义来源未完成首轮: %+v, %+v, %v", thread, state, err)
	}
	if calls := h.getTurnCalls(); len(calls) != 1 || calls[0].ThreadID != threadID {
		t.Fatalf("假 harness 调用不符合预期: %+v", calls)
	}
}
