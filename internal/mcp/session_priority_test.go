package mcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/agentconf"
)

// 同名同作用域时按来源 agent 排序：custom > claude > codex > 其他。
func TestMCPServerWins_来源优先级矩阵(t *testing.T) {
	cases := []struct {
		name      string
		candidate agentconf.MCPServer
		current   agentconf.MCPServer
		want      bool
	}{
		{
			name:      "custom 压过 claude",
			candidate: agentconf.MCPServer{Agent: "custom", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "claude", Scope: "global"},
			want:      true,
		},
		{
			name:      "claude 压不过 custom",
			candidate: agentconf.MCPServer{Agent: "claude", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "custom", Scope: "global"},
			want:      false,
		},
		{
			name:      "claude 压过 codex",
			candidate: agentconf.MCPServer{Agent: "claude", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "codex", Scope: "global"},
			want:      true,
		},
		{
			name:      "codex 压过未知 agent",
			candidate: agentconf.MCPServer{Agent: "codex", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "cursor", Scope: "global"},
			want:      true,
		},
		{
			name:      "未知 agent 压不过 codex",
			candidate: agentconf.MCPServer{Agent: "cursor", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "codex", Scope: "global"},
			want:      false,
		},
		{
			name:      "同来源保持先到者",
			candidate: agentconf.MCPServer{Agent: "claude", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "claude", Scope: "global"},
			want:      false,
		},
		{
			name:      "project 压过 global",
			candidate: agentconf.MCPServer{Agent: "codex", Scope: "project"},
			current:   agentconf.MCPServer{Agent: "custom", Scope: "global"},
			want:      true,
		},
		{
			name:      "global 压不过 project",
			candidate: agentconf.MCPServer{Agent: "custom", Scope: "global"},
			current:   agentconf.MCPServer{Agent: "codex", Scope: "project"},
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpServerWins(tc.candidate, tc.current); got != tc.want {
				t.Fatalf("期望 %v，实际 %v", tc.want, got)
			}
		})
	}
}

// 作用域不同时 agent 优先级不参与比较。
func TestAgentPriority_排序符合预期(t *testing.T) {
	if agentPriority("custom") <= agentPriority("claude") {
		t.Fatal("custom 应高于 claude")
	}
	if agentPriority("claude") <= agentPriority("codex") {
		t.Fatal("claude 应高于 codex")
	}
	if agentPriority("codex") <= agentPriority("anything") {
		t.Fatal("codex 应高于未知 agent")
	}
}

// newSessionRouterForTest 构造带 Hub 与已启动 Proxy 的路由器，便于直接验证 specs 清理。
func newSessionRouterForTest(t *testing.T, home string) *SessionRouter {
	t.Helper()
	hub := NewHub(nil)
	t.Cleanup(func() { _ = hub.Close() })
	proxy := NewProxy(hub)
	if err := proxy.Start(context.Background()); err != nil {
		t.Fatalf("proxy start failed: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	return &SessionRouter{Hub: hub, Proxy: proxy, Home: home}
}

// 超过 specRetention 未再出现的端点连同上游配置一起清理。
func TestSessionRouter_过期spec被清理(t *testing.T) {
	home, projectDir := t.TempDir(), t.TempDir()
	writeSessionConfig(t, filepath.Join(home, ".claude.json"),
		`{"mcpServers":{"keep":{"command":"keep-command"}}}`)
	router := newSessionRouterForTest(t, home)

	endpoints := router.Endpoints(projectDir, projectDir, nil)
	if len(endpoints) != 1 {
		t.Fatalf("期望 1 个端点，实际 %d", len(endpoints))
	}
	var keepID string
	for id := range router.specs {
		keepID = id
	}
	if keepID == "" {
		t.Fatal("端点配置未写入 specs")
	}

	// 手工塞入一个早已消失的端点，模拟项目关闭后残留的配置。
	router.mu.Lock()
	router.specs["stale-id"] = UpstreamSpec{ID: "stale-id", Name: "stale", Transport: "stdio"}
	router.lastSeen["stale-id"] = time.Now().Add(-2 * specRetention)
	router.mu.Unlock()

	router.Endpoints(projectDir, projectDir, nil)

	router.mu.Lock()
	_, staleExists := router.specs["stale-id"]
	_, staleSeen := router.lastSeen["stale-id"]
	_, keepExists := router.specs[keepID]
	keepSeen := router.lastSeen[keepID]
	router.mu.Unlock()

	if staleExists || staleSeen {
		t.Fatal("超期未出现的端点应被清理")
	}
	if !keepExists {
		t.Fatal("仍在使用的端点不应被清理")
	}
	if time.Since(keepSeen) > time.Minute {
		t.Fatal("存活端点的 lastSeen 应被刷新")
	}
}

// 未超期的端点不应被清理。
func TestSessionRouter_未过期spec保留(t *testing.T) {
	home, projectDir := t.TempDir(), t.TempDir()
	router := newSessionRouterForTest(t, home)
	router.Endpoints(projectDir, projectDir, nil)

	router.mu.Lock()
	router.specs["recent-id"] = UpstreamSpec{ID: "recent-id", Name: "recent", Transport: "stdio"}
	router.lastSeen["recent-id"] = time.Now().Add(-time.Hour)
	router.mu.Unlock()

	router.Endpoints(projectDir, projectDir, nil)

	router.mu.Lock()
	_, exists := router.specs["recent-id"]
	router.mu.Unlock()
	if !exists {
		t.Fatal("未超过保留期的端点应保留")
	}
}
