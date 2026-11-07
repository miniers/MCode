package remote

import (
	"context"
	"encoding/json"
	"strings"

	"mcode/internal/remote/wire"
)

// MCPExtServiceName 是 app 层注入的 MCP 服务器列表服务名。
const MCPExtServiceName = "mcp"

// MCPExtListServersMethod 是 MCP 服务器列表方法名。
const MCPExtListServersMethod = "mcp.servers.list"

// mcpExtService 由 app 层注入：给出会话（或项目）可用的 MCP 服务器，
// 以及该会话的禁用状态。threadID 为空表示按 projectID 列清单，此时禁用状态全为 false。
type mcpExtService interface {
	ListServers(ctx context.Context, threadID, projectID string) (wire.MCPExtListServersResult, error)
}

func init() {
	RegisterMethod(MCPExtListServersMethod, handleMCPExtListServers)
}

// handleMCPExtListServers 处理 mcp.servers.list：只读，参数严格解码。
func handleMCPExtListServers(ctx context.Context, d *Dispatcher, _ Conn, params json.RawMessage) (any, error) {
	var p wire.MCPExtListServersParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	threadID := strings.TrimSpace(p.ThreadID)
	projectID := strings.TrimSpace(p.ProjectID)
	if threadID == "" && projectID == "" {
		return nil, ExtInvalidParams("Invalid params: 'threadID' or 'projectID' is required")
	}
	if threadID != "" && !isValidUUID(threadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if projectID != "" && !isValidUUID(projectID) {
		return nil, ExtInvalidParams("Invalid params: 'projectID' must be a valid UUID")
	}

	svc, ok := mcpExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	result, err := svc.ListServers(ctx, threadID, projectID)
	if err != nil {
		return nil, err
	}
	if result.Servers == nil {
		result.Servers = []wire.MCPExtServer{}
	}
	if result.ThreadID == "" {
		result.ThreadID = threadID
	}
	if result.ProjectID == "" {
		result.ProjectID = projectID
	}
	return result, nil
}

// mcpExtServiceOf 取出注入的 MCP 服务；未注入时返回 false，由调用方回 NotImplemented。
func mcpExtServiceOf(d *Dispatcher) (mcpExtService, bool) {
	if d == nil {
		return nil, false
	}
	svc, ok := backendService[mcpExtService](d.backend, MCPExtServiceName)
	if !ok {
		return nil, false
	}
	return svc, true
}
