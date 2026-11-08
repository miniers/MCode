package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadLargeFileStopsAtLimit(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 20000; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	put(t, filepath.Join(root, "large"), b.String())
	r := runTool(t, Env{WorkDir: root}, "read", map[string]any{"path": "large", "limit": 10})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if !strings.Contains(r.Text, "10: line-10") || strings.Contains(r.Text, "11: line-11") {
		t.Fatalf("%q", r.Text)
	}
	if !strings.Contains(r.Text, "More lines available; use offset 11") {
		t.Fatalf("%q", r.Text)
	}
}

func TestFileTextSkipsOversizedFiles(t *testing.T) {
	root := t.TempDir()
	old := maxFileTextBytes
	maxFileTextBytes = 16
	defer func() { maxFileTextBytes = old }()
	put(t, filepath.Join(root, "big"), strings.Repeat("x", 100))
	put(t, filepath.Join(root, "small"), "tiny")
	env := Env{WorkDir: root}
	if _, ok := fileText(env, filepath.Join(root, "big")); ok {
		t.Fatal("超过上限的文件应被跳过")
	}
	text, ok := fileText(env, filepath.Join(root, "small"))
	if !ok || text != "tiny" {
		t.Fatalf("text=%q ok=%v", text, ok)
	}
}

func TestGlobMatchesChineseFilename(t *testing.T) {
	ok, err := globMatch("中文*.txt", "中文文件.txt")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	root := t.TempDir()
	put(t, filepath.Join(root, "中文文件.txt"), "x")
	r := runTool(t, Env{WorkDir: root}, "find", map[string]any{"pattern": "中文*.txt"})
	if r.IsError || !strings.Contains(r.Text, "中文文件.txt") {
		t.Fatalf("%q", r.Text)
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("中", 20000) + "\n"
	got := truncate(s, "Output truncated.")
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8，长度 %d", len(got))
	}
	if len(got) > maxOutputBytes+1+len("Output truncated.") {
		t.Fatalf("截断结果过长：%d", len(got))
	}
}

func TestEditMixedNewlinesKeepsLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a")
	put(t, path, "one\r\ntwo\n")
	r := runTool(t, Env{WorkDir: root}, "edit", map[string]any{"path": "a", "edits": []textEdit{{"two", "TWO\nLINE"}}})
	if r.IsError {
		t.Fatal(r.Text)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "one\r\nTWO\nLINE\n" {
		t.Fatalf("content=%q err=%v", b, err)
	}
}

func TestDiffLinesDropsTrailingEmpty(t *testing.T) {
	lines := diffLines("a\nb\n")
	if len(lines) != 2 || lines[0] != "a\n" || lines[1] != "b\n" {
		t.Fatalf("%q", lines)
	}
}
