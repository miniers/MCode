package magpie

// 本文件测手动粘贴的人设列表：三种 JSON 形态、非法输入、ID 去重、30 条上限与名字回落。

import (
	"fmt"
	"strings"
	"testing"
)

func TestParsePersonasJSONShapes(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			name: "personas 对象",
			text: `{"personas":[{"id":"coder","name":"编码","description":"只写代码"}]}`,
		},
		{
			name: "裸数组",
			text: `[{"id":"coder","name":"编码","description":"只写代码"}]`,
		},
		{
			// 直接粘插件页的整段导出。
			name: "options.personas",
			text: `{"options":{"personas":[{"id":"coder","name":"编码","description":"只写代码"}]}}`,
		},
	}
	want := Persona{ID: "coder", Name: "编码", Description: "只写代码"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePersonasJSON(tc.text)
			if err != nil {
				t.Fatalf("ParsePersonasJSON 出错: %v", err)
			}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("ParsePersonasJSON = %+v，想要 %+v", got, want)
			}
		})
	}
}

func TestParsePersonasJSONNameFallsBackToID(t *testing.T) {
	got, err := ParsePersonasJSON(`[{"id":"writer"},{"id":"coder","name":"   "}]`)
	if err != nil {
		t.Fatalf("ParsePersonasJSON 出错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ParsePersonasJSON 条数 = %d，想要 2", len(got))
	}
	for _, p := range got {
		if p.Name != p.ID {
			t.Fatalf("%q 的名字 = %q，想要回落为 ID", p.ID, p.Name)
		}
	}
}

func TestParsePersonasJSONIgnoresText(t *testing.T) {
	// 手动列表没有正文，正文由 magpie 上的插件按 id 注入；有 text 也不影响解析。
	got, err := ParsePersonasJSON(`[{"id":"coder","text":"你是资深工程师"}]`)
	if err != nil {
		t.Fatalf("ParsePersonasJSON 出错: %v", err)
	}
	if len(got) != 1 || got[0].ID != "coder" || got[0].Description != "" {
		t.Fatalf("ParsePersonasJSON = %+v，想要只取 id/name/description", got)
	}
}

func TestParsePersonasJSONDuplicateIDs(t *testing.T) {
	got, err := ParsePersonasJSON(`[{"id":"coder","name":"第一个"},{"id":"coder","name":"第二个"},{"id":"writer"}]`)
	if err != nil {
		t.Fatalf("ParsePersonasJSON 出错: %v", err)
	}
	if want := []string{"coder", "writer"}; strings.Join(personaIDs(got), ",") != strings.Join(want, ",") {
		t.Fatalf("ParsePersonasJSON = %v，想要 %v", personaIDs(got), want)
	}
	if got[0].Name != "第一个" {
		t.Fatalf("重复 ID 保留了 %q，想要第一个", got[0].Name)
	}
}

func TestParsePersonasJSONLimit(t *testing.T) {
	items := make([]string, 0, 35)
	for i := 0; i < 35; i++ {
		items = append(items, fmt.Sprintf(`{"id":"p%02d"}`, i))
	}
	got, err := ParsePersonasJSON("[" + strings.Join(items, ",") + "]")
	if err != nil {
		t.Fatalf("ParsePersonasJSON 出错: %v", err)
	}
	if len(got) != personaLimit {
		t.Fatalf("ParsePersonasJSON 条数 = %d，想要 %d", len(got), personaLimit)
	}
	// 保持原顺序：前 personaLimit 条被保留。
	if got[0].ID != "p00" || got[personaLimit-1].ID != "p29" {
		t.Fatalf("ParsePersonasJSON 顺序不对: 首条 %q、末条 %q", got[0].ID, got[personaLimit-1].ID)
	}
}

func TestParsePersonasJSONSkipsInvalidIDs(t *testing.T) {
	got, err := ParsePersonasJSON(`[{"id":""},{"id":"有 空格"},{"id":"中文"},{"id":"ok-1"},{"id":"ok_2"}]`)
	if err != nil {
		t.Fatalf("ParsePersonasJSON 出错: %v", err)
	}
	if want := []string{"ok-1", "ok_2"}; strings.Join(personaIDs(got), ",") != strings.Join(want, ",") {
		t.Fatalf("ParsePersonasJSON = %v，想要 %v", personaIDs(got), want)
	}
}

func TestParsePersonasJSONRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "空串", text: ""},
		{name: "只有空白", text: "  \n\t "},
		{name: "数组语法错误", text: `[{"id":"coder"`},
		{name: "对象语法错误", text: `{"personas":`},
		{name: "不是 JSON", text: "coder, writer"},
		{name: "数组里是字符串", text: `["coder"]`},
		{name: "没有可用条目", text: `{"personas":[]}`},
		{name: "ID 全不合法", text: `{"personas":[{"id":"有 空格"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParsePersonasJSON(tc.text); err == nil {
				t.Fatalf("ParsePersonasJSON(%q) = %v，想要错误", tc.text, personaIDs(got))
			}
		})
	}
}
