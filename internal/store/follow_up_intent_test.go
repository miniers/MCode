package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupFollowUpIntentThread 建好项目与会话，供 Intent 读写用例复用。
func setupFollowUpIntentThread(t *testing.T, s *Store, threadID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p_intent", Name: "Intent Project", Path: "/tmp/intent", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: threadID, ProjectID: "p_intent", Title: "Intent Thread", Provider: "mock", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
}

// FollowUp.Intent 应随排队项一起落库并按原值读回，空值保持空字符串。
func TestFollowUpIntentRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "follow_up_intent.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	setupFollowUpIntentThread(t, s, "t_intent")
	now := time.Now().UTC()
	items := []FollowUp{
		{ID: "f_insert", ThreadID: "t_intent", Text: "插入降级项", Intent: "insert", CreatedAt: now, UpdatedAt: now},
		{ID: "f_queue", ThreadID: "t_intent", Text: "普通排队项", Intent: "queue", CreatedAt: now, UpdatedAt: now},
		{ID: "f_empty", ThreadID: "t_intent", Text: "未写入意图", CreatedAt: now, UpdatedAt: now},
	}
	if err := s.ReplaceFollowUps(ctx, "t_intent", 1, items); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}

	list, err := s.ListFollowUps(ctx, "t_intent")
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("期望 3 条 follow-up，实际 %d 条", len(list))
	}
	if list[0].Intent != "insert" {
		t.Fatalf("期望 f_insert 的 Intent 为 insert，实际 %q", list[0].Intent)
	}
	if list[1].Intent != "queue" {
		t.Fatalf("期望 f_queue 的 Intent 为 queue，实际 %q", list[1].Intent)
	}
	if list[2].Intent != "" {
		t.Fatalf("期望 f_empty 的 Intent 为空字符串，实际 %q", list[2].Intent)
	}
}

// 旧库没有 intent 列时，迁移应补上该列并让存量跟进项读到空 Intent。
func TestFollowUpIntentMigrationDefaultsEmpty(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "follow_up_intent_migration.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	setupFollowUpIntentThread(t, s, "t_old_intent")
	// 先写入一条旧排队项（此时已有 intent 列，值为空）。
	now := time.Now().UTC()
	if err := s.ReplaceFollowUps(ctx, "t_old_intent", 1, []FollowUp{
		{ID: "f_old_intent", ThreadID: "t_old_intent", Text: "旧库排队项", CreatedAt: now, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceFollowUps 失败: %v", err)
	}
	// 模拟升级前的旧库：去掉 intent 列并把 user_version 回退到补 intent 列的那条迁移之前，
	// 让 Open 从该迁移开始重跑。
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE thread_follow_ups DROP COLUMN intent;"); err != nil {
		t.Fatalf("模拟旧库删除 intent 列失败: %v", err)
	}
	intentMigrationIndex := -1
	for i, migration := range migrations {
		if strings.Contains(migration, "ALTER TABLE thread_follow_ups ADD COLUMN intent") {
			intentMigrationIndex = i
			break
		}
	}
	if intentMigrationIndex < 0 {
		t.Fatalf("migrations 中找不到补 intent 列的迁移")
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", intentMigrationIndex)); err != nil {
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

	list, err := reopened.ListFollowUps(ctx, "t_old_intent")
	if err != nil {
		t.Fatalf("升级后 ListFollowUps 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 条旧排队项，实际 %d 条", len(list))
	}
	if list[0].Intent != "" {
		t.Fatalf("旧库迁移后 Intent 应为空字符串，实际 %q", list[0].Intent)
	}
}
