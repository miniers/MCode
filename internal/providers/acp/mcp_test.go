package acp

import (
	"reflect"
	"strings"
	"testing"

	"mcode/internal/harness"
)

// mcpEntryByName 在 mcpServers 列表中按 name 取条目。
func mcpEntryByName(t *testing.T, servers []any, name string) map[string]any {
	t.Helper()
	for _, s := range servers {
		entry, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("mcpServers 条目类型错误: %T", s)
		}
		if entry["name"] == name {
			return entry
		}
	}
	t.Fatalf("缺少名为 %q 的服务: %v", name, servers)
	return nil
}

func TestMCPServers_HTTP(t *testing.T) {
	h := New(Options{
		Agent:      AgentOpenCode,
		BuiltinMCP: harness.BuiltinMCP{ThreadBase: "http://127.0.0.1:9000/mcp/", ThreadToken: "thread-token"},
		ExternalMCP: func(workDir, threadID string) []harness.MCPEndpoint {
			return []harness.MCPEndpoint{
				{Name: "remote", URL: "http://proxy/remote", Token: "ext-token"},
				{Name: "mcode", URL: "http://duplicate"},
			}
		},
	})

	servers := h.mcpServers(harness.Turn{ThreadID: "thread-1", WorkDir: "/repo/a"}, true)
	if len(servers) != 2 {
		t.Fatalf("服务数量应为 2（内置 + 外部，保留名跳过），实际: %v", servers)
	}

	builtin := mcpEntryByName(t, servers, "mcode")
	if builtin["type"] != "http" || builtin["url"] != "http://127.0.0.1:9000/mcp/thread-1" {
		t.Errorf("内置服务应为线程端点的 http 条目: %v", builtin)
	}
	wantHeaders := []any{
		map[string]any{"name": "Authorization", "value": "Bearer thread-token"},
	}
	if !reflect.DeepEqual(builtin["headers"], wantHeaders) {
		t.Errorf("内置服务 headers = %v，期望 %v", builtin["headers"], wantHeaders)
	}

	ext := mcpEntryByName(t, servers, "remote")
	if ext["url"] != "http://proxy/remote" {
		t.Errorf("外部端点 url = %v", ext["url"])
	}
	wantExtHeaders := []any{
		map[string]any{"name": "Authorization", "value": "Bearer ext-token"},
	}
	if !reflect.DeepEqual(ext["headers"], wantExtHeaders) {
		t.Errorf("外部端点 headers = %v，期望 %v", ext["headers"], wantExtHeaders)
	}
}

func TestMCPServers_StdioFallback(t *testing.T) {
	h := New(Options{
		Agent:      AgentOpenCode,
		BuiltinMCP: harness.BuiltinMCP{URL: "http://shared/mcp", Token: "shared-token"},
	})

	servers := h.mcpServers(harness.Turn{WorkDir: "/repo/a"}, false)
	if len(servers) != 1 {
		t.Fatalf("服务数量应为 1，实际: %v", servers)
	}

	entry := mcpEntryByName(t, servers, "mcode")
	if entry["command"] != "npx" {
		t.Errorf("退化条目应为 npx mcp-remote: %v", entry)
	}
	if _, has := entry["type"]; has {
		t.Errorf("stdio 条目不应带 type: %v", entry)
	}
	wantArgs := []any{
		"-y", mcpRemotePackage, "http://shared/mcp",
		"--header", "Authorization:${MCODE_MCP_HEADER_0}",
		"--header", "X-Mcode-Workdir:${MCODE_MCP_HEADER_1}",
	}
	if !reflect.DeepEqual(entry["args"], wantArgs) {
		t.Errorf("args = %v，期望 %v", entry["args"], wantArgs)
	}
	// 令牌只经 env 传给子进程，不能出现在 argv 里（ps 能看到）。
	for _, arg := range entry["args"].([]any) {
		if s, ok := arg.(string); ok && (strings.Contains(s, "shared-token") || strings.Contains(s, "/repo/a")) {
			t.Errorf("argv 不应出现 header 真实值: %v", arg)
		}
	}
	wantEnv := []any{
		map[string]any{"name": "MCODE_MCP_HEADER_0", "value": "Bearer shared-token"},
		map[string]any{"name": "MCODE_MCP_HEADER_1", "value": "/repo/a"},
	}
	if !reflect.DeepEqual(entry["env"], wantEnv) {
		t.Errorf("env = %v，期望 %v", entry["env"], wantEnv)
	}
}

func TestMCPServers_Empty(t *testing.T) {
	h := New(Options{Agent: AgentOpenCode})
	if servers := h.mcpServers(harness.Turn{WorkDir: "/repo/a"}, true); len(servers) != 0 {
		t.Fatalf("未配置 MCP 时应返回空列表，实际: %v", servers)
	}
}
