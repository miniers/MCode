package git

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrRevertHunkConflict = errors.New("工作区文件已改变")
	ErrRevertHunkInvalid  = errors.New("无效的审阅块")
)

type revertHunkRange struct{ oldStart, oldEnd, newStart, newEnd int }

// RevertHunkHash 返回新侧 hunk 文本的稳定哈希。startLine 为 1-based 行号。
func RevertHunkHash(newText string, startLine, lineCount int) (string, error) {
	lines, _ := splitReviewLines([]byte(newText))
	if startLine < 1 || lineCount < 0 || startLine-1 > len(lines) || startLine-1+lineCount > len(lines) {
		return "", ErrRevertHunkInvalid
	}
	return hashBytes([]byte(strings.Join(lines[startLine-1:startLine-1+lineCount], ""))), nil
}

// RevertHunk 只替换工作树中指定审阅块，不操作 Git index、HEAD 或引用。
func RevertHunk(root, path, oldText, newText string, startLine, lineCount int, hunkHash, expectedFileHash string) (string, error) {
	if err := validateReviewPath(root, path); err != nil {
		return "", err
	}
	if oldText == "" || newText == "" || !utf8.ValidString(oldText) || !utf8.ValidString(newText) || strings.ContainsRune(oldText, 0) || strings.ContainsRune(newText, 0) {
		return "", ErrRevertHunkInvalid
	}
	oldLines, oldFinal := splitReviewLines([]byte(oldText))
	newLines, newFinal := splitReviewLines([]byte(newText))
	if oldFinal != newFinal {
		// The replacement still preserves the current file's newline convention.
	}
	hunks := reviewHunks(oldLines, newLines)
	var selected *revertHunkRange
	for i := range hunks {
		h := &hunks[i]
		if h.newStart == startLine-1 && h.newEnd-h.newStart == lineCount {
			selected = h
			break
		}
	}
	if selected == nil {
		return "", ErrRevertHunkInvalid
	}
	if got, err := RevertHunkHash(newText, startLine, lineCount); err != nil || got != hunkHash {
		return "", ErrRevertHunkInvalid
	}

	fullPath := filepath.Join(root, filepath.FromSlash(path))
	current, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("读取工作区文件: %w", err)
	}
	if hashBytes(current) != expectedFileHash {
		return "", ErrRevertHunkConflict
	}
	currentLines, currentFinal := splitReviewLines(current)
	if selected.newEnd > len(currentLines) || !sameLines(currentLines[selected.newStart:selected.newEnd], newLines[selected.newStart:selected.newEnd]) {
		return "", ErrRevertHunkConflict
	}
	outLines := make([]string, 0, len(currentLines)-selected.newEnd+selected.newStart+selected.oldEnd-selected.oldStart)
	outLines = append(outLines, currentLines[:selected.newStart]...)
	oldReplacement := oldLines[selected.oldStart:selected.oldEnd]
	if bytes.Contains(current, []byte("\r\n")) {
		oldReplacement = make([]string, len(oldReplacement))
		for i, line := range oldLines[selected.oldStart:selected.oldEnd] {
			oldReplacement[i] = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") + "\r\n"
		}
	}
	outLines = append(outLines, oldReplacement...)
	outLines = append(outLines, currentLines[selected.newEnd:]...)
	out := []byte(strings.Join(outLines, ""))
	if !currentFinal && len(outLines) > 0 {
		if bytes.HasSuffix(out, []byte("\r\n")) {
			out = bytes.TrimSuffix(out, []byte("\r\n"))
		} else {
			out = bytes.TrimSuffix(out, []byte("\n"))
		}
	}
	if err := atomicReviewWrite(fullPath, out); err != nil {
		return "", err
	}
	return hashBytes(out), nil
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func validateReviewPath(root, path string) error {
	if root == "" || path == "" || filepath.IsAbs(path) {
		return ErrRevertHunkInvalid
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ErrRevertHunkInvalid
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ErrRevertHunkInvalid
	}
	full := filepath.Join(rootReal, clean)
	rel, err := filepath.Rel(rootReal, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrRevertHunkInvalid
	}
	cur := rootReal
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		info, e := os.Lstat(cur)
		if e != nil {
			if os.IsNotExist(e) && cur == full {
				break
			}
			return ErrRevertHunkInvalid
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrRevertHunkInvalid
		}
	}
	return nil
}

func splitReviewLines(b []byte) ([]string, bool) {
	if len(b) == 0 {
		return nil, true
	}
	final := bytes.HasSuffix(b, []byte("\n"))
	parts := strings.Split(string(b), "\n")
	if final {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] += "\n"
	}
	if !final && len(parts) > 0 {
		parts[len(parts)-1] = strings.TrimSuffix(parts[len(parts)-1], "\n")
	}
	return parts, final
}
func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reviewHunks uses Myers' O((N+M)D) diff and returns half-open line ranges.
func reviewHunks(a, b []string) []revertHunkRange {
	type op struct{ kind byte }
	ops := myersOps(a, b)
	var out []revertHunkRange
	oi, ni := 0, 0
	for i := 0; i < len(ops); {
		if ops[i].kind == '=' {
			oi++
			ni++
			i++
			continue
		}
		h := revertHunkRange{oldStart: oi, newStart: ni}
		for i < len(ops) && ops[i].kind != '=' {
			if ops[i].kind == '-' {
				oi++
			} else {
				ni++
			}
			i++
		}
		h.oldEnd, h.newEnd = oi, ni
		out = append(out, h)
	}
	return out
}
func myersOps(a, b []string) []struct{ kind byte } {
	n, m := len(a), len(b)
	max := n + m
	v := map[int]int{1: 0}
	trace := make([]map[int]int, 0, max+1)
	end := 0
	for d := 0; d <= max; d++ {
		cur := make(map[int]int)
		for k := -d; k <= d; k += 2 {
			x := 0
			if k == -d || (k != d && v[k-1] < v[k+1]) {
				x = v[k+1]
			} else {
				x = v[k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			cur[k] = x
			if x >= n && y >= m {
				trace = append(trace, cur)
				end = d
				goto backtrack
			}
		}
		trace = append(trace, cur)
		v = cur
	}
backtrack:
	var rev []struct{ kind byte }
	x, y := n, m
	for d := end; d > 0; d-- {
		v := trace[d-1]
		k := x - y
		var pk, px int
		if k == -d || (k != d && v[k-1] < v[k+1]) {
			pk = k + 1
			px = v[pk]
		} else {
			pk = k - 1
			px = v[pk]
		}
		py := px - pk
		for x > px && y > py {
			rev = append(rev, struct{ kind byte }{'='})
			x--
			y--
		}
		if x == px {
			rev = append(rev, struct{ kind byte }{'+'})
			y--
		} else {
			rev = append(rev, struct{ kind byte }{'-'})
			x--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, struct{ kind byte }{'='})
		x--
		y--
	}
	for x > 0 {
		rev = append(rev, struct{ kind byte }{'-'})
		x--
	}
	for y > 0 {
		rev = append(rev, struct{ kind byte }{'+'})
		y--
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func atomicReviewWrite(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcode-revert-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
