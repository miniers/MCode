package mcodeui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 长文本粘贴的最小阈值：行数或字数任一达到即视为长文本。
// 长文本不直接塞进输入框（会把输入框撑爆、难编辑），而是在光标处只留一个占位符，
// 发送时再把占位符展开成原文，作为内联文本发给模型（不是文件附件）。
const (
	pastedTextMinLines = 20
	pastedTextMinRunes = 1500
)

// isLongPastedText 判断粘贴进来的文本是否算长文本。
func isLongPastedText(text string) bool {
	if text == "" {
		return false
	}
	if strings.Count(text, "\n")+1 >= pastedTextMinLines {
		return true
	}
	return utf8.RuneCountInString(text) >= pastedTextMinRunes
}

// pastedPlaceholderLabel 生成占位符「[粘贴文本 #N · M 行]」，M 是行数（换行数加一）。
func pastedPlaceholderLabel(seq, lines int) string {
	return fmt.Sprintf("[粘贴文本 #%d · %d 行]", seq, lines)
}

// registerPastedPlaceholder 登记一段长文本并返回它的占位符，序号在 chatState 内自增（从 1 开始）。
func (s *chatState) registerPastedPlaceholder(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pastedPlaceholderSeq++
	label := pastedPlaceholderLabel(s.pastedPlaceholderSeq, strings.Count(text, "\n")+1)
	if s.pastedPlaceholders == nil {
		s.pastedPlaceholders = make(map[string]string)
	}
	s.pastedPlaceholders[label] = text
	return label
}

// expandPastedPlaceholders 把文本里出现且已登记的占位符整串换回原文。
// 它只是纯查找替换，不动映射：发送可能被拒绝或后端失败，输入框里仍是占位符，
// 用户重试时要能再展开一次，所以展开必须幂等、不能消费掉映射条目。
// 映射里没有的占位符（例如重启后从草稿恢复出来的旧占位符）保持原样：宁可原样发出去，也不猜原文。
// 映射只随 chatState 生命周期存在，条目很少也很小，因此不做主动清理：
// 用户删掉占位符、发送成功或失败都不删除条目，会话结束随 chatState 一起释放。
func (s *chatState) expandPastedPlaceholders(text string) string {
	if text == "" {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pastedPlaceholders) == 0 {
		return text
	}
	for label, original := range s.pastedPlaceholders {
		if !strings.Contains(text, label) {
			continue
		}
		text = strings.ReplaceAll(text, label, original)
	}
	return text
}

// insertPastedPlaceholderAt 把占位符插到草稿的 [start, end) 区间（光标处即 start == end），
// 返回新草稿和插入后光标应处的 rune 偏移。区间越界会钳到文本长度内。
func insertPastedPlaceholderAt(draft string, start, end int, label string) (string, int) {
	runes := []rune(draft)
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if start > len(runes) {
		start = len(runes)
	}
	if end > len(runes) {
		end = len(runes)
	}
	if end < start {
		start, end = end, start
	}
	next := string(runes[:start]) + label + string(runes[end:])
	return next, start + utf8.RuneCountInString(label)
}

// queuePastedPlaceholder 登记长文本占位符并请求一帧重绘，由下一帧构建 composer 时插到光标处。
// 占位符原样写进草稿（composer_drafts.go），重启后映射已丢、无法展开，会当作普通文本发出去；
// 这是长文本折叠的已知局限，不为它加持久化。
func (s *chatState) queuePastedPlaceholder(text string) {
	label := s.registerPastedPlaceholder(text)
	s.pendingPastedPlaceholders = append(s.pendingPastedPlaceholders, label)
	s.invalidate()
}
