package mcodeui

import "strings"

// reasoningHeadlineRunes 是单行思考摘要最多保留的字符数。
const reasoningHeadlineRunes = 60

// reasoningHeadline 从一条思考的正文里抽一行做单行摘要：按行拆分、跳过空行，
// latest 为 true 取最后一行，否则取第一行；超过上限按 rune 截断并补省略号。没有内容时返回空串。
func reasoningHeadline(text string, latest bool) string {
	picked := reasoningHeadlineFull(text, latest)
	if picked == "" {
		return ""
	}
	return truncateRunes(picked, reasoningHeadlineRunes)
}

// reasoningHeadlineFull 返回未截断的首行（或最后一行）摘要，供标题 Tooltip 展示完整内容。
func reasoningHeadlineFull(text string, latest bool) string {
	picked := ""
	for _, line := range strings.Split(text, "\n") {
		line = reasoningHeadlineText(line)
		if line == "" {
			continue
		}
		picked = line
		if !latest {
			break
		}
	}
	return picked
}

// reasoningHeaderTitle 拼出思考行的标题：有正文时显示「思考过程 · 要旨」，
// 正文为空时只显示标题本身，不留下多余的间隔号。
func reasoningHeaderTitle(text string, streaming bool) string {
	headline := reasoningHeadline(text, streaming)
	if headline == "" {
		return reasoningHeaderLabel(streaming)
	}
	return reasoningHeaderLabel(streaming) + " · " + headline
}

// reasoningHeadlineText 把一行思考正文整理成摘要文字：去掉全部 ** 加粗标记与行内反引号，
// 再反复剥掉行首可能叠在一起的有序列表序号（如「1. 」「2) 」）与 # > - * 标记
// （例如「> - 1. 要点」）以及它们之间的空白。
func reasoningHeadlineText(line string) string {
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "`", "")
	line = strings.TrimSpace(line)
	for {
		next := strings.TrimSpace(strings.TrimLeft(line, "#> -*"))
		next = stripOrderedListPrefix(next)
		if next == line {
			break
		}
		line = next
	}
	return line
}

// stripOrderedListPrefix 去掉行首的有序列表序号，要求序号后跟空白，
// 避免把「1.5 秒」这类小数误当成列表。
func stripOrderedListPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 3 || i >= len(line) {
		return line
	}
	if line[i] != '.' && line[i] != ')' {
		return line
	}
	if i+1 < len(line) && line[i+1] != ' ' && line[i+1] != '\t' {
		return line
	}
	return strings.TrimSpace(line[i+1:])
}

// reasoningHoldOpen 决定一条刚结束流式输出的思考要怎么收起：hold 表示这一帧先保持展开，
// forget 表示已消费掉「曾经流式」这个状态（下一帧起不再特殊对待）。
// wasLive 为假说明不是刚结束的帧；用户正在往上翻阅（不在底部）且开着自动折叠时，
// 突然收起会把视线跳走，所以先保持展开；其余情况（在底部、或关掉了自动折叠）都按普通规则收起。
func reasoningHoldOpen(wasLive, autoCollapse, atEnd bool) (hold, forget bool) {
	if !wasLive {
		return false, false
	}
	if autoCollapse && !atEnd {
		return true, false
	}
	return false, true
}
