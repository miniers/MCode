package mcodeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFindTerminalLinksURLTail 验证 URL 尾部的标点、未配对的右括号都不算进链接。
func TestFindTerminalLinksURLTail(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"逗号结尾", "见 https://example.com/a?b=1, 后继续", "https://example.com/a?b=1"},
		{"括号包裹", "(https://x.dev/y).", "https://x.dev/y"},
		{"分号结尾与大写协议", "打开 HTTP://Example.COM/z; 结束", "HTTP://Example.COM/z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			links := findTerminalLinks(tc.line)
			if len(links) != 1 {
				t.Fatalf("想要 1 个链接，实际 %d 个: %+v", len(links), links)
			}
			if links[0].Kind != terminalLinkURL {
				t.Fatalf("想要 URL 类型，实际 %v", links[0].Kind)
			}
			if links[0].Raw != tc.want {
				t.Fatalf("链接原文 = %q，想要 %q", links[0].Raw, tc.want)
			}
			runes := []rune(tc.line)
			if got := string(runes[links[0].Start:links[0].End]); got != tc.want {
				t.Fatalf("列区间 %d-%d 取到 %q，想要 %q", links[0].Start, links[0].End, got, tc.want)
			}
		})
	}
}

// TestFindTerminalLinksPathWithLineCol 验证 a/b.go:12:3 被整体识别，并解析出行列号。
func TestFindTerminalLinksPathWithLineCol(t *testing.T) {
	const line = "at a/b.go:12:3: boom"
	links := findTerminalLinks(line)
	if len(links) != 1 {
		t.Fatalf("想要 1 个链接，实际 %d 个: %+v", len(links), links)
	}
	link := links[0]
	if link.Kind != terminalLinkPath {
		t.Fatalf("想要路径类型，实际 %v", link.Kind)
	}
	if link.Raw != "a/b.go:12:3" {
		t.Fatalf("链接原文 = %q", link.Raw)
	}
	body, lineNo, col := splitTerminalLinkPos(link.Raw)
	if body != "a/b.go" || lineNo != 12 || col != 3 {
		t.Fatalf("解析结果 = (%q,%d,%d)，想要 (a/b.go,12,3)", body, lineNo, col)
	}

	cwd := t.TempDir()
	target := filepath.Join(cwd, "a", "b.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, gotLine, gotCol, ok := resolveTerminalLinkTarget(cwd, link.Raw)
	if !ok {
		t.Fatalf("相对路径应当能解析到已存在的文件")
	}
	if path != target || gotLine != 12 || gotCol != 3 {
		t.Fatalf("解析结果 = (%q,%d,%d)，想要 (%q,12,3)", path, gotLine, gotCol, target)
	}
}

// TestFindTerminalLinksParenPath 验证 (x.ts:5) 形式只把 x.ts:5 当作链接。
func TestFindTerminalLinksParenPath(t *testing.T) {
	links := findTerminalLinks("(x.ts:5)")
	if len(links) != 1 {
		t.Fatalf("想要 1 个链接，实际 %d 个: %+v", len(links), links)
	}
	if links[0].Raw != "x.ts:5" || links[0].Start != 1 || links[0].End != 7 {
		t.Fatalf("链接 = %+v，想要 x.ts:5 位于 [1,7)", links[0])
	}

	body, lineNo, col := splitTerminalLinkPos(links[0].Raw)
	if body != "x.ts" || lineNo != 5 || col != 0 {
		t.Fatalf("解析结果 = (%q,%d,%d)，想要 (x.ts,5,0)", body, lineNo, col)
	}

	cwd := t.TempDir()
	target := filepath.Join(cwd, "x.ts")
	if err := os.WriteFile(target, []byte("export {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, gotLine, _, ok := resolveTerminalLinkTarget(cwd, links[0].Raw)
	if !ok || path != target || gotLine != 5 {
		t.Fatalf("解析结果 = (%q,%d,%v)，想要 (%q,5,true)", path, gotLine, ok, target)
	}
}

// TestFindTerminalLinksRejectsSpaces 验证带空格的路径不会被当成一个链接。
func TestFindTerminalLinksRejectsSpaces(t *testing.T) {
	lines := []string{
		"打开 /Users/me/My Docs/note.md 看看",
		"cat a b/c.go 失败",
		"错误: 找不到 ./my dir/file.txt:12",
	}
	for _, line := range lines {
		for _, link := range findTerminalLinks(line) {
			if strings.ContainsAny(link.Raw, " \t") {
				t.Fatalf("行 %q 里识别出了带空格的链接 %q", line, link.Raw)
			}
		}
	}
}

// TestFindTerminalLinksWrapped 验证被终端宽度软换行切断的长路径能拼回来，并映射回各行片段。
func TestFindTerminalLinksWrapped(t *testing.T) {
	const first = "见 ./internal/ui/te"
	const second = "rminal.go:42:9 结束"
	const full = "./internal/ui/terminal.go:42:9"
	rows := []terminalOutputRow{
		{Text: first, Wrapped: true},
		{Text: second},
	}
	spans := findTerminalLinksWrapped(rows)
	if len(spans) != 2 {
		t.Fatalf("想要 2 个映射片段（每个物理行一个），实际 %d 个: %+v", len(spans), spans)
	}
	for _, span := range spans {
		if span.Link.Raw != full {
			t.Fatalf("拼接后链接 = %q，想要 %q", span.Link.Raw, full)
		}
	}
	firstRunes := utf8.RuneCountInString(first)
	start := utf8.RuneCountInString("见 ")
	if spans[0].Row != 0 || spans[0].Start != start || spans[0].End != firstRunes {
		t.Fatalf("第一行片段 = %+v，想要 row 0 [%d,%d)", spans[0], start, firstRunes)
	}
	if spans[1].Row != 1 || spans[1].Start != 0 {
		t.Fatalf("第二行片段 = %+v，想要 row 1 从 0 开始", spans[1])
	}
	// 两段拼起来正好是整个链接。
	if got := spans[0].End - spans[0].Start + spans[1].End - spans[1].Start; got != utf8.RuneCountInString(full) {
		t.Fatalf("两段长度合计 %d，想要 %d", got, utf8.RuneCountInString(full))
	}

	// 不做折行拼接时，两行各自都识别不出完整链接。
	for _, row := range rows {
		for _, link := range findTerminalLinks(row.Text) {
			if link.Raw == full {
				t.Fatalf("单行识别不该得到完整链接 %q", full)
			}
		}
	}
}

// TestFindTerminalLinksWrappedNotJoined 验证 Wrapped 为假的行不会被拼到下一行。
func TestFindTerminalLinksWrappedNotJoined(t *testing.T) {
	rows := []terminalOutputRow{
		{Text: "./internal/ui/te"},
		{Text: "rminal.go:42:9"},
	}
	spans := findTerminalLinksWrapped(rows)
	for _, span := range spans {
		if span.Link.Raw == "./internal/ui/terminal.go:42:9" {
			t.Fatalf("没有软换行标记却被拼接：%+v", span)
		}
	}
}

// TestResolveTerminalLinkTargetRejects 验证不存在文件、URL 与纯单词都不算可点击目标。
func TestResolveTerminalLinkTargetRejects(t *testing.T) {
	cwd := t.TempDir()
	cases := []string{
		"./missing.go:1",
		"https://example.com/a",
		"main.go",
		"v1.2",
		"",
	}
	for _, raw := range cases {
		if path, _, _, ok := resolveTerminalLinkTarget(cwd, raw); ok {
			t.Fatalf("%q 不该解析成功，得到 %q", raw, path)
		}
	}
}

// TestClassifyTerminalLink 验证 URL 与路径的分类：OpenLink 回调要靠它决定是否交回 mygo。
func TestClassifyTerminalLink(t *testing.T) {
	if kind, ok := classifyTerminalLink("https://example.com/x"); !ok || kind != terminalLinkURL {
		t.Fatalf("URL 分类 = (%v,%v)", kind, ok)
	}
	if kind, ok := classifyTerminalLink("./internal/ui/terminal.go:12:3"); !ok || kind != terminalLinkPath {
		t.Fatalf("路径分类 = (%v,%v)", kind, ok)
	}
	if _, ok := classifyTerminalLink("随便一段话"); ok {
		t.Fatal("普通文本不该被识别成链接")
	}
}
