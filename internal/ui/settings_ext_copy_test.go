package mcodeui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
	"mcode/internal/testenv"
)

// TestMCPToolRowsNoRepeatedLabels 覆盖：展开有多个工具的 MCP 服务后，
// 「禁用」「免审批」只在表头各出现一次，不重复出现在每个工具行；每个工具的开关读屏标签仍带工具名。
func TestMCPToolRowsNoRepeatedLabels(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	uiSt := getMCPUIState(st)
	srv := agentconf.MCPServer{
		Agent:     "custom",
		Scope:     "global",
		Name:      "tools-mcp",
		Transport: "stdio",
		Command:   "echo",
	}
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	uiSt.expandedServers[key] = true
	uiSt.toolsCache[key] = []agentconf.MCPToolInfo{
		{Name: "tool-one", Description: "first"},
		{Name: "tool-two", Description: "second"},
		{Name: "tool-three", Description: "third"},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(900).Children(func() {
			renderMCPServerRow(c, st, uiSt, srv, 0, nil, nil, nil, nil, true)
			renderMCPServerToolsSection(c, st, uiSt, srv, 0)
		})
	}, 1000, 1000)

	disableCount := 0
	approveCount := 0
	for _, text := range tt.Texts() {
		switch text {
		case "禁用":
			disableCount++
		case "免审批":
			approveCount++
		}
	}
	if disableCount != 1 {
		t.Fatalf("「禁用」出现次数 = %d，期望 1；全部文本=%v", disableCount, tt.Texts())
	}
	if approveCount != 1 {
		t.Fatalf("「免审批」出现次数 = %d，期望 1；全部文本=%v", approveCount, tt.Texts())
	}
	for _, name := range []string{"tool-one", "tool-two", "tool-three"} {
		if _, ok := tt.Find("切换禁用 " + name); !ok {
			t.Fatalf("缺少带工具名的禁用开关读屏标签 %q", "切换禁用 "+name)
		}
		if _, ok := tt.Find("切换免审批 " + name); !ok {
			t.Fatalf("缺少带工具名的免审批开关读屏标签 %q", "切换免审批 "+name)
		}
	}
}

// TestMCPProbedNoToolsShowsEmpty 覆盖：未探测时显示「连接后可见」，
// 探测成功但工具列表为空时显示「无可用工具」而非「连接后可见」。
func TestMCPProbedNoToolsShowsEmpty(t *testing.T) {
	testenv.TempHome(t)
	useMCPToolLister(t, func(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
		return nil, nil
	})

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	uiSt := getMCPUIState(st)
	srv := agentconf.MCPServer{
		Agent:     "custom",
		Scope:     "global",
		Name:      "empty-mcp",
		Transport: "stdio",
		Command:   "echo",
	}
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	uiSt.expandedServers[key] = true

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(900).Children(func() {
			renderMCPServerRow(c, st, uiSt, srv, 0, nil, nil, nil, nil, true)
			renderMCPServerToolsSection(c, st, uiSt, srv, 0)
		})
	}, 1000, 800)

	if !tt.HasText("连接后可见") || tt.HasText("无可用工具") {
		t.Fatalf("未探测时应显示「连接后可见」，实际文本=%v", tt.Texts())
	}

	startMCPToolProbe(st, uiSt, srv)
	waitFor(t, tt, func() bool {
		return tt.HasText("无可用工具")
	})

	if tt.HasText("连接后可见") {
		t.Fatalf("已探测且工具为空时不应显示「连接后可见」，实际文本=%v", tt.Texts())
	}
}

// TestSkillSearchMatchesDescription 覆盖：技能搜索按全字段匹配，
// 仅出现在描述里的关键字也能命中对应技能，且支持清除搜索。
func TestSkillSearchMatchesDescription(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoaded = true
	st.skills = []agentconf.Skill{
		{Agent: "claude", Scope: "global", Name: "alpha-skill", Description: "处理数据库迁移", Enabled: true},
		{Agent: "claude", Scope: "global", Name: "beta-skill", Description: "生成单元测试", Enabled: true},
	}

	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 4000)
	if err := tt.Click("搜索技能"); err != nil {
		t.Fatalf("点击搜索框失败: %v", err)
	}
	tt.Type("数据库迁移")

	if !tt.HasText("alpha-skill") {
		t.Fatalf("按描述关键字搜索应命中 alpha-skill: %v", tt.Texts())
	}
	if tt.HasText("beta-skill") {
		t.Fatalf("不匹配描述关键字的 beta-skill 应被过滤: %v", tt.Texts())
	}

	if err := tt.Click("清除搜索"); err != nil {
		t.Fatalf("点击清除搜索失败: %v", err)
	}
	if !tt.HasText("alpha-skill") || !tt.HasText("beta-skill") {
		t.Fatalf("清除搜索后应恢复显示全部技能: %v", tt.Texts())
	}
}

// TestSkillBadgesDeduplicatedByGroup 覆盖：分组标题已表达的作用域和全局供应商徽标不再重复显示，
// 组内仍有区分意义的标签（如非全局组的 Agent、插件名）保留。
func TestSkillBadgesDeduplicatedByGroup(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoaded = true
	st.skills = []agentconf.Skill{
		{Agent: "claude", Scope: "global", Name: "g-skill", Enabled: true},
		{Agent: "codex", Scope: "project", Name: "p-skill", Enabled: true},
		{Agent: "cursor", Scope: "external", Plugin: "demo-plugin", Name: "e-skill", Enabled: true},
	}

	tt := ui.NewTester(func(c *ui.Context) { renderSkillsContent(c, st) }, 1000, 2000)

	for _, redundant := range []string{"全局", "项目", "外部", "Claude"} {
		if _, ok := tt.Find(redundant); ok {
			t.Fatalf("分组标题已表达的重复徽标不应渲染 %q；全部文本=%v", redundant, tt.Texts())
		}
	}
	for _, kept := range []string{"Codex", "Cursor", "插件 · demo-plugin"} {
		if _, ok := tt.Find(kept); !ok {
			t.Fatalf("组内有区分意义的标签应保留 %q；全部文本=%v", kept, tt.Texts())
		}
	}
}
