package store

import (
	"context"
	"testing"
	"time"
)

// getDelivery 按 message_id 读一行，读不到直接终止测试。
func getDelivery(t *testing.T, s *Store, messageID string) Delivery {
	t.Helper()
	d, err := scanDelivery(s.db.QueryRowContext(context.Background(),
		"SELECT "+deliverySelectCols+" FROM thread_deliveries WHERE message_id = ?", messageID))
	if err != nil {
		t.Fatalf("读取 delivery %s 失败: %v", messageID, err)
	}
	return d
}

// TestDeliveryTransitionsOnlyFromDispatching 只有 dispatching 行能迁到 accepted/failed，终态不被覆盖。
func TestDeliveryTransitionsOnlyFromDispatching(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []string{"m1", "m2"} {
		if err := s.InsertDelivery(ctx, Delivery{
			MessageID: id, ThreadID: threadID, FollowUpID: "f-" + id, CreatedAt: now,
		}); err != nil {
			t.Fatalf("InsertDelivery 失败: %v", err)
		}
	}
	if got := getDelivery(t, s, "m1"); got.State != DeliveryDispatching || got.Attempt != 1 || got.SettledAt != nil {
		t.Fatalf("初始行不对: %+v", got)
	}

	if err := s.MarkDeliveryAccepted(ctx, "m1", "turn-9"); err != nil {
		t.Fatalf("MarkDeliveryAccepted 失败: %v", err)
	}
	if got := getDelivery(t, s, "m1"); got.State != DeliveryAccepted || got.TurnID != "turn-9" || got.SettledAt == nil {
		t.Fatalf("accepted 行不对: %+v", got)
	}

	// 已是 accepted，后续 failed 与再次 accepted 都不应改写。
	if err := s.MarkDeliveryFailed(ctx, "m1", "late_failure"); err != nil {
		t.Fatalf("终态 failed 迁移不应报错: %v", err)
	}
	if err := s.MarkDeliveryAccepted(ctx, "m1", "turn-10"); err != nil {
		t.Fatalf("重复 accepted 不应报错: %v", err)
	}
	if got := getDelivery(t, s, "m1"); got.State != DeliveryAccepted || got.TurnID != "turn-9" || got.Failure != "" {
		t.Fatalf("终态被二次迁移改写: %+v", got)
	}

	// 无回执的 provider 传空 turn_id 时保留原值（这里原值为空）。
	if err := s.MarkDeliveryAccepted(ctx, "m2", ""); err != nil {
		t.Fatalf("MarkDeliveryAccepted 失败: %v", err)
	}
	if got := getDelivery(t, s, "m2"); got.State != DeliveryAccepted || got.TurnID != "" || got.SettledAt == nil {
		t.Fatalf("无回执 accepted 行不对: %+v", got)
	}

	if err := s.InsertDelivery(ctx, Delivery{MessageID: "m3", ThreadID: threadID, CreatedAt: now}); err != nil {
		t.Fatalf("InsertDelivery 失败: %v", err)
	}
	if err := s.MarkDeliveryFailed(ctx, "m3", "provider_start_failed"); err != nil {
		t.Fatalf("MarkDeliveryFailed 失败: %v", err)
	}
	if got := getDelivery(t, s, "m3"); got.State != DeliveryFailed || got.Failure != "provider_start_failed" || got.SettledAt == nil {
		t.Fatalf("failed 行不对: %+v", got)
	}

	if err := s.MarkDeliveryFailed(ctx, "missing", "x"); err != nil {
		t.Fatalf("不存在的行不应报错: %v", err)
	}
}

// TestSettleInterruptedDeliveriesReturnsOnlyOnce dispatching 一次性转 unknown 且只返回一次。
func TestSettleInterruptedDeliveriesReturnsOnlyOnce(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []string{"m1", "m2"} {
		if err := s.InsertDelivery(ctx, Delivery{MessageID: id, ThreadID: threadID, CreatedAt: now}); err != nil {
			t.Fatalf("InsertDelivery 失败: %v", err)
		}
	}
	if err := s.InsertDelivery(ctx, Delivery{
		MessageID: "m3", ThreadID: threadID, State: DeliveryAccepted,
		CreatedAt: now, SettledAt: &now,
	}); err != nil {
		t.Fatalf("InsertDelivery 失败: %v", err)
	}

	interrupted, err := s.SettleInterruptedDeliveries(ctx)
	if err != nil {
		t.Fatalf("SettleInterruptedDeliveries 失败: %v", err)
	}
	if len(interrupted) != 2 {
		t.Fatalf("返回行数 = %d，期望 2", len(interrupted))
	}
	for _, d := range interrupted {
		if d.State != DeliveryUnknown || d.SettledAt == nil {
			t.Fatalf("返回行未被写成 unknown: %+v", d)
		}
	}

	again, err := s.SettleInterruptedDeliveries(ctx)
	if err != nil {
		t.Fatalf("第二次 SettleInterruptedDeliveries 失败: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("第二次返回行数 = %d，期望 0", len(again))
	}

	if got := getDelivery(t, s, "m1"); got.State != DeliveryUnknown || got.SettledAt == nil {
		t.Fatalf("m1 未落库为 unknown: %+v", got)
	}
	if got := getDelivery(t, s, "m3"); got.State != DeliveryAccepted {
		t.Fatalf("已接受行被改写: %+v", got)
	}
}

// TestDeliveryCascadeOnThreadDelete 会话删除后投递行随 threads 级联删除。
func TestDeliveryCascadeOnThreadDelete(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()
	if err := s.InsertDelivery(ctx, Delivery{MessageID: "m1", ThreadID: threadID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("InsertDelivery 失败: %v", err)
	}
	if err := s.DeleteThread(ctx, threadID); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}
	if n := countRows(t, s, "thread_deliveries"); n != 0 {
		t.Fatalf("thread_deliveries 残留 %d 行", n)
	}
}

// TestFollowUpDeliveryEventsRequireSourceID 新投递事件与 delivered/steered 一样强制要求 source_id，
// 否则 event_key 退化，同一 follow-up 的多次投递会被 UNIQUE 静默吞掉。
func TestFollowUpDeliveryEventsRequireSourceID(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, []FollowUp{{ID: "f1", Text: "排队消息"}}, nil); err != nil {
		t.Fatalf("写入队列失败: %v", err)
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, []FollowUp{{ID: "f1", Text: "排队消息"}}, []FollowUpEvent{
		{FollowUpID: "f1", Kind: FollowUpEventDispatching},
	}); err == nil {
		t.Fatal("缺少 source_id 的 dispatching 事件应被拒绝")
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, []FollowUp{{ID: "f1", Text: "排队消息"}}, []FollowUpEvent{
		{FollowUpID: "f1", Kind: FollowUpEventDispatching, SourceID: "msg-1"},
		{FollowUpID: "f1", Kind: FollowUpEventAccepted, SourceID: "msg-1"},
		{FollowUpID: "f1", Kind: FollowUpEventUnknown, SourceID: "msg-2"},
		{FollowUpID: "f1", Kind: FollowUpEventFailed, SourceID: "msg-3"},
	}); err != nil {
		t.Fatalf("写入投递事件失败: %v", err)
	}
	for _, kind := range []string{FollowUpEventDispatching, FollowUpEventAccepted, FollowUpEventUnknown, FollowUpEventFailed} {
		if got := countEvents(listEvents(t, s, threadID), kind, "", "f1"); got != 1 {
			t.Fatalf("%s 事件数 = %d，期望 1", kind, got)
		}
	}
}
