package mcodeui

import "testing"

func TestSettingsExtensions_SectionsAndPageOwnership(t *testing.T) {
	want := []struct{ id, title string }{
		{"skills.list", "技能"},
		{"mcp.custom", "MCP 服务"},
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
	for _, id := range []string{"skills", "mcp"} {
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
