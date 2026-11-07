package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"mcode/internal/harness"
)

// openAIToolDefinition 对应 OpenAI 规范的 function calling 工具定义。
type openAIToolDefinition struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// defaultTools 定义 OpenAI 原生会话所支持的 5 个本地文件与搜索工具。
// userRejectedText 是用户拒绝某次工具调用时回给模型的结果。
const userRejectedText = "The user rejected this action. Don't retry it; ask the user how they want to proceed."

var defaultTools = []openAIToolDefinition{
	{
		Type: "function",
		Function: openAIFunctionDef{
			Name: "read_file",
			Description: "Return the text of a project file (path relative to the project root). " +
				"Without start_line the whole file comes back; for long files ask for a window with " +
				"start_line and line_count, and use the total line count in the result to page further.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to the project root",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "1-based line to start from",
					},
					"line_count": map[string]any{
						"type":        "integer",
						"description": "Number of lines to return, starting at start_line",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: openAIFunctionDef{
			Name:        "list_files",
			Description: "Show the entries of a project directory; set recursive to walk subdirectories too.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory relative to the project root; leave empty for the root itself",
					},
					"recursive": map[string]any{
						"type": "boolean",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: openAIFunctionDef{
			Name:        "search_text",
			Description: "Find lines in project files that contain pattern, matched literally unless regex is true.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type": "string",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Limit the search to this directory or file (relative to the project root)",
					},
					"regex": map[string]any{
						"type": "boolean",
					},
				},
				"required": []string{"pattern"},
			},
		},
	},
	{
		Type: "function",
		Function: openAIFunctionDef{
			Name:        "write_file",
			Description: "Write the complete content of a project file, creating it or replacing whatever was there.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type": "string",
					},
					"content": map[string]any{
						"type": "string",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: openAIFunctionDef{
			Name:        "edit_file",
			Description: "Swap old_string for new_string in a project file. old_string has to match the file exactly and occur only once.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type": "string",
					},
					"old_string": map[string]any{
						"type": "string",
					},
					"new_string": map[string]any{
						"type": "string",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
	},
}

// skippedDirectories 遍历与搜索文件时跳过的目录集合。
var skippedDirectories = map[string]bool{
	".git":         true,
	"node_modules": true,
	".build":       true,
	"build":        true,
	"dist":         true,
	"DerivedData":  true,
	"Pods":         true,
	"target":       true,
	".venv":        true,
	"venv":         true,
}

// isReadOnlyTool 标注工具是否为只读类型。
func isReadOnlyTool(toolName string) bool {
	switch toolName {
	case "read_file", "list_files", "search_text":
		return true
	case "write_file", "edit_file":
		return false
	default:
		return false
	}
}

// resolveProjectURL 校验并将模型提供的路径安全解析为绝对路径与相对于工作区的相对路径。
// 严禁 ".." 越界与软链接逃逸到 turn.WorkDir 外部。
func resolveProjectURL(workDir, inputPath string) (absPath string, relPath string, err error) {
	trimmed := strings.TrimSpace(inputPath)
	if trimmed == "" {
		return "", "", errors.New("A file path is required.")
	}

	rootAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid workDir: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		rootReal = rootAbs
	}

	var cand string
	if strings.HasPrefix(trimmed, "~") {
		home, hErr := os.UserHomeDir()
		if hErr != nil {
			return "", "", fmt.Errorf("resolve home dir: %w", hErr)
		}
		cand = filepath.Join(home, strings.TrimPrefix(trimmed, "~"))
	} else if filepath.IsAbs(trimmed) {
		cand = trimmed
	} else {
		cand = filepath.Join(rootAbs, trimmed)
	}

	candClean := filepath.Clean(cand)

	// 1. 逻辑路径必须在 rootAbs 之下
	rel, err := filepath.Rel(rootAbs, candClean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("That path is outside the project. Stay inside %s.", workDir)
	}

	// 2. 真实物理路径必须在 rootReal 之下，防止符号链接逃逸
	if fi, statErr := os.Lstat(candClean); statErr == nil {
		realTarget, evalErr := filepath.EvalSymlinks(candClean)
		if evalErr == nil {
			realRel, rErr := filepath.Rel(rootReal, realTarget)
			if rErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
				return "", "", fmt.Errorf("That path is outside the project. Stay inside %s.", workDir)
			}
		} else if fi.Mode()&os.ModeSymlink != 0 {
			// 存在破损符号链接或无法解析
			return "", "", fmt.Errorf("That path is outside the project. Stay inside %s.", workDir)
		}
	} else {
		// 文件尚不存在（如新建文件），逐级向上检查其存在的父目录真实物理路径
		parent := filepath.Dir(candClean)
		for parent != "" && parent != "." && parent != "/" {
			if _, pErr := os.Stat(parent); pErr == nil {
				realParent, evalErr := filepath.EvalSymlinks(parent)
				if evalErr == nil {
					realRel, rErr := filepath.Rel(rootReal, realParent)
					if rErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
						return "", "", fmt.Errorf("That path is outside the project. Stay inside %s.", workDir)
					}
				}
				break
			}
			parent = filepath.Dir(parent)
		}
	}

	return candClean, rel, nil
}

