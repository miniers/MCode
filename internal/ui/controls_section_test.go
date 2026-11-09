package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSectionHeading(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) { SectionHeading(c, "section", "分组标题") }, 300, 100)
	if _, ok := tt.Find("分组标题"); !ok {
		t.Fatal("缺少分组标题")
	}
}
