package mcodeui

import (
	"math"
	"testing"
)

func TestElevationPresets(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  Elevation
		want Elevation
	}{
		{"popover", ElevPopover, Elevation{{Y: 2, Blur: 6, Alpha: 0.08}, {Y: 8, Blur: 24, Alpha: 0.12}}},
		{"dialog", ElevDialog, Elevation{{Y: 2, Blur: 8, Alpha: 0.08}, {Y: 12, Blur: 32, Alpha: 0.16}}},
		{"float", ElevFloat, ElevDialog},
		{"none", ElevNone, Elevation{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
	if ScrimOpacity != 0.35 {
		t.Errorf("ScrimOpacity = %v, want 0.35", ScrimOpacity)
	}
}

func TestElevationAlpha(t *testing.T) {
	for _, tc := range []struct {
		alpha float32
		layer int
		dark  bool
		want  float32
	}{
		{0.08, 0, true, 0.20},
		{0.12, 1, true, 0.32},
		{0.16, 1, true, 0.16 * (32.0 / 12.0)},
		{0.08, 0, false, 0.08},
		{0.12, 1, false, 0.12},
	} {
		if got := elevationAlpha(tc.alpha, tc.layer, tc.dark); math.Abs(float64(got-tc.want)) > 0.000001 {
			t.Errorf("elevationAlpha(%v, %d, %t) = %v, want %v", tc.alpha, tc.layer, tc.dark, got, tc.want)
		}
	}
}
