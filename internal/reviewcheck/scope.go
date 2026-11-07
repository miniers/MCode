package reviewcheck

import (
	"path"
	"strings"
	"unicode"
)

// Scope 是 brief 与改动文件的比对结果。
type Scope struct {
	// Allowed 是 brief 与 WritePaths 给出的允许集；目录以 / 结尾按前缀匹配，
	// 含 * ? [ 的项按 path.Match 匹配。
	Allowed map[string]struct{}
	// Hard 是硬越界的改动文件（源码、配置等）。
	Hard []string
	// Soft 是软越界的改动文件（测试、文档、计划文档）。
	Soft []string
	// Known 表示 brief 里至少提取到一个路径；false 时不允许 L1 按行数自动通过。
	Known bool
}

// pathExtensions 是只凭扩展名也能认定为 repo 相对路径的后缀集合。
var pathExtensions = map[string]struct{}{
	".go":       {},
	".md":       {},
	".mdx":      {},
	".markdown": {},
	".txt":      {},
	".ts":       {},
	".tsx":      {},
	".js":       {},
	".jsx":      {},
	".json":     {},
	".yaml":     {},
	".yml":      {},
	".toml":     {},
	".mod":      {},
	".sum":      {},
	".py":       {},
	".rs":       {},
	".c":        {},
	".h":        {},
	".cc":       {},
	".cpp":      {},
	".java":     {},
	".kt":       {},
	".swift":    {},
	".sh":       {},
	".css":      {},
	".html":     {},
	".sql":      {},
	".proto":    {},
	".lock":     {},
}

// ExtractPaths 从 brief 中提取 repo 相对路径：先取反引号片段，再扫描其余
// 按空白分隔的 token；自动去掉 :行号 后缀和尾部标点，目录保留 / 结尾。
func ExtractPaths(brief string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(token string) {
		p, ok := normalizePathToken(token)
		if !ok {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	var plain strings.Builder
	rest := brief
	for {
		start := strings.IndexByte(rest, '`')
		if start < 0 {
			plain.WriteString(rest)
			break
		}
		plain.WriteString(rest[:start])
		rest = rest[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			plain.WriteString(rest)
			break
		}
		segment := rest[:end]
		rest = rest[end+1:]
		if _, ok := normalizePathToken(segment); ok {
			add(segment)
			continue
		}
		for _, token := range splitTokens(segment) {
			add(token)
		}
	}
	for _, token := range splitTokens(plain.String()) {
		add(token)
	}
	return out
}

// ParseScope 按设计文档 §4.1 比对 brief、改动文件与 WritePaths：
// 命中允许集或允许目录前缀的改动放行，测试、文档、计划文档越界记入 Soft，
// 其余越界记入 Hard；brief 没提取到任何路径时 Known=false。
func ParseScope(brief string, changed, writePaths []string) Scope {
	scope := Scope{Allowed: map[string]struct{}{}}
	briefPaths := ExtractPaths(brief)
	scope.Known = len(briefPaths) > 0
	for _, p := range briefPaths {
		scope.Allowed[p] = struct{}{}
	}
	for _, p := range writePaths {
		if norm, ok := normalizeWritePath(p); ok {
			scope.Allowed[norm] = struct{}{}
		}
	}
	for _, file := range changed {
		file = cleanChangedPath(file)
		if file == "" {
			continue
		}
		if _, ok := scope.Allowed[file]; ok {
			continue
		}
		if isAllowedPath(scope.Allowed, file) {
			continue
		}
		switch Classify(file) {
		case ClassTest, ClassDoc, ClassPlan:
			scope.Soft = append(scope.Soft, file)
		default:
			scope.Hard = append(scope.Hard, file)
		}
	}
	return scope
}

// isAllowedPath 判断文件是否命中目录前缀或 glob 允许项。
func isAllowedPath(allowed map[string]struct{}, file string) bool {
	for item := range allowed {
		if strings.HasSuffix(item, "/") {
			if strings.HasPrefix(file, item) {
				return true
			}
			continue
		}
		if strings.ContainsAny(item, "*?[") {
			if ok, _ := path.Match(item, file); ok {
				return true
			}
			continue
		}
		if strings.HasPrefix(file, item+"/") {
			return true
		}
	}
	return false
}

// splitTokens 按空白和中文标点切分 brief，避免中文句读把路径粘进正文。
// ASCII 的 : 不切分，保留 internal/x.go:12 这类行号引用。
func splitTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		if unicode.IsSpace(r) {
			return true
		}
		switch r {
		case '，', '。', '；', '、', '：', '（', '）', '【', '】', '《', '》':
			return true
		}
		return false
	})
}

