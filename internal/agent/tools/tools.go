package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/harness"
)

// Env 提供工具执行所需的工作目录和运行环境。
type Env struct {
	WorkDir        string
	AdditionalDirs []string
	Sandbox        harness.Sandbox
	Mode           harness.PermissionMode
	ExtraEnv       []string
	OnOutput       func(chunk string)
}

// FileChange 描述文件修改及其差异。
type FileChange struct {
	Path  string
	Patch string
}

// Result 保存工具输出和可选的文件修改。
type Result struct {
	Text    string
	IsError bool
	Change  *FileChange
}

// Tool 描述工具的参数和执行函数。
type Tool struct {
	Name, Description string
	Parameters        json.RawMessage
	ReadOnly          bool
	Run               func(ctx context.Context, env Env, args json.RawMessage) (Result, error)
}

func failure(err any) (Result, error) { return Result{Text: fmt.Sprint(err), IsError: true}, nil }
func parse(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// FileTools 返回文件操作工具。
func FileTools() []Tool {
	return []Tool{
		{"read", "Read a file with line numbers.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1}},"required":["path"]}`), true, readFile},
		{"write", "Write a file.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`), false, writeFile},
		{"edit", "Replace unique, non-overlapping text in a file.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"]}}},"required":["path","edits"]}`), false, editFile},
		{"ls", "List directory entries.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1}}}`), true, listDir},
		{"find", "Find files by glob.", json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"limit":{"type":"integer","minimum":1}},"required":["pattern"]}`), true, findFiles},
		{"grep", "Search files for a pattern.", json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"ignoreCase":{"type":"boolean"},"literal":{"type":"boolean"},"context":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["pattern"]}`), true, grepFiles},
	}
}
func readFile(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Path          string `json:"path"`
		Offset, Limit int
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if a.Path == "" || a.Offset < 0 || a.Limit < 0 {
		return failure("invalid path, offset or limit")
	}
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	p, e := resolve(env, a.Path)
	if e != nil {
		return failure(e)
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return Result{Text: "Image file; binary content is not displayed."}, nil
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return failure(e)
	}
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if a.Offset == 0 {
		a.Offset = 1
	}
	start := a.Offset - 1
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if a.Limit > 0 && start+a.Limit < end {
		end = start + a.Limit
	}
	var out strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&out, "%d: %s", i+1, lines[i])
		if !strings.HasSuffix(lines[i], "\n") {
			out.WriteByte('\n')
		}
	}
	s := truncate(out.String(), "Output truncated; use offset to continue reading.")
	if end < len(lines) {
		s += fmt.Sprintf("\nMore lines available; use offset %d to continue reading.", end+1)
	}
	return Result{Text: s}, nil
}
func writeFile(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if a.Path == "" || a.Content == nil {
		return failure("path and content are required")
	}
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	p, e := resolve(env, a.Path)
	if e != nil {
		return failure(e)
	}
	old, e := os.ReadFile(p)
	if e != nil && !os.IsNotExist(e) {
		return failure(e)
	}
	if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		return failure(e)
	}
	mode := os.FileMode(0644)
	info, statErr := os.Stat(p)
	if statErr == nil {
		mode = info.Mode()
	} else if !os.IsNotExist(statErr) {
		return failure(statErr)
	}
	if e = os.WriteFile(p, []byte(*a.Content), mode); e != nil {
		return failure(e)
	}
	return Result{Text: "File written.", Change: &FileChange{Path: p, Patch: unifiedDiff(p, string(old), *a.Content)}}, nil
}
func listDir(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Path  string
		Limit int
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if a.Limit < 0 {
		return failure("invalid limit")
	}
	if a.Limit == 0 {
		a.Limit = 500
	}
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	p, e := resolve(env, a.Path)
	if e != nil {
		return failure(e)
	}
	entries, e := os.ReadDir(p)
	if e != nil {
		return failure(e)
	}
	var b strings.Builder
	for i, x := range entries {
		if i >= a.Limit {
			b.WriteString("Output truncated.\n")
			break
		}
		name := x.Name()
		if x.IsDir() {
			name += "/"
		}
		fmt.Fprintln(&b, name)
	}
	return Result{Text: truncate(b.String(), "Output truncated.")}, nil
}
