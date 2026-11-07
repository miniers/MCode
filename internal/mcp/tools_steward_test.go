package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/nmem"
	"mcode/internal/store"
)

// callStewardTool 以线程作用域调用总管工具，返回结果文本与是否出错。
func callStewardTool(t *testing.T, srv *Server, threadID, name string, args map[string]any) (string, bool) {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if status != 200 {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", status)
	}
	text, isError, _ := parseToolText(t, res)
	return text, isError
}

// decodeStewardPayload 把工具结果文本解析成 JSON 对象，用于校验序列化后的字段名与取值。
func decodeStewardPayload(t *testing.T, text string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("工具结果不是 JSON 对象: %q (%v)", text, err)
	}
	return payload
}

// stewardSameNames 判断两个工具名集合是否完全一致。
func stewardSameNames(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if !b[name] {
			return false
		}
	}
	return true
}

// newStewardTestHooks 返回一套可用的总管钩子，只有 steward-1 是总管。
// listLimit 记录 ListAttention 收到的 limit，供参数夹取测试断言。
func newStewardTestHooks(listLimit *int, resolved *string, notified *[3]string) *StewardHooks {
	return &StewardHooks{
		IsSteward: func(threadID string) bool { return threadID == "steward-1" },
		ListAttention: func(_ context.Context, unresolvedOnly bool, projectID string, limit int) ([]AttentionItem, error) {
			if listLimit != nil {
				*listLimit = limit
			}
			return []AttentionItem{{
				ID:          "att-1",
				Kind:        "thread_failed",
				SubjectType: "thread",
				SubjectID:   "lead-1",
				ProjectID:   projectID,
				ThreadID:    "lead-1",
				Title:       "Conversation failed",
				Detail:      "provider returned 500",
				EnteredAt:   "2026-01-02T03:04:05Z",
				CreatedAt:   "2026-01-02T03:04:05Z",
				Resolved:    !unresolvedOnly,
			}}, nil
		},
		ResolveAttention: func(_ context.Context, id string) error {
			if resolved != nil {
				*resolved = id
			}
			return nil
		},
		ThreadStatus: func(_ context.Context, projectID string) ([]ThreadStatusItem, error) {
			return []ThreadStatusItem{{
				ThreadID:         "lead-1",
				ProjectID:        projectID,
				Title:            "Lead conversation",
				Provider:         "anthropic",
				Model:            "claude",
				Running:          true,
				AwaitingApproval: true,
				AwaitingInput:    false,
				Failed:           false,
				FailureReason:    "",
				LastActivityText: "waiting for approval",
				UpdatedAt:        "2026-01-02T03:04:05Z",
			}}, nil
		},
		NotifyOwner: func(_ context.Context, title, body, threadID string) error {
			if notified != nil {
				*notified = [3]string{title, body, threadID}
			}
			return nil
		},
	}
}

func TestStewardToolsHiddenWithoutHooks(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	names := threadToolNames(t, srv, "steward-1")
	for name := range stewardToolNames {
		if names[name] {
			t.Fatalf("没有注入总管钩子时不应列出 %s", name)
		}
	}

	// 没有钩子时直接调用按未知工具拒绝，不暴露工具存在。
	text, isError := callStewardTool(t, srv, "steward-1", stewardAttentionListToolName, map[string]any{})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("未接线时应按未知工具拒绝: %q error=%v", text, isError)
	}
}

func TestStewardToolsHiddenForNonSteward(t *testing.T) {
	srv, st, projDir := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")

	// 普通会话的工具列表在注入钩子前后必须完全一致。
	before := threadToolNames(t, srv, "lead-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))
	after := threadToolNames(t, srv, "lead-1")
	if !stewardSameNames(before, after) {
		t.Fatalf("普通会话的工具列表被改动: 前 %v 后 %v", before, after)
	}
	if !after[kanbanEntryToolName] {
		t.Fatal("普通会话应保留看板入口工具")
	}

	validArgs := map[string]map[string]any{
		stewardAttentionListToolName:    {},
		stewardAttentionResolveToolName: {"id": "att-1"},
		stewardThreadStatusToolName:     {},
		stewardNotifyOwnerToolName:      {"title": "t", "body": "b"},
		stewardApprovalSuggestToolName:  {"thread_id": "lead-1", "request_id": "req-1", "option_id": "allow", "reason": "r"},
		stewardProjectsListToolName:     {},
	}
	for name := range stewardToolNames {
		if after[name] {
			t.Fatalf("非总管的会话不应列出 %s", name)
		}
		text, isError := callStewardTool(t, srv, "lead-1", name, validArgs[name])
		if !isError || !strings.Contains(text, "Unknown tool") {
			t.Fatalf("非总管调用 %s 应按未知工具拒绝: %q error=%v", name, text, isError)
		}
	}

	// 没有会话作用域的共享端点同样看不到总管专属工具。
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for name := range listToolNames(t, ts.URL+"/mcp", srv.Token(), projDir) {
		if stewardToolNames[name] {
			t.Fatalf("共享端点不应列出 %s", name)
		}
	}
}

func TestStewardSeesOnlyReadOnlyTools(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))
	// 未接入 Nowledge 时检索工具会被隐藏，这里模拟已接入。
	srv.opts.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
		return nil, nil
	}
	srv.opts.MemoryGet = func(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
		return nmem.Memory{}, nil
	}
	srv.opts.ThreadSearch = func(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error) {
		return nil, nil
	}
	srv.opts.ThreadFetch = func(ctx context.Context, projectPath, threadID string, offset, limit int) (nmem.ThreadPage, error) {
		return nmem.ThreadPage{}, nil
	}

	names := threadToolNames(t, srv, "steward-1")
	for name := range stewardToolNames {
		if !names[name] {
			t.Fatalf("总管的会话应列出 %s", name)
		}
	}
	for name := range stewardReadOnlyTools {
		if !names[name] {
			t.Fatalf("总管的会话应列出只读工具 %s", name)
		}
	}
	for name := range stewardReceiptTools {
		if !names[name] {
			t.Fatalf("总管的会话应列出回执工具 %s", name)
		}
	}
	for _, name := range []string{
		kanbanEntryToolName,
		"kanban_create_task", "kanban_create_goal", "kanban_create_draft",
		"kanban_update_card", "kanban_update_task", "kanban_move_card",
		"kanban_delete_card", "kanban_delete_task", "kanban_set_goal",
		"kanban_start_task", "kanban_review_task",
		// automation_create 现在是动作工具，由 TestStewardAutomationCreateVisibleOnlyWhenCanAct 覆盖。
		"automation_update", "automation_cancel",
		swarmDelegateToolName, "memory_propose",
	} {
		if names[name] {
			t.Fatalf("总管的会话不应看到写工具 %s", name)
		}
	}
}

// TestStewardSeesAutomationReportResult 验证总管的 tools/list 里有 automation_report_result：
// 总管线程可能是 heartbeat 自动化的来源会话，每轮都要能提交运行回执。
func TestStewardSeesAutomationReportResult(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))

	names := threadToolNames(t, srv, "steward-1")
	if !names["automation_report_result"] {
		t.Fatal("总管的会话应列出 automation_report_result")
	}
	if !stewardToolAllowed("automation_report_result") {
		t.Fatal("automation_report_result 应在总管的可调用白名单里")
	}
	if !stewardCallAllowed("automation_report_result") {
		t.Fatal("automation_report_result 应允许总管直接调用")
	}
}

func TestStewardAttentionListClampsArguments(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var gotLimit int
	srv.SetSteward(newStewardTestHooks(&gotLimit, nil, nil))

	cases := []struct {
		name string
		args map[string]any
		want int
	}{
		{"缺省", map[string]any{}, attentionListDefaultLimit},
		{"下限以下", map[string]any{"limit": float64(0)}, 1},
		{"上限以上", map[string]any{"limit": float64(500)}, attentionListMaxLimit},
		{"区间内", map[string]any{"limit": float64(7)}, 7},
	}
	for _, c := range cases {
		text, isError := callStewardTool(t, srv, "steward-1", stewardAttentionListToolName, c.args)
		if isError {
			t.Fatalf("%s: 调用失败: %q", c.name, text)
		}
		if gotLimit != c.want {
			t.Fatalf("%s: limit 期望夹到 %d, 实际为 %d", c.name, c.want, gotLimit)
		}
	}
}

func TestStewardToolsSerializeHookData(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))

	text, isError := callStewardTool(t, srv, "steward-1", stewardAttentionListToolName, map[string]any{"project_id": " proj-test-1 "})
	if isError {
		t.Fatalf("attention_list 调用失败: %q", text)
	}
	payload := decodeStewardPayload(t, text)
	if count, _ := payload["count"].(float64); count != 1 {
		t.Fatalf("attention_list count 期望 1, 实际为 %v", payload["count"])
	}
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("attention_list items 期望 1 条, 实际为 %v", payload["items"])
	}
	item, _ := items[0].(map[string]any)
	for key, want := range map[string]any{
		"id":          "att-1",
		"kind":        "thread_failed",
		"subjectType": "thread",
		"subjectId":   "lead-1",
		"projectId":   "proj-test-1",
		"threadId":    "lead-1",
		"title":       "Conversation failed",
		"detail":      "provider returned 500",
		"enteredAt":   "2026-01-02T03:04:05Z",
		"createdAt":   "2026-01-02T03:04:05Z",
		"resolved":    false,
	} {
		if item[key] != want {
			t.Fatalf("attention_list 字段 %s 期望 %v, 实际为 %v", key, want, item[key])
		}
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardThreadStatusToolName, map[string]any{})
	if isError {
		t.Fatalf("thread_status 调用失败: %q", text)
	}
	payload = decodeStewardPayload(t, text)
	threads, _ := payload["threads"].([]any)
	if len(threads) != 1 {
		t.Fatalf("thread_status threads 期望 1 条, 实际为 %v", payload["threads"])
	}
	thread, _ := threads[0].(map[string]any)
	for key, want := range map[string]any{
		"threadId":         "lead-1",
		"title":            "Lead conversation",
		"provider":         "anthropic",
		"model":            "claude",
		"running":          true,
		"awaitingApproval": true,
		"awaitingInput":    false,
		"failed":           false,
		"failureReason":    "",
		"lastActivityText": "waiting for approval",
		"updatedAt":        "2026-01-02T03:04:05Z",
	} {
		if thread[key] != want {
			t.Fatalf("thread_status 字段 %s 期望 %v, 实际为 %v", key, want, thread[key])
		}
	}
}

