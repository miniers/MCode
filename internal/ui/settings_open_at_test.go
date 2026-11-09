package mcodeui

import (
	"strings"
	"testing"
)

func TestOpenSettingsAt(t *testing.T) {
	resetSettingsWindowForTest()
	t.Cleanup(resetSettingsWindowForTest)
	owner := newChatState(NewFakeClient())
	t.Cleanup(owner.close)
	st := NewSettingsState(settingsTestDir(t), nil, owner)
	setChatSettings(owner, st)

	openSettingsAt(owner, "row-steward-enabled")
	st.mu.Lock()
	page, highlight, jump := st.currentPage, st.highlightItemID, st.jumpToItemID
	st.mu.Unlock()
	if !st.IsOpen() || page != "steward" || highlight != "row-steward-enabled" || jump != "row-steward-enabled" {
		t.Fatalf("设置项定位错误：打开 %v，页面 %q，高亮 %q，跳转 %q", st.IsOpen(), page, highlight, jump)
	}

	openSettingsAt(owner, "appearance")
	st.mu.Lock()
	page, highlight = st.currentPage, st.highlightItemID
	st.mu.Unlock()
	if page != "appearance" || highlight != "" {
		t.Fatalf("页面回退错误：页面 %q，高亮 %q", page, highlight)
	}
}

func TestSettingsPagePaletteCommands(t *testing.T) {
	resetSettingsWindowForTest()
	t.Cleanup(resetSettingsWindowForTest)
	owner := newChatState(NewFakeClient())
	t.Cleanup(owner.close)
	st := NewSettingsState(settingsTestDir(t), nil, owner)
	setChatSettings(owner, st)

	pages := make(map[string]settingsNavDef)
	for _, page := range settingsPages() {
		if page.id != "about" {
			pages["cmd-settings-"+page.id] = page
		}
	}
	items := buildPaletteItems(owner)
	seen := make(map[string]bool)
	var appearance *paletteItem
	for i := range items {
		item := &items[i]
		if !strings.HasPrefix(item.id, "cmd-settings-") {
			continue
		}
		page, ok := pages[item.id]
		if !ok || seen[item.id] {
			t.Fatalf("多余或重复的设置命令：%q", item.id)
		}
		seen[item.id] = true
		if item.kind != "command" || item.title != "设置："+page.title || item.keywords != "设置 settings "+page.title+" "+strings.Join(page.keywords, " ") || item.run == nil {
			t.Fatalf("设置命令错误：%+v", *item)
		}
		if item.id == "cmd-settings-appearance" {
			appearance = item
		}
	}
	if len(seen) != len(pages) || !seen["cmd-settings-appearance"] || !seen["cmd-settings-models"] {
		t.Fatalf("设置命令不完整：已找到 %d，期望 %d", len(seen), len(pages))
	}
	appearance.run(owner)
	st.mu.Lock()
	page := st.currentPage
	st.mu.Unlock()
	if !st.IsOpen() || page != "appearance" {
		t.Fatalf("外观命令应打开外观页面，实际打开 %v，页面 %q", st.IsOpen(), page)
	}
}
