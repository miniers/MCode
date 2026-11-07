package mcp

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"mcode/internal/store"
)

func TestKanbanSetGoal_ScopeAndTaskSemantics(t *testing.T) {
	srv, st, dir := setupTestMCP(t)
	ctx := context.Background()
	projectID, otherID := "proj-test-1", "other-project"
	for _, task := range []store.Task{
		{ID: "own-task", ProjectID: &projectID, Title: "own"},
		{ID: "other-task", ProjectID: &otherID, Title: "other"},
		{ID: "orphan-task", Title: "orphan"},
	} {
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	// 即使存在同 ID 的独立 goal，set_goal 也只能更新任务。
	if err := st.CreateGoal(ctx, store.Goal{ID: "own-task", ProjectID: otherID, Prompt: "must stay unchanged"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	call := func(taskID, goal, key string) (bool, map[string]any) {
		_, res := postRPC(t, ts.URL+"/mcp", srv.Token(), dir, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "kanban_set_goal", "arguments": map[string]any{"task_id": taskID, "goal": goal, "request_id": key}},
		})
		_, isError, payload := parseToolText(t, res)
		return isError, payload
	}
	for _, id := range []string{"other-task", "orphan-task", "missing-task"} {
		if isError, payload := call(id, "attack", id); !isError || payload["code"] != CodeNotFound {
			t.Fatalf("scope check failed for %s: %v", id, payload)
		}
	}
	if isError, payload := call("own-task", "  exact goal\n", "set-key"); isError || payload["goal"] != "  exact goal\n" || len(payload) != 3 {
		t.Fatalf("wrong set_goal contract: %v", payload)
	}
	task, err := st.GetTask(ctx, "own-task")
	if err != nil || task.GoalPrompt == nil || *task.GoalPrompt != "  exact goal\n" {
		t.Fatal("task objective was not persisted")
	}
	if isError, _ := call("own-task", "  exact goal\n", "set-key"); isError {
		t.Fatal("idempotent replay failed")
	}
	replayed, err := st.GetTask(ctx, "own-task")
	if err != nil || replayed.Revision != task.Revision {
		t.Fatal("replay wrote the task again")
	}
	if isError, payload := call("own-task", "", "clear-key"); isError || payload["goal"] != nil {
		t.Fatalf("clear failed: %v", payload)
	}
	goal, err := st.GetGoal(ctx, "own-task")
	if err != nil || goal.ProjectID != otherID || goal.Prompt != "must stay unchanged" {
		t.Fatal("set_goal modified an independent goal")
	}
}

func TestResolveProject_WorktreeOwnershipBeatsAncestor(t *testing.T) {
	var worktreeRoot, callbackDir string
	srv, st, dir := setupTestMCPWithOptions(t, func(opts *Options) {
		// DataDir 可以位于另一个已注册项目的目录内。
		worktreeRoot = filepath.Join(t.TempDir(), "worktrees")
		opts.WorktreeDir = worktreeRoot
		callbackDir = filepath.Join(filepath.Dir(worktreeRoot), "hydra", "head")
		opts.ResolveProjectID = func(ctx context.Context, workDir string) (string, bool) {
			return "owner-project", workDir == callbackDir
		}
	})
	ctx := context.Background()
	owner := store.Project{ID: "owner-project", Name: "owner", Path: t.TempDir()}
	if err := st.UpsertProject(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(ctx, store.Project{ID: "ancestor-project", Name: "ancestor", Path: filepath.Dir(worktreeRoot)}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(ctx, store.Task{ID: "task", ProjectID: &owner.ID, Title: "owned task"}); err != nil {
		t.Fatal(err)
	}
	for _, workDir := range []string{filepath.Join(worktreeRoot, "task", "subdir"), callbackDir} {
		project, err := srv.ProjectForWorkDir(ctx, workDir)
		if err != nil || project.ID != owner.ID {
			t.Fatalf("wrong worktree owner for %s: %s, %v", workDir, project.ID, err)
		}
	}
	if _, err := srv.ProjectForWorkDir(ctx, dir+"-suffix"); err == nil {
		t.Fatal("similar directory prefix matched a project")
	}
}
