package mcodeui

import (
	"context"
	"testing"
)

// TestFakeClientMoveQueuedNotifiesWatchers 验证 FakeClient 的重排会真正调整队列并通知 watcher。
func TestFakeClientMoveQueuedNotifiesWatchers(t *testing.T) {
	f := NewFakeClient()
	ctx := context.Background()
	for _, text := range []string{"one", "two", "three"} {
		if err := f.Enqueue(ctx, "t1", text, nil); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	var notified []QueuedFollowUp
	stop := f.WatchQueue(func(threadID string, items []QueuedFollowUp) {
		notified = append([]QueuedFollowUp(nil), items...)
	})
	defer stop()

	items, err := f.Queue(ctx, "t1")
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("queue = %d items, want 3", len(items))
	}

	// 把最后一项移到最前。
	if err := f.MoveQueued(ctx, "t1", items[2].ID, items[0].ID); err != nil {
		t.Fatalf("MoveQueued: %v", err)
	}
	moved, err := f.Queue(ctx, "t1")
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	want := []string{items[2].ID, items[0].ID, items[1].ID}
	for i := range want {
		if moved[i].ID != want[i] {
			t.Fatalf("queue order = %v, want %v", queueIDs(moved), want)
		}
	}
	if len(notified) != 3 || notified[0].ID != items[2].ID {
		t.Fatalf("watcher notified with %v", queueIDs(notified))
	}

	// 空 beforeFollowUpID 表示移到队尾。
	if err := f.MoveQueued(ctx, "t1", items[0].ID, ""); err != nil {
		t.Fatalf("MoveQueued to tail: %v", err)
	}
	tail, err := f.Queue(ctx, "t1")
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if tail[len(tail)-1].ID != items[0].ID {
		t.Fatalf("queue order = %v, want %s at tail", queueIDs(tail), items[0].ID)
	}
	if len(f.moveQueuedCalls) != 2 {
		t.Fatalf("moveQueuedCalls = %d, want 2", len(f.moveQueuedCalls))
	}
	if f.moveQueuedCalls[0].BeforeFollowUpID != items[0].ID {
		t.Fatalf("recorded beforeFollowUpID = %q", f.moveQueuedCalls[0].BeforeFollowUpID)
	}
}

// queueIDs 提取排队项 ID，便于失败信息阅读。
func queueIDs(items []QueuedFollowUp) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
