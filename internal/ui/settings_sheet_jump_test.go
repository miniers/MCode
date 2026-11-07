package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSettingsSearch_OpenInHitOpensSheet(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.open = true
	st.currentPage = "conversation"
	st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
		{Name: "Zed", AppPath: "/Applications/Zed.app", Enabled: true},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 1200, 1600)
	tt.Frame()
	if err := tt.Click("搜索设置"); err != nil {
		t.Fatalf("点击搜索框失败: %v", err)
	}
	tt.Type("Cursor")
	waitFor(t, tt, func() bool { return tt.HasText("settings-nav-hit-row-openin-Cursor") })
	if err := tt.Click("settings-nav-hit-row-openin-Cursor"); err != nil {
		t.Fatalf("点击应用搜索结果失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		open, _ := settingsTreeSheetState(st)
		return open == "open-in" && tt.HasText("关闭打开方式") && tt.HasText("Cursor") && tt.HasText("切换Cursor")
	})
}

// 跳转标记要留到目标行真正渲染：先展开高级，再消费一次性滚动。
func TestProviderSheetJump_KeepsJumpIDUntilRowRendered(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	st.mu.Lock()
	st.providerRows.currentProvider = "agy"
	st.providerRows.currentProviderKind = "cli"
	st.providerAdvancedOpen = false
	st.providerAdvancedKey = ""
	st.mu.Unlock()
	openSettingsSheetFrom(st, "provider", "sec-agy-auto-warmup", "")
	st.mu.Lock()
	st.jumpToItemID = "sec-agy-auto-warmup"
	st.mu.Unlock()
	waitFor(t, tt, func() bool {
		return providerSheetAdvancedOpen(st, "cli", "agy") && tt.HasText("到期自动预热")
	})
	tt.Frame()
	st.mu.Lock()
	jump := st.jumpToItemID
	st.mu.Unlock()
	if jump != "" {
		t.Fatalf("目标行已渲染，跳转标记仍未消费：%q", jump)
	}
	// 换到其他供应商时高级状态不能沿用。
	if providerSheetAdvancedOpen(st, "cli", "claude") {
		t.Fatal("切换供应商后高级应收起")
	}
}