func TestStewardResolveAndNotifyPassThrough(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var resolved string
	var notified [3]string
	srv.SetSteward(newStewardTestHooks(nil, &resolved, &notified))

	text, isError := callStewardTool(t, srv, "steward-1", stewardAttentionResolveToolName, map[string]any{"id": " att-1 "})
	if isError {
		t.Fatalf("attention_resolve 调用失败: %q", text)
	}
	if resolved != "att-1" {
		t.Fatalf("钩子收到的 id 期望 att-1, 实际为 %q", resolved)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardNotifyOwnerToolName, map[string]any{
		"title":     " Conversation failed ",
		"body":      "The lead conversation stopped on a provider error.",
		"thread_id": " lead-1 ",
	})
	if isError {
		t.Fatalf("notify_owner 调用失败: %q", text)
	}
	if notified != [3]string{"Conversation failed", "The lead conversation stopped on a provider error.", "lead-1"} {
		t.Fatalf("钩子收到的通知参数异常: %v", notified)
	}
}

func TestStewardToolValidation(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	calls := 0
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.ListAttention = func(context.Context, bool, string, int) ([]AttentionItem, error) {
		calls++
		return nil, nil
	}
	hooks.ResolveAttention = func(context.Context, string) error {
		calls++
		return nil
	}
	hooks.NotifyOwner = func(context.Context, string, string, string) error {
		calls++
		return nil
	}
	srv.SetSteward(hooks)

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"缺 id", stewardAttentionResolveToolName, map[string]any{}},
		{"id 为空白", stewardAttentionResolveToolName, map[string]any{"id": "   "}},
		{"缺 title", stewardNotifyOwnerToolName, map[string]any{"body": "b"}},
		{"缺 body", stewardNotifyOwnerToolName, map[string]any{"title": "t"}},
		{"body 为空白", stewardNotifyOwnerToolName, map[string]any{"title": "t", "body": "  "}},
		{"title 超长", stewardNotifyOwnerToolName, map[string]any{"title": strings.Repeat("标", notifyOwnerTitleMaxRunes+1), "body": "b"}},
		{"body 超长", stewardNotifyOwnerToolName, map[string]any{"title": "t", "body": strings.Repeat("文", notifyOwnerBodyMaxRunes+1)}},
		{"limit 类型错误", stewardAttentionListToolName, map[string]any{"limit": "10"}},
		{"未知参数", stewardAttentionListToolName, map[string]any{"unresolved": true}},
	}
	for _, c := range cases {
		text, isError := callStewardTool(t, srv, "steward-1", c.tool, c.args)
		if !isError {
			t.Fatalf("%s: 期望参数错误, 实际为 %q", c.name, text)
		}
		if code := toolErrorCode(ToolResult{Text: text, IsError: true}); code != CodeInvalidArguments && code != CodeUnknownField {
			t.Fatalf("%s: 期望参数错误码, 实际为 %q (%q)", c.name, code, text)
		}
	}
	if calls != 0 {
		t.Fatalf("参数无效时不应调用钩子, 实际调用 %d 次", calls)
	}
}

// TestStewardNotifyOwnerBodyLimit 验证正文上限按 rune 计数：恰好到达上限时放行，多一个字符即拒绝。
func TestStewardNotifyOwnerBodyLimit(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var notified [3]string
	srv.SetSteward(newStewardTestHooks(nil, nil, &notified))

	text, isError := callStewardTool(t, srv, "steward-1", stewardNotifyOwnerToolName, map[string]any{
		"title": "t",
		"body":  strings.Repeat("文", notifyOwnerBodyMaxRunes),
	})
	if isError {
		t.Fatalf("正文恰好 %d 个字符时应成功: %q", notifyOwnerBodyMaxRunes, text)
	}
	if n := len([]rune(notified[1])); n != notifyOwnerBodyMaxRunes {
		t.Fatalf("钩子收到的正文长度期望 %d, 实际为 %d", notifyOwnerBodyMaxRunes, n)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardNotifyOwnerToolName, map[string]any{
		"title": "t",
		"body":  strings.Repeat("文", notifyOwnerBodyMaxRunes+1),
	})
	if !isError {
		t.Fatalf("正文超过 %d 个字符时应失败: %q", notifyOwnerBodyMaxRunes, text)
	}
	if code := toolErrorCode(ToolResult{Text: text, IsError: true}); code != CodeInvalidArguments {
		t.Fatalf("期望参数错误码, 实际为 %q (%q)", code, text)
	}
}

func TestStewardToolsReportNotEnabled(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	// 只接 IsSteward，其余字段为 nil：工具可见但每个都报未启用。
	srv.SetSteward(&StewardHooks{IsSteward: func(threadID string) bool { return threadID == "steward-1" }})
	if !threadToolNames(t, srv, "steward-1")[stewardAttentionListToolName] {
		t.Fatal("总管的会话应列出 attention_list")
	}

	cases := []struct {
		tool string
		args map[string]any
	}{
		{stewardAttentionListToolName, map[string]any{}},
		{stewardAttentionResolveToolName, map[string]any{"id": "att-1"}},
		{stewardThreadStatusToolName, map[string]any{}},
		{stewardNotifyOwnerToolName, map[string]any{"title": "t", "body": "b"}},
		{stewardProjectsListToolName, map[string]any{}},
	}
	for _, c := range cases {
		text, isError := callStewardTool(t, srv, "steward-1", c.tool, c.args)
		if !isError || !strings.Contains(text, "not enabled") {
			t.Fatalf("%s 未接线时应报未启用: %q error=%v", c.tool, text, isError)
		}
	}
}

func TestStewardToolErrorsFromHooks(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	srv.SetSteward(&StewardHooks{
		IsSteward: func(threadID string) bool { return threadID == "steward-1" },
		ListAttention: func(context.Context, bool, string, int) ([]AttentionItem, error) {
			return nil, context.DeadlineExceeded
		},
		ResolveAttention: func(context.Context, string) error { return context.DeadlineExceeded },
		ThreadStatus:     func(context.Context, string) ([]ThreadStatusItem, error) { return nil, context.DeadlineExceeded },
		NotifyOwner:      func(context.Context, string, string, string) error { return context.DeadlineExceeded },
	})

	cases := []struct {
		tool string
		args map[string]any
	}{
		{stewardAttentionListToolName, map[string]any{}},
		{stewardAttentionResolveToolName, map[string]any{"id": "att-1"}},
		{stewardThreadStatusToolName, map[string]any{}},
		{stewardNotifyOwnerToolName, map[string]any{"title": "t", "body": "b"}},
	}
	for _, c := range cases {
		text, isError := callStewardTool(t, srv, "steward-1", c.tool, c.args)
		if !isError || !strings.Contains(text, "context deadline exceeded") {
			t.Fatalf("%s 应把钩子错误透传为工具错误: %q error=%v", c.tool, text, isError)
		}
	}
}

// TestStewardProjectsList 校验总管能列出用户已添加的项目，而普通会话与话题会话都看不到、也调不了。
func TestStewardProjectsList(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")
	registerThread(t, st, "topic-1", "proj-test-1")

	called := 0
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.IsTopic = func(threadID string) bool { return threadID == "topic-1" }
	// observe 档：CanAct 返回 false，只读工具仍应可见、可调用。
	hooks.CanAct = func() bool { return false }
	hooks.ListProjects = func(_ context.Context) ([]ProjectItem, error) {
		called++
		return []ProjectItem{
			{ID: "proj-1", Name: "mcode", Path: "/work/mcode"},
			{ID: "proj-2", Name: "总管", Path: "/work/steward"},
		}, nil
	}
	srv.SetSteward(hooks)

	if !threadToolNames(t, srv, "steward-1")[stewardProjectsListToolName] {
		t.Fatal("总管的会话应列出 steward_projects_list")
	}
	text, isError := callStewardTool(t, srv, "steward-1", stewardProjectsListToolName, map[string]any{})
	if isError {
		t.Fatalf("总管调用 steward_projects_list 不应出错: %q", text)
	}
	payload := decodeStewardPayload(t, text)
	projects, ok := payload["projects"].([]any)
	if !ok || len(projects) != 2 {
		t.Fatalf("结果应带两个项目: %q", text)
	}
	first, _ := projects[0].(map[string]any)
	if first["id"] != "proj-1" || first["name"] != "mcode" || first["path"] != "/work/mcode" {
		t.Fatalf("项目字段应为 id、name、path: %v", projects[0])
	}
	if called != 1 {
		t.Fatalf("ListProjects 应被调用一次，实际 %d 次", called)
	}

	// 普通会话与话题会话都看不到，直接调用按未知工具拒绝。
	for _, threadID := range []string{"lead-1", "topic-1"} {
		if threadToolNames(t, srv, threadID)[stewardProjectsListToolName] {
			t.Fatalf("%s 不应列出 steward_projects_list", threadID)
		}
		text, isError := callStewardTool(t, srv, threadID, stewardProjectsListToolName, map[string]any{})
		if !isError || !strings.Contains(text, "Unknown tool") {
			t.Fatalf("%s 调用 steward_projects_list 应按未知工具拒绝: %q error=%v", threadID, text, isError)
		}
	}
}

