package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/testenv"
)

func TestSettingsExtensions_SectionsAndPageOwnership(t *testing.T) {
	want := []struct{ id, title string }{
		{"skills.list", "技能"},
		{"mcp.custom", "MCP 服务"},
		{"nowledge.memory", "记忆"},
		{"nowledge.spaces", "项目记忆空间"},
		{"advanced.memory", "高级设置"},
	}
	page, ok := settingsTreePageOf("extensions")
	if !ok || len(page.Sections) != len(want) {
		t.Fatalf("扩展页或节数量不正确: %+v", page.Sections)
	}
	for i, expected := range want {
		if section := page.Sections[i]; section.ID != expected.id || section.Title != expected.title {
			t.Errorf("第 %d 节 = %s / %s，期望 %s / %s", i, section.ID, section.Title, expected.id, expected.title)
		}
	}
	for _, id := range []string{"skills", "mcp", "nowledge"} {
		if _, ok := settingsTreePageOf(id); ok {
			t.Errorf("旧页面 %s 仍然注册", id)
		}
	}
	seen := make(map[string]string)
	for _, page := range settingsTreePages() {
		for _, section := range page.Sections {
			for _, row := range section.Rows {
				if previous, ok := seen[row.ID]; ok {
					t.Errorf("行 %s 同时属于 %s 和 %s", row.ID, previous, page.ID)
				}
				seen[row.ID] = page.ID
			}
		}
	}
}

func TestExtensionsTab_DefaultOnlySkills(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoaded = true
	st.selectSettingsPage("extensions")

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 2000)
	tt.Frame()

	for _, label := range []string{"技能", "MCP 服务", "记忆"} {
		rect, ok := tt.Find(label)
		if !ok {
			t.Fatalf("缺少分段按钮 %q: %v", label, tt.Texts())
		}
		if rect.H != ControlSM {
			t.Errorf("分段 %q 高度 = %.1f，期望 %.1f", label, rect.H, ControlSM)
		}
	}
	skillsRect, _ := tt.Find("技能")
	mcpRect, _ := tt.Find("MCP 服务")
	memRect, _ := tt.Find("记忆")
	if diff := skillsRect.W - mcpRect.W; diff < -1.5 || diff > 1.5 {
		t.Errorf("分段未等分宽度：技能=%.1f MCP 服务=%.1f", skillsRect.W, mcpRect.W)
	}
	if diff := mcpRect.W - memRect.W; diff < -1.5 || diff > 1.5 {
		t.Errorf("分段未等分宽度：MCP 服务=%.1f 记忆=%.1f", mcpRect.W, memRect.W)
	}

	if _, ok := tt.Find("设置卡片 skills.list"); !ok {
		t.Fatalf("默认应渲染技能段卡片: %v", tt.Texts())
	}
	for _, absent := range []string{"设置卡片 mcp.custom", "设置卡片 nowledge.memory", "设置卡片 advanced.memory"} {
		if _, ok := tt.Find(absent); ok {
			t.Errorf("默认技能段不应渲染 %q", absent)
		}
	}
	for _, text := range []string{"折叠大量工具", "记忆后端", "Working Memory 字数上限"} {
		if tt.HasText(text) {
			t.Errorf("默认技能段不应渲染 %q", text)
		}
	}
}

