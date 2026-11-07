package mcp

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/harness"
)

// specRetention 为上游配置的保留时长：某端点超时未再出现时将被清理，
// 避免长期运行的进程里 r.specs 只增不减。
const specRetention = 24 * time.Hour

// agentPriority 返回来源 agent 在同名同作用域服务上的优先级，数值越大越优先。
// custom 是 mcode 自己保存的服务，用户显式添加过，不应被外部 agent 的同名配置顶掉。
func agentPriority(agent string) int {
	switch agent {
	case "custom":
		return 3
	case "claude":
		return 2
	case "codex":
		return 1
	default:
		return 0
	}
}

// scopePriority 返回作用域优先级，project 压过 global。
func scopePriority(scope string) int {
	if scope == "project" {
		return 1
	}
	return 0
}

// mcpServerWins 判断同名服务中 candidate 是否应该取代 current。
// 先比作用域（项目级更具体），再比来源 agent 优先级，都相同则保持先到者。
func mcpServerWins(candidate, current agentconf.MCPServer) bool {
	if scopePriority(candidate.Scope) != scopePriority(current.Scope) {
		return scopePriority(candidate.Scope) > scopePriority(current.Scope)
	}
	return agentPriority(candidate.Agent) > agentPriority(current.Agent)
}

// SessionRouter 按项目加载配置；已发给其他会话的代理端点保持可用。
type SessionRouter struct {
	mu    sync.Mutex
	Hub   *Hub
	Proxy *Proxy
	Home  string
	specs map[string]UpstreamSpec
	// lastSeen 记录每个端点最近一次被生成的时间，供 specRetention 清理判定。
	// 零值可用，首次使用时判空创建。
	lastSeen map[string]time.Time
}

// injectionAllowed 失败关闭的注入白名单：只有 mcode 自己会拉起的内置来源才能进入会话。
// agy、pi 等外部 Agent 配置以及在设置页只读展示的服务（ReadOnly）一律不注入，
// 否则它们会被挂到每个线程的 MCP 列表里，用户以为不可编辑却仍在执行命令；
// 新增来源若忘了在这里登记，默认就是不注入。
func injectionAllowed(server agentconf.MCPServer) bool {
	if server.ReadOnly {
		return false
	}
	switch server.Agent {
	case "claude", "codex", "custom", "mcode":
		return true
	}
	return false
}

// EndpointKey 返回外部 MCP 服务在会话级禁用名单里的键，格式与设置页展示一致：agent:scope:name。
func EndpointKey(agent, scope, name string) string {
	return agent + ":" + scope + ":" + name
}

// Endpoints 汇总可注入会话的外部 MCP 端点。disabled 是当前会话的禁用名单（EndpointKey 键），
// nil 或空表示沿用全局设置。
func (r *SessionRouter) Endpoints(projectDir, workDir string, disabled map[string]bool) []harness.MCPEndpoint {
	servers, err := agentconf.ScanMCP(agentconf.Options{
		Home: r.Home, ProjectDir: projectDir, WithCredentials: true,
	})
	if err != nil {
		return nil
	}
	byName := make(map[string]agentconf.MCPServer)
	nativeCodexNames := make(map[string]bool)
	for _, server := range servers {
		if server.Agent == "codex" {
			nativeCodexNames[server.Name] = true
		}
		// 失败关闭：只读展示来源与未知 Agent 不得注入会话。
		if !injectionAllowed(server) {
			continue
		}
		if server.Name == ServerName || server.Name == "mcode_approval" {
			continue
		}
		old, exists := byName[server.Name]
		if !exists || mcpServerWins(server, old) {
			byName[server.Name] = server
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.specs == nil {
		r.specs = make(map[string]UpstreamSpec)
	}
	if r.lastSeen == nil {
		r.lastSeen = make(map[string]time.Time)
	}
	now := time.Now()
	var endpoints []harness.MCPEndpoint
	for _, name := range names {
		server := byName[name]
		// 仓库内配置未获用户信任时一律不拉起命令。
		if server.Disabled || server.Untrusted {
			continue
		}
		// 会话级禁用名单在全局设置之上再筛一层。
		if disabled[EndpointKey(server.Agent, server.Scope, server.Name)] {
			continue
		}
		// 同名项目服务器不能复用其他项目的进程、环境变量或认证配置。
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(workDir+"\x00"+projectDir+"\x00"+server.Agent+"\x00"+server.Source+"\x00"+name)))
		spec := SpecFromAgentconf(id, server)
		spec.Dir = workDir
		r.specs[id] = spec
		r.lastSeen[id] = now
		endpoint := harness.MCPEndpoint{Name: name, URL: r.Proxy.URL(id), Token: r.Proxy.Token()}
		if nativeCodexNames[name] {
			hash := sha256.Sum256([]byte(name))
			alias := fmt.Sprintf("mcode_proxy_%x", hash[:8])
			for byName[alias].Name != "" || nativeCodexNames[alias] {
				alias += "_"
			}
			endpoint.CodexName = alias
		}
		endpoints = append(endpoints, endpoint)
	}
	// 已长期未出现的端点（项目已关闭、配置已删除）连同上游配置一起清掉，
	// 否则 r.specs 会随项目数量无限增长。
	for id := range r.specs {
		seen, ok := r.lastSeen[id]
		if ok && now.Sub(seen) <= specRetention {
			continue
		}
		delete(r.specs, id)
		delete(r.lastSeen, id)
	}
	specs := make([]UpstreamSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		specs = append(specs, spec)
	}
	r.Hub.SetUpstreams(specs)
	return endpoints
}

// UntrustedServerNames 返回仓库内配置中尚未获得用户信任的 MCP 服务名（按 agent 过滤）。
// 外部 agent 可能自己读取工作目录里的配置并直接拉起命令，调用方需在线程配置里强制关闭这些服务。
func (r *SessionRouter) UntrustedServerNames(projectDir, agent string) []string {
	servers, err := agentconf.ScanMCP(agentconf.Options{Home: r.Home, ProjectDir: projectDir})
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		if server.Agent != agent || !server.Untrusted || seen[server.Name] {
			continue
		}
		seen[server.Name] = true
		names = append(names, server.Name)
	}
	sort.Strings(names)
	return names
}
