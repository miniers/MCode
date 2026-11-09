package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func TestTablePopup_PublicResize(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	t.Cleanup(func() { closeTablePopup(nil, "table") })
	src := "| A | B |\n| --- | --- |\n" + strings.Repeat("| a | b |\n", 100)
	openTablePopup(s, "table", src, &markdown.Options{}, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderTablePopupOverlay(c, s) }, 1200, 1000)
	tt.Frame()
	initial, ok := tt.Find("table-popup-card")
	if !ok {
		t.Fatal("未找到表格弹窗")
	}
	x, y := initial.X+initial.W-4, initial.Y+initial.H-4
	tt.Press(x, y)
	tt.Move(x+30, y+30)
	tt.Release(x+30, y+30)
	tt.Frame()
	larger, ok := tt.Find("table-popup-card")
	if !ok || larger.W != initial.W+30 || larger.H != initial.H+30 {
		t.Fatalf("单次拖动前 %v，拖动后 %v", initial, larger)
	}
	x, y = larger.X+larger.W-4, larger.Y+larger.H-4
	tt.Press(x, y)
	tt.Move(x, y-900)
	tt.Release(x, y-900)
	tt.Frame()
	small, ok := tt.Find("table-popup-card")
	if !ok || small.H != tablePopupMinHeight {
		t.Fatalf("最小高度 = %v", small)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	openTablePopup(s, "table", src, &markdown.Options{}, nil)
	tt.Frame()
	reopened, ok := tt.Find("table-popup-card")
	if !ok || reopened.W != initial.W || reopened.H != initial.H {
		t.Fatalf("重开尺寸 = %v，初始 = %v", reopened, initial)
	}
}

func TestDialog_ResizeOptIn(t *testing.T) {
	for _, resizable := range []bool{false, true} {
		name := "default"
		if resizable {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			key := "resize-dialog-" + name
			t.Cleanup(func() { modalDragReset(key) })
			open := true
			tt := ui.NewTester(func(c *ui.Context) {
				testDraggableSheet(c, key, &open, "测试", 500, false, resizable, func() { ui.Text(c, "正文") })
			}, 1200, 1000)
			tt.Frame()
			initial, ok := tt.Find("dialog-card-" + key)
			if !ok {
				t.Fatal("未找到弹窗")
			}
			x, y := initial.X+initial.W-4, initial.Y+initial.H-4
			tt.Press(x, y)
			tt.Move(x+30, y+30)
			tt.Release(x+30, y+30)
			tt.Frame()
			after, ok := tt.Find("dialog-card-" + key)
			if !ok {
				t.Fatal("弹窗意外关闭")
			}
			if resizable && (after.W <= initial.W || after.H <= initial.H) {
				t.Fatalf("拖动前 %v，拖动后 %v", initial, after)
			}
			if !resizable && (after.W != initial.W || after.H != initial.H) {
				t.Fatalf("默认不应缩放：拖动前 %v，拖动后 %v", initial, after)
			}
		})
	}
}
