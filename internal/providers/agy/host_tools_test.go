package agy

import (
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/testenv"
)

// TestHostToolsAvailableFollowsGeminiDir 验证宿主工具可用性与 MCP 注入的跳过条件一致：
// 源 gemini 目录不存在或为空时没有注入，也就没有回执工具；目录非空时才有。
func TestHostToolsAvailableFollowsGeminiDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	builtin := harness.BuiltinMCP{URL: "http://127.0.0.1:9000/mcp"}
	h := New(Options{BuiltinMCP: builtin})

	if h.HostToolsAvailable() {
		t.Fatal("源 gemini 目录不存在时应报告没有宿主工具")
	}

	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir gemini dir: %v", err)
	}
	if h.HostToolsAvailable() {
		t.Fatal("源 gemini 目录为空时应报告没有宿主工具")
	}

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if !h.HostToolsAvailable() {
		t.Fatal("源 gemini 目录非空时应报告有宿主工具")
	}

	// 没有配置内置 MCP 时根本没有可注入的工具。
	if New(Options{}).HostToolsAvailable() {
		t.Fatal("未配置内置 MCP 时应报告没有宿主工具")
	}
}
