package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/swarm"
)

func callSwarmDelegate(t *testing.T, srv *Server, threadID string, args map[string]any) (string, bool) {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": swarmDelegateToolName, "arguments": args},
	})
	if status != 200 {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", status)
	}
	text, isError, _ := parseToolText(t, res)
	return text, isError
}

func TestSwarmToolsAlwaysLoadMeta(t *testing.T) {
	t.Parallel()
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")
	srv.SetSwarm(&SwarmHooks{
		Available: func(string) bool { return true },
		Delegate:  func(context.Context, string, []map[string]any) (string, error) { return "ok", nil },
	})
	status, res := postThreadRPC(t, srv, "lead-1", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if status != 200 {
		t.Fatalf("tools/list 状态码异常: %d", status)
	}
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list 结果异常: %v", res)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list 工具列表异常: %v", result)
	}
	want := map[string]bool{swarmDelegateToolName: false, swarmReportGetToolName: false}
	other := false
	for _, item := range tools {
		tool := item.(map[string]any)
		name := tool["name"].(string)
		meta, exists := tool["_meta"]
		if _, target := want[name]; target {
			fields, ok := meta.(map[string]any)
			if !ok || len(fields) != 1 || fields["anthropic/alwaysLoad"] != true {
				t.Errorf("%s 的 _meta 异常: %v", name, meta)
			}
			want[name] = true
		} else {
			other = true
			if exists {
				t.Errorf("%s 不应包含 _meta: %v", name, meta)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tools/list 缺少 %s", name)
		}
	}
	if !other {
		t.Error("tools/list 缺少其他工具")
	}
}

func TestSwarmDelegateHiddenUntilAvailable(t *testing.T) {
	t.Parallel()
	srv, st, projDir := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")

	if threadToolNames(t, srv, "lead-1")[swarmDelegateToolName] {
		t.Fatal("没有注入编排钩子时不应列出 swarm_delegate")
	}

	available := false
	srv.SetSwarm(&SwarmHooks{
		Available: func(threadID string) bool { return available && threadID == "lead-1" },
		Delegate:  func(context.Context, string, []map[string]any) (string, error) { return "ok", nil },
	})
	if threadToolNames(t, srv, "lead-1")[swarmDelegateToolName] {
		t.Fatal("会话不可用时不应列出 swarm_delegate")
	}

	available = true
	if !threadToolNames(t, srv, "lead-1")[swarmDelegateToolName] {
		t.Fatal("会话可用时应列出 swarm_delegate")
	}

	// 没有会话作用域的共享端点没有线程身份，不能派发。
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if status != 200 {
		t.Fatalf("共享端点 tools/list 状态码异常: %d", status)
	}
	tools, _ := res["result"].(map[string]any)["tools"].([]any)
	for _, item := range tools {
		if name, _ := item.(map[string]any)["name"].(string); name == swarmDelegateToolName {
			t.Fatal("共享端点不应列出 swarm_delegate")
		}
	}
}

func TestSwarmDelegateCallPassesEntriesToHook(t *testing.T) {
	t.Parallel()
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")

	var gotThread string
	var gotEntries []map[string]any
	srv.SetSwarm(&SwarmHooks{
		Available: func(string) bool { return true },
		Delegate: func(_ context.Context, threadID string, entries []map[string]any) (string, error) {
			gotThread, gotEntries = threadID, entries
			return "Started 2 bee(s).", nil
		},
	})

	text, isError := callSwarmDelegate(t, srv, "lead-1", map[string]any{"tasks": []any{
		map[string]any{"prompt": "do a\n```go\nx\n```", "task": "A", "acceptance": []any{" true ", ""}},
		map[string]any{"prompt": "do b", "skills": []any{"review"}, "acceptance": " true "},
	}})
	if isError || text != "Started 2 bee(s)." {
		t.Fatalf("工具结果异常: %q error=%v", text, isError)
	}
	if gotThread != "lead-1" || len(gotEntries) != 2 || !strings.Contains(gotEntries[0]["prompt"].(string), "```go") {
		t.Fatalf("钩子收到的参数异常: thread=%q entries=%v", gotThread, gotEntries)
	}
	for _, entry := range gotEntries {
		parsed, ok := swarm.ParseEntry(entry)
		if !ok || len(parsed.Acceptance) != 1 || parsed.Acceptance[0] != "true" {
			t.Fatalf("acceptance未传给解析入口：%+v", parsed)
		}
	}

}

