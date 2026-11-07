package app

import (
	"context"
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestClientQueueIgnoresOutOfOrderNotifications(t *testing.T) {
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()
	backend.AddThread(wire.ThreadSummary{ID: "t1", IsRunning: true}, nil)
	client := &Client{backend: backend}
	firstSnapshot := make(chan struct{})
	release := make(chan struct{})
	backend.SetQueueChangedHook(func(snap wire.ThreadsQueueSnapshot) {
		if len(snap.FollowUps) == 1 && snap.FollowUps[0].Text == "第一条" {
			close(firstSnapshot)
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
		}
	})
	updates := make(chan []mcodeui.QueuedFollowUp, 4)
	stop := client.WatchQueue(func(threadID string, items []mcodeui.QueuedFollowUp) {
		updates <- items
	})
	defer stop()
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- client.Enqueue(ctx, "t1", "第一条", nil) }()
	select {
	case <-firstSnapshot:
	case <-time.After(2 * time.Second):
		t.Fatal("等待第一条队列通知超时")
	}
	if err := client.Enqueue(ctx, "t1", "第二条", nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待第一次入队完成超时")
	}
	select {
	case items := <-updates:
		if len(items) != 2 || items[1].Text != "第二条" {
			t.Fatalf("未收到最新快照: %+v", items)
		}
	default:
		t.Fatal("未收到队列通知")
	}
	select {
	case items := <-updates:
		t.Fatalf("旧快照不应再次覆盖队列: %+v", items)
	default:
	}
	items, err := client.Queue(ctx, "t1")
	if err != nil || len(items) != 2 {
		t.Fatalf("Queue = %+v, %v", items, err)
	}
	if err := client.RemoveQueued(ctx, "t1", items[0].ID); err != nil {
		t.Fatal(err)
	}
	items, err = client.Queue(ctx, "t1")
	if err != nil || len(items) != 1 || items[0].Text != "第二条" {
		t.Fatalf("删除后的 Queue = %+v, %v", items, err)
	}
}

// wire 协议层的 Intent 必须原样搬到 UI 层，insert 表示插入降级，空串表示普通排队。
func TestFollowUpsWireToUIPreservesIntent(t *testing.T) {
	items := FollowUpsWireToUI([]wire.FollowUp{
		{ID: "f_insert", Text: "插入降级", Intent: "insert"},
		{ID: "f_queue", Text: "普通排队"},
	})
	if len(items) != 2 {
		t.Fatalf("期望 2 条 QueuedFollowUp，实际 %d 条", len(items))
	}
	if items[0].Intent != "insert" {
		t.Fatalf("降级项 Intent 应为 insert，实际 %q", items[0].Intent)
	}
	if items[1].Intent != "" {
		t.Fatalf("普通排队项 Intent 应为空串，实际 %q", items[1].Intent)
	}
}
