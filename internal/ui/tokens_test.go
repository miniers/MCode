package mcodeui

import (
	"testing"
	"time"

	"mcode/internal/config"
)

func TestDensityOf(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  Density
	}{
		{config.DensityCompact, DensityCompact},
		{config.DensityComfortable, DensityComfortable},
		{"", DensityComfortable},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := densityOf(config.AppearanceConfig{ConversationDensity: tc.input}); got != tc.want {
				t.Errorf("densityOf(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestStaggerDelay(t *testing.T) {
	for _, tc := range []struct {
		input int
		want  time.Duration
	}{
		{0, 50 * time.Millisecond},
		{8, 290 * time.Millisecond},
		{20, 290 * time.Millisecond},
		{-1, 50 * time.Millisecond},
	} {
		if got := staggerDelay(tc.input); got != tc.want {
			t.Errorf("staggerDelay(%d) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestWeightForTheme(t *testing.T) {
	for _, tc := range []struct {
		weight int
		dark   bool
		want   int
	}{
		{600, true, 500},
		{500, true, 400},
		{400, true, 400},
		{700, true, 700},
		{600, false, 600},
		{500, false, 500},
	} {
		if got := weightForTheme(tc.weight, tc.dark); got != tc.want {
			t.Errorf("weightForTheme(%d, %t) = %d, want %d", tc.weight, tc.dark, got, tc.want)
		}
	}
}

func TestMotionTokenAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"motionFast", motionFast, DurSnap},
		{"motionIconFade", motionIconFade, DurFast},
		{"motionUnfold", motionUnfold, DurBase},
		{"motionPop", motionPop, DurBase},
		{"motionRollFade", motionRollFade, DurSlow},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
