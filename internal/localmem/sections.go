package localmem

import "strings"

// Section 表示二级标题及其正文；行号从零开始，EndLine 为开区间。
type Section struct {
	Heading   string
	Body      string
	StartLine int
	EndLine   int
}

// fenceState 跟踪代码围栏的开闭：char 为 0 表示不在围栏内，length 为围栏字符个数。
type fenceState struct {
	char   byte
	length int
}

// scan 用一行文本更新围栏状态，并报告该行本身是否为围栏行。
func (f *fenceState) scan(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	indent := len(line) - len(trimmed)
	if indent > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return false
	}
	c := trimmed[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return false
	}
	if f.char == 0 {
		f.char, f.length = c, n
	} else if f.char == c && n >= f.length && strings.TrimSpace(trimmed[n:]) == "" {
		f.char = 0
	}
	return true
}

// open 报告当前是否仍在代码围栏内。
func (f *fenceState) open() bool { return f.char != 0 }

// openFence 返回文本末尾仍未闭合的围栏字符与长度；没有未闭合围栏时返回 0。
func openFence(text string) (byte, int) {
	var f fenceState
	for _, line := range strings.SplitAfter(text, "\n") {
		f.scan(line)
	}
	return f.char, f.length
}

// ListSections 按二级标题切分文档，忽略代码围栏内的标题与空标题。
func ListSections(text string) []Section {
	lines := strings.SplitAfter(text, "\n")
	var sections []Section
	var fence fenceState
	for i, line := range lines {
		if fence.scan(line) || fence.open() {
			continue
		}
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		if heading == "" {
			continue
		}
		if len(sections) > 0 {
			sections[len(sections)-1].EndLine = i
		}
		sections = append(sections, Section{Heading: heading, StartLine: i, EndLine: len(lines)})
	}
	for i := range sections {
		s := &sections[i]
		s.Body = strings.TrimSpace(strings.Join(lines[s.StartLine+1:s.EndLine], ""))
	}
	return sections
}

// normalizeBody 规整待写入的小节正文：代码围栏之外的行首 "## " 降级为 "### "，
// 以免正文里的二级标题把这条记忆切成新小节；围栏未闭合时在末尾补上闭合围栏。
func normalizeBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	lines := strings.SplitAfter(body, "\n")
	out := make([]string, 0, len(lines)+1)
	var fence fenceState
	for _, line := range lines {
		if !fence.scan(line) && !fence.open() && strings.HasPrefix(line, "## ") {
			line = "#" + line
		}
		out = append(out, line)
	}
	if fence.open() {
		out = append(out, strings.Repeat(string(fence.char), fence.length)+"\n")
	}
	return strings.TrimSpace(strings.Join(out, ""))
}

// ReplaceSection 把小节的标题行与正文一起换掉，其余文本保持不变；找不到小节时原样返回。
func ReplaceSection(text, oldHeading, newHeading, body string) string {
	lines := strings.SplitAfter(text, "\n")
	for _, s := range ListSections(text) {
		if s.Heading != oldHeading {
			continue
		}
		prefix := strings.Join(lines[:s.StartLine], "")
		suffix := strings.Join(lines[s.EndLine:], "")
		replacement := "## " + newHeading + "\n\n" + normalizeBody(body) + "\n\n"
		if suffix == "" {
			replacement = strings.TrimRight(replacement, "\n") + "\n"
		}
		return prefix + replacement + suffix
	}
	return text
}

// UpsertSection 替换指定小节的正文，或在文末追加小节。
func UpsertSection(text, heading, body string) string {
	for _, s := range ListSections(text) {
		if s.Heading == heading {
			return ReplaceSection(text, heading, heading, body)
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" && !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}
	// 文档末尾的围栏还开着时先补上闭合围栏，否则新小节会被当成围栏里的内容。
	if c, n := openFence(text); c != 0 {
		text += strings.Repeat(string(c), n) + "\n\n"
	}
	return text + "## " + heading + "\n\n" + normalizeBody(body) + "\n"
}

// RemoveSection 删除指定小节，保留其余文本。
func RemoveSection(text, heading string) string {
	lines := strings.SplitAfter(text, "\n")
	for _, s := range ListSections(text) {
		if s.Heading == heading {
			return strings.Join(lines[:s.StartLine], "") + strings.Join(lines[s.EndLine:], "")
		}
	}
	return text
}
