package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTabButtonLabels(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Children(func() {
			TabButton(c, TabButtonProps{Key: "active", Label: "已选标签", Active: true}, nil)
			TabButton(c, TabButtonProps{Key: "inactive", Label: "其他标签"}, nil)
			TabButton(c, TabButtonProps{Key: "custom", Label: "自定义标签"}, func() {
				ui.Text(c, "自定义内容")
			})
		})
	}, 500, 100)
	for _, label := range []string{"已选标签", "其他标签", "自定义标签", "自定义内容"} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("找不到 %q", label)
		}
	}
}
