package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestTaskAcceptanceFieldsRoundTrip 校验验收标准与打回计数在创建、查询、列表与更新中都能往返。
func TestTaskAcceptanceFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "task_acceptance.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	projID := "proj-accept"
	criteria := "- [命令] `go test ./internal/kanban/`\n- [审阅] 评审改动是否符合设计"
	task := Task{
		ID:                 "task-acceptance",
		ProjectID:          &projID,
		Title:              "带验收标准的任务",
		Status:             "todo",
		Priority:           "none",
		AcceptanceCriteria: criteria,
		RejectCount:        2,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建 Task 失败: %v", err)
	}

	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取 Task 失败: %v", err)
	}
	if got.AcceptanceCriteria != criteria {
		t.Fatalf("AcceptanceCriteria 不匹配: %q", got.AcceptanceCriteria)
	}
	if got.RejectCount != 2 {
		t.Fatalf("RejectCount 期望 2，实际 %d", got.RejectCount)
	}

	list, err := s.ListTasks(ctx, projID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTasks 期望 1 条，实际 %d，错误: %v", len(list), err)
	}
	if list[0].AcceptanceCriteria != criteria || list[0].RejectCount != 2 {
		t.Fatalf("ListTasks 未带回新列: %+v", list[0])
	}

	got.AcceptanceCriteria = "- [你验] 手工点一遍界面"
	got.RejectCount = 3
	updated, err := s.UpdateTask(ctx, got, got.Revision)
	if err != nil {
		t.Fatalf("UpdateTask 失败: %v", err)
	}
	if updated.AcceptanceCriteria != got.AcceptanceCriteria || updated.RejectCount != 3 {
		t.Fatalf("UpdateTask 返回未带回新列: %+v", updated)
	}
	reloaded, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("更新后获取 Task 失败: %v", err)
	}
	if reloaded.AcceptanceCriteria != got.AcceptanceCriteria || reloaded.RejectCount != 3 {
		t.Fatalf("更新未落库: %+v", reloaded)
	}
}

// TestGoalOwnerRoundTrip 校验目标创建者标记在创建、查询、列表与更新中都能往返。
func TestGoalOwnerRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "goal_owner.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	steward := Goal{ID: "goal-steward", ProjectID: "proj", Title: "总管建的", Prompt: "p", Status: "planning", Owner: "steward", Policy: map[string]any{}}
	if err := s.CreateGoal(ctx, steward); err != nil {
		t.Fatalf("创建 Goal 失败: %v", err)
	}
	plain := Goal{ID: "goal-plain", ProjectID: "proj", Title: "普通目标", Prompt: "p", Status: "planning", Policy: map[string]any{}}
	if err := s.CreateGoal(ctx, plain); err != nil {
		t.Fatalf("创建 Goal 失败: %v", err)
	}

	got, err := s.GetGoal(ctx, steward.ID)
	if err != nil {
		t.Fatalf("获取 Goal 失败: %v", err)
	}
	if got.Owner != "steward" {
		t.Fatalf("Owner 期望 steward，实际 %q", got.Owner)
	}
	gotPlain, err := s.GetGoal(ctx, plain.ID)
	if err != nil {
		t.Fatalf("获取 Goal 失败: %v", err)
	}
	if gotPlain.Owner != "" {
		t.Fatalf("普通目标 Owner 期望空串，实际 %q", gotPlain.Owner)
	}

	goals, err := s.ListGoals(ctx, "proj")
	if err != nil || len(goals) != 2 {
		t.Fatalf("ListGoals 期望 2 条，实际 %d，错误: %v", len(goals), err)
	}
	owners := map[string]string{}
	for _, g := range goals {
		owners[g.ID] = g.Owner
	}
	if owners["goal-steward"] != "steward" || owners["goal-plain"] != "" {
		t.Fatalf("ListGoals 未带回 owner: %+v", owners)
	}

	got.Owner = "steward"
	got.Title = "总管建的（已更新）"
	updated, err := s.UpdateGoal(ctx, got, got.Revision)
	if err != nil {
		t.Fatalf("UpdateGoal 失败: %v", err)
	}
	if updated.Owner != "steward" {
		t.Fatalf("UpdateGoal 丢失 owner: %q", updated.Owner)
	}
}

// TestKanbanAcceptanceMigrationFromOldDB 模拟旧库（只应用新列之前的所有迁移），
// 校验升级后新列存在且可读写。
func TestKanbanAcceptanceMigrationFromOldDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old_kanban.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db}
	// 按字符串定位 owner 迁移，避免后续追加迁移后固定偏移失效。
	legacyCount := 0
	for i, m := range migrations {
		if strings.Contains(m, "ALTER TABLE goals ADD COLUMN owner") {
			legacyCount = i
			break
		}
	}
	if legacyCount == 0 {
		t.Fatal("未能定位 goals.owner 迁移")
	}
	for i := 0; i < legacyCount; i++ {
		if err := legacy.applyMigration(ctx, migrations[i], i+1); err != nil {
			t.Fatalf("应用旧迁移 %d 失败: %v", i+1, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, title, created_at, updated_at) VALUES ('legacy-task', '旧任务', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("写入旧任务失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO goals (id, project_id, title, prompt, created_at, updated_at) VALUES ('legacy-goal', 'proj', '旧目标', 'p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("写入旧目标失败: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("升级打开失败: %v", err)
	}
	defer s.Close()

	task, err := s.GetTask(ctx, "legacy-task")
	if err != nil {
		t.Fatalf("升级后读取旧任务失败: %v", err)
	}
	if task.AcceptanceCriteria != "" || task.RejectCount != 0 {
		t.Fatalf("旧任务新列默认值不正确: %+v", task)
	}
	task.AcceptanceCriteria = "- [命令] `echo ok`"
	task.RejectCount = 1
	if _, err := s.UpdateTask(ctx, task, task.Revision); err != nil {
		t.Fatalf("升级后写新列失败: %v", err)
	}
	reloaded, err := s.GetTask(ctx, "legacy-task")
	if err != nil {
		t.Fatalf("升级后重读失败: %v", err)
	}
	if reloaded.AcceptanceCriteria != task.AcceptanceCriteria || reloaded.RejectCount != 1 {
		t.Fatalf("新列未落库: %+v", reloaded)
	}

	goal, err := s.GetGoal(ctx, "legacy-goal")
	if err != nil {
		t.Fatalf("升级后读取旧目标失败: %v", err)
	}
	if goal.Owner != "" {
		t.Fatalf("旧目标 owner 默认值应为空: %q", goal.Owner)
	}
	goal.Owner = "steward"
	if _, err := s.UpdateGoal(ctx, goal, goal.Revision); err != nil {
		t.Fatalf("升级后写 owner 失败: %v", err)
	}
	reloadedGoal, err := s.GetGoal(ctx, "legacy-goal")
	if err != nil || reloadedGoal.Owner != "steward" {
		t.Fatalf("owner 未落库: %+v, %v", reloadedGoal, err)
	}
}
