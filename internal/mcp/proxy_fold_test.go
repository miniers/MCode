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
	t.Parallel()
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
			if !strings.Contains(entry.Description, "query") || !strings.Contains(entry.Description, "一行简介") {
				t.Errorf("描述应说明 query 与省略用法, got %q", entry.Description)
			}
			if !strings.Contains(entry.Description, "tool_0") || !strings.Contains(entry.Description, "tool_40") {
				t.Errorf("描述末尾应附全部工具名, got %q", entry.Description)
			}
			if entry.InputSchema["type"] != "object" {
				t.Errorf("inputSchema 类型应为 object, got %v", entry.InputSchema["type"])
			}
			props, _ := entry.InputSchema["properties"].(map[string]any)
			queryProp, _ := props["query"].(map[string]any)
			if queryProp["type"] != "string" {
				t.Errorf("inputSchema.properties.query 应为 string 类型, got %v", queryProp)
			}
		})
	}
}

// TestFoldToolsListDescriptionTruncated 覆盖：工具名过多时描述按 2000 字符截断。
func TestFoldToolsListDescriptionTruncated(t *testing.T) {
	t.Parallel()
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
// 省略 tool 与 query 返回精简清单，带 tool 时改写成内部工具调用。
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

	// 3. 省略 tool 与 query 返回只含名字与一行简介的精简清单
	code, resp = postProxy(t, handler, "/mcp/demo", token, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": foldEntryToolName, "arguments": map[string]any{}},
	})
	if code != http.StatusOK || resp["error"] != nil {
		t.Fatalf("省略 tool 应成功返回清单, code=%d resp=%v", code, resp)
	}
	summary := firstContentText(t, resp)
	if strings.Contains(summary, "inputSchema") {
		t.Fatalf("省略 query 时不应返回 inputSchema, got %q", summary)
	}
	lines := strings.Split(summary, "\n")
	if len(lines) != 45 || lines[0] != "tool_0: 模拟工具 tool_0" || lines[44] != "tool_44: 模拟工具 tool_44" {
		t.Fatalf("应返回 45 行精简工具清单, got %d 行 first=%q", len(lines), lines[0])
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

// newFoldQueryProxy 构造一个已缓存自定义工具列表的折叠代理，供 TestFoldQuery* 使用。
func newFoldQueryProxy(t *testing.T, tools []map[string]any) *Proxy {
	t.Helper()
	raw := foldListResult(t, tools)
	_, names, ok := parseToolsList(raw)
	if !ok {
		t.Fatal("自定义测试工具列表解析失败")
	}
	p := NewProxy(nil)
	p.setFoldState("srv", foldState{folded: true, list: raw, names: names})
	return p
}

// foldQueryCustomTools 生成超过阈值（42 个）的自定义工具集，覆盖多行描述、空描述、同名优先与 10 条截断。
func foldQueryCustomTools() []map[string]any {
	tools := []map[string]any{
		{
			"name":        "alpha",
			"description": "第一行简介\n第二行详细说明",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"secret_field": map[string]any{"type": "string"}},
			},
		},
		{
			"name":        "beta",
			"description": "",
			"inputSchema": map[string]any{"type": "object"},
		},
		{
			"name":        "gamma",
			"description": "   \n  ",
			"inputSchema": map[string]any{"type": "object"},
		},
	}
	for i := 0; i < 12; i++ {
		tools = append(tools, map[string]any{
			"name":        "file_read_" + strconv.Itoa(i),
			"description": "读取文件片段 " + strconv.Itoa(i),
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
			},
		})
	}
	tools = append(tools,
		map[string]any{
			"name":        "read",
			"description": "读取指定路径",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"target": map[string]any{"type": "string"}},
			},
		},
		map[string]any{
			"name":        "write_cfg",
			"description": "Update CONFIG entry",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"key": map[string]any{"type": "string"}},
			},
		},
	)
	for len(tools) < 42 {
		idx := len(tools)
		tools = append(tools, map[string]any{
			"name":        "extra_" + strconv.Itoa(idx),
			"description": "辅助工具 " + strconv.Itoa(idx),
			"inputSchema": map[string]any{"type": "object"},
		})
	}
	return tools
}

// foldPlanText 从 foldCallPlan 的直接返回结果中取出第一段 text 并返回是否报错。
func foldPlanText(t *testing.T, plan foldCallPlan) (string, bool) {
	t.Helper()
	if plan.result == nil {
		t.Fatal("预期直接返回 result，但 plan.result 为 nil")
	}
	content, _ := plan.result["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("plan.result 缺少 content: %v", plan.result)
	}
	text, _ := content[0]["text"].(string)
	isErr, _ := plan.result["isError"].(bool)
	return text, isErr
}