// TestStewardProjectsListDisabledAndErrors 校验钩子缺失时报未启用，钩子出错时错误透传。
func TestStewardProjectsListDisabledAndErrors(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	// 只接 IsSteward：工具可见，调用报未启用。
	srv.SetSteward(&StewardHooks{IsSteward: func(threadID string) bool { return threadID == "steward-1" }})
	if !threadToolNames(t, srv, "steward-1")[stewardProjectsListToolName] {
		t.Fatal("总管的会话应列出 steward_projects_list")
	}
	text, isError := callStewardTool(t, srv, "steward-1", stewardProjectsListToolName, map[string]any{})
	if !isError || !strings.Contains(text, "not enabled") {
		t.Fatalf("未接线时应报未启用: %q error=%v", text, isError)
	}

	srv.SetSteward(&StewardHooks{
		IsSteward:    func(threadID string) bool { return threadID == "steward-1" },
		ListProjects: func(context.Context) ([]ProjectItem, error) { return nil, context.DeadlineExceeded },
	})
	text, isError = callStewardTool(t, srv, "steward-1", stewardProjectsListToolName, map[string]any{})
	if !isError || !strings.Contains(text, "context deadline exceeded") {
		t.Fatalf("钩子错误应透传为工具错误: %q error=%v", text, isError)
	}
}

// TestStewardPlanRequestVisibility 校验 plan_request 只给总管主会话：话题会话看不到、也调不了。
func TestStewardPlanRequestVisibility(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "topic-1", "proj-test-1")

	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.IsTopic = func(threadID string) bool { return threadID == "topic-1" }
	hooks.TurnStartedByUser = func(threadID string) bool { return true }
	hooks.PlanRequest = func(context.Context, string, PlanRequestInput) (PlanRequestResult, error) {
		return PlanRequestResult{BeeName: "Cove", ProjectName: "mcode", PlanDir: "/work/mcode/docs"}, nil
	}
	srv.SetSteward(hooks)

	if !threadToolNames(t, srv, "steward-1")[stewardPlanRequestToolName] {
		t.Fatal("总管的会话应列出 plan_request")
	}
	if threadToolNames(t, srv, "topic-1")[stewardPlanRequestToolName] {
		t.Fatal("话题会话不应列出 plan_request")
	}
	text, isError := callStewardTool(t, srv, "topic-1", stewardPlanRequestToolName, map[string]any{"goal": "g"})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("话题会话调用 plan_request 应按未知工具拒绍: %q error=%v", text, isError)
	}
}

// TestStewardPlanRequestRequiresUserTurn 校验只有用户亲自发消息的那一轮才能派出方案子代理：
// 被宿主事件唤醒或收到子代理汇报的轮次一律拒绝，且不会真的派活。
func TestStewardPlanRequestRequiresUserTurn(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	called := 0
	hooks := newStewardTestHooks(nil, nil, nil)
	// observe 档也允许调用 plan_request，所以 CanAct 为 nil 不应拦住它。
	hooks.TurnStartedByUser = func(string) bool { return false }
	hooks.PlanRequest = func(context.Context, string, PlanRequestInput) (PlanRequestResult, error) {
		called++
		return PlanRequestResult{}, nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "steward-1", stewardPlanRequestToolName, map[string]any{"goal": "写一个方案"})
	if !isError || !strings.Contains(text, "notify_owner") {
		t.Fatalf("被事件唤醒的一轮调用 plan_request 应被拒绝: %q error=%v", text, isError)
	}
	if called != 0 {
		t.Fatalf("被拒绝时不应调用 PlanRequest，实际调用 %d 次", called)
	}

	// TurnStartedByUser 未接线时同样拒绝。
	nilHooks := newStewardTestHooks(nil, nil, nil)
	nilHooks.PlanRequest = hooks.PlanRequest
	srv.SetSteward(nilHooks)
	text, isError = callStewardTool(t, srv, "steward-1", stewardPlanRequestToolName, map[string]any{"goal": "写一个方案"})
	if !isError || !strings.Contains(text, "notify_owner") {
		t.Fatalf("TurnStartedByUser 未接线时应拒绝: %q error=%v", text, isError)
	}
	if called != 0 {
		t.Fatalf("未接线时不应调用 PlanRequest，实际调用 %d 次", called)
	}
}

// TestStewardPlanRequestDispatches 校验参数透传、成功文案与参数校验。
func TestStewardPlanRequestDispatches(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var gotThread string
	var gotReq PlanRequestInput
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.CanAct = func() bool { return false }
	hooks.TurnStartedByUser = func(threadID string) bool { return threadID == "steward-1" }
	hooks.PlanRequest = func(_ context.Context, threadID string, req PlanRequestInput) (PlanRequestResult, error) {
		gotThread, gotReq = threadID, req
		return PlanRequestResult{
			BeeName:     "Cove",
			ProjectName: "mcode",
			WorkDir:     "/work/mcode",
			PlanDir:     "/work/mcode/docs/plans",
		}, nil
	}
	srv.SetSteward(hooks)

	// observe 档（CanAct 为 false）仍应可见可用。
	if !threadToolNames(t, srv, "steward-1")[stewardPlanRequestToolName] {
		t.Fatal("observe 档也应列出 plan_request")
	}
	text, isError := callStewardTool(t, srv, "steward-1", stewardPlanRequestToolName, map[string]any{
		"project": " mcode ",
		"goal":    "把方案写出来",
		"context": " 相关会话 lead-1 ",
	})
	if isError {
		t.Fatalf("用户亲自发消息的一轮调用 plan_request 不应出错: %q", text)
	}
	if gotThread != "steward-1" {
		t.Fatalf("PlanRequest 应收到会话 ID steward-1，实际为 %q", gotThread)
	}
	want := PlanRequestInput{Project: "mcode", Goal: "把方案写出来", Context: "相关会话 lead-1"}
	if gotReq != want {
		t.Fatalf("PlanRequest 参数不符: 期望 %+v，实际 %+v", want, gotReq)
	}
	if !strings.Contains(text, "Cove") || !strings.Contains(text, "/work/mcode/docs/plans") {
		t.Fatalf("成功文案应带上子代理名与方案目录: %q", text)
	}

	// goal 去空白后为空时按参数错误拒绝，且不派活。
	gotThread, gotReq = "", PlanRequestInput{}
	text, isError = callStewardTool(t, srv, "steward-1", stewardPlanRequestToolName, map[string]any{"goal": "   "})
	if !isError || !strings.Contains(text, "goal") {
		t.Fatalf("goal 为空时应报参数错误: %q error=%v", text, isError)
	}
	if gotThread != "" {
		t.Fatalf("参数错误时不应调用 PlanRequest，实际收到会话 ID %q", gotThread)
	}
}

// TestStewardPlanRequestDisabledAndErrors 校验钩子缺失时报未启用，钩子出错时错误透传。
func TestStewardPlanRequestDisabledAndErrors(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	// 只接 IsSteward 与用户轮次：工具可见，调用报未启用。
	srv.SetSteward(&StewardHooks{
		IsSteward:         func(threadID string) bool { return threadID == "steward-1" },
		TurnStartedByUser: func(string) bool { return true },
	})
	text, isError := callStewardTool(t, srv, "steward-1", stewardPlanRequestToolName, map[string]any{"goal": "写一个方案"})
	if !isError || !strings.Contains(text, "not enabled") {
		t.Fatalf("未接线时应报未启用: %q error=%v", text, isError)
	}

	srv.SetSteward(&StewardHooks{
		IsSteward:         func(threadID string) bool { return threadID == "steward-1" },
		TurnStartedByUser: func(string) bool { return true },
		PlanRequest: func(context.Context, string, PlanRequestInput) (PlanRequestResult, error) {
			return PlanRequestResult{}, context.DeadlineExceeded
		},
	})
	text, isError = callStewardTool(t, srv, "steward-1", stewardPlanRequestToolName, map[string]any{"goal": "写一个方案"})
	if !isError || !strings.Contains(text, "context deadline exceeded") {
		t.Fatalf("钩子错误应透传为工具错误: %q error=%v", text, isError)
	}
}

// TestStewardAssignTaskRegisteredAndScoped 校验 steward_assign_task 只给总管主会话：
// 登记在总管白名单里，话题会话既看不到也调不了。
func TestStewardAssignTaskRegisteredAndScoped(t *testing.T) {
	if !stewardToolNames[stewardAssignTaskToolName] {
		t.Fatal("steward_assign_task 应登记在总管白名单里")
	}
	if !stewardCallAllowed(stewardAssignTaskToolName) {
		t.Fatal("stewardCallAllowed 应对 steward_assign_task 为真")
	}
	if stewardTopicCallAllowed(stewardAssignTaskToolName) {
		t.Fatal("话题白名单不应放行 steward_assign_task")
	}
	if filtered := filterTopicTools(nil, []ToolDefinition{{Name: stewardAssignTaskToolName}}, true); len(filtered) != 0 {
		t.Fatalf("filterTopicTools 不应放行 steward_assign_task: %v", filtered)
	}

	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "topic-1", "proj-test-1")
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.IsTopic = func(threadID string) bool { return threadID == "topic-1" }
	srv.SetSteward(hooks)

	if !threadToolNames(t, srv, "steward-1")[stewardAssignTaskToolName] {
		t.Fatal("总管的会话应列出 steward_assign_task")
	}
	if threadToolNames(t, srv, "topic-1")[stewardAssignTaskToolName] {
		t.Fatal("话题会话不应列出 steward_assign_task")
	}
	text, isError := callStewardTool(t, srv, "topic-1", stewardAssignTaskToolName, map[string]any{
		"project": "proj-test-1",
		"task":    "改一下 README",
	})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("话题会话调用 steward_assign_task 应按未知工具拒绝: %q error=%v", text, isError)
	}
}

