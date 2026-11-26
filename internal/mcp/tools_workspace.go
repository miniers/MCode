package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mcode/internal/procattr"
)

const (
	workspaceInlineMaxBytes   = 8800
	workspaceSearchMaxQueries = 10
	workspaceSearchDefMatches = 50
	workspaceSearchMinMatches = 1
	workspaceSearchMaxMatches = 200
	workspaceSearchLineRunes  = 200
	workspaceSearchTimeout    = 15 * time.Second
	workspaceWalkMaxFileBytes = 1 << 20 // 1MB
	workspaceWalkProbeBytes   = 8192

	workspaceOutlineMaxPaths     = 20
	workspaceOutlineMaxFileBytes = 2 << 20 // 2MB
	workspaceOutlineLineRunes    = 160
)

var (
	rgLookPathOnce sync.Once
	rgCachedPath   string
	// lookupRGPath 在生产环境缓存 rg 可执行路径，测试可覆盖以强制走 WalkDir 回退。
	lookupRGPath = defaultLookupRGPath

	workspaceOutlineFallbackRe = regexp.MustCompile(`^\s*(export\s+)?(default\s+)?(async\s+)?(function|class|def|interface|type|struct|enum|fn|func|impl|trait|module)\b`)
)

func defaultLookupRGPath() string {
	rgLookPathOnce.Do(func() {
		if path, err := exec.LookPath("rg"); err == nil {
			rgCachedPath = path
		}
	})
	return rgCachedPath
}

func (r *Registry) registerWorkspaceTools(s *Server) {
	r.Register(ToolDefinition{
		Name:        "workspace_search",
		Description: "Run several code searches in one call. Prefer this over separate grep calls when checking multiple patterns.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"queries": map[string]any{
					"type":        "array",
					"description": "1 to 10 search queries to run concurrently.",
					"minItems":    1,
					"maxItems":    workspaceSearchMaxQueries,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"pattern": map[string]any{
								"type":        "string",
								"description": "Search pattern string.",
							},
							"regex": map[string]any{
								"type":        "boolean",
								"description": "Interpret pattern as a regular expression (default false).",
							},
							"glob": map[string]any{
								"type":        "string",
								"description": "Optional glob filter such as *.go.",
							},
							"path": map[string]any{
								"type":        "string",
								"description": "Optional relative subdirectory within the workspace.",
							},
						},
						"required": []string{"pattern"},
					},
				},
				"max_matches": map[string]any{
					"type":        "integer",
					"description": "Maximum matches per query, default 50, clamped to 1-200.",
				},
			},
			"required": []string{"queries"},
		},
		IsWrite: false,
		Handler: handleWorkspaceSearch,
	})

	r.Register(ToolDefinition{
		Name:        "workspace_outline",
		Description: "Extract top-level symbols or headings from multiple workspace files in one call.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{
					"type":        "array",
					"description": "1 to 20 relative file paths to outline concurrently.",
					"minItems":    1,
					"maxItems":    workspaceOutlineMaxPaths,
					"items": map[string]any{
						"type": "string",
					},
				},
			},
			"required": []string{"paths"},
		},
		IsWrite: false,
		Handler: handleWorkspaceOutline,
	})
}

// resolveWorkspaceRoot 解析并校验工作根目录（已做 EvalSymlinks），保证后续相对路径边界可信。
func resolveWorkspaceRoot(tc ToolContext) (string, *ToolResult) {
	root := strings.TrimSpace(tc.WorkDir)
	if root == "" {
		root = strings.TrimSpace(tc.Project.Path)
	}
	if root == "" {
		res := toolError(CodeInvalidArguments, "Workspace root directory is unavailable.", false)
		return "", &res
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		res := toolError(CodeInvalidArguments, "Invalid workspace root: "+err.Error(), false)
		return "", &res
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		res := toolError(CodeInvalidArguments, "Invalid workspace root: "+err.Error(), false)
		return "", &res
	}
	info, err := os.Stat(realRoot)
	if err != nil || !info.IsDir() {
		res := toolError(CodeInvalidArguments, "Workspace root is not a valid directory.", false)
		return "", &res
	}
	return realRoot, nil
}

