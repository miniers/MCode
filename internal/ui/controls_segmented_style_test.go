package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSegmentedHorizontalPadding(t *testing.T) {
	// 容忍布局取整和浮点误差。
	const segmentPadTolerance float32 = 0.5

	segments := []Segment{{Label: "跟随系统"}, {Label: "浅色"}, {Label: "深色"}}
	selected := 0
	tt := ui.NewTester(func(c *ui.Context) {
		Segmented(c, "segments", &selected, segments, nil)
	}, 400, 100)
	textTester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			for _, segment := range segments {
				ui.Row(c).AlignItems(ui.Start).Children(func() {
					ui.Text(c, segment.Label).FontSize(TypeBody).SingleLine()
				})
			}
		})
	}, 400, 200)

	var previousTextRight float32
	for i, segment := range segments {
		box, ok := tt.Find(segment.Label)
		if !ok {
			t.Fatalf("找不到分段 %q", segment.Label)
		}
		text, ok := textTester.Find(segment.Label)
		if !ok {
			t.Fatalf("找不到参照文字 %q", segment.Label)
		}
		if box.W < text.W+2*settingsCompactPadX-segmentPadTolerance {
			t.Errorf("分段 %q 宽度 %.1f，文字宽度 %.1f，左右内边距不足", segment.Label, box.W, text.W)
		}
		// SegmentedBase 的分段居中排列子元素，由文字宽度推算文字边缘。
		textLeft := box.X + (box.W-text.W)/2
		if i > 0 && textLeft-previousTextRight < 2*settingsCompactPadX-segmentPadTolerance {
			t.Errorf("分段 %q 与前一项文字间距 %.1f，左右内边距不足", segment.Label, textLeft-previousTextRight)
		}
		previousTextRight = textLeft + text.W
	}
}
