package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// newAskDeliveryTestStore 建一个带项目和线程的临时库，供 pending_asks 与 thread_deliveries 测试共用。
func newAskDeliveryTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	testenv.TempHome(t)
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "asks_deliveries.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{
		ID: "t1", ProjectID: "p1", Title: "提问与投递", Provider: "test",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return s, "t1"
}

// getPendingAsk 按 request_id 读一行，读不到直接终止测试。
func getPendingAsk(t *testing.T, s *Store, requestID string) PendingAsk {
	t.Helper()
	ask, err := scanPendingAsk(s.db.QueryRowContext(context.Background(),
		"SELECT "+pendingAskSelectCols+" FROM pending_asks WHERE request_id = ?", requestID))
	if err != nil {
		t.Fatalf("读取 pending_ask %s 失败: %v", requestID, err)
	}
	return ask
}

// countRows 返回表内行数，用于断言级联删除。
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return n
}

// TestPendingAskAndDeliveryMigrationsIdempotent 重复 Open 同一个库不应重复建表或报错，
// 两个迁移都应落在迁移数组末尾并推进 user_version。
func TestPendingAskAndDeliveryMigrationsIdempotent(t *testing.T) {
	testenv.TempHome(t)
	path := filepath.Join(t.TempDir(), "migrations.db")
	for attempt := 0; attempt < 3; attempt++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次 Open 失败: %v", attempt+1, err)
		}
		var version int
		if err := s.db.QueryRowContext(context.Background(), "PRAGMA user_version;").Scan(&version); err != nil {
			t.Fatalf("读取 user_version 失败: %v", err)
		}
		if version != len(migrations) {
			t.Fatalf("user_version = %d，期望 %d", version, len(migrations))
		}
		var objects int
		if err := s.db.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM sqlite_master WHERE name IN
				('pending_asks', 'thread_deliveries', 'idx_pending_asks_thread', 'idx_thread_deliveries_state')
		`).Scan(&objects); err != nil {
			t.Fatalf("检查建表结果失败: %v", err)
		}
		if objects != 4 {
			t.Fatalf("表/索引对象数 = %d，期望 4", objects)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	}
}

// TestExpirePendingAsksReturnsOnlyOnce 启动对账只能返回一次被转换的行，已终态的行不受影响。
func TestExpirePendingAsksReturnsOnlyOnce(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.InsertPendingAsk(ctx, PendingAsk{
		RequestID: "r1", ThreadID: threadID, TurnID: "turn-1", Kind: "approval", Title: "批准命令", CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertPendingAsk 失败: %v", err)
	}
	if err := s.InsertPendingAsk(ctx, PendingAsk{
		RequestID: "r2", ThreadID: threadID, Kind: "question", Title: "选择方案", CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertPendingAsk 失败: %v", err)
	}
	settledAt := now.Add(-time.Minute)
	if err := s.InsertPendingAsk(ctx, PendingAsk{
		RequestID: "r3", ThreadID: threadID, Kind: "approval", State: PendingAskSettled,
		Resolution: "allow", CreatedAt: now, SettledAt: &settledAt,
	}); err != nil {
		t.Fatalf("InsertPendingAsk 失败: %v", err)
	}

	expired, err := s.ExpirePendingAsks(ctx, "host_restarted")
	if err != nil {
		t.Fatalf("ExpirePendingAsks 失败: %v", err)
	}
	if len(expired) != 2 {
		t.Fatalf("返回行数 = %d，期望 2", len(expired))
	}
	for _, ask := range expired {
		if ask.State != PendingAskExpired || ask.Resolution != "host_restarted" || ask.SettledAt == nil {
			t.Fatalf("返回行未被写成 expired: %+v", ask)
		}
	}

	again, err := s.ExpirePendingAsks(ctx, "host_restarted")
	if err != nil {
		t.Fatalf("第二次 ExpirePendingAsks 失败: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("第二次返回行数 = %d，期望 0", len(again))
	}

	if got := getPendingAsk(t, s, "r1"); got.State != PendingAskExpired || got.SettledAt == nil {
		t.Fatalf("r1 未落库为 expired: %+v", got)
	}
	if got := getPendingAsk(t, s, "r3"); got.State != PendingAskSettled || got.Resolution != "allow" || got.SettledAt == nil {
		t.Fatalf("已结算行被改写: %+v", got)
	}
}

// TestSettlePendingAskIdempotentOnTerminalRow 只有 pending 行能被结算，终态之后再结算不生效也不报错。
func TestSettlePendingAskIdempotentOnTerminalRow(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()

	if err := s.InsertPendingAsk(ctx, PendingAsk{
		RequestID: "r1", ThreadID: threadID, Kind: "approval", Title: "批准命令", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertPendingAsk 失败: %v", err)
	}
	if err := s.SettlePendingAsk(ctx, "r1", PendingAskSettled, "allow"); err != nil {
		t.Fatalf("首次结算失败: %v", err)
	}
	if got := getPendingAsk(t, s, "r1"); got.State != PendingAskSettled || got.Resolution != "allow" || got.SettledAt == nil {
		t.Fatalf("首次结算结果不对: %+v", got)
	}

	if err := s.SettlePendingAsk(ctx, "r1", PendingAskWithdrawn, "archive"); err != nil {
		t.Fatalf("终态再次结算不应报错: %v", err)
	}
	if got := getPendingAsk(t, s, "r1"); got.State != PendingAskSettled || got.Resolution != "allow" {
		t.Fatalf("终态被二次结算改写: %+v", got)
	}
	if err := s.SettlePendingAsk(ctx, "missing", PendingAskSettled, "answered"); err != nil {
		t.Fatalf("不存在的行不应报错: %v", err)
	}
	if err := s.SettlePendingAsk(ctx, "r1", PendingAskPending, ""); err == nil {
		t.Fatal("非终态 state 应被拒绝")
	}
	expired, err := s.ExpirePendingAsks(ctx, "host_restarted")
	if err != nil {
		t.Fatalf("ExpirePendingAsks 失败: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("已结算行不应再被过期: %+v", expired)
	}
}

func TestSettlePendingAskChanged(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()
	if err := s.InsertPendingAsk(ctx, PendingAsk{RequestID: "r1", ThreadID: threadID, Kind: "question"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"r1", true}, {"r1", false}, {"missing", false}} {
		changed, err := s.SettlePendingAskChanged(ctx, tc.id, PendingAskExpired, "caller_cancelled")
		if err != nil || changed != tc.want {
			t.Fatalf("结算 %s: changed=%v, err=%v，期望 %v", tc.id, changed, err, tc.want)
		}
	}
}

// TestPendingAskCascadeOnThreadDelete 会话删除后留痕行随 threads 级联删除，避免指向不存在会话。
func TestPendingAskCascadeOnThreadDelete(t *testing.T) {
	s, threadID := newAskDeliveryTestStore(t)
	ctx := context.Background()
	if err := s.InsertPendingAsk(ctx, PendingAsk{
		RequestID: "r1", ThreadID: threadID, Kind: "question", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertPendingAsk 失败: %v", err)
	}
	if err := s.DeleteThread(ctx, threadID); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}
	if n := countRows(t, s, "pending_asks"); n != 0 {
		t.Fatalf("pending_asks 残留 %d 行", n)
	}
}
