package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/agentconf"
)

// TestSessionRouterEndpoints_HonorsThreadDisabled 验证会话级禁用名单能在全局设置之上再筛掉指定服务。
func TestSessionRouterEndpoints_HonorsThreadDisabled(t *testing.T) {
	home, projectDir := t.TempDir(), t.TempDir()
	writeSessionConfig(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"alpha":{"command":"alpha-command"},"beta":{"command":"beta-command"}}}`)

	hub := NewHub(nil)
	defer hub.Close()
	proxy := NewProxy(hub)
	if err := proxy.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	router := &SessionRouter{Home: home, Hub: hub, Proxy: proxy}

	all := router.Endpoints(projectDir, projectDir, nil)
	if len(all) != 2 {
		t.Fatalf("want 2 endpoints, got %d", len(all))
	}

	// 用扫描结果算出设置页展示的键，确保会话级禁用与设置页用的是同一套键。
	servers, err := agentconf.ScanMCP(agentconf.Options{Home: home, ProjectDir: projectDir, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	var alphaKey string
	for _, server := range servers {
		if server.Name == "alpha" {
			alphaKey = EndpointKey(server.Agent, server.Scope, server.Name)
		}
	}
	if alphaKey == "" {
		t.Fatal("alpha not found in scan")
	}

	filtered := router.Endpoints(projectDir, projectDir, map[string]bool{alphaKey: true})
	if len(filtered) != 1 || filtered[0].Name != "beta" {
		t.Fatalf("thread-level disabled must filter alpha, got %v", filtered)
	}

	// 空名单等价于沿用全局设置。
	if got := router.Endpoints(projectDir, projectDir, map[string]bool{}); len(got) != 2 {
		t.Fatalf("empty disabled must keep all endpoints, got %d", len(got))
	}
}