// resolveWorkspaceRelPath 校验相对路径并限制在 realRoot（已 EvalSymlinks）之内。
// 返回值依次为：实际解析后的绝对路径、Clean 后的相对 slash 路径、是否属于边界越权错误、错误本身。
func resolveWorkspaceRelPath(realRoot, raw string, allowEmpty bool) (resolved string, relSlash string, boundaryErr bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if allowEmpty {
			return realRoot, ".", false, nil
		}
		return "", "", true, errors.New("path must not be empty")
	}

	native := filepath.FromSlash(trimmed)
	if filepath.IsAbs(trimmed) || filepath.IsAbs(native) || filepath.VolumeName(native) != "" ||
		strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") {
		return "", "", true, fmt.Errorf("absolute path is not allowed: %s", raw)
	}

	cleaned := filepath.Clean(native)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", "", true, fmt.Errorf("path escapes workspace root: %s", raw)
	}

	joined := filepath.Join(realRoot, cleaned)
	if !isWithinWorkspaceRoot(realRoot, joined) {
		return "", "", true, fmt.Errorf("path escapes workspace root: %s", raw)
	}

	realTarget, evalErr := filepath.EvalSymlinks(joined)
	if evalErr == nil {
		if !isWithinWorkspaceRoot(realRoot, realTarget) {
			return "", "", true, fmt.Errorf("symlink resolves outside workspace root: %s", raw)
		}
		return realTarget, filepath.ToSlash(cleaned), false, nil
	}

	// 即使目标不存在（如悬空软链或缺失文件），仍检查软链目标与最近存在祖先是否越界。
	if info, lerr := os.Lstat(joined); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		if linkTarget, rerr := os.Readlink(joined); rerr == nil {
			targetPath := linkTarget
			if !filepath.IsAbs(targetPath) {
				targetPath = filepath.Join(filepath.Dir(joined), targetPath)
			}
			if !isWithinWorkspaceRoot(realRoot, filepath.Clean(targetPath)) {
				return "", "", true, fmt.Errorf("symlink resolves outside workspace root: %s", raw)
			}
		}
	}
	if ancestorErr := checkAncestorWithinRoot(realRoot, joined); ancestorErr != nil {
		return "", "", true, ancestorErr
	}
	return joined, filepath.ToSlash(cleaned), false, evalErr
}

