package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestSettingsRowInset_AllBlocksAndSheets 从声明枚举覆盖范围，新增项目必须提供 fixture。
func TestSettingsRowInset_AllBlocksAndSheets(t *testing.T) {
	type fixture struct {
		card, content string
		render        func(*ui.Context, *SettingsState)
	}
	fixtures := map[string]fixture{
		"block-swarm-pairs": {"设置卡片 swarm.pairs", "添加配对", func(c *ui.Context, st *SettingsState) {
			renderSettingsSection(c, "swarm.pairs", "模型配对", func() { renderSwarmPairsBody(c, st) })
		}},
		"block-mcp-custom": {"设置卡片 mcp.custom", "正在扫描 MCP 服务…", func(c *ui.Context, st *SettingsState) {
			st.agentConfLoading = true
			renderSettingsSection(c, "mcp.custom", "MCP 服务", func() { renderMCPCustomBody(c, st) })
		}},
		"sheet:quick-replies": {"设置卡片 sec-quick-replies", "还没有快捷回复。", func(c *ui.Context, st *SettingsState) {
			renderSettingsTreeSheet(c, st, sheetQuickReplies)
		}},
	}
	// shortcut: 以下面板和块尚无稳定的内容矩形 fixture，补齐后删除对应条目。
	knownMissing := map[string]bool{
		"block-skills":          true,
		"block-swarm-tiers":     true,
		"block-swarm-review":    true,
		"block-steward-score":   true,
		"sheet:archive":         true,
		"sheet:magpie-gateway":  true,
		"sheet:magpie-personas": true,
		"sheet:mcp-add":         true,
		"sheet:mcp-detail":      true,
		"sheet:open-in":         true,
		"sheet:provider":        true,
		"sheet:provider-add":    true,
		"sheet:shortcuts":       true,
		"sheet:worktree":        true,
	}
	seen := map[string]bool{}
	check := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		f, ok := fixtures[id]
		if !ok {
			if !knownMissing[id] {
				t.Errorf("%s 缺少间距 fixture", id)
			}
			return
		}
		t.Run(id, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, nil)
			if strings.HasPrefix(id, "sheet:") {
				openSettingsSheet(st, strings.TrimPrefix(id, "sheet:"), "")
				t.Cleanup(func() { closeSettingsSheet(st) })
			}
			tt := ui.NewTester(func(c *ui.Context) { f.render(c, st) }, 1000, 4000)
			tt.Frame()
			card, ok := tt.Find(f.card)
			if !ok {
				t.Fatalf("%s 缺少卡片 %q: %v", id, f.card, tt.Texts())
			}
			content, ok := tt.Find(f.content)
			if !ok {
				t.Fatalf("%s 缺少内容 %q: %v", id, f.content, tt.Texts())
			}
			assertSettingsRowInset(t, id, content, card)
		})
	}
	sections := func(sections []settingsSection) {
		for _, section := range sections {
			for _, row := range section.Rows {
				if strings.HasPrefix(row.ID, "block-") {
					check(row.ID)
				}
			}
		}
	}
	for _, page := range settingsTreePages() {
		sections(page.Sections)
	}
	for _, sheet := range settingsTreeSheets() {
		check("sheet:" + sheet.ID)
		sections(sheet.Sections)
	}
}

// assertSettingsRowInset 断言元素左边缘相对卡片左边缘的内缩等于设置行水平内边距，
// 允许 1px 的取整误差。
func assertSettingsRowInset(t *testing.T, name string, rect, card ui.Rect) {
	t.Helper()
	inset := rect.X - card.X
	if inset < settingsRowPadX-1 || inset > settingsRowPadX+1 {
		t.Errorf("%s 左边缘相对卡片内缩 = %v，期望 %v", name, inset, settingsRowPadX)
	}
}

// assertProviderRowBox 断言供应商行盒子贴着卡片内容区，证明外层没有再多包一层内边距。
func assertProviderRowBox(t *testing.T, name string, row, card ui.Rect) {
	t.Helper()
	if row.X-card.X > 1 {
		t.Errorf("%s 行盒子相对卡片内缩 = %v，外层多了一层内边距", name, row.X-card.X)
	}
}

