package remote

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 快速模式写入会话摘要与 store，并作为 Turn.FastMode/SpeedTier 下发给 harness；重载后仍可恢复。
func TestSetThreadFastMode_AppliedToTurnAndPersist(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "fastmode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "reply"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	b.SetHarness(mock, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	threadID := res.ThreadID

	// 新建会话默认关闭快速模式
	th, err := b.GetThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if th.FastMode || th.SpeedTier != nil {
		t.Fatalf("新建会话默认应关闭快速模式: %+v", th)
	}

	sum, err := b.SetThreadFastMode(ctx, threadID, true, "fast")
	if err != nil {
		t.Fatalf("SetThreadFastMode failed: %v", err)
	}
	if !sum.FastMode || sum.SpeedTier == nil || *sum.SpeedTier != "fast" {
		t.Fatalf("summary 未更新快速模式: %+v", sum)
	}

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "turn 1", ActionID: "act-1"}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	calls := mock.getTurnCalls()
	if len(calls) != 1 || !calls[0].FastMode || calls[0].SpeedTier != "fast" {
		t.Fatalf("Turn 未带上快速模式: %+v", calls)
	}

	// 同一 store 重建后端应恢复快速模式与档位
	reloaded := NewMemoryBackend(hub)
	defer reloaded.Close()
	if err := reloaded.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.GetThread(ctx, threadID)
	if err != nil || !got.FastMode || got.SpeedTier == nil || *got.SpeedTier != "fast" {
		t.Fatalf("重载后快速模式未恢复: %+v, %v", got, err)
	}

	// 关闭快速模式时档位置空
	sum, err = b.SetThreadFastMode(ctx, threadID, false, "fast")
	if err != nil {
		t.Fatalf("SetThreadFastMode(false) failed: %v", err)
	}
	if sum.FastMode || sum.SpeedTier != nil {
		t.Fatalf("关闭快速模式后档位应置空: %+v", sum)
	}
	th, err = b.GetThread(ctx, threadID)
	if err != nil || th.FastMode || th.SpeedTier != nil {
		t.Fatalf("关闭快速模式后状态不符合预期: %+v, %v", th, err)
	}
}
