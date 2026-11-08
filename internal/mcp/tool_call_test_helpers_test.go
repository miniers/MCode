package mcp

import "testing"

// callThreadTool 以线程作用域调用工具，返回结果文本与是否出错。
func callThreadTool(t *testing.T, srv *Server, threadID, name string, args map[string]any) (string, bool) {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if status != 200 {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", status)
	}
	text, isError, _ := parseToolText(t, res)
	return text, isError
}
