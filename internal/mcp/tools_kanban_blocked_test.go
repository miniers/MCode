package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

func TestKanbanCreateStartBlockedCheckError(t *testing.T) {
	t.Parallel()

	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	projDir := t.TempDir()
	ctx := context.Background()
	proj := store.Project{
		ID:   "proj-test-1",
		Name: "test-proj",
		Path: projDir,
	}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("插入测试项目失败: %v", err)
	}

	driver := newTestDriver()
	kanbanSvc := kanban.NewService(kanban.Options{
		Store:    s,
		Driver:   driver,
		Projects: &testProjects{dir: projDir},
	})
	t.Cleanup(func() { kanbanSvc.Close() })

	// Store 置空使 taskBlocked 依赖检查返回错误，同时 Kanban 服务仍可建卡。
	srv := New(Options{
		Store:  nil,
		Kanban: kanbanSvc,
	})

	tc := ToolContext{
		Context: ctx,
		Project: proj,
		WorkDir: projDir,
		Server:  srv,
	}

	res := handleKanbanCreateTask(tc, map[string]any{
		"title":      "依赖检查失败任务",
		"request_id": "req-blocked-check-err",
	})
	if !res.IsError {
		t.Fatalf("期望返回错误，实际成功: %s", res.Text)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Text), &payload); err != nil {
		t.Fatalf("解析工具错误载荷失败: %v (%s)", err, res.Text)
	}
	if payload["code"] != CodePartialFailure {
		t.Fatalf("错误码期望 %q，实际为 %v", CodePartialFailure, payload["code"])
	}
	hint, _ := payload["hint"].(string)
	if !strings.Contains(hint, "task_id") {
		t.Fatalf("提示应包含 task_id，实际为 %q", hint)
	}

	driver.mu.Lock()
	startedCount := len(driver.starts)
	driver.mu.Unlock()
	if startedCount != 0 {
		t.Fatalf("依赖检查失败时不应启动任务，实际调用 StartSession %d 次", startedCount)
	}

	tasks, err := s.ListTasks(ctx, proj.ID)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != string(kanban.TaskStatusTodo) {
		t.Fatalf("卡片应已创建且停留在 todo，实际为 %+v", tasks)
	}
}
