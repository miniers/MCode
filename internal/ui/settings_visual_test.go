package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
)

func TestSettingsVisual_RowSpacing(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	section := pageGeneral.Sections[1]
	section.Rows = section.Rows[:1]
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSection(c, st, section, false)
	}, 1000, 4000)
	card, ok := tt.Find("设置卡片 general.notifications")
	if !ok {
		t.Fatal("缺少通知卡片")
	}
	title, ok := tt.Find("完成后通知")
	if !ok {
		t.Fatal("缺少通知行")
	}
	if inset := title.X - card.X; inset < settingsRowPadX-1 || inset > settingsRowPadX+2 {
		t.Errorf("行标题左边距 = %v，期望 %v 到 %v", inset, settingsRowPadX-1, settingsRowPadX+2)
	}
	if maxHeight := settingsRowMinHeight + 2*settingsRowPadY + 2; card.H > maxHeight {
		t.Errorf("单行卡片高度 = %v，大于 %v", card.H, maxHeight)
	}
}

func TestSettingsVisual_SwarmNoDoubleInset(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.selectSettingsPage("swarm")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 4000)
	mainCard, ok := tt.Find("设置卡片 swarm.main")
	if !ok {
		t.Fatal("缺少编排卡片")
	}
	pairsCard, ok := tt.Find("设置卡片 swarm.pairs")
	if !ok {
		t.Fatal("缺少配对卡片")
	}
	enabled, ok := tt.Find("启用编排")
	if !ok {
		t.Fatal("缺少启用编排行")
	}
	add, ok := tt.Find("添加配对")
	if !ok {
		t.Fatal("缺少添加配对按钮")
	}
	// Find 命中按钮的 Label；按钮左边缘应与普通行文字左缘对齐。
	diff := (enabled.X - mainCard.X) - (add.X - pairsCard.X)
	if diff < -2 || diff > 2 {
		t.Errorf("按钮与行文字左边距之差 = %v，超过 2", diff)
	}
	count := 0
	for _, text := range tt.Texts() {
		if text == "编排" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("详情区编排标题出现 %d 次，期望 1", count)
	}
}

func TestSettingsVisual_RowHeightsUniform(t *testing.T) {
	t.Cleanup(func() { setTextScale(config.DefaultTextScale) })

	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "appearance")

	rects := make(map[string]ui.Rect)
	for _, title := range []string{"主题", "紧凑侧边栏", "代码字体", "代码字号"} {
		rect, ok := tt.Find(title)
		if !ok {
			t.Fatalf("外观页缺少 %q: %v", title, tt.Texts())
		}
		rects[title] = rect
	}
	for _, title := range []string{"紧凑侧边栏", "代码字体"} {
		if rects[title].X != rects["主题"].X {
			t.Errorf("%s 左缘 X = %v，主题左缘 X = %v", title, rects[title].X, rects["主题"].X)
		}
	}
	if gap := rects["代码字号"].Y - rects["代码字体"].Y; gap < settingsRowMinHeight {
		t.Errorf("代码字体与代码字号标题的 Y 间距 = %v，小于 %v", gap, settingsRowMinHeight)
	}
}

func TestSettingsVisual_MCPNoDoubleInset(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoaded = true
	st.mcpServers = []agentconf.MCPServer{{Agent: "custom", Name: "视觉测试 MCP 服务"}}
	st.selectSettingsPage("extensions")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 4000)
	card, ok := tt.Find("设置卡片 mcp.custom")
	if !ok {
		t.Fatal("缺少 MCP 服务卡片")
	}
	title, ok := tt.Find("视觉测试 MCP 服务")
	if !ok {
		t.Fatal("缺少 MCP 服务条目")
	}
	if inset := title.X - card.X; inset < 0 || inset > settingsRowPadX+2 {
		t.Errorf("MCP 服务标题左边距 = %v，期望 0 到 %v", inset, settingsRowPadX+2)
	}
}
