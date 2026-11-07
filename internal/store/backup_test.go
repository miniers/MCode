package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func backupTestOpen(t *testing.T, name string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func backupTestExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil { t.Fatalf("%s: %v", query, err) }
}

func backupTestString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil { t.Fatalf("%s: %v", query, err) }
	return value
}

func backupTestSeed(t *testing.T, s *Store) {
	t.Helper()
	backupTestExec(t, s.db, `INSERT INTO projects(id,name,path,created_at,updated_at) VALUES('shared','source','/source','now','now')`)
	backupTestExec(t, s.db, `INSERT INTO threads(id,project_id,title,provider,created_at,updated_at) VALUES('source-thread','shared','thread','test','now','now')`)
	backupTestExec(t, s.db, `INSERT INTO messages(id,thread_id,seq,role,kind,text,turn_index,created_at) VALUES('source-message','source-thread',1,'user','text','secret history',0,'now')`)
	backupTestExec(t, s.db, `INSERT INTO goals(id,project_id,title,prompt,status,plan_thread_id,created_at,updated_at) VALUES('goal','shared','goal','prompt','running','source-thread','now','now')`)
	backupTestExec(t, s.db, `INSERT INTO tasks(id,project_id,goal_id,title,status,thread_id,review_thread_id,created_at,updated_at) VALUES('task','shared','goal','task','in_progress','source-thread','source-thread','now','now')`)
	backupTestExec(t, s.db, `INSERT INTO goal_tasks(goal_id,task_id) VALUES('goal','task')`)
	backupTestExec(t, s.db, `INSERT INTO automations(id,project_id,name,prompt,schedule,created_at,updated_at) VALUES('source-auto','shared','auto','prompt','daily','now','now')`)
	backupTestExec(t, s.db, `INSERT INTO automation_runs(id,automation_id,status,started_at) VALUES('source-run','source-auto','done','now')`)
	backupTestExec(t, s.db, `INSERT INTO app_settings(key,value,updated_at) VALUES('source-setting','value','now')`)
}

func TestBackupSnapshot(t *testing.T) {
	ctx := context.Background()
	s, path := backupTestOpen(t, "source")
	backupTestExec(t, s.db, `PRAGMA wal_autocheckpoint=0`)
	backupTestSeed(t, s)
	for _, history := range []bool{false, true} {
		t.Run(fmt.Sprintf("history_%t", history), func(t *testing.T) {
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if err := SnapshotFile(ctx, path, snapshot, history); err != nil { t.Fatal(err) }
			db, err := backupOpen(snapshot, "ro")
			if err != nil { t.Fatal(err) }
			defer db.Close()
			for _, table := range BackupTables {
				if got := backupTestString(t, db, `SELECT CAST(count(*) AS TEXT) FROM `+backupQuote(table)); got != "1" { t.Fatalf("%s count %s", table, got) }
			}
			want := "0"; if history { want = "1" }
			for _, table := range []string{"threads", "messages", "automation_runs"} {
				if got := backupTestString(t, db, `SELECT CAST(count(*) AS TEXT) FROM `+backupQuote(table)); got != want { t.Fatalf("%s count %s, want %s", table, got, want) }
			}
			if !history {
				if got := backupTestString(t, db, `SELECT CAST(thread_id IS NULL AND review_thread_id IS NULL AS TEXT) FROM tasks`); got != "1" { t.Fatal("task references retained") }
				if got := backupTestString(t, db, `SELECT CAST(plan_thread_id IS NULL AS TEXT) FROM goals`); got != "1" { t.Fatal("goal reference retained") }
			} else if got := backupTestString(t, db, `SELECT text FROM messages`); got != "secret history" { t.Fatalf("message = %q", got) }
			paths, err := SnapshotProjectPaths(ctx, snapshot)
			if err != nil || len(paths) != 1 || paths["shared"] != "/source" { t.Fatalf("paths = %v, err = %v", paths, err) }
		})
	}
}

