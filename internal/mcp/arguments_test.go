package mcp

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestMCP_EditArgumentsRejectInvalidFieldsBeforeWriting(t *testing.T) {
	srv, st, dir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, test := range []struct {
		name string
		args map[string]any
		code string
	}{
		{"kanban_create_draft", map[string]any{"title": "draft", "goal_id": 42}, CodeInvalidArguments},
		{"kanban_create_draft", map[string]any{"title": "draft", "notes": []string{"invalid"}}, CodeInvalidArguments},
		{"kanban_create_draft", map[string]any{"title": "draft", "request_id": true}, CodeInvalidArguments},
		{"kanban_create_draft", map[string]any{"title": "draft", "unexpected": "value"}, CodeUnknownField},
		{"kanban_update_card", map[string]any{"task_id": "task", "title": true, "notes": "must not be saved"}, CodeInvalidArguments},
	} {
		_, response := postRPC(t, ts.URL+"/mcp", srv.Token(), dir, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": test.name, "arguments": test.args},
		})
		_, isError, payload := parseToolText(t, response)
		if !isError || payload["code"] != test.code {
			t.Fatalf("%s: %v", test.name, payload)
		}
	}
	tasks, err := st.ListTasks(context.Background(), "")
	if err != nil || len(tasks) != 0 {
		t.Fatal("invalid arguments created a task")
	}
}
