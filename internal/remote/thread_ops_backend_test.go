package remote

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type controlledTestHarness struct {
	mu             sync.Mutex
	startedCh      chan struct{}
	releaseCh      chan struct{}
	promptReceived string
}

func (h *controlledTestHarness) Kind() harness.Kind {
	return "controlled"
}

func (h *controlledTestHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	h.mu.Lock()
	h.promptReceived = turn.Prompt
	h.mu.Unlock()

	emit(harness.Event{
		Type: harness.EventText,
		Text: "reply to: " + turn.Prompt,
	})

	if h.startedCh != nil {
		select {
		case h.startedCh <- struct{}{}:
		default:
		}
	}

	if h.releaseCh != nil {
		select {
		case <-h.releaseCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// 1. 会话运行中用 whenRunning=queue 发送会返回 Queued 和 FollowUpID，并触发 hook；
// 2. 当前轮正常完成后排队消息会自动发送（新的用户消息出现在时间线上、队列清空、revision 递增）；
func TestThreadOps_QueueWhileRunningAndAutoDequeue(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	hookCh := make(chan wire.ThreadsQueueSnapshot, 5)
	backend.SetQueueChangedHook(func(snap wire.ThreadsQueueSnapshot) {
		hookCh <- snap
	})

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Queue Test", Provider: "controlled"}, nil)

	// 发送首条消息开启第一轮
	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "turn 1",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("first SendMessage failed: %v", err)
	}

	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first turn to start")
	}

	// 正在运行，使用 whenRunning="queue" 发送排队消息
	queueRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "queued text 1",
		WhenRunning: "queue",
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("queue SendMessage failed: %v", err)
	}
	if queueRes.Queued == nil || !*queueRes.Queued {
		t.Fatalf("expected Queued=true, got %+v", queueRes.Queued)
	}
	if queueRes.FollowUpID == nil || *queueRes.FollowUpID == "" {
		t.Fatalf("expected non-empty FollowUpID, got %+v", queueRes.FollowUpID)
	}

	// 验证触发 hook
	select {
	case snap := <-hookCh:
		if snap.ThreadID != threadID || len(snap.FollowUps) != 1 || snap.FollowUps[0].Text != "queued text 1" {
			t.Fatalf("unexpected hook snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queueChangedHook")
	}

	// 释放第一轮使其正常完成
	releaseCh <- struct{}{}

	// 等待自动出队的新一轮完成（第二轮也会走 harness，重置 startedCh 与 releaseCh）
	// 第二轮不需要阻塞，直接让 releaseCh 有可用令牌
	releaseCh <- struct{}{}

	waitThreadIdle(t, backend, ctx, threadID)

	// 验证时间线上出现了排队消息
	history, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var foundQueuedUserMsg bool
	for _, m := range history.Messages {
		if m.Role == "user" && m.Text == "queued text 1" {
			foundQueuedUserMsg = true
			break
		}
	}
	if !foundQueuedUserMsg {
		t.Fatal("queued message was not sent to timeline")
	}

	// 验证队列已清空，revision 递增
	qSnap, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(qSnap.FollowUps) != 0 {
		t.Fatalf("expected empty queue, got %d items", len(qSnap.FollowUps))
	}
	if qSnap.Revision <= 1 {
		t.Fatalf("expected revision > 1, got %d", qSnap.Revision)
	}
}

// 验证 AddQueueChangedListener 多路监听与 hook 并存：
// 同时设置 hook 和两个 listener，入队后三者都收到通知；调用 remove 后被移除的 listener 不再收到。
func TestThreadOps_AddQueueChangedListener(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	hookCh := make(chan wire.ThreadsQueueSnapshot, 5)
	backend.SetQueueChangedHook(func(snap wire.ThreadsQueueSnapshot) {
		hookCh <- snap
	})

	listener1Ch := make(chan wire.ThreadsQueueSnapshot, 5)
	remove1 := backend.AddQueueChangedListener(func(snap wire.ThreadsQueueSnapshot) {
		listener1Ch <- snap
	})
	_ = remove1

	listener2Ch := make(chan wire.ThreadsQueueSnapshot, 5)
	remove2 := backend.AddQueueChangedListener(func(snap wire.ThreadsQueueSnapshot) {
		listener2Ch <- snap
	})

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Queue Listener Test", Provider: "controlled"}, nil)

	// 发送首条消息开启第一轮
	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "turn 1",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("first SendMessage failed: %v", err)
	}

	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first turn to start")
	}

	// 正在运行，使用 whenRunning="queue" 发送排队消息 1
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "queued text 1",
		WhenRunning: "queue",
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("first queue SendMessage failed: %v", err)
	}

	// 验证 hook 和两个 listener 都收到通知
	select {
	case snap := <-hookCh:
		if snap.ThreadID != threadID || len(snap.FollowUps) != 1 || snap.FollowUps[0].Text != "queued text 1" {
			t.Fatalf("unexpected hook snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook")
	}

	select {
	case snap := <-listener1Ch:
		if snap.ThreadID != threadID || len(snap.FollowUps) != 1 || snap.FollowUps[0].Text != "queued text 1" {
			t.Fatalf("unexpected listener1 snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for listener1")
	}

	select {
	case snap := <-listener2Ch:
		if snap.ThreadID != threadID || len(snap.FollowUps) != 1 || snap.FollowUps[0].Text != "queued text 1" {
			t.Fatalf("unexpected listener2 snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for listener2")
	}

	// 调用 remove2 移除 listener2
	remove2()

	// 再次入队消息 2
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "queued text 2",
		WhenRunning: "queue",
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("second queue SendMessage failed: %v", err)
	}

	// hook 和 listener1 应收到新快照
	select {
	case snap := <-hookCh:
		if len(snap.FollowUps) != 2 || snap.FollowUps[1].Text != "queued text 2" {
			t.Fatalf("unexpected second hook snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook second notification")
	}

	select {
	case snap := <-listener1Ch:
		if len(snap.FollowUps) != 2 || snap.FollowUps[1].Text != "queued text 2" {
			t.Fatalf("unexpected second listener1 snapshot: %+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for listener1 second notification")
	}

	// listener2 已被移除，不应再收到新通知
	select {
	case snap := <-listener2Ch:
		t.Fatalf("listener2 should not receive notification after removal, got: %+v", snap)
	case <-time.After(200 * time.Millisecond):
	}

	// 释放执行中的回合并清理
	releaseCh <- struct{}{}
	releaseCh <- struct{}{}
}

// 3. 被中断的轮次不会自动出队；
func TestThreadOps_InterruptedTurnDoesNotAutoDequeue(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Interrupt Test", Provider: "controlled"}, nil)

	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial turn",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn start")
	}

	// 排队一条消息
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "queued text that should stay",
		WhenRunning: "queue",
		ActionID:    newUUID(),
	})
	if err != nil {
		t.Fatalf("queue SendMessage failed: %v", err)
	}

	// 中断当前轮次
	_, err = backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   *sendRes.TurnID,
	})
	if err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	// 验证队列保持不动
	qSnap, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(qSnap.FollowUps) != 1 || qSnap.FollowUps[0].Text != "queued text that should stay" {
		t.Fatalf("expected queue to keep items, got %+v", qSnap.FollowUps)
	}
}

