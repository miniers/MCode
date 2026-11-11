package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

func parseToolText(t *testing.T, res map[string]any) (string, bool, map[string]any) {
	t.Helper()
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in response: %v", res)
	}
	isError, _ := result["isError"].(bool)
	contentArr, _ := result["content"].([]any)
	if len(contentArr) == 0 {
		return "", isError, nil
	}
	first, _ := contentArr[0].(map[string]any)
	text, _ := first["text"].(string)
	var jsonMap map[string]any
	_ = json.Unmarshal([]byte(text), &jsonMap)
	return text, isError, jsonMap
}

func TestMCP_KanbanUpdateCardProgress(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	task, err := srv.opts.Kanban.CreateTask(context.Background(), kanban.CreateTaskInput{
		ProjectID: "proj-test-1",
		Title:     "进展测试",
	})
	if err != nil {
		t.Fatal(err)
	}
	var changes atomic.Int32
	srv.opts.Kanban.SetOnChange(func() { changes.Add(1) })
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for i, tc := range []struct {
		progress string
		want     string
	}{
		{"  完成第一步  ", "完成第一步"},
		{" \t" + strings.Repeat("进", 80) + "额外内容  ", strings.Repeat("进", 80)},
	} {
		status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "method": "tools/call",
			"params": map[string]any{"name": "kanban_update_card", "arguments": map[string]any{
				"task_id": task.ID, "progress": tc.progress, "notes_append": "备注",
			}},
		})
		_, isErr, payload := parseToolText(t, res)
		if status != http.StatusOK || isErr {
			t.Fatalf("更新失败：状态 %d，结果 %v", status, payload)
		}
		stored, err := s.GetTask(context.Background(), task.ID)
		if err != nil || stored.LastActivityText == nil || *stored.LastActivityText != tc.want {
			t.Fatalf("进展不符：%v，错误：%v", stored.LastActivityText, err)
		}
		if stored.Notes != strings.TrimSuffix(strings.Repeat("备注\n", i+1), "\n") {
			t.Fatalf("追加备注不符：%q", stored.Notes)
		}
		if got := changes.Load(); got != int32(i+1) {
			t.Fatalf("通知次数不符：%d", got)
		}
	}
}

