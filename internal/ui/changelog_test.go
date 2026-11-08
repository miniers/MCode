package mcodeui

import "testing"

func TestChangelogParsing(t *testing.T) {
	text := "# 更新日志\n\n## 1.2.3\n\n第一版\n\n## 1.2.2\n\n旧版\n"
	if got := parseChangelogBody(text); got != "## 1.2.3\n\n第一版\n\n## 1.2.2\n\n旧版" {
		t.Fatalf("更新日志正文 = %q", got)
	}
	for _, tc := range []struct{ version, want string }{
		{"1.2.3", "第一版"}, {"v1.2.3", "第一版"}, {"1.2", ""}, {"1.2.2", "旧版"},
	} {
		if got := parseChangelogSection(text, tc.version); got != tc.want {
			t.Errorf("版本 %q = %q，期望 %q", tc.version, got, tc.want)
		}
	}
}
