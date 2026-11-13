package mcodeui

import (
	"context"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

func TestMCP_UnifiedListAndDetail(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	dataDir := settingsTestDir(t)
	servers := []agentconf.MCPServer{
		{Agent: "custom", Scope: "global", Name: "shared-service", Transport: "http", URL: "https://example.com/mcp"},
		{Agent: "pi", Scope: "global", Name: "shared-service", Transport: "stdio", Command: "node", ReadOnly: true},
	}
	stubMCPAuthScans(t, func(agentconf.Options) ([]agentconf.MCPServer, error) { return servers, nil }, func(agentconf.Options) map[string]bool { return nil })
	useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return []agentconf.MCPToolInfo{{Name: "shared-tool"}}, nil
	})
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1200, 6000)
	openMCPPage(t, tt)
	waitFor(t, tt, func() bool {
		_, custom := tt.Find("打开 MCP 服务 custom:global:shared-service")
		_, pi := tt.Find("打开 MCP 服务 pi:global:shared-service")
		return custom && pi
	})
	if err := tt.Click("打开 MCP 服务 pi:global:shared-service"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("只读 · 外部配置") && tt.HasText("shared-tool") })
	for _, label := range []string{"切换启用 shared-service", "切换shared-service", "登录", "删除", "编辑", "全部禁用"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("只读服务不应显示 %q", label)
		}
	}
	if err := tt.Click("关闭MCP 服务"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("打开 MCP 服务 custom:global:shared-service"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("登录") && tt.HasText("shared-tool") && tt.HasText("新会话默认启用")
	})
	if err := tt.Click("切换禁用 shared-tool"); err != nil {
		t.Fatal(err)
	}
	policy, err := agentconf.LoadMCPToolPolicy(agentconf.MCPToolPolicyPath(dataDir))
	if err != nil || !policy.Disabled["shared-service"]["shared-tool"] {
		t.Fatalf("工具开关未保存: %v, %v", policy, err)
	}
	if err := tt.Click("切换shared-service"); err != nil {
		t.Fatal(err)
	}
	opts := agentconf.Options{Home: home, DataDir: dataDir}
	if got := agentconf.NewThreadDisabledServers(opts); !slices.Contains(got, "custom:global:shared-service") {
		t.Fatalf("新会话开关未保存: %v", got)
	}
	if err := tt.Click("登录"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已登录") })
}

func TestMCP_AddSheet(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	dataDir := settingsTestDir(t)
	useMCPToolLister(t, func(context.Context, agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) { return nil, nil })
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1200, 6000)
	openMCPPage(t, tt)
	openMCPAddSheet(t, tt)
	waitFor(t, tt, func() bool { return tt.HasText("手动填写") && tt.HasText("编辑搜索预设") })
	if err := tt.Click("编辑搜索预设"); err != nil {
		t.Fatal(err)
	}
	tt.Type("fetch")
	waitFor(t, tt, func() bool { return tt.HasText("Fetch") })
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	opts := agentconf.Options{Home: home, DataDir: dataDir}
	waitFor(t, tt, func() bool {
		servers, err := agentconf.ScanMCP(opts)
		if err != nil {
			return false
		}
		for _, srv := range servers {
			if srv.Name == "fetch" {
				return true
			}
		}
		return false
	})
	if err := tt.Click("手动填写"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("编辑服务名称") })
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("服务名称不能为空") })
	if err := tt.Click("编辑服务名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("manual-service")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("执行命令不能为空") })
	if err := tt.Click("编辑执行命令"); err != nil {
		t.Fatal(err)
	}
	tt.Type("echo")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("manual-service") })
	servers, err := agentconf.ScanMCP(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range servers {
		if srv.Name == "manual-service" && srv.Command == "echo" {
			return
		}
	}
	t.Fatal("手动服务未保存")
}