func TestExtensionsTab_SwitchTabsAndMotionSettles(t *testing.T) {
	testenv.TempHome(t)

	t.Run("切换MCP与记忆仅渲染对应段且动效收敛", func(t *testing.T) {
		setAppReduceMotion(t, false)
		setSettingsSectionExpanded(advancedMemory.ID, false)
		t.Cleanup(func() { setSettingsSectionExpanded(advancedMemory.ID, false) })
		chat := newChatState(NewFakeClient())
		t.Cleanup(chat.close)
		st := NewSettingsState(settingsTestDir(t), nil, chat)
		st.agentConfLoaded = true
		st.selectSettingsPage("extensions")

		tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 2400)
		tt.Frame()

		if err := tt.Click("MCP 服务"); err != nil {
			t.Fatalf("点击 MCP 服务失败: %v", err)
		}
		tt.Advance(DurSnap + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("切换到 MCP 服务后动画未停止")
		}
		if _, ok := tt.Find("设置卡片 mcp.custom"); !ok || !tt.HasText("折叠大量工具") {
			t.Fatalf("切换到 MCP 服务后未渲染 MCP section: %v", tt.Texts())
		}
		for _, absent := range []string{"设置卡片 skills.list", "设置卡片 nowledge.memory", "设置卡片 advanced.memory"} {
			if _, ok := tt.Find(absent); ok {
				t.Errorf("MCP 段不应渲染 %q", absent)
			}
		}

		if err := tt.Click("记忆"); err != nil {
			t.Fatalf("点击 记忆失败: %v", err)
		}
		tt.Advance(DurSnap + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("切换到记忆后动画未停止")
		}
		if _, ok := tt.Find("设置卡片 nowledge.memory"); !ok || !tt.HasText("记忆后端") {
			t.Fatalf("切换到记忆后未渲染 nowledgeSections: %v", tt.Texts())
		}
		if _, ok := tt.Find("设置卡片 advanced.memory"); !ok || !tt.HasText("高级设置") {
			t.Fatalf("切换到记忆后未渲染 advancedMemory: %v", tt.Texts())
		}
		if err := tt.Click("高级设置"); err != nil {
			t.Fatalf("展开高级设置失败: %v", err)
		}
		tt.Advance(DurBase + time.Second)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("展开高级设置后动画未停止")
		}
		if !tt.HasText("Working Memory 字数上限") {
			t.Fatalf("展开高级设置后未渲染 Working Memory 字数上限: %v", tt.Texts())
		}
		for _, absent := range []string{"设置卡片 skills.list", "设置卡片 mcp.custom"} {
			if _, ok := tt.Find(absent); ok {
				t.Errorf("记忆段不应渲染 %q", absent)
			}
		}

		if err := tt.Click("技能"); err != nil {
			t.Fatalf("点击 技能失败: %v", err)
		}
		tt.Advance(DurSnap + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("切回技能后动画未停止")
		}
		if _, ok := tt.Find("设置卡片 skills.list"); !ok {
			t.Fatalf("切回技能后未渲染技能卡片: %v", tt.Texts())
		}
		for _, absent := range []string{"设置卡片 mcp.custom", "设置卡片 nowledge.memory", "设置卡片 advanced.memory"} {
			if _, ok := tt.Find(absent); ok {
				t.Errorf("技能段不应渲染 %q", absent)
			}
		}
	})

	t.Run("ReduceMotion开启时切段立即收敛", func(t *testing.T) {
		setAppReduceMotion(t, true)
		st := NewSettingsState(settingsTestDir(t), nil, nil)
		st.agentConfLoaded = true
		st.selectSettingsPage("extensions")

		tt := ui.NewTesterWith(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 2000, ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		tt.Frame()

		if err := tt.Click("MCP 服务"); err != nil {
			t.Fatalf("点击 MCP 服务失败: %v", err)
		}
		if tt.FramePending() {
			t.Fatal("ReduceMotion 开启时切换到 MCP 服务不应请求后续帧")
		}
		if _, ok := tt.Find("设置卡片 mcp.custom"); !ok {
			t.Fatalf("切换到 MCP 服务后未渲染 MCP 卡片: %v", tt.Texts())
		}

		if err := tt.Click("记忆"); err != nil {
			t.Fatalf("点击记忆失败: %v", err)
		}
		if tt.FramePending() {
			t.Fatal("ReduceMotion 开启时切换到记忆不应请求后续帧")
		}
		if _, ok := tt.Find("设置卡片 nowledge.memory"); !ok {
			t.Fatalf("切换到记忆后未渲染记忆卡片: %v", tt.Texts())
		}
	})
}

