package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/nmem"
)

// sharedToolNames 以无会话作用域的共享端点拉取 tools/list。
func sharedToolNames(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	return listToolNames(t, ts.URL+"/mcp", srv.Token(), "")
}

// callEntryTool 以线程作用域经族入口工具发起一次 tools/call。
func callEntryTool(t *testing.T, srv *Server, threadID, entry string, args map[string]any) (string, bool, map[string]any) {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      entry,
			"arguments": args,
		},
	})
	if status != 200 {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", status)
	}
	return parseToolText(t, res)
}

// entryActionEnum 从 tools/list 读取入口工具的 action enum，与模型实际看到的一致。
func entryActionEnum(t *testing.T, srv *Server, threadID, entry string) map[string]bool {
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
	for _, item := range toolsArray {
		tm, _ := item.(map[string]any)
		if name, _ := tm["name"].(string); name != entry {
			continue
		}
		schema, _ := tm["inputSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		actionSchema, _ := properties["action"].(map[string]any)
		enum, _ := actionSchema["enum"].([]any)
		out := make(map[string]bool, len(enum))
		for _, raw := range enum {
			if name, ok := raw.(string); ok {
				out[name] = true
			}
		}
		return out
	}
	t.Fatalf("tools/list 中缺少入口工具 %s", entry)
	return nil
}

// TestAutomationEntryEnum 校验自动化入口覆盖全部自动化动作，且不含总管专属的 run_now。
func TestAutomationEntryEnum(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	want := []string{"create", "list", "update", "cancel", "report_result", entryActionHelp}
	enum := entryActionEnum(t, srv, "thread-ordinary", automationFamily.entry)
	for _, action := range want {
		if !enum[action] {
			t.Fatalf("自动化入口 enum 缺少 %s: %v", action, enum)
		}
	}
	if enum["run_now"] {
		t.Fatalf("总管专属的 run_now 不应出现在自动化入口: %v", enum)
	}

	text, isError, _ := callEntryTool(t, srv, "thread-ordinary", automationFamily.entry,
		map[string]any{"action": entryActionHelp})
	if isError || !strings.Contains(text, "list：") {
		t.Fatalf("action=help 应列出操作清单: %q error=%v", text, isError)
	}
}

// TestAutomationEntryForwardsAction 校验入口把 action 与 args 透传给类型化工具。
func TestAutomationEntryForwardsAction(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	text, isError, payload := callEntryTool(t, srv, "thread-ordinary", automationFamily.entry,
		map[string]any{"action": "list", "args": map[string]any{}})
	if isError {
		t.Fatalf("经入口调用 list 失败: %q", text)
	}
	if _, ok := payload["automations"]; !ok {
		t.Fatalf("list 结果应带 automations: %v", payload)
	}
}

// TestAutomationEntryRejectsExcludedAction 校验总管专属动作不能经入口调用。
func TestAutomationEntryRejectsExcludedAction(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	text, isError, payload := callEntryTool(t, srv, "thread-ordinary", automationFamily.entry,
		map[string]any{"action": "run_now", "args": map[string]any{"automation_id": "x"}})
	if !isError || payload["code"] != CodeNotFound {
		t.Fatalf("run_now 应返回 %s: %q error=%v", CodeNotFound, text, isError)
	}
}

// TestMemoryEntryEnumShrinksToConfigured 校验记忆入口只列出已接入的操作，全未接入时整个入口消失。
func TestMemoryEntryEnumShrinksToConfigured(t *testing.T) {
	srv, st, _ := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(context.Context, string, nmem.SearchQuery) ([]nmem.Memory, error) {
			return nil, nil
		}
	})
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	names := threadToolNames(t, srv, "thread-ordinary")
	if !names[memoryFamily.entry] {
		t.Fatalf("普通线程应看到 memory 入口: %v", names)
	}

	enum := entryActionEnum(t, srv, "thread-ordinary", memoryFamily.entry)
	if !enum["search"] || !enum[entryActionHelp] {
		t.Fatalf("memory 入口应列出 search: %v", enum)
	}
	for _, action := range []string{"get", "forget", "thread_search", "thread_fetch_messages"} {
		if enum[action] {
			t.Fatalf("未接入的 %s 不应出现在 memory 入口: %v", action, enum)
		}
	}

	// propose 只写本地收件箱、不依赖远端回调，所以未接入任何回调时入口仍在，enum 只剩它。
	bare, bareStore, _ := setupTestMCP(t)
	registerThread(t, bareStore, "thread-ordinary", "proj-test-1")
	if !threadToolNames(t, bare, "thread-ordinary")[memoryFamily.entry] {
		t.Fatal("propose 可用时应保留 memory 入口")
	}
	bareEnum := entryActionEnum(t, bare, "thread-ordinary", memoryFamily.entry)
	if !bareEnum["propose"] {
		t.Fatalf("memory 入口应保留 propose: %v", bareEnum)
	}
	for _, action := range []string{"search", "get", "forget", "thread_search", "thread_fetch_messages"} {
		if bareEnum[action] {
			t.Fatalf("未接入的 %s 不应出现在 memory 入口: %v", action, bareEnum)
		}
	}
}

// TestMemoryEntryForwardsSearch 校验记忆入口把参数透传给 memory_search。
func TestMemoryEntryForwardsSearch(t *testing.T) {
	var gotQuery string
	srv, st, _ := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(_ context.Context, _ string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			gotQuery = q.Query
			return nil, nil
		}
	})
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	text, isError, _ := callEntryTool(t, srv, "thread-ordinary", memoryFamily.entry,
		map[string]any{"action": "search", "args": map[string]any{"query": "worktree 合并"}})
	if isError {
		t.Fatalf("经入口调用 search 失败: %q", text)
	}
	if gotQuery != "worktree 合并" {
		t.Fatalf("search 参数未透传: %q", gotQuery)
	}
}

// TestEntryHiddenOnSharedEndpoint 校验无会话作用域的共享端点保留类型化工具、隐藏入口工具。
func TestEntryHiddenOnSharedEndpoint(t *testing.T) {
	srv, _, _ := setupTestMCP(t)
	names := sharedToolNames(t, srv)
	for _, entry := range []string{kanbanEntryToolName, automationFamily.entry, memoryFamily.entry} {
		if names[entry] {
			t.Fatalf("共享端点不应暴露入口工具 %s: %v", entry, names)
		}
	}
	if !names["automation_list"] {
		t.Fatalf("共享端点应保留类型化工具: %v", names)
	}
}
