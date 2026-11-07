package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// seedCorruptTestData 建一个项目与若干线程，返回已打开的 store。
func seedCorruptTestData(t *testing.T, threadIDs ...string) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "corrupt_test.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	for _, id := range threadIDs {
		if err := s.CreateThread(ctx, Thread{ID: id, ProjectID: "p1", Title: id, Provider: "memory", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateThread %s 失败: %v", id, err)
		}
	}
	return s
}

// TestListThreadsTolerantSkipsCorruptThread 验证单行线程解码失败不影响其余线程，坏会话 ID 单独返回。
func TestListThreadsTolerantSkipsCorruptThread(t *testing.T) {
	ctx := context.Background()
	s := seedCorruptTestData(t, "t-good", "t-bad")
	defer s.Close()

	if _, err := s.db.ExecContext(ctx, "UPDATE threads SET created_at = 'not-a-time' WHERE id = 't-bad'"); err != nil {
		t.Fatalf("损坏 created_at 失败: %v", err)
	}

	// 严格版本应整体报错
	if _, err := s.ListThreads(ctx, "", true); err == nil {
		t.Fatalf("ListThreads 期望报错，实际成功")
	}

	threads, badIDs, err := s.ListThreadsTolerant(ctx)
	if err != nil {
		t.Fatalf("ListThreadsTolerant 不应报错: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "t-good" {
		t.Fatalf("期望只剩 t-good，实际: %+v", threads)
	}
	if len(badIDs) != 1 || badIDs[0] != "t-bad" {
		t.Fatalf("期望坏会话 ID [t-bad]，实际: %v", badIDs)
	}
}

// TestScanThreadDormantAtDegrade 验证 settled_at 解析失败时降级为 nil 而不是报错。
func TestScanThreadDormantAtDegrade(t *testing.T) {
	ctx := context.Background()
	s := seedCorruptTestData(t, "t1")
	defer s.Close()

	if _, err := s.db.ExecContext(ctx, "UPDATE threads SET is_settled = 1, settled_at = 'garbage' WHERE id = 't1'"); err != nil {
		t.Fatalf("损坏 settled_at 失败: %v", err)
	}

	got, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatalf("settled_at 解析失败不应导致 GetThread 报错: %v", err)
	}
	if got.DormantAt != nil {
		t.Fatalf("期望 DormantAt 降级为 nil，实际: %v", got.DormantAt)
	}
	if !got.IsDormant {
		t.Fatalf("IsDormant 应保持 true")
	}
}

// TestListMessagesTolerantBadMediaAndCreatedAt 验证单条消息的 media / created_at 损坏时降级继续，
// 其余消息、顺序与分页信息保持正常。
func TestListMessagesTolerantBadMediaAndCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := seedCorruptTestData(t, "t1")
	defer s.Close()

	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"m1", "m2", "m3"} {
		if _, err := s.AppendMessage(ctx, Message{
			ID:        id,
			ThreadID:  "t1",
			Role:      "user",
			Kind:      "text",
			Text:      id,
			TurnIndex: 1,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendMessage %s 失败: %v", id, err)
		}
	}
	// m3 的 media 写成非法 JSON；m2 的 created_at 写成非法字符串
	if _, err := s.db.ExecContext(ctx, "UPDATE messages SET media = '{bad json' WHERE id = 'm3'"); err != nil {
		t.Fatalf("损坏 media 失败: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE messages SET created_at = 'not-a-time' WHERE id = 'm2'"); err != nil {
		t.Fatalf("损坏 created_at 失败: %v", err)
	}

	// 严格版本应整体报错
	if _, _, err := s.ListMessages(ctx, "t1", 0, 100); err == nil {
		t.Fatalf("ListMessages 期望报错，实际成功")
	}

	msgs, hasMore, badIDs, err := s.ListMessagesTolerant(ctx, "t1", 0, 100)
	if err != nil {
		t.Fatalf("ListMessagesTolerant 不应报错: %v", err)
	}
	if hasMore {
		t.Fatalf("不应有更多消息")
	}
	if len(msgs) != 3 || msgs[0].ID != "m1" || msgs[1].ID != "m2" || msgs[2].ID != "m3" {
		t.Fatalf("期望按 seq 升序返回 m1/m2/m3，实际: %+v", msgs)
	}
	if msgs[2].Media != nil {
		t.Fatalf("m3 的 media 期望降级为 nil，实际: %+v", msgs[2].Media)
	}
	// m2 的 created_at 回退为同页前一条（seq 更晚）消息 m3 的时间
	if !msgs[1].CreatedAt.Equal(msgs[2].CreatedAt) || msgs[1].CreatedAt.IsZero() {
		t.Fatalf("m2 的 created_at 期望回退为 m3 的时间 %v，实际: %v", msgs[2].CreatedAt, msgs[1].CreatedAt)
	}
	badSet := make(map[string]bool, len(badIDs))
	for _, id := range badIDs {
		badSet[id] = true
	}
	if len(badIDs) != 2 || !badSet["m2"] || !badSet["m3"] {
		t.Fatalf("期望坏消息 ID [m2 m3]，实际: %v", badIDs)
	}
}

// TestQuarantineThreadUpsertAndList 验证隔离记录的 upsert 与列表读取。
func TestQuarantineThreadUpsertAndList(t *testing.T) {
	ctx := context.Background()
	s := seedCorruptTestData(t)
	defer s.Close()

	if err := s.QuarantineThread(ctx, "t1", "message decode failed", []string{"m1", "m2"}); err != nil {
		t.Fatalf("QuarantineThread t1 失败: %v", err)
	}
	if err := s.QuarantineThread(ctx, "t2", "thread row decode failed", nil); err != nil {
		t.Fatalf("QuarantineThread t2 失败: %v", err)
	}
	// 同一会话再次隔离应覆盖旧记录
	if err := s.QuarantineThread(ctx, "t1", "follow ups decode failed", nil); err != nil {
		t.Fatalf("QuarantineThread t1 覆盖失败: %v", err)
	}

	items, err := s.ListQuarantinedThreads(ctx)
	if err != nil {
		t.Fatalf("ListQuarantinedThreads 失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 条隔离记录，实际: %+v", items)
	}
	byID := make(map[string]QuarantinedThread, len(items))
	for _, q := range items {
		byID[q.ThreadID] = q
	}
	if q := byID["t1"]; q.Reason != "follow ups decode failed" || len(q.RawMessageIDs) != 0 || q.QuarantinedAt.IsZero() {
		t.Fatalf("t1 覆盖后的记录不符: %+v", q)
	}
	if q := byID["t2"]; q.Reason != "thread row decode failed" || len(q.RawMessageIDs) != 0 || q.QuarantinedAt.IsZero() {
		t.Fatalf("t2 记录不符: %+v", q)
	}
}
