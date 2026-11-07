package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcode/internal/agentconf"
	"mcode/internal/config"
)

func writeSessionConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// trustProjectServers 模拟用户在设置里启用仓库内服务，避开未信任门控。
func trustProjectServers(t *testing.T, home, projectDir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := agentconf.SetMCPServerEnabled(agentconf.Options{Home: home, ProjectDir: projectDir}, name, true); err != nil {
			t.Fatalf("trust %s: %v", name, err)
		}
	}
}

func TestSessionRouter_ProjectPrecedenceAndRefresh(t *testing.T) {
	home, projectA, projectB := t.TempDir(), t.TempDir(), t.TempDir()
	writeSessionConfig(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"shared":{"command":"global-command","env":{"TEST_VALUE":"configured-value"}}}}`)
	writeSessionConfig(t, filepath.Join(projectA, ".mcp.json"), `{"mcpServers":{"shared":{"command":"project-command","env":{"TEST_VALUE":"project-value"}}}}`)
	writeSessionConfig(t, filepath.Join(home, ".codex", "config.toml"), `
[mcp_servers.shared]
command = "lower-priority"
[mcp_servers.disabled]
command = "not-started"
enabled = false
`)
	// 项目配置属于仓库内来源，需先信任否则会被默认禁用而不参与路由。
	trustProjectServers(t, home, projectA, "shared")
	hub := NewHub(nil)
	defer hub.Close()
	proxy := NewProxy(hub)
	if err := proxy.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	router := &SessionRouter{Home: home, Hub: hub, Proxy: proxy}
	a := router.Endpoints(projectA, projectA, nil)
	if len(a) != 1 || a[0].Name != "shared" {
		t.Fatalf("unexpected endpoints: %d", len(a))
	}
	b := router.Endpoints(projectB, projectB, nil)
	if len(b) != 1 || a[0].URL == b[0].URL {
		t.Fatal("project endpoints must be isolated")
	}
	for _, spec := range router.specs {
		if spec.Dir == projectA {
			if spec.Command != "project-command" || spec.Env["TEST_VALUE"] != "project-value" {
				t.Fatal("project override or configured environment lost")
			}
		} else if spec.Dir == projectB {
			if spec.Command != "global-command" || spec.Env["TEST_VALUE"] != "configured-value" {
				t.Fatal("global config priority or environment lost")
			}
		}
	}
	writeSessionConfig(t, filepath.Join(projectA, ".mcp.json"), `{"mcpServers":{"shared":{"command":"updated-command"}}}`)
	// 命令变更后指纹变化，重新模拟一次用户确认信任。
	trustProjectServers(t, home, projectA, "shared")
	refreshed := router.Endpoints(projectA, projectA, nil)
	if len(refreshed) != 1 || refreshed[0].URL != a[0].URL {
		t.Fatal("refresh must retain endpoint identity")
	}
	for _, spec := range router.specs {
		if spec.Dir == projectA && spec.Command != "updated-command" {
			t.Fatal("configuration was not refreshed")
		}
	}
	worktree := t.TempDir()
	wtEndpoints := router.Endpoints(projectA, worktree, nil)
	if len(wtEndpoints) != 1 || wtEndpoints[0].URL == a[0].URL {
		t.Fatal("worktree must have a separate upstream process")
	}
	foundWorktree := false
	for _, spec := range router.specs {
		if spec.Dir == worktree && spec.Command == "updated-command" {
			foundWorktree = true
		}
	}
	if !foundWorktree {
		t.Fatal("worktree session did not load project config with its own working directory")
	}
}

func TestSessionRouter_HTTPAuthAndProxyFlow(t *testing.T) {
	var mu sync.Mutex
	var auth, custom string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth, custom = r.Header.Get("Authorization"), r.Header.Get("X-Test")
		mu.Unlock()
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "external-ok"}}}
		}
		writeRPCResponse(w, req.ID, result, nil)
	}))
	defer upstream.Close()
	server := agentconf.MCPServer{Name: "remote", URL: upstream.URL}
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(tokenAccount(server.AuthID()), `{"access_token":"fixture-oauth-token"}`); err != nil {
		t.Fatal(err)
	}
	oauth := NewOAuthManager(OAuthOptions{Secrets: secrets})
	hub := NewHub(oauth)
	defer hub.Close()
	proxy := NewProxy(hub)
	if err := proxy.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	home := t.TempDir()
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"remote": map[string]any{"type": "http", "url": upstream.URL, "headers": map[string]string{"X-Test": "configured-header"}}}})
	if err != nil {
		t.Fatal(err)
	}
	writeSessionConfig(t, filepath.Join(home, ".claude.json"), string(data))
	router := &SessionRouter{Home: home, Hub: hub, Proxy: proxy}
	endpoints := router.Endpoints(home, home, nil)
	if len(endpoints) != 1 {
		t.Fatal("remote endpoint missing")
	}
	endpoint := endpoints[0]
	for i, method := range []string{"initialize", "tools/list", "tools/call"} {
		status, res := postRPC(t, endpoint.URL, endpoint.Token, "", map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "method": method,
			"params": map[string]any{"name": "echo", "arguments": map[string]any{}},
		})
		if status != http.StatusOK || res["error"] != nil {
			t.Fatalf("%s failed: %v", method, res)
		}
	}
	mu.Lock()
	validHeaders := auth == "Bearer fixture-oauth-token" && custom == "configured-header"
	mu.Unlock()
	if !validHeaders {
		t.Fatal("configured headers or OAuth token did not reach upstream")
	}
	if err := oauth.SignOut(server.AuthID()); err != nil {
		t.Fatal(err)
	}
	_, res := postRPC(t, endpoint.URL, endpoint.Token, "", map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/list"})
	mu.Lock()
	signedOut := auth == ""
	mu.Unlock()
	if res["error"] != nil || !signedOut {
		t.Fatal("sign-out did not remove the OAuth header, or public server became inaccessible")
	}
}

func TestScanMCP_RuntimeCredentialsRemainPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCP_FIXTURE_TOKEN", "environment-token")
	writeSessionConfig(t, filepath.Join(home, ".codex", "config.toml"), `