// TestStewardAssignTaskRequiresUserTurn 校验只有用户亲自发消息的那一轮才能派活：
// 被事件唤醒或收到子代理汇报的轮次一律拒绝，且不会真的派。
func TestStewardAssignTaskRequiresUserTurn(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	called := 0
	hooks := newStewardTestHooks(nil, nil, nil)
	// observe 档也允许调用 steward_assign_task，所以 CanAct 为 nil 不应拦住它。
	hooks.TurnStartedByUser = func(string) bool { return false }
	hooks.AssignTask = func(context.Context, string, AssignTaskInput) (AssignTaskResult, error) {
		called++
		return AssignTaskResult{}, nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "steward-1", stewardAssignTaskToolName, map[string]any{
		"project": "proj-test-1",
		"task":    "改一下 README",
	})
	if !isError || !strings.Contains(text, "notify_owner") {
		t.Fatalf("被事件唤醒的一轮调用 steward_assign_task 应被拒绝: %q error=%v", text, isError)
	}
	if called != 0 {
		t.Fatalf("被拒绝时不应调用 AssignTask，实际调用 %d 次", called)
	}
}

// TestStewardAssignTaskRejectsAbsoluteWritePaths 校验 write_paths 只收仓库相对路径：
// 绝对路径与 .. 前缀在工具层就被拒绝，钩子不会被调用。
func TestStewardAssignTaskRejectsAbsoluteWritePaths(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	called := 0
	var gotReq AssignTaskInput
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.TurnStartedByUser = func(threadID string) bool { return threadID == "steward-1" }
	hooks.AssignTask = func(_ context.Context, threadID string, req AssignTaskInput) (AssignTaskResult, error) {
		called++
		gotReq = req
		return AssignTaskResult{Dispatched: true, BeeName: "Kestrel", ThreadID: "bee-1", ProjectName: "mcode", WorkDir: "/work/mcode"}, nil
	}
	srv.SetSteward(hooks)

	for _, bad := range [][]any{{"/tmp/x"}, {"../x"}} {
		text, isError := callStewardTool(t, srv, "steward-1", stewardAssignTaskToolName, map[string]any{
			"project":     "mcode",
			"task":        "改一下 README",
			"write_paths": bad,
		})
		if !isError || !strings.Contains(text, "write_paths") {
			t.Fatalf("write_paths=%v 应报参数错误: %q error=%v", bad, text, isError)
		}
		if called != 0 {
			t.Fatalf("write_paths=%v 被拒绝时不应调用 AssignTask，实际调用 %d 次", bad, called)
		}
	}

	// 合法的仓库相对路径应原样透传给钩子。
	text, isError := callStewardTool(t, srv, "steward-1", stewardAssignTaskToolName, map[string]any{
		"project":     " mcode ",
		"task":        "改一下 README",
		"context":     " 现象见 README.md ",
		"write_paths": []any{"README.md", "internal/steward/**"},
	})
	if isError {
		t.Fatalf("合法 write_paths 不应出错: %q", text)
	}
	if called != 1 {
		t.Fatalf("应调用 AssignTask 一次，实际 %d 次", called)
	}
	if gotReq.Project != "mcode" || gotReq.Task != "改一下 README" || gotReq.Context != "现象见 README.md" {
		t.Fatalf("AssignTask 参数不符: %+v", gotReq)
	}
	if len(gotReq.WritePaths) != 2 || gotReq.WritePaths[0] != "README.md" || gotReq.WritePaths[1] != "internal/steward/**" {
		t.Fatalf("write_paths 未原样透传: %v", gotReq.WritePaths)
	}
	payload := decodeStewardPayload(t, text)
	if payload["beeName"] != "Kestrel" || payload["threadId"] != "bee-1" || payload["projectName"] != "mcode" || payload["workDir"] != "/work/mcode" {
		t.Fatalf("成功返回值不符: %v", payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "Kestrel") || !strings.Contains(message, "自动送回本会话") {
		t.Fatalf("message = %q，期望包含子代理名与自动送回说明", message)
	}
}

// TestStewardAssignTaskSoftResultWhenSwarmDisabled 校验宿主没开编排时派活按成功返回，
// 带上 dispatched:false 与让总管自己动手的说明，而不是让工具报错。
func TestStewardAssignTaskSoftResultWhenSwarmDisabled(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.TurnStartedByUser = func(threadID string) bool { return threadID == "steward-1" }
	hooks.AssignTask = func(context.Context, string, AssignTaskInput) (AssignTaskResult, error) {
		return AssignTaskResult{Message: "宿主没开编排，请你自己做。"}, nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "steward-1", stewardAssignTaskToolName, map[string]any{
		"project": "mcode",
		"task":    "改一下 README",
	})
	if isError {
		t.Fatalf("宿主没开编排时不应返回错误: %q", text)
	}
	payload := decodeStewardPayload(t, text)
	if dispatched, ok := payload["dispatched"].(bool); !ok || dispatched {
		t.Fatalf("payload.dispatched 应为 false，实际 %v", payload["dispatched"])
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "没开编排") {
		t.Fatalf("payload.message = %q，期望说明宿主没开编排", message)
	}
}

func TestStewardCallRejectsHiddenWriteTools(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))

	// 这些写工具在 tools/list 里对总管隐藏，直接调用也必须按未知工具拒绝。
	cases := []struct {
		name string
		args map[string]any
	}{
		{"kanban_update_card", map[string]any{"task_id": "task-1", "title": "changed by the steward"}},
		// automation_create 已从写黑名单移到动作工具（白名单受 CanAct 控制），对应用例
		// 是 TestStewardAutomationCreateVisibleOnlyWhenCanAct。
		{"memory_propose", map[string]any{"title": "steward writes", "content": "the steward must not be able to write"}},
	}
	for _, c := range cases {
		if stewardToolAllowed(c.name) {
			t.Fatalf("%s 不应在总管的可调用白名单里", c.name)
		}
		text, isError := callStewardTool(t, srv, "steward-1", c.name, c.args)
		if !isError || !strings.Contains(text, "Unknown tool") {
			t.Fatalf("总管调用写工具 %s 应按未知工具拒绝: %q error=%v", c.name, text, isError)
		}
	}
}

func TestStewardCallAllowsReadOnlyTools(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))

	if !stewardToolAllowed("kanban_read_board") {
		t.Fatal("kanban_read_board 应在总管的可调用白名单里")
	}
	text, isError := callStewardTool(t, srv, "steward-1", "kanban_read_board", map[string]any{})
	if isError || strings.Contains(text, "Unknown tool") {
		t.Fatalf("总管调用只读工具 kanban_read_board 不应被白名单拦截: %q error=%v", text, isError)
	}
}

// TestStewardCanSeeSwarmReportGet 验证总管能取回被截断的子代理汇报全文：工具在只读白名单里，
// 不算跨项目工具，调用时不会被当成未知工具。
func TestStewardCanSeeSwarmReportGet(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))

	if !stewardToolAllowed("swarm_report_get") {
		t.Fatal("swarm_report_get 应在总管的可调用白名单里")
	}
	if stewardCrossProjectTools["swarm_report_get"] {
		t.Fatal("swarm_report_get 不应跨项目可用")
	}
	text, isError := callStewardTool(t, srv, "steward-1", "swarm_report_get", map[string]any{"bee_id": "bee-missing"})
	if strings.Contains(text, "Unknown tool") {
		t.Fatalf("总管调用 swarm_report_get 不应被白名单拦截: %q error=%v", text, isError)
	}
}

func TestStewardCrossProjectQueries(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var listProject, statusProject string
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.ListAttention = func(_ context.Context, _ bool, projectID string, _ int) ([]AttentionItem, error) {
		listProject = projectID
		return nil, nil
	}
	hooks.ThreadStatus = func(_ context.Context, projectID string) ([]ThreadStatusItem, error) {
		statusProject = projectID
		return nil, nil
	}
	srv.SetSteward(hooks)

	// 分诊要跨项目：这两个工具传其他项目的 ID 应直接把该值交给钩子。
	const otherProject = "proj-test-2"
	text, isError := callStewardTool(t, srv, "steward-1", stewardAttentionListToolName, map[string]any{"project_id": otherProject})
	if isError || listProject != otherProject {
		t.Fatalf("attention_list 应能查其他项目: %q error=%v 收到的 project_id 为 %q", text, isError, listProject)
	}
	text, isError = callStewardTool(t, srv, "steward-1", stewardThreadStatusToolName, map[string]any{"project_id": otherProject})
	if isError || statusProject != otherProject {
		t.Fatalf("thread_status 应能查其他项目: %q error=%v 收到的 project_id 为 %q", text, isError, statusProject)
	}

	// 其他带 project_id 的白名单只读工具不豁免：跨项目仍被拒绝，当前项目照常可用。
	if stewardCrossProjectTools["kanban_read_board"] {
		t.Fatal("kanban_read_board 不应跳过 project_id 的当前项目校验")
	}
	text, isError = callStewardTool(t, srv, "steward-1", "kanban_read_board", map[string]any{"project_id": otherProject})
	if !isError || !strings.Contains(text, "project_id must match") {
		t.Fatalf("总管用其他项目调用 kanban_read_board 应被拒绝: %q error=%v", text, isError)
	}
	if _, isError = callStewardTool(t, srv, "steward-1", "kanban_read_board", map[string]any{"project_id": "proj-test-1"}); isError {
		t.Fatal("总管用当前项目调用 kanban_read_board 应成功")
	}
}

