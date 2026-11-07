package swarm

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Credibility 记录汇报中代码引用的可信度核验统计。
type Credibility struct {
	Cited       int      `json:"cited"`
	Verified    int      `json:"verified"`
	Unverified  []string `json:"unverified,omitempty"`
	CitedPaths  []string `json:"-"`
	ReadChecked bool     `json:"read_checked,omitempty"`
	NotRead     []string `json:"not_read,omitempty"`
}

// Summary 生成可信度摘要文本。当 Cited==0 时返回空字符串；
// 否则返回格式形如 "引用 V/C 核实"，若存在不可核实项则追加 "，不可核实：a, b"（最多列出 3 项）。
// 当 Cited>0 时，根据读取记录追加：
// - 若 ReadChecked 且 NotRead 为空，追加 "，读取一致"；
// - 若 ReadChecked 且 NotRead 非空，追加 "，未读却引用：a, b"（最多列出 3 项）；
// - 若 !ReadChecked，追加 "，读取记录不可用"。
func (c Credibility) Summary() string {
	if c.Cited == 0 {
		return ""
	}
	res := fmt.Sprintf("引用 %d/%d 核实", c.Verified, c.Cited)
	if len(c.Unverified) > 0 {
		limit := len(c.Unverified)
		if limit > 3 {
			limit = 3
		}
		res += fmt.Sprintf("，不可核实：%s", strings.Join(c.Unverified[:limit], ", "))
	}
	if !c.ReadChecked {
		res += "，读取记录不可用"
	} else if len(c.NotRead) == 0 {
		res += "，读取一致"
	} else {
		limit := len(c.NotRead)
		if limit > 3 {
			limit = 3
		}
		res += fmt.Sprintf("，未读却引用：%s", strings.Join(c.NotRead[:limit], ", "))
	}
	return res
}

// ApplyReadTrace 根据子会话的读取文件记录核对代码引用：
// 当 len(paths)==0 时保持 ReadChecked 为 false 并直接返回；
// 否则置 ReadChecked 为 true，并将未在读取记录中的引用路径记录在 NotRead 中（匹配规则：相等，或任一方以 '/' + 另一方结尾）。
func (c *Credibility) ApplyReadTrace(paths []string) {
	if len(paths) == 0 {
		return
	}
	c.ReadChecked = true
	var notRead []string
	for _, cp := range c.CitedPaths {
		matched := false
		for _, rp := range paths {
			if pathMatches(cp, rp) {
				matched = true
				break
			}
		}
		if !matched {
			notRead = append(notRead, cp)
		}
	}
	c.NotRead = notRead
}

func pathMatches(a, b string) bool {
	a = filepath.ToSlash(filepath.Clean(a))
	b = filepath.ToSlash(filepath.Clean(b))
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a) {
		return true
	}
	return false
}

// tokenRegex 匹配可选被反引号包裹的 path:line 引用标记；
// 行号后允许可选范围（如 path:559-561），裸形式的路径字符类排除全角标点，
// 避免 “（foo.go:12）” 这类前缀把全角括号吃进路径。
var tokenRegex = regexp.MustCompile("`([^`\\s:（）【】，；：、。「」“”‘’《》]+):(\\d+)(?:[-–~](\\d+))?`|([^\\s`\"':()\\[\\]<>,;{}（）【】，；：、。「」“”‘’《》]+):(\\d+)(?:[-–~](\\d+))?")

var (
	// pathExtRegex 要求 basename 带扩展名，过滤 “121/:129” 之类的噪声 token。
	pathExtRegex = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)
	// pathLetterRegex 要求路径至少含一个 ASCII 字母。
	pathLetterRegex = regexp.MustCompile(`[A-Za-z]`)
	// ellipsisRegex 用于切分含省略号的片段。
	ellipsisRegex = regexp.MustCompile(`\.\.\.|…`)
	// identifierRegex 提取片段中的标识符，用于放宽的标识符级匹配。
	identifierRegex = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)
	// junkPlaceholderRegex 匹配 [...], ... 之类的占位符片段（比较前先去掉空白）。
	junkPlaceholderRegex = regexp.MustCompile(`^[.…\[\]()]+$`)
	// junkPathLikeRegex 匹配整体是路径或文件名（可带 :行号/:起-止）的片段，如 swarm.go、internal/remote/capabilities.go。
	junkPathLikeRegex = regexp.MustCompile(`^[\w./\\-]+\.[A-Za-z0-9]{1,8}(:\d+(-\d+)?)?$`)
	// junkNumberingRegex 匹配纯行号或报告自编号片段，如 307-308、#12、: 5。
	junkNumberingRegex = regexp.MustCompile(`^[#:\d.\-–~ ]+$`)
)

