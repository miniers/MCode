package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestCreateGoalTask_AtomicCAS(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	projectID, goalID := "project", "goal"
	if err := st.CreateGoal(ctx, Goal{ID: goalID, ProjectID: projectID, Status: "plan_review"}); err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "draft", ProjectID: &projectID, GoalID: &goalID, Title: "draft", IsDraft: true}
	if err := st.CreateGoalTask(ctx, task, 99); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected CAS conflict, got %v", err)
	}
	if _, err := st.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed CAS left an orphan task")
	}
	if err := st.CreateGoalTask(ctx, task, 1); err != nil {
		t.Fatal(err)
	}
	ids, err := st.ListGoalTaskIDs(ctx, goalID)
	if err != nil || len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("goal linkage failed: %v, %v", ids, err)
	}
	goal, err := st.GetGoal(ctx, goalID)
	if err != nil || goal.Revision != 2 {
		t.Fatalf("goal revision was not incremented: %d, %v", goal.Revision, err)
	}
	// 重复 task ID 使 INSERT 失败时，goal revision 也必须回滚。
	if err := st.CreateGoalTask(ctx, task, 2); err == nil {
		t.Fatal("duplicate task accepted")
	}
	goal, err = st.GetGoal(ctx, goalID)
	if err != nil || goal.Revision != 2 {
		t.Fatal("failed task insertion changed goal revision")
	}
	if err := st.DeleteTask(ctx, task.ID, 99); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("delete CAS failed: %v", err)
	}
	if err := st.DeleteTask(ctx, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	ids, err = st.ListGoalTaskIDs(ctx, goalID)
	if err != nil || len(ids) != 0 {
		t.Fatal("delete did not cascade goal_tasks")
	}
}
