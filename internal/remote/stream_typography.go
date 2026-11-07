package remote

import "strings"

// emDashRunes 是排版清洗的目标字符集合：
// U+2014 em dash、U+2015 horizontal bar、U+2E3A two-em dash、U+2E3B three-em dash。
const emDashRunes = "\u2014\u2015\u2E3A\u2E3B"

// withoutEmDashes 把模型输出中的破折号替换为普通连字符 "-"，保留原有空格：
// "works — every time" 变为 "works - every time"，"ships—sometimes" 变为 "ships-sometimes"。
// en dash（–，U+2013）是区间表达的正确符号，予以保留。不含目标字符时直接返回原串。
func withoutEmDashes(s string) string {
	if !strings.ContainsAny(s, emDashRunes) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isEmDash(r) {
			return '-'
		}
		return r
	}, s)
}

// isEmDash 判断字符是否为 em dash 及其等长变体。
func isEmDash(r rune) bool {
	switch r {
	case '\u2014', '\u2015', '\u2E3A', '\u2E3B':
		return true
	default:
		return false
	}
}

// isBlankDelta 判断流式增量去掉首尾空白后是否为空；空增量不应产生新的助手消息。
func isBlankDelta(s string) bool {
	return strings.TrimSpace(s) == ""
}
