package mcodeui

import (
	"slices"
	"testing"
)

func settingsTreePageDefOf(id string) (settingsPage, bool) {
	for _, p := range settingsTreePageDefs {
		if p.ID == id {
			return p, true
		}
	}
	return settingsPage{}, false
}

func settingsPageSectionTitles(p settingsPage) []string {
	titles := make([]string, 0, len(p.Sections))
	for _, sec := range p.Sections {
		titles = append(titles, sec.Title)
	}
	return titles
}

// TestSettingsRedesignSections 验证设置重设计后各页的分节顺序、分节标题与行顺序。
func TestSettingsRedesignSections(t *testing.T) {
	t.Run("通用页分节标题顺序", func(t *testing.T) {
		cases := []struct {
			pageID string
			want   []string
		}{
			{
				pageID: "general",
				want:   []string{"权限", "通知与提示音", "启动与托盘", "快捷键", "备份与恢复"},
			},
		}
		for _, tc := range cases {
			page, ok := settingsTreePageDefOf(tc.pageID)
			if !ok {
				t.Fatalf("找不到设置页 %q", tc.pageID)
			}
			got := settingsPageSectionTitles(page)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("页面 %q 分节标题顺序 = %v，期望 %v", tc.pageID, got, tc.want)
			}
		}
	})

	t.Run("各页分节标题", func(t *testing.T) {
		cases := []struct {
			pageID     string
			wantFirst  string
			wantTitles []string
		}{
			{
				pageID:     "magpie",
				wantTitles: []string{"智能体路由", "模型矩阵"},
			},
			{
				pageID:    "swarm",
				wantFirst: "开关",
			},
			{
				pageID:     "steward",
				wantTitles: []string{"值守与安静时段", "高级"},
			},
			{
				pageID:     "extensions",
				wantTitles: []string{"记忆高级"},
			},
			{
				pageID:     "workbench",
				wantTitles: []string{"看板 MCP 工具", "终端与预览"},
			},
		}
		for _, tc := range cases {
			page, ok := settingsTreePageDefOf(tc.pageID)
			if !ok {
				t.Fatalf("找不到设置页 %q", tc.pageID)
			}
			titles := settingsPageSectionTitles(page)
			if tc.wantFirst != "" {
				if len(titles) == 0 || titles[0] != tc.wantFirst {
					t.Fatalf("页面 %q 首节标题 = %v，期望 %q", tc.pageID, titles, tc.wantFirst)
				}
			}
			for _, want := range tc.wantTitles {
				if !slices.Contains(titles, want) {
					t.Fatalf("页面 %q 缺少分节 %q，实际分节：%v", tc.pageID, want, titles)
				}
			}
		}
	})

	t.Run("分节末尾行", func(t *testing.T) {
		cases := []struct {
			pageID       string
			sectionTitle string
			wantLastID   string
			wantLastName string
		}{
			{
				pageID:       "conversation",
				sectionTitle: "消息与排队",
				wantLastID:   "row-quick-replies",
				wantLastName: "快捷回复",
			},
		}
		for _, tc := range cases {
			page, ok := settingsTreePageDefOf(tc.pageID)
			if !ok {
				t.Fatalf("找不到设置页 %q", tc.pageID)
			}
			var target *settingsSection
			for i := range page.Sections {
				if page.Sections[i].Title == tc.sectionTitle {
					target = &page.Sections[i]
					break
				}
			}
			if target == nil {
				t.Fatalf("页面 %q 找不到分节 %q", tc.pageID, tc.sectionTitle)
			}
			if len(target.Rows) == 0 {
				t.Fatalf("页面 %q 分节 %q 没有任何行", tc.pageID, tc.sectionTitle)
			}
			last := target.Rows[len(target.Rows)-1]
			if last.ID != tc.wantLastID || last.Title != tc.wantLastName {
				t.Fatalf("页面 %q 分节 %q 最后一行为 (%q, %q)，期望 (%q, %q)",
					tc.pageID, tc.sectionTitle, last.ID, last.Title, tc.wantLastID, tc.wantLastName)
			}
		}
	})
}
