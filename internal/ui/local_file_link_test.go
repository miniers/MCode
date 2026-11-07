package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFileLinkPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "月报 (2026年10月).docx")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, dest string
		want       bool
	}{
		{"绝对路径", file, true},
		{"file URL 百分号编码", "file://" + dir + "/%E6%9C%88%E6%8A%A5%20(2026%E5%B9%B410%E6%9C%88).docx", true},
		{"位置后缀", file + "#L12", true},
		{"不存在", filepath.Join(dir, "none.docx"), false},
		{"网页", "https://example.com/a", false},
		{"相对路径", "a.docx", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := localFileLinkPath(tc.dest)
			if ok != tc.want {
				t.Fatalf("localFileLinkPath(%q) ok = %v, want %v", tc.dest, ok, tc.want)
			}
			if ok && got != file {
				t.Fatalf("path = %q, want %q", got, file)
			}
		})
	}
}

func TestExtractLocalFileLinks(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.docx")
	b := filepath.Join(dir, "b.pdf")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	text := "打开 [月报](" + a + ") 和 [网页](https://example.com)，再看 [重复](" + a + ") 与 [B](file://" + b + ")。"
	got := extractLocalFileLinks(text)
	if len(got) != 2 || got[0].Path != a || got[0].Label != "月报" || got[1].Path != b || got[1].Label != "B" {
		t.Fatalf("extractLocalFileLinks = %+v", got)
	}
	if extractLocalFileLinks("没有链接 /tmp/x") != nil {
		t.Fatal("无链接文本应返回 nil")
	}
}

func TestExtractLocalFileLinksFromLinkifiedText(t *testing.T) {
	dir := t.TempDir()
	file := writeTestFile(t, filepath.Join(dir, "docs", "plans", "x.md"))
	text := linkifyInlinePaths("见 `docs/plans/x.md:12`。", dir)
	got := extractLocalFileLinks(text)
	if len(got) != 1 || got[0].Path != file || got[0].Label != "docs/plans/x.md:12" {
		t.Fatalf("extractLocalFileLinks = %+v", got)
	}
}