// readFile 执行 read_file 工具逻辑。
func readFile(workDir, inputPath string, startLine, lineCount *int) (string, error) {
	absPath, relPath, err := resolveProjectURL(workDir, inputPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("No such file: %s.", relPath)
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("Path is a directory: %s.", relPath)
	}

	const readLimit = 1_000_000
	if info.Size() > readLimit {
		return "", fmt.Errorf("That file is too large to read (%d bytes).", info.Size())
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	if !utf8.Valid(data) && bytes.Contains(data, []byte{0}) {
		return "", errors.New("That file is not readable as text.")
	}

	text := string(data)

	if startLine != nil || lineCount != nil {
		lines := strings.Split(text, "\n")
		first := 1
		if startLine != nil && *startLine > 1 {
			first = *startLine
		}
		if first > len(lines) {
			return fmt.Sprintf("(the file has %d lines; start_line %d is past the end)", len(lines), first), nil
		}
		count := 200
		if lineCount != nil && *lineCount > 0 {
			count = *lineCount
		}
		last := first - 1 + count
		if last > len(lines) {
			last = len(lines)
		}
		slice := lines[first-1 : last]
		return fmt.Sprintf("(lines %d-%d of %d)\n%s", first, last, len(lines), strings.Join(slice, "\n")), nil
	}

	// 未指定行范围且超过 60,000 字符时截断
	runes := []rune(text)
	if len(runes) > 60_000 {
		return string(runes[:60_000]) + fmt.Sprintf("\n…(truncated, %d chars total; read the rest with start_line and line_count)", len(runes)), nil
	}

	return text, nil
}

// listFiles 执行 list_files 工具逻辑。
func listFiles(workDir, inputPath string, recursive bool) (string, error) {
	baseAbs := workDir
	var baseRel string
	trimmed := strings.TrimSpace(inputPath)
	if trimmed != "" {
		abs, rel, err := resolveProjectURL(workDir, trimmed)
		if err != nil {
			return "", err
		}
		baseAbs = abs
		baseRel = rel
	}

	info, err := os.Stat(baseAbs)
	if err != nil {
		if os.IsNotExist(err) {
			p := baseRel
			if p == "" {
				p = workDir
			}
			return "", fmt.Errorf("No such directory: %s.", p)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("No such directory: %s.", inputPath)
	}

	var results []string
	const maxItems = 300

	if recursive {
		err = filepath.WalkDir(baseAbs, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if path != baseAbs && (skippedDirectories[name] || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			rel, rErr := filepath.Rel(workDir, path)
			if rErr == nil {
				results = append(results, rel)
				if len(results) >= maxItems {
					results = append(results, "…(truncated)")
					return errors.New("limit reached")
				}
			}
			return nil
		})
		if err != nil && err.Error() != "limit reached" {
			return "", err
		}
	} else {
		entries, rErr := os.ReadDir(baseAbs)
		if rErr != nil {
			return "", rErr
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, entry := range entries {
			name := entry.Name()
			if skippedDirectories[name] || strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(baseAbs, name)
			rel, rErr := filepath.Rel(workDir, full)
			if rErr != nil {
				continue
			}
			if entry.IsDir() {
				rel += "/"
			}
			results = append(results, rel)
			if len(results) >= maxItems {
				results = append(results, "…(truncated)")
				break
			}
		}
	}

	if len(results) == 0 {
		return "(empty)", nil
	}
	return strings.Join(results, "\n"), nil
}

// searchText 执行 search_text 工具逻辑。
func searchText(workDir, pattern, inputPath string, isRegex bool) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", errors.New("Error: a search pattern is required.")
	}

	var targetAbs string
	trimmed := strings.TrimSpace(inputPath)
	if trimmed != "" {
		abs, _, err := resolveProjectURL(workDir, trimmed)
		if err != nil {
			return "", err
		}
		targetAbs = abs
	} else {
		targetAbs = workDir
	}

	var reg *regexp.Regexp
	var err error
	if isRegex {
		reg, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex: %w", err)
		}
	}

	var matches []string
	const maxMatches = 120
	totalCount := 0

	err = filepath.WalkDir(targetAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skippedDirectories[name] || (strings.HasPrefix(name, ".") && path != workDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}

		// 快速略过大于 1MB 的大文件或二进制文件
		fi, fErr := d.Info()
		if fErr != nil || fi.Size() > 1_000_000 {
			return nil
		}

		data, rErr := os.ReadFile(path)
		if rErr != nil || bytes.Contains(data, []byte{0}) {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		rel, rErr := filepath.Rel(workDir, path)
		if rErr != nil {
			rel = path
		}

		for idx, line := range lines {
			matched := false
			if isRegex {
				matched = reg.MatchString(line)
			} else {
				matched = strings.Contains(line, pattern)
			}
			if matched {
				totalCount++
				if len(matches) < maxMatches {
					matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, idx+1, line))
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "(no matches)", nil
	}
	out := strings.Join(matches, "\n")
	if totalCount > maxMatches {
		out += fmt.Sprintf("\n…(%d matches, truncated)", totalCount)
	}
	return out, nil
}

// writeFile 执行 write_file 工具逻辑，并在写入成功后产生 unified diff 与文件变更事件。
func writeFile(workDir, inputPath, content string, emit func(harness.Event)) (string, error) {
	absPath, relPath, err := resolveProjectURL(workDir, inputPath)
	if err != nil {
		return "", err
	}

	var oldText string
	oldData, err := os.ReadFile(absPath)
	if err == nil {
		oldText = string(oldData)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return "", err
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return "", err
	}

	patch := generateUnifiedDiff(relPath, oldText, content)
	_ = harness.Emit(emit, harness.Event{
		Type:  harness.EventFileChange,
		Path:  relPath,
		Patch: patch,
	})

	return fmt.Sprintf("Wrote %s (%d bytes).", relPath, len(content)), nil
}

// editFile 执行 edit_file 工具逻辑，替换文件中唯一的 old_string。
func editFile(workDir, inputPath, oldStr, newStr string, emit func(harness.Event)) (string, error) {
	if oldStr == "" {
		return "", errors.New("old_string is empty: look at the file and copy the exact text you want to replace.")
	}

	absPath, relPath, err := resolveProjectURL(workDir, inputPath)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("No such file: %s.", relPath)
		}
		return "", err
	}

	text := string(data)
	occurrences := strings.Count(text, oldStr)
	if occurrences == 0 {
		return "", fmt.Errorf("That exact text was not found in %s. Read the file and copy it exactly.", relPath)
	}
	if occurrences > 1 {
		return "", fmt.Errorf("That text appears %d times in %s. Include more context so it matches once.", occurrences, relPath)
	}

	replaced := strings.Replace(text, oldStr, newStr, 1)
	if err := os.WriteFile(absPath, []byte(replaced), 0644); err != nil {
		return "", err
	}

	patch := generateUnifiedDiff(relPath, text, replaced)
	_ = harness.Emit(emit, harness.Event{
		Type:  harness.EventFileChange,
		Path:  relPath,
		Patch: patch,
	})

	return fmt.Sprintf("Edited %s.", relPath), nil
}

