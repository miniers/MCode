package mcodeui

import "strings"

// composerHistoryState 维护输入框方向键历史导航的状态（仅在 UI 线程访问）。
// index 为 0 表示未在导航（零值即可用），1..n 表示正在浏览从最新往旧数第 index 条历史。
type composerHistoryState struct {
	index    int
	stash    string // 进入导航前的草稿，翻过最新一条或 Esc 退出时恢复
	recalled string // 最近一次回填的文本，用于识别用户改动并退出导航
}

// composerHistoryEntries 按时间顺序收集会话内用户消息的文本，供方向键历史导航使用。
// 只取 Role 为 "user" 的消息，过滤空文本，并对相邻重复去重。
func composerHistoryEntries(messages []Message) []string {
	var entries []string
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		// 跳过编排通知与子代理汇报：它们是系统消息，不是用户输入。
		if isSwarmNoticeText(text) {
			continue
		}
		if len(entries) > 0 && entries[len(entries)-1] == text {
			continue
		}
		entries = append(entries, text)
	}
	return entries
}

// composerHistoryEntriesCachedLocked 返回本会话的方向键历史条目，按 messagesRev 缓存：
// 每个 composer（主会话与每个浮窗面板各一个）每帧都要算一遍，
// 浮窗里 200 条消息的会话逐帧重扫全部消息、再拼一个字符串切片纯属浪费。
// 返回值只读，调用方不得修改。调用方须已持有 s.mu。
func (s *chatState) composerHistoryEntriesCachedLocked() []string {
	if s == nil {
		return nil
	}
	if s.composerHistoryValid && s.composerHistoryRev == s.messagesRev {
		return s.composerHistoryEntries
	}
	entries := composerHistoryEntries(s.messages)
	s.composerHistoryRev = s.messagesRev
	s.composerHistoryEntries = entries
	s.composerHistoryValid = true
	return entries
}

// caretOnFirstLine 判断 caret（rune 偏移）之前是否没有换行符，即光标是否位于首行。
func caretOnFirstLine(text string, caret int) bool {
	i := 0
	for _, r := range text {
		if i >= caret {
			return true
		}
		if r == '\n' {
			return false
		}
		i++
	}
	return true
}

// caretOnLastLine 判断 caret（rune 偏移）之后是否没有换行符，即光标是否位于末行。
func caretOnLastLine(text string, caret int) bool {
	i := 0
	for _, r := range text {
		if i >= caret && r == '\n' {
			return false
		}
		i++
	}
	return true
}

// prev 返回上一条（更旧）历史文本；首次调用时把 current 存入 stash。
// ok 为 false 表示没有历史或已翻到最旧一条，状态保持不变。
func (h *composerHistoryState) prev(entries []string, current string) (string, bool) {
	if h == nil || len(entries) == 0 {
		return "", false
	}
	if h.index == 0 {
		h.stash = current
	}
	if h.index >= len(entries) {
		return "", false
	}
	h.index++
	h.recalled = entries[len(entries)-h.index]
	return h.recalled, true
}

// next 返回下一条（更新）历史文本；翻过最新一条后返回 stash 并退出导航。
// ok 为 false 表示未在导航。
func (h *composerHistoryState) next(entries []string) (string, bool) {
	if h == nil || h.index == 0 {
		return "", false
	}
	if h.index > 1 && h.index <= len(entries) {
		h.index--
		h.recalled = entries[len(entries)-h.index]
		return h.recalled, true
	}
	text := h.stash
	h.reset()
	return text, true
}

// reset 退出导航并丢弃暂存草稿；调用方保留输入框当前文本。
func (h *composerHistoryState) reset() {
	if h == nil {
		return
	}
	h.index = 0
	h.stash = ""
	h.recalled = ""
}

// restore 退出导航并返回进入导航前的草稿；ok 为 false 表示未在导航。
func (h *composerHistoryState) restore() (string, bool) {
	if h == nil || h.index == 0 {
		return "", false
	}
	text := h.stash
	h.reset()
	return text, true
}
