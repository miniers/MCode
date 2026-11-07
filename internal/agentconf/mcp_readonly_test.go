package agentconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeReadOnlyFixture 在临时 home 下写入外部 Agent 的 MCP 配置。
func writeReadOnlyFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// findServer 按 Agent+Name 取扫描结果，取不到直接失败。
func findServer(t *testing.T, servers []MCPServer, agent, name string) MCPServer {
	t.Helper()
	for _, srv := range servers {
		if srv.Agent == agent && srv.Name == name {
			return srv
		}
	}
	t.Fatalf("missing %s/%s in scan result", agent, name)
	return MCPServer{}
}

// TestScanMCP_ReadOnlyExternalAgentSources 覆盖：agy 与 pi 的外部配置能扫出来，
// 标记成只读、全局作用域，并且即使开启 WithCredentials 也不带出任何凭据取值。
func TestScanMCP_ReadOnlyExternalAgentSources(t *testing.T) {
	home := t.TempDir()
	agyPath := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	piPath := filepath.Join(home, ".pi", "agent", "mcp.json")

	writeReadOnlyFixture(t, agyPath, `{
  "mcpServers": {
    "agy-remote": {
      "serverUrl": "https://agy.example.com/mcp",
      "headers": {
        "Authorization": "Bearer agy-secret"
      }
    },
    "agy-stdio": {
      "command": "agy-mcp",
      "args": ["--serve"],
      "env": {
        "AGY_TOKEN": "agy-env-secret"
      }
    }
  }
}`)
	writeReadOnlyFixture(t, piPath, `{
  "mcpServers": {
    "pi-stdio": {
      "command": "pi-mcp",
      "args": ["--stdio"]
    }
  }
}`)

	// 1. 默认（脱敏）扫描：ReadOnly 标记与 URL 推断都要对。
	servers, err := ScanMCP(Options{Home: home})
	if err != nil {
		t.Fatalf("ScanMCP error: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}

	remote := findServer(t, servers, "agy", "agy-remote")
	if !remote.ReadOnly {
		t.Error("agy 条目必须标记为只读")
	}
	if remote.Scope != "global" {
		t.Errorf("agy 作用域应为 global，got %q", remote.Scope)
	}
	if remote.Transport != "http" {
		t.Errorf("serverUrl 应按 http 处理，got %q", remote.Transport)
	}
	if remote.URL != "https://agy.example.com/mcp" {
		t.Errorf("serverUrl 未作为 URL 使用，got %q", remote.URL)
	}
	absAgy, _ := filepath.Abs(agyPath)
	if remote.Source != absAgy {
		t.Errorf("来源应为绝对路径 %q，got %q", absAgy, remote.Source)
	}
	if remote.Disabled {
		t.Error("只读条目不应被停用")
	}

	stdio := findServer(t, servers, "agy", "agy-stdio")
	if stdio.Transport != "stdio" || stdio.Command != "agy-mcp" {
		t.Errorf("agy stdio 条目解析错误：transport=%q command=%q", stdio.Transport, stdio.Command)
	}
	if len(stdio.Args) != 1 || stdio.Args[0] != "--serve" {
		t.Errorf("agy stdio 参数解析错误：%v", stdio.Args)
	}
	// 键名保留，取值不展开。
	if len(stdio.EnvKeys) != 1 || stdio.EnvKeys[0] != "AGY_TOKEN" {
		t.Errorf("环境变量键名应保留：%v", stdio.EnvKeys)
	}

	pi := findServer(t, servers, "pi", "pi-stdio")
	if !pi.ReadOnly {
		t.Error("pi 条目必须标记为只读")
	}
	if pi.Scope != "global" || pi.Command != "pi-mcp" {
		t.Errorf("pi 条目解析错误：scope=%q command=%q", pi.Scope, pi.Command)
	}
	absPi, _ := filepath.Abs(piPath)
	if pi.Source != absPi {
		t.Errorf("pi 来源应为绝对路径 %q，got %q", absPi, pi.Source)
	}

	// 2. WithCredentials 扫描：只读条目的 Env/Headers 取值必须为空。
	private, err := ScanMCP(Options{Home: home, WithCredentials: true})
	if err != nil {
		t.Fatalf("ScanMCP(WithCredentials) error: %v", err)
	}
	for _, srv := range private {
		if len(srv.Env) != 0 {
			t.Errorf("%s 只读来源不应带出环境变量取值：%v", srv.Name, srv.Env)
		}
		if len(srv.Headers) != 0 {
			t.Errorf("%s 只读来源不应带出请求头取值：%v", srv.Name, srv.Headers)
		}
		if len(srv.SecretEnvKeys) != 0 {
			t.Errorf("%s 只读来源不应声明凭据变量：%v", srv.Name, srv.SecretEnvKeys)
		}
	}
}

// TestScanMCP_ReadOnlySourcesMissing 覆盖：两个外部配置文件都不存在时不报错，也不改变扫描结果。
func TestScanMCP_ReadOnlySourcesMissing(t *testing.T) {
	home := t.TempDir()
	writeReadOnlyFixture(t, filepath.Join(home, ".claude.json"), `{
  "mcpServers": {
    "claude-only": {"command": "claude-mcp"}
  }
}`)

	servers, err := ScanMCP(Options{Home: home})
	if err != nil {
		t.Fatalf("缺少外部配置时不应报错：%v", err)
	}
	if len(servers) != 1 || servers[0].Agent != "claude" {
		t.Fatalf("扫描结果应只含 claude 条目，got %d", len(servers))
	}

	// 文件存在但内容损坏时同样静默跳过。
	writeReadOnlyFixture(t, filepath.Join(home, ".pi", "agent", "mcp.json"), "{not json")
	servers, err = ScanMCP(Options{Home: home})
	if err != nil {
		t.Fatalf("外部配置损坏时不应报错：%v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("损坏的外部配置不应产生条目，got %d", len(servers))
	}
}

// TestScanMCP_ReadOnlyIgnoresDisabledSet 覆盖：自定义配置里的停用表按名字跨 Agent 生效，
// 但不能作用到只读来源（设置页也不给它启停开关）。
func TestScanMCP_ReadOnlyIgnoresDisabledSet(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	writeReadOnlyFixture(t, filepath.Join(dataDir, "mcp.json"), `{
  "disabledServers": ["shared-handle"]
}`)
	writeReadOnlyFixture(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), `{
  "mcpServers": {
    "shared-handle": {"command": "agy-mcp"}
  }
}`)

	servers, err := ScanMCP(Options{Home: home, DataDir: dataDir})
	if err != nil {
		t.Fatalf("ScanMCP error: %v", err)
	}
	srv := findServer(t, servers, "agy", "shared-handle")
	if srv.Disabled {
		t.Error("停用表不应作用到只读来源")
	}
}

// TestSaveMCPServer_ReadOnlyRejected 覆盖：只读来源不允许保存修改。
func TestSaveMCPServer_ReadOnlyRejected(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	err := SaveMCPServer(Options{Home: home, DataDir: dataDir}, MCPServer{
		Name:      "agy-remote",
		Agent:     "agy",
		Scope:     "global",
		Transport: "http",
		URL:       "https://agy.example.com/mcp",
		ReadOnly:  true,
	})
	if err == nil {
		t.Fatal("只读来源不应允许保存")
	}
	if !strings.Contains(err.Error(), "只读") {
		t.Fatalf("错误提示应说明只读：%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "mcp.json")); statErr == nil {
		t.Error("被拒绝的保存不应写配置文件")
	}
}

// TestSetMCPServerEnabledFor_ReadOnlyRejected 覆盖：只读来源不允许启停。
func TestSetMCPServerEnabledFor_ReadOnlyRejected(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	srv := MCPServer{
		Name:     "agy-remote",
		Agent:    "agy",
		Scope:    "global",
		ReadOnly: true,
	}
	err := SetMCPServerEnabledFor(Options{Home: home, DataDir: dataDir}, srv, true)
	if err == nil {
		t.Fatal("只读来源不应允许启停")
	}
	if !strings.Contains(err.Error(), "只读") {
		t.Fatalf("错误提示应说明只读：%v", err)
	}
}
