package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/store"
)

// threadToolNames 以线程作用域发起 tools/list 并返回工具名集合。
func threadToolNames(t *testing.T, srv *Server, threadID string) map[string]bool {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	if status != 200 {
		t.Fatalf("tools/list 状态码期望 200, 实际为 %d", status)
	}
	result, _ := res["result"].(map[string]any)
	toolsArray, _ := result["tools"].([]any)
	names := make(map[string]bool, len(toolsArray))
	for _, item := range toolsArray {
		tm, _ := item.(map[string]any)
		if name, _ := tm["name"].(string); name != "" {
			names[name] = true
		}
	}
	return names
}

// kanbanNames 只保留类型化看板工具名，便于整集比对。
func kanbanNames(names map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for name := range names {
		if strings.HasPrefix(name, kanbanToolNamePrefix) {
			out[name] = true
		}
	}
	return out
}

// sameNameSet 判断两个工具名集合是否完全一致。
func sameNameSet(got map[string]bool, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, name := range want {
		if !got[name] {
			return false
		}
	}
	return true
}

// callKanbanEntry 以线程作用域经入口工具发起一次 tools/call。
func callKanbanEntry(t *testing.T, srv *Server, threadID string, args map[string]any) (string, bool, map[string]any) {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      kanbanEntryToolName,
			"arguments": args,
		},
	})
	if status != 200 {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", status)
	}
	return parseToolText(t, res)
}

func TestKanbanEntryOrdinaryThreadListsOnlyEntry(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	names := threadToolNames(t, srv, "thread-ordinary")
	if !names[kanbanEntryToolName] {
		t.Fatalf("普通线程 tools/list 应包含入口工具 %s, 实际为 %v", kanbanEntryToolName, names)
	}
	if hasToolPrefix(names, kanbanToolNamePrefix) {
		t.Fatalf("普通线程 tools/list 不应包含类型化看板工具, 实际为 %v", names)
	}
	if !hasToolPrefix(names, "context_") {
		t.Fatalf("非看板工具不应被裁剪, 实际为 %v", names)
	}
	for _, entry := range []string{automationFamily.entry, memoryFamily.entry} {
		if !names[entry] {
			t.Fatalf("普通线程 tools/list 应包含入口工具 %s, 实际为 %v", entry, names)
		}
	}
	if hasToolPrefix(names, automationFamily.prefix) || hasToolPrefix(names, memoryFamily.prefix) {
		t.Fatalf("普通线程 tools/list 不应包含类型化自动化或记忆工具, 实际为 %v", names)
	}
}

func TestKanbanEntryDisconnectedForOrdinaryThread(t *testing.T) {
	srv, st, _ := setupTestMCPWithOptions(t, func(o *Options) {
		o.KanbanEntryOrdinary = func() bool { return false }
	})
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	names := threadToolNames(t, srv, "thread-ordinary")
	if names[kanbanEntryToolName] || hasToolPrefix(names, kanbanToolNamePrefix) {
		t.Fatalf("关闭入口后普通线程不应看到任何看板工具, 实际为 %v", names)
	}
	if !hasToolPrefix(names, "context_") {
		t.Fatalf("非看板工具不应被裁剪, 实际为 %v", names)
	}
}

func TestKanbanEntrySharedEndpointKeepsTypedTools(t *testing.T) {
	srv, _, _ := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	names := listToolNames(t, ts.URL+"/mcp", srv.Token(), "")
	if names[kanbanEntryToolName] {
		t.Fatalf("共享端点 tools/list 不应包含入口工具, 实际为 %v", names)
	}
	if !hasToolPrefix(names, kanbanToolNamePrefix) {
		t.Fatalf("共享端点 tools/list 应保留类型化看板工具, 实际为 %v", names)
	}
}

