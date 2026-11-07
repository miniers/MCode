// 本文件的测试移植并改写了 magpie 的 internal/edit/toml_test.go、
// toml_top_test.go、crlf_test.go 等（MIT License），见 docs/THIRD_PARTY_NOTICES.md。

package magpiecfg

import (
	"path/filepath"
	"strings"
	"testing"
)

const tomlSample = `# 顶部注释
model = "gpt-4"
model_provider = "openai"

[model_providers.other]
name = "other"
base_url = "https://other.example"

[other_table]
key = 1
`

func TestSetTOMLTableKeepsOtherTablesAndComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, tomlSample)
	err := SetTOMLTable(p, "model_providers.x",
		KV{Path: "name", Value: "x"},
		KV{Path: "base_url", Value: "https://x.example"},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	for _, want := range []string{
		"# 顶部注释",
		`name = "other"`,
		"[other_table]",
		"key = 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q 丢失:\n%s", want, got)
		}
	}
	if i, j, k := strings.Index(got, "[model_providers.other]"), strings.Index(got, "[other_table]"), strings.Index(got, "[model_providers.x]"); !(i < j && j < k) {
		t.Fatalf("已有表的顺序变了或新表没追加在末尾:\n%s", got)
	}
	table, err := GetTOMLTable(p, "model_providers.x")
	if err != nil {
		t.Fatal(err)
	}
	if table == nil {
		t.Fatal("新表读不回来")
	}
	if table["name"] != "x" || table["base_url"] != "https://x.example" {
		t.Fatalf("新表内容不对: %#v", table)
	}
	other, err := GetTOMLTable(p, "model_providers.other")
	if err != nil {
		t.Fatal(err)
	}
	if other == nil || other["base_url"] != "https://other.example" {
		t.Fatalf("其它表被改坏: %#v", other)
	}
}

func TestDelTOMLTable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, tomlSample)
	if err := SetTOMLTable(p, "model_providers.x", KV{Path: "name", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := DelTOMLTable(p, "model_providers.x"); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if strings.Contains(got, "model_providers.x") || strings.Contains(got, `name = "x"`) {
		t.Fatalf("表没删干净:\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") || strings.HasSuffix(got, "\n\n") {
		t.Fatalf("删表后留下多余空行: %q", got)
	}
	for _, want := range []string{"# 顶部注释", "[model_providers.other]", "[other_table]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q 丢失:\n%s", want, got)
		}
	}
	if table, err := GetTOMLTable(p, "model_providers.x"); err != nil {
		t.Fatal(err)
	} else if table != nil {
		t.Fatal("删除的表还能读出来")
	}
}

func TestDelTOMLTop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, tomlSample)
	if err := DelTOMLTop(p, "model"); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if strings.Contains(got, `model = "gpt-4"`) {
		t.Fatalf("顶层键没删掉:\n%s", got)
	}
	if !strings.Contains(got, `model_provider = "openai"`) || !strings.Contains(got, "[model_providers.other]") {
		t.Fatalf("删顶层键时误删了其它内容:\n%s", got)
	}
	if v, ok := GetTOMLTop(p, "model_provider"); !ok || v != "openai" {
		t.Fatalf("剩余顶层键读不回来: %q %v", v, ok)
	}
}

func TestSetTOMLTopPreservingKeepsTrailingComment(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, "model = \"a\" # 我的注释\nmodel_provider = \"x\"\n")
	if err := SetTOMLTopPreserving(p, KV{Path: "model", Value: "b"}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if strings.Contains(got, `model = "a"`) {
		t.Fatalf("旧值没被替换:\n%s", got)
	}
	// 注释留在同一行、位于新值之后；magpie 原有实现只保留注释本身。
	if !strings.Contains(got, "# 我的注释") || strings.Index(got, `"b"`) > strings.Index(got, "# 我的注释") {
		t.Fatalf("行尾注释没有保留在新值之后:\n%s", got)
	}
	if v, ok := GetTOMLTop(p, "model"); !ok || v != "b" {
		t.Fatalf("写入的值读不回来: %q %v", v, ok)
	}
	if v, ok := GetTOMLTop(p, "model_provider"); !ok || v != "x" {
		t.Fatalf("其它顶层键被改坏: %q %v", v, ok)
	}
}

func TestSetTOMLTopAddsMissingKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, "# 顶部注释\n[t]\nkey = 1\n")
	if err := SetTOMLTop(p, KV{Path: "model", Value: "y"}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if !strings.Contains(got, `model = "y"`) {
		t.Fatalf("缺少的顶层键没有加上:\n%s", got)
	}
	if !strings.Contains(got, "# 顶部注释") || !strings.Contains(got, "key = 1") {
		t.Fatalf("写顶层键时破坏了其它内容:\n%s", got)
	}
}

func TestSetTOMLTableKeepsCRLF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, "model = \"a\"\r\n\r\n[t]\r\nk = 1\r\n")
	if err := SetTOMLTable(p, "u", KV{Path: "n", Value: 1}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Fatalf("CRLF 没有保留: %q", got)
	}
	table, err := GetTOMLTable(p, "u")
	if err != nil {
		t.Fatal(err)
	}
	if table == nil || table["n"] != "1" {
		t.Fatalf("新表读不回来: %#v", table)
	}
}

func TestSetTOMLTableReplacesExistingTable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, p, tomlSample)
	if err := SetTOMLTable(p, "model_providers.other", KV{Path: "name", Value: "renamed"}); err != nil {
		t.Fatal(err)
	}
	got := readTestFile(t, p)
	if !strings.Contains(got, `name = "renamed"`) || strings.Contains(got, `name = "other"`) {
		t.Fatalf("已有的表没有整块替换:\n%s", got)
	}
	if strings.Count(got, "[model_providers.other]") != 1 {
		t.Fatalf("表头重复:\n%s", got)
	}
	if !strings.Contains(got, "[other_table]") || !strings.Contains(got, "[model_providers.other]") {
		t.Fatalf("替换表时误删了其它表:\n%s", got)
	}
}
