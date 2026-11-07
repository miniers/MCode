package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// dequeueOrderProbe 是一次 commitQueueChange 回调里读到的库内状态。
// 回调发生在队列写库（删除出队行）之后，所以 messagePersisted 为真就说明
// 出队消息正文先于删队列行落了库；旧顺序下这里会是 false。
type dequeueOrderProbe struct {
	messagePersisted bool
	queueRowPresent  bool
	err              error
}

// probeDequeueCommit 安装队列变动回调，只在出队提交（快照里已经不含 dequeuedText）时采样。
// 采样结果通过容量为 1 的通道返回，回调内用非阻塞发送，不会拖住后端。
func probeDequeueCommit(t *testing.T, b *MemoryBackend, s *store.Store, threadID, dequeuedText string) <-chan dequeueOrderProbe {
	t.Helper()
	ch := make(chan dequeueOrderProbe, 1)
	b.SetQueueChangedHook(func(snap wire.ThreadsQueueSnapshot) {
		if snap.ThreadID != threadID {
			return
		}
		for _, fu := range snap.FollowUps {
			if fu.Text == dequeuedText {
				// 入队等尚未出队的提交，不采样。
				return
			}
		}
		probe := dequeueOrderProbe{}
		msgs, _, err := s.ListMessages(context.Background(), threadID, 0, 200)
		if err != nil {
			probe.err = err
			select {
			case ch <- probe:
			default:
			}
			return
		}
		for _, m := range msgs {
			if m.Role == "user" && m.Text == dequeuedText {
				probe.messagePersisted = true
				break
			}
		}
		rows, err := s.ListFollowUps(context.Background(), threadID)
		if err != nil {
			probe.err = err
		} else {
			for _, row := range rows {
				if row.Text == dequeuedText {
					probe.queueRowPresent = true
					break
				}
			}
		}
		select {
		case ch <- probe:
		default:
		}
	})
	return ch
}

// newDequeueOrderBackend 建一个带真实 store 的后端，线程已落库，可以走出队路径。
func newDequeueOrderBackend(t *testing.T, h *controlledTestHarness) (*MemoryBackend, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "dequeue_order.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)

	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetDataDir(dir)
	b.RegisterHarness(h)
	if err := b.AttachStore(context.Background(), s); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p-dequeue-order", Name: "dequeue-order", Path: dir})
	th, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: "p-dequeue-order",
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return b, s, th.ThreadID
}

// TestDequeueOrder_AutoDequeue 覆盖轮次结束自动出队的路径：出队提交时，
// 排队消息正文必须已经写进 store，否则两步之间崩溃会丢消息。
func TestDequeueOrder_AutoDequeue(t *testing.T) {
	ctx := context.Background()
	startedCh := make(chan struct{}, 2)
	releaseCh := make(chan struct{}, 2)
	b, s, threadID := newDequeueOrderBackend(t, &controlledTestHarness{
		startedCh: startedCh,
		releaseCh: releaseCh,
	})

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "turn 1",
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("首轮 SendMessage 失败: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("首轮未在预期时间内启动")
	}

	res, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "queued text",
		WhenRunning: "queue",
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("排队 SendMessage 失败: %v", err)
	}
	if res.Queued == nil || !*res.Queued {
		t.Fatalf("期望消息进入队列，实际 Queued=%v", res.Queued)
	}

	probeCh := probeDequeueCommit(t, b, s, threadID, "queued text")
	// 释放首轮：结束路径会自动出队并开启下一轮。
	releaseCh <- struct{}{}

	var probe dequeueOrderProbe
	select {
	case probe = <-probeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("未观察到自动出队时的队列提交")
	}
	if probe.err != nil {
		t.Fatalf("读取库内状态失败: %v", probe.err)
	}
	if !probe.messagePersisted {
		t.Fatal("出队提交时用户消息正文还不在库里：删队列行早于正文落库，崩溃会丢消息")
	}
	if probe.queueRowPresent {
		t.Fatal("出队提交后队列行仍在库里")
	}

	// 放行第二轮并等线程空闲，避免清理阶段留下未收尾的轮次。
	releaseCh <- struct{}{}
	waitThreadIdle(t, b, ctx, threadID)
}

// TestDequeueOrder_DrainQueuedFollowUps 覆盖 DrainQueuedFollowUps 这条同步出队路径。
func TestDequeueOrder_DrainQueuedFollowUps(t *testing.T) {
	ctx := context.Background()
	b, s, threadID := newDequeueOrderBackend(t, &controlledTestHarness{})

	// 先造一条已在库里的排队项，模拟重启后待投递的汇报。
	b.mu.Lock()
	td := b.threads[threadID]
	td.followUps = []wire.FollowUp{{
		ID:        "fu-drain-order",
		Text:      "drained report",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}}
	td.queueRevision++
	b.mu.Unlock()
	b.commitQueueChange(threadID)

	probeCh := probeDequeueCommit(t, b, s, threadID, "drained report")
	if err := b.DrainQueuedFollowUps(ctx, threadID); err != nil {
		t.Fatalf("DrainQueuedFollowUps 失败: %v", err)
	}

	var probe dequeueOrderProbe
	select {
	case probe = <-probeCh:
	default:
		t.Fatal("DrainQueuedFollowUps 未产生出队提交")
	}
	if probe.err != nil {
		t.Fatalf("读取库内状态失败: %v", probe.err)
	}
	if !probe.messagePersisted {
		t.Fatal("出队提交时用户消息正文还不在库里：删队列行早于正文落库，崩溃会丢消息")
	}
	if probe.queueRowPresent {
		t.Fatal("出队提交后队列行仍在库里")
	}

	waitThreadIdle(t, b, ctx, threadID)
}
