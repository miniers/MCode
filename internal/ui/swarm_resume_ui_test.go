package mcodeui

import "testing"

// swarmInterruptedBannerTitle 只描述有多少条目被中断；没有条目时应当不产出文案，
// 这样面板里连一条空横栏都不会多出来。
func TestSwarmInterruptedBannerTitle(t *testing.T) {
	cases := []struct {
		name        string
		interrupted int
		want        string
	}{
		{name: "没有中断", interrupted: 0, want: ""},
		{name: "负数当作没有中断", interrupted: -3, want: ""},
		{name: "单个条目", interrupted: 1, want: "有 1 个子代理或排队任务在 MCode 重启时被中断"},
		{name: "多个条目", interrupted: 4, want: "有 4 个子代理或排队任务在 MCode 重启时被中断"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := swarmInterruptedBannerTitle(tc.interrupted); got != tc.want {
				t.Fatalf("swarmInterruptedBannerTitle(%d) = %q, 期望 %q", tc.interrupted, got, tc.want)
			}
		})
	}
}
