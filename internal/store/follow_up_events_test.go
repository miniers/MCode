package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// newFollowUpTestStore 建一个带项目和线程的临时库，供队列审计测试共用。
func newFollowUpTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "follow_up_events.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{
		ID: "t1", ProjectID: "p1", Title: "审计", Provider: "anthropic",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return s, "t1"
}

func listEvents(t *testing.T, s *Store, threadID string) []FollowUpEvent {
	t.Helper()
	events, err := s.ListFollowUpEvents(context.Background(), threadID, 0)
	if err != nil {
		t.Fatalf("ListFollowUpEvents 失败: %v", err)
	}
	return events
}

func countEvents(events []FollowUpEvent, kind, reason, followUpID string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind != kind || ev.Reason != reason {
			continue
		}
		if followUpID != "" && ev.FollowUpID != followUpID {
			continue
		}
		n++
	}
	return n
}

func TestReplaceFollowUpsAuditDerivesQueuedDroppedEdited(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, []FollowUp{
		{ID: "f1", Text: "第一条"},
		{ID: "f2", Text: "第二条"},
	}, nil); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	events := listEvents(t, s, threadID)
	if got := countEvents(events, FollowUpEventQueued, "", ""); got != 2 {
		t.Fatalf("queued 事件数 = %d，期望 2: %+v", got, events)
	}
	if got := countEvents(events, FollowUpEventDropped, FollowUpDropVanished, ""); got != 0 {
		t.Fatalf("首次写入不应出现 vanished: %+v", events)
	}

	// f1 改文、f2 消失。
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, []FollowUp{
		{ID: "f1", Text: "第一条（改）"},
	}, nil); err != nil {
		t.Fatalf("第二次写入失败: %v", err)
	}
	events = listEvents(t, s, threadID)
	if got := countEvents(events, FollowUpEventEdited, "", "f1"); got != 1 {
		t.Fatalf("f1 edited 事件数 = %d，期望 1: %+v", got, events)
	}
	if got := countEvents(events, FollowUpEventDropped, FollowUpDropVanished, "f2"); got != 1 {
		t.Fatalf("f2 vanished 事件数 = %d，期望 1: %+v", got, events)
	}

	// 清空队列，f1 也应留下 vanished。
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 3, nil, nil); err != nil {
		t.Fatalf("清空写入失败: %v", err)
	}
	events = listEvents(t, s, threadID)
	if got := countEvents(events, FollowUpEventDropped, FollowUpDropVanished, "f1"); got != 1 {
		t.Fatalf("f1 vanished 事件数 = %d，期望 1: %+v", got, events)
	}
}

func TestReplaceFollowUpsAuditExplicitDeliveredSuppressesVanished(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, []FollowUp{{ID: "f1", Text: "投递"}}, nil); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, nil, []FollowUpEvent{
		{FollowUpID: "f1", Kind: FollowUpEventDelivered, SourceID: "turn-1"},
	}); err != nil {
		t.Fatalf("显式投递失败: %v", err)
	}

	events := listEvents(t, s, threadID)
	if got := countEvents(events, FollowUpEventDelivered, "", "f1"); got != 1 {
		t.Fatalf("delivered 事件数 = %d，期望 1: %+v", got, events)
	}
	if got := countEvents(events, FollowUpEventDropped, "", "f1"); got != 0 {
		t.Fatalf("显式投递后不应补 dropped: %+v", events)
	}
}

func TestFollowUpEventKeyIsDeterministic(t *testing.T) {
	base := followUpEventKey(FollowUpEventDelivered, "", "t1", "f1", "turn-1")
	if again := followUpEventKey(FollowUpEventDelivered, "", "t1", "f1", "turn-1"); again != base {
		t.Fatalf("同一逻辑事件的键不一致: %q vs %q", base, again)
	}
	sources := []string{
		followUpEventKey(FollowUpEventQueued, "", "t1", "f1", "enqueue"),
		followUpEventKey(FollowUpEventDelivered, "", "t1", "f1", "turn-1"),
		followUpEventKey(FollowUpEventDropped, FollowUpDropUserRemove, "t1", "f1", "f1"),
	}
	seen := map[string]bool{}
	for _, key := range sources {
		if seen[key] {
			t.Fatalf("不同触发来源生成了相同键: %q", key)
		}
		seen[key] = true
	}
}

func TestReplaceFollowUpsAuditReplayedDeliveryIsIdempotent(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, []FollowUp{{ID: "f1", Text: "投递"}}, nil); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	delivered := []FollowUpEvent{{FollowUpID: "f1", Kind: FollowUpEventDelivered, SourceID: "turn-1"}}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, nil, delivered); err != nil {
		t.Fatalf("首次投递失败: %v", err)
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 3, nil, delivered); err != nil {
		t.Fatalf("重放投递失败: %v", err)
	}

	events := listEvents(t, s, threadID)
	if got := countEvents(events, FollowUpEventDelivered, "", "f1"); got != 1 {
		t.Fatalf("重复投递后 delivered 事件数 = %d，期望 1: %+v", got, events)
	}
}

