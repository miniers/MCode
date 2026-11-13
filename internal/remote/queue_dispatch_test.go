package remote

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

// TestQueueDispatch_QueueWhileRunning_Order 验证在 QueueWhileRunning=true 时：
// 运行中依次发送 'A' 和 'B'，两者按序排队；当前轮次结束后自动派发 'A'，随后派发 'B'；
// 最终在会话历史中用户消息顺序为 original, A, B。
func TestQueueDispatch_QueueWhileRunning_Order(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			QueueWhileRunning: true,
		}}
	})

	firstTurnStarted := make(chan struct{})
	firstTurnProceed := make(chan struct{})
	var firstTurnOnce sync.Once

	var turnOrder []string
	var turnMu sync.Mutex

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turnMu.Lock()
			turnOrder = append(turnOrder, turn.Prompt)
			turnMu.Unlock()

			if strings.Contains(turn.Prompt, "original") {
				firstTurnOnce.Do(func() {
					close(firstTurnStarted)
				})
				select {
				case <-firstTurnProceed:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			emit(harness.Event{Type: harness.EventText, Text: "reply to: " + turn.Prompt})
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	threadID := "thread-queue-order-1"
	conn := newMockStreamConn()
	hub.Subscribe(conn, threadID)
	backend.AddThread(wire.ThreadSummary{
		ID:       threadID,
		Title:    "Queue Order Thread",
		Provider: "mock",
	}, nil)

	// 发送初始消息启动第一轮
	sendOrigRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "original",
		ActionID: "act-orig",
	})
	if err != nil {
		t.Fatalf("SendMessage original failed: %v", err)
	}
	if sendOrigRes.TurnID == nil || *sendOrigRes.TurnID == "" {
		t.Fatalf("expected non-empty TurnID for original turn, got: %+v", sendOrigRes.TurnID)
	}

	// 等待第一轮运行起来
	select {
	case <-firstTurnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first turn to start")
	}

	// 轮次运行中，依次发送 'A' 与 'B'
	resA, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "A",
		ActionID: "act-send-a",
	})
	if err != nil {
		t.Fatalf("SendMessage A failed: %v", err)
	}
	if resA.Queued == nil || !*resA.Queued {
		t.Fatalf("expected resA.Queued to be true, got: %+v", resA.Queued)
	}
	if resA.FollowUpID == nil || *resA.FollowUpID == "" {
		t.Fatalf("expected resA.FollowUpID to be set")
	}

	resB, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "B",
		ActionID: "act-send-b",
	})
	if err != nil {
		t.Fatalf("SendMessage B failed: %v", err)
	}
	if resB.Queued == nil || !*resB.Queued {
		t.Fatalf("expected resB.Queued to be true, got: %+v", resB.Queued)
	}
	if resB.FollowUpID == nil || *resB.FollowUpID == "" {
		t.Fatalf("expected resB.FollowUpID to be set")
	}

	// 验证 A 和 B 按顺序出现在队列中
	snap, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(snap.FollowUps) != 2 {
		t.Fatalf("expected 2 follow-ups in queue, got %d", len(snap.FollowUps))
	}
	if snap.FollowUps[0].Text != "A" || snap.FollowUps[1].Text != "B" {
		t.Fatalf("expected queue to have ['A', 'B'] in order, got [%s, %s]", snap.FollowUps[0].Text, snap.FollowUps[1].Text)
	}

	// 释放第一轮，让其执行结束
	close(firstTurnProceed)

	// 等待整个会话处理完排队消息并重新变为空闲
	summary := waitThreadIdle(t, backend, ctx, threadID)
	if summary.IsRunning {
		t.Fatalf("expected thread IsRunning to be false after dispatching all turns, got true")
	}

	// 验证队列已全部出队清空
	snapEnd, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList at end failed: %v", err)
	}
	if len(snapEnd.FollowUps) != 0 {
		t.Fatalf("expected queue to be empty after all turns complete, got %d items", len(snapEnd.FollowUps))
	}

	// 验证历史消息中用户消息顺序为 original, A, B
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var userTexts []string
	for _, m := range hist.Messages {
		if m.Role == "user" {
			userTexts = append(userTexts, m.Text)
		}
	}
	if len(userTexts) != 3 {
		t.Fatalf("expected 3 user messages in history, got %d: %v", len(userTexts), userTexts)
	}
	if userTexts[0] != "original" {
		t.Errorf("expected first user message to be 'original', got %q", userTexts[0])
	}
	if userTexts[1] != "A" {
		t.Errorf("expected second user message to be 'A', got %q", userTexts[1])
	}
	if userTexts[2] != "B" {
		t.Errorf("expected third user message to be 'B', got %q", userTexts[2])
	}
	hub.Flush(threadID)
	delivered := make(map[string]bool)
	for _, event := range conn.getEvents() {
		for _, message := range event.Payload.Messages {
			delivered[message.ID] = true
		}
	}
	for _, message := range hist.Messages {
		if !delivered[message.ID] {
			t.Errorf("subscriber missed message %q during queue dispatch", message.Text)
		}
	}
}

