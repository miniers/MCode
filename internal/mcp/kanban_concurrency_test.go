package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcode/internal/testenv"
)

func TestKanbanWriteGuardsAtTransport(t *testing.T) {
	testenv.TempHome(t)
	srv, _, workDir := setupTestMCP(t)
	calls := 0
	for _, tool := range srv.registry.ListTools() {
		if !tool.IsWrite || len(tool.Name) < 7 || tool.Name[:7] != "kanban_" {
			continue
		}
		tool.Handler = func(ToolContext, map[string]any) ToolResult {
			calls++
			return toolSuccess(map[string]any{"success": true})
		}
		srv.registry.Register(tool)
	}
	call := func(name, sessionID string, args map[string]any) ToolResult {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+srv.Token())
		req.Header.Set(WorkDirHeader, workDir)
		req.Header.Set("Mcp-Session-Id", sessionID)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		var response struct {
			Result struct {
				IsError bool                    `json:"isError"`
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Result.Content) != 1 {
			t.Fatalf("无工具结果: %s", rec.Body.String())
		}
		return ToolResult{IsError: response.Result.IsError, Text: response.Result.Content[0].Text}
	}

	// 不传会话标识的旧客户端也不能绕过限额。
	for _, sessionID := range []string{"caller", ""} {
		key := "mcp:" + sessionID
		if sessionID == "" {
			key = "workdir:proj-test-1:" + workDir
		}
		var releases []func()
		for i := 0; i < 4; i++ {
			release, err := srv.opts.Kanban.AcquireWrite(key, "", "")
			if err != nil {
				t.Fatal(err)
			}
			releases = append(releases, release)
			defer release()
		}
		before := calls
		for _, tool := range srv.registry.ListTools() {
			if !tool.IsWrite || len(tool.Name) < 7 || tool.Name[:7] != "kanban_" {
				continue
			}
			args := map[string]any{}
			required, _ := tool.InputSchema["required"].([]string)
			properties := tool.InputSchema["properties"].(map[string]any)
			for _, field := range required {
				args[field] = "test"
				if field == "request_id" {
					args[field] = "test-" + sessionID
				}
				property := properties[field].(map[string]any)
				if values, ok := property["enum"].([]string); ok {
					args[field] = values[0]
				}
			}
			result := call(tool.Name, sessionID, args)
			if !result.IsError || toolErrorCode(result) != CodeConflict {
				t.Fatalf("%s 应被限流，实际: %+v", tool.Name, result)
			}
		}
		if calls != before {
			t.Fatal("被限流的写操作仍进入处理函数")
		}
		if result := call("kanban_create_draft", "other-caller", map[string]any{"title": "draft"}); result.IsError {
			t.Fatalf("不应影响其他会话: %+v", result)
		}
		for _, release := range releases {
			release()
		}
		// 失败的幂等请求必须释放缓存占位，允许相同请求重试。
		if result := call("kanban_create_task", sessionID, map[string]any{"title": "test", "request_id": "test-" + sessionID}); result.IsError {
			t.Fatalf("释放后重试失败: %+v", result)
		}
	}

	release, err := srv.opts.Kanban.AcquireWrite("owner", "task-one", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	result := call("kanban_set_goal", "other-caller", map[string]any{"task_id": "task-one", "goal": "goal"})
	if !result.IsError || toolErrorCode(result) != CodeConflict {
		t.Fatalf("set_goal 应遵守跨会话任务锁: %+v", result)
	}

	releaseRequest, err := srv.opts.Kanban.AcquireWrite("mcp:caller", "", "shared-request")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRequest()
	result = call("kanban_create_draft", "caller", map[string]any{"title": "draft", "request_id": "shared-request"})
	if !result.IsError || toolErrorCode(result) != CodeConflict {
		t.Fatalf("应拒绝重叠 request_id: %+v", result)
	}
}

func TestInitializeAssignsCallerSession(t *testing.T) {
	testenv.TempHome(t)
	srv, _, _ := setupTestMCP(t)
	previous := ""
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		req.Header.Set("Authorization", "Bearer "+srv.Token())
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		sessionID := rec.Header().Get("Mcp-Session-Id")
		if sessionID == "" || sessionID == previous {
			t.Fatalf("initialize 未分配独立会话: %q", sessionID)
		}
		previous = sessionID
	}
}
