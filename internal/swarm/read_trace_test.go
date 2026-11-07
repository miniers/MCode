package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyReadTrace_NoPaths 验证当读取记录为空或未提供时，ReadChecked 保持 false，Summary 包含"读取记录不可用"。
func TestApplyReadTrace_NoPaths(t *testing.T) {
	c := Credibility{
		Cited:      2,
		Verified:   2,
		CitedPaths: []string{"calc.go", "internal/calc.go"},
	}

	// 传入 nil
	c.ApplyReadTrace(nil)
	if c.ReadChecked {
		t.Fatalf("传入 nil 时 ReadChecked 应为 false")
	}
	if s := c.Summary(); !strings.HasSuffix(s, "，读取记录不可用") {
		t.Fatalf("期望以 '，读取记录不可用' 结尾，实际: %q", s)
	}

	// 传入空切片
	c.ApplyReadTrace([]string{})
	if c.ReadChecked {
		t.Fatalf("传入空切片时 ReadChecked 应为 false")
	}
	if s := c.Summary(); !strings.HasSuffix(s, "，读取记录不可用") {
		t.Fatalf("期望以 '，读取记录不可用' 结尾，实际: %q", s)
	}
}

// TestApplyReadTrace_AllRead 验证当引用的文件全部在读取记录中时，ReadChecked 为 true，NotRead 为空，Summary 包含"读取一致"。
func TestApplyReadTrace_AllRead(t *testing.T) {
	c := Credibility{
		Cited:      2,
		Verified:   2,
		CitedPaths: []string{"calc.go", "internal/calc.go"},
	}

	c.ApplyReadTrace([]string{"calc.go", "internal/calc.go", "other.go"})
	if !c.ReadChecked {
		t.Fatalf("ReadChecked 应为 true")
	}
	if len(c.NotRead) != 0 {
		t.Fatalf("NotRead 应为空，实际: %v", c.NotRead)
	}
	expected := "引用 2/2 核实，读取一致"
	if s := c.Summary(); s != expected {
		t.Fatalf("期望 %q，实际: %q", expected, s)
	}
}

// TestApplyReadTrace_OneMissing 验证当存在未读引用的文件时，NotRead 包含该文件，Summary 列出未读引用。
func TestApplyReadTrace_OneMissing(t *testing.T) {
	c := Credibility{
		Cited:      3,
		Verified:   3,
		CitedPaths: []string{"calc.go", "internal/calc.go", "secret.go"},
	}

	c.ApplyReadTrace([]string{"calc.go", "internal/calc.go"})
	if !c.ReadChecked {
		t.Fatalf("ReadChecked 应为 true")
	}
	if len(c.NotRead) != 1 || c.NotRead[0] != "secret.go" {
		t.Fatalf("NotRead 应包含 [secret.go]，实际: %v", c.NotRead)
	}
	expected := "引用 3/3 核实，未读却引用：secret.go"
	if s := c.Summary(); s != expected {
		t.Fatalf("期望 %q，实际: %q", expected, s)
	}

	// 验证最多列出 3 项未读引用截断
	c4 := Credibility{
		Cited:      5,
		Verified:   5,
		CitedPaths: []string{"a.go", "b.go", "c.go", "d.go"},
	}
	c4.ApplyReadTrace([]string{"other.go"})
	if len(c4.NotRead) != 4 {
		t.Fatalf("NotRead 应包含 4 项，实际: %v", c4.NotRead)
	}
	expected4 := "引用 5/5 核实，未读却引用：a.go, b.go, c.go"
	if s := c4.Summary(); s != expected4 {
		t.Fatalf("期望 %q，实际: %q", expected4, s)
	}
}

// TestApplyReadTrace_SuffixMatching 验证绝对路径与相对路径之间的前缀与后缀匹配。
func TestApplyReadTrace_SuffixMatching(t *testing.T) {
	// 场景 1：引用为相对路径，读取记录为绝对路径
	c1 := Credibility{
		Cited:      1,
		Verified:   1,
		CitedPaths: []string{"internal/calc.go"},
	}
	c1.ApplyReadTrace([]string{"/workspace/repo/internal/calc.go"})
	if len(c1.NotRead) != 0 {
		t.Fatalf("引用相对路径且读取绝对路径应匹配成功，实际 NotRead: %v", c1.NotRead)
	}
	if !strings.HasSuffix(c1.Summary(), "，读取一致") {
		t.Fatalf("期望以 '，读取一致' 结尾，实际: %q", c1.Summary())
	}

	// 场景 2：引用为绝对路径，读取记录为相对路径
	c2 := Credibility{
		Cited:      1,
		Verified:   1,
		CitedPaths: []string{"/workspace/repo/internal/calc.go"},
	}
	c2.ApplyReadTrace([]string{"internal/calc.go"})
	if len(c2.NotRead) != 0 {
		t.Fatalf("引用绝对路径且读取相对路径应匹配成功，实际 NotRead: %v", c2.NotRead)
	}
	if !strings.HasSuffix(c2.Summary(), "，读取一致") {
		t.Fatalf("期望以 '，读取一致' 结尾，实际: %q", c2.Summary())
	}

	// 场景 3：非路径分隔符的同名后缀不应误匹配（如 not_calc.go 与 calc.go）
	c3 := Credibility{
		Cited:      1,
		Verified:   1,
		CitedPaths: []string{"calc.go"},
	}
	c3.ApplyReadTrace([]string{"not_calc.go"})
	if len(c3.NotRead) != 1 || c3.NotRead[0] != "calc.go" {
		t.Fatalf("非 '/' 分隔的后缀不应匹配，实际 NotRead: %v", c3.NotRead)
	}
}

// TestCheckCitations_CitedPathsPopulated 验证 CheckCitations 填充去重且规范化的 CitedPaths。
func TestCheckCitations_CitedPathsPopulated(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "calc.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// 包含多次引用同一个文件（不同行号）以及一个不存在的文件
	report := "在 `calc.go:4` 中实现了 `return a + b`。\n再次在 `calc.go:3` 中声明 `func Add`。\n在 `missing.go:1` 中有 `dummy code snippet`。"
	cred := CheckCitations(dir, report)

	if cred.Cited != 3 {
		t.Fatalf("期望 Cited 为 3，实际: %d", cred.Cited)
	}
	// calc.go 应被去重，CitedPaths 应为 ["calc.go", "missing.go"]
	expectedPaths := []string{"calc.go", "missing.go"}
	if len(cred.CitedPaths) != len(expectedPaths) {
		t.Fatalf("期望 CitedPaths 长度为 %d，实际: %v", len(expectedPaths), cred.CitedPaths)
	}
	for i, p := range expectedPaths {
		if cred.CitedPaths[i] != p {
			t.Fatalf("CitedPaths[%d] 期望 %q，实际 %q", i, p, cred.CitedPaths[i])
		}
	}
}
