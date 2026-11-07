package kanban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"mcode/internal/store"
)

func TestServiceEdit_CreateDraft(t *testing.T) {
	ctx := context.Background()
	svc, st, driver := setupTestService(t)

	// 1. 空白标题应拒绝
	_, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "   ",
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition for empty title, got: %v", err)
	}

	// 2. 正常创建草稿任务：IsDraft 应为 true，status 为 todo，不启动会话
	draft, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Draft Task 1",
		Notes:     "Some draft notes",
	})
	if err != nil {
		t.Fatalf("CreateDraft failed: %v", err)
	}
	if !draft.IsDraft {
		t.Errorf("expected IsDraft=true, got false")
	}
	if draft.Status != string(TaskStatusTodo) {
		t.Errorf("expected status todo, got %s", draft.Status)
	}
	if len(driver.starts) != 0 {
		t.Errorf("expected no session started for draft, got %d", len(driver.starts))
	}

	// 3. 关联 Goal 时追加到 goal_tasks
	goal := store.Goal{ID: "draft-goal", ProjectID: "proj-1", Prompt: "Objective for draft", Status: "plan_review"}
	err = st.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatalf("CreateGoal failed: %v", err)
	}
	draft2, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Draft Task With Goal",
		GoalID:    &goal.ID,
	})
	if err != nil {
		t.Fatalf("CreateDraft with GoalID failed: %v", err)
	}
	taskIDs, err := st.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		t.Fatalf("ListGoalTaskIDs failed: %v", err)
	}
	found := false
	for _, id := range taskIDs {
		if id == draft2.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected draft2 (%s) in goal_tasks, got %v", draft2.ID, taskIDs)
	}

	// 4. 依赖不存在或跨项目应报错
	_, err = svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Draft Task Bad Dep",
		DependsOn: []string{"non-existent-id"},
	})
	if err == nil {
		t.Fatalf("expected error for non-existent dependency, got nil")
	}

	// 5. DFS 成环检测
	// 创建 task A
	taskA, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Task A",
	})
	if err != nil {
		t.Fatalf("create Task A failed: %v", err)
	}
	// 创建 task B 依赖 A
	taskB, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Task B",
		DependsOn: []string{taskA.ID},
	})
	if err != nil {
		t.Fatalf("create Task B failed: %v", err)
	}
	// 试图创建 task C 依赖 B，而 A 后来形成闭环（直接测试 C 依赖自身或 A->B->C 环）
	// 新草稿 Task C 依赖 B
	taskC, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Task C",
		DependsOn: []string{taskB.ID},
	})
	if err != nil {
		t.Fatalf("create Task C failed: %v", err)
	}
	// 试图创建 Task D 依赖 D（自环）
	_, err = svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Self Cycle",
		DependsOn: []string{taskC.ID},
	})
	// 现在手动让 taskA 依赖 taskC 造成闭环，再次创建 taskD 依赖 taskA 应该被成环检测拦截
	taskA.DependsOn = []string{taskC.ID}
	_, _ = st.UpdateTask(ctx, taskA, taskA.Revision)

	_, err = svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Cycle Task",
		DependsOn: []string{taskA.ID},
	})
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got: %v", err)
	}
}

func TestServiceEdit_UpdateTask(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "Initial Title",
		Notes:     strPtr("Note line 1"),
	})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// 1. 正常更新 title 与追加 notes_append
	newTitle := "Updated Title"
	appendNote := "Note line 2"
	summary := "Done review"
	updated, err := svc.UpdateTask(ctx, UpdateTaskInput{
		TaskID:           task.ID,
		ExpectedRevision: task.Revision,
		Title:            &newTitle,
		NotesAppend:      &appendNote,
		ReviewSummary:    &summary,
	})
	if err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("expected Title 'Updated Title', got %s", updated.Title)
	}
	expectedNotes := "Note line 1\nNote line 2"
	if updated.Notes != expectedNotes {
		t.Errorf("expected Notes %q, got %q", expectedNotes, updated.Notes)
	}
	if updated.ReviewSummary == nil || *updated.ReviewSummary != summary {
		t.Errorf("expected ReviewSummary %q, got %v", summary, updated.ReviewSummary)
	}

	// 2. CAS revision 冲突
	_, err = svc.UpdateTask(ctx, UpdateTaskInput{
		TaskID:           task.ID,
		ExpectedRevision: task.Revision, // 旧版本号，应冲突
		Title:            &newTitle,
	})
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict, got: %v", err)
	}
}