// TestQueueDispatch_QueueWhileRunning_Rejected 验证在 QueueWhileRunning=false 时：
// 轮次运行中调用 SendMessage 会直接返回 ErrCodeOperationRejected 的 RPCError，且队列保持为空。
func TestQueueDispatch_QueueWhileRunning_Rejected(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			QueueWhileRunning: false,
		}}
	})

	turnStarted := make(chan struct{})
	turnProceed := make(chan struct{})
	var turnOnce sync.Once

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turnOnce.Do(func() {
				close(turnStarted)
			})
			select {
			case <-turnProceed:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(harness.Event{Type: harness.EventText, Text: "reply to: " + turn.Prompt})
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	threadID := "thread-queue-rejected-1"
	backend.AddThread(wire.ThreadSummary{
		ID:       threadID,
		Title:    "Queue Rejected Thread",
		Provider: "mock",
	}, nil)

	// 启动第一轮
	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial turn",
		ActionID: "act-rej-init",
	})
	if err != nil {
		t.Fatalf("SendMessage initial failed: %v", err)
	}

	select {
	case <-turnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn to start")
	}

	// 运行中发消息，QueueWhileRunning 为 false，应当返回 ErrCodeOperationRejected
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "should be rejected",
		ActionID: "act-rej-attempt",
	})
	if err == nil {
		t.Fatal("expected SendMessage while running to fail when QueueWhileRunning=false, got nil")
	}

	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *wire.RPCError, got %T (%v)", err, err)
	}
	if rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Errorf("expected ErrCodeOperationRejected (%d), got: %d", wire.ErrCodeOperationRejected, rpcErr.Code)
	}

	// 队列保持为空
	snap, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(snap.FollowUps) != 0 {
		t.Errorf("expected queue to remain empty, got %d items", len(snap.FollowUps))
	}

	close(turnProceed)
	waitThreadIdle(t, backend, ctx, threadID)
}

// TestQueueDispatch_QueueUpdate_InPlace 验证 QueueUpdate 在原地修改排队项内容且保持其队列位置不变。
func TestQueueDispatch_QueueUpdate_InPlace(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			QueueWhileRunning: true,
		}}
	})

	turnStarted := make(chan struct{})
	turnProceed := make(chan struct{})
	var turnOnce sync.Once

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turnOnce.Do(func() {
				close(turnStarted)
			})
			select {
			case <-turnProceed:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(harness.Event{Type: harness.EventText, Text: "reply to: " + turn.Prompt})
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	threadID := "thread-queue-update-1"
	backend.AddThread(wire.ThreadSummary{
		ID:       threadID,
		Title:    "Queue Update Thread",
		Provider: "mock",
	}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial turn",
		ActionID: "act-up-init",
	})
	if err != nil {
		t.Fatalf("SendMessage initial failed: %v", err)
	}

	select {
	case <-turnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn to start")
	}

	// 依次排队 3 条消息：item-1, item-2, item-3
	res1, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "item-1",
		ActionID: "act-item-1",
	})
	if err != nil || res1.FollowUpID == nil {
		t.Fatalf("SendMessage item-1 failed: %v", err)
	}
	fuID1 := *res1.FollowUpID

	res2, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "item-2",
		ActionID: "act-item-2",
	})
	if err != nil || res2.FollowUpID == nil {
		t.Fatalf("SendMessage item-2 failed: %v", err)
	}
	fuID2 := *res2.FollowUpID

	res3, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "item-3",
		ActionID: "act-item-3",
	})
	if err != nil || res3.FollowUpID == nil {
		t.Fatalf("SendMessage item-3 failed: %v", err)
	}
	fuID3 := *res3.FollowUpID

	// 初始队列应有 3 项
	snapBefore, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(snapBefore.FollowUps) != 3 {
		t.Fatalf("expected 3 items in queue, got %d", len(snapBefore.FollowUps))
	}
	if snapBefore.FollowUps[0].ID != fuID1 || snapBefore.FollowUps[1].ID != fuID2 || snapBefore.FollowUps[2].ID != fuID3 {
		t.Fatalf("unexpected queue IDs before update: %+v", snapBefore.FollowUps)
	}

	// 使用 QueueUpdate 更新中间一项 item-2 的文本
	updatedText := "item-2-modified"
	upRes, err := backend.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID:   threadID,
		FollowUpID: fuID2,
		Text:       &updatedText,
		ActionID:   "act-up-call",
	})
	if err != nil {
		t.Fatalf("QueueUpdate failed: %v", err)
	}
	if len(upRes.FollowUps) != 3 {
		t.Fatalf("expected 3 follow-ups in update result, got %d", len(upRes.FollowUps))
	}

	// 再次查询队列，验证 item-2 的内容原地修改，位置仍然保持在 index 1，前后项位置不变
	snapAfter, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList after update failed: %v", err)
	}
	if len(snapAfter.FollowUps) != 3 {
		t.Fatalf("expected 3 items in queue after update, got %d", len(snapAfter.FollowUps))
	}

	if snapAfter.FollowUps[0].ID != fuID1 || snapAfter.FollowUps[0].Text != "item-1" {
		t.Errorf("expected item-1 at index 0 unchanged, got: %+v", snapAfter.FollowUps[0])
	}
	if snapAfter.FollowUps[1].ID != fuID2 || snapAfter.FollowUps[1].Text != "item-2-modified" {
		t.Errorf("expected item-2 at index 1 updated in place to 'item-2-modified', got: %+v", snapAfter.FollowUps[1])
	}
	if snapAfter.FollowUps[2].ID != fuID3 || snapAfter.FollowUps[2].Text != "item-3" {
		t.Errorf("expected item-3 at index 2 unchanged, got: %+v", snapAfter.FollowUps[2])
	}

	close(turnProceed)
	waitThreadIdle(t, backend, ctx, threadID)
}
