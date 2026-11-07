package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
)

func TestMergeMCPArgs_ProxyWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"remote":{"command":"direct-command"},"other":{"command":"other-command","env":{"KEY":"value"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	generated := buildMCPArgs(harness.Turn{WorkDir: "/repo"}, "", "", harness.BuiltinMCP{URL: "http://builtin", Token: "token"}, []harness.MCPEndpoint{{Name: "remote", URL: "http://proxy"}})
	args, err := mergeMCPArgs(generated, []string{"--verbose", "--mcp-config", path})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for i, arg := range args {
		if arg != "--mcp-config" {
			continue
		}
		count++
		var cfg struct {
			Servers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Servers) != 3 || cfg.Servers["remote"]["url"] != "http://proxy" || cfg.Servers["remote"]["command"] != nil || cfg.Servers["other"]["command"] != "other-command" {
			t.Fatal("merge lost a server or retained a direct duplicate")
		}
	}
	if count != 1 {
		t.Fatalf("expected one --mcp-config, got %d", count)
	}
	if _, err := mergeMCPArgs(nil, []string{"--mcp-config", "{bad"}); err == nil {
		t.Fatal("malformed config accepted")
	}
}

// TestExternalizeMCPConfig 验证 MCP 令牌不进 argv：--mcp-config 的内联 JSON 被换成 0600
// 临时文件路径，文件内容仍是原配置，本轮结束后清理函数会把文件删掉。
func TestExternalizeMCPConfig(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	generated := buildMCPArgs(harness.Turn{WorkDir: "/repo", Mode: harness.ModeAgent, Approver: testDummyApprover{}}, "http://127.0.0.1:1/mcp", token,
		harness.BuiltinMCP{URL: "http://127.0.0.1:2/mcp", Token: token}, nil)
	args := buildArgs(harness.Turn{}, generated...)

	out, cleanup, err := externalizeMCPConfig(args)
	if err != nil {
		t.Fatalf("externalizeMCPConfig failed: %v", err)
	}

	var configPath string
	for i, arg := range out {
		if arg != "--mcp-config" || i+1 >= len(out) {
			continue
		}
		configPath = out[i+1]
		if strings.Contains(configPath, token) {
			t.Fatalf("token leaked into argv: %s", configPath)
		}
	}
	if configPath == "" {
		t.Fatalf("expected --mcp-config in args: %v", out)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("临时配置文件不存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("临时配置文件权限应为 0600，实际 %o", perm)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取临时配置文件: %v", err)
	}
	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("临时配置文件不是合法 JSON: %v", err)
	}
	if cfg.MCPServers["mcode_approval"].Headers["Authorization"] != "Bearer "+token {
		t.Errorf("临时配置文件丢了审批 server 的令牌: %s", data)
	}

	cleanup()
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("清理后临时配置文件应被删除，stat 错误: %v", err)
	}
}