// 4. whenRunning 为非法值时返回 InvalidParams；
func TestThreadOps_WhenRunningInvalidParams(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Invalid whenRunning Test"}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "hello",
		WhenRunning: "invalid_option",
		ActionID:    newUUID(),
	})
	if err == nil {
		t.Fatal("expected error for invalid whenRunning")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams error, got %v", err)
	}
}

// 5. QueueUpdate 传空文本会移除该项；
func TestThreadOps_QueueUpdateEmptyTextRemoves(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "QueueUpdate Test", Provider: "controlled"}, nil)

	_, _ = backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "run", ActionID: newUUID()})
	<-startedCh

	qRes, _ := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        "to be deleted",
		WhenRunning: "queue",
		ActionID:    newUUID(),
	})
	fuID := *qRes.FollowUpID

	emptyText := "   "
	res, err := backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID:   threadID,
		FollowUpID: fuID,
		Text:       &emptyText,
		ActionID:   newUUID(),
	})
	if err != nil {
		t.Fatalf("QueueUpdate failed: %v", err)
	}
	if len(res.RemovedFollowUpIDs) != 1 || res.RemovedFollowUpIDs[0] != fuID {
		t.Fatalf("expected RemovedFollowUpIDs to contain %s, got %+v", fuID, res.RemovedFollowUpIDs)
	}
	if len(res.FollowUps) != 0 {
		t.Fatalf("expected queue to be empty, got %+v", res.FollowUps)
	}

	// 释放首轮，队列已空所以不会自动出队
	releaseCh <- struct{}{}
	waitThreadIdle(t, backend, ctx, threadID)
}

// 6. QueueRemove 会整组移除 bundle；
func TestThreadOps_QueueRemoveBundle(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Bundle Remove Test"}, nil)

	b1 := "bundle-1"
	b2 := "bundle-2"
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.followUps = []wire.FollowUp{
		{ID: "fu-1", Text: "part 1", BundleID: &b1},
		{ID: "fu-2", Text: "part 2", BundleID: &b1},
		{ID: "fu-3", Text: "independent", BundleID: &b2},
	}
	td.queueRevision = 1
	backend.mu.Unlock()

	res, err := backend.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID:   threadID,
		FollowUpID: "fu-1",
		ActionID:   newUUID(),
	})
	if err != nil {
		t.Fatalf("QueueRemove failed: %v", err)
	}
	if len(res.RemovedFollowUpIDs) != 2 {
		t.Fatalf("expected 2 removed items for bundle, got %+v", res.RemovedFollowUpIDs)
	}
	// fu-3 原本在 bundle-2，现在只剩一项，bundle 应被归整为 nil
	if len(res.FollowUps) != 1 || res.FollowUps[0].ID != "fu-3" || res.FollowUps[0].BundleID != nil {
		t.Fatalf("expected fu-3 unbundled, got %+v", res.FollowUps)
	}
}

// 7. QueueSendNow 在空闲时立即发送；
func TestThreadOps_QueueSendNowIdle(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "SendNow Test"}, nil)

	backend.mu.Lock()
	td := backend.threads[threadID]
	td.followUps = []wire.FollowUp{
		{ID: "fu-send-now", Text: "immediate message", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	td.queueRevision = 1
	backend.mu.Unlock()

	res, err := backend.QueueSendNow(ctx, wire.ThreadsQueueSendNowParams{
		ThreadID:   threadID,
		FollowUpID: "fu-send-now",
		ActionID:   newUUID(),
	})
	if err != nil {
		t.Fatalf("QueueSendNow failed: %v", err)
	}
	if len(res.RemovedFollowUpIDs) != 1 || res.RemovedFollowUpIDs[0] != "fu-send-now" {
		t.Fatalf("unexpected removed IDs: %+v", res.RemovedFollowUpIDs)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	history, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	found := false
	for _, m := range history.Messages {
		if m.Role == "user" && m.Text == "immediate message" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("immediate message was not sent to thread")
	}
}

// 8. QueueInterruptAndSend 在空闲时被拒，运行中返回 stopping，中断后会发送新内容；
func TestThreadOps_QueueInterruptAndSend(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "InterruptAndSend Test", Provider: "controlled"}, nil)

	// 1. 空闲时被拒
	msgText := "interrupting text"
	_, err := backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: threadID,
		Text:     &msgText,
		ActionID: newUUID(),
	})
	if err == nil {
		t.Fatal("expected rejection when idle")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected, got %v", err)
	}

	// 2. 运行中返回 stopping，中断后发送新内容
	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	sendRes, _ := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial long turn",
		ActionID: newUUID(),
	})
	<-startedCh

	intRes, err := backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: threadID,
		Text:     &msgText,
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("QueueInterruptAndSend failed: %v", err)
	}
	if intRes.State != wire.ThreadsQueueInterruptStateStopping {
		t.Fatalf("expected state stopping, got %q", intRes.State)
	}
	if intRes.TurnID == nil || *intRes.TurnID != *sendRes.TurnID {
		t.Fatalf("expected turnID match, got %+v", intRes.TurnID)
	}

	// 中断轮收尾时会直接开启 pendingSend 对应的一轮，等它开始后再释放
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for follow-up turn to start")
	}
	releaseCh <- struct{}{}

	waitThreadIdle(t, backend, ctx, threadID)

	history, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	found := false
	for _, m := range history.Messages {
		if m.Role == "user" && m.Text == "interrupting text" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("interrupted text was not sent after stop")
	}
}

