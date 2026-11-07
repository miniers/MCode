package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// newRemoteMCPTestService 造一个带真实 MemoryBackend 的 remoteMCPService，
// 并登记一个临时目录作为项目。返回服务、后端、项目 ID 与项目路径。
func newRemoteMCPTestService(t *testing.T) (*remoteMCPService, *remote.MemoryBackend, string, string) {
	t.Helper()
	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())

	projectDir := t.TempDir()
	proj, err := backend.AddProjectPath(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("add project: %v", err)
	}
	resetMCPExtScanCache()
	t.Cleanup(resetMCPExtScanCache)
	return &remoteMCPService{client: NewClient(backend)}, backend, proj.ID, proj.Path
}

// stubMCPExtScan 替换扫描入口，返回预置服务器并记录调用次数。
func stubMCPExtScan(t *testing.T, servers []agentconf.MCPServer) *int {
	t.Helper()
	calls := 0
	orig := mcpExtScanFunc
	mcpExtScanFunc = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		calls++
		return servers, nil
	}
	t.Cleanup(func() { mcpExtScanFunc = orig })
	return &calls
}

func TestRemoteMCPServiceListServers(t *testing.T) {
	svc, backend, projectID, _ := newRemoteMCPTestService(t)
	servers := []agentconf.MCPServer{
		{Agent: "claude", Scope: "project", Name: "fs", Source: "/tmp/project/.mcp.json", Transport: "stdio", Command: "npx fs"},
		{Agent: "pi", Scope: "home", Name: "web", Source: "/tmp/mcp.json", Transport: "http", URL: "https://example.com/mcp"},
		{Agent: "custom", Scope: "home", Name: "legacy", Disabled: true},
		{Agent: "agy", Scope: "home", Name: "external", ReadOnly: true},
	}
	calls := stubMCPExtScan(t, servers)

	ctx := context.Background()
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projectID})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// 只禁用 project:fs，其余保持启用；全局已停用与被锁住的服务单独标注。
	disabledKey := "claude:project:fs"
	if _, err := backend.SetThreadMCPDisabled(ctx, created.ThreadID, []string{disabledKey}); err != nil {
		t.Fatalf("set mcp disabled: %v", err)
	}

	result, err := svc.ListServers(ctx, created.ThreadID, "")
	if err != nil {
		t.Fatalf("list servers: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("unexpected scan calls: %d", *calls)
	}
	if result.ThreadID != created.ThreadID || result.ProjectID != projectID {
		t.Fatalf("unexpected ids: %+v", result)
	}
	if len(result.Servers) != len(servers) {
		t.Fatalf("unexpected server count: %+v", result.Servers)
	}
	byKey := make(map[string]wire.MCPExtServer, len(result.Servers))
	for _, s := range result.Servers {
		byKey[s.Key] = s
	}
	fs, ok := byKey[disabledKey]
	if !ok {
		t.Fatalf("project fs missing: %+v", result.Servers)
	}
	if !fs.Disabled || fs.Locked || fs.Transport != "stdio" || fs.Command != "npx fs" {
		t.Fatalf("unexpected project server: %+v", fs)
	}
	web := byKey["pi:home:web"]
	if web.Disabled || web.Locked || web.URL != "https://example.com/mcp" {
		t.Fatalf("unexpected home server: %+v", web)
	}
	if legacy := byKey["custom:home:legacy"]; legacy.Disabled || !legacy.Locked || legacy.LockReason != "全局已停用" {
		t.Fatalf("globally disabled server should be locked, not session-disabled: %+v", legacy)
	}
	if external := byKey["agy:home:external"]; !external.Locked || external.LockReason != "只读" {
		t.Fatalf("read-only server should be locked: %+v", external)
	}
}

func TestRemoteMCPServiceListServersByProject(t *testing.T) {
	svc, _, projectID, _ := newRemoteMCPTestService(t)
	stubMCPExtScan(t, []agentconf.MCPServer{{Agent: "pi", Scope: "home", Name: "web"}})

	result, err := svc.ListServers(context.Background(), "", projectID)
	if err != nil {
		t.Fatalf("list servers: %v", err)
	}
	if result.ThreadID != "" || result.ProjectID != projectID {
		t.Fatalf("unexpected ids: %+v", result)
	}
	if len(result.Servers) != 1 || result.Servers[0].Disabled {
		t.Fatalf("unexpected servers: %+v", result.Servers)
	}
}

func TestRemoteMCPServiceListServersUnknownThread(t *testing.T) {
	svc, _, _, _ := newRemoteMCPTestService(t)
	stubMCPExtScan(t, nil)

	_, err := svc.ListServers(context.Background(), "3f0d2a86-0b52-4a45-9a2e-2a4bd1f2f0aa", "")
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ResourceNotFound, got: %v", err)
	}
}

func TestRemoteMCPServiceListServersUnknownProject(t *testing.T) {
	svc, _, _, _ := newRemoteMCPTestService(t)
	stubMCPExtScan(t, nil)

	_, err := svc.ListServers(context.Background(), "", "3f0d2a86-0b52-4a45-9a2e-2a4bd1f2f0aa")
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ResourceNotFound, got: %v", err)
	}
}

// 60s 内重复调用只扫一次，避免面板每次打开都读一遍配置。
func TestRemoteMCPServiceScanCache(t *testing.T) {
	svc, _, projectID, _ := newRemoteMCPTestService(t)
	calls := stubMCPExtScan(t, []agentconf.MCPServer{{Agent: "pi", Scope: "home", Name: "web"}})

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := svc.ListServers(ctx, "", projectID); err != nil {
			t.Fatalf("list servers: %v", err)
		}
	}
	if *calls != 1 {
		t.Fatalf("scan should be cached, calls=%d", *calls)
	}
}

// 全局设置锁住的原因与桌面端一致：只读 > 待信任 > 全局已停用。
func TestMCPExtLockReason(t *testing.T) {
	cases := []struct {
		name   string
		srv    agentconf.MCPServer
		locked bool
		reason string
	}{
		{"readOnly", agentconf.MCPServer{ReadOnly: true, Untrusted: true, Disabled: true}, true, "只读"},
		{"untrusted", agentconf.MCPServer{Untrusted: true, Disabled: true}, true, "待信任"},
		{"disabled", agentconf.MCPServer{Disabled: true}, true, "全局已停用"},
		{"enabled", agentconf.MCPServer{}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locked, reason := mcpExtLockReason(tc.srv)
			if locked != tc.locked || reason != tc.reason {
				t.Fatalf("got (%v, %q) want (%v, %q)", locked, reason, tc.locked, tc.reason)
			}
		})
	}
}