func TestExtensionsTab_SearchJumpAutoSwitches(t *testing.T) {
	testenv.TempHome(t)
	chat := newChatState(NewFakeClient())
	t.Cleanup(chat.close)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.open = true
	st.agentConfLoaded = true

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 2400)

	findHit := func(query, wantID string) settingsItemHit {
		for _, hit := range matchVisibleSettingsItemsIn(st, query, settingsItems()) {
			if hit.Item.ID == wantID {
				return hit
			}
		}
		t.Fatalf("搜索 %q 未命中 %s", query, wantID)
		return settingsItemHit{}
	}

	mcpHit := findHit("折叠大量工具", "row-mcp-fold-tools")
	st.focusSettingsItem(mcpHit)
	tt.Frame()
	if st.currentPage != "extensions" || st.extensionsTab != extensionsTabMCP {
		t.Fatalf("命中 MCP 行后 page=%q tab=%v", st.currentPage, st.extensionsTab)
	}
	if !tt.HasText("折叠大量工具") {
		t.Fatalf("命中 MCP 行后未渲染目标行: %v", tt.Texts())
	}
	if _, ok := tt.Find("设置卡片 skills.list"); ok {
		t.Fatal("命中 MCP 行后不应渲染技能卡片")
	}

	memHit := findHit("同步编排子代理会话", "row-nowledge-sync-helper-threads")
	st.focusSettingsItem(memHit)
	tt.Frame()
	if st.currentPage != "extensions" || st.extensionsTab != extensionsTabMemory {
		t.Fatalf("命中记忆行后 page=%q tab=%v", st.currentPage, st.extensionsTab)
	}
	if !tt.HasText("同步编排子代理会话") {
		t.Fatalf("命中记忆行后未渲染目标行: %v", tt.Texts())
	}
	if _, ok := tt.Find("设置卡片 mcp.custom"); ok {
		t.Fatal("命中记忆行后不应渲染 MCP 卡片")
	}

	// 直接写入 jumpToItemID / highlightItemID 时，渲染前也应切到对应段。
	st.mu.Lock()
	st.extensionsTab = extensionsTabSkills
	st.jumpToItemID = "row-mcp-fold-tools"
	st.highlightItemID = ""
	st.mu.Unlock()
	tt.Frame()
	if st.extensionsTab != extensionsTabMCP || !tt.HasText("折叠大量工具") {
		t.Fatalf("jumpToItemID 未在渲染前切到 MCP 段: tab=%v", st.extensionsTab)
	}

	st.mu.Lock()
	st.extensionsTab = extensionsTabSkills
	st.jumpToItemID = ""
	st.highlightItemID = "row-nowledge-working-memory-budget"
	st.mu.Unlock()
	tt.Frame()
	if st.extensionsTab != extensionsTabMemory || !tt.HasText("Working Memory 字数上限") {
		t.Fatalf("highlightItemID 未在渲染前切到记忆段: tab=%v", st.extensionsTab)
	}
}

func TestExtensionsTab_LegacyDeepLinks(t *testing.T) {
	testenv.TempHome(t)
	for _, tc := range []struct {
		legacy   string
		wantTab  extensionsTab
		wantCard string
		absent   string
	}{
		{"mcp", extensionsTabMCP, "设置卡片 mcp.custom", "设置卡片 skills.list"},
		{"nowledge", extensionsTabMemory, "设置卡片 nowledge.memory", "设置卡片 mcp.custom"},
		{"skills", extensionsTabSkills, "设置卡片 skills.list", "设置卡片 nowledge.memory"},
	} {
		t.Run(tc.legacy, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, nil)
			st.agentConfLoaded = true
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 2400)
			st.OpenPage(tc.legacy)
			tt.Frame()
			if st.currentPage != "extensions" || st.extensionsTab != tc.wantTab {
				t.Fatalf("OpenPage(%q) page=%q tab=%v，期望 extensions / %v", tc.legacy, st.currentPage, st.extensionsTab, tc.wantTab)
			}
			if _, ok := tt.Find(tc.wantCard); !ok {
				t.Fatalf("OpenPage(%q) 缺少卡片 %q", tc.legacy, tc.wantCard)
			}
			if _, ok := tt.Find(tc.absent); ok {
				t.Errorf("OpenPage(%q) 不应渲染 %q", tc.legacy, tc.absent)
			}
		})
	}
}
