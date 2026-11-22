package mcodeui

import "testing"

// personaMenuTestPersonas 返回测试用的人设列表。
func personaMenuTestPersonas() []MagpiePersona {
	return []MagpiePersona{
		{ID: "strict", Name: "严谨审阅", Description: "先找风险再动手"},
		{ID: "cheerful", Name: "轻松搭档", Description: "口语化回复"},
	}
}

// personaMenuChecked 与 personaMenuWriteValue 覆盖三态语义：空串/none/具体 id、以及 id 已不在列表里。
func TestPersonaMenuTriState(t *testing.T) {
	personas := personaMenuTestPersonas()

	checked := [][3]string{
		{"", personaNoneValue},
		{"none", personaNoneValue},
		{"gone", personaNoneValue}, // 人设已被删掉：回落到无人设
		{"strict", personaNoneValue},
		{"strict", "strict"},
		{"strict", "cheerful"},
		{"", "strict"},
		{"none", "strict"},
	}
	want := []bool{true, true, true, false, true, false, false, false}
	for i, c := range checked {
		if got := personaMenuChecked(c[0], c[1], personas); got != want[i] {
			t.Fatalf("personaMenuChecked(%q, %q) = %v, want %v", c[0], c[1], got, want[i])
		}
	}

	if value, ok := personaMenuWriteValue("", personaNoneValue); ok {
		t.Fatalf("从未设置时点无人设不应写入, got %q", value)
	}
	if value, ok := personaMenuWriteValue("strict", personaNoneValue); !ok || value != "none" {
		t.Fatalf("已有人设时点无人设应写 none, got %q ok=%v", value, ok)
	}
	if value, ok := personaMenuWriteValue("", "strict"); !ok || value != "strict" {
		t.Fatalf("选人设应写入其 id, got %q ok=%v", value, ok)
	}
	if _, ok := personaMenuWriteValue("strict", "strict"); ok {
		t.Fatalf("重复选中同一人设不应重复写入")
	}
}
