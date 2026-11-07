package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
)

// decodeMCPArgs 解析 `--additional-mcp-config <json>` 参数，返回 mcpServers 表。
func decodeMCPArgs(t *testing.T, args []string) map[string]mcpServerConfig {
	t.Helper()
	if len(args) != 2 || args[0] != "--additional-mcp-config" {
		t.Fatalf("参数不符合预期: %v", args)
	}
	var cfg mcpConfig
	if err := json.Unmarshal([]byte(args[1]), &cfg); err != nil {
		t.Fatalf("解析 additional-mcp-config 失败: %v", err)
	}
	return cfg.MCPServers
}

func TestBuildMCPArgs_ThreadEndpoint(t *testing.T) {
	builtin := harness.BuiltinMCP{ThreadBase: "http://127.0.0.1:9000/mcp/", ThreadToken: "thread-token", URL: "http://shared/mcp", Token: "shared-token"}
	turn := harness.Turn{ThreadID: "thread-1", WorkDir: "/repo/a"}

	args := buildMCPArgs(turn, builtin, []harness.MCPEndpoint{{Name: "remote", URL: "http://proxy/remote", Token: "ext-token"}})
	servers := decodeMCPArgs(t, args)

	if len(servers) != 2 {
		t.Fatalf("服务数量应为 2（内置 + 外部），实际: %v", servers)
	}
	got, ok := servers["mcode"]
	if !ok {
		t.Fatalf("缺少内置服务 mcode: %v", servers)
	}
	if got.Type != "http" || got.URL != "http://127.0.0.1:9000/mcp/thread-1" {
		t.Errorf("内置服务应为线程端点，实际: %+v", got)
	}
	if got.Headers["Authorization"] != "Bearer thread-token" {
		t.Errorf("内置服务令牌错误: %v", got.Headers)
	}
	if _, has := got.Headers["X-Mcode-Workdir"]; has {
		t.Errorf("线程端点不应带 workdir 头: %v", got.Headers)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "*" {
		t.Errorf("工具应放开全部: %v", got.Tools)
	}

	ext := servers["remote"]
	if ext.Type != "http" || ext.URL != "http://proxy/remote" || ext.Headers["Authorization"] != "Bearer ext-token" {
		t.Errorf("外部端点条目错误: %+v", ext)
	}
}

func TestBuildMCPArgs_SharedEndpointWorkdirHeader(t *testing.T) {
	builtin := harness.BuiltinMCP{URL: "http://shared/mcp", Token: "shared-token"}
	turn := harness.Turn{WorkDir: "/repo/a"}

	servers := decodeMCPArgs(t, buildMCPArgs(turn, builtin, nil))
	got, ok := servers["mcode"]
	if !ok {
		t.Fatalf("缺少内置服务 mcode: %v", servers)
	}
	if got.URL != "http://shared/mcp" {
		t.Errorf("无线程端点时应退回共享端点，实际: %q", got.URL)
	}
	if got.Headers["X-Mcode-Workdir"] != "/repo/a" {
		t.Errorf("共享端点应带 workdir 头: %v", got.Headers)
	}
}

func TestBuildMCPArgs_SkipsReservedExternalNames(t *testing.T) {
	external := []harness.MCPEndpoint{
		{Name: "mcode", URL: "http://duplicate"},
		{Name: "mcode_approval", URL: "http://approval"},
	}
	if args := buildMCPArgs(harness.Turn{}, harness.BuiltinMCP{}, external); args != nil {
		t.Fatalf("保留名与无内置端点时不应生成参数: %v", args)
	}
}

func TestHarnessMCPArgs_UsesExternalFunc(t *testing.T) {
	var gotWorkDir string
	h := New(Options{
		BuiltinMCP: harness.BuiltinMCP{URL: "http://shared/mcp", Token: "tok"},
		ExternalMCP: func(workDir, threadID string) []harness.MCPEndpoint {
			gotWorkDir = workDir
			return []harness.MCPEndpoint{{Name: "remote", URL: "http://proxy/remote"}}
		},
	})

	servers := decodeMCPArgs(t, h.mcpArgs(harness.Turn{WorkDir: "/repo/b"}))
	if gotWorkDir != "/repo/b" {
		t.Errorf("ExternalMCP 收到的 workdir = %q，期望 /repo/b", gotWorkDir)
	}
	if _, ok := servers["remote"]; !ok {
		t.Errorf("缺少外部服务: %v", servers)
	}
	if servers["remote"].Headers["Authorization"] != "" {
		t.Errorf("无令牌时不应带 Authorization 头: %v", servers["remote"].Headers)
	}
}

// TestExternalizeMCPConfig_WritesOwnerOnlyFile 校验令牌只落到 0600 临时文件：argv 里只剩 "@<路径>"。
func TestExternalizeMCPConfig_WritesOwnerOnlyFile(t *testing.T) {
	const token = "secret-token-abc"
	builtin := harness.BuiltinMCP{URL: "http://shared/mcp", Token: token}
	inline := buildMCPArgs(harness.Turn{WorkDir: "/repo/a"}, builtin, nil)

	args, cleanup, err := externalizeMCPConfig(copilotArguments(inline))
	if err != nil {
		t.Fatalf("externalizeMCPConfig 失败: %v", err)
	}
	defer cleanup()

	var path string
	found := false
	for i, arg := range args {
		if strings.Contains(arg, token) {
			t.Fatalf("argv 中不应出现令牌原文: %v", args)
		}
		if arg == "--additional-mcp-config" {
			if i+1 >= len(args) {
				t.Fatalf("缺少 --additional-mcp-config 取值: %v", args)
			}
			value := args[i+1]
			if !strings.HasPrefix(value, "@") {
				t.Fatalf("取值应以 @ 开头（copilot 只把 @ 前缀当文件路径）: %q", value)
			}
			path = strings.TrimPrefix(value, "@")
			found = true
		}
	}
	if !found || path == "" {
		t.Fatalf("未找到 --additional-mcp-config 参数: %v", args)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("临时文件应存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != mcpConfigFileMode {
		t.Errorf("临时文件权限 = %o，期望 %o", perm, mcpConfigFileMode)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("临时文件路径应为绝对路径（copilot 会把相对 @ 路径按会话目录解析）: %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取临时文件失败: %v", err)
	}
	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("临时文件内容应为 MCP 配置 JSON: %v", err)
	}
	if got := cfg.MCPServers["mcode"].Headers["Authorization"]; got != "Bearer "+token {
		t.Errorf("文件里的令牌头错误: %q", got)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup 后临时文件应被删除，stat err = %v", err)
	}
}

// TestExternalizeMCPConfig_PassesThroughPath 校验已经是 @<路径> 的取值不被改写。
func TestExternalizeMCPConfig_PassesThroughPath(t *testing.T) {
	args := []string{"--additional-mcp-config", "@/tmp/user-mcp.json"}
	out, cleanup, err := externalizeMCPConfig(args)
	if err != nil {
		t.Fatalf("externalizeMCPConfig 失败: %v", err)
	}
	defer cleanup()
	if len(out) != 2 || out[1] != "@/tmp/user-mcp.json" {
		t.Fatalf("用户自带的文件路径应原样保留: %v", out)
	}
}

// TestExternalizeMCPConfig_NoMCPArgs 校验无 MCP 参数时不做任何改写。
func TestExternalizeMCPConfig_NoMCPArgs(t *testing.T) {
	out, cleanup, err := externalizeMCPConfig(copilotArguments(nil))
	if err != nil {
		t.Fatalf("externalizeMCPConfig 失败: %v", err)
	}
	defer cleanup()
	if len(out) != len(copilotArguments(nil)) {
		t.Fatalf("无 MCP 参数时应原样返回: %v", out)
	}
}
