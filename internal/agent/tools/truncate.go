package tools

import (
	"strings"
	"unicode/utf8"
)

const maxOutputBytes = 50 * 1024
const maxOutputLines = 2000

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
		return s[:end] + "\n" + notice
	}
	return s
}
