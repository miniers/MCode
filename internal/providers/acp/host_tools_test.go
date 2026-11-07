package acp

import (
	"errors"
	"testing"

	"mcode/internal/harness"
)

// TestHostToolsAvailableRequiresNpxForFallback 验证只有退化到 npx mcp-remote 且找不到 npx 时
// 才报告没有宿主工具；Agent 已声明支持 HTTP MCP 时直连，不再依赖 npx。
func TestHostToolsAvailableRequiresNpxForFallback(t *testing.T) {
	orig := lookupNpx
	t.Cleanup(func() { lookupNpx = orig })

	builtin := harness.BuiltinMCP{URL: "http://127.0.0.1:9000/mcp"}

	lookupNpx = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	if New(Options{Agent: AgentOpenCode, BuiltinMCP: builtin}).HostToolsAvailable() {
		t.Fatal("找不到 npx 时应报告没有宿主工具")
	}

	lookupNpx = func(string) (string, error) { return "/usr/local/bin/npx", nil }
	if !New(Options{Agent: AgentOpenCode, BuiltinMCP: builtin}).HostToolsAvailable() {
		t.Fatal("npx 可用时应报告有宿主工具")
	}

	// initialize 应答确认支持 HTTP MCP 后，会话不再走 npx，即使 PATH 里没有 npx 也有工具。
	httpHarness := New(Options{Agent: AgentOpenCode, BuiltinMCP: builtin})
	httpHarness.mcpHTTP.Store(true)
	lookupNpx = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	if !httpHarness.HostToolsAvailable() {
		t.Fatal("Agent 支持 HTTP MCP 时不应依赖 npx")
	}

	// 没有配置内置 MCP 时根本没有可注入的工具。
	if New(Options{Agent: AgentOpenCode}).HostToolsAvailable() {
		t.Fatal("未配置内置 MCP 时应报告没有宿主工具")
	}
}
