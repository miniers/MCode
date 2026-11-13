package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

func TestSettingsPageMCP_Tree(t *testing.T) {
	testenv.TempHome(t)
	original := scanMCP
	t.Cleanup(func() { scanMCP = original })
	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{
			{Agent: "claude", Scope: "global", Name: "tree-first-service", Transport: "stdio", Command: "node"},
			{Agent: "claude", Scope: "global", Name: "tree-second-service", Transport: "stdio", Command: "node"},
		}, nil
	}
	dir := settingsTestDir(t)
	st := NewSettingsState(dir, nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 2000)
	waitFor(t, tt, func() bool { return tt.HasText("tree-first-service") && tt.HasText("tree-second-service") })
	for _, title := range []string{"折叠大量工具", "MCP 服务", "claude"} {
		if !tt.HasText(title) {
			t.Errorf("缺少标题 %s", title)
		}
	}
	first, ok := tt.Find("tree-first-service")
	if !ok {
		t.Fatal("缺少第一个服务")
	}
	second, ok := tt.Find("tree-second-service")
	if !ok {
		t.Fatal("缺少第二个服务")
	}
	if second.Y <= first.Y || second.X != first.X {
		t.Errorf("服务未纵向排列：%v，%v", first, second)
	}
	if err := tt.Click("切换折叠大量工具"); err != nil {
		t.Fatal(err)
	}
	policy, err := agentconf.LoadMCPToolPolicy(agentconf.MCPToolPolicyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if policy.FoldThreshold != -1 {
		t.Fatalf("折叠策略未保存：%d", policy.FoldThreshold)
	}

	// 再点一次恢复折叠：策略回到 0。
	if err := tt.Click("切换折叠大量工具"); err != nil {
		t.Fatal(err)
	}
	policy, err = agentconf.LoadMCPToolPolicy(agentconf.MCPToolPolicyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if policy.FoldThreshold != 0 {
		t.Fatalf("恢复折叠后策略应为 0：%d", policy.FoldThreshold)
	}
}
