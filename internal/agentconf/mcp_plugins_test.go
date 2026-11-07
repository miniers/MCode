package agentconf

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 已启用插件的 MCP 服务要被扫描到，名字与 Claude Code 一致，且展开 ${CLAUDE_PLUGIN_ROOT}；未启用的插件不出现。
func TestScanMCPEnabledPluginServers(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "plugins", "cache", "cm", "context-mode", "1.0")
	off := filepath.Join(home, ".claude", "plugins", "cache", "off")
	writeTestFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{
  "version": 2,
  "plugins": {
    "context-mode@cm": [{"scope": "user", "installPath": "`+root+`"}],
    "off@cm": [{"scope": "user", "installPath": "`+off+`"}]
  }
}`)
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"enabledPlugins": {"context-mode@cm": true, "off@cm": false}}`)
	writeTestFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), `{
  "name": "context-mode",
  "mcpServers": {"context-mode": {"command": "node", "args": ["${CLAUDE_PLUGIN_ROOT}/start.mjs"]}}
}`)
	writeTestFile(t, filepath.Join(off, ".mcp.json"), `{"mcpServers": {"x": {"command": "node"}}}`)

	opts := Options{Home: home, DataDir: t.TempDir(), WithCredentials: true}
	servers := mustScanMCP(t, opts)

	srv := mcpServerByName(t, servers, "plugin_context-mode_context-mode")
	if srv.Agent != "claude" || srv.Scope != "global" || srv.Transport != "stdio" || srv.Command != "node" {
		t.Fatalf("插件服务字段不对：%+v", srv)
	}
	if len(srv.Args) != 1 || srv.Args[0] != filepath.Join(root, "start.mjs") {
		t.Fatalf("${CLAUDE_PLUGIN_ROOT} 未展开：%v", srv.Args)
	}
	if srv.Env["CLAUDE_PLUGIN_ROOT"] != root {
		t.Fatalf("缺少 CLAUDE_PLUGIN_ROOT 环境变量：%v", srv.Env)
	}
	for _, s := range servers {
		if s.Name == "plugin_off_x" {
			t.Fatal("未启用的插件不应被扫描")
		}
	}
}

// 没有 plugin.json 声明时回退到插件根目录的 .mcp.json，直接以服务名为键的写法也要支持。
func TestScanMCPPluginFallbackMCPJSON(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "plugins", "cache", "p")
	writeTestFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		`{"plugins": {"p@m": [{"installPath": "`+root+`"}]}, "enabledPlugins": {"p@m": true}}`)
	writeTestFile(t, filepath.Join(root, ".mcp.json"),
		`{"remote": {"type": "http", "url": "https://example.com/mcp"}}`)

	servers := mustScanMCP(t, Options{Home: home, DataDir: t.TempDir()})
	srv := mcpServerByName(t, servers, "plugin_p_remote")
	if srv.Transport != "http" || srv.URL != "https://example.com/mcp" {
		t.Fatalf("回退 .mcp.json 解析不对：%+v", srv)
	}
}
