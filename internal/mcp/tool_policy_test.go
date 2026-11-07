package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"mcode/internal/agentconf"
)

// newPolicyFakeUpstream 启动一个提供 echo/add 两个工具的假 HTTP 上游，calls 统计收到的 tools/call 次数。
func newPolicyFakeUpstream(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "fake-policy-upstream"},
			}}
		case "tools/list":
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "echo", "description": "echo tool", "inputSchema": map[string]any{"type": "object"}},
					{"name": "add", "description": "add tool", "inputSchema": map[string]any{"type": "object"}},
				},
			}}
		case "tools/call":
			if calls != nil {
				calls.Add(1)
			}
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok"}},
			}}
		default:
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newPolicyProxy 构造一个挂载 demo-srv 上游的代理，并把工具策略指向 policyPath。
func newPolicyProxy(t *testing.T, upstreamURL, policyPath string) *Proxy {
	t.Helper()
	SetToolPolicyPath(policyPath)
	t.Cleanup(func() { SetToolPolicyPath("") })

	hub := NewHub(nil)
	t.Cleanup(func() { _ = hub.Close() })
	hub.SetUpstreams([]UpstreamSpec{{
		ID:        "demo",
		Name:      "demo-srv",
		Transport: "http",
		URL:       upstreamURL,
	}})

	proxy := NewProxy(hub)
	t.Cleanup(func() { _ = proxy.Close() })
	return proxy
}

func toolNamesFromResponse(t *testing.T, resp map[string]any) []string {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		names = append(names, name)
	}
	return names
}

// TestProxy_FiltersDisabledTools 覆盖：策略禁用的工具从 tools/list 移除，调用被拒绝且不转发到上游。
func TestProxy_FiltersDisabledTools(t *testing.T) {
	var calls atomic.Int64
	upstream := newPolicyFakeUpstream(t, &calls)

	policyPath := filepath.Join(t.TempDir(), "mcp_tool_policy.json")
	if err := agentconf.SaveMCPToolPolicy(policyPath, agentconf.MCPToolPolicy{
		Disabled: map[string]map[string]bool{"demo-srv": {"echo": true}},
	}); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	proxy := newPolicyProxy(t, upstream.URL, policyPath)
	handler := proxy.Handler()
	token := proxy.Token()

	code, resp := postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	if code != http.StatusOK {
		t.Fatalf("tools/list 状态码: expected 200, got %d", code)
	}
	names := toolNamesFromResponse(t, resp)
	if len(names) != 1 || names[0] != "add" {
		t.Fatalf("禁用工具应被过滤, got %v", names)
	}

	// 禁用的工具不能被调用，且不应转发到上游
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "echo", "arguments": map[string]any{}},
	})
	if code != http.StatusOK {
		t.Fatalf("tools/call 状态码: expected 200, got %d", code)
	}
	if resp["error"] == nil {
		t.Fatalf("禁用工具调用应返回 JSON-RPC 错误, got %v", resp)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("禁用工具调用不应转发到上游, calls=%d", got)
	}

	// 未禁用的工具照常调用
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": "add", "arguments": map[string]any{}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("未禁用工具应正常调用, code=%d resp=%v", code, resp)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("未禁用工具应转发到上游, calls=%d", got)
	}
}

// TestProxy_NoPolicyKeepsAllTools 覆盖：策略文件不存在时不改动工具列表与调用行为。
func TestProxy_NoPolicyKeepsAllTools(t *testing.T) {
	var calls atomic.Int64
	upstream := newPolicyFakeUpstream(t, &calls)

	proxy := newPolicyProxy(t, upstream.URL, filepath.Join(t.TempDir(), "missing_policy.json"))
	handler := proxy.Handler()
	token := proxy.Token()

	code, resp := postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	if code != http.StatusOK {
		t.Fatalf("tools/list 状态码: expected 200, got %d", code)
	}
	names := toolNamesFromResponse(t, resp)
	if len(names) != 2 || names[0] != "echo" || names[1] != "add" {
		t.Fatalf("无策略时工具列表应保持不变, got %v", names)
	}

	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "echo", "arguments": map[string]any{}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("无策略时调用应正常, code=%d resp=%v", code, resp)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("无策略时应转发到上游, calls=%d", got)
	}
}
