package mcodeui

import (
	"math"
	"testing"

	"mcode/internal/config"
)

func TestBubbleMaxWidthFor(t *testing.T) {
	for _, tc := range []struct {
		width   float32
		setting string
		want    float32
	}{
		{768, config.ContentWidthStandard, 652.8},
		{1120, config.ContentWidthWide, 952},
	} {
		if got := bubbleMaxWidthFor(tc.width, tc.setting, 100, contentColumnPadX); math.Abs(float64(got-tc.want)) > 0.001 {
			t.Errorf("宽度 %v：得到 %v，期望 %v", tc.width, got, tc.want)
		}
	}
	for _, tc := range []struct {
		width   float32
		percent int
	}{{400, 100}, {1200, 70}, {2000, 100}} {
		state := newProportionalWidthTestState(t, tc.percent, tc.width)
		setting, percent, padX := contentWidthPrefs(state)
		if got, want := bubbleMaxWidthFor(tc.width, setting, percent, padX), userBubbleMaxWidth(state); got != want {
			t.Errorf("按比例宽度 %v/%d：得到 %v，期望 %v", tc.width, tc.percent, got, want)
		}
	}
}
