package mcodeui

import "testing"

// TestCacheHitRateText 验证缓存命中率文本的边界、四舍五入与 0–100 夹紧逻辑。
func TestCacheHitRateText(t *testing.T) {
	cases := []struct {
		name   string
		input  int64
		cached int64
		want   string
	}{
		{name: "input为零", input: 0, cached: 100, want: ""},
		{name: "input为负", input: -10, cached: 100, want: ""},
		{name: "cached为零", input: 1000, cached: 0, want: ""},
		{name: "cached为负", input: 1000, cached: -5, want: ""},
		{name: "普通值", input: 1000, cached: 750, want: "（75%）"},
		{name: "四舍五入进位", input: 3, cached: 2, want: "（67%）"},
		{name: "四舍五入舍去", input: 3, cached: 1, want: "（33%）"},
		{name: "极小正数夹到零", input: 1000, cached: 1, want: "（0%）"},
		{name: "满命中", input: 500, cached: 500, want: "（100%）"},
		{name: "cached大于input夹到100", input: 100, cached: 150, want: "（100%）"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheHitRateText(tc.input, tc.cached); got != tc.want {
				t.Fatalf("cacheHitRateText(%d, %d) = %q, want %q", tc.input, tc.cached, got, tc.want)
			}
		})
	}
}