// 9. MoveThread 的 up、down 和越界返回 moved=false，并且 ListThreads 顺序随之变化；
func TestThreadOps_MoveThread(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	proj := wire.ProjectSummary{ID: "p-move", Name: "Move Proj"}
	backend.AddProject(proj)

	t1, _ := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p-move", ActionID: newUUID()})
	t2, _ := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p-move", ActionID: newUUID()})
	t3, _ := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p-move", ActionID: newUUID()})

	// CreateThread 每次分配 min - 1，所以顺序依次为 t3 (最顶), t2, t1 (最底)
	list1, _ := backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &proj.ID})
	if len(list1.Threads) != 3 || list1.Threads[0].ID != t3.ThreadID || list1.Threads[1].ID != t2.ThreadID || list1.Threads[2].ID != t1.ThreadID {
		t.Fatalf("unexpected initial order: %+v", list1.Threads)
	}

	// 顶部的 t3 向上越界
	mRes, err := backend.MoveThread(ctx, t3.ThreadID, "up")
	if err != nil {
		t.Fatalf("MoveThread up failed: %v", err)
	}
	if mRes.Moved {
		t.Fatal("expected moved=false for out of bounds")
	}

	// t2 向上移动，成为第 0 个
	mRes, err = backend.MoveThread(ctx, t2.ThreadID, "up")
	if err != nil {
		t.Fatalf("MoveThread up failed: %v", err)
	}
	if !mRes.Moved {
		t.Fatal("expected moved=true")
	}

	list2, _ := backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &proj.ID})
	if list2.Threads[0].ID != t2.ThreadID || list2.Threads[1].ID != t3.ThreadID || list2.Threads[2].ID != t1.ThreadID {
		t.Fatalf("unexpected order after move up: %+v", list2.Threads)
	}

	// t2 向下移动回到第 1 个
	mRes, err = backend.MoveThread(ctx, t2.ThreadID, "down")
	if err != nil || !mRes.Moved {
		t.Fatalf("MoveThread down failed: %v", err)
	}
	list3, _ := backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &proj.ID})
	if list3.Threads[0].ID != t3.ThreadID || list3.Threads[1].ID != t2.ThreadID || list3.Threads[2].ID != t1.ThreadID {
		t.Fatalf("unexpected order after move down: %+v", list3.Threads)
	}
}

// 10. BranchThread 对非最终答复返回 InvalidParams、正常路径下继承源标题（不加 Branch: 前缀）、消息复制到目标消息为止、重复 actionID 返回同一 threadID；
func TestThreadOps_BranchThread(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := newUUID()
	isFinal := true
	isNotFinal := false
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Original Title", ProjectID: "p-branch"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "u1"},
		{ID: "m2", Role: "assistant", Text: "a1 partial", IsFinalResponse: &isNotFinal},
		{ID: "m3", Role: "assistant", Text: "a1 final", IsFinalResponse: &isFinal},
		{ID: "m4", Role: "user", Text: "u2"},
	})

	// 1. 对非最终答复（m2）分支返回 InvalidParams
	_, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  threadID,
		MessageID: "m2",
		ActionID:  newUUID(),
	})
	if err == nil {
		t.Fatal("expected error on non-final response")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got %v", err)
	}

	// 2. 正常分支
	actionID := newUUID()
	bRes, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  threadID,
		MessageID: "m3",
		ActionID:  actionID,
	})
	if err != nil {
		t.Fatalf("BranchThread failed: %v", err)
	}
	if bRes.ThreadID == "" || bRes.ThreadID == threadID {
		t.Fatalf("unexpected branch thread ID: %s", bRes.ThreadID)
	}

	newTh, err := backend.GetThread(ctx, bRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread on branch failed: %v", err)
	}
	if newTh.Title != "Original Title" {
		t.Fatalf("unexpected branch title: %q", newTh.Title)
	}

	hPage, _ := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: bRes.ThreadID})
	// 只复制到分叉点的消息，不再追加分叉来源系统消息（来源由 ForkedFromThreadID 表达）。
	if len(hPage.Messages) != 3 {
		t.Fatalf("expected 3 copied messages up to m3, got %d: %+v", len(hPage.Messages), hPage.Messages)
	}
	if hPage.Messages[0].Text != "u1" || hPage.Messages[2].Text != "a1 final" {
		t.Fatalf("unexpected copied messages: %+v", hPage.Messages)
	}
	if newTh.ForkedFromThreadID == nil || *newTh.ForkedFromThreadID != threadID {
		t.Fatalf("expected ForkedFromThreadID=%s, got %v", threadID, newTh.ForkedFromThreadID)
	}
	if newTh.ForkedFromMessageID == nil || *newTh.ForkedFromMessageID != "m3" {
		t.Fatalf("expected ForkedFromMessageID=m3, got %v", newTh.ForkedFromMessageID)
	}

	// 3. 重复 actionID 返回同一 threadID
	bRes2, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  threadID,
		MessageID: "m3",
		ActionID:  actionID,
	})
	if err != nil {
		t.Fatalf("repeat BranchThread failed: %v", err)
	}
	if bRes2.ThreadID != bRes.ThreadID {
		t.Fatalf("expected idempotent thread ID %s, got %s", bRes.ThreadID, bRes2.ThreadID)
	}
}

