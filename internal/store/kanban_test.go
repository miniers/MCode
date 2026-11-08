package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestKanbanMigrationTables(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kanban_migration_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&version); err != nil {
		t.Fatalf("查询 user_version 失败: %v", err)
	}
	if version < 5 {
		t.Fatalf("期望 user_version >= 5，实际为 %d", version)
	}

	tables := []string{"tasks", "goals", "goal_tasks", "automations", "automation_runs"}
	for _, tbl := range tables {
		var name string
		err := s.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != nil {
			t.Fatalf("表 %s 未被成功创建: %v", tbl, err)
		}
	}
}

func TestTaskCRUDAndJSON(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "task_crud_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	projID := "proj-1"
	threadID := "thread-1"
	reviewSummary := "looks good"
	task1 := Task{
		ID:            "task-1",
		ProjectID:     &projID,
		ThreadID:      &threadID,
		DependsOn:     []string{"dep-a", "dep-b"},
		Title:         "实现任务存储",
		Notes:         "包含 CAS 与 DependsOn",
		Status:        "todo",
		Priority:      "high",
		ReviewSummary: &reviewSummary,
		AttemptCount:  2,
	}

	if err := s.CreateTask(ctx, task1); err != nil {
		t.Fatalf("创建 Task 失败: %v", err)
	}

	got, err := s.GetTask(ctx, task1.ID)
	if err != nil {
		t.Fatalf("获取 Task 失败: %v", err)
	}

	if got.ID != task1.ID || got.Title != task1.Title || got.Status != "todo" || got.Priority != "high" {
		t.Fatalf("获取到的 Task 字段不匹配: %+v", got)
	}
	if got.Revision != 1 {
		t.Fatalf("期望初始 revision 为 1，实际为 %d", got.Revision)
	}
	if got.Meta != "{}" {
		t.Fatalf("默认 meta = %q", got.Meta)
	}
	if len(got.DependsOn) != 2 || got.DependsOn[0] != "dep-a" || got.DependsOn[1] != "dep-b" {
		t.Fatalf("DependsOn 往返反序列化错误: %+v", got.DependsOn)
	}
	if got.ReviewSummary == nil || *got.ReviewSummary != reviewSummary {
		t.Fatalf("ReviewSummary 不匹配")
	}

	// 测试 ListTasks
	list, err := s.ListTasks(ctx, projID)
	if err != nil {
		t.Fatalf("ListTasks 按 projectID 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 个任务，实际 %d", len(list))
	}

	allList, err := s.ListTasks(ctx, "")
	if err != nil {
		t.Fatalf("ListTasks 全部失败: %v", err)
	}
	if len(allList) != 1 {
		t.Fatalf("期望全量 1 个任务，实际 %d", len(allList))
	}

	// 测试删除
	if err := s.DeleteTask(ctx, task1.ID); err != nil {
		t.Fatalf("删除 Task 失败: %v", err)
	}
	_, err = s.GetTask(ctx, task1.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后获取应返回 ErrNotFound，实际为: %v", err)
	}
}

func TestTaskCAS(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "task_cas_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	task := Task{
		ID:        "task-cas-1",
		Title:     "测试 CAS",
		Status:    "todo",
		Priority:  "medium",
		DependsOn: []string{},
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建 Task 失败: %v", err)
	}

	// 成功更新：expectedRevision = 1
	task.Title = "测试 CAS - 已更新"
	task.Status = "in_progress"
	task.Meta = `{"origin":"owner"}`
	updated, err := s.UpdateTask(ctx, task, 1)
	if err != nil {
		t.Fatalf("CAS 更新期望成功，实际失败: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("期望 revision 自增为 2，实际为 %d", updated.Revision)
	}
	reloaded, err := s.GetTask(ctx, task.ID)
	if err != nil || reloaded.Meta != task.Meta {
		t.Fatalf("meta 未正确保存: %+v, %v", reloaded, err)
	}

	// 用旧 revision 更新应触发冲突
	task.Title = "使用过期版本"
	_, err = s.UpdateTask(ctx, task, 1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("期望 ErrRevisionConflict，实际为: %v", err)
	}

	// 更新不存在的任务 ID 应返回 ErrNotFound
	notExistTask := task
	notExistTask.ID = "non-existent-task"
	_, err = s.UpdateTask(ctx, notExistTask, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的任务期望 ErrNotFound，实际为: %v", err)
	}
}

