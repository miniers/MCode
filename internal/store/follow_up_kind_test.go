package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupFollowUpKindThread 建好项目与会话，供 Kind 读写用例复用。
func setupFollowUpKindThread(t *testing.T, s *Store, threadID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p_kind", Name: "Kind Project", Path: "/tmp/kind", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: threadID, ProjectID: "p_kind", Title: "Kind Thread", Provider: "mock", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
}

// FollowUp.Kind 应随排队项一起落库并按原值读回，空值保持空字符串。
func TestFollowUpKindRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "follow_up_kind.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	setupFollowUpKindThread(t, s, "t_kind")
	now := time.Now().UTC()
	items := []FollowUp{
		{ID: "f_host", ThreadID: "t_kind", Text: "宿主事件", Kind: "host_event", CreatedAt: now, UpdatedAt: now},
		{ID: "f_user", ThreadID: "t_kind", Text: "普通消息", CreatedAt: now, UpdatedAt: now},
	}
	if err := s.ReplaceFollowUps(ctx, "t_kind", 1, items); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}

	list, err := s.ListFollowUps(ctx, "t_kind")
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望 2 条 follow-up，实际 %d 条", len(list))
	}
	if list[0].Kind != "host_event" {
		t.Fatalf("期望 f_host 的 Kind 为 host_event，实际 %q", list[0].Kind)
	}
	if list[1].Kind != "" {
		t.Fatalf("期望 f_user 的 Kind 为空字符串，实际 %q", list[1].Kind)
	}
}

// 旧库没有 kind 列时，迁移应补上该列并让存量跟进项读到空 Kind。
func TestFollowUpKindMigrationDefaultsEmpty(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "follow_up_kind_migration.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	setupFollowUpKindThread(t, s, "t_old")
	// 先写入一条旧排队项（此时已有 kind 列，值为空）。
	now := time.Now().UTC()
	if err := s.ReplaceFollowUps(ctx, "t_old", 1, []FollowUp{
		{ID: "f_old", ThreadID: "t_old", Text: "旧库排队项", CreatedAt: now, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}
	// 模拟升级前的旧库：去掉 kind 列并把 user_version 回退到补 kind 列的那条迁移之前，
	// 让 Open 从该迁移开始重跑。
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE thread_follow_ups DROP COLUMN kind;"); err != nil {
		t.Fatalf("模拟旧库删除 kind 列失败: %v", err)
	}
	kindMigrationIndex := -1
	for i, migration := range migrations {
		if strings.Contains(migration, "ALTER TABLE thread_follow_ups ADD COLUMN kind") {
			kindMigrationIndex = i
			break
		}
	}
	if kindMigrationIndex < 0 {
		t.Fatalf("migrations 中找不到补 kind 列的迁移")
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", kindMigrationIndex)); err != nil {
		t.Fatalf("回退 user_version 失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("升级旧库失败: %v", err)
	}
	defer reopened.Close()

	list, err := reopened.ListFollowUps(ctx, "t_old")
	if err != nil {
		t.Fatalf("升级后 ListFollowUps 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 条旧排队项，实际 %d 条", len(list))
	}
	if list[0].Kind != "" {
		t.Fatalf("旧库迁移后 Kind 应为空字符串，实际 %q", list[0].Kind)
	}
}
