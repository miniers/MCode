package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSettingsNavTitleFontSize(t *testing.T) {
	if settingsNavTitleFontSize < TypeBody || settingsNavTitleFontSize <= TypeFootnote {
		t.Fatalf("导航页名字号 = %v，应不小于 %v 且大于 %v", settingsNavTitleFontSize, TypeBody, TypeFootnote)
	}
}

// TestSettingsNavSearchTriggersLazyLoad 验证导航搜索把当前页滤掉时会自动切到第一个可见页，
// 并真的触发目标页的懒加载：渲染期只登记待切页，再由 applyPendingPageSwitch（详情区每帧开头）
// 走同一个 selectSettingsPage，以前渲染期直接写 currentPage，模型与供应商列表永远不会被拉取。
func TestSettingsNavSearchTriggersLazyLoad(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.open = true
	st.currentPage = "general"
	// 「供应商」只出现在模型与供应商页的关键词里，general 页不匹配。
	st.navSearch = "供应商"

	// 真实布局里导航先渲染、详情区随后在同一帧开头执行待切页，这里照样复现。
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsNav(c, st)
		st.applyPendingPageSwitch()
	}, 900, 1400)
	tt.Frame()

	st.mu.Lock()
	page := st.currentPage
	st.mu.Unlock()
	if page != "models" {
		t.Fatalf("搜索「供应商」应自动切到模型与供应商页，实际 %q", page)
	}

	// 懒加载真跑了：loadProviderRows 先把 loading 置起，再由后台 goroutine 收尾置 loaded。
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.providerRows.loaded
	})
}

// TestSettingsExtra_ShortcutOverridePersistsImmediately 验证显式覆盖（行内重置等）走 updateAndSave
// 立即落盘，而输入框的逐键输入只改草稿、由 flushShortcutDrafts 防抖合并落盘（见
// TestSettingsExtra_ShortcutDraftDebouncedSave）。
func TestSettingsExtra_ShortcutOverridePersistsImmediately(t *testing.T) {
	t.Cleanup(func() { setShortcutOverrides(nil) })
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	st.applyShortcutOverride("toggle-diff", "Alt+Cmd+D")
	st.applyShortcutOverride("toggle-diff", "Alt+Cmd+K")

	if cfg, err := config.Load(dataDir); err != nil {
		t.Fatalf("load saved config failed: %v", err)
	} else if got := cfg.Shortcuts.ShortcutOverrides["toggle-diff"]; got != "Alt+Cmd+K" {
		t.Fatalf("显式覆盖应立即落盘为最后一次值，磁盘上是 %q", got)
	}
}

// TestSettingsExtra_GlobalShortcutInputDebounced 验证全局快捷键输入框只写锁内的草稿与配置，
// 输入过程不落盘，强制防抖后才写入磁盘。
func TestSettingsExtra_GlobalShortcutInputDebounced(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	st.cfg.Shortcuts.GlobalShortcutEnabled = true

	// 全局快捷键已搬到「通用」页的快捷键分节。
	tt := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 3000)
	tt.Frame()
	if err := tt.Click("全局快捷键输入"); err != nil {
		t.Fatalf("focus global shortcut input failed: %v", err)
	}
	tt.Command("selectAll")
	tt.Type("CmdOrCtrl+Alt+K")
	waitFor(t, tt, func() bool {
		st.cfgMu.Lock()
		defer st.cfgMu.Unlock()
		return st.cfg.Shortcuts.GlobalShortcut == "CmdOrCtrl+Alt+K"
	})

	if cfg, err := config.Load(dataDir); err == nil && cfg.Shortcuts.GlobalShortcut != "CmdOrCtrl+Shift+Space" {
		t.Fatalf("输入过程中每次都写盘了，磁盘上已是 %q", cfg.Shortcuts.GlobalShortcut)
	}

	st.flushTextSave(true)
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if cfg.Shortcuts.GlobalShortcut != "CmdOrCtrl+Alt+K" {
		t.Fatalf("强制落盘后全局快捷键 = %q, want %q", cfg.Shortcuts.GlobalShortcut, "CmdOrCtrl+Alt+K")
	}
}
