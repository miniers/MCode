package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestRewritePathPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paths.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := filepath.Join(t.TempDir(), "a", "b%_目录")
	newDir := filepath.Join(t.TempDir(), "新目录")
	values := []any{old, filepath.Join(old, "worktrees", "bee"), old + "c", "before" + old, "relative/path", "", nil}
	wants := []any{newDir, filepath.Join(newDir, "worktrees", "bee"), old + "c", "before" + old, "relative/path", "", nil}
	for _, c := range pathColumns {
		if _, err := db.Exec(fmt.Sprintf("CREATE TABLE %s (%s TEXT)", c.table, c.column)); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if _, err := db.Exec(fmt.Sprintf("INSERT INTO %s (%s) VALUES (?)", c.table, c.column), value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := RewritePathPrefix(path, old, newDir); err != nil {
		t.Fatal(err)
	}
	for _, c := range pathColumns {
		for i, want := range wants {
			var got any
			if err := db.QueryRow(fmt.Sprintf("SELECT %s FROM %s WHERE rowid = ?", c.column, c.table), i+1).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s.%s[%d] = %v，期望 %v", c.table, c.column, i, got, want)
			}
		}
	}
}

func TestRewritePathPrefixRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paths.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old, newDir := t.TempDir(), t.TempDir()
	for _, query := range []string{
		`CREATE TABLE projects (path TEXT)`,
		`CREATE TABLE threads (worktree_path TEXT)`,
		`CREATE TRIGGER fail_rewrite BEFORE UPDATE ON threads BEGIN SELECT RAISE(ABORT, 'test failure'); END`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{`INSERT INTO projects VALUES (?)`, `INSERT INTO threads VALUES (?)`} {
		if _, err := db.Exec(query, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := RewritePathPrefix(path, old, newDir); err == nil {
		t.Fatal("预期事务失败")
	}
	var got string
	if err := db.QueryRow(`SELECT path FROM projects`).Scan(&got); err != nil || got != old {
		t.Fatalf("未完整回滚: %q, %v", got, err)
	}
}

func TestRewritePathPrefixOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old, newDir := t.TempDir(), t.TempDir()
	if _, err := db.Exec(`CREATE TABLE projects (path TEXT); CREATE TABLE threads (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects VALUES (?)`, old); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := RewritePathPrefix(path, old, newDir); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT path FROM projects`).Scan(&got); err != nil || got != newDir {
		t.Fatalf("旧库改写失败: %q, %v", got, err)
	}
}