func TestReplaceFollowUpsAuditRetryDoesNotDuplicateDerivedEvents(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, []FollowUp{{ID: "f1", Text: "A"}}, nil); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, []FollowUp{{ID: "f1", Text: "B"}}, nil); err != nil {
		t.Fatalf("改文失败: %v", err)
	}
	delivered := []FollowUpEvent{{FollowUpID: "f1", Kind: FollowUpEventDelivered, SourceID: "turn-1"}}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 3, nil, delivered); err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	before := listEvents(t, s, threadID)
	// 同一逻辑快照与事件再提交一次，模拟 revision 冲突后的重试。
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 4, nil, delivered); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	after := listEvents(t, s, threadID)

	for _, kind := range []string{FollowUpEventQueued, FollowUpEventEdited, FollowUpEventDelivered} {
		if got, want := countEvents(after, kind, "", ""), countEvents(before, kind, "", ""); got != want {
			t.Fatalf("重试后 %s 事件数 = %d，期望保持 %d: %+v", kind, got, want, after)
		}
	}
}

func TestFollowUpEventsCascadeOnThreadDelete(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, []FollowUp{{ID: "f1", Text: "会被级联"}}, nil); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if err := s.DeleteThread(ctx, threadID); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}

	events := listEvents(t, s, threadID)
	if len(events) != 0 {
		t.Fatalf("删除线程后事件 = %d 条，期望 0: %+v", len(events), events)
	}
}

func TestPruneFollowUpEvents(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	oldCreated := time.Now().UTC().Add(-31 * 24 * time.Hour)
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, nil, []FollowUpEvent{
		{FollowUpID: "old", Kind: FollowUpEventQueued, CreatedAt: oldCreated},
	}); err != nil {
		t.Fatalf("写入旧事件失败: %v", err)
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, nil, []FollowUpEvent{
		{FollowUpID: "fresh", Kind: FollowUpEventQueued},
	}); err != nil {
		t.Fatalf("写入新事件失败: %v", err)
	}

	n, err := s.PruneFollowUpEvents(ctx, time.Now().UTC().Add(-followUpEventTestRetention))
	if err != nil {
		t.Fatalf("PruneFollowUpEvents 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("清理条数 = %d，期望 1", n)
	}
	events := listEvents(t, s, threadID)
	if len(events) != 1 || events[0].FollowUpID != "fresh" {
		t.Fatalf("清理后剩余事件不符合预期: %+v", events)
	}
}

// followUpEventTestRetention 与 remote 的 30 天保留期保持一致；store 包不依赖 remote。
const followUpEventTestRetention = 30 * 24 * time.Hour

func TestReplaceFollowUpsWithoutAuditStillWritesQueue(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	if err := s.ReplaceFollowUps(ctx, threadID, 7, []FollowUp{
		{ID: "f1", Text: "第一"},
		{ID: "f2", Text: "第二"},
	}); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}
	items, err := s.ListFollowUps(ctx, threadID)
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(items) != 2 || items[0].ID != "f1" || items[1].ID != "f2" {
		t.Fatalf("队列快照不符合预期: %+v", items)
	}
	thread, err := s.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if thread.FollowUpRevision != 7 {
		t.Fatalf("follow_up_revision = %d，期望 7", thread.FollowUpRevision)
	}
}

func TestFollowUpEventsMigrationFromOldDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old_follow_up_events.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db}
	for i := 0; i < len(migrations)-1; i++ {
		if err := legacy.applyMigration(ctx, migrations[i], i+1); err != nil {
			t.Fatalf("应用旧迁移 %d 失败: %v", i+1, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, path, created_at, updated_at) VALUES ('p1', 'P1', '/tmp/p1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("写入旧项目失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO threads (id, project_id, title, provider, created_at, updated_at) VALUES ('t1', 'p1', '旧线程', 'anthropic', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("写入旧线程失败: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("升级打开失败: %v", err)
	}
	defer s.Close()

	var tableCount int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='thread_follow_up_events';").Scan(&tableCount); err != nil {
		t.Fatalf("查询事件表失败: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("升级后事件表不存在")
	}
	if err := s.ReplaceFollowUpsWithAudit(ctx, "t1", 1, []FollowUp{{ID: "f1", Text: "升级后可写"}}, nil); err != nil {
		t.Fatalf("升级后写入失败: %v", err)
	}
	if got := countEvents(listEvents(t, s, "t1"), FollowUpEventQueued, "", "f1"); got != 1 {
		t.Fatalf("升级后 queued 事件数 = %d，期望 1", got)
	}
}

// TestReplaceFollowUpsAuditUnchangedSnapshotAddsNoEvents 验证同一内容重复提交不会误报 edited/vanished；
// 空附件元数据在落库后读回为 nil，签名必须对 nil 与空切片给出一致结果。
func TestReplaceFollowUpsAuditUnchangedSnapshotAddsNoEvents(t *testing.T) {
	s, threadID := newFollowUpTestStore(t)
	ctx := context.Background()

	items := []FollowUp{{
		ID:             "f1",
		Text:           "内容不变",
		Attachments:    []string{},
		AttachmentMeta: []FollowUpAttachment{},
	}}
	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 1, items, nil); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	before := listEvents(t, s, threadID)

	if err := s.ReplaceFollowUpsWithAudit(ctx, threadID, 2, items, nil); err != nil {
		t.Fatalf("重复写入失败: %v", err)
	}
	after := listEvents(t, s, threadID)

	if len(after) != len(before) {
		t.Fatalf("重复写入后事件数 = %d，期望保持 %d: %+v", len(after), len(before), after)
	}
	if got := countEvents(after, FollowUpEventEdited, "", "f1"); got != 0 {
		t.Fatalf("内容未变却产生 edited 事件: %+v", after)
	}
	if got := countEvents(after, FollowUpEventDropped, FollowUpDropVanished, "f1"); got != 0 {
		t.Fatalf("内容未变却产生 vanished 事件: %+v", after)
	}
}
