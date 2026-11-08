package mcodeui

import (
	"reflect"
	"testing"
)

func TestParseChangelogVersions(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []changelogVersion
	}{
		{
			name: "多个版本及正文空白",
			text: "# 更新日志\n\n## 1.2.3\n\n 第一版 \n\n## 1.2.2\n\n旧版\n\n",
			want: []changelogVersion{{Version: "1.2.3", Body: "第一版"}, {Version: "1.2.2", Body: "旧版"}},
		},
		{
			name: "Windows 换行",
			text: "# 更新日志\r\n\r\n## 1.0\r\n\r\n第一行\r\n第二行\r\n",
			want: []changelogVersion{{Version: "1.0", Body: "第一行\n第二行"}},
		},
		{name: "没有版本", text: "# 更新日志\n\n暂无更新"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseChangelogVersions(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("版本 = %#v，期望 %#v", got, tc.want)
			}
		})
	}
}

func TestAboutChangelogDefaultCollapsed(t *testing.T) {
	if aboutChangelogExpanded["测试中不存在的版本"] {
		t.Fatal("未设置的版本应默认折叠")
	}
}

func TestChangelogParsing(t *testing.T) {
	text := "# 更新日志\n\n## 1.2.3\n\n第一版\n\n## 1.2.2\n\n旧版\n"
	for _, tc := range []struct{ version, want string }{
		{"1.2.3", "第一版"}, {"v1.2.3", "第一版"}, {"1.2", ""}, {"1.2.2", "旧版"},
	} {
		if got := parseChangelogSection(text, tc.version); got != tc.want {
			t.Errorf("版本 %q = %q，期望 %q", tc.version, got, tc.want)
		}
	}
}
