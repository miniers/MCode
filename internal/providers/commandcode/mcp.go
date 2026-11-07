package commandcode

// 本文件实现 Command Code 的 MCP 接入：把本轮的内置/外部 MCP 服务清单以 JSON 写进
// MCODE_MCP_SERVERS 环境变量，并在有服务时额外加载内嵌的桥接 mod（mcp_bridge.ts，
// 以额外的 `--mod` 传入，--mod 可重复）。桥自己做 Streamable HTTP 的 initialize /
// tools/list，把工具注册为 mcp__<server>__<tool>，调用时转发 tools/call。

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"

	"mcode/internal/harness"
)

// mcpBridgeSource 是 mcode 的 MCP 桥接 mod 源码（TypeScript，CLI 加载时编译）。
//
//go:embed mcp_bridge.ts
var mcpBridgeSource string

const (
	// mcpServersEnv 是桥读取服务清单的环境变量名，须与 mcp_bridge.ts 一致。
	mcpServersEnv = "MCODE_MCP_SERVERS"
	// mcpBridgeFile 是桥写到临时目录里的文件名。
	mcpBridgeFile = "mcode-mcp-bridge.ts"
	// builtinMCPServerName 是内置服务名，与 claude/codex 的注入保持一致。
	builtinMCPServerName = "mcode"
	// approvalMCPServerName 是 claude 审批服务名，外部端点里重名的跳过。
	approvalMCPServerName = "mcode_approval"
)

// mcpServerEntry 是桥读取的一条服务描述：{name, url, headers}。
type mcpServerEntry struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpServerList 组装本轮注入的服务清单：内置 mcode 服务在前，外部端点在后。
// 地址与是否附加 X-Mcode-Workdir 头由 harness.BuiltinMCP.Endpoint 决定，
// 外部端点里名为 mcode / mcode_approval 的跳过，避免顶掉内置服务与审批服务。
func mcpServerList(turn harness.Turn, builtin harness.BuiltinMCP, external []harness.MCPEndpoint) []mcpServerEntry {
	var servers []mcpServerEntry
	if url, token, workdirHeader := builtin.Endpoint(turn); url != "" {
		headers := map[string]string{"Authorization": "Bearer " + token}
		if workdirHeader {
			headers["X-Mcode-Workdir"] = turn.WorkDir
		}
		servers = append(servers, mcpServerEntry{Name: builtinMCPServerName, URL: url, Headers: headers})
	}
	for _, ep := range external {
		if ep.Name == builtinMCPServerName || ep.Name == approvalMCPServerName {
			continue
		}
		headers := map[string]string{}
		if ep.Token != "" {
			headers["Authorization"] = "Bearer " + ep.Token
		}
		servers = append(servers, mcpServerEntry{Name: ep.Name, URL: ep.URL, Headers: headers})
	}
	return servers
}

// mcpServersEnvValue 返回 MCODE_MCP_SERVERS 的取值；没有任何服务时 ok 为 false，
// 调用方既不注入变量，也不加载桥。
func mcpServersEnvValue(turn harness.Turn, builtin harness.BuiltinMCP, external func(workDir, threadID string) []harness.MCPEndpoint) (string, bool) {
	var endpoints []harness.MCPEndpoint
	if external != nil {
		endpoints = external(turn.WorkDir, turn.ThreadID)
	}
	servers := mcpServerList(turn, builtin, endpoints)
	if len(servers) == 0 {
		return "", false
	}
	data, err := json.Marshal(servers)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// installMCPBridge 把桥接 mod 写到 dir 下并返回路径。
// dir 是每轮新建的临时目录，不跨轮复用，因此直接覆盖写入。
func installMCPBridge(dir string) (string, error) {
	path := filepath.Join(dir, mcpBridgeFile)
	if err := os.WriteFile(path, []byte(mcpBridgeSource), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
