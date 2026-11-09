package apisource

import (
	"testing"

	"mcode/internal/harness"
)

func TestPresetByID(t *testing.T) {
	cases := []struct{ id, account string }{
		{"deepseek", "deepseek.apiKey"}, {"zai", "zai.apiKey"}, {"meta", "meta.apiKey"},
		{"magpie", "magpie1.apiKey"}, {"magpie2", "magpie2.apiKey"},
		{"magpie3", "magpie3.apiKey"}, {"magpie4", "magpie4.apiKey"},
	}
	for _, tc := range cases {
		p, ok := PresetByID(tc.id)
		if !ok || p.ID != tc.id || p.Kind != harness.Kind(tc.id) || p.APIKeyAccount != tc.account {
			t.Errorf("PresetByID(%q) = %+v, %v", tc.id, p, ok)
		}
	}
	for _, id := range []string{"unknown", "magpie1", "magpie5"} {
		if _, ok := PresetByID(id); ok {
			t.Errorf("PresetByID(%q) 不应存在", id)
		}
	}
	for _, slot := range []int{-1, 0, 5} {
		if _, ok := Magpie(slot); ok {
			t.Errorf("Magpie(%d) 不应存在", slot)
		}
	}
}

func TestPresetResolveBaseURL(t *testing.T) {
	p := Preset{DefaultBaseURL: "https://default.example", CodingPlanBaseURL: "https://coding.example"}
	for _, tc := range []struct {
		override string
		coding   bool
		want     string
	}{
		{" https://proxy.example ", true, "https://proxy.example"},
		{" ", true, "https://coding.example"},
		{"", false, "https://default.example"},
	} {
		if got := p.ResolveBaseURL(tc.override, tc.coding); got != tc.want {
			t.Errorf("ResolveBaseURL(%q, %v) = %q，想要 %q", tc.override, tc.coding, got, tc.want)
		}
	}
	if got := Meta.ResolveBaseURL("", true); got != Meta.DefaultBaseURL {
		t.Errorf("没有 Coding Plan 地址时得到 %q", got)
	}
}
