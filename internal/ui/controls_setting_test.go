package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingRowLayout(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		stacked     bool
	}{
		{"左右", "设置标题", false},
		{"上下", "堆叠标题", true},
		{"长标题", "这是一个很长的设置标题需要为右侧控件留出空间这是一个很长的设置标题", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				control := func() { ui.ButtonBase(c).Label("测试控件").Width(60).Height(24) }
				if tc.stacked {
					StackedSettingRow(c, "test-row", tc.title, control).FillWidth().Label("设置行")
				} else {
					SettingRow(c, "test-row", tc.title, control).FillWidth().Label("设置行")
				}
			}, 320, 200)
			title, titleOK := tt.Find(tc.title)
			control, controlOK := tt.Find("测试控件")
			row, rowOK := tt.Find("设置行")
			if !titleOK || !controlOK || !rowOK {
				t.Fatal("缺少标题、控件或设置行")
			}
			if tc.stacked {
				if control.Y < title.Y+title.H {
					t.Fatalf("控件应位于标题下方：title=%+v control=%+v", title, control)
				}
			} else if control.X < title.X+title.W || control.X+control.W > row.X+row.W {
				t.Fatalf("控件应位于标题右侧且不超出行：title=%+v control=%+v row=%+v", title, control, row)
			}
		})
	}
}
