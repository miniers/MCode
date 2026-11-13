package codex

import (
	"testing"

	"mcode/internal/harness"
)

func TestBuildThreadParams_NativeMCPIsDisabledBeforeProxy(t *testing.T) {
	params, _ := buildThreadParams(harness.Turn{WorkDir: "/repo"}, "read-only", "never", "", harness.BuiltinMCP{URL: "http://builtin", Token: "builtin-token"},
		[]harness.MCPEndpoint{{Name: "native-stdio", CodexName: "mcode_proxy_fixture", URL: "http://proxy", Token: "fixture-token"}}, nil)
	servers := params["config"].(map[string]any)["mcp_servers"].(map[string]any)
	native := servers["native-stdio"].(map[string]any)
	proxy := servers["mcode_proxy_fixture"].(map[string]any)
	if len(native) != 1 || native["enabled"] != false {
		t.Fatal("native stdio config must be disabled without adding HTTP fields")
	}
	if proxy["url"] != "http://proxy" || proxy["http_headers"].(map[string]any)["Authorization"] != "Bearer fixture-token" {
		t.Fatal("isolated proxy config missing")
	}
	if servers["mcode"].(map[string]any)["url"] != "http://builtin" {
		t.Fatal("builtin MCP name must remain mcode")
	}
}

// 仓库内未信任的服务必须在线程配置里强制关闭，避免 app-server 自己从工作目录配置拉起命令。
func TestBuildThreadParams_BlockedMCP(t *testing.T) {
	turn := harness.Turn{WorkDir: "/repo"}

	t.Run("blocked 服务写入 enabled=false", func(t *testing.T) {
		params, _ := buildThreadParams(turn, "read-only", "never", "", harness.BuiltinMCP{}, nil, []string{"evil"})
		cfg, ok := params["config"].(map[string]any)
		if !ok {
			t.Fatal("expected config when blocked servers exist")
		}
		servers := cfg["mcp_servers"].(map[string]any)
		evil, ok := servers["evil"].(map[string]any)
		if !ok || len(evil) != 1 || evil["enabled"] != false {
			t.Fatalf("blocked server must be disabled only: %v", servers["evil"])
		}
	})

	t.Run("blocked 与外部端点同名时保留外部配置", func(t *testing.T) {
		external := []harness.MCPEndpoint{{Name: "evil", URL: "http://proxy/evil", Token: "tok"}}
		params, _ := buildThreadParams(turn, "read-only", "never", "", harness.BuiltinMCP{}, external, []string{"evil", "mcode"})
		servers := params["config"].(map[string]any)["mcp_servers"].(map[string]any)
		evil, ok := servers["evil"].(map[string]any)
		if !ok || evil["url"] != "http://proxy/evil" {
			t.Fatalf("external config must survive blocking: %v", servers["evil"])
		}
		if _, ok := servers["mcode"]; ok {
			t.Fatal("保留名 mcode 不能被当成 blocked 服务写入")
		}
	})

	t.Run("没有内置与外部端点且 blocked 为空时不生成 config", func(t *testing.T) {
		params, _ := buildThreadParams(turn, "read-only", "never", "", harness.BuiltinMCP{}, nil, nil)
		if _, ok := params["config"]; ok && !isWindowsHost {
			t.Fatalf("expected no config, got %v", params["config"])
		}
	})
}