// identifierStopWords 是标识符级回退需要忽略的常见关键字与内置词（比较时统一转小写）。
// 这些词在窗口里本就随处可见，若算作标识符会让几乎任何片段都“命中”。
var identifierStopWords = map[string]struct{}{
	"func": {}, "return": {}, "type": {}, "struct": {}, "const": {}, "var": {},
	"import": {}, "package": {}, "interface": {}, "range": {}, "else": {}, "switch": {},
	"case": {}, "default": {}, "break": {}, "continue": {}, "defer": {}, "chan": {},
	"map": {}, "select": {}, "goto": {}, "fallthrough": {}, "true": {}, "false": {},
	"nil": {}, "this": {}, "self": {}, "class": {}, "function": {}, "public": {},
	"private": {}, "static": {}, "void": {}, "int64": {}, "uint64": {}, "float64": {},
	"string": {}, "bool": {}, "byte": {}, "error": {}, "from": {}, "with": {},
	"then": {}, "elif": {}, "while": {}, "for_each": {}, "await": {}, "async": {},
	"export": {}, "extends": {}, "implements": {}, "new": {}, "null": {}, "undefined": {},
}

const (
	// maxCitationLine 是允许的行号上限，超出视为噪声。
	maxCitationLine = 1000000
	// fileIndexLimit 是路径索引收集文件数的上限。
	fileIndexLimit = 30000
	// citedLimit 是单份汇报最多解析的引用条数。
	citedLimit = 50
)

// skippedDirNames 是构建路径索引时跳过的目录名。
var skippedDirNames = map[string]struct{}{
	".git":          {},
	"node_modules":  {},
	"vendor":        {},
	"build.noindex": {},
}

// CheckCitations 解析并核实 report 中的代码引用，最多解析 50 条。
//
// 解析流程：
//  1. 每行先用 tokenRegex 找出所有 path:line（可带行范围）标记；
//  2. 每条引用同时收集两个片段候选：「右段」为本标记结束到下一标记开始（最后一条到行尾）里的第一个可用片段，
//     「左段」为上一标记结束（首条为行首）到本标记开始里的最后一个可用片段；
//     路径/文件名、占位符、纯行号等垃圾片段（见 isJunkSnippet）会被丢弃；
//  3. 路径先按原逻辑拼 root 命中，未命中再按 root 文件索引做后缀匹配（只读 root 内文件）；
//  4. 候选片段依次在 ±3 行窗口（范围引用为 [起始行-3, 结束行+3]）内做去空白子串匹配，
//     失败时退化为省略号分段匹配与整词标识符匹配；任一候选命中即视为核实，都不命中且都有片段则记为不可核实；
//  5. 相同 (解析后路径, 行号, 采用候选的去空白片段) 的引用只计一次。
func CheckCitations(root, report string) Credibility {
	var cred Credibility
	if report == "" {
		return cred
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}

	fi := &fileIndex{root: absRoot}

	seenCited := make(map[string]struct{})
	recordCited := func(p string) {
		clean := filepath.ToSlash(filepath.Clean(p))
		if _, seen := seenCited[clean]; !seen {
			seenCited[clean] = struct{}{}
			cred.CitedPaths = append(cred.CitedPaths, clean)
		}
	}

	seenRefs := make(map[string]struct{})

	for _, rawLine := range strings.Split(report, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		locs := tokenRegex.FindAllStringSubmatchIndex(line, -1)
		for i, loc := range locs {
			if cred.Cited >= citedLimit {
				return cred
			}

			path, lineNum, endLine, tokenOK := parseCitationToken(line, loc)
			if !tokenOK {
				continue
			}

			// 片段候选：右段（本标记结束到下一标记开始）与左段（上一标记结束或行首到本标记开始）。
			segStart := loc[1]
			segEnd := len(line)
			if i+1 < len(locs) {
				segEnd = locs[i+1][0]
			}
			rightSnippet, hasRight := "", false
			if segStart < segEnd {
				rightSnippet, hasRight = findUsableQuotedSnippet(line[segStart:segEnd])
			}
			leftSnippet, hasLeft := "", false
			prevStart := 0
			if i > 0 {
				prevStart = locs[i-1][1]
			}
			if prevStart < loc[0] {
				leftSnippet, hasLeft = lastUsableQuotedSnippet(line[prevStart:loc[0]])
			}
			if !hasRight && !hasLeft {
				continue
			}

			candidates := resolveCitationPaths(fi, path)
			recordPath := path
			if len(candidates) > 0 {
				recordPath = candidates[0]
			}

			// 依次核验右段、左段候选；任何一个通过即视为核实，都不通过时按右段（无右段则左段）记录。
			adoptedRaw := rightSnippet
			if !hasRight {
				adoptedRaw = leftSnippet
			}
			verified := false
			for _, raw := range []string{rightSnippet, leftSnippet} {
				if raw == "" {
					continue
				}
				clean := stripWhitespace(raw)
				for _, rel := range candidates {
					if verifyCitation(absRoot, rel, lineNum, endLine, raw, clean) {
						verified = true
						adoptedRaw = raw
						break
					}
				}
				if verified {
					break
				}
			}

			key := recordPath + ":" + strconv.Itoa(lineNum) + ":" + stripWhitespace(adoptedRaw)
			if _, dup := seenRefs[key]; dup {
				continue
			}
			seenRefs[key] = struct{}{}

			recordCited(recordPath)
			cred.Cited++
			if verified {
				cred.Verified++
			} else {
				cred.Unverified = append(cred.Unverified, fmt.Sprintf("%s:%d", path, lineNum))
			}
		}
	}

	return cred
}

