package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// auditEvents 读取某个会话的全部队列审计事件，供断言使用。
func auditEvents(t *testing.T, s *store.Store, threadID string) []store.FollowUpEvent {
	t.Helper()
	events, err := s.ListFollowUpEvents(context.Background(), threadID, 0)
	if err != nil {
		t.Fatalf("ListFollowUpEvents 失败: %v", err)
	}
	return events
}

// hasAuditEvent 判断给定事件集合里是否存在匹配 kind/reason/followUpID 的记录；
// reason 或 followUpID 传空表示不限制该字段。
func hasAuditEvent(events []store.FollowUpEvent, kind, reason, followUpID string) bool {
	for _, ev := range events {
		if ev.Kind != kind {
			continue
		}
		if reason != "" && ev.Reason != reason {
			continue
		}
		if followUpID != "" && ev.FollowUpID != followUpID {
			continue
		}
		return true
	}
	return false
}

func TestQueueSendNowRecordsDispatchingEvent(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	ctx := context.Background()

	fuID := queueMediaMessage(t, b, threadID)
	if _, err := b.QueueSendNow(ctx, wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: fuID}); err != nil {
		t.Fatalf("QueueSendNow 失败: %v", err)
	}

	events := auditEvents(t, s, threadID)
	if !hasAuditEvent(events, store.FollowUpEventDispatching, "send_now", fuID) {
		t.Fatalf("缺少 dispatching/send_now 事件: %+v", events)
	}
	for _, ev := range events {
		if ev.FollowUpID == fuID && ev.Kind == store.FollowUpEventDispatching {
			if ev.SourceID == "" {
				t.Fatal("dispatching 事件缺少 source_id（轮次）")
			}
			return
		}
	}
}

func TestQueueRemoveRecordsDroppedEventWithReason(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	ctx := context.Background()

	first := queueMediaMessage(t, b, threadID)
	second := queueMediaMessage(t, b, threadID)
	if _, err := b.QueueRemove(ctx, wire.ThreadsQueueRemoveParams{ThreadID: threadID, FollowUpID: first}); err != nil {
		t.Fatalf("QueueRemove 失败: %v", err)
	}

	events := auditEvents(t, s, threadID)
	if !hasAuditEvent(events, store.FollowUpEventDropped, store.FollowUpDropUserRemove, first) {
		t.Fatalf("缺少 dropped/user_remove 事件: %+v", events)
	}
	if hasAuditEvent(events, store.FollowUpEventDropped, store.FollowUpDropVanished, first) {
		t.Fatalf("用户删除不应再补 vanished 事件: %+v", events)
	}
	if !hasAuditEvent(events, store.FollowUpEventQueued, "", second) {
		t.Fatalf("未删除项应保留 queued 事件: %+v", events)
	}
}

func TestQueueUpdateClearRecordsDroppedEventWithReason(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	ctx := context.Background()

	fuID := queueMediaMessage(t, b, threadID)
	empty := ""
	if _, err := b.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: fuID, Text: &empty, Attachments: []string{},
	}); err != nil {
		t.Fatalf("QueueUpdate 失败: %v", err)
	}

	events := auditEvents(t, s, threadID)
	if !hasAuditEvent(events, store.FollowUpEventDropped, store.FollowUpDropEmptyEdit, fuID) {
		t.Fatalf("缺少 dropped/empty_edit 事件: %+v", events)
	}
}

func TestQueueSendNowWhileRunningRecordsSteeredEvent(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	ctx := context.Background()

	fuID := queueMediaMessage(t, b, threadID)
	b.mu.Lock()
	td := b.threads[threadID]
	td.summary.IsRunning = true
	td.runTurnID = "turn-running"
	td.runCancel = func() {}
	b.mu.Unlock()

	if _, err := b.QueueSendNow(ctx, wire.ThreadsQueueSendNowParams{ThreadID: threadID, FollowUpID: fuID}); err != nil {
		t.Fatalf("QueueSendNow 失败: %v", err)
	}

	b.mu.Lock()
	pending := td.pendingSend
	b.mu.Unlock()
	if pending == nil {
		t.Fatal("运行中 sendNow 应生成 pendingSend")
	}

	events := auditEvents(t, s, threadID)
	if !hasAuditEvent(events, store.FollowUpEventSteered, "send_now_interrupt", fuID) {
		t.Fatalf("缺少 steered 事件: %+v", events)
	}
	for _, ev := range events {
		if ev.FollowUpID == fuID && ev.Kind == store.FollowUpEventSteered && ev.SourceID != "turn-running" {
			t.Fatalf("steered 事件 source_id = %q，期望 turn-running", ev.SourceID)
		}
	}
	if !hasAuditEvent(events, store.FollowUpEventQueued, "", pending.ID) {
		t.Fatalf("新 pendingSend 缺少 queued 事件: %+v", events)
	}
}

