package mcodeui

import (
	"fmt"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestProgressBarRatio(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ratio, want float32
	}{
		{"负数", -1, 0}, {"NaN", float32(math.NaN()), 0}, {"零", 0, 0},
		{"一半", 0.5, 0.5}, {"完成", 1, 1}, {"超出", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(200).Children(func() {
					ProgressBar(c, ProgressBarProps{Label: "进度", Ratio: tc.ratio, Height: Space1})
				})
			}, 300, 100)
			track, ok := tester.Find(fmt.Sprintf("进度 %.0f%%", tc.want*100))
			if !ok || track.W != 200 || track.H != Space1 {
				t.Fatalf("轨道尺寸 = %v，存在 = %v", track, ok)
			}
			fill, exists := tester.Find("进度 填充")
			if tc.want == 0 {
				if exists {
					t.Fatal("零进度显示了填充")
				}
				return
			}
			if !exists || math.Abs(float64(fill.W-track.W*tc.want)) > 0.01 || fill.W > track.W {
				t.Fatalf("填充 = %v，轨道 = %v，存在 = %v", fill, track, exists)
			}
		})
	}
}

func TestProgressBarReduceMotionUsesTarget(t *testing.T) {
	setAppReduceMotion(t, true)
	ratio := float32(0.2)
	tt := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c).Width(200).Children(func() {
			ProgressBar(c, ProgressBarProps{Key: "reduced-progress", Label: "进度", Ratio: ratio, Height: Space0})
		})
	}, 300, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	for _, target := range []float32{0.8, 0.1, 1} {
		ratio = target
		tt.Frame()
		fill, ok := tt.Find("进度 填充")
		if !ok || math.Abs(float64(fill.W-200*target)) > 0.01 {
			t.Fatalf("减少动效后的填充 = %+v，目标 = %v", fill, target)
		}
	}
}