// normalizePathToken 把 brief 里的原始 token 归一成允许集键；
// 不是 repo 相对路径时返回 false。
func normalizePathToken(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`")
	s = strings.TrimLeft(s, "\"'([{<")
	s = strings.TrimRight(s, "\"'」』）)]}>")
	s = strings.TrimRight(s, "，。；：、,.;:")
	s, _ = stripLineSuffix(s)
	s = strings.TrimRight(s, "，。；：、,.;:")
	if s == "" {
		return "", false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "//") {
		return "", false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return "", false
	}
	if strings.ContainsAny(s, "*?$`") {
		return "", false
	}
	if strings.HasPrefix(s, "-") {
		return "", false
	}
	if len(s) >= 2 && s[1] == ':' && isLetter(s[0]) {
		return "", false
	}
	isDir := strings.HasSuffix(s, "/")
	cleaned := path.Clean(strings.TrimRight(s, "/"))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", false
		}
	}
	if isDir {
		return cleaned + "/", true
	}
	if !strings.Contains(cleaned, "/") {
		if _, ok := pathExtensions[strings.ToLower(path.Ext(cleaned))]; !ok {
			return "", false
		}
	}
	// 目录形式（无扩展名且带 /）即使没写结尾斜杠也按前缀匹配。
	if !strings.Contains(cleaned, "/") || strings.ContainsAny(cleaned, "*?[") {
		return cleaned, true
	}
	if hasFileExtension(cleaned) {
		return cleaned, true
	}
	return cleaned + "/", true
}

// normalizeWritePath 归一 WritePaths 的项；与 brief 不同，这里允许 * ? [ 形式的 glob。
func normalizeWritePath(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`")
	s = strings.TrimLeft(s, "\"'([{<")
	s = strings.TrimRight(s, "\"'」』）)]}>")
	s = strings.TrimRight(s, "，。；：、,.;:")
	s, _ = stripLineSuffix(s)
	s = strings.TrimRight(s, "，。；：、,.;:")
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.ContainsAny(s, "$`") {
		return "", false
	}
	if len(s) >= 2 && s[1] == ':' && isLetter(s[0]) {
		return "", false
	}
	isDir := strings.HasSuffix(s, "/")
	cleaned := path.Clean(strings.TrimRight(s, "/"))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", false
		}
	}
	if strings.ContainsAny(cleaned, "*?[") {
		return cleaned, true
	}
	if isDir {
		return cleaned + "/", true
	}
	if !strings.Contains(cleaned, "/") {
		return cleaned, true
	}
	if !hasFileExtension(cleaned) {
		return cleaned + "/", true
	}
	return cleaned, true
}

// cleanChangedPath 归一 git 给出的改动路径，用于和允许集比对。
func cleanChangedPath(p string) string {
	p = normalizeSlashes(p)
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// stripLineSuffix 反复去掉末尾的 :行号 或 :起行-止行。
func stripLineSuffix(s string) (string, bool) {
	stripped := false
	for {
		i := strings.LastIndexByte(s, ':')
		if i <= 0 {
			return s, stripped
		}
		if !isLineRef(s[i+1:]) {
			return s, stripped
		}
		s = s[:i]
		stripped = true
	}
}

// isLineRef 判断 : 后的内容是否是行号引用。
func isLineRef(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// hasFileExtension 判断路径最后一段是否带已知文件扩展名。
func hasFileExtension(p string) bool {
	base := path.Base(p)
	ext := path.Ext(base)
	if ext == "" || ext == "." {
		return false
	}
	_, ok := pathExtensions[strings.ToLower(ext)]
	return ok
}

// isLetter 判断字节是否是 ASCII 字母，用于识别 Windows 盘符。
func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