func TestKanbanTaskIDDefaultsToSession(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	ctx := context.Background()
	projectID, threadID := "proj-test-1", "executor-thread"
	for _, task := range []store.Task{
		{ID: "bound-task", ProjectID: &projectID, ThreadID: &threadID, Title: "bound", Status: string(kanban.TaskStatusInProgress)},
		{ID: "other-task", ProjectID: &projectID, Title: "other", Status: string(kanban.TaskStatusInProgress)},
	} {
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	boundCtx := context.WithValue(ctx, threadScopeKey{}, threadID)
	unboundCtx := context.WithValue(ctx, threadScopeKey{}, "unbound-thread")
	tools := []struct {
		name string
		args map[string]any
	}{
		{"kanban_read_card", map[string]any{}},
		{"kanban_update_card", map[string]any{"notes": "updated"}},
		{"kanban_submit_result", map[string]any{"outcome": "done", "summary": "finished"}},
	}
	for _, tool := range tools {
		t.Run(tool.name, func(t *testing.T) {
			def, ok := srv.registry.Get(tool.name)
			if !ok {
				t.Fatal("工具未注册")
			}
			if failure := validateArguments(def, tool.args); failure != nil {
				t.Fatalf("省略 task_id 未通过 schema 校验: %s", failure.Text)
			}
			call := func(callCtx context.Context, args map[string]any) ToolResult {
				return def.Handler(ToolContext{Context: callCtx, Project: store.Project{ID: projectID}, Server: srv}, args)
			}
			result := call(boundCtx, tool.args)
			if result.IsError {
				t.Fatalf("默认任务调用失败: %s", result.Text)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(result.Text), &payload); err != nil {
				t.Fatal(err)
			}
			if tool.name == "kanban_read_card" {
				card, _ := payload["card"].(map[string]any)
				if card["id"] != "bound-task" {
					t.Fatalf("读取了错误任务: %v", payload)
				}
			} else if payload["task_id"] != "bound-task" {
				t.Fatalf("操作了错误任务: %v", payload)
			}
			for _, missingCtx := range []context.Context{ctx, unboundCtx} {
				result = call(missingCtx, tool.args)
				if !result.IsError || toolErrorCode(result) != CodeInvalidArguments || !strings.Contains(result.Text, "Missing required argument: 'task_id'.") {
					t.Fatalf("未绑定会话应返回缺参错误: %+v", result)
				}
			}
			explicitArgs := make(map[string]any, len(tool.args)+1)
			for k, v := range tool.args {
				explicitArgs[k] = v
			}
			explicitArgs["task_id"] = "  bound-task  "
			result = call(boundCtx, explicitArgs)
			if result.IsError {
				t.Fatalf("显式指定绑定任务失败: %+v", result)
			}
			explicitArgs["task_id"] = "  other-task  "
			result = call(boundCtx, explicitArgs)
			if tool.name == "kanban_submit_result" {
				if !result.IsError || toolErrorCode(result) != CodeInvalidArguments {
					t.Fatalf("不能代其他会话提交结果: %+v", result)
				}
			} else if result.IsError || !strings.Contains(result.Text, "other-task") {
				t.Fatalf("显式任务调用失败: %+v", result)
			}
		})
	}
	stored, err := st.GetTask(ctx, "bound-task")
	if err != nil || stored.Notes != "updated" {
		t.Fatalf("默认任务备注未更新: %+v, %v", stored, err)
	}
}

func TestMCP_KanbanEditTools_Flow(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	// 1. initialize
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize 状态码期望 200, 实际为 %d", status)
	}

	// 2. tools/list (包含这 7 个工具)
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list 状态码期望 200, 实际为 %d", status)
	}
	listResult, _ := res["result"].(map[string]any)
	toolsArray, _ := listResult["tools"].([]any)

	expectedTools := map[string]bool{
		"kanban_create_draft": false,
		"kanban_update_card":  false,
		"kanban_update_task":  false,
		"kanban_delete_card":  false,
		"kanban_delete_task":  false,
		"kanban_set_goal":     false,
		"kanban_read_goal":    false,
	}

	for _, item := range toolsArray {
		tm, _ := item.(map[string]any)
		name, _ := tm["name"].(string)
		if _, ok := expectedTools[name]; ok {
			expectedTools[name] = true
		}
	}
	for name, found := range expectedTools {
		if !found {
			t.Fatalf("tools/list 缺少期望的工具: %s", name)
		}
	}

	// 3. tools/call: kanban_create_draft
	// (a) 正常创建草稿任务与幂等重放
	draftReqID := "req-draft-test-1"
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_draft",
			"arguments": map[string]any{
				"title":      "Draft Task Alpha",
				"notes":      "Draft notes content",
				"request_id": draftReqID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_create_draft 状态码期望 200, 实际为 %d", status)
	}
	_, isErr, payload := parseToolText(t, res)
	if isErr {
		t.Fatalf("kanban_create_draft 失败: %v", payload)
	}
	if payload["status"] != "draft_created" {
		t.Fatalf("expected status draft_created, got %v", payload["status"])
	}
	taskMap, _ := payload["task"].(map[string]any)
	taskID, _ := taskMap["id"].(string)
	if taskID == "" {
		t.Fatalf("kanban_create_draft 未返回 task ID")
	}

	// 幂等重放相同 request_id
	status, resReplay := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      11,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_draft",
			"arguments": map[string]any{
				"title":      "Draft Task Alpha",
				"notes":      "Draft notes content",
				"request_id": draftReqID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_create_draft replay 状态码期望 200, 实际为 %d", status)
	}
	_, _, replayPayload := parseToolText(t, resReplay)
	replayTask, _ := replayPayload["task"].(map[string]any)
	if replayTask["id"] != taskID {
		t.Fatalf("幂等重放期望返回相同 task ID %s, 实际为 %v", taskID, replayTask["id"])
	}

	// (b) 成环拒绝
	// 创建任务 B 依赖 A
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      12,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_draft",
			"arguments": map[string]any{
				"title":               "Task Beta",
				"depends_on_task_ids": []string{taskID},
				"request_id":          "req-beta",
			},
		},
	})
	_, _, betaPayload := parseToolText(t, res)
	betaTask, _ := betaPayload["task"].(map[string]any)
	betaID, _ := betaTask["id"].(string)

	// 修改 taskID 依赖 betaID 造成循环，再尝试创建 task Gamma
	taskAObj, _ := s.GetTask(context.Background(), taskID)
	taskAObj.DependsOn = []string{betaID}
	_, _ = s.UpdateTask(context.Background(), taskAObj, taskAObj.Revision)

	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      13,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_draft",
			"arguments": map[string]any{
				"title":               "Task Gamma (Cycle)",
				"depends_on_task_ids": []string{taskID},
				"request_id":          "req-gamma",
			},
		},
	})
	_, isErr, cyclePayload := parseToolText(t, res)
	if !isErr {
		t.Fatalf("expected cycle error, got success: %v", cyclePayload)
	}

	// 4. tools/call: kanban_update_card
	updateReqID := "req-update-card-1"
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      20,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_update_card",
			"arguments": map[string]any{
				"task_id":        betaID,
				"title":          "Task Beta Renamed",
				"notes_append":   "Milestone achieved",
				"review_summary": "Ready for test",
				"request_id":     updateReqID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_update_card 状态码期望 200, 实际为 %d", status)
	}
	_, isErr, updatePayload := parseToolText(t, res)
	if isErr {
		t.Fatalf("kanban_update_card 失败: %v", updatePayload)
	}
	cardMap, _ := updatePayload["card"].(map[string]any)
	if cardMap["title"] != "Task Beta Renamed" {
		t.Errorf("expected Title 'Task Beta Renamed', got %v", cardMap["title"])
	}

	// 幂等重放 kanban_update_card
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      21,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_update_card",
			"arguments": map[string]any{
				"task_id":        betaID,
				"title":          "Task Beta Renamed",
				"notes_append":   "Milestone achieved",
				"review_summary": "Ready for test",
				"request_id":     updateReqID,
			},
		},
	})
	_, _, replayUpdatePayload := parseToolText(t, res)
	replayCard, _ := replayUpdatePayload["card"].(map[string]any)
	if replayCard["title"] != "Task Beta Renamed" {
		t.Fatalf("kanban_update_card replay 返回不一致: %v", replayUpdatePayload)
	}

	// 5. tools/call: kanban_update_task
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      30,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_update_task",
			"arguments": map[string]any{
				"task_id":      betaID,
				"notes_append": "Another note via update_task",
				"request_id":   "req-update-task-1",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_update_task 状态码期望 200, 实际为 %d", status)
	}
	_, isErr, taskUpPayload := parseToolText(t, res)
	if isErr {
		t.Fatalf("kanban_update_task 失败: %v", taskUpPayload)
	}

	// 6. tools/call: kanban_set_goal 与 kanban_read_goal
	goalPrompt := "Implement intelligent automated testing pipeline."
	h := sha256.Sum256([]byte(goalPrompt))
	expectedHash := hex.EncodeToString(h[:])
	goalTargetID := taskID
	if err := s.CreateGoal(context.Background(), store.Goal{ID: goalTargetID, ProjectID: *taskAObj.ProjectID, Prompt: goalPrompt, ContentHash: expectedHash, Status: "plan_review"}); err != nil {
		t.Fatal(err)
	}

	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      40,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_set_goal",
			"arguments": map[string]any{
				"task_id":    goalTargetID,
				"goal":       goalPrompt,
				"request_id": "req-set-goal-1",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_set_goal 状态码期望 200, 实际为 %d", status)
	}
	_, isErr, setGoalPayload := parseToolText(t, res)
	if isErr {
		t.Fatalf("kanban_set_goal 失败: %v", setGoalPayload)
	}
	if setGoalPayload["goal"] != goalPrompt {
		t.Fatalf("wrong task goal: %v", setGoalPayload)
	}

	// 幂等重放 kanban_set_goal
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      41,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_set_goal",
			"arguments": map[string]any{
				"task_id":    goalTargetID,
				"goal":       goalPrompt,
				"request_id": "req-set-goal-1",
			},
		},
	})
	_, _, replaySetGoal := parseToolText(t, res)
	if replaySetGoal["goal"] != goalPrompt {
		t.Fatalf("wrong replay goal: %v", replaySetGoal)
	}

	// kanban_read_goal: 成功读取
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      42,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_read_goal",
			"arguments": map[string]any{
				"goal_id":      goalTargetID,
				"content_hash": expectedHash,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_read_goal 状态码期望 200, 实际为 %d", status)
	}
	text, isErr, _ := parseToolText(t, res)
	if isErr || text != goalPrompt {
		t.Fatalf("kanban_read_goal 期望原文 %q, 实际为 %q (isErr=%v)", goalPrompt, text, isErr)
	}

	// kanban_read_goal: hash 不匹配拒绝
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      43,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_read_goal",
			"arguments": map[string]any{
				"goal_id":      goalTargetID,
				"content_hash": "badbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadb",
			},
		},
	})
	_, isErr, hashErrPayload := parseToolText(t, res)
	if !isErr {
		t.Fatalf("kanban_read_goal 期望 hash 不匹配报错，实际成功: %v", hashErrPayload)
	}

	// 7. tools/call: kanban_delete_card
	deleteCardID := betaID
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      50,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_delete_card",
			"arguments": map[string]any{
				"task_id": deleteCardID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_delete_card 状态码期望 200, 实际为 %d", status)
	}
	_, isErr, delPayload := parseToolText(t, res)
	if isErr || delPayload["deleted"] != true {
		t.Fatalf("kanban_delete_card 失败: %v", delPayload)
	}

	// 再次删除返回 already_deleted
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      51,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_delete_card",
			"arguments": map[string]any{
				"task_id": deleteCardID,
			},
		},
	})
	_, isErr, delAgainPayload := parseToolText(t, res)
	if isErr || delAgainPayload["already_deleted"] != true {
		t.Fatalf("kanban_delete_card 再次删除期望 already_deleted=true, 实际: %v", delAgainPayload)
	}

	// 8. tools/call: kanban_delete_task
	deleteTaskID := taskID
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      60,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_delete_task",
			"arguments": map[string]any{
				"task_id": deleteTaskID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_delete_task 状态码期望 200, 实际为 %d", status)
	}
	_, isErr, delTaskPayload := parseToolText(t, res)
	if isErr || delTaskPayload["deleted"] != true {
		t.Fatalf("kanban_delete_task 失败: %v", delTaskPayload)
	}

	// 再次删除返回 already_deleted
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      61,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_delete_task",
			"arguments": map[string]any{
				"task_id": deleteTaskID,
			},
		},
	})
	_, isErr, delTaskAgainPayload := parseToolText(t, res)
	if isErr || delTaskAgainPayload["already_deleted"] != true {
		t.Fatalf("kanban_delete_task 再次删除期望 already_deleted=true, 实际: %v", delTaskAgainPayload)
	}
}