// TestFoldQuery_EmptyReturnsSummary 覆盖：query 为空或仅空白时只返回工具名与第一行简介，不含 inputSchema。
func TestFoldQuery_EmptyReturnsSummary(t *testing.T) {
	t.Parallel()
	p := newFoldQueryProxy(t, foldQueryCustomTools())

	for _, args := range []string{`{}`, `{"query": "   "}`} {
		plan := p.dispatchFoldCall(t.Context(), "srv", "demo", json.RawMessage(args))
		text, isErr := foldPlanText(t, plan)
		if isErr {
			t.Fatalf("args=%s 不应返回错误: %s", args, text)
		}
		if strings.Contains(text, "inputSchema") || strings.Contains(text, "secret_field") {
			t.Fatalf("args=%s 不应包含 inputSchema，got %q", args, text)
		}
		if strings.Contains(text, "第二行详细说明") {
			t.Fatalf("args=%s 只应包含描述的第一行，got %q", args, text)
		}
		lines := strings.Split(text, "\n")
		if len(lines) < 3 {
			t.Fatalf("行数不足: %v", lines)
		}
		if lines[0] != "alpha: 第一行简介" {
			t.Errorf("多行描述首行不符, got %q", lines[0])
		}
		if lines[1] != "beta" {
			t.Errorf("空描述应只写名字, got %q", lines[1])
		}
		if lines[2] != "gamma" {
			t.Errorf("空白描述应只写名字, got %q", lines[2])
		}
	}
}

// TestFoldQuery_MatchesAndLimits 覆盖：关键词命中返回完整 inputSchema、完全同名排第一、多关键词大小写无关、最多 10 个。
func TestFoldQuery_MatchesAndLimits(t *testing.T) {
	t.Parallel()
	p := newFoldQueryProxy(t, foldQueryCustomTools())

	// 1. read 完全同名排最前，总计 13 个命中时截断到 10 个且保留原有相对顺序。
	plan := p.dispatchFoldCall(t.Context(), "srv", "demo", json.RawMessage(`{"query":"read"}`))
	text, isErr := foldPlanText(t, plan)
	if isErr {
		t.Fatalf("query=read 不应返回错误: %s", text)
	}
	var matched []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal([]byte(text), &matched); err != nil {
		t.Fatalf("query 命中结果应为 JSON 数组: %v, raw=%s", err, text)
	}
	if len(matched) != 10 {
		t.Fatalf("query 最多返回 10 个工具, got %d", len(matched))
	}
	if matched[0].Name != "read" {
		t.Fatalf("完全同名工具应排第一, got %q", matched[0].Name)
	}
	if matched[0].InputSchema["properties"] == nil {
		t.Fatalf("匹配结果应保留完整 inputSchema, got %v", matched[0].InputSchema)
	}
	for i := 1; i < 10; i++ {
		want := "file_read_" + strconv.Itoa(i-1)
		if matched[i].Name != want {
			t.Fatalf("第 %d 项顺序不符: got %q, want %q", i, matched[i].Name, want)
		}
	}

	// 2. 多关键词且大小写不同，同时匹配工具名与描述。
	plan = p.dispatchFoldCall(t.Context(), "srv", "demo", json.RawMessage(`{"query":"  config   ALPHA "}`))
	text, isErr = foldPlanText(t, plan)
	if isErr {
		t.Fatalf("多关键词 query 不应返回错误: %s", text)
	}
	matched = nil
	if err := json.Unmarshal([]byte(text), &matched); err != nil {
		t.Fatalf("多关键词结果应为 JSON 数组: %v", err)
	}
	if len(matched) != 2 || matched[0].Name != "alpha" || matched[1].Name != "write_cfg" {
		t.Fatalf("多关键词匹配结果不符, got %+v", matched)
	}
}

// TestFoldQuery_NoMatch 覆盖：无匹配时返回提示文本与全部可用工具名，不标 isError。
func TestFoldQuery_NoMatch(t *testing.T) {
	t.Parallel()
	p := newFoldQueryProxy(t, foldQueryCustomTools())

	plan := p.dispatchFoldCall(t.Context(), "srv", "demo", json.RawMessage(`{"query":"no_such_keyword"}`))
	text, isErr := foldPlanText(t, plan)
	if isErr {
		t.Fatalf("无匹配时不应标记 isError, got text=%q", text)
	}
	if !strings.HasPrefix(text, "没有匹配的工具。可用工具：") {
		t.Fatalf("无匹配提示前缀不符, got %q", text)
	}
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "read") {
		t.Fatalf("无匹配提示应列出可用工具名, got %q", text)
	}
}