func TestKanbanEntryRoleToolSets(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	ctx := context.Background()
	projectID := "proj-test-1"
	runThread, reviewThread, planThread := "thread-run", "thread-review", "thread-plan"

	if err := st.CreateTask(ctx, store.Task{ID: "run-task", ProjectID: &projectID, ThreadID: &runThread, Title: "run"}); err != nil {
		t.Fatalf("创建执行任务失败: %v", err)
	}
	if err := st.CreateTask(ctx, store.Task{ID: "review-task", ProjectID: &projectID, ReviewThreadID: &reviewThread, Title: "review"}); err != nil {
		t.Fatalf("创建评审任务失败: %v", err)
	}
	if err := st.CreateGoal(ctx, store.Goal{ID: "plan-goal", ProjectID: projectID, Prompt: "plan", PlanThreadID: &planThread}); err != nil {
		t.Fatalf("创建规划目标失败: %v", err)
	}
	for _, threadID := range []string{runThread, reviewThread, planThread} {
		registerThread(t, st, threadID, projectID)
	}

	cases := []struct {
		name   string
		thread string
		want   []string
	}{
		{
			name:   "执行线程",
			thread: runThread,
			want: []string{
				"kanban_read_board", "kanban_read_card", "kanban_list_tasks", "kanban_update_card", "kanban_submit_result",
			},
		},
		{
			name:   "审核线程",
			thread: reviewThread,
			want: []string{
				"kanban_read_board", "kanban_read_card", "kanban_list_tasks", "kanban_task_diff",
			},
		},
		{
			name:   "规划线程",
			thread: planThread,
			want: []string{
				"kanban_read_board", "kanban_goal_status", "kanban_read_card", "kanban_task_diff", "kanban_create_draft",
				"kanban_update_card", "kanban_move_card", "kanban_start_task", "kanban_read_goal", "kanban_review_task",
			},
		},
	}
	for _, tc := range cases {
		names := threadToolNames(t, srv, tc.thread)
		if names[kanbanEntryToolName] {
			t.Fatalf("%s tools/list 不应包含入口工具, 实际为 %v", tc.name, names)
		}
		if !sameNameSet(kanbanNames(names), tc.want) {
			t.Fatalf("%s 看板工具集不符合预期: %v", tc.name, kanbanNames(names))
		}
		if !hasToolPrefix(names, "context_") || !hasToolPrefix(names, "automation_") {
			t.Fatalf("%s 非看板工具不应被裁剪, 实际为 %v", tc.name, names)
		}
	}
}

func TestKanbanEntryHelpAndForwarding(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	const threadID = "thread-ordinary"
	registerThread(t, st, threadID, "proj-test-1")

	// 1. action=help 返回操作清单。
	text, isError, _ := callKanbanEntry(t, srv, threadID, map[string]any{"action": "help"})
	if isError {
		t.Fatalf("action=help 不应失败: %s", text)
	}
	for _, action := range []string{"read_board", "update_card", "read_goal"} {
		if !strings.Contains(text, action) {
			t.Fatalf("帮助清单缺少操作 %s: %s", action, text)
		}
	}

	// 2. help 加 args.action 返回该工具说明与参数字段。
	text, isError, _ = callKanbanEntry(t, srv, threadID, map[string]any{
		"action": "help",
		"args":   map[string]any{"action": "read_card"},
	})
	if isError {
		t.Fatalf("help read_card 不应失败: %s", text)
	}
	if !strings.Contains(text, "task_id") || !strings.Contains(text, "可选") {
		t.Fatalf("read_card 参数说明不完整: %s", text)
	}

	// 3. 只读动作经入口转发成功。
	_, isError, payload := callKanbanEntry(t, srv, threadID, map[string]any{"action": "read_board"})
	if isError || payload["project_id"] != "proj-test-1" {
		t.Fatalf("read_board 转发失败: %v", payload)
	}

	// 4. 不存在的 action 返回 not_found。
	text, isError, payload = callKanbanEntry(t, srv, threadID, map[string]any{"action": "no_such_action"})
	if !isError || payload["code"] != CodeNotFound {
		t.Fatalf("未知 action 应返回 %s: %s", CodeNotFound, text)
	}
}

func TestKanbanEntryEnumMatchesRegisteredTools(t *testing.T) {
	srv, _, _ := setupTestMCP(t)
	tool, ok := srv.registry.Get(kanbanEntryToolName)
	if !ok {
		t.Fatalf("未注册入口工具 %s", kanbanEntryToolName)
	}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	actionSchema, _ := properties["action"].(map[string]any)
	enum, ok := actionSchema["enum"].([]string)
	if !ok {
		t.Fatalf("入口工具缺少 action enum: %v", actionSchema)
	}

	want := []string{entryActionHelp}
	want = append(want, srv.registry.entryActions(kanbanFamily, nil)...)
	got := make(map[string]bool, len(enum))
	for _, name := range enum {
		got[name] = true
	}
	if !sameNameSet(got, want) {
		t.Fatalf("入口 enum 与实际注册的看板工具不一致: %v", enum)
	}
}
