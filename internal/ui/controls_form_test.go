package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFormActionsRightAligned(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		FormActions(c, "actions", func() {
			Button(c, ButtonOpts{Text: "取消"})
			Button(c, ButtonOpts{Text: "保存"})
		}).Label("操作行")
	}, 400, 100)
	row, ok := tester.Find("操作行")
	if !ok {
		t.Fatal("找不到操作行")
	}
	first, ok := tester.Find("取消")
	if !ok {
		t.Fatal("找不到取消按钮")
	}
	last, ok := tester.Find("保存")
	if !ok {
		t.Fatal("找不到保存按钮")
	}
	if first.X >= last.X || last.X+last.W < row.X+row.W-1 || last.X+last.W > row.X+row.W+1 {
		t.Fatalf("按钮未按顺序右对齐：行=%v，取消=%v，保存=%v", row, first, last)
	}
}
