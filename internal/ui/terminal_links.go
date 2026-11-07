package mcodeui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// 终端输出的链接识别：把一行（或被终端宽度折行后的若干行）里的 URL 与文件路径找出来，
// 供点击打开使用。所有识别都是纯函数，列号一律是 rune 下标（0 起、左闭右开），
// 与 terminal.go 里逐字符渲染的语义一致。

// terminalLinkKind 是终端输出里可点击目标的类型。
type terminalLinkKind int

const (
	// terminalLinkURL 是 http(s) 链接，交给系统默认浏览器打开。
	terminalLinkURL terminalLinkKind = iota
	// terminalLinkPath 是文件路径，可能带 :line 或 :line:col 后缀。
	terminalLinkPath
)

// terminalLink 是一行文本里识别出的链接片段。
type terminalLink struct {
	// Start 与 End 是行内 rune 列（左闭右开）。
	Start int
	End   int
	// Raw 是原文片段，路径包含末尾的 :line(:col)。
	Raw  string
	Kind terminalLinkKind
}

// terminalURLPattern 匹配文本里的 http(s) URL；尾部标点由 trimTerminalLinkTail 去掉。
// 与 mygo 内置的 urlPattern 保持一致：不跨空白与引号，也避开 ANSI 控制字符区间。
var terminalURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>` + "`" + "\x00-\x1f]+")

// terminalPathPattern 匹配三类路径：以 / ~ ./ ../ 开头、含斜杠的相对路径、以及带 :line 的文件名。
// 末尾的 :line(:col) 作为整体匹配，避免被拆成两段；是否真是路径还要过 looksLikeTerminalPath。
var terminalPathPattern = regexp.MustCompile(
	`(?:(?:~|\.{1,2})?/[^\s"'<>` + "`" + `\x00-\x1f]*` +
		`|[\w.@+-]+(?:/[\w.@+-]+)+/?` +
		`|[\w@+-][\w.@+-]*\.[A-Za-z0-9_]+)(?::\d+(?::\d+)?)?`)

// findTerminalLinks 识别单行文本里的 URL 与文件路径，按出现顺序返回。
// URL 优先：与 URL 区间重叠的路径候选一律丢弃（例如 URL 里的 //host）。
func findTerminalLinks(line string) []terminalLink {
	if line == "" {
		return nil
	}
	type span struct{ start, end int }
	var urlSpans []span
	var links []terminalLink
	overlapsURL := func(start, end int) bool {
		for _, sp := range urlSpans {
			if start < sp.end && sp.start < end {
				return true
			}
		}
		return false
	}

	for _, m := range terminalURLPattern.FindAllStringIndex(line, -1) {
		raw := trimTerminalLinkTail(line[m[0]:m[1]])
		if raw == "" {
			continue
		}
		start := len([]rune(line[:m[0]]))
		end := start + len([]rune(raw))
		urlSpans = append(urlSpans, span{start, end})
		links = append(links, terminalLink{Start: start, End: end, Raw: raw, Kind: terminalLinkURL})
	}

	for _, m := range terminalPathPattern.FindAllStringIndex(line, -1) {
		start := len([]rune(line[:m[0]]))
		if overlapsURL(start, len([]rune(line[:m[1]]))) {
			continue
		}
		raw := trimTerminalLinkTail(line[m[0]:m[1]])
		body, lineNo, _ := splitTerminalLinkPos(raw)
		if !looksLikeTerminalPath(body, lineNo) {
			continue
		}
		end := start + len([]rune(raw))
		if overlapsURL(start, end) {
			continue
		}
		links = append(links, terminalLink{Start: start, End: end, Raw: raw, Kind: terminalLinkPath})
	}

	sort.SliceStable(links, func(i, j int) bool { return links[i].Start < links[j].Start })
	return links
}

// trimTerminalLinkTail 去掉句子尾部的标点，以及 URL 自己没有配对的右括号，
// 让「见 https://example.com/a.(」这类输出只把真正属于链接的部分算进识别结果。
func trimTerminalLinkTail(raw string) string {
	for len(raw) > 0 {
		switch last := raw[len(raw)-1]; last {
		case '.', ',', ';', ':', '!', '?', '\'', '"', '`':
			raw = raw[:len(raw)-1]
			continue
		case ')', ']', '}':
			open := map[byte]string{')': "(", ']': "[", '}': "{"}[last]
			if strings.Count(raw, open) < strings.Count(raw, string(last)) {
				raw = raw[:len(raw)-1]
				continue
			}
		}
		return raw
	}
	return raw
}

// splitTerminalLinkPos 从路径尾部剥离 :line 与 :col；没有时行列号为 0。
func splitTerminalLinkPos(raw string) (body string, line, col int) {
	if head, n, ok := cutTerminalLinkNumber(raw); ok {
		if head2, n2, ok2 := cutTerminalLinkNumber(head); ok2 {
			return head2, n2, n
		}
		return head, n, 0
	}
	return raw, 0, 0
}

// cutTerminalLinkNumber 去掉形如 ":123" 的尾部数字后缀。
func cutTerminalLinkNumber(s string) (head string, n int, ok bool) {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return s, 0, false
	}
	digits := s[i+1:]
	for _, r := range digits {
		if r < '0' || r > '9' {
			return s, 0, false
		}
	}
	parsed, err := strconv.Atoi(digits)
	if err != nil || parsed <= 0 {
		return s, 0, false
	}
	head = s[:i]
	// Windows 盘符（C:）不是行号。
	if len(head) == 1 && isTerminalAlpha(head[0]) {
		return s, 0, false
	}
	return head, parsed, true
}