// parseCitationToken 从 tokenRegex 的匹配位置中取出路径、起始行与结束行（无范围时结束行为 0）。
func parseCitationToken(line string, loc []int) (path string, start int, end int, ok bool) {
	var startStr, endStr string
	if loc[2] >= 0 {
		path = line[loc[2]:loc[3]]
		startStr = line[loc[4]:loc[5]]
		if loc[6] >= 0 {
			endStr = line[loc[6]:loc[7]]
		}
	} else if loc[8] >= 0 {
		path = line[loc[8]:loc[9]]
		startStr = line[loc[10]:loc[11]]
		if loc[12] >= 0 {
			endStr = line[loc[12]:loc[13]]
		}
	} else {
		return "", 0, 0, false
	}

	start, err := strconv.Atoi(startStr)
	if err != nil || start <= 0 || start > maxCitationLine {
		return "", 0, 0, false
	}
	if endStr != "" {
		if e, eErr := strconv.Atoi(endStr); eErr == nil && e > start && e <= maxCitationLine {
			end = e
		}
	}
	if !validCitationPath(path) {
		return "", 0, 0, false
	}
	return path, start, end, true
}

// validCitationPath 判断引用路径是否值得核验：排除 URL、无字母、basename 无扩展名的噪声 token。
func validCitationPath(p string) bool {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return false
	}
	if !pathLetterRegex.MatchString(p) {
		return false
	}
	base := p
	if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
		base = p[idx+1:]
	}
	return pathExtRegex.MatchString(base)
}

// fileIndex 是 root 下文件路径的懒构建索引，路径以 root 相对的斜杠形式保存。
type fileIndex struct {
	root  string
	files []string
	built bool
}

func (fi *fileIndex) all() []string {
	if !fi.built {
		fi.built = true
		fi.files = buildFileIndex(fi.root)
	}
	return fi.files
}

// matchSuffix 返回索引中等于 suffix 或以 "/"+suffix 结尾的全部 root 相对路径。
func (fi *fileIndex) matchSuffix(suffix string) []string {
	if suffix == "" {
		return nil
	}
	var out []string
	for _, f := range fi.all() {
		if f == suffix || strings.HasSuffix(f, "/"+suffix) {
			out = append(out, f)
		}
	}
	return out
}

