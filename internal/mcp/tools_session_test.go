package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcode/internal/store"
)

func TestSessionGet(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "caller", "proj-test-1")
	ctx := context.Background()
	if err := st.UpsertProject(ctx, store.Project{ID: "other", Name: "Other", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateThread(ctx, store.Thread{ID: "target", ProjectID: "other", Title: "Target", Provider: "test", Model: "model"}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("长", 4001)
	input := strings.Repeat("入", 4001)
	name, status := "shell", "completed"
	for _, msg := range []store.Message{
		{ID: "m1", ThreadID: "target", Role: "user", Text: "first"},
		{ID: "m2", ThreadID: "target", Role: "assistant", Text: long},
		{ID: "m3", ThreadID: "target", Role: "tool", Text: "last", ToolName: &name, ToolStatus: &status, ToolInput: &input},
	} {
		if _, err := st.AppendMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args map[string]any
		code string
	}{
		{map[string]any{}, CodeInvalidArguments},
		{map[string]any{"session_id": "  "}, CodeInvalidArguments},
		{map[string]any{"session_id": "missing"}, CodeNotFound},
	} {
		text, isError := callThreadTool(t, srv, "caller", "session_get", tc.args)
		if !isError || !strings.Contains(text, tc.code) {
			t.Fatalf("期望 %s，实际 %s", tc.code, text)
		}
	}
	read := func(args map[string]any) map[string]any {
		t.Helper()
		text, isError := callThreadTool(t, srv, "caller", "session_get", args)
		if isError {
			t.Fatalf("读取失败: %s", text)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	page := read(map[string]any{"session_id": " target ", "limit": 2})
	session := page["session"].(map[string]any)
	if session["id"] != "target" || session["title"] != "Target" || session["project_name"] != "Other" || session["project_path"] == "" || session["model"] != "model" {
		t.Fatalf("元数据错误: %v", session)
	}
	messages := page["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["seq"] != float64(2) || messages[1].(map[string]any)["seq"] != float64(3) || page["has_more"] != true || page["next_before_seq"] != float64(2) {
		t.Fatalf("分页错误: %v", page)
	}
	if messages[0].(map[string]any)["text"] != strings.Repeat("长", 4000)+"…(truncated)" {
		t.Fatal("正文截断错误")
	}
	tool := messages[1].(map[string]any)
	if tool["tool_name"] != name || tool["tool_status"] != status || tool["tool_input"] != strings.Repeat("入", 4000)+"…(truncated)" {
		t.Fatal("工具消息错误")
	}
	older := read(map[string]any{"session_id": "target", "before_seq": page["next_before_seq"], "limit": 2})
	oldMessages := older["messages"].([]any)
	if len(oldMessages) != 1 || oldMessages[0].(map[string]any)["text"] != "first" || older["has_more"] != false {
		t.Fatalf("上一页错误: %v", older)
	}
}
