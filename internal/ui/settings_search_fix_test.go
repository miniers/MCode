package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func searchSettingsItem(t *testing.T, query, id string) settingsItemHit {
	t.Helper()
	for _, hit := range matchSettingsItems(query) {
		if hit.Item.ID == id {
			return hit
		}
	}
	t.Fatalf("搜索 %q 未找到 %q", query, id)
	return settingsItemHit{}
}

func TestSettingsSearchTargetsExist(t *testing.T) {
	for _, item := range settingsItems() {
		if item.ID == "row-models-none" {
			t.Error("已移除的空模型入口仍在索引中")
		}
		if _, ok := settingsTreePageOf(item.Page); !ok {
			t.Errorf("%s 的页面 %q 不存在", item.ID, item.Page)
		}
		if item.Sheet != "" {
			if _, ok := settingsTreeSheetOf(item.Sheet); !ok {
				t.Errorf("%s 的面板 %q 不存在", item.ID, item.Sheet)
			}
		}
	}
}

func TestSettingsSearchBodySheetTargets(t *testing.T) {
	for _, tc := range []struct {
		query, id, page, sheet string
	}{
		{"工作树清理", "block-archive-worktrees", "sourceControl", "worktree"},
		{"快捷回复", "block-quick-replies", "conversation", "quick-replies"},
		{"已归档会话", "row-archive-", "conversation", "archive"},
		{"打开方式", "row-openin-", "workbench", "open-in"},
		{"功能快捷键", "row-shortcut-", "general", "shortcuts"},
		{"添加服务", "block-mcp-catalog", "extensions", "mcp-add"},
		{"Magpie 网关", "sheet-magpie-gateway", "magpie", "magpie-gateway"},
		{"人设管理地址", "row-magpie-persona-weburl", "magpie", "magpie-personas"},
	} {
		t.Run(tc.sheet, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.currentPage = tc.page
			if tc.sheet == "worktree" {
				st.currentPage = "general"
			}
			st.focusSettingsItem(searchSettingsItem(t, tc.query, tc.id))
			if st.currentPage != tc.page || st.openSheet != tc.sheet {
				t.Fatalf("定位到 %q/%q，期望 %q/%q", st.currentPage, st.openSheet, tc.page, tc.sheet)
			}
			if (tc.id == "block-archive-worktrees" || tc.id == "block-quick-replies") && st.jumpToItemID != "" {
				t.Errorf("没有对应行的面板保留了定位标记 %q", st.jumpToItemID)
			}
		})
	}
}

func TestSettingsSearchProviderAddTypes(t *testing.T) {
	for _, tc := range []struct {
		query, id string
		kind      int
	}{
		{"命令行供应商", "row-provider-add-cli-", 0},
		{"预设供应商", "row-provider-add-preset-", 1},
		{"自定义端点", "row-provider-add-custom", 1},
		{"名称", "row-provider-add-name", 2},
		{"密钥", "row-provider-add-apikey", 2},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.currentPage = "models"
			st.presets.addType = (tc.kind + 1) % 3
			st.focusSettingsItem(searchSettingsItem(t, tc.query, tc.id))
			if st.openSheet != "provider-add" || st.presets.addType != tc.kind {
				t.Fatalf("面板 %q 类型 %d，期望 provider-add 类型 %d", st.openSheet, st.presets.addType, tc.kind)
			}
		})
	}
}

func TestSettingsSearchCustomEndpointRows(t *testing.T) {
	for _, tc := range []struct{ query, id, row, label string }{
		{"名称", "row-ep-form-name", "row-provider-add-name", "端点名称"},
		{"接口地址", "row-ep-form-baseurl", "row-provider-add-baseurl", "端点 BaseURL"},
		{"默认模型", "row-ep-form-model", "row-provider-add-model", "端点默认模型"},
		{"密钥", "row-ep-form-apikey", "row-provider-add-apikey", "端点 API Key"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.currentPage = "models"
			st.presets.loaded = true
			st.focusSettingsItem(searchSettingsItem(t, tc.query, tc.id))
			if st.openSheet != "provider-add" || st.presets.addType != 2 || st.jumpToItemID != tc.row {
				t.Fatalf("面板 %q 类型 %d 定位 %q", st.openSheet, st.presets.addType, st.jumpToItemID)
			}
			tt := ui.NewTester(func(c *ui.Context) {
				st.syncSettingsRowFocus()
				renderProviderAddSheetBody(c, st, nil)
			}, 900, 1000)
			tt.Frame()
			if !tt.HasText(tc.label) || st.jumpToItemID != "" || st.highlightItemID != tc.row {
				t.Errorf("目标行未渲染或未消费定位：label=%q jump=%q highlight=%q", tc.label, st.jumpToItemID, st.highlightItemID)
			}
		})
	}
}

func TestSettingsSearchProviderFallback(t *testing.T) {
	for _, tc := range []struct{ query, id string }{
		{"余额", "row-provider-balance"},
		{"可执行文件路径", "row-provider-bin"},
		{"接口地址", "row-preset-form-baseurl"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.currentPage = "models"
			st.openSheet = "provider"
			st.focusSettingsItem(searchSettingsItem(t, tc.query, tc.id))
			if st.currentPage != "models" || st.openSheet != "" || st.jumpToItemID != "row-providers-list" || st.highlightItemID != "row-providers-list" {
				t.Fatalf("定位到 %q/%q，行 %q，高亮 %q", st.currentPage, st.openSheet, st.jumpToItemID, st.highlightItemID)
			}
		})
	}
}

func TestSettingsSearchDynamicFocusFirstRow(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.currentPage = "workbench"
	st.focusSettingsItem(searchSettingsItem(t, "打开方式", "row-openin-"))
	tt := ui.NewTester(func(c *ui.Context) {
		st.syncSettingsRowFocus()
		renderSettingRow(c, "row-openin-first", "第一个", func() {})
		renderSettingRow(c, "row-openin-second", "第二个", func() {})
	}, 900, 1000)
	tt.Frame()
	if st.openSheet != "open-in" || st.jumpToItemID != "" || st.highlightItemID != "row-openin-first" {
		t.Fatalf("面板 %q 定位 %q 高亮 %q", st.openSheet, st.jumpToItemID, st.highlightItemID)
	}
}
