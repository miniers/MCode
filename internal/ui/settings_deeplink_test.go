package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestNormalizeSettingsPageID(t *testing.T) {
	cases := map[string]string{
		"providers": "models", "steward": "swarm", "openIn": "workbench",
		"quickReplies": "conversation", "sourceControl": "sourceControl", "archive": "conversation",
		"shortcuts": "general", "about": "general", "skills": "extensions",
		"mcp": "extensions", "nowledge": "nowledge",
		"general": "general", "appearance": "appearance", "conversation": "conversation",
		"models": "models", "swarm": "swarm", "extensions": "extensions",
		"workbench": "workbench", "remote": "remote", "advanced": "general", "": "",
	}
	for id, want := range cases {
		if got := normalizeSettingsPageID(id); got != want {
			t.Errorf("normalizeSettingsPageID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSettingsLegacyPageDeepLinks(t *testing.T) {
	for _, tc := range []struct{ id, page, sheet string }{
		{"shortcuts", "general", "shortcuts"}, {"quickReplies", "conversation", "quick-replies"},
		{"steward", "swarm", "steward"}, {"archive", "conversation", "archive"}, {"openIn", "workbench", "open-in"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, nil)
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1400, 4200)
			st.OpenPage(tc.id)
			tt.Frame()
			sheet, _ := settingsTreeSheetState(st)
			if st.currentPage != tc.page || sheet != tc.sheet {
				t.Fatalf("page=%q sheet=%q, want %q %q", st.currentPage, sheet, tc.page, tc.sheet)
			}
		})
	}
}

func TestSettingsAdvanced_SearchIdle(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.navSearch = "空闲"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 1000, 2500)
	tt.Frame()
	if !tt.HasText("settings-nav-hit-row-nowledge-sync-helper-threads") {
		t.Fatal("未命中记忆页的同步编排子代理会话")
	}
	if err := tt.Click("settings-nav-hit-row-nowledge-sync-helper-threads"); err != nil {
		t.Fatal(err)
	}
	if st.currentPage != "nowledge" {
		t.Fatalf("搜索命中后页面为 %q", st.currentPage)
	}
}
