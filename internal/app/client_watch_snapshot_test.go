package app

import (
	"reflect"
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

func localTestEvent(runID string, seq int, title string) wire.ThreadEvent {
	return wire.ThreadEvent{
		ThreadID: "t1", RunID: runID, Sequence: seq,
		Payload: wire.ThreadEventPayload{Thread: wire.ThreadSummary{ID: "t1", Title: title}},
	}
}

func TestLocalConn_DropsOldSequenceAndAcceptsNewRun(t *testing.T) {
	var titles []string
	conn := &localConn{threadID: "t1", onUpdate: func(u mcodeui.ThreadUpdate) { titles = append(titles, u.Thread.Title) }}
	conn.applySnapshot(nil)
	for _, ev := range []wire.ThreadEvent{
		localTestEvent("run-1", 3, "终稿"),
		localTestEvent("run-1", 2, "旧稿"),
		localTestEvent("run-1", 3, "重复帧"),
		localTestEvent("run-2", 1, "新轮次"),
	} {
		_ = conn.Notify("threads.event", ev)
	}
	if !reflect.DeepEqual(titles, []string{"终稿", "新轮次"}) {
		t.Fatalf("帧过滤结果错误：%v", titles)
	}
}

func TestLocalConn_ReplaysBufferedEventsAfterSnapshot(t *testing.T) {
	var titles []string
	conn := &localConn{threadID: "t1", onUpdate: func(u mcodeui.ThreadUpdate) { titles = append(titles, u.Thread.Title) }}
	for _, ev := range []wire.ThreadEvent{
		localTestEvent("run-1", 5, "第五帧"),
		localTestEvent("run-2", 1, "新轮次"),
		localTestEvent("run-1", 2, "快照已包含"),
		localTestEvent("run-1", 4, "第四帧"),
		localTestEvent("run-1", 1, "快照之前"),
		localTestEvent("run-1", 4, "重复帧"),
	} {
		_ = conn.Notify("threads.event", ev)
	}
	if len(titles) != 0 {
		t.Fatal("快照之前不应调用更新回调")
	}
	conn.applySnapshot(&wire.ThreadStreamSnapshot{
		ThreadID: "t1", RunID: "run-1", Sequence: 2,
		Thread: wire.ThreadSummary{ID: "t1", Title: "快照"},
	})
	_ = conn.Notify("threads.event", localTestEvent("run-2", 2, "直接应用"))
	if !reflect.DeepEqual(titles, []string{"快照", "第四帧", "第五帧", "新轮次", "直接应用"}) {
		t.Fatalf("快照与缓冲帧顺序错误：%v", titles)
	}
	if !conn.snapshotApplied || conn.pendingEvents != nil {
		t.Fatal("回放后未完成快照初始化或未清理缓冲")
	}
}

func TestLocalConn_SerializesUpdatesAndAllowsCloseInCallback(t *testing.T) {
	entered, release, firstDone, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var titles []string
	var conn *localConn
	conn = &localConn{threadID: "t1", onUpdate: func(u mcodeui.ThreadUpdate) {
		if u.Thread.Title == "第三帧" {
			close(entered)
			<-release
		}
		titles = append(titles, u.Thread.Title)
		if u.Thread.Title == "第四帧" {
			_ = conn.Close()
		}
	}}
	conn.applySnapshot(nil)
	go func() {
		_ = conn.Notify("threads.event", localTestEvent("run", 3, "第三帧"))
		close(firstDone)
	}()
	<-entered
	go func() {
		_ = conn.Notify("threads.event", localTestEvent("run", 4, "第四帧"))
		close(secondDone)
	}()
	select {
	case <-secondDone:
		close(release)
		<-firstDone
		t.Fatal("前一回调尚未完成，后一帧已应用")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-firstDone
	<-secondDone
	_ = conn.Notify("threads.event", localTestEvent("run", 5, "关闭后"))
	if !reflect.DeepEqual(titles, []string{"第三帧", "第四帧"}) {
		t.Fatalf("回调未串行应用或关闭后仍收到事件：%v", titles)
	}
}

func TestLocalConn_ApprovalsDoNotWaitForSnapshot(t *testing.T) {
	calls := 0
	conn := &localConn{threadID: "t1", onApprovals: func([]mcodeui.Approval) { calls++ }}
	_ = conn.Notify("requests.changed", wire.RequestsChangedParams{ThreadID: "other"})
	_ = conn.Notify("requests.changed", wire.RequestsChangedParams{ThreadID: "t1"})
	_ = conn.Close()
	_ = conn.Notify("requests.changed", wire.RequestsChangedParams{ThreadID: "t1"})
	if calls != 1 {
		t.Fatalf("审批回调次数 = %d，期望 1", calls)
	}
}
