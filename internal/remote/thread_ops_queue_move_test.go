package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// seedQueueMoveThread 建一个会话并把排队项直接写进内存，便于校验重排结果。
func seedQueueMoveThread(t *testing.T, b *MemoryBackend, threadID string, items []wire.FollowUp) {
	t.Helper()
	b.AddThread(wire.ThreadSummary{ID: threadID, Provider: "claude"}, nil)
	b.mu.Lock()
	td := b.threads[threadID]
	td.followUps = items
	td.queueRevision = 3
	b.mu.Unlock()
}

// queueMoveIDs 返回当前排队项的 ID 顺序。
func queueMoveIDs(b *MemoryBackend, threadID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	td := b.threads[threadID]
	ids := make([]string, 0, len(td.followUps))
	for _, fu := range td.followUps {
		ids = append(ids, fu.ID)
	}
	return ids
}

func queueMoveBundleID(id string) *string { return &id }

func assertQueueMoveOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("queue order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("queue order = %v, want %v", got, want)
		}
	}
}

func TestQueueMove_MovesBundleBeforeTarget(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	seedQueueMoveThread(t, b, "t1", []wire.FollowUp{
		{ID: "a", Text: "a", BundleID: queueMoveBundleID("bx")},
		{ID: "b", Text: "b", BundleID: queueMoveBundleID("bx")},
		{ID: "c", Text: "c"},
		{ID: "d", Text: "d"},
	})

	res, err := b.QueueMove(context.Background(), wire.ThreadsQueueMoveParams{
		ThreadID:         "t1",
		FollowUpID:       "b",
		BeforeFollowUpID: "d",
	})
	if err != nil {
		t.Fatalf("QueueMove: %v", err)
	}
	if res.Revision != 4 {
		t.Fatalf("revision = %d, want 4", res.Revision)
	}
	// bundle 整组移动：a、b 一起落到 d 之前。
	assertQueueMoveOrder(t, queueMoveIDs(b, "t1"), "c", "a", "b", "d")
	if len(res.FollowUps) != 4 || res.FollowUps[1].ID != "a" || res.FollowUps[2].ID != "b" {
		t.Fatalf("result followUps = %v", res.FollowUps)
	}

	// 空 beforeFollowUpID 表示移到队尾。
	if _, err := b.QueueMove(context.Background(), wire.ThreadsQueueMoveParams{
		ThreadID:   "t1",
		FollowUpID: "a",
	}); err != nil {
		t.Fatalf("QueueMove to tail: %v", err)
	}
	assertQueueMoveOrder(t, queueMoveIDs(b, "t1"), "c", "d", "a", "b")
}

func TestQueueMove_SameTargetIsNoop(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	seedQueueMoveThread(t, b, "t1", []wire.FollowUp{
		{ID: "a", Text: "a"},
		{ID: "b", Text: "b", BundleID: queueMoveBundleID("bx")},
		{ID: "c", Text: "c", BundleID: queueMoveBundleID("bx")},
	})

	res, err := b.QueueMove(context.Background(), wire.ThreadsQueueMoveParams{
		ThreadID:         "t1",
		FollowUpID:       "b",
		BeforeFollowUpID: "b",
	})
	if err != nil {
		t.Fatalf("QueueMove: %v", err)
	}
	if res.Revision != 3 {
		t.Fatalf("revision = %d, want 3（无操作不得递增）", res.Revision)
	}
	assertQueueMoveOrder(t, queueMoveIDs(b, "t1"), "a", "b", "c")
}

func TestQueueMove_UnknownIDFails(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	seedQueueMoveThread(t, b, "t1", []wire.FollowUp{
		{ID: "a", Text: "a"},
		{ID: "b", Text: "b"},
	})

	if _, err := b.QueueMove(context.Background(), wire.ThreadsQueueMoveParams{
		ThreadID:   "t1",
		FollowUpID: "missing",
	}); err == nil {
		t.Fatal("unknown followUpID must fail")
	}
	if _, err := b.QueueMove(context.Background(), wire.ThreadsQueueMoveParams{
		ThreadID:         "t1",
		FollowUpID:       "a",
		BeforeFollowUpID: "missing",
	}); err == nil {
		t.Fatal("unknown beforeFollowUpID must fail")
	}
	assertQueueMoveOrder(t, queueMoveIDs(b, "t1"), "a", "b")
}
