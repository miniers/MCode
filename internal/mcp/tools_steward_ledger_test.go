package mcp

import (
	"context"
	"strings"
	"testing"
)

// ledgerTestHooks 返回一套只接账本的总管钩子：四个账本函数都记录调用，
// 读账本固定返回一条进行中的条目，新增返回带 ID 的条目。
func ledgerTestHooks(calls *ledgerCalls) *StewardHooks {
	hooks := newStewardTestHooks(nil, nil, nil)
	hooks.LedgerGet = func(context.Context) ([]LedgerItem, string, error) {
		if calls != nil {
			calls.gets++
		}
		return []LedgerItem{{
			ID:        "l-1",
			Section:   "in_progress",
			Text:      "写登录流程任务书",
			CreatedAt: "2026-10-31T01:02:03Z",
			UpdatedAt: "2026-10-31T01:02:03Z",
		}}, "### 进行中\n- [l-1] 写登录流程任务书", nil
	}
	hooks.LedgerAdd = func(_ context.Context, section, text, topicThreadID, subjectType, subjectID string) (LedgerItem, error) {
		if calls != nil {
			calls.add = [5]string{section, text, topicThreadID, subjectType, subjectID}
		}
		return LedgerItem{ID: "l-2", Section: section, Text: text, TopicThreadID: topicThreadID,
			SubjectType: subjectType, SubjectID: subjectID}, nil
	}
	hooks.LedgerEdit = func(_ context.Context, id, section, text string) (LedgerItem, error) {
		if calls != nil {
			calls.edit = [3]string{id, section, text}
		}
		return LedgerItem{ID: id, Section: section, Text: text}, nil
	}
	hooks.LedgerRemove = func(_ context.Context, id string) error {
		if calls != nil {
			calls.remove = id
		}
		return nil
	}
	return hooks
}

// ledgerCalls 记录账本工具落到钩子上的参数。
type ledgerCalls struct {
	gets   int
	add    [5]string
	edit   [3]string
	remove string
}

// TestStewardLedgerGet 验证账本读取工具：总管能看到并调用，结果里有条目与渲染文本；
// 普通会话看不到，直接调用按未知工具拒绝。
func TestStewardLedgerGet(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "lead-1", "proj-test-1")
	calls := &ledgerCalls{}
	srv.SetSteward(ledgerTestHooks(calls))

	names := threadToolNames(t, srv, "steward-1")
	for _, name := range []string{stewardLedgerGetToolName, stewardLedgerUpdateToolName} {
		if !names[name] {
			t.Fatalf("总管应当看到 %s", name)
		}
	}
	if lead := threadToolNames(t, srv, "lead-1"); lead[stewardLedgerGetToolName] {
		t.Fatal("普通会话不应看到账本工具")
	}

	text, isError := callStewardTool(t, srv, "steward-1", stewardLedgerGetToolName, map[string]any{})
	if isError {
		t.Fatalf("读取账本失败: %s", text)
	}
	payload := decodeStewardPayload(t, text)
	if payload["count"] != float64(1) {
		t.Fatalf("count 应为 1，实际 %v", payload["count"])
	}
	if rendered, _ := payload["rendered"].(string); !strings.Contains(rendered, "写登录流程任务书") {
		t.Fatalf("rendered 应包含账本内容，实际 %q", rendered)
	}
	entries, _ := payload["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("应当返回一条账本条目，实际 %v", payload["entries"])
	}
	if calls.gets != 1 {
		t.Fatalf("钩子应被调用一次，实际 %d 次", calls.gets)
	}

	if text, isError := callStewardTool(t, srv, "lead-1", stewardLedgerGetToolName, map[string]any{}); !isError ||
		!strings.Contains(text, "Unknown tool") {
		t.Fatalf("普通会话调用账本工具应按未知工具拒绝: %q error=%v", text, isError)
	}
}

// TestStewardLedgerUpdate 验证账本改写工具：add 把分区、正文、话题会话与关联对象透传给钩子；
// update 与 remove 按 ID 定位；非法操作、缺参数、超长正文都被拦下且不落到钩子。
func TestStewardLedgerUpdate(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	calls := &ledgerCalls{}
	srv.SetSteward(ledgerTestHooks(calls))

	text, isError := callStewardTool(t, srv, "steward-1", stewardLedgerUpdateToolName, map[string]any{
		"op":              "add",
		"section":         "pending_user",
		"text":            "  写登录流程任务书  ",
		"topic_thread_id": "topic-1",
		"subject_type":    " kanban_task ",
		"subject_id":      " t1 ",
	})
	if isError {
		t.Fatalf("新增账本条目失败: %s", text)
	}
	if calls.add != [5]string{"pending_user", "写登录流程任务书", "topic-1", "kanban_task", "t1"} {
		t.Fatalf("新增参数没有正确透传: %+v", calls.add)
	}
	payload := decodeStewardPayload(t, text)
	if payload["ok"] != true || payload["op"] != "add" {
		t.Fatalf("新增结果不对: %+v", payload)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardLedgerUpdateToolName, map[string]any{
		"op":      "update",
		"id":      "l-1",
		"section": "decision",
	})
	if isError {
		t.Fatalf("改写账本条目失败: %s", text)
	}
	if calls.edit != [3]string{"l-1", "decision", ""} {
		t.Fatalf("改写参数没有正确透传: %+v", calls.edit)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardLedgerUpdateToolName, map[string]any{
		"op": "remove",
		"id": "l-1",
	})
	if isError {
		t.Fatalf("删除账本条目失败: %s", text)
	}
	if calls.remove != "l-1" {
		t.Fatalf("删除参数没有正确透传: %q", calls.remove)
	}

	invalid := []struct {
		name string
		args map[string]any
		want string
	}{
		{"非法操作", map[string]any{"op": "clear"}, "op"},
		{"非法分区", map[string]any{"op": "add", "section": "todo", "text": "x"}, "section"},
		{"新增缺分区", map[string]any{"op": "add", "text": "x"}, "section"},
		{"新增缺正文", map[string]any{"op": "add", "section": "in_progress"}, "text"},
		{"关联对象只给类型", map[string]any{"op": "add", "section": "pending_user", "text": "x", "subject_type": "kanban_task"}, "subject_id"},
		{"改写缺 ID", map[string]any{"op": "update", "text": "x"}, "id"},
		{"改写没给新内容", map[string]any{"op": "update", "id": "l-1"}, "section"},
		{"正文超长", map[string]any{"op": "add", "section": "in_progress",
			"text": strings.Repeat("长", stewardLedgerTextMaxRunes+1)}, "text"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			text, isError := callStewardTool(t, srv, "steward-1", stewardLedgerUpdateToolName, tc.args)
			if !isError {
				t.Fatalf("%s 应当报错，实际成功: %s", tc.name, text)
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("%s 的报错应提到 %q，实际 %q", tc.name, tc.want, text)
			}
		})
	}
}