// 11. ForwardThread 继承源标题（不加 Continued: 前缀并写入 ForkedFromThreadID）、空会话会被拒，重复 actionID 返回同一 threadID；
func TestThreadOps_ForwardThread(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	emptyThreadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: emptyThreadID, Title: "Empty"}, nil)

	// 空会话被拒
	_, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: emptyThreadID,
		ActionID: newUUID(),
	})
	if err == nil {
		t.Fatal("expected rejection on empty thread")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected, got %v", err)
	}

	hasMsgThreadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: hasMsgThreadID, Title: "Has Msg", ProjectID: "p-fwd"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "hello fwd"},
	})

	actionID := newUUID()
	fRes, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: hasMsgThreadID,
		ActionID: actionID,
	})
	if err != nil {
		t.Fatalf("ForwardThread failed: %v", err)
	}

	fwdTh, _ := backend.GetThread(ctx, fRes.ThreadID)
	if fwdTh.Title != "Has Msg" {
		t.Fatalf("expected title 'Has Msg', got %q", fwdTh.Title)
	}
	if fwdTh.ForkedFromThreadID == nil || *fwdTh.ForkedFromThreadID != hasMsgThreadID {
		t.Fatalf("expected ForkedFromThreadID=%s, got %v", hasMsgThreadID, fwdTh.ForkedFromThreadID)
	}
	if fwdTh.ForkedFromMessageID != nil {
		t.Fatalf("expected ForkedFromMessageID=nil, got %v", fwdTh.ForkedFromMessageID)
	}

	// 历史消息不复制
	hPage, _ := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: fRes.ThreadID})
	if len(hPage.Messages) != 0 {
		t.Fatalf("expected 0 messages in forwarded thread, got %d", len(hPage.Messages))
	}

	// 重复 actionID 返回同一 threadID
	fRes2, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: hasMsgThreadID,
		ActionID: actionID,
	})
	if err != nil || fRes2.ThreadID != fRes.ThreadID {
		t.Fatalf("expected idempotent threadID %s, got %s, err: %v", fRes.ThreadID, fRes2.ThreadID, err)
	}
}

// 覆盖：源会话标题为 'Continued: Continued: 修复登录' 时，转发出的新会话标题是 '修复登录'，ForkedFromThreadID 等于源会话 ID，ForkedFromMessageID 为 nil。
func TestThreadOps_ForwardThread_StripLegacyPrefixes(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	srcID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:        srcID,
		Title:     "Continued: Continued: 修复登录",
		ProjectID: "p-strip-fwd",
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "登录有问题"},
	})

	fRes, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: srcID,
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("ForwardThread failed: %v", err)
	}

	newTh, err := backend.GetThread(ctx, fRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if newTh.Title != "修复登录" {
		t.Fatalf("expected title '修复登录', got %q", newTh.Title)
	}
	if newTh.ForkedFromThreadID == nil || *newTh.ForkedFromThreadID != srcID {
		t.Fatalf("expected ForkedFromThreadID=%s, got %v", srcID, newTh.ForkedFromThreadID)
	}
	if newTh.ForkedFromMessageID != nil {
		t.Fatalf("expected ForkedFromMessageID=nil, got %v", newTh.ForkedFromMessageID)
	}
}

// 覆盖：分支出的新会话标题与源标题相同（无前缀），ForkedFromMessageID 等于分叉点消息 ID。
func TestThreadOps_BranchThread_TitleAndForkedFrom(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	srcID := newUUID()
	isFinal := true
	backend.AddThread(wire.ThreadSummary{
		ID:        srcID,
		Title:     "修复购物车",
		ProjectID: "p-branch-fork",
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "结算失败"},
		{ID: "m2", Role: "assistant", Text: "已定位原因", IsFinalResponse: &isFinal},
	})

	bRes, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  srcID,
		MessageID: "m2",
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("BranchThread failed: %v", err)
	}

	newTh, err := backend.GetThread(ctx, bRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if newTh.Title != "修复购物车" {
		t.Fatalf("expected title '修复购物车', got %q", newTh.Title)
	}
	if newTh.ForkedFromThreadID == nil || *newTh.ForkedFromThreadID != srcID {
		t.Fatalf("expected ForkedFromThreadID=%s, got %v", srcID, newTh.ForkedFromThreadID)
	}
	if newTh.ForkedFromMessageID == nil || *newTh.ForkedFromMessageID != "m2" {
		t.Fatalf("expected ForkedFromMessageID=m2, got %v", newTh.ForkedFromMessageID)
	}

	// 源会话带遗留前缀时，分支新会话也应剥除前缀
	legacySrcID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:        legacySrcID,
		Title:     "Branch: Continued: 修复购物车",
		ProjectID: "p-branch-fork",
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "结算失败"},
		{ID: "m2", Role: "assistant", Text: "已定位原因", IsFinalResponse: &isFinal},
	})
	bResLegacy, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  legacySrcID,
		MessageID: "m2",
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("BranchThread on legacy title failed: %v", err)
	}
	legacyTh, err := backend.GetThread(ctx, bResLegacy.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if legacyTh.Title != "修复购物车" {
		t.Fatalf("expected title '修复购物车', got %q", legacyTh.Title)
	}
}

