package mcodeui

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestFile 在目录下创建文件，返回其绝对路径。
func writeTestFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveInlinePath(t *testing.T) {
	dir := t.TempDir()
	file := writeTestFile(t, filepath.Join(dir, "docs", "plans", "x.md"))
	home, homeErr := os.UserHomeDir()
	cases := []struct {
		name, code, workDir string
		want                string
		ok                  bool
	}{
		{"相对路径", "docs/plans/x.md", dir, file, true},
		{"纯文件名", "x.md", filepath.Dir(file), file, true},
		{"行号后缀", "docs/plans/x.md:12", dir, file, true},
		{"行列后缀", "docs/plans/x.md:12:5", dir, file, true},
		{"冒号行号范围", "docs/plans/x.md:12-20", dir, file, true},
		{"行号范围后缀", "docs/plans/x.md#L12-L20", dir, file, true},
		{"绝对路径", file, "", file, true},
		{"目录", "docs", dir, filepath.Join(dir, "docs"), true},
		{"不存在", "docs/none.md", dir, "", false},
		{"含空格", "my file.md", dir, "", false},
		{"通配符", "docs/*.md", dir, "", false},
		{"网址", "https://example.com/x", dir, "", false},
		{"空", "   ", dir, "", false},
		{"无工作目录", "docs/plans/x.md", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveInlinePath(tc.code, tc.workDir)
			if ok != tc.ok {
				t.Fatalf("resolveInlinePath(%q, %q) ok = %v, want %v", tc.code, tc.workDir, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("resolveInlinePath(%q, %q) = %q, want %q", tc.code, tc.workDir, got, tc.want)
			}
		})
	}
	t.Run("用户主目录", func(t *testing.T) {
		if homeErr != nil {
			t.Skip("无法获取用户主目录")
		}
		got, ok := resolveInlinePath("~/", "")
		if !ok || got != filepath.Clean(home) {
			t.Fatalf("resolveInlinePath(~/ ) = %q, %v", got, ok)
		}
	})
}

func TestLinkifyInlinePathsLineRanges(t *testing.T) {
	dir := t.TempDir()
	file := writeTestFile(t, filepath.Join(dir, "a.go"))
	for _, tc := range []struct{ code, fragment string }{
		{"a.go", ""},
		{"a.go:186", "L186"},
		{"a.go:12:5", "L12"},
		{"a.go:12-20", "L12-L20"},
		{"a.go#L12-L20", "L12-L20"},
		{"a.go:20-12", "L12-L20"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			want := "[`" + tc.code + "`](" + (&url.URL{Scheme: "file", Path: file, Fragment: tc.fragment}).String() + ")"
			if got := linkifyInlinePaths("`"+tc.code+"`", dir); got != want {
				t.Fatalf("得到 %q，期望 %q", got, want)
			}
		})
	}
}

func TestLinkifyInlinePathsRewritesRelativePath(t *testing.T) {
	dir := t.TempDir()
	file := writeTestFile(t, filepath.Join(dir, "docs", "plans", "x.md"))
	got := linkifyInlinePaths("见 `docs/plans/x.md` 与 `docs/none.md`。", dir)
	want := "见 [`docs/plans/x.md`](" + (&url.URL{Scheme: "file", Path: file}).String() + ") 与 `docs/none.md`。"
	if got != want {
		t.Fatalf("linkifyInlinePaths = %q, want %q", got, want)
	}
}

func TestLinkifyInlinePathsKeepsNonPaths(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "docs", "my file.md"))
	writeTestFile(t, filepath.Join(dir, "docs", "x.md"))
	cases := []struct {
		name, text string
	}{
		{"不存在的路径", "看 `docs/none.md`。"},
		{"含空格", "看 `docs/my file.md`。"},
		{"网址", "看 `https://example.com/x`。"},
		{"围栏代码块", "```\n`docs/x.md`\n```"},
		{"已在链接中", "看 [`docs/x.md`](https://example.com/x)。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkifyInlinePaths(tc.text, dir); got != tc.text {
				t.Fatalf("linkifyInlinePaths = %q, want 原文 %q", got, tc.text)
			}
		})
	}
	t.Run("无工作目录", func(t *testing.T) {
		text := "看 `docs/x.md`。"
		if got := linkifyInlinePaths(text, ""); got != text {
			t.Fatalf("linkifyInlinePaths = %q, want 原文 %q", got, text)
		}
	})
}

func TestLinkifyInlinePathsAbsoluteAndHome(t *testing.T) {
	dir := t.TempDir()
	file := writeTestFile(t, filepath.Join(dir, "x.md"))
	text := "看 `" + file + "`。"
	got := linkifyInlinePaths(text, t.TempDir())
	want := "看 [`" + file + "`](" + (&url.URL{Scheme: "file", Path: file}).String() + ")。"
	if got != want {
		t.Fatalf("linkifyInlinePaths = %q, want %q", got, want)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取用户主目录")
	}
	got = linkifyInlinePaths("看 `~/`。", "")
	if !strings.Contains(got, "](file://") {
		t.Fatalf("linkifyInlinePaths(~/ ) = %q，未改写成链接", got)
	}
	if !strings.Contains(got, (&url.URL{Scheme: "file", Path: filepath.Clean(home)}).String()) {
		t.Fatalf("linkifyInlinePaths(~/ ) = %q, 目标不是用户主目录", got)
	}
}

func TestLinkifyMessageTextSkipsPlainText(t *testing.T) {
	if got := linkifyMessageText(nil, "没有行内代码"); got != "没有行内代码" {
		t.Fatalf("linkifyMessageText = %q", got)
	}
	file := writeTestFile(t, filepath.Join(t.TempDir(), "x.md"))
	text := "看 `" + file + "`。"
	first := linkifyMessageText(nil, text)
	if !strings.Contains(first, "](file://") {
		t.Fatalf("linkifyMessageText = %q，绝对路径未改写成链接", first)
	}
	if second := linkifyMessageText(nil, text); second != first {
		t.Fatalf("缓存结果不一致：%q != %q", second, first)
	}
}
