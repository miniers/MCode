package mcodeui

import (
	"context"
	"testing"
	"time"

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
// 主行只留名称与插件名等标签；点开行菜单 ··· 后，组内有区分意义的 Agent 标签才出现在菜单里，
// 全局组无区分意义的 Agent 标签仍不出现。
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

	// 主行不显示重复的作用域与供应商徽标，也不直接露出 Agent 名。
	for _, hidden := range []string{"全局", "项目", "外部", "Claude", "Codex", "Cursor"} {
		if _, ok := tt.Find(hidden); ok {
			t.Fatalf("主行不应直接渲染标签 %q；全部文本=%v", hidden, tt.Texts())
		}
	}
	if _, ok := tt.Find("插件 · demo-plugin"); !ok {
		t.Fatalf("主行应保留插件标签 %q；全部文本=%v", "插件 · demo-plugin", tt.Texts())
	}

	// 全局组菜单：分组标题已含供应商，菜单里不重复显示 "Claude"。
	if err := tt.Click("更多操作「g-skill」"); err != nil {
		t.Fatalf("点击全局技能菜单失败: %v", err)
	}
	tt.Advance(motionPop + 60*time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("Claude"); ok {
		t.Fatalf("全局组行菜单不应重复显示 %q；全部文本=%v", "Claude", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Advance(motionPop + 60*time.Millisecond)
	tt.Frame()

	// 项目组菜单：组内有区分意义的 "Codex" 应出现在菜单里。
	if err := tt.Click("更多操作「p-skill」"); err != nil {
		t.Fatalf("点击项目技能菜单失败: %v", err)
	}
	tt.Advance(motionPop + 60*time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("Codex"); !ok {
		t.Fatalf("项目组行菜单应显示 %q；全部文本=%v", "Codex", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Advance(motionPop + 60*time.Millisecond)
	tt.Frame()

	// 外部组菜单：组内有区分意义的 "Cursor" 应出现在菜单里，上一行菜单已关闭。
	if err := tt.Click("更多操作「e-skill」"); err != nil {
		t.Fatalf("点击外部技能菜单失败: %v", err)
	}
	tt.Advance(motionPop + 60*time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("Cursor"); !ok {
		t.Fatalf("外部组行菜单应显示 %q；全部文本=%v", "Cursor", tt.Texts())
	}
	if _, ok := tt.Find("Codex"); ok {
		t.Fatalf("切换行菜单后上一行的 %q 不应继续显示；全部文本=%v", "Codex", tt.Texts())
	}
}