// TestSettingsRowInset_SwarmPairsHeader 覆盖模型配对卡片顶部的按钮与普通行对齐。
func TestSettingsRowInset_SwarmPairsHeader(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.selectSettingsPage("swarm")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 4000)
	tt.Frame()

	card, ok := tt.Find("设置卡片 swarm.pairs")
	if !ok {
		t.Fatalf("缺少模型配对卡片：%v", tt.Texts())
	}
	add, ok := tt.Find("添加配对")
	if !ok {
		t.Fatalf("缺少添加配对按钮：%v", tt.Texts())
	}
	assertSettingsRowInset(t, "添加配对", add, card)
}

// TestSettingsRowInset_ModelsCards 覆盖收藏模型与供应商卡片里的自定义行。
func TestSettingsRowInset_ModelsCards(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.providerRows.loaded = true
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreePage(c, st, pageModels)
	}, 1000, 4000)
	tt.Frame()
	waitFor(t, tt, func() bool { return tt.HasText("尚未选择模型") })

	pinnedCard, ok := tt.Find("设置卡片 sec-models-pinned")
	if !ok {
		t.Fatalf("缺少收藏模型卡片：%v", tt.Texts())
	}
	empty, ok := tt.Find("尚未选择模型")
	if !ok {
		t.Fatalf("缺少空状态行：%v", tt.Texts())
	}
	assertSettingsRowInset(t, "尚未选择模型", empty, pinnedCard)
	add, ok := tt.Find("添加收藏")
	if !ok {
		t.Fatalf("缺少添加收藏按钮：%v", tt.Texts())
	}
	assertSettingsRowInset(t, "添加收藏", add, pinnedCard)

	providerName := providerDisplayName("claude")
	waitFor(t, tt, func() bool { return tt.HasText(providerName) })
	providersCard, ok := tt.Find("设置卡片 sec-providers")
	if !ok {
		t.Fatalf("缺少供应商卡片：%v", tt.Texts())
	}
	providerRow, ok := tt.Find(providerName)
	if !ok {
		t.Fatalf("缺少 %s 供应商行：%v", providerName, tt.Texts())
	}
	// 供应商行的 Label 就是供应商名，Find 拿到的是整行盒子；行自身的内边距
	// 才是 settingsRowPadX，所以这里断言行盒子从卡片内容区开始（只剩 1px 边框）。
	assertProviderRowBox(t, providerName, providerRow, providersCard)
	if providerRow.H < settingsRowMinHeight {
		t.Errorf("%s 行高 = %v，至少期望 %v", providerName, providerRow.H, settingsRowMinHeight)
	}
	addProvider, ok := tt.Find("添加供应商")
	if !ok {
		t.Fatalf("缺少添加供应商行：%v", tt.Texts())
	}
	assertProviderRowBox(t, "添加供应商", addProvider, providersCard)
	if addProvider.H < settingsRowMinHeight {
		t.Errorf("添加供应商行高 = %v，至少期望 %v", addProvider.H, settingsRowMinHeight)
	}
}

// TestSettingsRowInset_MCPCustomLoading 覆盖自定义服务扫描状态的水平内边距。
func TestSettingsRowInset_MCPCustomLoading(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoading = true
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsSection(c, "mcp.custom", "MCP 服务", func() {
			renderMCPCustomBody(c, st)
		})
	}, 1000, 4000)
	tt.Frame()

	card, ok := tt.Find("设置卡片 mcp.custom")
	if !ok {
		t.Fatalf("缺少 MCP 服务卡片：%v", tt.Texts())
	}
	loading, ok := tt.Find("正在扫描 MCP 服务…")
	if !ok {
		t.Fatalf("缺少 MCP 扫描状态：%v", tt.Texts())
	}
	assertSettingsRowInset(t, "MCP 扫描状态", loading, card)
}

// TestSettingsRowInset_AboutFooter 覆盖关于页的常驻声明行。
func TestSettingsRowInset_AboutFooter(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.selectSettingsPage("about")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 4000)
	tt.Frame()

	card, ok := tt.Find("设置卡片 about.app")
	if !ok {
		t.Fatalf("缺少关于卡片：%v", tt.Texts())
	}
	version, ok := tt.Find("版本号")
	if !ok {
		t.Fatalf("缺少版本号行：%v", tt.Texts())
	}
	assertSettingsRowInset(t, "版本号", version, card)
}