// 覆盖：stripLegacyOriginPrefixes 的表驱动测试，包括交替前缀、只有前缀、普通标题。
func TestStripLegacyOriginPrefixes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// 普通标题
		{name: "plain title", input: "修复登录问题", want: "修复登录问题"},
		{name: "plain title with whitespace", input: "  优化性能  ", want: "优化性能"},
		{name: "empty title", input: "", want: ""},
		{name: "whitespace only", input: "   ", want: ""},
		{name: "title containing prefix words inside", input: "fix: Continued: not at start", want: "fix: Continued: not at start"},
		{name: "prefix without space", input: "ContinuedWithoutSpace", want: "ContinuedWithoutSpace"},

		// 单一前缀
		{name: "single continued", input: "Continued: 修复登录", want: "修复登录"},
		{name: "single branch", input: "Branch: 重构侧边栏", want: "重构侧边栏"},
		{name: "single continued with extra space", input: "  Continued:   修复登录  ", want: "修复登录"},

		// 多重与交替前缀
		{name: "double continued", input: "Continued: Continued: 修复登录", want: "修复登录"},
		{name: "double branch", input: "Branch: Branch: 重构侧边栏", want: "重构侧边栏"},
		{name: "alternating continued then branch", input: "Continued: Branch: 修复登录", want: "修复登录"},
		{name: "alternating branch then continued", input: "Branch: Continued: 修复登录", want: "修复登录"},
		{name: "mixed multiple prefixes", input: "Continued: Branch: Continued: 长期任务", want: "长期任务"},
		{name: "mixed multiple prefixes with spaces", input: "  Branch:   Continued:   Branch:  深度测试 ", want: "深度测试"},

		// 只有前缀
		{name: "only continued prefix with space", input: "Continued: ", want: "Continued:"},
		{name: "only continued prefix trimmed", input: "Continued:", want: "Continued:"},
		{name: "only branch prefix with space", input: "Branch: ", want: "Branch:"},
		{name: "only branch prefix trimmed", input: "Branch:", want: "Branch:"},
		{name: "only continued and branch alternating", input: "Continued: Branch: ", want: "Continued: Branch:"},
		{name: "only branch and continued alternating", input: "Branch: Continued: ", want: "Branch: Continued:"},
		{name: "only multiple continued", input: "Continued: Continued: ", want: "Continued: Continued:"},
		{name: "only multiple branch", input: "Branch: Branch: ", want: "Branch: Branch:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripLegacyOriginPrefixes(tt.input)
			if got != tt.want {
				t.Errorf("stripLegacyOriginPrefixes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// 转发出的新会话必须与源会话保持同一套模型、推理强度、子代理与编排配置。
func TestThreadOps_ForwardThread_InheritsModelAndSwarm(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	strPtr := func(s string) *string { return &s }
	srcID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:                srcID,
		Title:             "Source",
		ProjectID:         "p-inherit",
		Provider:          "anthropic",
		Model:             strPtr("claude-opus-4"),
		Effort:            strPtr("high"),
		SpeedTier:         strPtr("fast"),
		SwarmMode:         "collaborative",
		SwarmBeeTiers:     "cheap",
		SwarmPairID:       strPtr("pair-1"),
		SwarmBeesProvider: strPtr("openai"),
		SwarmBeesModel:    strPtr("gpt-5"),
		SwarmBeesEffort:   strPtr("low"),
	}, []wire.ThreadMessage{{ID: "m1", Role: "user", Text: "源会话问题"}})

	// enabled 模拟管理器里的编排开关：继承回调把源会话的开关带到新会话，
	// 这样 GetThread 按真实路径判断也能看到新会话已开启。
	var mu sync.Mutex
	enabled := map[string]bool{srcID: true}
	backend.SetSwarm(true, func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return enabled[id]
	})
	var inherited [][2]string
	backend.SetSwarmInheritor(func(from, to string) {
		mu.Lock()
		defer mu.Unlock()
		inherited = append(inherited, [2]string{from, to})
		enabled[to] = enabled[from]
	})

	res, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{ThreadID: srcID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("ForwardThread failed: %v", err)
	}

	src, err := backend.GetThread(ctx, srcID)
	if err != nil {
		t.Fatalf("GetThread source failed: %v", err)
	}
	fwd, err := backend.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread forwarded failed: %v", err)
	}

	if fwd.Provider != src.Provider || derefString(fwd.Model) != derefString(src.Model) || derefString(fwd.Model) != "claude-opus-4" {
		t.Fatalf("转发会话未继承模型: src=%s/%s fwd=%s/%s", src.Provider, derefString(src.Model), fwd.Provider, derefString(fwd.Model))
	}
	if fwd.Effort == nil || src.Effort == nil || *fwd.Effort != *src.Effort {
		t.Fatalf("转发会话未继承推理强度: src=%v fwd=%v", src.Effort, fwd.Effort)
	}
	if fwd.SpeedTier == nil || src.SpeedTier == nil || *fwd.SpeedTier != *src.SpeedTier {
		t.Fatalf("转发会话未继承速度档位: src=%v fwd=%v", src.SpeedTier, fwd.SpeedTier)
	}
	if fwd.SwarmMode != src.SwarmMode || fwd.SwarmBeeTiers != src.SwarmBeeTiers {
		t.Fatalf("转发会话未继承编排模式/档位: src=%s/%s fwd=%s/%s", src.SwarmMode, src.SwarmBeeTiers, fwd.SwarmMode, fwd.SwarmBeeTiers)
	}
	for name, pair := range map[string][2]*string{
		"SwarmPairID":       {src.SwarmPairID, fwd.SwarmPairID},
		"SwarmBeesProvider": {src.SwarmBeesProvider, fwd.SwarmBeesProvider},
		"SwarmBeesModel":    {src.SwarmBeesModel, fwd.SwarmBeesModel},
		"SwarmBeesEffort":   {src.SwarmBeesEffort, fwd.SwarmBeesEffort},
	} {
		if pair[0] == nil || pair[1] == nil || *pair[0] != *pair[1] {
			t.Fatalf("转发会话未继承 %s: src=%v fwd=%v", name, pair[0], pair[1])
		}
	}
	if !fwd.SwarmEnabled {
		t.Fatal("源会话开启编排时，转发出的新会话也应开启")
	}
	if len(inherited) != 1 || inherited[0] != [2]string{srcID, res.ThreadID} {
		t.Fatalf("继承回调入参不符合预期: %v", inherited)
	}

	// 源会话未开启编排时新会话保持关闭。
	srcOffID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: srcOffID, Title: "Source Off", ProjectID: "p-inherit"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "未开启编排的源会话"},
	})
	triggered := false
	backend.SetSwarmInheritor(func(from, to string) {
		mu.Lock()
		defer mu.Unlock()
		triggered = true
		enabled[to] = enabled[from]
	})
	offRes, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{ThreadID: srcOffID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("ForwardThread(off) failed: %v", err)
	}
	if !triggered {
		t.Fatal("未开启编排的源会话也应触发继承回调")
	}
	backend.mu.Lock()
	stored := backend.threads[offRes.ThreadID].summary.SwarmEnabled
	backend.mu.Unlock()
	if stored {
		t.Fatal("源会话未开启编排时，新会话摘要里的编排开关应为 false")
	}
	offTh, err := backend.GetThread(ctx, offRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread forwarded(off) failed: %v", err)
	}
	if offTh.SwarmEnabled {
		t.Fatal("源会话未开启编排时，GetThread 读出的编排开关应为 false")
	}
}

