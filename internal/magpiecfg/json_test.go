// 本文件的测试移植并改写了 magpie 的 internal/edit/crlf_json_test.go、
// jsonc_comments_test.go、bom_test.go、del_test.go 等（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。

package magpiecfg

import (
	"path/filepath"
	"strings"
	"testing"
)

const jsoncWithComment = `// 顶层说明
{
    /* 块注释 */
    "alpha": 1,
    "beta": {
        "gamma": true
    }
}
`

func TestSetJSONKeepsCommentsIndentAndOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.jsonc")
	writeTestFile(t, p, jsoncWithComment)
	if err := SetJSON(p, KV{Path: "beta.delta", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	for _, want := range []string{"// 顶层说明", "/* 块注释 */", `        "delta": "x"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q 不在结果里:\n%s", want, got)
		}
	}
	if strings.Index(got, `"alpha"`) > strings.Index(got, `"beta"`) {
		t.Fatalf("已有键的相对顺序变了:\n%s", got)
	}
	if v, ok := GetJSON(p, "beta.delta"); !ok || v != "x" {
		t.Fatalf("写入的嵌套键读不回来: %q %v", v, ok)
	}
	if v, ok := GetJSON(p, "alpha"); !ok || v != "1" {
		t.Fatalf("已有键被改坏: %q %v", v, ok)
	}
}

func TestSetJSONReplacesInPlace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.jsonc")
	writeTestFile(t, p, jsoncWithComment)
	if err := SetJSON(p, KV{Path: "alpha", Value: 2}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if !strings.Contains(got, `"alpha": 2`) {
		t.Fatalf("值没有就地替换:\n%s", got)
	}
	if strings.Count(got, `"alpha"`) != 1 {
		t.Fatalf("键被写了多次:\n%s", got)
	}
	if strings.Index(got, `"alpha"`) > strings.Index(got, `"beta"`) {
		t.Fatalf("替换改变了键顺序:\n%s", got)
	}
}

func TestSetJSONKeepsCRLF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, "{\r\n    \"a\": 1\r\n}\r\n")
	if err := SetJSON(p, KV{Path: "b", Value: 2}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Fatalf("CRLF 没有保留: %q", got)
	}
	if !strings.Contains(got, "\r\n    \"b\": 2,") {
		t.Fatalf("新增的行没有使用 CRLF 和四空格缩进: %q", got)
	}
	if strings.Count(got, `"a"`) != 1 || strings.Count(got, `"b"`) != 1 {
		t.Fatalf("写入结果不对: %q", got)
	}
}

func TestSetJSONKeepsBOM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, "\xef\xbb\xbf{\n  \"a\": 1\n}\n")
	if err := SetJSON(p, KV{Path: "a", Value: 2}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if !strings.HasPrefix(got, "\xef\xbb\xbf") || strings.Count(got, "\xef\xbb\xbf") != 1 {
		t.Fatalf("BOM 没有原样保留: %q", got)
	}
	if v, ok := GetJSON(p, "a"); !ok || v != "2" {
		t.Fatalf("读不回写入的值: %q %v", v, ok)
	}
}

func TestSetJSONCreatesFileAndParents(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "settings.json")
	if err := SetJSON(p, KV{Path: "model", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := GetJSON(p, "model"); !ok || v != "x" {
		t.Fatalf("新文件读不回写入的值: %q %v", v, ok)
	}
	got := readTestFile(t, p)
	if !strings.HasPrefix(got, "{\n") || !strings.HasSuffix(got, "}\n") {
		t.Fatalf("新文件不是对象: %q", got)
	}
}

func TestDelJSONNestedKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, "{\n    \"a\": {\n        \"b\": 1,\n        \"c\": 2\n    },\n    \"d\": 3\n}\n")
	if err := DelJSON(p, "a.b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := GetJSON(p, "a.b"); ok {
		t.Fatal("a.b 没删掉")
	}
	if v, ok := GetJSON(p, "a.c"); !ok || v != "2" {
		t.Fatalf("同级的键被删了: %q %v", v, ok)
	}
	if v, ok := GetJSON(p, "d"); !ok || v != "3" {
		t.Fatalf("其它键被删了: %q %v", v, ok)
	}
	if err := DelJSON(p, "d"); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if strings.Contains(got, `"d"`) {
		t.Fatalf("顶层键没删掉:\n%s", got)
	}
	if !ValidJSONC([]byte(got)) {
		t.Fatalf("删除后不是有效 JSONC:\n%s", got)
	}
}

func TestDelJSONMissingKeyIsNoop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, "{\n  \"a\": 1\n}\n")
	before := readTestFile(t, p)
	if err := DelJSON(p, "nope.deep"); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, p); got != before {
		t.Fatalf("删不存在的键改动了文件: %q", got)
	}
}

// TestSetThenDelJSONRestoresInlineObject 校验往单行对象里插入成员再删掉后，
// 文件逐字节回到原样：插入时加的逗号与换行要由删除一并带走。
func TestSetThenDelJSONRestoresInlineObject(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.json")
	original := "{\n  \"version\": 1,\n  \"providers\": {\"other\": {\"name\": \"Other\"}}\n}\n"
	writeTestFile(t, p, original)
	if err := SetJSON(p, KV{Path: "providers.mcode-magpie", Value: map[string]any{"name": "Magpie"}}); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	if _, ok := GetJSON(p, "providers.mcode-magpie.name"); !ok {
		t.Fatal("插入的成员读不回来")
	}
	if err := DelJSON(p, "providers.mcode-magpie"); err != nil {
		t.Fatalf("DelJSON: %v", err)
	}
	if got := readTestFile(t, p); got != original {
		t.Fatalf("插入再删除后没有逐字节还原:\n得到: %q\n想要: %q", got, original)
	}
}

// TestSetThenDelJSONRestoresMultilineObject 校验往普通多行对象（成员独占
// 一行）里插入成员再删掉也逐字节还原：插入时加在 '{' 后面的换行和缩进要
// 由删除一并带走，不管对象里已经有几个成员、成员在哪一层。
func TestSetThenDelJSONRestoresMultilineObject(t *testing.T) {
	cases := []struct{ name, original, keyPath string }{
		{
			name:     "已有成员",
			original: "{\n  \"version\": 1,\n  \"providers\": {\n    \"other\": {\n      \"name\": \"Other\"\n    }\n  }\n}\n",
			keyPath:  "providers.mcode-magpie",
		},
		{
			name:     "只有一个成员",
			original: "{\n  \"providers\": {\n    \"other\": {\"name\": \"Other\"}\n  }\n}\n",
			keyPath:  "providers.mcode-magpie",
		},
		{
			name:     "顶层键",
			original: "{\n  \"version\": 1\n}\n",
			keyPath:  "mcode-magpie",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "models.json")
			writeTestFile(t, p, c.original)
			if err := SetJSON(p, KV{Path: c.keyPath, Value: map[string]any{"name": "Magpie"}}); err != nil {
				t.Fatalf("SetJSON: %v", err)
			}
			if _, ok := GetJSON(p, c.keyPath+".name"); !ok {
				t.Fatal("插入的成员读不回来")
			}
			if err := DelJSON(p, c.keyPath); err != nil {
				t.Fatalf("DelJSON: %v", err)
			}
			if got := readTestFile(t, p); got != c.original {
				t.Fatalf("插入再删除后没有逐字节还原:\n得到: %q\n想要: %q", got, c.original)
			}
		})
	}
}

func TestValidJSONC(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"{\n  // c\n  \"a\": 1,\n}\n", true},
		{"{\"a\": 1, \"b\": {\"c\": 2}}\n", true},
		{"{\"a\": ,}\n", false},
		{"{,}\n", false},
		{"{\"a\": 1 /* 未闭合\n", false},
	}
	for _, c := range cases {
		if got := ValidJSONC([]byte(c.in)); got != c.want {
			t.Fatalf("ValidJSONC(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGetJSONRaw(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, p, `{
  // 用户的注释
  "modelPicker": {
    "replaceBuiltInOptions": true,
    "options": [
      {"label": "Sonnet", "model": "claude-sonnet-4-5", "description": "via magpie"}
    ]
  },
  "model": "user-model",
  "count": 200000
}
`)
	raw, had := GetJSONRaw(p, "modelPicker")
	if !had {
		t.Fatal("modelPicker 应该存在")
	}
	want := `{"options":[{"description":"via magpie","label":"Sonnet","model":"claude-sonnet-4-5"}],"replaceBuiltInOptions":true}`
	if raw != want {
		t.Fatalf("modelPicker = %s，想要 %s", raw, want)
	}
	if got, _ := GetJSONRaw(p, "model"); got != `"user-model"` {
		t.Fatalf("字符串值应带引号，得到 %s", got)
	}
	if got, _ := GetJSONRaw(p, "count"); got != "200000" {
		t.Fatalf("数字应保留原文，得到 %s", got)
	}
	if got, had := GetJSONRaw(p, "missing"); had || got != "" {
		t.Fatalf("不存在的键应返回空串和 false，得到 (%q, %v)", got, had)
	}
}

func TestSameJSONValue(t *testing.T) {
	pretty := fieldState{Value: `{
  "options": [{"label": "Sonnet", "model": "m"}],
  "replaceBuiltInOptions": true
}`, Had: true}
	compact := fieldState{Value: `{"replaceBuiltInOptions":true,"options":[{"model":"m","label":"Sonnet"}]}`, Had: true}
	if !sameJSONValue(pretty, compact) {
		t.Fatal("缩进和键序不同的同一对象应该相等")
	}
	if sameJSONValue(pretty, fieldState{}) {
		t.Fatal("存在与不存在不应该相等")
	}
	if sameJSONValue(fieldState{Value: `"200000"`, Had: true}, fieldState{Value: "200000", Had: true}) {
		t.Fatal("字符串 \"200000\" 与数字 200000 不应该相等")
	}
	if !sameJSONValue(fieldState{Value: "user-model", Had: true}, fieldState{Value: `"user-model"`, Had: true}) {
		t.Fatal("旧版状态里的裸字符串应等于带引号的同一个字符串")
	}
	if sameJSONValue(fieldState{Value: `"a"`, Had: true}, fieldState{Value: `"b"`, Had: true}) {
		t.Fatal("不同的字符串不应该相等")
	}
}
