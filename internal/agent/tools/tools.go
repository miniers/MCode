package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/harness"
)

// Env 提供工具执行所需的工作目录和运行环境。
type Env struct {
	WorkDir        string
	AdditionalDirs []string
	// ReadOnlyDirs 保留供调用方兼容；读取不再受目录限制，写入仍不放行这些目录。
	ReadOnlyDirs []string
	Sandbox      harness.Sandbox
	Mode         harness.PermissionMode
	ExtraEnv     []string
	OnOutput     func(chunk string)
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
	// Changes 是一次修改多个文件时的逐文件变更；非空时取代 Change。
	Changes []FileChange
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
		{"read", "Read a file with line numbers. Without limit, returns at most 400 lines; for large files, grep first, then read the needed range with offset and limit.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1}},"required":["path"]}`), true, readFile},
		{"write", "Write a file.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`), false, writeFile},
		{"edit", "Replace unique, non-overlapping text in a file.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"]}}},"required":["path","edits"]}`), false, editFile},
		{"ls", "List directory entries.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1}}}`), true, listDir},
		{"find", "Find files by glob.", json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"limit":{"type":"integer","minimum":1}},"required":["pattern"]}`), true, findFiles},
		{"grep", "Search files for a pattern.", json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"ignoreCase":{"type":"boolean"},"literal":{"type":"boolean"},"context":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["pattern"]}`), true, grepFiles},
	}
}

// 未指定 limit 时的默认读取量，用来控制上下文增长。
const (
	readDefaultLines = 400
	readDefaultBytes = 16 * 1024
)

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
	p, e := resolveRead(env, a.Path)
	if e != nil {
		return failure(e)
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return Result{Text: "Image file; binary content is not displayed."}, nil
	}
	f, e := os.Open(p)
	if e != nil {
		return failure(e)
	}
	defer f.Close()
	explicitOffset := a.Offset != 0
	if a.Offset == 0 {
		a.Offset = 1
	}
	start := a.Offset - 1
	lineLimit, byteLimit := maxOutputLines, maxOutputBytes
	if a.Limit == 0 {
		lineLimit, byteLimit = readDefaultLines, readDefaultBytes
	}
	reader := bufio.NewReaderSize(f, 32*1024)
	lineNo := 0
	lineCount := 0
	var out strings.Builder
	truncated := false
	lineLimitReached := false
	for {
		lineNo++
		inRange := lineNo > start && (a.Limit == 0 || lineNo-start <= a.Limit)
		wrotePrefix := false
		hadData, newline, readErr := readLineChunks(reader, func(piece []byte) bool {
			if !inRange {
				return true
			}
			if !wrotePrefix {
				prefix := fmt.Sprintf("%d: ", lineNo)
				if a.Limit == 0 && out.Len()+len(prefix) > byteLimit {
					truncated = true
					return false
				}
				out.WriteString(prefix)
				wrotePrefix = true
			}
			room := byteLimit - out.Len()
			if room <= 0 {
				truncated = true
				return false
			}
			if len(piece) > room {
				out.Write(piece[:room])
				truncated = true
				return false
			}
			out.Write(piece)
			return true
		})
		if readErr != nil {
			return failure(readErr)
		}
		if !hadData && !newline {
			if explicitOffset && lineNo <= a.Offset {
				return failure(fmt.Sprintf("offset %d is beyond end of file (%d lines)", a.Offset, lineNo-1))
			}
			break
		}
		if inRange && !newline && out.Len() < byteLimit {
			out.WriteByte('\n')
		}
		if truncated {
			break
		}
		if inRange {
			lineCount++
		}
		if a.Limit > 0 && lineNo-start >= a.Limit {
			break
		}
		if lineCount >= lineLimit {
			lineLimitReached = true
			break
		}
	}
	if lineLimitReached {
		// 只有后续确实还有内容时才提示输出被截断。
		if _, err := reader.Peek(1); err == nil {
			truncated = true
		}
	}
	s := out.String()
	if truncated {
		s += fmt.Sprintf("\nOutput truncated; continue with offset=%d", lineNo+1)
	}
	// limit 窗口之外还有内容时提示继续读取。
	if a.Limit > 0 {
		if _, err := reader.Peek(1); err == nil {
			s += fmt.Sprintf("\nMore lines available; use offset %d to continue reading.", lineNo+1)
		}
	}
	return Result{Text: s}, nil
}

// readLineChunks 按块读取一行，避免一次把超长行读入内存。
// write 返回 false 时提前停止读取。
func readLineChunks(reader *bufio.Reader, write func([]byte) bool) (hadData, newline bool, err error) {
	for {
		piece, readErr := reader.ReadSlice('\n')
		if len(piece) > 0 {
			hadData = true
			if write != nil && !write(piece) {
				return hadData, false, nil
			}
		}
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if readErr == io.EOF {
			return hadData, false, nil
		}
		if readErr != nil {
			return hadData, false, readErr
		}
		return hadData, true, nil
	}
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
	if e = writeFileAtomic(p, []byte(*a.Content), mode); e != nil {
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
	p, e := resolveRead(env, a.Path)
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
