package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrSnapshotTooNew = errors.New("store: backup snapshot schema is newer than this build")
var BackupTables = []string{"projects", "tasks", "goals", "goal_tasks", "automations", "app_settings"}

func backupDSN(path, mode string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil { return "", err }
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func backupOpen(path, mode string) (*sql.DB, error) {
	dsn, err := backupDSN(path, mode)
	if err != nil { return nil, err }
	db, err := sql.Open("sqlite", dsn)
	if err != nil { return nil, err }
	db.SetMaxOpenConns(1)
	return db, nil
}

func backupQuote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func SnapshotFile(ctx context.Context, dbPath, destination string, includeHistory bool) (err error) {
	if err = ctx.Err(); err != nil { return fmt.Errorf("snapshot: %w", err) }
	// Never remove or overwrite an existing destination on failure.
	if _, statErr := os.Lstat(destination); statErr == nil { return fmt.Errorf("snapshot destination already exists: %s", destination) } else if !errors.Is(statErr, os.ErrNotExist) { return fmt.Errorf("stat snapshot destination: %w", statErr) }
	created := false
	defer func() { if err != nil && created { _ = os.Remove(destination); _ = os.Remove(destination + "-wal"); _ = os.Remove(destination + "-shm") } }()
	db, err := backupOpen(dbPath, "ro")
	if err != nil { return fmt.Errorf("open snapshot source: %w", err) }
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", destination)
	// VACUUM can create a partial file before reporting an error.
	created = true
	closeErr := db.Close()
	if err != nil { return fmt.Errorf("create snapshot: %w", err) }
	if closeErr != nil { return fmt.Errorf("close snapshot source: %w", closeErr) }
	if includeHistory { return nil }
	db, err = backupOpen(destination, "rw")
	if err != nil { return fmt.Errorf("open snapshot: %w", err) }
	defer func() { if e := db.Close(); err == nil && e != nil { err = fmt.Errorf("close snapshot: %w", e) } }()
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil { return fmt.Errorf("disable snapshot foreign keys: %w", err) }
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil { return fmt.Errorf("list snapshot tables: %w", err) }
	var tables []string
	for rows.Next() { var name string; if err = rows.Scan(&name); err != nil { _ = rows.Close(); return fmt.Errorf("scan snapshot table: %w", err) }; tables = append(tables, name) }
	err = rows.Err(); _ = rows.Close()
	if err != nil { return fmt.Errorf("list snapshot tables: %w", err) }
	keep := make(map[string]bool, len(BackupTables))
	for _, name := range BackupTables { keep[name] = true }
	for _, name := range tables { if !keep[name] { if _, err = db.ExecContext(ctx, "DELETE FROM "+backupQuote(name)); err != nil { return fmt.Errorf("clear snapshot table %s: %w", name, err) } } }
	for _, stmt := range []string{"UPDATE tasks SET thread_id=NULL, review_thread_id=NULL", "UPDATE goals SET plan_thread_id=NULL", "VACUUM"} {
		if _, err = db.ExecContext(ctx, stmt); err != nil { return fmt.Errorf("clean snapshot: %w", err) }
	}
	return nil
}

func PrepareSnapshot(ctx context.Context, path string) error {
	db, err := backupOpen(path, "rw")
	if err != nil { return fmt.Errorf("open snapshot: %w", err) }
	defer db.Close()
	if err := scanQuickCheck(ctx, db); err != nil { return fmt.Errorf("check snapshot: %w", err) }
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil { return fmt.Errorf("read snapshot version: %w", err) }
	if version > len(migrations) { return fmt.Errorf("snapshot version %d > %d: %w", version, len(migrations), ErrSnapshotTooNew) }
	if version < len(migrations) {
		// Reuse the migration logic directly; Open also performs recovery, sweeping and background checks.
		if err := (&Store{db: db}).migrate(ctx); err != nil { return fmt.Errorf("migrate snapshot: %w", err) }
	}
	return nil
}

func SnapshotProjectPaths(ctx context.Context, path string) (map[string]string, error) {
	db, err := backupOpen(path, "ro")
	if err != nil { return nil, fmt.Errorf("open snapshot: %w", err) }
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT id, path FROM projects")
	if err != nil { return nil, fmt.Errorf("query snapshot projects: %w", err) }
	defer rows.Close()
	paths := make(map[string]string)
	for rows.Next() { var id, path string; if err := rows.Scan(&id, &path); err != nil { return nil, fmt.Errorf("scan snapshot project: %w", err) }; paths[id] = path }
	if err := rows.Err(); err != nil { return nil, fmt.Errorf("read snapshot projects: %w", err) }
	return paths, nil
}

func backupColumns(ctx context.Context, conn *sql.Conn, schema, table string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA "+schema+".table_info("+backupQuote(table)+")")
	if err != nil { return nil, err }
	defer rows.Close()
	var columns []string
	for rows.Next() { var cid, notNull, pk int; var name, typ string; var defaultValue sql.NullString; if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil { return nil, err }; columns = append(columns, name) }
	return columns, rows.Err()
}

func backupInsert(ctx context.Context, conn *sql.Conn, table string) error {
	mainCols, err := backupColumns(ctx, conn, "main", table)
	if err != nil { return err }
	incomingCols, err := backupColumns(ctx, conn, "incoming", table)
	if err != nil { return err }
	available := make(map[string]bool, len(incomingCols))
	for _, c := range incomingCols { available[c] = true }
	var cols, values, updates []string
	for _, c := range mainCols {
		if !available[c] { continue }
		q := backupQuote(c)
		cols = append(cols, q)
		value := q
		if (table == "tasks" && (c == "thread_id" || c == "review_thread_id")) || (table == "goals" && c == "plan_thread_id") { value = "NULL" }
		// In-flight tasks return to todo, approval waits return to todo, and running/planning goals pause or fail planning.
		if c == "status" && table == "tasks" { value = "CASE WHEN status IN ('in_progress','needs_approval') THEN 'todo' ELSE status END" }
		if c == "status" && table == "goals" { value = "CASE WHEN status='running' THEN 'paused' WHEN status='planning' THEN 'plan_failed' ELSE status END" }
		values = append(values, value)
		if (table == "projects" && (c == "name" || c == "path" || c == "updated_at")) || (table == "app_settings" && c != "key") || (table == "automations" && c != "id") { updates = append(updates, q+"=excluded."+q) }
	}
	if len(cols) == 0 { return fmt.Errorf("no shared columns for %s", table) }
	query := "INSERT INTO main."+backupQuote(table)+" ("+strings.Join(cols, ",")+") SELECT "+strings.Join(values, ",")+" FROM incoming."+backupQuote(table)+" WHERE true"
	if table == "projects" || table == "automations" || table == "app_settings" {
		key := "id"; if table == "app_settings" { key = "key" }
		if !available[key] || len(updates) == 0 { return fmt.Errorf("missing upsert columns for %s", table) }
		query += " ON CONFLICT("+backupQuote(key)+") DO UPDATE SET "+strings.Join(updates, ",")
	}
	_, err = conn.ExecContext(ctx, query)
	return err
}

func (s *Store) MergeBackup(ctx context.Context, snapshotPath, restoreID string) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil { return fmt.Errorf("get merge connection: %w", err) }
	defer conn.Close()
	// ATTACH uses mode=ro so a missing snapshot cannot create an empty database.
	dsn, err := backupDSN(snapshotPath, "ro")
	if err != nil { return fmt.Errorf("snapshot path: %w", err) }
	if _, err = conn.ExecContext(ctx, "ATTACH DATABASE ? AS incoming", dsn); err != nil { return fmt.Errorf("attach snapshot: %w", err) }
	inTransaction := false
	defer func() {
		cleanup := context.Background()
		if inTransaction { if _, e := conn.ExecContext(cleanup, "ROLLBACK"); e != nil { err = errors.Join(err, fmt.Errorf("rollback merge: %w", e)) } }
		if _, e := conn.ExecContext(cleanup, "DETACH DATABASE incoming"); e != nil { err = errors.Join(err, fmt.Errorf("detach snapshot: %w", e)) }
	}()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil { return fmt.Errorf("begin merge: %w", err) }
	inTransaction = true
	var previous string
	err = conn.QueryRowContext(ctx, "SELECT value FROM main.app_settings WHERE key=?", "backup"+"."+"restore_id").Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { return fmt.Errorf("read restore marker: %w", err) }
	if err == nil && previous == restoreID {
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil { return fmt.Errorf("commit repeated merge: %w", err) }
		inTransaction = false
		return nil
	}
	for _, table := range []string{"projects", "app_settings"} { if err = backupInsert(ctx, conn, table); err != nil { return fmt.Errorf("merge %s: %w", table, err) } }
	for _, table := range []string{"goal_tasks", "tasks", "goals"} { if _, err = conn.ExecContext(ctx, "DELETE FROM main."+backupQuote(table)); err != nil { return fmt.Errorf("clear %s: %w", table, err) } }
	for _, table := range []string{"goals", "tasks", "goal_tasks", "automations"} { if err = backupInsert(ctx, conn, table); err != nil { return fmt.Errorf("merge %s: %w", table, err) } }
	_, err = conn.ExecContext(ctx, "INSERT INTO main.app_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at", "backup"+"."+"restore_id", restoreID, formatTime(time.Now()))
	if err != nil { return fmt.Errorf("write restore marker: %w", err) }
	rows, err := conn.QueryContext(ctx, "PRAGMA main.foreign_key_check")
	if err != nil { return fmt.Errorf("check foreign keys: %w", err) }
	invalid := rows.Next()
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil { return fmt.Errorf("check foreign keys: %w", rowsErr) }
	if closeErr != nil { return fmt.Errorf("close foreign key check: %w", closeErr) }
	if invalid { return errors.New("merge backup: foreign key violation") }
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil { return fmt.Errorf("commit merge: %w", err) }
	inTransaction = false
	return nil
}
