package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"mcode/internal/fsutil"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

func TestLocalFileLinkPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "月报 (2026年10月).docx")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	javaDir := filepath.Join(dir, "中文 目录")
	if err := os.MkdirAll(javaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	javaFile := filepath.Join(javaDir, "Example.java")
	if err := os.WriteFile(javaFile, []byte("package example;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, dest string
		want       bool
		wantPath   string
	}{
		{"绝对路径", file, true, file},
		{"file URL 百分号编码", fsutil.FileURL(file).String(), true, file},
		{"位置后缀", file + "#L12", true, file},
		{"URL 行号范围", fsutil.FileURL(file).String() + "#L12-L20", true, file},
		{"中文空格路径冒号行号 173", javaFile + ":173", true, javaFile},
		{"中文空格路径冒号行号 269", javaFile + ":269", true, javaFile},
		{"file URL 中文空格路径冒号行号", fsutil.FileURL(javaFile).String() + ":173", true, javaFile},
		{"冒号行号带列号", javaFile + ":173:25", true, javaFile},
		{"冒号行号范围", javaFile + ":100-200", true, javaFile},
		{"不存在", filepath.Join(dir, "none.docx"), false, ""},
		{"网页", "https://example.com/a", false, ""},
		{"相对路径", "a.docx", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := localFileLinkPath(tc.dest)
			if ok != tc.want {
				t.Fatalf("localFileLinkPath(%q) ok = %v, want %v", tc.dest, ok, tc.want)
			}
			if ok && got != tc.wantPath {
				t.Fatalf("path = %q, want %q", got, tc.wantPath)
			}
		})
	}
}