func TestSwarmDelegateRejectsUnavailableAndEmpty(t *testing.T) {
	t.Parallel()
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")

	calls := 0
	available := false
	srv.SetSwarm(&SwarmHooks{
		Available: func(string) bool { return available },
		Delegate: func(context.Context, string, []map[string]any) (string, error) {
			calls++
			return "ok", nil
		},
	})

	args := map[string]any{"tasks": []any{map[string]any{"prompt": "x"}}}
	if text, isError := callSwarmDelegate(t, srv, "lead-1", args); !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("不可用的会话应按未知工具拒绝: %q error=%v", text, isError)
	}
	if calls != 0 {
		t.Fatal("不可用时不应调用钩子")
	}

	available = true
	if text, isError := callSwarmDelegate(t, srv, "lead-1", map[string]any{"tasks": []any{}}); !isError || !strings.Contains(text, CodeInvalidArguments) {
		t.Fatalf("空 tasks 应报参数错误: %q error=%v", text, isError)
	}
	if calls != 0 {
		t.Fatal("参数无效时不应调用钩子")
	}
}

func TestSwarmDelegateSchemaDeclaresReplaces(t *testing.T) {
	t.Parallel()
	srv, _, _ := setupTestMCP(t)

	tool, ok := srv.registry.Get(swarmDelegateToolName)
	if !ok {
		t.Fatal("swarm_delegate 应始终注册在 registry 中")
	}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	tasks, _ := properties["tasks"].(map[string]any)
	items, _ := tasks["items"].(map[string]any)
	entryProps, _ := items["properties"].(map[string]any)

	field, ok := entryProps["replaces"].(map[string]any)
	if !ok {
		t.Fatal("swarm_delegate 条目 schema 应声明 replaces")
	}
	if field["type"] != "string" {
		t.Fatalf("replaces 类型期望 string, 实际为 %v", field["type"])
	}
	reworkOf, ok := entryProps["reworkOf"].(map[string]any)
	if !ok || reworkOf["type"] != "string" {
		t.Fatalf("reworkOf schema=%v", reworkOf)
	}
	rerun, ok := entryProps["rerun"].(map[string]any)
	if !ok || rerun["type"] != "boolean" {
		t.Fatalf("rerun schema=%v", rerun)
	}
	acceptance, ok := entryProps["acceptance"].(map[string]any)
	if !ok || acceptance["type"] != "array" {
		t.Fatalf("acceptance schema=%v", acceptance)
	}
	element, _ := acceptance["items"].(map[string]any)
	if element["type"] != "string" {
		t.Fatalf("acceptance items=%v", element)
	}

}

func TestSwarmDelegateSchemaDeclaresFiles(t *testing.T) {
	t.Parallel()
	srv, _, _ := setupTestMCP(t)

	tool, ok := srv.registry.Get(swarmDelegateToolName)
	if !ok {
		t.Fatal("swarm_delegate 应始终注册在 registry 中")
	}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	tasks, _ := properties["tasks"].(map[string]any)
	items, _ := tasks["items"].(map[string]any)
	entryProps, _ := items["properties"].(map[string]any)

	files, ok := entryProps["files"].(map[string]any)
	if !ok {
		t.Fatal("swarm_delegate 条目 schema 应声明 files")
	}
	if files["type"] != "array" {
		t.Fatalf("files 类型期望 array, 实际为 %v", files["type"])
	}
	element, ok := files["items"].(map[string]any)
	if !ok || element["type"] != "string" {
		t.Fatalf("files items 期望 string, 实际为 %v", files["items"])
	}
	if req, ok := items["required"].([]string); ok {
		for _, name := range req {
			if name == "files" {
				t.Fatalf("files 不应在 required 列表中: %v", req)
			}
		}
	} else if reqAny, ok := items["required"].([]any); ok {
		for _, item := range reqAny {
			if item == "files" {
				t.Fatalf("files 不应在 required 列表中: %v", reqAny)
			}
		}
	}
}

func TestSwarmDelegateRejectsEntriesWithoutPrompt(t *testing.T) {
	t.Parallel()
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")

	calls := 0
	srv.SetSwarm(&SwarmHooks{
		Available: func(string) bool { return true },
		Delegate: func(context.Context, string, []map[string]any) (string, error) {
			calls++
			return "ok", nil
		},
	})

	for _, tasks := range []any{
		[]any{map[string]any{}},
		[]any{map[string]any{"task": "x"}},
		[]any{"foo", 123},
		[]any{map[string]any{"prompt": "   "}},
	} {
		text, isError := callSwarmDelegate(t, srv, "lead-1", map[string]any{"tasks": tasks})
		if !isError || !strings.Contains(text, CodeInvalidArguments) {
			t.Fatalf("没有可用 prompt 的 tasks 应报参数错误: %v -> %q error=%v", tasks, text, isError)
		}
	}
	if calls != 0 {
		t.Fatal("参数无效时不应调用钩子")
	}
}
