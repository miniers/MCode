package mcodeui

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

// useMCPToolLister 临时替换工具探测钩子，测试结束后恢复。
func useMCPToolLister(t *testing.T, lister agentconf.ToolListerFunc) {
	t.Helper()
	orig := agentconf.DefaultToolLister
	agentconf.DefaultToolLister = lister
	t.Cleanup(func() { agentconf.DefaultToolLister = orig })
}

// installMCPCatalogPreset 让扫描结果里出现预设 fetch，使对应卡片处于已添加状态。
func installMCPCatalogPreset(t *testing.T) {
	t.Helper()
	origScanMCP := scanMCP
	t.Cleanup(func() { scanMCP = origScanMCP })
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{{
			Agent:     "custom",
			Scope:     "global",
			Name:      "fetch",
			Transport: "stdio",
			Command:   "uvx",
		}}, nil
	}
}

// openMCPPage 从设置页进入包含 MCP 配置的扩展页。
func openMCPPage(t *testing.T, tt *ui.Tester) {
	t.Helper()
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("扩展") })
	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '扩展' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("+ 添加服务") })
}

func openMCPDetail(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	waitFor(t, tt, func() bool { return tt.HasText(name) })
	if err := tt.Click(name); err != nil {
		t.Fatalf("打开服务 %s 详情失败: %v", name, err)
	}
}

func openMCPAddSheet(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("+ 添加服务"); err != nil {
		t.Fatalf("打开添加服务面板失败: %v", err)
	}
}

// TestMCPCatalogCardShowsConnectedToolCount 覆盖：已安装且无需登录的预设探测成功后显示「已连接 · N 个工具」。
func TestMCPCatalogCardShowsConnectedToolCount(t *testing.T) {
	testenv.TempHome(t)
	installMCPCatalogPreset(t)
	useMCPToolLister(t, func(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return []agentconf.MCPToolInfo{
			{Name: "fetch", Description: "抓取网页"},
			{Name: "fetch_markdown", Description: "抓取并转成 Markdown"},
		}, nil
	})

	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(settingsTestDir(t), nil)), 1000, 6000)
	openMCPPage(t, tt)
	openMCPAddSheet(t, tt)

	waitFor(t, tt, func() bool {
		return tt.HasText("已连接 · 2 个工具")
	})
}

// TestMCPCatalogCardShowsProbeFailure 覆盖：探测失败时预设卡片显示「连接失败」与错误详情。
func TestMCPCatalogCardShowsProbeFailure(t *testing.T) {
	testenv.TempHome(t)
	installMCPCatalogPreset(t)
	useMCPToolLister(t, func(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return nil, errors.New("连接被拒绝")
	})

	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(settingsTestDir(t), nil)), 1000, 6000)
	openMCPPage(t, tt)
	openMCPAddSheet(t, tt)

	waitFor(t, tt, func() bool {
		return tt.HasText("连接失败") && tt.HasText("连接被拒绝")
	})
}

// TestMCPCatalogPlanMatchesFullCatalog 覆盖缓存计划：空搜索词覆盖全部预设，搜索能缩小范围，已保存服务判定为已安装。
func TestMCPCatalogPlanMatchesFullCatalog(t *testing.T) {
	all := agentconf.MCPCatalog()

	plan := buildMCPCatalogPlan("", nil, "")
	if got := countMCPCatalogPlanRows(plan); got != len(all) {
		t.Fatalf("空搜索词应包含全部预设：计划 %d 条，目录 %d 条", got, len(all))
	}

	filtered := buildMCPCatalogPlan("playwright", nil, "")
	if got := countMCPCatalogPlanRows(filtered); got == 0 || got >= len(all) {
		t.Fatalf("搜索应缩小结果范围：命中 %d 条，目录 %d 条", got, len(all))
	}

	installed := buildMCPCatalogPlan("", []agentconf.MCPServer{{
		Agent: "custom",
		Name:  "fetch",
		URL:   "https://example.com/mcp",
	}}, "")
	found := false
	for _, group := range installed.groups {
		for _, row := range group.rows {
			if row.view.entry.ID != "fetch" {
				continue
			}
			found = true
			if !row.installed || row.saved.Name != "fetch" {
				t.Fatalf("已保存的预设应判定为已安装: installed=%v name=%q", row.installed, row.saved.Name)
			}
		}
	}
	if !found {
		t.Fatal("计划中应包含 fetch 预设")
	}
}

// countMCPCatalogPlanRows 统计计划中的卡片总数。
func countMCPCatalogPlanRows(plan mcpCatalogPlan) int {
	total := 0
	for _, group := range plan.groups {
		total += len(group.rows)
	}
	return total
}

// TestMCPCatalogSearchFiltersCards 覆盖：预设目录的搜索框按关键词过滤卡片。
func TestMCPCatalogSearchFiltersCards(t *testing.T) {
	testenv.TempHome(t)
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(settingsTestDir(t), nil)), 1000, 4000)
	openMCPPage(t, tt)
	openMCPAddSheet(t, tt)
	waitFor(t, tt, func() bool { return tt.HasText("Playwright") && tt.HasText("Sequential Thinking") })

	if err := tt.Click("编辑搜索预设"); err != nil {
		t.Fatalf("click '编辑搜索预设' failed: %v", err)
	}
	tt.Type("playwright")
	waitFor(t, tt, func() bool { return tt.HasText("Playwright") && !tt.HasText("Sequential Thinking") })
}
