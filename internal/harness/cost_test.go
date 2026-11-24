package harness

import (
	"math"
	"testing"
)

func TestCumulativeCostUSD(t *testing.T) {
	cases := []struct {
		name  string
		costs []float64
		want  float64
	}{
		{"空", nil, 0},
		{"单调递增", []float64{0.7, 0.74, 7.8}, 7.8},
		{"中途重置", []float64{1, 3, 0.5, 2}, 5},
		{"含 0 项", []float64{0, 1, 0, 3, 0}, 3},
		{"含 0 项且重置", []float64{1, 0, 0.5}, 1.5},
	}
	for _, tc := range cases {
		if got := CumulativeCostUSD(tc.costs); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
