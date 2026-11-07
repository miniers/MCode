package wire

// 本文件定义 MCP 服务器列表（mcp.servers.list）远程扩展方法的参数与结果类型。
// 列表来源与桌面端输入框的 MCP 菜单、设置页同源：agentconf 对 MCP 配置与 .mcp.json 的磁盘扫描，
// 不是 provider 能力。
// 参数二选一：threadID 用于带上该会话的 mcpDisabled 禁用状态；projectID 用于只关心项目清单的调用方。
// 两者都给时以 threadID 解析出的项目为准。
// schema 尚未并入 docs/remote-control.schema.json，待 schema 维护者补齐后再按兼容规则演进。

// MCPExtListServersParams 对应 mcp.servers.list 请求参数。
type MCPExtListServersParams struct {
	// ThreadID 与 ProjectID 至少要给一个，两者都是 UUID。
	ThreadID  string `json:"threadID,omitempty"`
	ProjectID string `json:"projectID,omitempty"`
}

// MCPExtServer 是列表里的单个 MCP 服务器，供客户端直接渲染开关。
type MCPExtServer struct {
	// Key 是会话禁用名单（thread.mcpDisabled）里使用的键，形如 agent:scope:name。
	Key string `json:"key"`
	// Name 是服务名，Agent 是配置来源（claude/pi/agy/custom 等），Scope 是作用域（home/project）。
	Name  string `json:"name"`
	Agent string `json:"agent,omitempty"`
	Scope string `json:"scope"`
	// Source 是配置文件名或路径，Transport 是 stdio/http/sse，URL 与 Command 二选一。
	Source    string `json:"source,omitempty"`
	Transport string `json:"transport,omitempty"`
	URL       string `json:"url,omitempty"`
	Command   string `json:"command,omitempty"`
	// Disabled 表示该会话的 mcpDisabled 里含这个键。
	Disabled bool `json:"disabled"`
	// Locked 表示被全局设置锁住（只读、待信任、全局已停用），此时开关不可用；原因见 LockReason。
	Locked     bool   `json:"locked,omitempty"`
	LockReason string `json:"lockReason,omitempty"`
}

// MCPExtListServersResult 对应 mcp.servers.list 成功返回。
type MCPExtListServersResult struct {
	ThreadID  string         `json:"threadID,omitempty"`
	ProjectID string         `json:"projectID,omitempty"`
	Servers   []MCPExtServer `json:"servers"`
}