func TestBackupPrepareSnapshot(t *testing.T) {
	ctx := context.Background()
	s, path := backupTestOpen(t, "versions")
	backupTestExec(t, s.db, fmt.Sprintf("PRAGMA user_version=%d", len(migrations)+1))
	if err := PrepareSnapshot(ctx, path); !errors.Is(err, ErrSnapshotTooNew) { t.Fatalf("new schema: %v", err) }
	backupTestExec(t, s.db, fmt.Sprintf("PRAGMA user_version=%d", len(migrations)-1))
	if err := PrepareSnapshot(ctx, path); err != nil { t.Fatalf("old schema: %v", err) }
	if got := backupTestString(t, s.db, `PRAGMA user_version`); got != fmt.Sprint(len(migrations)) { t.Fatalf("version = %s", got) }
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not a sqlite database"), 0600); err != nil { t.Fatal(err) }
	if err := PrepareSnapshot(ctx, corrupt); err == nil { t.Fatal("corrupt snapshot accepted") }
}

func TestBackupMerge(t *testing.T) {
	ctx := context.Background()
	source, sourcePath := backupTestOpen(t, "source")
	backupTestSeed(t, source)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := SnapshotFile(ctx, sourcePath, snapshot, true); err != nil { t.Fatal(err) }
	target, _ := backupTestOpen(t, "target")
	backupTestExec(t, target.db, `INSERT INTO projects(id,name,path,created_at,updated_at) VALUES('shared','old','/old','old','old'),('local','local','/local','now','now')`)
	backupTestExec(t, target.db, `INSERT INTO threads(id,project_id,title,provider,created_at,updated_at) VALUES('local-thread','shared','local','test','now','now')`)
	backupTestExec(t, target.db, `INSERT INTO messages(id,thread_id,seq,role,kind,text,turn_index,created_at) VALUES('local-message','local-thread',1,'user','text','local history',0,'now')`)
	backupTestExec(t, target.db, `INSERT INTO tasks(id,title,created_at,updated_at) VALUES('old-task','old','now','now')`)
	backupTestExec(t, target.db, `INSERT INTO automations(id,name,prompt,schedule,created_at,updated_at) VALUES('local-auto','local','prompt','daily','now','now')`)
	backupTestExec(t, target.db, `INSERT INTO automation_runs(id,automation_id,thread_id,status,started_at) VALUES('local-run','local-auto','local-thread','done','now')`)
	if err := target.MergeBackup(ctx, snapshot, "restore-1"); err != nil { t.Fatal(err) }
	checks := map[string]string{
		`SELECT path FROM projects WHERE id='shared'`: "/source",
		`SELECT path FROM projects WHERE id='local'`: "/local",
		`SELECT text FROM messages WHERE id='local-message'`: "local history",
		`SELECT project_id FROM threads WHERE id='local-thread'`: "shared",
		`SELECT thread_id FROM automation_runs WHERE id='local-run'`: "local-thread",
		`SELECT CAST(count(*) AS TEXT) FROM automation_runs WHERE id='source-run'`: "0",
		`SELECT CAST(count(*) AS TEXT) FROM automations`: "2",
		`SELECT CAST(count(*) AS TEXT) FROM tasks WHERE id='old-task'`: "0",
		`SELECT CAST(thread_id IS NULL AND review_thread_id IS NULL AS TEXT) FROM tasks WHERE id='task'`: "1",
		`SELECT status FROM tasks WHERE id='task'`: "todo",
		`SELECT status FROM goals WHERE id='goal'`: "paused",
		`SELECT CAST(plan_thread_id IS NULL AS TEXT) FROM goals WHERE id='goal'`: "1",
		`SELECT CAST(count(*) AS TEXT) FROM goal_tasks`: "1",
		`SELECT CAST(count(*) AS TEXT) FROM pragma_foreign_key_check`: "0",
	}
	for query, want := range checks { if got := backupTestString(t, target.db, query); got != want { t.Errorf("%s = %q, want %q", query, got, want) } }
	backupTestExec(t, target.db, `UPDATE tasks SET title='modified after restore' WHERE id='task'`)
	if err := target.MergeBackup(ctx, snapshot, "restore-1"); err != nil { t.Fatal(err) }
	if got := backupTestString(t, target.db, `SELECT title FROM tasks WHERE id='task'`); got != "modified after restore" { t.Fatalf("repeat merge changed task: %q", got) }
}