func TestMCP_ResolveProject_MultiProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "multi_proj_test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	proj1Dir := t.TempDir()
	proj2Dir := t.TempDir()

	proj1 := store.Project{
		ID:   "proj-multi-1",
		Name: "project-1",
		Path: proj1Dir,
	}
	proj2 := store.Project{
		ID:   "proj-multi-2",
		Name: "project-2",
		Path: proj2Dir,
	}
	if err := s.UpsertProject(ctx, proj1); err != nil {
		t.Fatalf("插入 proj1 失败: %v", err)
	}
	if err := s.UpsertProject(ctx, proj2); err != nil {
		t.Fatalf("插入 proj2 失败: %v", err)
	}

	wtRoot := t.TempDir()
	taskID := "task-multi-2"
	task := store.Task{
		ID:        taskID,
		ProjectID: &proj2.ID,
		Title:     "Task in Project 2",
		Status:    "todo",
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建测试任务失败: %v", err)
	}

	taskWorktreeDir := filepath.Join(wtRoot, taskID)
	taskSubDir := filepath.Join(taskWorktreeDir, "sub", "deep")
	_ = os.MkdirAll(taskSubDir, 0755)

	driver := newTestDriver()
	projects := &testProjects{dir: proj1Dir}
	kanbanSvc := kanban.NewService(kanban.Options{
		Store:    s,
		Driver:   driver,
		Projects: projects,
	})
	t.Cleanup(func() { kanbanSvc.Close() })

	opts := Options{
		Store:       s,
		Kanban:      kanbanSvc,
		WorktreeDir: wtRoot,
		ResolveProjectID: func(ctx context.Context, workDir string) (string, bool) {
			if workDir == "/special/callback/workdir" {
				return proj2.ID, true
			}
			return "", false
		},
	}
	srv := New(opts)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	// 1. 测试 worktree 子目录能够被识别为 proj2
	status, res := postRPC(t, mcpURL, token, taskSubDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "kanban_read_board",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	_, isErr, payload := parseToolText(t, res)
	if isErr {
		t.Fatalf("kanban_read_board 失败: %v", payload)
	}
	if payload["project_id"] != proj2.ID {
		t.Fatalf("worktree 子目录解析期望 project_id=%s, 实际为 %v", proj2.ID, payload["project_id"])
	}

	// 2. 测试 ResolveProjectID 回调能够识别项目
	status, res = postRPC(t, mcpURL, token, "/special/callback/workdir", map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "kanban_read_board",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	_, isErr, callbackPayload := parseToolText(t, res)
	if isErr {
		t.Fatalf("通过 ResolveProjectID 调用的 kanban_read_board 失败: %v", callbackPayload)
	}
	if callbackPayload["project_id"] != proj2.ID {
		t.Fatalf("ResolveProjectID 解析期望 project_id=%s, 实际为 %v", proj2.ID, callbackPayload["project_id"])
	}
}

func TestMCP_KanbanLifecycleErrors_MapInvalidTransition(t *testing.T) {
	srv, st, projDir := setupTestMCP(t)
	ctx := context.Background()
	projectID := "proj-test-1"
	for _, task := range []store.Task{
		{ID: "review-card", ProjectID: &projectID, Title: "waiting review", Status: string(kanban.TaskStatusReview)},
		{ID: "todo-card", ProjectID: &projectID, Title: "fresh card", Status: string(kanban.TaskStatusTodo)},
	} {
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	call := func(name string, args map[string]any) (bool, map[string]any) {
		t.Helper()
		_, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		_, isError, payload := parseToolText(t, res)
		return isError, payload
	}
	// 启动一张不在 todo 的卡片、评审一张不在 review 的卡片都是状态迁移非法：
	// 应报 invalid_arguments 且不可重试，而不是让模型把内部错误原样重试。
	for _, scenario := range []struct {
		name   string
		args   map[string]any
		prefix string
	}{
		{"kanban_start_task", map[string]any{"task_id": "review-card"}, "Could not start task"},
		{"kanban_review_task", map[string]any{"task_id": "todo-card", "decision": "accept"}, "The review decision could not be recorded"},
	} {
		isError, payload := call(scenario.name, scenario.args)
		if !isError || payload["code"] != CodeInvalidArguments {
			t.Fatalf("%s should report %s, got %v", scenario.name, CodeInvalidArguments, payload)
		}
		if retryable, _ := payload["retryable"].(bool); retryable {
			t.Fatalf("%s must not be retryable: %v", scenario.name, payload)
		}
		message, _ := payload["message"].(string)
		if !strings.Contains(message, scenario.prefix) {
			t.Fatalf("%s failed for the wrong reason: %v", scenario.name, payload)
		}
	}
}