// 分叉出的新会话必须与源会话保持同一套模型、推理强度、子代理与编排配置。
func TestThreadOps_BranchThread_InheritsModelAndSwarm(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	isFinal := true
	srcID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:                srcID,
		Title:             "Source",
		ProjectID:         "p-branch-inherit",
		Provider:          "anthropic",
		Model:             strPtr("claude-opus-4"),
		Effort:            strPtr("high"),
		SpeedTier:         strPtr("fast"),
		SwarmMode:         "collaborative",
		SwarmBeeTiers:     "cheap",
		SwarmPairID:       strPtr("pair-1"),
		SwarmBeesProvider: strPtr("openai"),
		SwarmBeesModel:    strPtr("gpt-5"),
		SwarmBeesEffort:   strPtr("low"),
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "源会话问题"},
		{ID: "m2", Role: "assistant", Text: "源会话回答", IsFinalResponse: &isFinal},
	})

	// enabled 模拟管理器里的编排开关：继承回调把源会话的开关带到新会话，
	// 这样 GetThread 按真实路径判断也能看到新会话已开启。
	var mu sync.Mutex
	enabled := map[string]bool{srcID: true}
	backend.SetSwarm(true, func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return enabled[id]
	})
	var inherited [][2]string
	backend.SetSwarmInheritor(func(from, to string) {
		mu.Lock()
		defer mu.Unlock()
		inherited = append(inherited, [2]string{from, to})
		enabled[to] = enabled[from]
	})

	res, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  srcID,
		MessageID: "m2",
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("BranchThread failed: %v", err)
	}

	src, err := backend.GetThread(ctx, srcID)
	if err != nil {
		t.Fatalf("GetThread source failed: %v", err)
	}
	br, err := backend.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread branched failed: %v", err)
	}

	if br.Provider != src.Provider || derefString(br.Model) != derefString(src.Model) || derefString(br.Model) != "claude-opus-4" {
		t.Fatalf("分叉会话未继承模型: src=%s/%s br=%s/%s", src.Provider, derefString(src.Model), br.Provider, derefString(br.Model))
	}
	if br.Effort == nil || src.Effort == nil || *br.Effort != *src.Effort || *br.Effort != "high" {
		t.Fatalf("分叉会话未继承推理强度: src=%v br=%v", src.Effort, br.Effort)
	}
	if br.SpeedTier == nil || src.SpeedTier == nil || *br.SpeedTier != *src.SpeedTier {
		t.Fatalf("分叉会话未继承速度档位: src=%v br=%v", src.SpeedTier, br.SpeedTier)
	}
	if br.SwarmMode != src.SwarmMode || br.SwarmMode != "collaborative" {
		t.Fatalf("分叉会话未继承编排模式: src=%s br=%s", src.SwarmMode, br.SwarmMode)
	}
	if br.SwarmBeeTiers != src.SwarmBeeTiers || br.SwarmBeeTiers != "cheap" {
		t.Fatalf("分叉会话未继承子代理档位: src=%s br=%s", src.SwarmBeeTiers, br.SwarmBeeTiers)
	}
	for name, tc := range map[string]struct {
		src, br *string
		want    string
	}{
		"SwarmPairID":       {src.SwarmPairID, br.SwarmPairID, "pair-1"},
		"SwarmBeesProvider": {src.SwarmBeesProvider, br.SwarmBeesProvider, "openai"},
		"SwarmBeesModel":    {src.SwarmBeesModel, br.SwarmBeesModel, "gpt-5"},
		"SwarmBeesEffort":   {src.SwarmBeesEffort, br.SwarmBeesEffort, "low"},
	} {
		if tc.src == nil || tc.br == nil || *tc.src != *tc.br || *tc.br != tc.want {
			t.Fatalf("分叉会话未继承 %s: src=%v br=%v", name, tc.src, tc.br)
		}
	}
	if !br.SwarmEnabled {
		t.Fatal("源会话开启编排时，分叉出的新会话也应开启")
	}
	if len(inherited) != 1 || inherited[0] != [2]string{srcID, res.ThreadID} {
		t.Fatalf("继承回调入参不符合预期: %v", inherited)
	}

	// 源会话未开启编排时新会话保持关闭。
	srcOffID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: srcOffID, Title: "Source Off", ProjectID: "p-branch-inherit"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "未开启编排的源会话"},
		{ID: "m2", Role: "assistant", Text: "回答", IsFinalResponse: &isFinal},
	})
	triggered := false
	backend.SetSwarmInheritor(func(from, to string) {
		mu.Lock()
		defer mu.Unlock()
		triggered = true
		enabled[to] = enabled[from]
	})
	offRes, err := backend.BranchThread(ctx, wire.ThreadsBranchParams{
		ThreadID:  srcOffID,
		MessageID: "m2",
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("BranchThread(off) failed: %v", err)
	}
	if !triggered {
		t.Fatal("未开启编排的源会话也应触发继承回调")
	}
	backend.mu.Lock()
	stored := backend.threads[offRes.ThreadID].summary.SwarmEnabled
	backend.mu.Unlock()
	if stored {
		t.Fatal("源会话未开启编排时，新会话摘要里的编排开关应为 false")
	}
	offTh, err := backend.GetThread(ctx, offRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread branched(off) failed: %v", err)
	}
	if offTh.SwarmEnabled {
		t.Fatal("源会话未开启编排时，GetThread 读出的编排开关应为 false")
	}
}

// C-ST-07：冷会话（消息已被内存淘汰、仍在 store 中）转发时应先回载历史，而不是当成空会话拒绝。
func TestThreadOps_ForwardThread_ColdEvictedThread(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "forward-cold.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	backend.AddProject(wire.ProjectSummary{ID: "p-cold", Name: "冷会话项目", Path: "/tmp/cold-project"})
	threadID := newUUID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p-cold", Title: "冷会话"}, []wire.ThreadMessage{
		{ID: newUUID(), Role: "user", Text: "冷会话的问题", CreatedAt: now},
		{ID: newUUID(), Role: "assistant", Text: "冷会话的回答", CreatedAt: now},
	})

	// 模拟冷会话淘汰：消息已持久化到 store，但内存中的 messages 被清空。
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.messages = nil
	td.messagesEvicted = true
	backend.mu.Unlock()

	res, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{ThreadID: threadID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("冷会话转发应成功，实际报错: %v", err)
	}
	if res.ThreadID == "" || res.ThreadID == threadID {
		t.Fatalf("期望返回新的 ThreadID，实际 %q", res.ThreadID)
	}

	backend.mu.Lock()
	continuation := backend.threads[res.ThreadID].continuation
	reloaded := len(backend.threads[threadID].messages)
	backend.mu.Unlock()
	if reloaded != 2 {
		t.Fatalf("期望源会话回载 2 条消息，实际 %d", reloaded)
	}
	if !strings.Contains(continuation, "冷会话的问题") || !strings.Contains(continuation, "冷会话的回答") {
		t.Fatalf("转发上下文缺少历史消息: %q", continuation)
	}
	if !strings.HasPrefix(continuation, "The user carried their previous chat \"冷会话\" over into this new thread.") {
		t.Fatalf("转发上下文缺少源会话标题导言: %q", continuation)
	}
}