func TestOrdinarySessionWriteToolsUnchanged(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")
	srv.SetSteward(newStewardTestHooks(nil, nil, nil))

	// 普通会话不受总管白名单影响：写工具照旧进入 handler，由工具自身校验参数。
	text, isError := callStewardTool(t, srv, "lead-1", "kanban_update_card", map[string]any{"task_id": "missing-task", "title": "x"})
	if isError && strings.Contains(text, "Unknown tool") {
		t.Fatalf("普通会话调用 kanban_update_card 不应被总管白名单拦截: %q", text)
	}
	// 总管专属工具对普通会话仍按未知工具拒绝，与改动前一致。
	text, isError = callStewardTool(t, srv, "lead-1", stewardAttentionListToolName, map[string]any{})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("普通会话调用 attention_list 应按未知工具拒绝: %q error=%v", text, isError)
	}
}

// stewardActionCall 记录动作钩子收到的参数，用于校验它们被原样透传。
type stewardActionCall struct {
	count               int
	threadID, text      string
	reason              string
	automationID, runID string
	taskID, status      string
}

// newStewardActionHooks 返回一套总管动作钩子；canAct 决定 CanAct 是否放行，err 非空时每次调用都返回它。
func newStewardActionHooks(canAct bool, call *stewardActionCall, err error) *StewardHooks {
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.CanAct = func() bool { return canAct }
	hooks.SendMessage = func(_ context.Context, threadID, text, reason string) error {
		call.count++
		call.threadID, call.text, call.reason = threadID, text, reason
		return err
	}
	hooks.InterruptThread = func(_ context.Context, threadID, reason string) error {
		call.count++
		call.threadID, call.reason = threadID, reason
		return err
	}
	hooks.RunAutomation = func(_ context.Context, automationID, reason string) (string, error) {
		call.count++
		call.automationID, call.reason = automationID, reason
		if err != nil {
			return "", err
		}
		call.runID = "run-1"
		return call.runID, nil
	}
	hooks.MoveKanbanTask = func(_ context.Context, taskID, status, reason string) error {
		call.count++
		call.taskID, call.status, call.reason = taskID, status, reason
		return err
	}
	return hooks
}

// stewardActionArgs 返回四个动作工具的一组合法参数。
func stewardActionArgs() map[string]map[string]any {
	return map[string]map[string]any{
		stewardThreadSendMessageToolName: {"thread_id": "lead-1", "text": "quota is back, continue", "reason": "resume after quota"},
		stewardThreadInterruptToolName:   {"thread_id": "lead-1", "reason": "looping on the same tool"},
		stewardAutomationRunNowToolName:  {"automation_id": "auto-1", "reason": "retry after provider error"},
		stewardKanbanMoveTaskToolName:    {"task_id": "task-1", "status": "todo", "reason": "requeue after failure"},
	}
}

// TestStewardActionToolsRequireCanAct 校验 CanAct 为假（总管未启用）时四个工具既不可见、也不可调用。
func TestStewardActionToolsRequireCanAct(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	cases := []struct {
		name   string
		canAct func() bool
	}{
		{"CanAct 未接线", nil},
		{"CanAct 为假", func() bool { return false }},
	}
	for _, c := range cases {
		var call stewardActionCall
		hooks := newStewardActionHooks(true, &call, nil)
		hooks.CanAct = c.canAct
		srv.SetSteward(hooks)

		names := threadToolNames(t, srv, "steward-1")
		for name, args := range stewardActionArgs() {
			if names[name] {
				t.Fatalf("%s: 总管未启用时不应列出 %s", c.name, name)
			}
			text, isError := callStewardTool(t, srv, "steward-1", name, args)
			if !isError {
				t.Fatalf("%s: 总管未启用时 %s 应被拒绝: %q", c.name, name, text)
			}
			if strings.Contains(text, "Unknown tool") {
				t.Fatalf("%s: %s 应返回权限错误而不是未知工具: %q", c.name, name, text)
			}
			if !strings.Contains(text, "总管未启用") {
				t.Fatalf("%s: %s 的错误应说明总管未启用: %q", c.name, name, text)
			}
			// 报错要指向设置页里真实的入口；“设置 → 总管”与页面标题一致。
			if !strings.Contains(text, "设置 → 总管") {
				t.Fatalf("%s: %s 的错误应指向「设置 → 总管」: %q", c.name, name, text)
			}
			if code := toolErrorCode(ToolResult{Text: text, IsError: true}); code != CodeForbidden {
				t.Fatalf("%s: %s 的错误码期望 %s, 实际为 %q", c.name, name, CodeForbidden, code)
			}
		}
		if call.count != 0 {
			t.Fatalf("%s: 权限不足时不应调用钩子, 实际调用 %d 次", c.name, call.count)
		}
		// 启用开关只影响动作工具，观察类工具照旧可用。
		if !names[stewardAttentionListToolName] || !names[stewardNotifyOwnerToolName] {
			t.Fatalf("%s: 观察与通知工具不应受启用开关影响", c.name)
		}
	}
}

// TestStewardActionToolsPassThrough 校验总管启用时动作工具全部可见，且参数原样交给钩子。
func TestStewardActionToolsPassThrough(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(true, &call, nil))

	names := threadToolNames(t, srv, "steward-1")
	for name := range stewardActionToolNames {
		if !names[name] {
			t.Fatalf("总管启用时应列出 %s", name)
		}
	}

	text, isError := callStewardTool(t, srv, "steward-1", stewardThreadSendMessageToolName, map[string]any{
		"thread_id": " lead-1 ", "text": " quota is back, continue ", "reason": " resume after quota ",
	})
	if isError {
		t.Fatalf("thread_send_message 调用失败: %q", text)
	}
	if call.threadID != "lead-1" || call.text != "quota is back, continue" || call.reason != "resume after quota" {
		t.Fatalf("thread_send_message 参数透传异常: %+v", call)
	}
	if !strings.Contains(text, "lead-1") {
		t.Fatalf("thread_send_message 结果应说明消息进了哪个会话: %q", text)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardThreadInterruptToolName, map[string]any{
		"thread_id": "lead-1", "reason": "looping on the same tool",
	})
	if isError {
		t.Fatalf("thread_interrupt 调用失败: %q", text)
	}
	if call.threadID != "lead-1" || call.reason != "looping on the same tool" {
		t.Fatalf("thread_interrupt 参数透传异常: %+v", call)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardAutomationRunNowToolName, map[string]any{
		"automation_id": "auto-1", "reason": "retry after provider error",
	})
	if isError {
		t.Fatalf("automation_run_now 调用失败: %q", text)
	}
	if call.automationID != "auto-1" || call.reason != "retry after provider error" {
		t.Fatalf("automation_run_now 参数透传异常: %+v", call)
	}
	payload := decodeStewardPayload(t, text)
	if payload["runId"] != "run-1" || payload["automationId"] != "auto-1" {
		t.Fatalf("automation_run_now 应返回 runId: %v", payload)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardKanbanMoveTaskToolName, map[string]any{
		"task_id": " task-1 ", "status": " todo ", "reason": " requeue after failure ",
	})
	if isError {
		t.Fatalf("kanban_move_task 调用失败: %q", text)
	}
	if call.taskID != "task-1" || call.status != "todo" || call.reason != "requeue after failure" {
		t.Fatalf("kanban_move_task 参数透传异常: %+v", call)
	}

	// 四种允许的状态都要能交给钩子。
	for _, status := range []string{"todo", "review", "done", "rejected"} {
		if _, isError := callStewardTool(t, srv, "steward-1", stewardKanbanMoveTaskToolName, map[string]any{
			"task_id": "task-1", "status": status, "reason": "triage",
		}); isError {
			t.Fatalf("kanban_move_task 应接受状态 %s", status)
		}
		if call.status != status {
			t.Fatalf("kanban_move_task 状态透传异常: 期望 %s, 实际 %q", status, call.status)
		}
	}
}

// TestStewardAutomationCreateVisibleOnlyWhenCanAct 校验 automation_create 现在是动作工具：
// 总管可写时在 tools/list 里可见且能建自动化；不可写时不可见，直接调用也按权限错误拒绝。
func TestStewardAutomationCreateVisibleOnlyWhenCanAct(t *testing.T) {
	createArgs := func() map[string]any {
		return map[string]any{
			"name":           "每早看板巡检",
			"prompt":         "看一下看板上的失败任务，有就总结给我",
			"schedule_type":  "daily",
			"schedule_value": "09:00",
		}
	}

	for _, c := range []struct {
		name   string
		canAct func() bool
	}{
		{"CanAct 未接线", nil},
		{"CanAct 为假", func() bool { return false }},
	} {
		srv, st, _ := setupTestMCP(t)
		registerThread(t, st, "steward-1", "proj-test-1")
		var call stewardActionCall
		hooks := newStewardActionHooks(false, &call, nil)
		hooks.CanAct = c.canAct
		srv.SetSteward(hooks)

		if names := threadToolNames(t, srv, "steward-1"); names[stewardAutomationCreateToolName] {
			t.Fatalf("%s: 总管不可写时不应列出 automation_create", c.name)
		}
		text, isError := callStewardTool(t, srv, "steward-1", stewardAutomationCreateToolName, createArgs())
		if !isError {
			t.Fatalf("%s: 总管不可写时 automation_create 应被拒绝: %q", c.name, text)
		}
		if strings.Contains(text, "Unknown tool") {
			t.Fatalf("%s: 应返回权限错误而不是未知工具: %q", c.name, text)
		}
		if !strings.Contains(text, "总管未启用") {
			t.Fatalf("%s: 错误应说明总管未启用: %q", c.name, text)
		}
	}

	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(true, &call, nil))
	ctx := context.Background()

	if names := threadToolNames(t, srv, "steward-1"); !names[stewardAutomationCreateToolName] {
		t.Fatal("总管可写时应列出 automation_create")
	}

	// 不传 project_id 时建在当前会话的项目里。
	text, isError := callStewardTool(t, srv, "steward-1", stewardAutomationCreateToolName, createArgs())
	if isError {
		t.Fatalf("总管可写时 automation_create 应成功: %q", text)
	}
	assertAutomationProject(t, ctx, st, decodeStewardPayload(t, text), "proj-test-1")

	// 传另一个项目的 ID：总管可以跨项目建；未知 ID 要报参数错而不是建错项目。
	if err := st.UpsertProject(ctx, store.Project{ID: "proj-test-2", Name: "other-proj", Path: t.TempDir()}); err != nil {
		t.Fatalf("插入第二个项目失败: %v", err)
	}
	crossArgs := createArgs()
	crossArgs["project_id"] = "proj-test-2"
	text, isError = callStewardTool(t, srv, "steward-1", stewardAutomationCreateToolName, crossArgs)
	if isError {
		t.Fatalf("总管应按 project_id 跨项目建自动化: %q", text)
	}
	assertAutomationProject(t, ctx, st, decodeStewardPayload(t, text), "proj-test-2")

	crossArgs["project_id"] = "proj-missing"
	text, isError = callStewardTool(t, srv, "steward-1", stewardAutomationCreateToolName, crossArgs)
	if !isError || !strings.Contains(text, "Unknown project_id") {
		t.Fatalf("未知项目应报参数错: %q error=%v", text, isError)
	}
}

