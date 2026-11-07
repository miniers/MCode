package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationFromDivergedBranches(t *testing.T) {
	const mcpFields = `
		ALTER TABLE tasks ADD COLUMN is_draft INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE goals ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
	`
	for _, test := range []struct {
		name    string
		scripts []string
	}{
		{"main-v9", []string{swarmLeadsMigration}},
		{"mcp-v9", []string{mcpFields}},
		{"mcp-v10", []string{mcpFields, `ALTER TABLE tasks ADD COLUMN goal_prompt TEXT;`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "old.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			old := &Store{db: db}
			for i, script := range migrations[:8] {
				if err := old.applyMigration(ctx, script, i+1); err != nil {
					t.Fatal(err)
				}
			}
			for i, script := range test.scripts {
				if err := old.applyMigration(ctx, script, i+9); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, title, created_at, updated_at)
				VALUES ('legacy-task', 'existing task', '2026-10-17T00:00:00Z', '2026-10-17T00:00:00Z');
				INSERT INTO goals (id, project_id, title, prompt, created_at, updated_at)
				VALUES ('legacy-goal', 'project', 'existing goal', 'original prompt', '2026-10-17T00:00:00Z', '2026-10-17T00:00:00Z');`); err != nil {
				t.Fatal(err)
			}
			if test.name == "main-v9" {
				if _, err := db.ExecContext(ctx, `INSERT INTO hydra_leads (lead_thread_id, project_id, updated_at)
					VALUES ('lead', 'project', '2026-10-17T00:00:00Z')`); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "mcp-v10" {
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET goal_prompt = 'existing objective' WHERE id = 'legacy-task'`); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				upgraded, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = upgraded.Close() })
				task, err := upgraded.GetTask(ctx, "legacy-task")
				if err != nil || task.Title != "existing task" || task.IsDraft {
					t.Fatalf("legacy task lost or MCP columns missing: %+v, %v", task, err)
				}
				if test.name == "mcp-v10" && (task.GoalPrompt == nil || *task.GoalPrompt != "existing objective") {
					t.Fatal("existing task objective lost")
				}
				goal, err := upgraded.GetGoal(ctx, "legacy-goal")
				if err != nil || goal.Prompt != "original prompt" {
					t.Fatalf("legacy goal lost or content_hash column missing: %+v, %v", goal, err)
				}
				var count int
				if err := upgraded.db.QueryRowContext(ctx, "SELECT count(*) FROM hydra_leads").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if test.name == "main-v9" && count != 1 {
					t.Fatal("existing Swarm record lost")
				}
				if err := upgraded.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
