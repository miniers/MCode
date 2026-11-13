package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mcode/internal/procattr"
)

func globMatch(pattern, path string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	var re strings.Builder
	re.WriteByte('^')
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					re.WriteString("(?:.*/)?")
				} else {
					re.WriteString(".*")
				}
			} else {
				re.WriteString("[^/]*")
			}
		case '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	re.WriteByte('$')
	expr, err := regexp.Compile(re.String())
	if err != nil {
		return false, err
	}
	if !expr.MatchString(path) && !strings.Contains(pattern, "/") {
		return expr.MatchString(filepath.Base(path)), nil
	}
	return expr.MatchString(path), nil
}

var gitSearchTimeout = 10 * time.Second

func candidates(ctx context.Context, env Env, root string) ([]string, error) {
	work, e := resolvePath(env, ".")
	if e != nil {
		return nil, e
	}
	var files []string
	gitCtx, cancel := context.WithTimeout(ctx, gitSearchTimeout)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(gitCtx, "git", "-C", work, "ls-files", "--cached", "--others", "--exclude-standard", "-z"))
	out, e := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if gitCtx.Err() != nil {
		e = gitCtx.Err()
	}
	relRoot, relErr := filepath.Rel(work, root)
	if relErr != nil || relRoot == ".." || strings.HasPrefix(relRoot, ".."+string(filepath.Separator)) {
		e = fmt.Errorf("outside repository")
	}
	if e == nil {
		for _, name := range strings.Split(string(out), "\x00") {
			if name == "" {
				continue
			}
			p := filepath.Join(work, name)
			rel, err := filepath.Rel(root, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			resolved, err := resolveRead(env, p)
			if err != nil {
				continue
			}
			info, err := os.Stat(resolved)
			if err == nil && info.Mode().IsRegular() {
				files = append(files, p)
			}
		}
	} else {
		e = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if SecretPath(p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != root {
					switch d.Name() {
					case ".git", "node_modules", ".venv", "venv", "__pycache__", "dist", "build", "target", ".next", ".cache", "vendor":
						return filepath.SkipDir
					}
				}
				return nil
			}
			if _, err := resolveRead(env, p); err != nil {
				return nil
			}
			info, err := os.Stat(p)
			if err == nil && info.Mode().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	sort.Strings(files)
	return files, nil
}

// maxFileTextBytes 是文本搜索允许读取的单个文件大小上限。
var maxFileTextBytes int64 = 10 << 20

// openText 只读取文件头 8KB 判断是否为二进制，并返回已打开的文件。
func openText(safe string) ([]byte, *os.File, bool) {
	info, e := os.Stat(safe)
	if e != nil || info.Size() > maxFileTextBytes {
		return nil, nil, false
	}
	f, e := os.Open(safe)
	if e != nil {
		return nil, nil, false
	}
	prefix := make([]byte, 8192)
	n, _ := io.ReadFull(f, prefix)
	prefix = prefix[:n]
	if bytes.IndexByte(prefix, 0) >= 0 {
		f.Close()
		return nil, nil, false
	}
	return prefix, f, true
}

func fileText(env Env, p string) (string, bool) {
	safe, e := resolveRead(env, p)
	if e != nil {
		return "", false
	}
	prefix, f, ok := openText(safe)
	if !ok {
		return "", false
	}
	defer f.Close()
	rest, e := io.ReadAll(f)
	if e != nil {
		return "", false
	}
	return string(prefix) + string(rest), true
}

// isTextFile 只读取文件头，用于 find 过滤二进制文件。
func isTextFile(env Env, p string) bool {
	safe, e := resolveRead(env, p)
	if e != nil {
		return false
	}
	_, f, ok := openText(safe)
	if f != nil {
		f.Close()
	}
	return ok
}
func findFiles(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Pattern, Path string
		Limit         int
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if a.Pattern == "" || a.Limit < 0 {
		return failure("invalid pattern or limit")
	}
	if a.Limit == 0 {
		a.Limit = 1000
	}
	root, e := resolveRead(env, a.Path)
	if e != nil {
		return failure(e)
	}
	files, e := candidates(ctx, env, root)
	if e != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return failure(e)
	}
	var out strings.Builder
	n := 0
	for _, p := range files {
		if e := ctx.Err(); e != nil {
			return Result{}, e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			continue
		}
		match, e := globMatch(a.Pattern, rel)
		if e != nil {
			return failure(e)
		}
		if !match {
			continue
		}
		if !isTextFile(env, p) {
			continue
		}
		if n == a.Limit {
			out.WriteString("Output truncated.\n")
			break
		}
		fmt.Fprintln(&out, filepath.ToSlash(rel))
		n++
	}
	return Result{Text: truncate(out.String(), "Output truncated.")}, nil
}
func grepFiles(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Pattern, Path, Glob string
		IgnoreCase, Literal bool
		Context, Limit      int
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if a.Pattern == "" || a.Context < 0 || a.Limit < 0 {
		return failure("invalid pattern, context or limit")
	}
	if a.Limit == 0 {
		a.Limit = 100
	}
	pattern := a.Pattern
	if a.Literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if a.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, e := regexp.Compile(pattern)
	if e != nil {
		return failure(e)
	}
	root, e := resolveRead(env, a.Path)
	if e != nil {
		return failure(e)
	}
	files, e := candidates(ctx, env, root)
	if e != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return failure(e)
	}
	var out strings.Builder
	n := 0
	truncated := false
	for _, p := range files {
		if e := ctx.Err(); e != nil {
			return Result{}, e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			continue
		}
		if a.Glob != "" {
			match, err := globMatch(a.Glob, rel)
			if err != nil {
				return failure(err)
			}
			if !match {
				continue
			}
		}
		text, ok := fileText(env, p)
		if !ok {
			continue
		}
		lines := strings.Split(text, "\n")
		last := -1
		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			if n >= a.Limit {
				truncated = true
				break
			}
			n++
			start := i - a.Context
			if start < 0 {
				start = 0
			}
			if start <= last {
				start = last + 1
			}
			end := i + a.Context
			if end >= len(lines) {
				end = len(lines) - 1
			}
			for j := start; j <= end; j++ {
				s := strings.TrimSuffix(lines[j], "\r")
				r := []rune(s)
				if len(r) > 500 {
					s = string(r[:500]) + "…"
				}
				fmt.Fprintf(&out, "%s:%d:%s\n", filepath.ToSlash(rel), j+1, s)
			}
			last = end
		}
		if truncated {
			break
		}
	}
	if truncated {
		out.WriteString("Output truncated.\n")
	}
	return Result{Text: truncate(out.String(), "Output truncated.")}, nil
}
