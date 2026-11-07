package harness

import "net/url"

// MCPEndpoint 描述外部接入的 MCP 服务端点。
type MCPEndpoint struct {
	Name  string
	URL   string
	Token string
	// CodexName 非空时使用隔离名称，并禁用同名原生配置，避免 Codex 深合并传输字段。
	CodexName string
}

// BuiltinMCP 描述注入给 provider 的内置 MCP 端点。
// ThreadBase/ThreadToken 是代理按会话路由的端点，地址本身即会话身份；
// URL/Token 是共享端点，调用方靠 X-Mcode-Workdir 头识别项目。
type BuiltinMCP struct {
	ThreadBase  string
	ThreadToken string
	URL         string
	Token       string
}

// Enabled 报告是否配置了任一内置 MCP 接入方式。
func (b BuiltinMCP) Enabled() bool {
	return b.ThreadBase != "" || b.URL != ""
}

// Endpoint 返回本轮应注入的地址与令牌，以及调用方是否需要附加 X-Mcode-Workdir 头。
// ThreadBase 与 turn.ThreadID 均非空时使用线程端点，否则退回共享端点。
func (b BuiltinMCP) Endpoint(turn Turn) (url, token string, workdirHeader bool) {
	if b.ThreadBase != "" && turn.ThreadID != "" {
		return threadEndpoint(b.ThreadBase, turn.ThreadID), b.ThreadToken, false
	}
	return b.URL, b.Token, true
}

// threadEndpoint 拼接按会话路由的地址，对会话 ID 做路径转义。
func threadEndpoint(base, threadID string) string {
	return base + url.PathEscape(threadID)
}