[mcp_servers.fixture]
command = "fixture"
env = { INLINE = "literal,with,commas" }
[mcp_servers.fixture.env]
CONFIGURED = "config-only-value"
[mcp_servers.http]
url = "http://example.invalid/mcp"
bearer_token_env_var = "MCP_FIXTURE_TOKEN"
http_headers = { "X-Inline" = "inline-header" }
[mcp_servers.http.http_headers]
"X-Configured" = "configured-header"
[mcp_servers.http.env_http_headers]
"X-Environment" = "MCP_FIXTURE_TOKEN"
`)
	public, err := agentconf.ScanMCP(agentconf.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range public {
		if len(server.Env) != 0 || len(server.Headers) != 0 {
			t.Fatal("settings scan exposed credentials")
		}
	}
	private, err := agentconf.ScanMCP(agentconf.Options{Home: home, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range private {
		spec := SpecFromAgentconf(server.Name, server)
		switch server.Name {
		case "fixture":
			if spec.Env["CONFIGURED"] != "config-only-value" || spec.Env["INLINE"] != "literal,with,commas" {
				t.Fatal("TOML environment values lost")
			}
		case "http":
			if spec.Headers["Authorization"] != "Bearer environment-token" || spec.Headers["X-Configured"] != "configured-header" || spec.Headers["X-Inline"] != "inline-header" || spec.Headers["X-Environment"] != "environment-token" {
				t.Fatal("TOML HTTP headers lost")
			}
		}
		data, err := json.Marshal(server)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "config-only-value") || strings.Contains(string(data), "environment-token") {
			t.Fatal("serialized server exposed credentials")
		}
	}
}

// 仓库内未信任的 codex 服务名要能被外部 agent 拿到，用于在线程配置里强制关闭。
func TestSessionRouter_UntrustedServerNames(t *testing.T) {
	home, projectDir := t.TempDir(), t.TempDir()
	writeSessionConfig(t, filepath.Join(projectDir, ".codex", "config.toml"), `
[mcp_servers.evil]
command = "evil-command"
`)
	router := &SessionRouter{Home: home}
	names := router.UntrustedServerNames(projectDir, "codex")
	if len(names) != 1 || names[0] != "evil" {
		t.Fatalf("unexpected untrusted names: %v", names)
	}
	if got := router.UntrustedServerNames(projectDir, "claude"); len(got) != 0 {
		t.Fatalf("其它 agent 不应返回 codex 的服务：%v", got)
	}
	trustProjectServers(t, home, projectDir, "evil")
	if got := router.UntrustedServerNames(projectDir, "codex"); len(got) != 0 {
		t.Fatalf("信任后不应再屏蔽：%v", got)
	}
}
