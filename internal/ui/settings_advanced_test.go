package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// newSettingsAdvancedTester 打开设置面板，返回记下的 SettingsState。
func newSettingsAdvancedTester(t *testing.T) (*ui.Tester, *SettingsState) {
	t.Helper()
	var st *SettingsState
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(settingsTestDir(t), nil), func(s *chatState) {
		st = getChatSettings(s)
	}), 1400, 4200)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("打开设置面板失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st != nil })
	return tt, st
}

// openSettingsAdvancedPage 切到指定页（与导航点击走同一条路径）。
func openSettingsAdvancedPage(t *testing.T, tt *ui.Tester, st *SettingsState, page string) {
	t.Helper()
	st.selectSettingsPage(page)
	tt.Frame()
}

// TestSettingsAdvanced_TreePagesHaveNoCollapsedArea 覆盖：已迁入页面树的页面没有页底高级折叠区。
func TestSettingsAdvanced_TreePagesHaveNoCollapsedArea(t *testing.T) {
	for _, id := range []string{"general", "appearance", "conversation", "swarm", "extensions", "workbench", "sourceControl", "nowledge", "remote"} {
		page, ok := settingsTreePageOf(id)
		if !ok {
			t.Fatalf("页 %q 应在页面树里", id)
		}
		if page.ID != id {
			t.Fatalf("settingsTreePageOf(%q) 返回了 %q", id, page.ID)
		}
	}
	for _, page := range settingsTreePages() {
		for _, section := range page.Sections {
			if strings.HasPrefix(section.Title, "高级（") {
				t.Fatalf("树页 %q 不应有「高级（N 项）」折叠入口", page.ID)
			}
			for _, row := range section.Rows {
				if strings.HasPrefix(row.Title, "高级（") {
					t.Fatalf("树页 %q 不应有「高级（N 项）」折叠入口", page.ID)
				}
			}
		}
	}
}

// TestSettingsAdvanced_PersonaRowsInMagpieForm 覆盖：人设来源三行归属 Magpie 网关表单，
// 不依赖页底高级区，打开 hub 的网关表单后能看到它们。
func TestSettingsAdvanced_PersonaRowsInMagpieForm(t *testing.T) {
	st := &SettingsState{}
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gateway", Name: "Magpie", Magpie: true, Enabled: true}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderMagpiePersonasSheetBody(c, st)
	}, 1000, 4000)
	tt.Frame()

	waitFor(t, tt, func() bool {
		return tt.HasText("人设规则") &&
			tt.HasText("人设管理地址") &&
			tt.HasText("人设管理密钥") &&
			tt.HasText("手动人设列表") &&
			tt.HasText("测试并刷新")
	})
}

// TestSettingsModels_NoAdvancedArea 覆盖：模型页不再有页底「高级（N 项）」折叠区。
func TestSettingsModels_NoAdvancedArea(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "models")
	waitFor(t, tt, func() bool { return tt.HasText("收藏模型") && tt.HasText("添加供应商") })
	if _, ok := tt.Find("高级（"); ok {
		t.Fatal("模型页不应有高级折叠区")
	}
	for _, section := range pageModels.Sections {
		if strings.HasPrefix(section.Title, "高级") {
			t.Fatalf("模型页不应有高级区: %q", section.Title)
		}
	}
}

// TestSettingsModels_AgyDispatchSubsection 覆盖 agy 面板的调度策略与终端登录。
func TestSettingsModels_AgyDispatchSubsection(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "models")
	clickProviderDetailTabForTest(t, tt, "agy", "")
	// 调度策略直接可见，终端登录需要展开高级。
	selectProviderSheetSectionForTest(t, tt, st, "sec-agy-strategy")
	waitFor(t, tt, func() bool { return tt.HasText("调度策略") })
	selectProviderSheetSectionForTest(t, tt, st, "sec-agy-advanced-login")
	waitFor(t, tt, func() bool { return tt.HasText("终端登录（备用）") })
}

// TestSettingsModels_ClaudeBinRowInExpandedArea 覆盖 Claude 面板的可执行文件路径输入框。
func TestSettingsModels_ClaudeBinRowInExpandedArea(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "models")
	clickProviderDetailTabForTest(t, tt, "claude", "")
	// 展开高级后查看可执行文件路径。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	if _, ok := tt.Find("可执行文件路径 Claude"); !ok {
		t.Fatalf("Claude 面板缺少可执行文件路径输入框: %v", tt.Texts())
	}
}

// TestSettingsModels_SearchExpandsProviderRow 覆盖：搜索命中 agy 调度项后定位到供应商列表。
func TestSettingsModels_SearchExpandsProviderRow(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "models")
	st.mu.Lock()
	st.navSearch = "调度策略"
	st.mu.Unlock()
	tt.Frame()
	if _, ok := tt.Find("settings-nav-hit-sec-agy-routing-strategy"); !ok {
		t.Fatalf("没有调度策略搜索结果: %v", tt.Texts())
	}
	if err := tt.Click("settings-nav-hit-sec-agy-routing-strategy"); err != nil {
		t.Fatalf("点击搜索结果失败: %v", err)
	}
	tt.Frame()
	st.mu.Lock()
	page := st.currentPage
	st.mu.Unlock()
	if page != "models" {
		t.Fatalf("搜索结果应打开模型页，实际 %q", page)
	}
	if _, ok := tt.Find(providerDisplayName("agy")); !ok {
		t.Fatalf("供应商列表缺少 agy: %v", tt.Texts())
	}
}