// TestOrdinarySessionSeesSharedAutomationCreate 校验 automation_create 是总管与普通会话共用的动作工具：
// 普通会话照旧可见，其他总管动作工具仍被排除，总管未启用动作时总管自己也看不到它。
func TestOrdinarySessionSeesSharedAutomationCreate(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")

	// 总管未启用动作（CanAct 为假）时，共享动作工具只对普通会话可见。
	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(false, &call, nil))

	names := threadToolNames(t, srv, "lead-1")
	if !names[automationFamily.entry] {
		t.Fatal("普通会话应能看到 automation 入口")
	}
	if names[stewardAutomationCreateToolName] {
		t.Fatal("普通会话不应再列出类型化 automation_create")
	}
	for name := range stewardActionToolNames {
		if stewardSharedActionToolNames[name] {
			continue
		}
		if names[name] {
			t.Fatalf("普通会话不应列出总管动作工具 %s", name)
		}
	}

	if threadToolNames(t, srv, "steward-1")[stewardAutomationCreateToolName] {
		t.Fatal("总管未启用动作时不应列出 automation_create")
	}
}

// assertAutomationProject 校验新自动化落在期望的项目里：结果不带项目字段，因此回查存储。
func assertAutomationProject(t *testing.T, ctx context.Context, st *store.Store, payload map[string]any, want string) {
	t.Helper()
	id, _ := payload["automation_id"].(string)
	if id == "" {
		t.Fatalf("结果应带 automation_id: %v", payload)
	}
	created, err := st.GetAutomation(ctx, id)
	if err != nil {
		t.Fatalf("回查自动化失败: %v", err)
	}
	if created.ProjectID == nil || *created.ProjectID != want {
		t.Fatalf("自动化应建在项目 %s，实际 %v", want, created.ProjectID)
	}
}

// TestStewardActionToolValidation 校验四个动作工具的参数校验，且参数无效时不调用钩子。
func TestStewardActionToolValidation(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(true, &call, nil))

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"缺 thread_id", stewardThreadSendMessageToolName, map[string]any{"text": "t", "reason": "r"}},
		{"thread_id 为空白", stewardThreadSendMessageToolName, map[string]any{"thread_id": "  ", "text": "t", "reason": "r"}},
		{"缺 text", stewardThreadSendMessageToolName, map[string]any{"thread_id": "lead-1", "reason": "r"}},
		{"text 为空白", stewardThreadSendMessageToolName, map[string]any{"thread_id": "lead-1", "text": "   ", "reason": "r"}},
		{"text 超长", stewardThreadSendMessageToolName, map[string]any{"thread_id": "lead-1", "text": strings.Repeat("文", stewardSendMessageTextMaxRunes+1), "reason": "r"}},
		{"缺 reason", stewardThreadSendMessageToolName, map[string]any{"thread_id": "lead-1", "text": "t"}},
		{"reason 超长", stewardThreadInterruptToolName, map[string]any{"thread_id": "lead-1", "reason": strings.Repeat("由", stewardActionReasonMaxRunes+1)}},
		{"中断缺 thread_id", stewardThreadInterruptToolName, map[string]any{"reason": "r"}},
		{"中断缺 reason", stewardThreadInterruptToolName, map[string]any{"thread_id": "lead-1"}},
		{"缺 automation_id", stewardAutomationRunNowToolName, map[string]any{"reason": "r"}},
		{"跑自动化缺 reason", stewardAutomationRunNowToolName, map[string]any{"automation_id": "auto-1"}},
		{"缺 task_id", stewardKanbanMoveTaskToolName, map[string]any{"status": "todo", "reason": "r"}},
		{"缺 status", stewardKanbanMoveTaskToolName, map[string]any{"task_id": "task-1", "reason": "r"}},
		{"status 非法", stewardKanbanMoveTaskToolName, map[string]any{"task_id": "task-1", "status": "in_progress", "reason": "r"}},
		{"status 大小写不符", stewardKanbanMoveTaskToolName, map[string]any{"task_id": "task-1", "status": "Todo", "reason": "r"}},
		{"移动缺 reason", stewardKanbanMoveTaskToolName, map[string]any{"task_id": "task-1", "status": "todo"}},
		{"text 类型错误", stewardThreadSendMessageToolName, map[string]any{"thread_id": "lead-1", "text": float64(1), "reason": "r"}},
		{"未知参数", stewardThreadSendMessageToolName, map[string]any{"thread_id": "lead-1", "text": "t", "reason": "r", "interrupt": true}},
	}
	for _, c := range cases {
		text, isError := callStewardTool(t, srv, "steward-1", c.tool, c.args)
		if !isError {
			t.Fatalf("%s: 期望参数错误, 实际为 %q", c.name, text)
		}
		if code := toolErrorCode(ToolResult{Text: text, IsError: true}); code != CodeInvalidArguments && code != CodeUnknownField {
			t.Fatalf("%s: 期望参数错误码, 实际为 %q (%q)", c.name, code, text)
		}
	}
	if call.count != 0 {
		t.Fatalf("参数无效时不应调用钩子, 实际调用 %d 次", call.count)
	}

	// 边界值：正好等于上限的消息与理由可以交付。
	text, isError := callStewardTool(t, srv, "steward-1", stewardThreadSendMessageToolName, map[string]any{
		"thread_id": "lead-1",
		"text":      strings.Repeat("文", stewardSendMessageTextMaxRunes),
		"reason":    strings.Repeat("由", stewardActionReasonMaxRunes),
	})
	if isError {
		t.Fatalf("正好等于上限时应成功: %q", text)
	}
	if call.count != 1 {
		t.Fatalf("边界调用应触发一次钩子, 实际 %d 次", call.count)
	}
}

// TestStewardActionToolErrorsFromHooks 校验钩子错误原样透传，宿主护栏的拒绝理由能到总管手里。
func TestStewardActionToolErrorsFromHooks(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	const refusal = "guard rails refused this action: the conversation is waiting for the user's approval"
	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(true, &call, errors.New(refusal)))

	for name, args := range stewardActionArgs() {
		text, isError := callStewardTool(t, srv, "steward-1", name, args)
		if !isError {
			t.Fatalf("%s 钩子报错时应作为工具错误返回: %q", name, text)
		}
		if !strings.Contains(text, refusal) {
			t.Fatalf("%s 应把钩子错误原样透传: %q", name, text)
		}
	}
	if call.count != len(stewardActionArgs()) {
		t.Fatalf("每个动作工具都应调到钩子, 实际 %d 次", call.count)
	}
}

// TestStewardToolDescriptionsCarryTurnAndFieldConstraints 锁定从身份段移进工具描述的约束：
// 「用户亲自发消息的那一轮」与 pendingRequests 必须写在对应工具描述里，模型才看得到。
func TestStewardToolDescriptionsCarryTurnAndFieldConstraints(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(true, &call, nil))
	descriptions := threadToolDescriptions(t, srv, "steward-1")
	for _, name := range []string{stewardAssignTaskToolName, stewardPlanRequestToolName} {
		if !strings.Contains(descriptions[name], "用户亲自发消息的那一轮") {
			t.Fatalf("%s 的描述应写明只能在用户亲自发消息的那一轮调用: %q", name, descriptions[name])
		}
	}
	if !strings.Contains(descriptions[stewardApprovalSuggestToolName], "pendingRequests") {
		t.Fatalf("approval_suggest 的描述应指明 request_id 取自 pendingRequests: %q", descriptions[stewardApprovalSuggestToolName])
	}
}

// TestStewardToolDescriptionsAreChinese 锁定总管、话题与动作工具的描述全部是中文：
// 逐个遍历已注册工具，断言描述与参数说明的首字符都不是 ASCII 字母。
func TestStewardToolDescriptionsAreChinese(t *testing.T) {
	srv, _, _ := setupTestMCP(t)
	expected := make(map[string]bool)
	for _, names := range []map[string]bool{stewardToolNames, stewardTopicToolNames, stewardActionToolNames} {
		for name := range names {
			expected[name] = true
		}
	}
	checked := 0
	for _, tool := range srv.registry.ListTools() {
		if !expected[tool.Name] {
			continue
		}
		checked++
		assertChineseDescription(t, tool.Name, tool.Description)
		props, _ := tool.InputSchema["properties"].(map[string]any)
		for propName, raw := range props {
			prop, _ := raw.(map[string]any)
			if desc, _ := prop["description"].(string); desc != "" {
				assertChineseDescription(t, tool.Name+"."+propName, desc)
			}
		}
	}
	if checked != len(expected) {
		t.Fatalf("只检查到 %d 个总管工具，登记了 %d 个", checked, len(expected))
	}
}

