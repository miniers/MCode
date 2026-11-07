package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeMCPJSONFixture 把 v 序列化后写入 path，自动创建父目录。
func writeMCPJSONFixture(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化测试配置失败：%v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("写入测试配置失败：%v", err)
	}
}

// 引擎侧停用名单（~/.claude.json 的 disabledMcpServers / disabledMcpjsonServers）
// 只打 DisabledByEngine 展示标记，不能顺带改掉 mcode 自己的 Disabled。
func TestScanMCPDisabledByEngine(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	opts := Options{Home: home, ProjectDir: proj, DataDir: t.TempDir()}

	writeMCPJSONFixture(t, filepath.Join(home, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{
			"ctx7":      map[string]any{"command": "npx", "args": []string{"ctx7"}},
			"global-on": map[string]any{"command": "node"},
		},
		"projects": map[string]any{
			proj: map[string]any{
				"mcpServers": map[string]any{
					"proj-off": map[string]any{"command": "node"},
					"proj-on":  map[string]any{"command": "node"},
				},
				"disabledMcpServers":     []string{"ctx7", "proj-off", "sentry-by-name"},
				"disabledMcpjsonServers": []string{"sentry"},
			},
		},
	})
	writeMCPJSONFixture(t, filepath.Join(proj, ".mcp.json"), map[string]any{
		"mcpServers": map[string]any{
			"sentry":         map[string]any{"command": "node"},
			"sentry-by-name": map[string]any{"command": "node"},
			"mcp-on":         map[string]any{"command": "node"},
		},
	})

	// .mcp.json 属于仓库内来源，默认未信任会把 Disabled 也置为 true；
	// 先按设置页启用的方式信任它们，才能单独断言引擎标记与 mcode 停用互不影响。
	for _, name := range []string{"sentry", "sentry-by-name", "mcp-on"} {
		if err := SetMCPServerEnabled(opts, name, true); err != nil {
			t.Fatalf("信任 %s 失败：%v", name, err)
		}
	}

	servers := mustScanMCP(t, opts)
	cases := []struct {
		agent, scope, name string
		engine, disabled   bool
	}{
		{agent: "claude", scope: "global", name: "ctx7", engine: true},
		{agent: "claude", scope: "global", name: "global-on"},
		{agent: "claude", scope: "project", name: "proj-off", engine: true},
		{agent: "claude", scope: "project", name: "proj-on"},
		{agent: "claude", scope: "project", name: "sentry", engine: true},
		{agent: "claude", scope: "project", name: "sentry-by-name", engine: true},
		{agent: "claude", scope: "project", name: "mcp-on"},
	}
	for _, tc := range cases {
		var srv MCPServer
		found := false
		for _, s := range servers {
			if s.Agent == tc.agent && s.Scope == tc.scope && s.Name == tc.name {
				srv = s
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("扫描结果里没有 %s/%s/%s", tc.agent, tc.scope, tc.name)
		}
		if srv.DisabledByEngine != tc.engine {
			t.Errorf("%s 的 DisabledByEngine 应为 %v，got %v", tc.name, tc.engine, srv.DisabledByEngine)
		}
		if srv.Disabled != tc.disabled {
			t.Errorf("%s 的 Disabled 应为 %v，got %v", tc.name, tc.disabled, srv.Disabled)
		}
	}
}

// Claude Code 记录的是真实路径，调用方把符号链接目录当 projectDir 时也要能匹配到项目条目。
func TestScanMCPDisabledByEngineSymlinkedProjectDir(t *testing.T) {
	home := t.TempDir()
	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "proj-link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("创建符号链接失败：%v", err)
	}
	realResolved, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("解析真实路径失败：%v", err)
	}

	// 键写成真实路径，Options.ProjectDir 使用链接路径，扫描时靠 EvalSymlinks 对上。
	writeMCPJSONFixture(t, filepath.Join(home, ".claude.json"), map[string]any{
		"projects": map[string]any{
			realResolved: map[string]any{
				"mcpServers": map[string]any{
					"link-off": map[string]any{"command": "node"},
					"link-on":  map[string]any{"command": "node"},
				},
				"disabledMcpServers": []string{"link-off"},
			},
		},
	})

	servers := mustScanMCP(t, Options{Home: home, ProjectDir: linkDir, DataDir: t.TempDir()})
	off := findServer(t, servers, "claude", "link-off")
	if !off.DisabledByEngine {
		t.Fatalf("符号链接目录下的停用服务未标记：%+v", off)
	}
	on := findServer(t, servers, "claude", "link-on")
	if on.DisabledByEngine {
		t.Fatalf("未停用的服务不应标记：%+v", on)
	}
}
