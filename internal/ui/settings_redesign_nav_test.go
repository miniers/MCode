package mcodeui

import "testing"

func TestSettingsRedesignNav(t *testing.T) {
	pages := settingsPages()
	want := []struct {
		id    string
		group string
	}{
		{"general", "基础"},
		{"appearance", "基础"},
		{"remote", "基础"},
		{"models", "模型与代理"},
		{"magpie", "模型与代理"},
		{"conversation", "模型与代理"},
		{"swarm", "模型与代理"},
		{"steward", "模型与代理"},
		{"extensions", "能力与工作区"},
		{"workbench", "能力与工作区"},
		{"sourceControl", "能力与工作区"},
		{"about", ""},
	}
	if len(pages) != len(want) {
		t.Fatalf("settingsPages() 数量 = %d，期望 %d", len(pages), len(want))
	}
	for i, w := range want {
		if pages[i].id != w.id || pages[i].group != w.group {
			t.Fatalf("第 %d 页 = (%q, %q)，期望 (%q, %q)", i, pages[i].id, pages[i].group, w.id, w.group)
		}
	}

	itemPage := make(map[string]string)
	for _, item := range settingsItems() {
		itemPage[item.ID] = item.Page
	}
	wantRows := map[string]string{
		"row-theme":                  "appearance",
		"row-steward-enabled":        "steward",
		"row-sc-generate-commit-msg": "sourceControl",
		"row-storage-cleanup":        "sourceControl",
	}
	for id, wantPage := range wantRows {
		if got := itemPage[id]; got != wantPage {
			t.Errorf("settingsItems()[%q].Page = %q，期望 %q", id, got, wantPage)
		}
	}

	normCases := map[string]string{
		"storage":    "sourceControl",
		"appearance": "appearance",
		"shortcuts":  "general",
	}
	for in, wantID := range normCases {
		if got := normalizeSettingsPageID(in); got != wantID {
			t.Errorf("normalizeSettingsPageID(%q) = %q，期望 %q", in, got, wantID)
		}
	}
}
