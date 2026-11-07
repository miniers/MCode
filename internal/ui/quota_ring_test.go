package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestQuotaRingWindows(t *testing.T) {
	windows := []ProviderQuotaWindow{
		{Label: "Weekly · Opus", Period: "7d", UsedPercent: 95},
		{Label: "Weekly", Period: "7d", UsedPercent: 80},
		{Label: "5h", Period: "5h", UsedPercent: 42},
	}
	outer, inner := quotaRingWindows("claude", windows)
	if outer == nil || inner == nil || outer.used != 42 || inner.used != 80 {
		t.Fatalf("圆环窗口分配错误: outer=%+v inner=%+v", outer, inner)
	}
	outer, inner = quotaRingWindows("claude", windows[1:2])
	if outer == nil || outer.used != 80 || inner != nil {
		t.Fatalf("单窗口应只显示外环: outer=%+v inner=%+v", outer, inner)
	}
	for _, w := range []ProviderQuotaWindow{{Period: "5h", Text: true}, {Period: "7d", Unlimited: true}, {Label: "未知"}} {
		outer, inner = quotaRingWindows("claude", []ProviderQuotaWindow{w})
		if outer != nil || inner != nil {
			t.Fatalf("不可用窗口应回退: %+v", w)
		}
	}
}

func TestQuotaRingSVGCache(t *testing.T) {
	color := ui.Color{R: 1, G: 2, B: 3, A: 255}
	a := makeQuotaRingSVG(false, 0.421, 0.8, true, true, color, color)
	b := makeQuotaRingSVG(false, 0.424, 0.8, true, true, color, color)
	if a != b {
		t.Fatal("相同百分比应命中缓存")
	}
}
