package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcode/internal/store"
)

// listToolNames 发起 tools/list 并返回工具名集合。
func listToolNames(t *testing.T, mcpURL, token, workDir string) map[string]bool {
	t.Helper()
	status, res := postRPC(t, mcpURL, token, workDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	if status != http.StatusOK {
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

func hasToolPrefix(names map[string]bool, prefix string) bool {
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// TestCapabilityNilCallbackListsAllTools 校验未配置回调时保持原有行为：三组工具全部可见且可调用。
func TestCapabilityNilCallbackListsAllTools(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	names := listToolNames(t, mcpURL, token, projDir)
	if !hasToolPrefix(names, "kanban_") || !hasToolPrefix(names, "context_") || !hasToolPrefix(names, "automation_") {
		t.Fatalf("nil 回调应放行全部工具, 实际为 %v", names)
	}

	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "automation_list",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("automation_list 状态码期望 200, 实际为 %d", status)
	}
	if _, isError := toolCallText(t, res); isError {
		t.Fatalf("nil 回调下 automation_list 不应被拒绝: %v", res)
	}
}

// TestCapabilityDisabledToolIsHiddenAndRejected 校验关闭某项能力后 tools/list 裁剪且 tools/call 被拒。
func TestCapabilityDisabledToolIsHiddenAndRejected(t *testing.T) {
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.CapabilityEnabled = func(project store.Project, capability string) bool {
			return capability != CapAutomation
		}
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	names := listToolNames(t, mcpURL, token, projDir)
	if hasToolPrefix(names, "automation_") {
		t.Fatalf("关闭 automation 能力后 tools/list 不应包含 automation 工具, 实际为 %v", names)
	}
	if !hasToolPrefix(names, "kanban_") || !hasToolPrefix(names, "context_") {
		t.Fatalf("其余能力分组应保持可见, 实际为 %v", names)
	}

	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "automation_list",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("automation_list 状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if !isError {
		t.Fatalf("能力被关闭时 automation_list 必须被拒绝: %v", res)
	}
	if payload["code"] != CodeNotFound {
		t.Fatalf("拒绝错误码期望 %s, 实际为 %v", CodeNotFound, payload["code"])
	}
}

// TestCapabilityEmptyAlwaysVisible 校验未声明 Capability 的工具不受能力回调裁剪。
func TestCapabilityEmptyAlwaysVisible(t *testing.T) {
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.CapabilityEnabled = func(project store.Project, capability string) bool {
			return false
		}
	})
	srv.registry.Register(ToolDefinition{
		Name:        "capability_probe",
		Description: "test-only tool without capability",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(tc ToolContext, args map[string]any) ToolResult {
			return toolSuccess("ok")
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	names := listToolNames(t, mcpURL, token, projDir)
	if !names["capability_probe"] {
		t.Fatalf("Capability 为空的工具应始终可见, 实际为 %v", names)
	}
	if hasToolPrefix(names, "kanban_") || hasToolPrefix(names, "context_") || hasToolPrefix(names, "automation_") {
		t.Fatalf("能力全部关闭时分组工具应被裁剪, 实际为 %v", names)
	}

	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "capability_probe",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("capability_probe 状态码期望 200, 实际为 %d", status)
	}
	text, isError := toolCallText(t, res)
	if isError || text != "ok" {
		t.Fatalf("capability_probe 期望返回 ok, 实际为 %q (isError=%v)", text, isError)
	}
}