func TestServiceEdit_DeleteTask(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := setupTestService(t)

	goal := store.Goal{ID: "delete-goal", ProjectID: "proj-1", Prompt: "A goal for deletion", Status: "plan_review"}
	err := st.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatalf("CreateGoal failed: %v", err)
	}

	draft, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Task To Delete",
		GoalID:    &goal.ID,
	})
	if err != nil {
		t.Fatalf("CreateDraft failed: %v", err)
	}

	// 确认 task 在 goal_tasks 中
	taskIDs, _ := st.ListGoalTaskIDs(ctx, goal.ID)
	if len(taskIDs) != 1 || taskIDs[0] != draft.ID {
		t.Fatalf("expected task in goal_tasks, got %v", taskIDs)
	}

	// 1. Revision 不匹配时拒绝
	err = svc.DeleteTask(ctx, draft.ID, draft.Revision+99)
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict, got: %v", err)
	}

	// 2. 成功删除
	err = svc.DeleteTask(ctx, draft.ID, draft.Revision)
	if err != nil {
		t.Fatalf("DeleteTask failed: %v", err)
	}

	// 确认从 store 彻底删除
	_, err = st.GetTask(ctx, draft.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for deleted task, got: %v", err)
	}

	// 确认从 goal_tasks 移除
	remainingIDs, _ := st.ListGoalTaskIDs(ctx, goal.ID)
	if len(remainingIDs) != 0 {
		t.Errorf("expected 0 goal tasks after delete, got %v", remainingIDs)
	}
}

func TestServiceEdit_SetGoalAndReadGoal(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := setupTestService(t)
	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "Task"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := "  exact task objective\n"
	updated, err := svc.SetGoal(ctx, SetGoalInput{TaskID: task.ID, ProjectID: "proj-1", Prompt: prompt, ExpectedRevision: task.Revision})
	if err != nil || updated.GoalPrompt == nil || *updated.GoalPrompt != prompt {
		t.Fatalf("SetGoal: %+v, %v", updated, err)
	}
	if _, err := svc.SetGoal(ctx, SetGoalInput{TaskID: task.ID, ProjectID: "other", Prompt: "attack"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-project goal update: %v", err)
	}
	if _, err := svc.SetGoal(ctx, SetGoalInput{TaskID: task.ID, ProjectID: "proj-1", ExpectedRevision: task.Revision}); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	cleared, err := svc.SetGoal(ctx, SetGoalInput{TaskID: task.ID, ProjectID: "proj-1", ExpectedRevision: updated.Revision})
	if err != nil || cleared.GoalPrompt != nil {
		t.Fatalf("clear goal: %+v, %v", cleared, err)
	}
	goals, err := st.ListGoals(ctx, "")
	if err != nil || len(goals) != 0 {
		t.Fatalf("SetGoal must not create goal records: %v, %v", goals, err)
	}
	h := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(h[:])
	goal := store.Goal{ID: "goal-original", ProjectID: "proj-1", Prompt: prompt, ContentHash: hash, Status: "plan_review"}
	if err := st.CreateGoal(ctx, goal); err != nil {
		t.Fatal(err)
	}
	text, err := svc.ReadGoal(ctx, goal.ID, hash)
	if err != nil || text != prompt {
		t.Fatalf("ReadGoal: %q, %v", text, err)
	}
	if _, err := svc.ReadGoal(ctx, goal.ID, "wrong"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("hash mismatch: %v", err)
	}
	persisted, err := st.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Prompt = "tampered"
	if _, err := st.UpdateGoal(ctx, persisted, persisted.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadGoal(ctx, goal.ID, hash); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("tampered content accepted: %v", err)
	}
}

func TestServiceEdit_DeleteLiveTask(t *testing.T) {
	ctx := context.Background()
	svc, st, driver := setupTestService(t)
	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "Running task"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = svc.StartTask(ctx, task.ID, task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	driver.setThreadState(*task.ThreadID, SessionState{Running: true})
	if err := svc.DeleteTask(ctx, task.ID, task.Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("live deletion: %v", err)
	}
	if _, err := st.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("live task lost: %v", err)
	}
	driver.setThreadState(*task.ThreadID, SessionState{})
	reviewID := "review-thread"
	task.ReviewThreadID = &reviewID
	task, err = st.UpdateTask(ctx, task, task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	driver.setThreadState(reviewID, SessionState{AwaitingApproval: true})
	if err := svc.DeleteTask(ctx, task.ID, task.Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("live review deletion: %v", err)
	}
	driver.setThreadState(reviewID, SessionState{})
	if err := svc.DeleteTask(ctx, task.ID, task.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestServiceEdit_StartTaskClearsIsDraft(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	draft, err := svc.CreateDraft(ctx, CreateDraftInput{
		ProjectID: "proj-1",
		Title:     "Draft To Be Started",
	})
	if err != nil {
		t.Fatalf("CreateDraft failed: %v", err)
	}
	if !draft.IsDraft {
		t.Fatalf("expected draft.IsDraft=true")
	}

	started, err := svc.StartTask(ctx, draft.ID, draft.Revision)
	if err != nil {
		t.Fatalf("StartTask failed: %v", err)
	}
	if started.IsDraft {
		t.Errorf("expected started.IsDraft=false after StartTask, got true")
	}
	if started.Status != string(TaskStatusInProgress) {
		t.Errorf("expected status in_progress, got %s", started.Status)
	}
}

func strPtr(s string) *string {
	return &s
}