func checkAncestorWithinRoot(realRoot, target string) error {
	cur := filepath.Dir(target)
	for {
		if realCur, err := filepath.EvalSymlinks(cur); err == nil {
			if !isWithinWorkspaceRoot(realRoot, realCur) {
				return fmt.Errorf("ancestor symlink resolves outside workspace root: %s", cur)
			}
			return nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

func isWithinWorkspaceRoot(realRoot, target string) bool {
	rel, err := filepath.Rel(realRoot, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// truncateWorkspaceOutput 按 UTF-8 rune 边界截断输出，使最终字节数不超过 maxBytes。
func truncateWorkspaceOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 {
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		suffix := fmt.Sprintf("…(truncated %d bytes)", len(s)-cut)
		if cut+len(suffix) <= maxBytes {
			return s[:cut] + suffix
		}
		if cut == 0 {
			break
		}
		cut--
	}
	suffix := fmt.Sprintf("…(truncated %d bytes)", len(s))
	if len(suffix) <= maxBytes {
		return suffix
	}
	return suffix[:maxBytes]
}

func truncateRunesSimple(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes == 1 {
		return string(runes[:1])
	}
	return string(runes[:maxRunes-1]) + "…"
}

type workspaceSearchQuery struct {
	pattern  string
	regex    bool
	glob     string
	subRel   string
	resolved string
}

type workspaceSearchMatch struct {
	path string
	line int
	text string
}

type workspaceSearchResult struct {
	pattern string
	matches []workspaceSearchMatch
	errMsg  string
}

func handleWorkspaceSearch(tc ToolContext, args map[string]any) ToolResult {
	realRoot, errRes := resolveWorkspaceRoot(tc)
	if errRes != nil {
		return *errRes
	}

	rawQueries, ok := args["queries"].([]any)
	if !ok || len(rawQueries) < 1 || len(rawQueries) > workspaceSearchMaxQueries {
		return toolError(CodeInvalidArguments, fmt.Sprintf("Argument 'queries' must contain 1 to %d items.", workspaceSearchMaxQueries), false)
	}

	queries := make([]workspaceSearchQuery, 0, len(rawQueries))
	for i, item := range rawQueries {
		obj, ok := item.(map[string]any)
		if !ok {
			return toolError(CodeInvalidArguments, fmt.Sprintf("queries[%d] must be an object.", i), false)
		}
		pattern, ok := obj["pattern"].(string)
		if !ok || pattern == "" {
			return toolError(CodeInvalidArguments, fmt.Sprintf("queries[%d].pattern must be a non-empty string.", i), false)
		}
		var isRegex bool
		if rawRegex, exists := obj["regex"]; exists {
			b, ok := rawRegex.(bool)
			if !ok {
				return toolError(CodeInvalidArguments, fmt.Sprintf("queries[%d].regex must be a boolean.", i), false)
			}
			isRegex = b
		}
		var glob string
		if rawGlob, exists := obj["glob"]; exists {
			g, ok := rawGlob.(string)
			if !ok {
				return toolError(CodeInvalidArguments, fmt.Sprintf("queries[%d].glob must be a string.", i), false)
			}
			glob = strings.TrimSpace(g)
		}
		var rawPath string
		if pVal, exists := obj["path"]; exists {
			p, ok := pVal.(string)
			if !ok {
				return toolError(CodeInvalidArguments, fmt.Sprintf("queries[%d].path must be a string.", i), false)
			}
			rawPath = p
		}

		resolved, relSlash, boundaryErr, err := resolveWorkspaceRelPath(realRoot, rawPath, true)
		if boundaryErr {
			return toolError(CodeInvalidArguments, err.Error(), false)
		}
		if err != nil {
			return toolError(CodeInvalidArguments, fmt.Sprintf("queries[%d].path is invalid: %v", i, err), false)
		}
		queries = append(queries, workspaceSearchQuery{
			pattern:  pattern,
			regex:    isRegex,
			glob:     glob,
			subRel:   relSlash,
			resolved: resolved,
		})
	}

	maxMatches := clampIntArg(args["max_matches"], workspaceSearchDefMatches, workspaceSearchMinMatches, workspaceSearchMaxMatches)
	baseCtx := tc.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	rgPath := lookupRGPath()
	results := make([]workspaceSearchResult, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(idx int, query workspaceSearchQuery) {
			defer wg.Done()
			qCtx, cancel := context.WithTimeout(baseCtx, workspaceSearchTimeout)
			defer cancel()
			if rgPath != "" {
				results[idx] = runRGSearch(qCtx, rgPath, realRoot, query, maxMatches)
			} else {
				results[idx] = runWalkDirSearch(qCtx, realRoot, query, maxMatches)
			}
		}(i, q)
	}
	wg.Wait()

	var b strings.Builder
	for i, res := range results {
		if i > 0 {
			b.WriteByte('\n')
		}
		if res.errMsg != "" {
			fmt.Fprintf(&b, "## %s (error: %s)\n", res.pattern, res.errMsg)
			continue
		}
		fmt.Fprintf(&b, "## %s (%d matches)\n", res.pattern, len(res.matches))
		sort.Slice(res.matches, func(a, b int) bool {
			if res.matches[a].path != res.matches[b].path {
				return res.matches[a].path < res.matches[b].path
			}
			return res.matches[a].line < res.matches[b].line
		})
		for _, m := range res.matches {
			fmt.Fprintf(&b, "%s:%d: %s\n", m.path, m.line, m.text)
		}
	}
	return toolSuccess(truncateWorkspaceOutput(strings.TrimRight(b.String(), "\n"), workspaceInlineMaxBytes))
}

func runRGSearch(ctx context.Context, rgPath, realRoot string, q workspaceSearchQuery, maxMatches int) workspaceSearchResult {
	res := workspaceSearchResult{pattern: q.pattern}
	args := []string{"-n", "--no-heading", "--color", "never", "--max-count", strconv.Itoa(maxMatches)}
	if !q.regex {
		args = append(args, "-F")
	}
	if q.glob != "" {
		args = append(args, "-g", q.glob)
	}
	args = append(args, "-e", q.pattern, "--")
	if q.subRel != "" && q.subRel != "." {
		args = append(args, q.subRel)
	}

	cmd := procattr.HideWindow(exec.CommandContext(ctx, rgPath, args...))
	cmd.Dir = realRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.errMsg = "search timed out"
		return res
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return res
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		res.errMsg = msg
		return res
	}

	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if len(res.matches) >= maxMatches {
			break
		}
		path, lineNum, text, ok := parseRGOutputLine(scanner.Text())
		if !ok {
			continue
		}
		path = strings.TrimPrefix(filepath.ToSlash(path), "./")
		text = strings.TrimRight(text, "\r")
		res.matches = append(res.matches, workspaceSearchMatch{
			path: path,
			line: lineNum,
			text: truncateRunesSimple(text, workspaceSearchLineRunes),
		})
	}
	return res
}

// parseRGOutputLine 解析 `path:line:text` 格式的一行 rg 输出。
func parseRGOutputLine(line string) (path string, lineNum int, text string, ok bool) {
	for i := 0; i < len(line); i++ {
		if line[i] != ':' {
			continue
		}
		rest := line[i+1:]
		nextColon := strings.IndexByte(rest, ':')
		if nextColon <= 0 {
			continue
		}
		n, err := strconv.Atoi(rest[:nextColon])
		if err != nil || n <= 0 {
			continue
		}
		return line[:i], n, rest[nextColon+1:], true
	}
	return "", 0, "", false
}

func runWalkDirSearch(ctx context.Context, realRoot string, q workspaceSearchQuery, maxMatches int) workspaceSearchResult {
	res := workspaceSearchResult{pattern: q.pattern}
	var re *regexp.Regexp
	if q.regex {
		var err error
		re, err = regexp.Compile(q.pattern)
		if err != nil {
			res.errMsg = err.Error()
			return res
		}
	}

	walkErr := filepath.WalkDir(q.resolved, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		// 不跟随符号链接，与 rg 默认行为一致并守住工作区边界。
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() > workspaceWalkMaxFileBytes {
			return nil
		}

		rel, relErr := filepath.Rel(realRoot, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if q.glob != "" && !matchSearchGlob(q.glob, relSlash) {
			return nil
		}

		data, readErr := readBoundedFile(path, workspaceWalkMaxFileBytes)
		if readErr != nil {
			return nil
		}
		probeLen := len(data)
		if probeLen > workspaceWalkProbeBytes {
			probeLen = workspaceWalkProbeBytes
		}
		if bytes.IndexByte(data[:probeLen], 0) >= 0 {
			return nil
		}

		lineNum := 0
		rest := data
		for len(rest) > 0 {
			lineNum++
			idx := bytes.IndexByte(rest, '\n')
			var lineBytes []byte
			if idx >= 0 {
				lineBytes = rest[:idx]
				rest = rest[idx+1:]
			} else {
				lineBytes = rest
				rest = nil
			}
			lineStr := strings.TrimRight(string(lineBytes), "\r")
			matched := false
			if re != nil {
				matched = re.MatchString(lineStr)
			} else {
				matched = strings.Contains(lineStr, q.pattern)
			}
			if matched {
				res.matches = append(res.matches, workspaceSearchMatch{
					path: relSlash,
					line: lineNum,
					text: truncateRunesSimple(lineStr, workspaceSearchLineRunes),
				})
				if len(res.matches) >= maxMatches {
					return fs.SkipAll
				}
			}
		}
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.errMsg = "search timed out"
		} else {
			res.errMsg = walkErr.Error()
		}
	}
	return res
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file too large")
	}
	return data, nil
}

func matchSearchGlob(glob, relSlash string) bool {
	negate := false
	if strings.HasPrefix(glob, "!") {
		negate = true
		glob = strings.TrimPrefix(glob, "!")
	}
	glob = strings.TrimPrefix(filepath.ToSlash(glob), "./")
	matched := false
	if strings.Contains(glob, "/") {
		if strings.HasPrefix(glob, "**/") {
			sub := strings.TrimPrefix(glob, "**/")
			if ok, _ := filepath.Match(sub, filepath.Base(relSlash)); ok {
				matched = true
			} else if strings.HasSuffix(relSlash, "/"+sub) || relSlash == sub {
				matched = true
			}
		} else {
			matched, _ = filepath.Match(glob, relSlash)
		}
	} else {
		matched, _ = filepath.Match(glob, filepath.Base(relSlash))
	}
	if negate {
		return !matched
	}
	return matched
}

type workspaceOutlineTarget struct {
	relSlash     string
	resolvedPath string
	lookupErr    error
}

func handleWorkspaceOutline(tc ToolContext, args map[string]any) ToolResult {
	realRoot, errRes := resolveWorkspaceRoot(tc)
	if errRes != nil {
		return *errRes
	}

	rawPaths, ok := args["paths"].([]any)
	if !ok || len(rawPaths) < 1 || len(rawPaths) > workspaceOutlineMaxPaths {
		return toolError(CodeInvalidArguments, fmt.Sprintf("Argument 'paths' must contain 1 to %d items.", workspaceOutlineMaxPaths), false)
	}

	targets := make([]workspaceOutlineTarget, 0, len(rawPaths))
	for i, item := range rawPaths {
		p, ok := item.(string)
		if !ok {
			return toolError(CodeInvalidArguments, fmt.Sprintf("paths[%d] must be a string.", i), false)
		}
		resolved, relSlash, boundaryErr, err := resolveWorkspaceRelPath(realRoot, p, false)
		if boundaryErr {
			return toolError(CodeInvalidArguments, err.Error(), false)
		}
		targets = append(targets, workspaceOutlineTarget{
			relSlash:     relSlash,
			resolvedPath: resolved,
			lookupErr:    err,
		})
	}

	sections := make([][]string, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(idx int, t workspaceOutlineTarget) {
			defer wg.Done()
			sections[idx] = outlineSingleFile(t)
		}(i, target)
	}
	wg.Wait()

	var b strings.Builder
	for i, target := range targets {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "## %s\n", target.relSlash)
		for _, line := range sections[i] {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return toolSuccess(truncateWorkspaceOutput(strings.TrimRight(b.String(), "\n"), workspaceInlineMaxBytes))
}

func outlineSingleFile(t workspaceOutlineTarget) []string {
	if t.lookupErr != nil {
		return []string{"error: " + t.lookupErr.Error()}
	}
	info, err := os.Stat(t.resolvedPath)
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	if info.IsDir() {
		return []string{"error: path is a directory"}
	}
	if info.Size() > workspaceOutlineMaxFileBytes {
		return []string{"error: file exceeds 2MB limit"}
	}
	data, err := os.ReadFile(t.resolvedPath)
	if err != nil {
		return []string{"error: " + err.Error()}
	}

	switch strings.ToLower(filepath.Ext(t.resolvedPath)) {
	case ".go":
		return outlineGoFile(t.resolvedPath, data)
	case ".md":
		return outlineMarkdownFile(data)
	default:
		return outlineRegexFile(data)
	}
}

func outlineGoFile(path string, data []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if f == nil && err != nil {
		return []string{"error: " + err.Error()}
	}

	var lines []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			line := fset.Position(d.Pos()).Line
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = fmt.Sprintf("(%s) %s", formatGoRecvType(d.Recv.List[0].Type), name)
			}
			lines = append(lines, fmt.Sprintf("L%d func %s", line, name))
		case *ast.GenDecl:
			kind := strings.ToLower(d.Tok.String())
			if kind != "type" && kind != "const" && kind != "var" {
				continue
			}
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					line := fset.Position(sp.Pos()).Line
					lines = append(lines, fmt.Sprintf("L%d type %s", line, sp.Name.Name))
				case *ast.ValueSpec:
					line := fset.Position(sp.Pos()).Line
					names := make([]string, 0, len(sp.Names))
					for _, ident := range sp.Names {
						names = append(names, ident.Name)
					}
					if len(names) > 0 {
						lines = append(lines, fmt.Sprintf("L%d %s %s", line, kind, strings.Join(names, ", ")))
					}
				}
			}
		}
	}
	if len(lines) == 0 && err != nil {
		return []string{"error: " + err.Error()}
	}
	return lines
}

