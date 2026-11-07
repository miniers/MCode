package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 搜索框：空查询不显示清除按钮，输入后出现清除按钮，点击后查询串清空且按钮消失。
func TestSearchFieldClearButton(t *testing.T) {
	query := ""
	changed := -1
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("search-test-shell")).Fill().Padding(8).Children(func() {
			SearchField(c, TextFieldProps{
				Key:         "search-test",
				Value:       &query,
				Label:       "搜索",
				Placeholder: "搜索…",
				Clearable:   true,
				ClearLabel:  "清除搜索",
				OnChange: func(val string) {
					changed = len(val)
				},
			})
		})
	}, 300, 80)

	if _, ok := tt.Find("清除搜索"); ok {
		t.Fatal("空查询不应显示清除按钮")
	}

	if err := tt.Click("搜索"); err != nil {
		t.Fatalf("click search failed: %v", err)
	}
	tt.Type("hello")
	tt.Frame()
	if _, ok := tt.Find("清除搜索"); !ok {
		t.Fatalf("非空查询应显示清除按钮, texts: %q", tt.Texts())
	}

	if err := tt.Click("清除搜索"); err != nil {
		t.Fatalf("click clear failed: %v", err)
	}
	tt.Frame()
	if query != "" {
		t.Fatalf("点击清除后 query = %q, want empty", query)
	}
	if changed != 0 {
		t.Fatalf("OnChange 回调值长度 = %d, want 0", changed)
	}
	if _, ok := tt.Find("清除搜索"); ok {
		t.Fatal("清空后不应再显示清除按钮")
	}
}