// buildFileIndex 递归收集 root 下的文件（跳过 VCS 与依赖目录，最多 fileIndexLimit 个）。
func buildFileIndex(root string) []string {
	files := make([]string, 0, 256)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := skippedDirNames[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}
		if len(files) >= fileIndexLimit {
			return fs.SkipAll
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files
}

// resolveCitationPaths 把引用路径解析为 root 内的候选相对路径（斜杠分隔）。
// 先按原逻辑拼 root 命中；未命中则按后缀匹配索引文件（长后缀优先，命中即停止）。
func resolveCitationPaths(fi *fileIndex, cited string) []string {
	p := strings.ReplaceAll(cited, `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	absolute := strings.HasPrefix(p, "/")
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return nil
	}

	var target string
	if absolute {
		target = filepath.Clean(string(filepath.Separator) + filepath.FromSlash(p))
	} else {
		target = filepath.Clean(filepath.Join(fi.root, filepath.FromSlash(p)))
	}
	if rel, relErr := filepath.Rel(fi.root, target); relErr == nil && !escapesRoot(rel) {
		if info, statErr := os.Stat(target); statErr == nil && !info.IsDir() {
			return []string{filepath.ToSlash(rel)}
		}
	}

	parts := strings.Split(p, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			continue
		}
		cleaned = append(cleaned, part)
	}
	for k := len(cleaned); k >= 1; k-- {
		suffix := strings.Join(cleaned[len(cleaned)-k:], "/")
		if matches := fi.matchSuffix(suffix); len(matches) > 0 {
			return matches
		}
	}
	return nil
}

// escapesRoot 判断 rel 是否越出 root（rel 为 filepath.Rel 的结果）。
func escapesRoot(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// verifyCitation 在 root 内的 rel 文件的行窗口里核验片段。
func verifyCitation(absRoot, rel string, startLine, endLine int, rawSnippet, cleanSnippet string) bool {
	data, readErr := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(rel)))
	if readErr != nil {
		return false
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return false
	}
	fileLines := strings.Split(content, "\n")

	totalLines := len(fileLines)
	if startLine > totalLines {
		return false
	}
	windowEnd := endLine
	if windowEnd < startLine {
		windowEnd = startLine
	}
	lo := startLine - 3
	if lo < 1 {
		lo = 1
	}
	hi := windowEnd + 3
	if hi > totalLines {
		hi = totalLines
	}
	if lo > hi {
		return false
	}

	var window strings.Builder
	var rawWindow strings.Builder
	for l := lo; l <= hi; l++ {
		window.WriteString(stripWhitespace(fileLines[l-1]))
		rawWindow.WriteString(fileLines[l-1])
		rawWindow.WriteByte('\n')
	}
	// 去空白拼接串供前两个阶段使用；保留空白与换行的原文供标识符整词匹配使用。
	return snippetMatchesWindow(rawSnippet, cleanSnippet, window.String(), rawWindow.String())
}

// snippetMatchesWindow 判断片段是否命中窗口文本。
// windowText 为逐行去空白后的拼接串（子串与省略号分段阶段用），
// rawWindowText 为保留空白的原文（标识符整词阶段用）。
// 依次尝试：去空白子串匹配、省略号分段匹配、标识符整词匹配。
func snippetMatchesWindow(rawSnippet, cleanSnippet, windowText, rawWindowText string) bool {
	if cleanSnippet == "" || windowText == "" {
		return false
	}
	if strings.Contains(windowText, cleanSnippet) {
		return true
	}

	// 含省略号的片段：忽略去空白后不足 2 个字符的段，要求所有保留段都出现，且至少一段不短于 4 个字符。
	parts := ellipsisRegex.Split(cleanSnippet, -1)
	kept := make([]string, 0, len(parts))
	hasLongPart := false
	for _, part := range parts {
		if utf8.RuneCountInString(part) < 2 {
			continue
		}
		if utf8.RuneCountInString(part) >= 4 {
			hasLongPart = true
		}
		kept = append(kept, part)
	}
	if hasLongPart {
		allFound := true
		for _, part := range kept {
			if !strings.Contains(windowText, part) {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}

	// 标识符级匹配（整词）：过滤停用词后至少 2 个不同标识符且全部整词出现在窗口原文中，
	// 或恰好 1 个标识符且不短于 8 个字符；单个短标识符不再足以单独通过。
	seen := make(map[string]struct{}, 6)
	identifiers := make([]string, 0, 6)
	for _, id := range identifierRegex.FindAllString(rawSnippet, -1) {
		if _, stop := identifierStopWords[strings.ToLower(id)]; stop {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		identifiers = append(identifiers, id)
		if len(identifiers) >= 6 {
			break
		}
	}
	if len(identifiers) == 0 {
		return false
	}
	for _, id := range identifiers {
		if !containsWholeWord(rawWindowText, id) {
			return false
		}
	}
	if len(identifiers) >= 2 {
		return true
	}
	return utf8.RuneCountInString(identifiers[0]) >= 8
}

// containsWholeWord 判断 word 是否以整词形式出现在 text 中（前后字符不是字母、数字或下划线）。
func containsWholeWord(text, word string) bool {
	if word == "" {
		return false
	}
	from := 0
	for {
		idx := strings.Index(text[from:], word)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(word)
		if !isIdentWordByte(byteAt(text, start-1)) && !isIdentWordByte(byteAt(text, end)) {
			return true
		}
		from = start + 1
	}
}

// byteAt 返回 s 在位置 i 的字节，越界时返回 0（0 不是标识符字符，即视为边界）。
func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

// isIdentWordByte 判断字节是否属于标识符字符（ASCII 字母、数字、下划线）。
func isIdentWordByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_':
		return true
	}
	return false
}

// findQuotedSnippet 在 rest 字符串中查找第一个反引号或双引号包裹的片段。
func findQuotedSnippet(rest string) (snippet string, endIdx int, ok bool) {
	firstBacktick := strings.IndexByte(rest, '`')
	firstDblQuote := strings.IndexByte(rest, '"')

	if firstBacktick == -1 && firstDblQuote == -1 {
		return "", 0, false
	}

	quoteChar := byte(0)
	startIdx := -1
	if firstBacktick != -1 && (firstDblQuote == -1 || firstBacktick < firstDblQuote) {
		quoteChar = '`'
		startIdx = firstBacktick
	} else {
		quoteChar = '"'
		startIdx = firstDblQuote
	}

	if quoteChar == '`' {
		closeIdx := strings.IndexByte(rest[startIdx+1:], '`')
		if closeIdx == -1 {
			return "", 0, false
		}
		snippet = rest[startIdx+1 : startIdx+1+closeIdx]
		endIdx = startIdx + 1 + closeIdx + 1
		return snippet, endIdx, true
	}

	// 双引号片段：处理可能存在的转义引号 \"
	for i := startIdx + 1; i < len(rest); i++ {
		if rest[i] == '"' {
			bs := 0
			for j := i - 1; j >= startIdx+1 && rest[j] == '\\'; j-- {
				bs++
			}
			if bs%2 == 0 {
				raw := rest[startIdx+1 : i]
				snippet = strings.ReplaceAll(raw, `\"`, `"`)
				endIdx = i + 1
				return snippet, endIdx, true
			}
		}
	}
	return "", 0, false
}

// isJunkSnippet 判断引号片段是否不具核验价值：占位符、整体是路径/文件名、纯行号或编号。
func isJunkSnippet(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return true
	}
	if junkPlaceholderRegex.MatchString(stripWhitespace(s)) {
		return true
	}
	if junkPathLikeRegex.MatchString(trimmed) {
		return true
	}
	return junkNumberingRegex.MatchString(trimmed)
}

// usableSnippet 判断引号片段能否作为核验候选：既不是垃圾片段，去空白后也不短于 4 个字符。
func usableSnippet(s string) bool {
	if isJunkSnippet(s) {
		return false
	}
	return utf8.RuneCountInString(stripWhitespace(s)) >= 4
}

// findUsableQuotedSnippet 在 rest 中从前往后找第一个可用引号片段，跳过垃圾片段。
func findUsableQuotedSnippet(rest string) (string, bool) {
	offset := 0
	for offset < len(rest) {
		snippet, endIdx, ok := findQuotedSnippet(rest[offset:])
		if !ok || endIdx <= 0 {
			return "", false
		}
		offset += endIdx
		if usableSnippet(snippet) {
			return snippet, true
		}
	}
	return "", false
}

// lastUsableQuotedSnippet 返回 rest 中最后一个可用引号片段，跳过垃圾片段。
func lastUsableQuotedSnippet(rest string) (string, bool) {
	var last string
	found := false
	offset := 0
	for offset < len(rest) {
		snippet, endIdx, ok := findQuotedSnippet(rest[offset:])
		if !ok || endIdx <= 0 {
			break
		}
		offset += endIdx
		if usableSnippet(snippet) {
			last = snippet
			found = true
		}
	}
	return last, found
}

// stripWhitespace 移除字符串中的所有空白字符。
func stripWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
