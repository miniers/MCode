package mcodeui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

// settingsAnyIsNil 报告 v 是否为 nil，包括装进 any 的 typed nil 指针。
func settingsAnyIsNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

// TestSettingsState_PageClose 覆盖「关页不留状态」：走遍导航里的每一页、
// 打开每个已注册面板并写入各页草稿后关闭设置页，八个页面级状态字段必须全部清空，
// 且关闭过程不能把配置写回磁盘。
func TestSettingsState_PageClose(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, newChatState(fake))
	st.mu.Lock()
	st.open = true
	st.mu.Unlock()

	// 与真实设置面板同构：左导航、右详情；面板由详情区挂在最上层。
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c.Key("test-settings-page-close")).Fill().Children(func() {
			ui.Column(c.Key("test-settings-page-close-nav")).Width(300).FillHeight().Children(func() {
				renderSettingsNav(c, st)
			})
			ui.Column(c.Key("test-settings-page-close-main")).Grow(1).Children(func() {
				renderSettingsDetail(c, st)
			})
		})
	}, 1200, 1600)
	tt.Frame()

	for _, page := range settingsPages() {
		st.selectSettingsPage(page.id)
		tt.Frame()
		tt.Frame()
		st.mu.Lock()
		current := st.currentPage
		st.mu.Unlock()
		if want := normalizeSettingsPageID(page.id); current != want {
			t.Fatalf("切到 %q 后当前页是 %q", want, current)
		}
	}

	// 面板每帧都渲染：打开、渲染两帧，再关闭。
	sheets := settingsTreeSheets()
	if len(sheets) == 0 {
		t.Fatal("没有注册的面板")
	}
	for _, sheet := range sheets {
		openSettingsSheet(st, sheet.ID, "")
		tt.Frame()
		tt.Frame()
		closeSettingsSheet(st)
		tt.Frame()
		if openID, _ := settingsTreeSheetState(st); openID != "" {
			t.Fatalf("面板 %q 关闭后仍处于打开状态: %q", sheet.ID, openID)
		}
	}

	// 各页 getter 目前还写在包级 map 里，这里先走一遍入口，再把状态直接放进字段，
	// 保证下面的 nil 断言不是空转；getter 迁到字段后这一步自然变成同一份状态。
	getMCPUIState(st)
	getMCPCatalogUIState(st)
	getSwarmTiersUIState(st)
	getSwarmUIState(st)
	getSkillsSearchText(st)
	getOpenInPageState(st)
	getQuickRepliesPage(st)
	skillsDraft := "技能草稿"
	nowledge := newNowledgeSpacePageState()
	st.mu.Lock()
	st.mcpPage = &mcpPageUIState{}
	st.mcpCatalogPage = &mcpCatalogUIState{}
	st.swarmTiersPage = &swarmTiersUIState{}
	st.swarmPage = &swarmPairsUIState{}
	st.skillsSearch = &skillsDraft
	st.openInPage = &openInPageState{draftName: "草稿"}
	st.quickRepliesPage = newQuickRepliesPageState(nil)
	st.nowledgePage = &nowledge
	st.mu.Unlock()

	st.cfgMu.Lock()
	cfgBefore := cloneConfig(st.cfg)
	baseBefore := cloneConfig(st.base)
	st.cfgMu.Unlock()
	readConfigFile := func() (content string, found bool) {
		data, err := os.ReadFile(filepath.Join(dataDir, "config.json"))
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		if err != nil {
			t.Fatalf("读取 config.json 失败: %v", err)
		}
		return string(data), true
	}
	cfgFileBefore, hadCfgFile := readConfigFile()

	st.Close()
	if st.IsOpen() {
		t.Fatal("Close 后设置页仍是打开状态")
	}

	st.mu.Lock()
	pages := map[string]any{
		"mcpPage":          st.mcpPage,
		"mcpCatalogPage":   st.mcpCatalogPage,
		"swarmTiersPage":   st.swarmTiersPage,
		"swarmPage":        st.swarmPage,
		"skillsSearch":     st.skillsSearch,
		"openInPage":       st.openInPage,
		"quickRepliesPage": st.quickRepliesPage,
		"nowledgePage":     st.nowledgePage,
	}
	st.mu.Unlock()
	for name, value := range pages {
		// typed nil 指针装进 any 之后和 nil 比较永远不等，这里按反射判断底层值是否为 nil。
		if !settingsAnyIsNil(value) {
			t.Errorf("Close 后 %s 应为 nil，仍是 %#v", name, value)
		}
	}

	// 关闭只落盘待保存的草稿，没有改动时不该碰配置。
	st.cfgMu.Lock()
	cfgAfter := cloneConfig(st.cfg)
	baseAfter := cloneConfig(st.base)
	st.cfgMu.Unlock()
	if !reflect.DeepEqual(cfgBefore, cfgAfter) {
		t.Errorf("Close 改动了内存配置")
	}
	if !reflect.DeepEqual(baseBefore, baseAfter) {
		t.Errorf("Close 改动了配置基线")
	}
	if cfgFileAfter, hasCfgFile := readConfigFile(); hasCfgFile != hadCfgFile || cfgFileAfter != cfgFileBefore {
		t.Fatalf("没有改动时 Close 不应写 config.json")
	}
}