func TestLocalFileLinkLineRange(t *testing.T) {
	for _, tc := range []struct {
		dest        string
		first, last int
	}{
		{"file:///a.go#L12", 12, 12},
		{"/a.go#L12-L20", 12, 20},
		{"/a.go#L20-L12", 12, 20},
		{"/a.go:173", 173, 173},
		{"/a.go:269", 269, 269},
		{"file:///a.go:173", 173, 173},
		{"file:///workspace/中文%20目录/Example.java:173", 173, 173},
		{filepath.Join(t.TempDir(), "中文 目录", "src", "main", "java", "example", "Example.java") + ":173", 173, 173},
		{"/a.go:173:25", 173, 173},
		{"/a.go:10-20", 10, 20},
		{"/a.go:20-10", 10, 20},
		{"/a.go", 0, 0},
		{"/a.go#L0", 0, 0},
		{"/a.go#L12-L0", 0, 0},
		{"/a.go#L12-20", 0, 0},
		{"/a.go#L12-L20x", 0, 0},
		{"/a.go#L999999999999999999999999", 0, 0},
		{"/a.go#L12#L20", 0, 0},
		{"/a.go:0", 0, 0},
		{"/a.go:12-0", 0, 0},
		{"/a.go:12-20x", 0, 0},
	} {
		t.Run(tc.dest, func(t *testing.T) {
			first, last := localFileLinkLineRange(tc.dest)
			if first != tc.first || last != tc.last {
				t.Fatalf("得到 %d, %d，期望 %d, %d", first, last, tc.first, tc.last)
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

// TestLocalPreviewTextLines 校验取行函数：保留全部行、二进制内容返回空、长行截断。
func TestLocalPreviewTextLines(t *testing.T) {
	var many strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&many, "第%d行\n", i)
	}
	lines := localPreviewTextLines([]byte(many.String()))
	if len(lines) != 13 {
		t.Fatalf("行数 = %d，期望 13", len(lines))
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

// TestLocalFilePreviewLineWindow 校验窗口边界。
func TestLocalFilePreviewLineWindow(t *testing.T) {
	lines := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	for _, tc := range []struct {
		start int
		want  []string
	}{
		{1, lines[:8]},
		{0, lines[:8]},
		{3, lines[2:10]},
		{9, lines[8:]},
		{11, nil},
	} {
		if got := localPreviewLineWindow(lines, tc.start); !slices.Equal(got, tc.want) {
			t.Errorf("起始行 %d：得到 %v，期望 %v", tc.start, got, tc.want)
		}
	}
}

// TestLocalFileLinkContextMenu 校验右键本地文件链接弹出文件菜单，右键普通文字不弹。
func TestLocalFileLinkContextMenu(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("第一行\n第二行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 真实消息里两种写法都会出现：绝对路径与 file:// URL。
	for _, dest := range []string{file, fsutil.FileURL(file).String()} {
		t.Run(dest, func(t *testing.T) {
			// 链接文字不能是整段文字：Tester.RightClick 取第一个文字相同的元素，
			// 段落文字与链接文字相同时会点到段落的宽标签上，落点在链接外。
			src := "附件见 [报告](" + dest + ")，请查收。\n\n普通文字\n"
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
			for _, want := range []string{revealInFileManagerLabel(), "复制路径"} {
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
				if item == revealInFileManagerLabel() || item == "复制路径" {
					t.Fatalf("普通文字不应弹出文件菜单: %v", menu)
				}
			}
		})
	}
}

// TestLocalFileLinkMarkdownIntegration 验证 [文件](<中文绝对路径:173>) 保留并解析正确目标且可用于文件菜单与预览入口，
// 并覆盖原有真实带冒号文件名的优先匹配行为。
func TestLocalFileLinkMarkdownIntegration(t *testing.T) {
	dir := t.TempDir()
	javaDir := filepath.Join(dir, "中文 目录")
	if err := os.MkdirAll(javaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	javaFile := filepath.Join(javaDir, "Example.java")
	var javaContent strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&javaContent, "// 第 %d 行代码\n", i)
	}
	if err := os.WriteFile(javaFile, []byte(javaContent.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	rawDest := javaFile + ":173"
	src := fmt.Sprintf("实现位置：[文件](<%s>)\n", rawDest)

	// 1. Markdown parser 层面验证：<中文绝对路径:173> 作为 destination 被原样保留
	doc := parser.New(parser.Default)
	doc.Set(src)
	var parsedDests []string
	for i := 1; i < doc.Len(); i++ {
		for _, node := range doc.Inlines(i) {
			if node.Kind == parser.Link {
				parsedDests = append(parsedDests, node.Dest)
			}
		}
	}
	if !slices.Contains(parsedDests, rawDest) {
		t.Fatalf("Markdown parser 未能解析并保留目标 %q，得到: %v", rawDest, parsedDests)
	}

	// 2. ui.Tester 验证：渲染后右键弹出包含文件项的上下文菜单（文件菜单入口，不触发外部程序）
	o := &markdown.Options{}
	installLocalFileLinkHook(o, &chatState{})
	tt := ui.NewTester(func(c *ui.Context) {
		markdown.View(c, src, o)
	}, 900, 400)

	if err := tt.RightClick("文件"); err != nil {
		t.Fatalf("右键链接失败: %v", err)
	}
	tt.Frame()
	menu := tt.Menu()
	for _, want := range []string{revealInFileManagerLabel(), "复制路径", "侧边栏预览"} {
		if !slices.Contains(menu, want) {
			t.Fatalf("链接菜单缺少 %q: %v", want, menu)
		}
	}
	tt.CloseMenu()

	// 3. 悬停预览入口验证：解析出物理文件路径、正确行号，并成功生成预览卡片
	resolved, ok := localFileLinkPath(rawDest)
	if !ok || resolved != javaFile {
		t.Fatalf("localFileLinkPath(%q) = %q, %v，期望 %q, true", rawDest, resolved, ok, javaFile)
	}
	first, last := localFileLinkLineRange(rawDest)
	if first != 173 || last != 173 {
		t.Fatalf("localFileLinkLineRange(%q) = %d, %d，期望 173, 173", rawDest, first, last)
	}
	previewFn := o.Preview(nil, rawDest)
	if previewFn == nil {
		t.Fatalf("o.Preview(nil, %q) 应返回卡片填充函数", rawDest)
	}

	// 4. 保留原有冒号真实文件名优先行为
	t.Run("真实冒号文件名优先", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows 文件名不允许冒号")
		}
		colonFile := filepath.Join(dir, "Service.java:173")
		plainFile := filepath.Join(dir, "Service.java")
		if err := os.WriteFile(colonFile, []byte("colon content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plainFile, []byte("plain content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, ok := localFileLinkPath(colonFile); !ok || got != colonFile {
			t.Fatalf("真实带冒号文件存在时应优先匹配自身，得到 %q, %v，期望 %q", got, ok, colonFile)
		}
		if err := os.Remove(colonFile); err != nil {
			t.Fatal(err)
		}
		if got, ok := localFileLinkPath(colonFile); !ok || got != plainFile {
			t.Fatalf("带冒号文件不存在时应回退到物理文件，得到 %q, %v，期望 %q", got, ok, plainFile)
		}
	})
}