// 12. buildForwardContext 的转发导言、附件声明与截断省略标记；
func TestThreadOps_BuildForwardContext(t *testing.T) {
	const sourceTitle = "源会话标题"

	// 0. units 为空时仍返回导言与空标记块
	emptyStr := buildForwardContext(sourceTitle, nil)
	if !strings.Contains(emptyStr, "The user carried their previous chat \""+sourceTitle+"\" over into this new thread.") {
		t.Fatalf("空上下文缺少导言: %q", emptyStr)
	}
	if !strings.Contains(emptyStr, forwardedContextStart+"\n\n"+forwardedContextEnd) {
		t.Fatalf("空上下文缺少空标记块: %q", emptyStr)
	}

	// 1. 包含超长首条用户消息（> 4000 字符）
	longUserText := strings.Repeat("A", 4500)
	msgs := []wire.ThreadMessage{
		{Role: "user", Text: longUserText},
		{Role: "assistant", Text: "short reply"},
	}
	ctxStr := buildForwardContext(sourceTitle, msgs)
	if !strings.Contains(ctxStr, "[trimmed]") {
		t.Fatal("expected [trimmed] for long first user message")
	}
	if !strings.HasPrefix(ctxStr, "The user carried their previous chat \""+sourceTitle+"\" over into this new thread.") {
		t.Fatalf("missing title intro: %q", ctxStr)
	}
	if !strings.Contains(ctxStr, "Everything between the markers is a copy of that chat") {
		t.Fatal("missing historical-reference intro line")
	}
	if !strings.Contains(ctxStr, mentionTrimmedNotice) {
		t.Fatal("missing truncation notice")
	}
	if !strings.Contains(ctxStr, "Nothing else carries over: no provider session, tool state or active bees. The original chat may keep running on its own. Only the conversation was copied, not files or a worktree, so work in this thread's own working directory.") {
		t.Fatal("missing isolation notice")
	}
	if strings.Contains(ctxStr, "that mcode still has on disk") {
		t.Fatal("unexpected attachment notice without attachments")
	}
	if !strings.Contains(ctxStr, forwardedContextStart+"\n") || !strings.HasSuffix(ctxStr, forwardedContextEnd) {
		t.Fatal("missing delimiters")
	}

	// 2. 消息过多超出 48000 字符，测试省略标记
	var manyMsgs []wire.ThreadMessage
	manyMsgs = append(manyMsgs, wire.ThreadMessage{Role: "user", Text: "first question"})
	for i := 0; i < 20; i++ {
		manyMsgs = append(manyMsgs, wire.ThreadMessage{Role: "assistant", Text: fmt.Sprintf("chunk %d: %s", i, strings.Repeat("X", 3000))})
	}
	ctxStr2 := buildForwardContext(sourceTitle, manyMsgs)
	if !strings.Contains(ctxStr2, "[… part of the copied conversation was dropped here to stay within the size budget …]") {
		t.Fatal("expected omission marker when messages exceed total budget")
	}
	if !strings.Contains(ctxStr2, mentionTrimmedNotice) {
		t.Fatal("expected truncation notice when messages exceed total budget")
	}

	// 3. 附件声明按总数区分单复数
	oneAttachment := []wire.ThreadMessage{
		{Role: "user", Text: "看看这张图", Media: []wire.MediaRef{{ID: newUUID(), Name: "a.png"}}},
	}
	ctxStr3 := buildForwardContext(sourceTitle, oneAttachment)
	if !strings.Contains(ctxStr3, "The copied chat mentions a file that mcode still has on disk; the names and paths appear in the copy below.") {
		t.Fatalf("expected singular attachment notice: %q", ctxStr3)
	}
	if strings.Contains(ctxStr3, mentionTrimmedNotice) {
		t.Fatal("unexpected truncation notice without truncation")
	}

	multiAttachment := []wire.ThreadMessage{
		{Role: "user", Text: "看看这两张图", Media: []wire.MediaRef{{ID: newUUID(), Name: "a.png"}, {ID: newUUID(), Name: "b.png"}}},
	}
	ctxStr4 := buildForwardContext(sourceTitle, multiAttachment)
	if !strings.Contains(ctxStr4, "The copied chat mentions several files that mcode still has on disk; the names and paths appear in the copy below.") {
		t.Fatalf("expected plural attachment notice: %q", ctxStr4)
	}
}

// 13. SettleAction 在 journalID 不匹配时被拒，未知 actionID 会写入墓碑并返回 safeToRecoverDraft=true，之后用该 actionID 调 SendMessage 被拒；
func TestThreadOps_SettleAction(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	// 1. journalID 不匹配被拒
	_, err := backend.SettleAction(ctx, wire.ThreadsSettleActionParams{
		ActionID:        newUUID(),
		ActionJournalID: "wrong-journal-id",
	})
	if err == nil {
		t.Fatal("expected error on journalID mismatch")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected, got %v", err)
	}

	// 2. 未知 actionID 写入墓碑
	actID := newUUID()
	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Tombstone Test"}, nil)

	sRes, err := backend.SettleAction(ctx, wire.ThreadsSettleActionParams{
		ActionID:        actID,
		ActionJournalID: backend.actionJournalID,
		ThreadID:        &threadID,
	})
	if err != nil {
		t.Fatalf("SettleAction failed: %v", err)
	}
	if !sRes.SafeToRecoverDraft {
		t.Fatal("expected safeToRecoverDraft=true for tombstone")
	}
	if sRes.State != wire.ActionStateFailed {
		t.Fatalf("expected state failed, got %q", sRes.State)
	}

	// 3. 用该 actionID 调用 SendMessage 被拒
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "should fail",
		ActionID: actID,
	})
	if err == nil {
		t.Fatal("expected SendMessage to fail for tombstoned actionID")
	}
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected, got %v", err)
	}
}