func TestGoalCRUDAndJSON(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "goal_crud_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	goal := Goal{
		ID:        "goal-1",
		ProjectID: "proj-1",
		Title:     "全自动部署",
		Prompt:    "配置流水线并上线",
		Policy:    map[string]any{"auto_retry": true, "max_retry": float64(3)},
		Progress:  map[string]any{"step": "stage1", "percent": float64(50)},
	}

	if err := s.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建 Goal 失败: %v", err)
	}

	got, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取 Goal 失败: %v", err)
	}
	if got.Revision != 1 {
		t.Fatalf("期望初始 revision 为 1，实际为 %d", got.Revision)
	}
	if got.Status != "planning" {
		t.Fatalf("期望默认状态 planning，实际为 %s", got.Status)
	}
	if got.Policy["auto_retry"] != true || got.Policy["max_retry"] != float64(3) {
		t.Fatalf("Policy JSON 往返不匹配: %+v", got.Policy)
	}
	if got.Progress["step"] != "stage1" {
		t.Fatalf("Progress JSON 往返不匹配: %+v", got.Progress)
	}

	// CAS 更新 Goal
	got.Title = "全自动部署 - 运行中"
	got.Status = "running"
	updated, err := s.UpdateGoal(ctx, got, 1)
	if err != nil {
		t.Fatalf("CAS 更新 Goal 失败: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("期望 revision 为 2，实际为 %d", updated.Revision)
	}

	// 冲突测试
	_, err = s.UpdateGoal(ctx, got, 1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("期望 ErrRevisionConflict，实际为: %v", err)
	}

	// 不存在的 ID
	fake := got
	fake.ID = "fake-goal"
	_, err = s.UpdateGoal(ctx, fake, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际为: %v", err)
	}
}

func TestGoalTasksOrderAndCascade(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "goal_tasks_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	goal := Goal{
		ID:        "goal-order-1",
		ProjectID: "proj-1",
		Title:     "计划任务列表",
		Prompt:    "执行多个任务",
	}
	if err := s.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建 Goal 失败: %v", err)
	}

	t1 := Task{ID: "task-ord-1", Title: "Task 1"}
	t2 := Task{ID: "task-ord-2", Title: "Task 2"}
	t3 := Task{ID: "task-ord-3", Title: "Task 3"}
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatalf("创建 t1 失败: %v", err)
	}
	if err := s.CreateTask(ctx, t2); err != nil {
		t.Fatalf("创建 t2 失败: %v", err)
	}
	if err := s.CreateTask(ctx, t3); err != nil {
		t.Fatalf("创建 t3 失败: %v", err)
	}

	order1 := []string{"task-ord-1", "task-ord-2", "task-ord-3"}
	if err := s.SetGoalTasks(ctx, goal.ID, order1, 1); err != nil {
		t.Fatalf("设置任务顺序 1 失败: %v", err)
	}

	gotOrder1, err := s.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取任务顺序 1 失败: %v", err)
	}
	if len(gotOrder1) != 3 || gotOrder1[0] != "task-ord-1" || gotOrder1[1] != "task-ord-2" || gotOrder1[2] != "task-ord-3" {
		t.Fatalf("顺序 1 不符合期望: %+v", gotOrder1)
	}

	// 重新设置顺序
	order2 := []string{"task-ord-3", "task-ord-1"}
	if err := s.SetGoalTasks(ctx, goal.ID, order2, 2); err != nil {
		t.Fatalf("重新设置任务顺序失败: %v", err)
	}
	gotOrder2, err := s.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取任务顺序 2 失败: %v", err)
	}
	if len(gotOrder2) != 2 || gotOrder2[0] != "task-ord-3" || gotOrder2[1] != "task-ord-1" {
		t.Fatalf("顺序 2 不符合期望: %+v", gotOrder2)
	}

	// 删除目标，验证级联删除
	if err := s.DeleteGoal(ctx, goal.ID); err != nil {
		t.Fatalf("删除 Goal 失败: %v", err)
	}
	gotAfterDelete, err := s.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		t.Fatalf("删除后查询关联失败: %v", err)
	}
	if len(gotAfterDelete) != 0 {
		t.Fatalf("期望 goal_tasks 级联删除清空，实际剩余 %d 个", len(gotAfterDelete))
	}
}