// assertChineseDescription 断言描述首字符不是 ASCII 字母：描述一律用中文，避免中英混杂。
func assertChineseDescription(t *testing.T, name, desc string) {
	t.Helper()
	if strings.TrimSpace(desc) == "" {
		t.Fatalf("%s 缺少描述", name)
	}
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(desc))
	if r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
		t.Fatalf("%s 的描述应以中文开头: %q", name, desc)
	}
}

// threadToolDescriptions 以线程作用域发起 tools/list 并返回工具名到描述的对应关系。
func threadToolDescriptions(t *testing.T, srv *Server, threadID string) map[string]string {
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
	descriptions := make(map[string]string, len(toolsArray))
	for _, item := range toolsArray {
		tm, _ := item.(map[string]any)
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		if name != "" {
			descriptions[name] = desc
		}
	}
	return descriptions
}

// TestStewardActionToolsHiddenForNonSteward 校验非总管的会话看不到也调不了动作工具。
func TestStewardActionToolsHiddenForNonSteward(t *testing.T) {
	srv, st, projDir := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")

	// 普通会话的工具列表在接入允许动手的总管钩子前后必须完全一致。
	before := threadToolNames(t, srv, "lead-1")
	var call stewardActionCall
	srv.SetSteward(newStewardActionHooks(true, &call, nil))
	after := threadToolNames(t, srv, "lead-1")
	if !stewardSameNames(before, after) {
		t.Fatalf("普通会话的工具列表被动作工具改动: 前 %v 后 %v", before, after)
	}

	for name, args := range stewardActionArgs() {
		if after[name] {
			t.Fatalf("非总管的会话不应列出 %s", name)
		}
		text, isError := callStewardTool(t, srv, "lead-1", name, args)
		if !isError || !strings.Contains(text, "Unknown tool") {
			t.Fatalf("非总管调用 %s 应按未知工具拒绝: %q error=%v", name, text, isError)
		}
	}
	if call.count != 0 {
		t.Fatalf("非总管调用不应触发钩子, 实际 %d 次", call.count)
	}

	// 没有会话作用域的共享端点同样看不到动作工具；共用的 automation_create 不算总管专属，照旧可见。
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	sharedNames := listToolNames(t, ts.URL+"/mcp", srv.Token(), projDir)
	for name := range stewardActionToolNames {
		if stewardSharedActionToolNames[name] {
			continue
		}
		if sharedNames[name] {
			t.Fatalf("共享端点不应列出 %s", name)
		}
	}
	if !sharedNames[stewardAutomationCreateToolName] {
		t.Fatal("共享端点应照旧列出共用的 automation_create")
	}
}

// TestStewardActionToolsReportNotEnabled 校验 CanAct 放行但钩子未接线时报未启用。
func TestStewardActionToolsReportNotEnabled(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	// 只接 IsSteward 与 CanAct：动作工具可见，但每个都报未启用。
	srv.SetSteward(&StewardHooks{
		IsSteward: func(threadID string) bool { return threadID == "steward-1" },
		CanAct:    func() bool { return true },
	})
	names := threadToolNames(t, srv, "steward-1")
	for name, args := range stewardActionArgs() {
		if !names[name] {
			t.Fatalf("总管启用时应列出 %s", name)
		}
		text, isError := callStewardTool(t, srv, "steward-1", name, args)
		if !isError || !strings.Contains(text, "not enabled") {
			t.Fatalf("%s 未接线时应报未启用: %q error=%v", name, text, isError)
		}
	}
}

// approvalSuggestCall 记录 SuggestApproval 钩子收到的参数。
type approvalSuggestCall struct {
	count                                 int
	threadID, requestID, optionID, reason string
}

// newApprovalSuggestHooks 返回接好 SuggestApproval 的总管钩子；canAct 决定权限档，err 非空时钩子返回它。
func newApprovalSuggestHooks(canAct bool, call *approvalSuggestCall, err error) *StewardHooks {
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.CanAct = func() bool { return canAct }
	hooks.SuggestApproval = func(_ context.Context, threadID, requestID, optionID, reason string) error {
		call.count++
		call.threadID, call.requestID, call.optionID, call.reason = threadID, requestID, optionID, reason
		return err
	}
	return hooks
}

// approvalSuggestArgs 返回 approval_suggest 的一组合法参数。
func approvalSuggestArgs() map[string]any {
	return map[string]any{"thread_id": "lead-1", "request_id": "req-1", "option_id": "allow", "reason": "read-only command"}
}

// TestApprovalSuggestVisibleInBothLevels 校验 CanAct 为真或为假时都能看到 approval_suggest，且参数 trim 后原样交给钩子。
func TestApprovalSuggestVisibleInBothLevels(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	for _, canAct := range []bool{false, true} {
		var call approvalSuggestCall
		srv.SetSteward(newApprovalSuggestHooks(canAct, &call, nil))

		if !threadToolNames(t, srv, "steward-1")[stewardApprovalSuggestToolName] {
			t.Fatalf("CanAct=%v: 总管的会话应列出 approval_suggest", canAct)
		}
		text, isError := callStewardTool(t, srv, "steward-1", stewardApprovalSuggestToolName, map[string]any{
			"thread_id": " lead-1 ", "request_id": " req-1 ", "option_id": " deny ", "reason": " writes outside the project ",
		})
		if isError {
			t.Fatalf("CanAct=%v: approval_suggest 调用失败: %q", canAct, text)
		}
		if call.count != 1 || call.threadID != "lead-1" || call.requestID != "req-1" ||
			call.optionID != "deny" || call.reason != "writes outside the project" {
			t.Fatalf("CanAct=%v: approval_suggest 参数透传异常: %+v", canAct, call)
		}
		payload := decodeStewardPayload(t, text)
		for key, want := range map[string]any{"ok": true, "threadId": "lead-1", "requestId": "req-1", "optionId": "deny"} {
			if payload[key] != want {
				t.Fatalf("CanAct=%v: approval_suggest 结果字段 %s 期望 %v, 实际为 %v", canAct, key, want, payload[key])
			}
		}

		// 同一请求重复建议不能被幂等缓存拦下：换个选项再提一次仍应交给钩子。
		args := approvalSuggestArgs()
		if text, isError := callStewardTool(t, srv, "steward-1", stewardApprovalSuggestToolName, args); isError {
			t.Fatalf("CanAct=%v: 同一请求重复建议应成功: %q", canAct, text)
		}
		if call.count != 2 || call.optionID != "allow" {
			t.Fatalf("CanAct=%v: 重复建议应覆盖为 allow: %+v", canAct, call)
		}
	}
}

// TestApprovalSuggestValidation 校验 approval_suggest 的参数校验，且参数无效时不调用钩子。
func TestApprovalSuggestValidation(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	var call approvalSuggestCall
	srv.SetSteward(newApprovalSuggestHooks(false, &call, nil))

	with := func(key string, value any) map[string]any {
		args := approvalSuggestArgs()
		if value == nil {
			delete(args, key)
		} else {
			args[key] = value
		}
		return args
	}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"缺 thread_id", with("thread_id", nil)},
		{"缺 request_id", with("request_id", nil)},
		{"request_id 为空白", with("request_id", "  ")},
		{"缺 option_id", with("option_id", nil)},
		{"option_id 非法", with("option_id", "allow_always")},
		{"option_id 大小写不符", with("option_id", "Allow")},
		{"缺 reason", with("reason", nil)},
		{"reason 超长", with("reason", strings.Repeat("由", stewardActionReasonMaxRunes+1))},
		{"option_id 类型错误", with("option_id", true)},
		{"未知参数", with("execute", true)},
	}
	for _, c := range cases {
		text, isError := callStewardTool(t, srv, "steward-1", stewardApprovalSuggestToolName, c.args)
		if !isError {
			t.Fatalf("%s: 期望参数错误, 实际为 %q", c.name, text)
		}
		if code := toolErrorCode(ToolResult{Text: text, IsError: true}); code != CodeInvalidArguments && code != CodeUnknownField {
			t.Fatalf("%s: 期望参数错误码, 实际为 %q (%q)", c.name, code, text)
		}
	}
	if call.count != 0 {
		t.Fatalf("参数无效时不应调用钩子, 实际调用 %d 次", call.count)
	}

	// 边界值：理由正好等于上限时可以提交。
	if text, isError := callStewardTool(t, srv, "steward-1", stewardApprovalSuggestToolName,
		with("reason", strings.Repeat("由", stewardActionReasonMaxRunes))); isError {
		t.Fatalf("理由正好等于上限时应成功: %q", text)
	}
}

