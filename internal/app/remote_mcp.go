package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/mcp"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// errMCPExtUnavailable 表示装配层没有给出 Client 或后端，无法扫描 MCP 配置。
var errMCPExtUnavailable = errors.New("MCP 服务不可用")

// mcpExtScanTTL 是 MCP 配置扫描的进程内缓存时长：扫描要读多个配置文件，
// 而输入框的服务器列表会随面板反复打开，60s 内复用一次结果即可。
const mcpExtScanTTL = 60 * time.Second

// remoteMCPService 实现 remote.mcpExtService：把 agentconf 的扫描结果转成可远程渲染的列表。
type remoteMCPService struct {
	client *Client
}

// ListServers 返回会话（或项目）可用的 MCP 服务器。
// threadID 非空时先解析会话拿到项目与 mcpDisabled；projectID 只在没有 threadID 时使用。
func (s remoteMCPService) ListServers(ctx context.Context, threadID, projectID string) (wire.MCPExtListServersResult, error) {
	result := wire.MCPExtListServersResult{ThreadID: threadID}
	if s.client == nil || s.client.backend == nil {
		return result, errMCPExtUnavailable
	}

	disabled := map[string]bool{}
	if threadID != "" {
		thread, err := s.client.backend.GetThread(ctx, threadID)
		if err != nil {
			return result, err
		}
		projectID = thread.ProjectID
		for _, key := range thread.MCPDisabled {
			if trimmed := strings.TrimSpace(key); trimmed != "" {
				disabled[trimmed] = true
			}
		}
	}

	projectDir := ""
	if projectID != "" {
		path, ok, err := s.projectPath(ctx, projectID)
		if err != nil {
			return result, err
		}
		if !ok {
			return result, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: fmt.Sprintf("project %s not found", projectID)}
		}
		projectDir = path
	}
	result.ProjectID = projectID

	servers, err := mcpExtServersForProject(projectDir)
	if err != nil {
		return result, err
	}
	result.Servers = make([]wire.MCPExtServer, 0, len(servers))
	for _, srv := range servers {
		result.Servers = append(result.Servers, mcpExtServerToWire(srv, disabled))
	}
	sort.Slice(result.Servers, func(i, j int) bool {
		if result.Servers[i].Scope != result.Servers[j].Scope {
			return result.Servers[i].Scope < result.Servers[j].Scope
		}
		if result.Servers[i].Name != result.Servers[j].Name {
			return result.Servers[i].Name < result.Servers[j].Name
		}
		return result.Servers[i].Agent < result.Servers[j].Agent
	})
	return result, nil
}

// projectPath 按项目 ID 查本地目录，项目不存在时 ok 为 false。
func (s remoteMCPService) projectPath(ctx context.Context, projectID string) (string, bool, error) {
	projects, err := s.client.backend.ListProjects(ctx)
	if err != nil {
		return "", false, err
	}
	for _, p := range projects.Projects {
		if p.ID == projectID {
			return p.Path, true, nil
		}
	}
	return "", false, nil
}

// mcpExtServerToWire 把扫描结果转成远程列表项。
// 禁用状态取该会话的 mcpDisabled 名单；被全局设置锁住的服务单独标注，
// 与桌面端 mcpServerLockReason 的三种原因保持一致。
func mcpExtServerToWire(srv agentconf.MCPServer, disabled map[string]bool) wire.MCPExtServer {
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	locked, reason := mcpExtLockReason(srv)
	return wire.MCPExtServer{
		Key:        key,
		Name:       srv.Name,
		Agent:      srv.Agent,
		Scope:      srv.Scope,
		Source:     srv.Source,
		Transport:  srv.Transport,
		URL:        srv.URL,
		Command:    srv.Command,
		Disabled:   disabled[key],
		Locked:     locked,
		LockReason: reason,
	}
}

// mcpExtLockReason 判断服务是否被全局设置锁住，并给出可读原因，语义同桌面端。
func mcpExtLockReason(srv agentconf.MCPServer) (bool, string) {
	switch {
	case srv.ReadOnly:
		return true, "只读"
	case srv.Untrusted:
		return true, "待信任"
	case srv.Disabled:
		return true, "全局已停用"
	}
	return false, ""
}

// mcpExtScanFunc 是扫描入口，测试可替换它统计调用次数。
var mcpExtScanFunc = agentconf.ScanMCP

// mcpExtScanCache 按项目目录缓存扫描结果；projectDir 为空表示只看全局来源。
var mcpExtScanCache = struct {
	mu      sync.Mutex
	entries map[string]mcpExtScanEntry
}{entries: make(map[string]mcpExtScanEntry)}

type mcpExtScanEntry struct {
	servers  []agentconf.MCPServer
	loadedAt time.Time
}

// mcpExtServersForProject 返回项目目录下的 MCP 扫描结果，TTL 内复用缓存。
func mcpExtServersForProject(projectDir string) ([]agentconf.MCPServer, error) {
	now := time.Now()
	mcpExtScanCache.mu.Lock()
	if entry, ok := mcpExtScanCache.entries[projectDir]; ok && now.Sub(entry.loadedAt) < mcpExtScanTTL {
		servers := entry.servers
		mcpExtScanCache.mu.Unlock()
		return servers, nil
	}
	mcpExtScanCache.mu.Unlock()

	home, _ := os.UserHomeDir()
	servers, err := mcpExtScanFunc(agentconf.Options{
		Home:       home,
		ProjectDir: projectDir,
		DataDir:    getAppDataDir(),
	})
	if err != nil {
		return nil, err
	}

	mcpExtScanCache.mu.Lock()
	if mcpExtScanCache.entries == nil {
		mcpExtScanCache.entries = make(map[string]mcpExtScanEntry)
	}
	mcpExtScanCache.entries[projectDir] = mcpExtScanEntry{servers: servers, loadedAt: time.Now()}
	mcpExtScanCache.mu.Unlock()
	return servers, nil
}

// resetMCPExtScanCache 清空扫描缓存，仅供测试隔离用例。
func resetMCPExtScanCache() {
	mcpExtScanCache.mu.Lock()
	mcpExtScanCache.entries = make(map[string]mcpExtScanEntry)
	mcpExtScanCache.mu.Unlock()
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		a.backend.SetService(remote.MCPExtServiceName, remoteMCPService{client: a.remoteClient()})
	})
}
