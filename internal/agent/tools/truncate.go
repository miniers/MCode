package tools

import "strings"

const maxOutputBytes = 50 * 1024
const maxOutputLines = 2000

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
		return s[:end] + "\n" + notice
	}
	return s
}
