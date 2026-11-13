package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLoadingIndicatorLabels(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		setAppReduceMotion(t, reduced)
		for _, text := range []string{"", "正在读取"} {
			tester := ui.NewTesterWith(func(c *ui.Context) {
				LoadingIndicator(c, "loading", text, IconSM)
			}, 300, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			label := text
			if label == "" {
				label = "加载中"
			}
			r, ok := tester.Find(label)
			if !ok {
				t.Fatalf("减少动效 = %v，找不到加载标签 %q", reduced, label)
			}
			if text == "" && (r.W != IconSM || r.H != IconSM) {
				t.Errorf("无文字的加载尺寸 = %v，期望 %v", r, IconSM)
			}
			if text != "" {
				_, spinner := tester.Find("加载中")
				if spinner == reduced {
					t.Errorf("减少动效 = %v，旋转指示器存在 = %v", reduced, spinner)
				}
			}
		}
	}
}
