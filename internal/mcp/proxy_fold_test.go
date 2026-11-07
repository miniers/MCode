package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/agentconf"
)

// foldFakeTools 生成 count 个工具定义（tool_0、tool_1 …），带描述与 inputSchema。
func foldFakeTools(count int) []map[string]any {
	tools := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		name := "tool_" + strconv.Itoa(i)
		tools = append(tools, map[string]any{
			"name":        name,
			"description": "模拟工具 " + name,
			"inputSchema": map[string]any{"type": "object"},
		})
	}
	return tools
}

// foldListResult 拼一份 tools/list 结果，用于 foldToolsList 的表驱动测试。
func foldListResult(t *testing.T, tools []map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		t.Fatalf("拼装 tools/list 结果失败: %v", err)
	}
	return raw
}

// newFoldFakeUpstream 启动一个提供 count 个工具的假 HTTP 上游；
// calls 统计收到的 tools/call 次数，lastTool 非 nil 时记录最近一次调用的工具名。
func newFoldFakeUpstream(t *testing.T, count int, calls *atomic.Int64, lastTool *atomic.Value) *httptest.Server {
	t.Helper()
	tools := foldFakeTools(count)
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
				"serverInfo":      map[string]any{"name": "fake-fold-upstream"},
			}}
		case "tools/list":
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": tools}}
		case "tools/call":
			if calls != nil {
				calls.Add(1)
			}
			var callParams struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &callParams)
			if lastTool != nil {
				lastTool.Store(callParams.Name)
			}
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "called:" + callParams.Name}},
			}}
		default:
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// firstContentText 取 tools/call 结果里第一条 text 内容。
func firstContentText(t *testing.T, resp map[string]any) string {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call 结果缺少 content: %v", resp)
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	return text
}

