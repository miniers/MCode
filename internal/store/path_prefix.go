package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pathColumns 列出建表和 ALTER 中保存独立绝对路径的列，不改写正文或 JSON。
var pathColumns = []struct{ table, column string }{
	{"projects", "path"},
	{"threads", "worktree_path"},
	{"hydra_leads", "lead_work_dir"},
	{"memory_proposals", "project_path"},
}

// RewritePathPrefix 在数据目录迁移后，以单个事务改写路径；不执行建表或自愈。
func RewritePathPrefix(dbPath, oldPrefix, newPrefix string) error {
	if !filepath.IsAbs(oldPrefix) || !filepath.IsAbs(newPrefix) {
		return fmt.Errorf("路径前缀必须为绝对路径")
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	oldPrefix, newPrefix = filepath.Clean(oldPrefix), filepath.Clean(newPrefix)
	oldBase := strings.TrimRight(oldPrefix, string(filepath.Separator)) + string(filepath.Separator)
	newBase := strings.TrimRight(newPrefix, string(filepath.Separator)) + string(filepath.Separator)
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	uriPath := filepath.ToSlash(absPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range pathColumns {
		// 旧版本数据库可能还没有后续迁移添加的表或列。
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		query := fmt.Sprintf(`UPDATE %s SET %s = CASE WHEN %s = ? THEN ? ELSE ? || substr(%s, length(?) + 1) END
			WHERE %s = ? OR substr(%s, 1, length(?)) = ?`, c.table, c.column, c.column, c.column, c.column, c.column)
		if _, err := tx.ExecContext(ctx, query, oldPrefix, newPrefix, newBase, oldBase, oldPrefix, oldBase, oldBase); err != nil {
			return fmt.Errorf("改写 %s.%s: %w", c.table, c.column, err)
		}
	}
	return tx.Commit()
}
