package app

import (
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// TestWatchAppliesAuthoritativeSnapshot 验证打开会话时 Watch 会立刻把后端订阅快照
// 交给界面：会话列表里的运行标记只是某次加载时的快照，若一轮的结束事件在未订阅时
// 发布，界面会一直显示进行中，打开会话也不会自愈。
func TestWatchAppliesAuthoritativeSnapshot(t *testing.T) {
	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	backend.AddThread(wire.ThreadSummary{ID: "t1", ProjectID: "p1", Title: "会话", IsRunning: true}, nil)

	// 后端真实状态：没有存活的轮次，但摘要上的运行标记是残留值。
	updates := make(chan mcodeui.ThreadUpdate, 1)
	client := &Client{backend: backend}
	stop := client.Watch("t1", func(update mcodeui.ThreadUpdate) {
		select {
		case updates <- update:
		default:
		}
	}, nil)
	defer stop()

	select {
	case update := <-updates:
		if update.ThreadID != "t1" {
			t.Fatalf("快照会话 ID = %q，期望 t1", update.ThreadID)
		}
		if update.Thread.IsRunning {
			t.Fatal("快照未纠正残留的运行中标记")
		}
		if update.Thread.Title != "会话" {
			t.Fatalf("快照标题 = %q，期望 会话", update.Thread.Title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("订阅后没有收到权威快照")
	}
}
