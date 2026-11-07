package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestOpenRepairsMissingThreadBeeTiersAtLatestVersion(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "missing_head_tiers.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	if _, err := s.db.ExecContext(ctx, "ALTER TABLE threads DROP COLUMN head_tiers;"); err != nil {
		t.Fatalf("删除 head_tiers 失败: %v", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&version); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("期望数据库版本 %d，实际为 %d", len(migrations), version)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	s, err = Open(dbPath)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	defer s.Close()
	var column string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM pragma_table_info('threads') WHERE name = 'head_tiers';").Scan(&column); err != nil {
		t.Fatalf("head_tiers 未恢复: %v", err)
	}
	if column != "head_tiers" {
		t.Fatalf("期望 head_tiers 列，实际为 %q", column)
	}

	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/tmp/p1"}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	thread := Thread{
		ID:        "repaired-thread",
		ProjectID: "p1",
		Title:     "迁移修复后的会话",
		Provider:  "codex",
		BeeTiers:  `{"edit":{"model":"gpt-4o","provider":"codex"}}`,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("修复后 CreateThread 失败: %v", err)
	}
	loaded, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if loaded.BeeTiers != thread.BeeTiers {
		t.Fatalf("期望 BeeTiers %q，实际为 %q", thread.BeeTiers, loaded.BeeTiers)
	}
}

// TestMigrateAddsMCPDisabledToLegacyThreads 验证旧库（无 mcp_disabled 列且带存量数据）迁移后不报错，
// 老行 MCPDisabled 为 nil。
func TestMigrateAddsMCPDisabledToLegacyThreads(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy_mcp_disabled.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/tmp/p1"}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: "legacy-thread", ProjectID: "p1", Title: "旧库会话", Provider: "codex"}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}

	// 回退成引入 mcp_disabled 之前的旧库：删掉新列并把版本号减一。
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE threads DROP COLUMN mcp_disabled;"); err != nil {
		t.Fatalf("删除 mcp_disabled 失败: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", len(migrations)-1)); err != nil {
		t.Fatalf("回退 user_version 失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	s, err = Open(dbPath)
	if err != nil {
		t.Fatalf("旧库迁移后重新 Open 失败: %v", err)
	}
	defer s.Close()

	loaded, err := s.GetThread(ctx, "legacy-thread")
	if err != nil {
		t.Fatalf("迁移后 GetThread 失败: %v", err)
	}
	if loaded.MCPDisabled != nil {
		t.Fatalf("期望老行 MCPDisabled 为 nil，实际为 %v", loaded.MCPDisabled)
	}
}
