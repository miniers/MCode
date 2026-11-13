package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartupBulkLists(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "bulk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject(ctx, Project{ID: "p", Name: "p", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateThread(ctx, Thread{ID: id, ProjectID: "p", Title: id, Provider: "codex"}); err != nil {
			t.Fatal(err)
		}
		for _, index := range []int{3, 1, 2} {
			if err := s.UpsertTurnUsage(ctx, TurnUsage{ThreadID: id, TurnIndex: index, InputTokens: int64(index)}); err != nil {
				t.Fatal(err)
			}
		}
		items := []FollowUp{{ID: id + "-1", Text: "first", CreatedAt: now, UpdatedAt: now}, {ID: id + "-2", Text: "second", Attachments: []string{"x"}, CreatedAt: now, UpdatedAt: now}}
		if err := s.ReplaceFollowUps(ctx, id, 1, items); err != nil {
			t.Fatal(err)
		}
	}
	usages, err := s.ListAllTurnUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	followUps, err := s.ListAllFollowUps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		singleUsage, err := s.ListTurnUsage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		singleFollowUps, err := s.ListFollowUps(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(usages[id], singleUsage) || len(usages[id]) != 3 || usages[id][0].TurnIndex != 1 || usages[id][2].TurnIndex != 3 {
			t.Fatalf("usage %s: %#v", id, usages[id])
		}
		if !reflect.DeepEqual(followUps[id], singleFollowUps) || len(followUps[id]) != 2 || followUps[id][0].Text != "first" || followUps[id][1].Text != "second" {
			t.Fatalf("follow ups %s: %#v", id, followUps[id])
		}
	}
	if len(usages) != 2 || len(followUps) != 2 {
		t.Fatal("unexpected groups")
	}
}

func TestStartupRecoveryIndexes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "indexes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct{ query, index string }{
		{"SELECT " + messageSelectCols + " FROM messages WHERE role = 'tool' AND tool_status = 'background' ORDER BY thread_id, seq", "idx_messages_tool_background"},
		{"SELECT " + messageSelectCols + " FROM messages WHERE role = 'tool' AND tool_status = 'running' ORDER BY thread_id, seq", "idx_messages_tool_running"},
		{"SELECT " + turnSelectCols + " FROM turns WHERE status IN ('running', 'starting') ORDER BY thread_id, turn_index", "idx_turns_status"},
	} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN " + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(plan, tc.index) {
			t.Errorf("expected %s in plan %s", tc.index, plan)
		}
	}
}
