package remote

import (
	"fmt"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

func TestActionLedgerRecordAndGet(t *testing.T) {
	currentTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ledger := newActionLedger(func() time.Time {
		return currentTime
	})

	actionID := "00000000-0000-0000-0000-000000000001"
	threadID := "11111111-1111-1111-1111-111111111111"
	ledger.record(wire.ActionStatusResult{
		ActionID: actionID,
		State:    wire.ActionStateAccepted,
		ThreadID: &threadID,
	})

	res, ok := ledger.get(actionID)
	if !ok {
		t.Fatalf("expected entry to exist")
	}
	if res.ActionID != actionID || res.State != wire.ActionStateAccepted || res.ThreadID == nil || *res.ThreadID != threadID {
		t.Errorf("unexpected entry result: %+v", res)
	}

	// 查不存在的 actionID
	_, ok = ledger.get("non-existent-action-id")
	if ok {
		t.Errorf("expected non-existent entry to return false")
	}
}

func TestActionLedgerOverwrite(t *testing.T) {
	currentTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ledger := newActionLedger(func() time.Time {
		return currentTime
	})

	actionID := "00000000-0000-0000-0000-000000000001"
	threadID := "11111111-1111-1111-1111-111111111111"
	messageID := "22222222-2222-2222-2222-222222222222"

	// 首次记录为 pending
	ledger.record(wire.ActionStatusResult{
		ActionID: actionID,
		State:    wire.ActionStatePending,
		ThreadID: &threadID,
	})
	res1, ok1 := ledger.get(actionID)
	if !ok1 || res1.State != wire.ActionStatePending {
		t.Fatalf("expected pending state, got %+v", res1)
	}

	// 覆盖写入为 accepted
	ledger.record(wire.ActionStatusResult{
		ActionID:  actionID,
		State:     wire.ActionStateAccepted,
		ThreadID:  &threadID,
		MessageID: &messageID,
	})
	res2, ok2 := ledger.get(actionID)
	if !ok2 || res2.State != wire.ActionStateAccepted || res2.MessageID == nil || *res2.MessageID != messageID {
		t.Fatalf("expected accepted state with messageID, got %+v", res2)
	}

	// 账本条目数仍应为 1
	if len(ledger.entries) != 1 || len(ledger.order) != 1 {
		t.Errorf("expected 1 entry, entries=%d, order=%d", len(ledger.entries), len(ledger.order))
	}
}

func TestActionLedgerCapacityEviction(t *testing.T) {
	currentTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ledger := newActionLedger(func() time.Time {
		return currentTime
	})

	// 插入 256 条数据
	for i := 0; i < 256; i++ {
		ledger.record(wire.ActionStatusResult{
			ActionID: fmt.Sprintf("action-%03d", i),
			State:    wire.ActionStateAccepted,
		})
	}

	if len(ledger.entries) != 256 {
		t.Fatalf("expected 256 entries, got %d", len(ledger.entries))
	}

	// 再插入 2 条数据，最旧的 action-000 与 action-001 应当被淘汰
	ledger.record(wire.ActionStatusResult{
		ActionID: "action-256",
		State:    wire.ActionStateAccepted,
	})
	ledger.record(wire.ActionStatusResult{
		ActionID: "action-257",
		State:    wire.ActionStateAccepted,
	})

	if len(ledger.entries) != 256 {
		t.Fatalf("expected capacity to remain 256, got %d", len(ledger.entries))
	}

	if _, ok := ledger.get("action-000"); ok {
		t.Errorf("action-000 should have been evicted")
	}
	if _, ok := ledger.get("action-001"); ok {
		t.Errorf("action-001 should have been evicted")
	}
	if _, ok := ledger.get("action-002"); !ok {
		t.Errorf("action-002 should still exist")
	}
	if _, ok := ledger.get("action-256"); !ok {
		t.Errorf("action-256 should exist")
	}
	if _, ok := ledger.get("action-257"); !ok {
		t.Errorf("action-257 should exist")
	}
}

func TestActionLedgerTTL(t *testing.T) {
	currentTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ledger := newActionLedger(func() time.Time {
		return currentTime
	})

	actionID := "00000000-0000-0000-0000-000000000001"
	ledger.record(wire.ActionStatusResult{
		ActionID: actionID,
		State:    wire.ActionStateAccepted,
	})

	// 9 分钟后查询，尚未过期
	currentTime = currentTime.Add(9 * time.Minute)
	if _, ok := ledger.get(actionID); !ok {
		t.Errorf("expected entry to exist at 9 minutes")
	}

	// 超过 10 分钟后查询，应判定为过期并返回 false
	currentTime = currentTime.Add(1*time.Minute + time.Second)
	if _, ok := ledger.get(actionID); ok {
		t.Errorf("expected entry to expire after 10 minutes")
	}
}