func TestQueueAuditQueuedOnEnqueue(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	ctx := context.Background()

	normal := queueMediaMessage(t, b, threadID)
	events := auditEvents(t, s, threadID)
	if !hasAuditEvent(events, store.FollowUpEventQueued, "", normal) {
		t.Fatalf("普通入队缺少 queued 事件: %+v", events)
	}

	// priority 入队走 SendSwarmReport：运行中排队时插到队首。
	b.mu.Lock()
	td := b.threads[threadID]
	td.summary.IsRunning = true
	b.mu.Unlock()
	if err := b.SendSwarmReport(ctx, threadID, "子代理汇报"); err != nil {
		t.Fatalf("SendSwarmReport 失败: %v", err)
	}
	b.mu.Lock()
	var priorityID string
	for _, fu := range td.followUps {
		if td.isPriorityFollowUpLocked(fu) {
			priorityID = fu.ID
			break
		}
	}
	b.mu.Unlock()
	if priorityID == "" {
		t.Fatal("未找到 priority 入队项")
	}

	events = auditEvents(t, s, threadID)
	if !hasAuditEvent(events, store.FollowUpEventQueued, "", priorityID) {
		t.Fatalf("priority 入队缺少 queued 事件: %+v", events)
	}
}

// TestStartNextQueuedTurnRecordsNaturalDelivery 验证自然出队（非 sendNow、非 pendingSend）
// 记录的 dispatching 事件 reason 为空、source 为本轮 turn ID。
func TestStartNextQueuedTurnRecordsNaturalDelivery(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	fuID := queueMediaMessage(t, b, threadID)

	b.mu.Lock()
	td := b.threads[threadID]
	started, _, ok := b.startNextQueuedTurnLocked(td, true, true)
	b.mu.Unlock()
	if !ok || started == nil {
		t.Fatal("自然出队未开轮")
	}
	b.commitQueueChange(threadID)

	events := auditEvents(t, s, threadID)
	for _, ev := range events {
		if ev.Kind == store.FollowUpEventDispatching && ev.FollowUpID == fuID {
			if ev.Reason != "" {
				t.Fatalf("自然出队 reason = %q，期望空", ev.Reason)
			}
			if ev.SourceID != started.turnID {
				t.Fatalf("自然出队 source_id = %q，期望 %q", ev.SourceID, started.turnID)
			}
			return
		}
	}
	t.Fatalf("缺少自然出队的 dispatching 事件: %+v", events)
}

// TestStartNextQueuedTurnRecordsPendingSendDelivery 验证 sendNow 打断后暂存的 pendingSend
// 在下一轮出队时记录 dispatching/pending_send，source 为新轮次 ID。
func TestStartNextQueuedTurnRecordsPendingSendDelivery(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)

	pendingID := "pending-1"
	b.mu.Lock()
	td := b.threads[threadID]
	td.pendingSend = &wire.FollowUp{
		ID:        pendingID,
		Text:      "打断后待发送",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	started, _, ok := b.startNextQueuedTurnLocked(td, false, false)
	b.mu.Unlock()
	if !ok || started == nil {
		t.Fatal("pendingSend 未开轮")
	}
	b.commitQueueChange(threadID)

	events := auditEvents(t, s, threadID)
	for _, ev := range events {
		if ev.Kind == store.FollowUpEventDispatching && ev.FollowUpID == pendingID {
			if ev.Reason != "pending_send" {
				t.Fatalf("pendingSend 出队 reason = %q，期望 pending_send", ev.Reason)
			}
			if ev.SourceID != started.turnID {
				t.Fatalf("pendingSend 出队 source_id = %q，期望 %q", ev.SourceID, started.turnID)
			}
			return
		}
	}
	t.Fatalf("缺少 pendingSend 的 dispatching 事件: %+v", events)
}

// TestQueueAuditEditAndMoveDoNotEmitDropped 验证改文只产生 edited、移动顺序不产生任何事件，
// 且不会因为重排快照误报 dropped/vanished。
func TestQueueAuditEditAndMoveDoNotEmitDropped(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	ctx := context.Background()

	first := queueMediaMessage(t, b, threadID)
	second := queueMediaMessage(t, b, threadID)
	text := "改后的内容"
	if _, err := b.QueueUpdate(ctx, wire.ThreadsQueueUpdateParams{
		ThreadID: threadID, FollowUpID: first, Text: &text, Attachments: []string{},
	}); err != nil {
		t.Fatalf("QueueUpdate 失败: %v", err)
	}

	events := auditEvents(t, s, threadID)
	edited := 0
	for _, ev := range events {
		if ev.FollowUpID != first {
			continue
		}
		switch {
		case ev.Kind == store.FollowUpEventEdited:
			edited++
		case ev.Kind == store.FollowUpEventDropped:
			t.Fatalf("改文不应产生 dropped 事件: %+v", ev)
		}
	}
	if edited != 1 {
		t.Fatalf("改文后 edited 事件数 = %d，期望 1: %+v", edited, events)
	}

	beforeMove := len(events)
	if _, err := b.QueueMove(ctx, wire.ThreadsQueueMoveParams{
		ThreadID: threadID, FollowUpID: second, BeforeFollowUpID: first,
	}); err != nil {
		t.Fatalf("QueueMove 失败: %v", err)
	}
	afterMove := auditEvents(t, s, threadID)
	if len(afterMove) != beforeMove {
		t.Fatalf("移动顺序产生了多余事件: %d -> %d: %+v", beforeMove, len(afterMove), afterMove)
	}
}
