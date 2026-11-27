package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestEnsureMigratedColumnsRepairsAllTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "missing_migrated_columns.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}

	targets := []struct {
		table   string
		column  string
		dropSQL string
	}{
		{table: "tasks", column: "goal_prompt", dropSQL: "ALTER TABLE tasks DROP COLUMN goal_prompt;"},
		{table: "messages", column: "media", dropSQL: "ALTER TABLE messages DROP COLUMN media;"},
		{table: "automation_runs", column: "trigger", dropSQL: `ALTER TABLE automation_runs DROP COLUMN "trigger";`},
	}

	for _, target := range targets {
		if _, err := s.db.ExecContext(ctx, target.dropSQL); err != nil {
			_ = s.Close()
			t.Fatalf("删除 %s.%s 失败: %v", target.table, target.column, err)
		}
	}

	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&version); err != nil {
		_ = s.Close()
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	if version != len(migrations) {
		_ = s.Close()
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

	for _, target := range targets {
		if !tableHasColumn(t, ctx, s, target.table, target.column) {
			t.Fatalf("%s.%s 未恢复", target.table, target.column)
		}
	}
}

func tableHasColumn(t *testing.T, ctx context.Context, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s);", table))
	if err != nil {
		t.Fatalf("读取 %s 列信息失败: %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("扫描 %s 列信息失败: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 %s 列信息失败: %v", table, err)
	}
	return false
}