// TestApprovalSuggestHookErrorsAndNotEnabled 校验钩子错误原样透传，钩子未接线时报未启用。
func TestApprovalSuggestHookErrorsAndNotEnabled(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	const refusal = "request req-1 is a plan approval, which the steward cannot suggest"
	var call approvalSuggestCall
	srv.SetSteward(newApprovalSuggestHooks(false, &call, errors.New(refusal)))
	text, isError := callStewardTool(t, srv, "steward-1", stewardApprovalSuggestToolName, approvalSuggestArgs())
	if !isError || !strings.Contains(text, refusal) {
		t.Fatalf("approval_suggest 应把钩子错误原样透传: %q error=%v", text, isError)
	}

	srv.SetSteward(&StewardHooks{IsSteward: func(threadID string) bool { return threadID == "steward-1" }})
	if !threadToolNames(t, srv, "steward-1")[stewardApprovalSuggestToolName] {
		t.Fatal("钩子未接线时总管的会话仍应列出 approval_suggest")
	}
	text, isError = callStewardTool(t, srv, "steward-1", stewardApprovalSuggestToolName, approvalSuggestArgs())
	if !isError || !strings.Contains(text, "not enabled") {
		t.Fatalf("approval_suggest 未接线时应报未启用: %q error=%v", text, isError)
	}
}

// TestApprovalSuggestHiddenForNonSteward 校验非总管的会话看不到也调不了 approval_suggest。
func TestApprovalSuggestHiddenForNonSteward(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")

	var call approvalSuggestCall
	srv.SetSteward(newApprovalSuggestHooks(true, &call, nil))
	if threadToolNames(t, srv, "lead-1")[stewardApprovalSuggestToolName] {
		t.Fatal("非总管的会话不应列出 approval_suggest")
	}
	text, isError := callStewardTool(t, srv, "lead-1", stewardApprovalSuggestToolName, approvalSuggestArgs())
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("非总管调用 approval_suggest 应按未知工具拒绝: %q error=%v", text, isError)
	}
	if call.count != 0 {
		t.Fatalf("非总管调用不应触发钩子, 实际 %d 次", call.count)
	}
}

// TestThreadStatusSerializesPendingRequests 校验 thread_status 输出待审批请求，字段名为 camelCase。
func TestThreadStatusSerializesPendingRequests(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")

	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.ThreadStatus = func(context.Context, string) ([]ThreadStatusItem, error) {
		return []ThreadStatusItem{
			{
				ThreadID:         "lead-1",
				AwaitingApproval: true,
				PendingRequests: []StewardPendingRequest{{
					RequestID:  "req-1",
					Kind:       "tool",
					Title:      "Run shell command",
					Detail:     "go test ./...",
					Options:    []string{"allow", "allow_always", "deny"},
					Suggestion: "allow",
				}},
			},
			{ThreadID: "lead-2"},
		}, nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "steward-1", stewardThreadStatusToolName, map[string]any{})
	if isError {
		t.Fatalf("thread_status 调用失败: %q", text)
	}
	threads, _ := decodeStewardPayload(t, text)["threads"].([]any)
	if len(threads) != 2 {
		t.Fatalf("thread_status threads 期望 2 条, 实际为 %q", text)
	}
	first, _ := threads[0].(map[string]any)
	pending, _ := first["pendingRequests"].([]any)
	if len(pending) != 1 {
		t.Fatalf("thread_status 应输出 pendingRequests: %q", text)
	}
	req, _ := pending[0].(map[string]any)
	for key, want := range map[string]any{
		"requestId":  "req-1",
		"kind":       "tool",
		"title":      "Run shell command",
		"detail":     "go test ./...",
		"suggestion": "allow",
	} {
		if req[key] != want {
			t.Fatalf("pendingRequests 字段 %s 期望 %v, 实际为 %v", key, want, req[key])
		}
	}
	if options, _ := req["options"].([]any); len(options) != 3 || options[1] != "allow_always" {
		t.Fatalf("pendingRequests options 异常: %v", req["options"])
	}
	// 没有待审批请求的会话不输出 pendingRequests。
	second, _ := threads[1].(map[string]any)
	if _, ok := second["pendingRequests"]; ok {
		t.Fatalf("没有待审批请求时不应输出 pendingRequests: %v", second)
	}
}

// newTopicScopeHooks 返回一套话题会话的动作钩子；owns 决定归属判定结果。
func newTopicScopeHooks(canAct bool, call *stewardActionCall, owns func(subjectType, subjectID string) bool) *StewardHooks {
	hooks := newStewardActionHooks(canAct, call, nil)
	hooks.IsTopic = func(threadID string) bool { return threadID == "topic-1" }
	hooks.TopicOwnsSubject = func(_ context.Context, _, subjectType, subjectID string) (bool, error) {
		return owns(subjectType, subjectID), nil
	}
	return hooks
}

// TestStewardTopicActionToolsVisibility 校验话题动作工具只在计划角色且能动手时出现。
func TestStewardTopicActionToolsVisibility(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call stewardActionCall
	srv.SetSteward(newTopicScopeHooks(true, &call, func(string, string) bool { return true }))

	// 还没建目标时是普通角色，即使能动手也不给动作工具。
	names := threadToolNames(t, srv, "topic-1")
	for name := range stewardTopicActionTools {
		if names[name] {
			t.Errorf("还没建目标的话题会话不该看到 %s", name)
		}
	}

	planThreadID := "topic-1"
	if err := st.CreateGoal(context.Background(), store.Goal{
		ID:           "goal-1",
		ProjectID:    "proj-test-1",
		Title:        "重构登录",
		Status:       "plan_review",
		PlanThreadID: &planThreadID,
	}); err != nil {
		t.Fatalf("插入目标失败: %v", err)
	}
	names = threadToolNames(t, srv, "topic-1")
	for name := range stewardTopicActionTools {
		if !names[name] {
			t.Errorf("计划角色且能动手的话题会话应看到 %s", name)
		}
	}

	// 总管未启用时动作工具消失。
	srv.SetSteward(newTopicScopeHooks(false, &call, func(string, string) bool { return true }))
	names = threadToolNames(t, srv, "topic-1")
	for name := range stewardTopicActionTools {
		if names[name] {
			t.Errorf("总管未启用时话题会话不该看到 %s", name)
		}
	}
}

// TestStewardTopicActionToolsScoped 校验话题会话只能操作本目标下的对象。
func TestStewardTopicActionToolsScoped(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call stewardActionCall
	var checked []string
	srv.SetSteward(newTopicScopeHooks(true, &call, func(subjectType, subjectID string) bool {
		checked = append(checked, subjectType+":"+subjectID)
		return subjectType == "thread" && subjectID == "lead-1"
	}))

	text, isError := callStewardTool(t, srv, "topic-1", stewardThreadSendMessageToolName, stewardActionArgs()[stewardThreadSendMessageToolName])
	if isError {
		t.Fatalf("本目标下的会话应允许催促: %q", text)
	}
	if call.count != 1 || call.threadID != "lead-1" {
		t.Fatalf("thread_send_message 参数透传异常: %+v", call)
	}
	if len(checked) != 1 || checked[0] != "thread:lead-1" {
		t.Fatalf("归属判定参数异常: %v", checked)
	}

	call.count = 0
	text, isError = callStewardTool(t, srv, "topic-1", stewardThreadSendMessageToolName, map[string]any{
		"thread_id": "lead-2", "text": "hello", "reason": "nudge",
	})
	if !isError || !strings.Contains(text, "own goal") {
		t.Fatalf("别的目标的会话应被拒绝: %q error=%v", text, isError)
	}
	if call.count != 0 {
		t.Fatalf("被拒绝时不应调用 SendMessage: %+v", call)
	}

	text, isError = callStewardTool(t, srv, "topic-1", stewardKanbanMoveTaskToolName, map[string]any{
		"task_id": "task-2", "status": "todo", "reason": "requeue",
	})
	if !isError || !strings.Contains(text, "own goal") {
		t.Fatalf("别的目标的任务应被拒绝: %q error=%v", text, isError)
	}
	if call.count != 0 {
		t.Fatalf("被拒绝时不应调用 MoveKanbanTask: %+v", call)
	}
}

// TestStewardActionGuardTopicRejectedWhenCannotAct 校验话题会话在 CanAct 为假时拿不到动作工具，
// 且不会真的调用 SendMessage：守卫在 handler 里按未知工具拒绝。
func TestStewardActionGuardTopicRejectedWhenCannotAct(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call stewardActionCall
	srv.SetSteward(newTopicScopeHooks(false, &call, func(string, string) bool { return true }))

	text, isError := callStewardTool(t, srv, "topic-1", stewardThreadSendMessageToolName, stewardActionArgs()[stewardThreadSendMessageToolName])
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("总管未启用时话题会话应被按未知工具拒绝: %q error=%v", text, isError)
	}
	if call.count != 0 {
		t.Fatalf("被拒绝时不应调用 SendMessage: %+v", call)
	}
}

// TestStewardTopicCannotRunAutomation 校验话题会话拿不到 automation_run_now。
func TestStewardTopicCannotRunAutomation(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call stewardActionCall
	srv.SetSteward(newTopicScopeHooks(true, &call, func(string, string) bool { return true }))

	text, isError := callStewardTool(t, srv, "topic-1", stewardAutomationRunNowToolName, stewardActionArgs()[stewardAutomationRunNowToolName])
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("话题会话调用 automation_run_now 应按未知工具拒绝: %q error=%v", text, isError)
	}
	if call.count != 0 {
		t.Fatalf("话题会话不应触发自动化: %+v", call)
	}
}

// TestStewardMainSessionSkipsTopicScope 校验总管主会话调用动作工具时不走话题归属判定。
func TestStewardMainSessionSkipsTopicScope(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	var call stewardActionCall
	checks := 0
	hooks := newStewardActionHooks(true, &call, nil)
	hooks.TopicOwnsSubject = func(_ context.Context, _, _, _ string) (bool, error) {
		checks++
		return false, nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "steward-1", stewardThreadSendMessageToolName, stewardActionArgs()[stewardThreadSendMessageToolName])
	if isError {
		t.Fatalf("总管主会话调用动作工具失败: %q", text)
	}
	if checks != 0 {
		t.Fatalf("总管主会话不应触发话题归属判定，实际 %d 次", checks)
	}
}