// TestFoldToolsList 覆盖折叠判定的四种边界：未超阈值、超阈值、关闭折叠、上游自带 call_tool。
func TestFoldToolsList(t *testing.T) {
	withEntryTool := append(foldFakeTools(40), map[string]any{
		"name":        foldEntryToolName,
		"description": "上游自带的入口工具",
		"inputSchema": map[string]any{"type": "object"},
	})

	cases := []struct {
		name      string
		tools     []map[string]any
		threshold int
		wantFold  bool
	}{
		{name: "数量不超过阈值时原样返回", tools: foldFakeTools(40), threshold: 40, wantFold: false},
		{name: "数量超过阈值时折叠", tools: foldFakeTools(41), threshold: 40, wantFold: true},
		{name: "阈值 -1 表示关闭折叠", tools: foldFakeTools(45), threshold: -1, wantFold: false},
		{name: "上游已有 call_tool 时不折叠", tools: withEntryTool, threshold: 40, wantFold: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := foldListResult(t, c.tools)
			out, folded := foldToolsList(in, c.threshold)
			if folded != c.wantFold {
				t.Fatalf("folded = %v, want %v", folded, c.wantFold)
			}
			if !folded {
				if string(out) != string(in) {
					t.Fatalf("未折叠时应原样返回, got %s", out)
				}
				return
			}

			_, names, ok := parseToolsList(out)
			if !ok || len(names) != 1 || names[0] != foldEntryToolName {
				t.Fatalf("折叠后应只剩 %s, got %v", foldEntryToolName, names)
			}

			var envelope struct {
				Tools []struct {
					Name        string         `json:"name"`
					Description string         `json:"description"`
					InputSchema map[string]any `json:"inputSchema"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(out, &envelope); err != nil {
				t.Fatalf("折叠结果不是合法 JSON: %v", err)
			}
			entry := envelope.Tools[0]
			if !strings.Contains(entry.Description, "41") {
				t.Errorf("描述应写明工具总数, got %q", entry.Description)
			}
			if !strings.Contains(entry.Description, "tool_0") || !strings.Contains(entry.Description, "tool_40") {
				t.Errorf("描述末尾应附全部工具名, got %q", entry.Description)
			}
			if entry.InputSchema["type"] != "object" {
				t.Errorf("inputSchema 类型应为 object, got %v", entry.InputSchema["type"])
			}
		})
	}
}

// TestFoldToolsListDescriptionTruncated 覆盖：工具名过多时描述按 2000 字符截断。
func TestFoldToolsListDescriptionTruncated(t *testing.T) {
	tools := make([]map[string]any, 0, 300)
	for i := 0; i < 300; i++ {
		tools = append(tools, map[string]any{
			"name":        "a_very_long_tool_name_for_truncation_" + strconv.Itoa(i),
			"description": "模拟工具",
			"inputSchema": map[string]any{"type": "object"},
		})
	}
	out, folded := foldToolsList(foldListResult(t, tools), 40)
	if !folded {
		t.Fatal("300 个工具应折叠")
	}
	var envelope struct {
		Tools []struct {
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("折叠结果不是合法 JSON: %v", err)
	}
	if n := len([]rune(envelope.Tools[0].Description)); n > foldEntryDescriptionLimit {
		t.Fatalf("描述长度 %d 超过上限 %d", n, foldEntryDescriptionLimit)
	}
}

// TestProxy_FoldsManyTools 覆盖：45 个工具的上游只暴露 call_tool，
// 省略 tool 返回完整清单，带 tool 时改写成内部工具调用。
func TestProxy_FoldsManyTools(t *testing.T) {
	var calls atomic.Int64
	var lastTool atomic.Value
	upstream := newFoldFakeUpstream(t, 45, &calls, &lastTool)

	// 策略文件不存在：使用默认阈值 40，折叠默认开启。
	proxy := newPolicyProxy(t, upstream.URL, filepath.Join(t.TempDir(), "missing_policy.json"))
	handler := proxy.Handler()
	token := proxy.Token()

	// 1. 还没列过清单时直接调 call_tool：代理应自己拉一次 tools/list 重新判定。
	code, resp := postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": foldEntryToolName, "arguments": map[string]any{"tool": "tool_5"}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("折叠入口调用应成功, code=%d resp=%v", code, resp)
	}
	if got, _ := lastTool.Load().(string); got != "tool_5" {
		t.Fatalf("上游收到的工具名应为 tool_5, got %v", got)
	}

	// 2. tools/list 只返回 call_tool
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	if code != http.StatusOK {
		t.Fatalf("tools/list 状态码: expected 200, got %d", code)
	}
	names := toolNamesFromResponse(t, resp)
	if len(names) != 1 || names[0] != foldEntryToolName {
		t.Fatalf("45 个工具应折叠成单一入口, got %v", names)
	}

	// 3. 省略 tool 返回完整工具清单
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": foldEntryToolName, "arguments": map[string]any{}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("省略 tool 应成功返回清单, code=%d resp=%v", code, resp)
	}
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(firstContentText(t, resp)), &listed); err != nil {
		t.Fatalf("省略 tool 时 content 应是 JSON 工具清单: %v", err)
	}
	if len(listed.Tools) != 45 || listed.Tools[0].Name != "tool_0" || listed.Tools[44].Name != "tool_44" {
		t.Fatalf("应返回全部 45 个工具, got %d 个", len(listed.Tools))
	}
	if listed.Tools[0].InputSchema == nil {
		t.Fatal("返回的清单应包含 inputSchema")
	}

	// 4. 带 tool 时改写成内部工具调用
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      foldEntryToolName,
			"arguments": map[string]any{"tool": "tool_7", "arguments": map[string]any{"q": "x"}},
		},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("带 tool 的入口调用应成功, code=%d resp=%v", code, resp)
	}
	if got, _ := lastTool.Load().(string); got != "tool_7" {
		t.Fatalf("上游收到的工具名应为 tool_7, got %v", got)
	}
	if got := firstContentText(t, resp); got != "called:tool_7" {
		t.Fatalf("调用结果应与直接调用内部工具一致, got %q", got)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("应只有两次真实工具调用, calls=%d", got)
	}

	// 5. 内部工具名不存在时返回错误并列出可用工具
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params":  map[string]any{"name": foldEntryToolName, "arguments": map[string]any{"tool": "nope"}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("未知内部工具应返回 isError 结果而非 JSON-RPC 错误, code=%d resp=%v", code, resp)
	}
	result, _ := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("未知内部工具应标记 isError, got %v", result)
	}
	if text := firstContentText(t, resp); !strings.Contains(text, "tool_0") {
		t.Fatalf("未知内部工具应列出可用工具名, got %q", text)
	}
}

// TestProxy_FoldRejectsDisabledInnerTool 覆盖：折叠后对内部工具名照样执行禁用检查。
func TestProxy_FoldRejectsDisabledInnerTool(t *testing.T) {
	var calls atomic.Int64
	var lastTool atomic.Value
	upstream := newFoldFakeUpstream(t, 45, &calls, &lastTool)

	policyPath := filepath.Join(t.TempDir(), "mcp_tool_policy.json")
	if err := agentconf.SaveMCPToolPolicy(policyPath, agentconf.MCPToolPolicy{
		Disabled: map[string]map[string]bool{"demo-srv": {"tool_7": true}},
	}); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	proxy := newPolicyProxy(t, upstream.URL, policyPath)
	handler := proxy.Handler()
	token := proxy.Token()

	// 禁用检查命中的是内部工具名，而不是 call_tool
	code, resp := postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": foldEntryToolName, "arguments": map[string]any{"tool": "tool_7"}},
	})
	if code != http.StatusOK || resp["error"] == nil {
		t.Fatalf("被禁用的内部工具应被拒绝, code=%d resp=%v", code, resp)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("被禁用的内部工具不应转发到上游, calls=%d", got)
	}

	// 未禁用的内部工具照常转发
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": foldEntryToolName, "arguments": map[string]any{"tool": "tool_8"}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("未禁用的内部工具应正常调用, code=%d resp=%v", code, resp)
	}
	if got, _ := lastTool.Load().(string); got != "tool_8" {
		t.Fatalf("上游收到的工具名应为 tool_8, got %v", got)
	}
}

// TestProxy_FoldDisabledByPolicy 覆盖：策略里 FoldThreshold=-1 时恢复原样的工具列表。
func TestProxy_FoldDisabledByPolicy(t *testing.T) {
	var calls atomic.Int64
	upstream := newFoldFakeUpstream(t, 45, &calls, nil)

	policyPath := filepath.Join(t.TempDir(), "mcp_tool_policy.json")
	if err := agentconf.SaveMCPToolPolicy(policyPath, agentconf.MCPToolPolicy{FoldThreshold: -1}); err != nil {
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
	if names := toolNamesFromResponse(t, resp); len(names) != 45 {
		t.Fatalf("关闭折叠后应保留全部 45 个工具, got %d 个", len(names))
	}

	// call_tool 此时不归代理管，未折叠时不改写请求，直接按上游名字转发。
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "tool_3", "arguments": map[string]any{}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("关闭折叠后直接调用内部工具应正常, code=%d resp=%v", code, resp)
	}
	if got := firstContentText(t, resp); got != "called:tool_3" {
		t.Fatalf("直接调用结果不符, got %q", got)
	}
}

// TestProxy_FoldSkippedForAutoApprove 覆盖：配置了免审批工具的服务不折叠，
// 否则工具名统一变成 call_tool 后免审批按 mcp__<server>__<tool> 匹配会失效。
func TestProxy_FoldSkippedForAutoApprove(t *testing.T) {
	var calls atomic.Int64
	upstream := newFoldFakeUpstream(t, 45, &calls, nil)

	policyPath := filepath.Join(t.TempDir(), "mcp_tool_policy.json")
	if err := agentconf.SaveMCPToolPolicy(policyPath, agentconf.MCPToolPolicy{
		AutoApprove: map[string]map[string]bool{"demo-srv": {"tool_1": true}},
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
	if names := toolNamesFromResponse(t, resp); len(names) != 45 {
		t.Fatalf("有免审批工具的服务不应折叠, got %d 个工具", len(names))
	}
}
