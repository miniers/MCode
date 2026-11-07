package mcodeui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
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

func TestInstallLocalFileLinkHook(t *testing.T) {
	s := &chatState{}
	o := &markdown.Options{}
	installLocalFileLinkHook(o, s)
	if o.OnLink == nil || o.LinkMenu == nil {
		t.Fatal("应安装链接点击和菜单钩子")
	}

	clicked := false
	o = &markdown.Options{OnLink: func(string) { clicked = true }}
	installLocalFileLinkHook(o, s)
	if o.LinkMenu == nil {
		t.Fatal("已有点击钩子时仍应安装菜单钩子")
	}
	o.OnLink("")
	if !clicked {
		t.Fatal("不应覆盖已有点击钩子")
	}

	called := false
	o = &markdown.Options{LinkMenu: func(*ui.Menu, string) { called = true }}
	installLocalFileLinkHook(o, s)
	if o.OnLink == nil {
		t.Fatal("已有菜单钩子时仍应安装点击钩子")
	}
	o.LinkMenu(nil, "")
	if !called {
		t.Fatal("不应覆盖已有菜单钩子")
	}
}
