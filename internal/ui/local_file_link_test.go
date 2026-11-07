package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestInstallLocalFileLinkPreview 校验悬停预览钩子的安装与不覆盖已有钩子。
func TestInstallLocalFileLinkPreview(t *testing.T) {
	s := &chatState{}
	o := &markdown.Options{}
	installLocalFileLinkHook(o, s)
	if o.Preview == nil {
		t.Fatal("应安装链接悬停预览钩子")
	}

	called := false
	o = &markdown.Options{Preview: func(*ui.Context, string) func(ui.Element) {
		called = true
		return nil
	}}
	installLocalFileLinkHook(o, s)
	o.Preview(nil, "")
	if !called {
		t.Fatal("不应覆盖已有的预览钩子")
	}
}

// TestLocalFileLinkPreviewDest 校验只有本地图片与文本文件才有内容预览，网页链接等返回 nil。
func TestLocalFileLinkPreviewDest(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("第一行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, dest string
		want       bool
	}{
		{"网页", "https://example.com/a", false},
		{"文本文件", file, true},
		{"文本文件带位置后缀", file + "#L12", true},
		{"图片文件", filepath.Join(dir, "shot.png"), true},
		{"二进制文件", filepath.Join(dir, "report.pdf"), false},
		{"不存在的扩展名", filepath.Join(dir, "data.bin"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localFileLinkPreview(nil, tc.dest) != nil
			if got != tc.want {
				t.Fatalf("localFileLinkPreview(%q) 有内容 = %v，期望 %v", tc.dest, got, tc.want)
			}
		})
	}
}

// TestLocalPreviewTextLines 校验取行函数：只取前几行、二进制内容返回空、长行截断。
func TestLocalPreviewTextLines(t *testing.T) {
	var many strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&many, "第%d行\n", i)
	}
	lines := localPreviewTextLines([]byte(many.String()))
	if len(lines) != localPreviewLineCount {
		t.Fatalf("行数 = %d，期望 %d", len(lines), localPreviewLineCount)
	}
	if lines[0] != "第1行" || lines[localPreviewLineCount-1] != "第8行" {
		t.Fatalf("取到的行不对: %v", lines)
	}

	if got := localPreviewTextLines([]byte("第一行\n\x00二进制")); len(got) != 0 {
		t.Fatalf("含 NUL 的内容应返回空，得到 %v", got)
	}

	long := strings.Repeat("a", localPreviewLineRunes+10)
	lines = localPreviewTextLines([]byte(long))
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "…") || !strings.HasPrefix(lines[0], strings.Repeat("a", localPreviewLineRunes)) {
		t.Fatalf("长行应截断到 %d 个字符并补省略号，得到 %q", localPreviewLineRunes, lines)
	}
}

// TestLocalFileLinkContextMenu 校验右键本地文件链接弹出文件菜单，右键普通文字不弹。
func TestLocalFileLinkContextMenu(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("第一行\n第二行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "[报告](" + file + ")\n\n普通文字\n"
	o := &markdown.Options{}
	installLocalFileLinkHook(o, &chatState{})
	tt := ui.NewTester(func(c *ui.Context) {
		markdown.View(c, src, o)
	}, 900, 400)

	if err := tt.RightClick("报告"); err != nil {
		t.Fatalf("右键链接失败: %v", err)
	}
	tt.Frame()
	menu := tt.Menu()
	for _, want := range []string{"在访达中显示", "复制路径"} {
		if !slices.Contains(menu, want) {
			t.Fatalf("链接菜单缺少 %q: %v", want, menu)
		}
	}
	tt.CloseMenu()

	if err := tt.RightClick("普通文字"); err != nil {
		t.Fatalf("右键普通文字失败: %v", err)
	}
	tt.Frame()
	menu = tt.Menu()
	for _, item := range menu {
		if item == "在访达中显示" || item == "复制路径" {
			t.Fatalf("普通文字不应弹出文件菜单: %v", menu)
		}
	}
}
