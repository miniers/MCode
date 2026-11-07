package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/agentconf"
)

// TestSessionRouter_ReadOnlySourcesNotInjected 覆盖：agy 与 pi 的外部配置只在设置页展示，
// 不得进入会话注入；同名时仍以 claude 的配置为准。
func TestSessionRouter_ReadOnlySourcesNotInjected(t *testing.T) {
	home, projectDir := t.TempDir(), t.TempDir()
	writeSessionConfig(t, filepath.Join(home, ".claude.json"), `{
  "mcpServers": {
    "claude-only": {"command": "claude-command"},
    "nowledge-mem": {"command": "claude-shared"}
  }
}`)
	writeSessionConfig(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), `{
  "mcpServers": {
    "agy-only": {"command": "agy-command"},
    "nowledge-mem": {"command": "agy-shared"}
  }
}`)
	writeSessionConfig(t, filepath.Join(home, ".pi", "agent", "mcp.json"), `{
  "mcpServers": {
    "pi-only": {"command": "pi-command"}
  }
}`)

	hub := NewHub(nil)
	defer hub.Close()
	proxy := NewProxy(hub)
	if err := proxy.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	router := &SessionRouter{Home: home, Hub: hub, Proxy: proxy}

	endpoints := router.Endpoints(projectDir, projectDir, nil)
	names := make(map[string]bool, len(endpoints))
	for _, ep := range endpoints {
		names[ep.Name] = true
	}
	if len(endpoints) != 2 || !names["claude-only"] || !names["nowledge-mem"] {
		t.Fatalf("endpoints 应只含 claude 的服务，got %v", names)
	}

	// 同名 nowledge-mem 必须是 claude 的那一份（agy 的命令被白名单挡掉）。
	foundShared := false
	for _, spec := range router.specs {
		if spec.Name != "nowledge-mem" {
			continue
		}
		foundShared = true
		if spec.Command != "claude-shared" {
			t.Fatalf("同名服务应保留 claude 的配置，got command %q", spec.Command)
		}
	}
	if !foundShared {
		t.Fatal("缺少 nowledge-mem 的上游配置")
	}
	// agy/pi 独有的服务连上游配置都不该生成。
	for _, spec := range router.specs {
		if spec.Name == "agy-only" || spec.Name == "pi-only" {
			t.Fatalf("只读来源 %s 不应生成上游配置", spec.Name)
		}
	}
}

// TestInjectionAllowed_Whitelist 覆盖：注入白名单失败关闭——只有 claude/codex/custom/mcode
// 能进入会话，agy、pi、未知 Agent 以及任何 ReadOnly 条目一律不注入。
func TestInjectionAllowed_Whitelist(t *testing.T) {
	allowed := []agentconf.MCPServer{
		{Agent: "claude", Name: "a"},
		{Agent: "codex", Name: "b"},
		{Agent: "custom", Name: "c"},
		{Agent: "mcode", Name: "d"},
	}
	for _, srv := range allowed {
		if !injectionAllowed(srv) {
			t.Errorf("内置来源 %s 应允许注入", srv.Agent)
		}
	}

	denied := []agentconf.MCPServer{
		{Agent: "agy", Name: "e"},
		{Agent: "pi", Name: "f"},
		{Agent: "something-new", Name: "g"},
		{Agent: "claude", Name: "h", ReadOnly: true},
		{Agent: "custom", Name: "i", ReadOnly: true},
	}
	for _, srv := range denied {
		if injectionAllowed(srv) {
			t.Errorf("%s/只读=%v 不应允许注入", srv.Agent, srv.ReadOnly)
		}
	}
}