// generateUnifiedDiff 计算两个文本版本之间的统一差异 (Unified Diff)。
func generateUnifiedDiff(relPath, oldText, newText string) string {
	if oldText == newText {
		return ""
	}

	var sb strings.Builder
	if oldText == "" {
		sb.WriteString("--- /dev/null\n")
		sb.WriteString(fmt.Sprintf("+++ b/%s\n", relPath))
		newLines := strings.Split(newText, "\n")
		sb.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(newLines)))
		for _, l := range newLines {
			sb.WriteString("+" + l + "\n")
		}
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("--- a/%s\n", relPath))
	sb.WriteString(fmt.Sprintf("+++ b/%s\n", relPath))

	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	sb.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines)))
	for _, l := range oldLines {
		sb.WriteString("-" + l + "\n")
	}
	for _, l := range newLines {
		sb.WriteString("+" + l + "\n")
	}
	return sb.String()
}

// executeToolCall 在不涉及 MCP 工具时执行内置文件与搜索工具，保留原有调用方式。
func executeToolCall(ctx context.Context, turn harness.Turn, call toolCall, emit func(harness.Event)) (output string, isError bool) {
	return executeToolCallWithMCP(ctx, turn, call, emit, nil)
}

// executeToolCallWithMCP 根据模型解析后的工具调用执行具体操作，包含权限判决、审批交互及错误捕获。
// 以 mcp__ 开头的工具名转发给本轮发现的 MCP 端点。
func executeToolCallWithMCP(ctx context.Context, turn harness.Turn, call toolCall, emit func(harness.Event), mcpTools *mcpSession) (output string, isError bool) {
	var args map[string]any
	trimmedArgs := strings.TrimSpace(call.Arguments)
	if trimmedArgs != "" {
		if err := json.Unmarshal([]byte(trimmedArgs), &args); err != nil {
			return fmt.Sprintf("error: invalid tool arguments JSON: %v", err), true
		}
	} else {
		args = make(map[string]any)
	}
	if args == nil {
		args = make(map[string]any)
	}

	getString := func(key string, fallbacks ...string) string {
		if v, ok := args[key].(string); ok {
			return v
		}
		for _, fb := range fallbacks {
			if v, ok := args[fb].(string); ok {
				return v
			}
		}
		return ""
	}

	getIntPtr := func(key string) *int {
		if v, ok := args[key].(float64); ok {
			val := int(v)
			return &val
		}
		return nil
	}

	getBool := func(key string) bool {
		if v, ok := args[key].(bool); ok {
			return v
		}
		return false
	}

	toolName := call.Name
	readOnly := isReadOnlyTool(toolName)

	// 写类工具前置权限判定；总管会话的内置 MCP 工具由审批方按会话身份放行
	// （白名单在 MCP 服务器一侧），其余情况仍按权限模式判定。
	if !readOnly && !harness.MCPToolAllowed(ctx, turn.Approver, toolName) {
		pathArg := getString("path")
		decision := harness.Decide(turn.Mode, nil, toolName, pathArg, false)
		switch decision {
		case harness.DecisionAllow:
			// 放行
		case harness.DecisionDeny:
			return userRejectedText, true
		case harness.DecisionAsk:
			if turn.Approver == nil {
				return userRejectedText, true
			}
			summary := toolName
			if pathArg != "" {
				summary = fmt.Sprintf("%s %s", toolName, pathArg)
			}
			req := harness.ApprovalRequest{
				ToolID:   call.ID,
				ToolName: toolName,
				Input:    json.RawMessage(call.Arguments),
				Summary:  summary,
			}
			apprDecision, err := turn.Approver.Approve(ctx, req)
			if err != nil {
				harness.AckDelivery(turn.Approver, req, err)
				return userRejectedText, true
			}
			harness.AckDelivery(turn.Approver, req, nil)
			if apprDecision != harness.DecisionAllow {
				return userRejectedText, true
			}
		}
	}

	switch toolName {
	case "read_file":
		path := getString("path")
		res, err := readFile(turn.WorkDir, path, getIntPtr("start_line"), getIntPtr("line_count"))
		if err != nil {
			return fmt.Sprintf("Error: %s", err.Error()), true
		}
		return res, false

	case "list_files":
		path := getString("path")
		res, err := listFiles(turn.WorkDir, path, getBool("recursive"))
		if err != nil {
			return fmt.Sprintf("Error: %s", err.Error()), true
		}
		return res, false

	case "search_text":
		pattern := getString("pattern")
		path := getString("path")
		regex := getBool("regex")
		res, err := searchText(turn.WorkDir, pattern, path, regex)
		if err != nil {
			return fmt.Sprintf("Error: %s", err.Error()), true
		}
		return res, false

	case "write_file":
		path := getString("path")
		content := getString("content")
		res, err := writeFile(turn.WorkDir, path, content, emit)
		if err != nil {
			return fmt.Sprintf("Error: %s", err.Error()), true
		}
		return res, false

	case "edit_file":
		path := getString("path")
		oldStr := getString("old_string", "old_text")
		newStr := getString("new_string", "new_text")
		res, err := editFile(turn.WorkDir, path, oldStr, newStr, emit)
		if err != nil {
			return fmt.Sprintf("Error: %s", err.Error()), true
		}
		return res, false

	default:
		if strings.HasPrefix(toolName, "mcp__") {
			if mcpTools == nil {
				return fmt.Sprintf("Error: unknown tool %s.", toolName), true
			}
			res, isErr, err := mcpTools.callTool(ctx, toolName, args)
			if err != nil {
				return fmt.Sprintf("Error: %s", err.Error()), true
			}
			if strings.TrimSpace(res) == "" {
				if isErr {
					return "Error: the MCP tool failed without returning any output.", true
				}
				return "(no output)", false
			}
			return res, isErr
		}
		return fmt.Sprintf("Error: unknown tool %s.", toolName), true
	}
}