// 14. 带 store 时重建一个新 backend 并 AttachStore 后，队列和排序都能恢复。
func TestThreadOps_StoreReloadQueueAndSortOrder(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "ops_test.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store 1: %v", err)
	}

	proj := wire.ProjectSummary{ID: "p-reload", Name: "Reload Proj"}
	backend1.AddProject(proj)

	th1, _ := backend1.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p-reload", ActionID: newUUID()})
	th2, _ := backend1.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p-reload", ActionID: newUUID()})

	// 调整顺序
	_, err = backend1.MoveThread(ctx, th1.ThreadID, "up")
	if err != nil {
		t.Fatalf("MoveThread failed: %v", err)
	}

	// 为 th1 添加排队项并持久化
	backend1.mu.Lock()
	td := backend1.threads[th1.ThreadID]
	td.followUps = []wire.FollowUp{
		{ID: "fu-persisted-1", Text: "persisted follow-up", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	td.queueRevision = 5
	backend1.mu.Unlock()
	backend1.commitQueueChange(th1.ThreadID)

	// 关闭第一组实例
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	// 重新打开并加载到新 backend
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	// 验证排序恢复
	list, err := backend2.ListThreads(ctx, wire.ThreadListParams{ProjectID: &proj.ID})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	if len(list.Threads) != 2 || list.Threads[0].ID != th1.ThreadID || list.Threads[1].ID != th2.ThreadID {
		t.Fatalf("expected th1 before th2 after reload, got %+v", list.Threads)
	}

	// 验证队列恢复
	qSnap, err := backend2.QueueList(ctx, th1.ThreadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(qSnap.FollowUps) != 1 || qSnap.FollowUps[0].ID != "fu-persisted-1" || qSnap.FollowUps[0].Text != "persisted follow-up" {
		t.Fatalf("unexpected queue after reload: %+v", qSnap)
	}
	if qSnap.Revision != 5 {
		t.Fatalf("expected revision 5, got %d", qSnap.Revision)
	}
}

// 系统消息（priority 跟进）在快照里带 System 标记，后端拒绝编辑与撤回；普通用户条目不受影响。
func TestThreadOps_PriorityFollowUpsAreReadOnly(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	backend.RegisterHarness(&controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh})
	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "System FollowUp Test", Provider: "controlled"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "run", ActionID: newUUID()}); err != nil {
		t.Fatal(err)
	}
	<-startedCh
	qRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID, Text: "user follow-up", WhenRunning: "queue", ActionID: newUUID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	userID := *qRes.FollowUpID
	if err := backend.SendSwarmReport(ctx, threadID, "[Hydra] report"); err != nil {
		t.Fatal(err)
	}

	snap, err := backend.QueueList(ctx, threadID)
	if err != nil || len(snap.FollowUps) != 2 {
		t.Fatalf("queue snapshot: %+v, %v", snap, err)
	}
	var reportID string
	for _, fu := range snap.FollowUps {
		if fu.ID == userID {
			if fu.System {
				t.Fatalf("user follow-up must not be system: %+v", fu)
			}
			continue
		}
		reportID = fu.ID
		if !fu.System {
			t.Fatalf("priority report must be system: %+v", fu)
		}
	}
	if reportID == "" {
		t.Fatalf("swarm report not found in queue: %+v", snap.FollowUps)
	}

	edited := "edited"
	if _, err := backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: reportID, Text: &edited, ActionID: newUUID(),
	}); err == nil {
		t.Fatal("QueueUpdate must reject system follow-up")
	} else if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.ErrCodeOperationRejected || rpcErr.Message != "系统消息不可编辑" {
		t.Fatalf("unexpected QueueUpdate error: %v", err)
	}
	if _, err := backend.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID: threadID, FollowUpID: reportID, ActionID: newUUID(),
	}); err == nil {
		t.Fatal("QueueRemove must reject system follow-up")
	} else if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.ErrCodeOperationRejected || rpcErr.Message != "系统消息不可撤回" {
		t.Fatalf("unexpected QueueRemove error: %v", err)
	}
	if snap, _ := backend.QueueList(ctx, threadID); len(snap.FollowUps) != 2 {
		t.Fatalf("rejected mutations must keep queue unchanged: %+v", snap.FollowUps)
	}

	// 普通用户条目仍可编辑与撤回。
	editedUser := "edited user"
	if _, err := backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: userID, Text: &editedUser, ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("user follow-up must stay editable: %v", err)
	}
	if _, err := backend.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID: threadID, FollowUpID: userID, ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("user follow-up must stay removable: %v", err)
	}
	if snap, _ := backend.QueueList(ctx, threadID); len(snap.FollowUps) != 1 || snap.FollowUps[0].ID != reportID || !snap.FollowUps[0].System {
		t.Fatalf("only the system follow-up should remain: %+v", snap.FollowUps)
	}
}

// bundle 里只要有一条系统消息，撤回同组普通条目也要被拒绝。
func TestThreadOps_QueueRemoveRejectsBundleContainingSystemFollowUp(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "System Bundle Test"}, nil)

	bundle := "bundle-sys"
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.followUps = []wire.FollowUp{
		{ID: "fu-sys", Text: "系统消息", BundleID: &bundle},
		{ID: "fu-user", Text: "用户消息", BundleID: &bundle},
	}
	td.queueRevision = 1
	td.priorityFollowUps = map[string]bool{"fu-sys": true}
	backend.mu.Unlock()

	if _, err := backend.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{
		ThreadID: threadID, FollowUpID: "fu-user", ActionID: newUUID(),
	}); err == nil {
		t.Fatal("QueueRemove must reject a bundle containing a system follow-up")
	} else if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.ErrCodeOperationRejected || rpcErr.Message != "系统消息不可撤回" {
		t.Fatalf("unexpected QueueRemove error: %v", err)
	}

	snap, err := backend.QueueList(ctx, threadID)
	if err != nil || len(snap.FollowUps) != 2 {
		t.Fatalf("rejected bundle removal must keep queue unchanged: %+v, %v", snap, err)
	}
	for _, fu := range snap.FollowUps {
		wantSystem := fu.ID == "fu-sys"
		if fu.System != wantSystem {
			t.Fatalf("follow-up %s System = %v, want %v", fu.ID, fu.System, wantSystem)
		}
	}
}