// isTerminalAlpha 判断字节是否为 ASCII 字母。
func isTerminalAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// looksLikeTerminalPath 过滤正则的误报：必须含字母，并且含路径分隔符（或带 :line 后缀）。
// 这样「main.go」这种裸文件名、「v1.2」「example.com」这类普通单词不会被当成可点击路径。
func looksLikeTerminalPath(body string, line int) bool {
	if body == "" || body == "." || body == ".." {
		return false
	}
	// "//host/path" 是 URL 片段，不是文件路径。
	if strings.HasPrefix(body, "//") || strings.HasPrefix(body, `\\`) {
		return false
	}
	hasLetter := false
	for _, r := range body {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return false
	}
	hasSep := strings.ContainsAny(body, `/\~`)
	return hasSep || line > 0
}

// classifyTerminalLink 判断一段被点击的文本是 URL 还是文件路径；两者都不像时 ok 为 false。
// mygo 的 OpenLink 回调对 URL 与路径走同一个入口，需要在这里区分：URL 让 mygo 用系统浏览器打开。
func classifyTerminalLink(raw string) (terminalLinkKind, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return terminalLinkPath, false
	}
	if m := terminalURLPattern.FindString(trimmed); m != "" {
		// 调用方传来的文本可能带尾部标点（正则把标点一起吞了），两种形式都当 URL。
		if trimmedURL := trimTerminalLinkTail(trimmed); trimmedURL == trimmed || trimmedURL == trimTerminalLinkTail(m) {
			return terminalLinkURL, true
		}
	}
	body, line, _ := splitTerminalLinkPos(trimTerminalLinkTail(trimmed))
	if looksLikeTerminalPath(body, line) {
		return terminalLinkPath, true
	}
	return terminalLinkPath, false
}

// resolveTerminalLinkTarget 把终端里点到的原始文本解析成绝对路径与行列号。
// 相对路径基于终端标签的 dir（cwd）解析，路径必须真实存在才返回 ok；URL 一律返回 ok=false。
func resolveTerminalLinkTarget(cwd, raw string) (path string, line, col int, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", 0, 0, false
	}
	if kind, ok := classifyTerminalLink(trimmed); !ok || kind == terminalLinkURL {
		return "", 0, 0, false
	}
	body, line, col := splitTerminalLinkPos(trimTerminalLinkTail(trimmed))
	if !looksLikeTerminalPath(body, line) {
		return "", 0, 0, false
	}
	target := body
	switch {
	case body == "~" || strings.HasPrefix(body, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", 0, 0, false
		}
		rest := strings.TrimPrefix(body, "~/")
		if rest == "" || rest == body {
			target = home
		} else {
			target = filepath.Join(home, rest)
		}
	case filepath.IsAbs(body):
		target = body
	default:
		if cwd == "" {
			return "", 0, 0, false
		}
		target = filepath.Join(cwd, body)
	}
	target = filepath.Clean(target)
	if _, err := os.Stat(target); err != nil {
		return "", 0, 0, false
	}
	return target, line, col, true
}

// terminalOutputRow 是终端里的一行输出；Wrapped 为真表示这一行是终端按宽度软换行的结果，
// 下一行是它的续行。
type terminalOutputRow struct {
	Text    string
	Wrapped bool
}

// terminalLinkSpan 是折行拼接识别后再映射回单行的链接片段。
type terminalLinkSpan struct {
	// Row 是行下标（对应传入的 rows）。
	Row int
	// Start 与 End 是该行内的 rune 列（左闭右开）。
	Start int
	End   int
	// Link 是拼接后识别出的完整链接。
	Link terminalLink
}

// findTerminalLinksWrapped 先把被软换行切开的多行拼回整行再识别链接，然后把结果映射回各行片段。
// 只有长 URL 或长路径被折行切断时才需要它：单行识别会把两半都判成无效片段。
func findTerminalLinksWrapped(rows []terminalOutputRow) []terminalLinkSpan {
	var spans []terminalLinkSpan
	for i := 0; i < len(rows); {
		j := i
		var b strings.Builder
		// offsets[k] 是第 i+k 行在拼接文本里的起始 rune 列。
		offsets := []int{0}
		for {
			b.WriteString(rows[j].Text)
			next := len([]rune(b.String()))
			if !rows[j].Wrapped || j+1 >= len(rows) {
				break
			}
			j++
			offsets = append(offsets, next)
		}
		joined := b.String()
		rowRunes := make([]int, 0, j-i+1)
		for k := i; k <= j; k++ {
			rowRunes = append(rowRunes, len([]rune(rows[k].Text)))
		}
		for _, link := range findTerminalLinks(joined) {
			for k := range rowRunes {
				rowStart := offsets[k]
				rowEnd := rowStart + rowRunes[k]
				start := max(link.Start, rowStart)
				end := min(link.End, rowEnd)
				if end <= start {
					continue
				}
				spans = append(spans, terminalLinkSpan{
					Row:   i + k,
					Start: start - rowStart,
					End:   end - rowStart,
					Link:  link,
				})
			}
		}
		i = j + 1
	}
	return spans
}
