package tools

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxOutputBytes = 50 * 1024
const maxOutputLines = 2000

// toolOutputTempDir 是工具输出临时文件根目录，测试可替换。
var toolOutputTempDir = os.TempDir

// toolOutputMaxAge 是清理旧工具输出文件的阈值。
var toolOutputMaxAge = 24 * time.Hour

var toolOutputCleanupOnce sync.Once

// Truncate 供其他包使用统一的工具输出截断规则。
func Truncate(s, notice string) string { return truncate(s, notice) }

func truncate(s, notice string) string {
	end, lines := 0, 0
	for end < len(s) && lines < maxOutputLines && end < maxOutputBytes {
		n := strings.IndexByte(s[end:], '\n')
		if n < 0 {
			n = len(s) - end
		} else {
			n++
		}
		if end+n > maxOutputBytes {
			end = maxOutputBytes
			break
		}
		end += n
		lines++
	}
	if end < len(s) {
		// 截断点可能落在多字节字符中间，退到合法的 rune 边界。
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		out := s[:end] + "\n" + notice
		path, err := saveToolOutput(s)
		if err != nil {
			log.Print(err)
			return out
		}
		return out + " Full output saved to " + path + "; read it with offset/limit."
	}
	return s
}

// saveToolOutput 将完整工具输出写入临时文件，并在首次写入时触发旧文件清理。
func saveToolOutput(s string) (string, error) {
	dir := filepath.Join(toolOutputTempDir(), "mcode-tool-output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	toolOutputCleanupOnce.Do(func() {
		go cleanupOldToolOutputs(dir)
	})
	f, err := os.CreateTemp(dir, "mcode-tool-*.txt")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.WriteString(s); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// cleanupOldToolOutputs 删除超过 toolOutputMaxAge 的工具输出文件。
func cleanupOldToolOutputs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-toolOutputMaxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matched, err := filepath.Match("mcode-tool-*.txt", entry.Name())
		if err != nil || !matched {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}