func formatGoRecvType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + formatGoRecvType(t.X)
	case *ast.IndexExpr:
		return formatGoRecvType(t.X) + "[" + formatGoRecvType(t.Index) + "]"
	case *ast.IndexListExpr:
		parts := make([]string, 0, len(t.Indices))
		for _, idx := range t.Indices {
			parts = append(parts, formatGoRecvType(idx))
		}
		return formatGoRecvType(t.X) + "[" + strings.Join(parts, ", ") + "]"
	case *ast.ParenExpr:
		return formatGoRecvType(t.X)
	default:
		return "?"
	}
}

func outlineMarkdownFile(data []byte) []string {
	var lines []string
	var fenceChar byte
	var fenceLen int
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), workspaceOutlineMaxFileBytes+1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		trimmed := strings.TrimSpace(scanner.Text())
		if fc, fl, isFence := parseMarkdownFence(trimmed); isFence {
			if fenceChar == 0 {
				fenceChar, fenceLen = fc, fl
			} else if fc == fenceChar && fl >= fenceLen && strings.TrimSpace(strings.TrimLeft(trimmed, string(fc))) == "" {
				fenceChar, fenceLen = 0, 0
			}
			continue
		}
		if fenceChar != 0 {
			continue
		}
		if isMarkdownHeading(trimmed) {
			lines = append(lines, fmt.Sprintf("L%d %s", lineNum, trimmed))
		}
	}
	return lines
}

func parseMarkdownFence(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	ch := trimmed[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	return ch, n, true
}

func isMarkdownHeading(trimmed string) bool {
	hashes := 0
	for hashes < len(trimmed) && trimmed[hashes] == '#' {
		hashes++
	}
	if hashes < 1 || hashes > 6 {
		return false
	}
	return hashes == len(trimmed) || trimmed[hashes] == ' ' || trimmed[hashes] == '\t'
}

func outlineRegexFile(data []byte) []string {
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), workspaceOutlineMaxFileBytes+1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		if workspaceOutlineFallbackRe.MatchString(raw) {
			trimmed := strings.TrimSpace(raw)
			lines = append(lines, fmt.Sprintf("L%d %s", lineNum, truncateRunesSimple(trimmed, workspaceOutlineLineRunes)))
		}
	}
	return lines
}
