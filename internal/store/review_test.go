package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func openReviewStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestKanbanMigrationUpgrade(t *testing.T) {
	for _, version := range []int{4, 5} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			old := &Store{db: db}
			for i := 0; i < version; i++ {
				if err := old.applyMigration(ctx, migrations[i], i+1); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.UpsertProject(ctx, Project{ID: "project", Name: "preserved", Path: "/tmp/project"}); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			projects, err := s.ListProjects(ctx)
			if err != nil || len(projects) != 1 || projects[0].Name != "preserved" {
				t.Fatalf("upgrade lost projects: %v, %v", projects, err)
			}
			for _, table := range []string{"tasks", "goals", "goal_tasks", "automations", "automation_runs"} {
				rows, err := s.db.QueryContext(ctx, "SELECT revision FROM "+table+" LIMIT 0")
				if err != nil {
					t.Fatalf("%s lacks revision: %v", table, err)
				}
				rows.Close()
			}
		})
	}
}

func TestGoalTasksCASAndRollback(t *testing.T) {
	s := openReviewStore(t)
	ctx := context.Background()
	if err := s.CreateGoal(ctx, Goal{ID: "g", ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.CreateTask(ctx, Task{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetGoalTasks(ctx, "g", []string{"a", "b"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGoalTasks(ctx, "g", nil, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale replacement: %v", err)
	}
	for _, ids := range [][]string{{"b", "missing"}, {"b", "b"}} {
		if err := s.SetGoalTasks(ctx, "g", ids, 2); err == nil {
			t.Fatal("expected invalid replacement to fail")
		}
		goal, err := s.GetGoal(ctx, "g")
		if err != nil || goal.Revision != 2 {
			t.Fatalf("failed replacement changed revision: %+v, %v", goal, err)
		}
		got, err := s.ListGoalTaskIDs(ctx, "g")
		if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("failed replacement changed links: %v, %v", got, err)
		}
	}
	if err := s.SetGoalTasks(ctx, "g", []string{"b"}, 2); err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM goal_tasks WHERE goal_id = 'g' AND task_id = 'b'").Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("association revision: %d, %v", revision, err)
	}
	if err := s.SetGoalTasks(ctx, "missing", nil, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing goal: %v", err)
	}
}

func TestGoalJSONErrorsPreserveData(t *testing.T) {
	s := openReviewStore(t)
	ctx := context.Background()
	for _, field := range []string{"policy", "progress"} {
		t.Run(field, func(t *testing.T) {
			goal := Goal{ID: field, ProjectID: "p"}
			bad := map[string]any{"invalid": math.NaN()}
			if field == "policy" {
				goal.Policy = bad
			} else {
				goal.Progress = bad
			}
			if err := s.CreateGoal(ctx, goal); err == nil {
				t.Fatal("invalid JSON was silently accepted")
			}
			if _, err := s.GetGoal(ctx, goal.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("failed create persisted data: %v", err)
			}
			if err := s.CreateGoal(ctx, Goal{ID: goal.ID, ProjectID: "p", Policy: map[string]any{"keep": true}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateGoal(ctx, goal, 1); err == nil {
				t.Fatal("invalid update was silently accepted")
			}
			got, err := s.GetGoal(ctx, goal.ID)
			if err != nil || got.Revision != 1 || got.Policy["keep"] != true {
				t.Fatalf("invalid update lost data: %+v, %v", got, err)
			}
			if _, err := s.db.ExecContext(ctx, "UPDATE goals SET "+field+" = '{' WHERE id = ?", goal.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetGoal(ctx, goal.ID); err == nil {
				t.Fatal("corrupt JSON was silently accepted")
			}
			if _, err := s.ListGoals(ctx, "p"); err == nil {
				t.Fatal("list silently accepted corrupt JSON")
			}
		})
	}
}

func TestCASConcurrentAndReturnedRecords(t *testing.T) {
	ctx := context.Background()
	s := openReviewStore(t)
	if err := s.CreateTask(ctx, Task{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGoal(ctx, Goal{ID: "g", ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAutomation(ctx, Automation{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAutomationRun(ctx, AutomationRun{ID: "r", AutomationID: "a"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		update func() (any, error)
		get    func() (any, error)
	}{
		{"task", func() (any, error) { return s.UpdateTask(ctx, Task{ID: "t"}, 1) }, func() (any, error) { return s.GetTask(ctx, "t") }},
		{"goal", func() (any, error) { return s.UpdateGoal(ctx, Goal{ID: "g", ProjectID: "p"}, 1) }, func() (any, error) { return s.GetGoal(ctx, "g") }},
		{"automation", func() (any, error) { return s.UpdateAutomation(ctx, Automation{ID: "a"}, 1) }, func() (any, error) { return s.GetAutomation(ctx, "a") }},
		{"run", func() (any, error) { return s.UpdateAutomationRun(ctx, AutomationRun{ID: "r"}, 1) }, func() (any, error) { return s.GetAutomationRun(ctx, "r") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			type result struct {
				value any
				err   error
			}
			results := make(chan result, 8)
			var wg sync.WaitGroup
			for i := 0; i < cap(results); i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					value, err := tc.update()
					results <- result{value, err}
				}()
			}
			wg.Wait()
			close(results)
			winners := 0
			for r := range results {
				if r.err == nil {
					winners++
					got, err := tc.get()
					if err != nil || !reflect.DeepEqual(r.value, got) {
						t.Errorf("returned record differs from stored: %#v / %#v, %v", r.value, got, err)
					}
				} else if !errors.Is(r.err, ErrRevisionConflict) {
					t.Errorf("unexpected CAS error: %v", r.err)
				}
			}
			if winners != 1 {
				t.Fatalf("expected one CAS winner, got %d", winners)
			}
		})
	}
}

func TestAutomationSubsecondOrdering(t *testing.T) {
	s := openReviewStore(t)
	ctx := context.Background()
	second := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	times := []time.Time{second, second.Add(time.Nanosecond), second.Add(100 * time.Millisecond), second.Add(110 * time.Millisecond)}
	for i, at := range times {
		id := fmt.Sprint(i)
		if err := s.CreateAutomation(ctx, Automation{ID: id, Enabled: true, NextRunAt: &at}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateAutomationRun(ctx, AutomationRun{ID: id, AutomationID: "0", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	for i, now := range times {
		due, err := s.ListDueAutomations(ctx, now)
		if err != nil || len(due) != i+1 {
			t.Fatalf("due at %v: %v, %v", now, due, err)
		}
		for j, a := range due {
			if a.ID != fmt.Sprint(j) {
				t.Fatalf("wrong due order: %v", due)
			}
		}
	}
	runs, err := s.ListAutomationRuns(ctx, "0", 2)
	if err != nil || len(runs) != 2 || runs[0].ID != "3" || runs[1].ID != "2" {
		t.Fatalf("wrong run order/limit: %v, %v", runs, err)
	}
	if err := s.DeleteAutomationRun(ctx, "0"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAutomationRun(ctx, "0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run deletion: %v", err)
	}
}
