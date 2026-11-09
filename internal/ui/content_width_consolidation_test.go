package mcodeui

import (
	"math"
	"testing"
)

func TestBubbleMaxWidthFor(t *testing.T) {
	for _, tc := range []struct {
		width float32
		want  float32
	}{
		{768, 652.8},
		{1120, 952},
	} {
		if got := bubbleMaxWidthFor(tc.width); math.Abs(float64(got-tc.want)) > 0.001 {
			t.Errorf("宽度 %v：得到 %v，期望 %v", tc.width, got, tc.want)
		}
	}
	for _, tc := range []struct {
		width   float32
		percent int
		want    float32
	}{{400, 100, 600}, {1200, 70, 630}, {2000, 100, 960}} {
		state := newProportionalWidthTestState(t, tc.percent, tc.width)
		if got := userBubbleMaxWidth(state); got != tc.want {
			t.Errorf("按比例宽度 %v/%d：得到 %v，期望 %v", tc.width, tc.percent, got, tc.want)
		}
	}
}
