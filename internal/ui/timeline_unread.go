package mcodeui

import "strconv"

// timelineUnreadLabelMax 是回到底部胶囊里显示的最大未读条数，超过它只显示「99+」。
const timelineUnreadLabelMax = 99

// timelineUnreadAnchor 是某个会话“用户离开底部那一刻”的时间线基线。
// 计数以基线时刻最后一条消息的 ID 作锚、并记下当时的已计数条数：
// 同一条消息流式变长时 ID 不变，不会重复计数。
type timelineUnreadAnchor struct {
	lastMessageID string // 基线时刻最后一条消息的 ID，可能为空
	messageCount  int    // 基线时刻已出现的用户消息与助手正文条数
}

// timelineUnreadState 按会话 ID 记录未读基线，切换会话时各自统计、互不影响。
type timelineUnreadState struct {
	anchors map[string]timelineUnreadAnchor
}

// unreadCountedMessage 判断消息是否计入未读条数：用户消息与助手正文各计 1 条，
// 工具调用、思考过程、系统消息（含问答记录）不计。
func unreadCountedMessage(m Message) bool {
	if isQuestionAnswerMessage(m) || isPlanDecisionMessage(m) {
		// 问答记录是用户自己刚答过的提问，不产生新消息提示。
		return false
	}
	switch m.Role {
	case "user":
		return true
	case "assistant":
		return !isReasoningMessage(m) && m.Kind != "tool"
	default:
		return false
	}
}

// newTimelineUnreadAnchor 以当前消息列表生成未读基线。
func newTimelineUnreadAnchor(messages []Message) timelineUnreadAnchor {
	anchor := timelineUnreadAnchor{}
	for _, m := range messages {
		if unreadCountedMessage(m) {
			anchor.messageCount++
		}
	}
	if n := len(messages); n > 0 {
		anchor.lastMessageID = messages[n-1].ID
	}
	return anchor
}

// countUnreadMessages 统计基线之后新增的未读消息条数。
// 以基线最后一条消息的 ID 为起点向后数用户消息与助手正文；
// 该消息已不在列表里（会话被截断或改写）时退回按条数差估算。
func countUnreadMessages(messages []Message, anchor timelineUnreadAnchor) int {
	start := -1
	if anchor.lastMessageID != "" {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].ID == anchor.lastMessageID {
				start = i + 1
				break
			}
		}
	}
	if start < 0 {
		counted := 0
		for _, m := range messages {
			if unreadCountedMessage(m) {
				counted++
			}
		}
		if extra := counted - anchor.messageCount; extra > 0 {
			return extra
		}
		return 0
	}
	unread := 0
	for _, m := range messages[start:] {
		if unreadCountedMessage(m) {
			unread++
		}
	}
	return unread
}

// unreadJumpLabel 生成回到底部胶囊的未读文案；超过 99 条显示「99+」，非正数返回空串。
func unreadJumpLabel(n int) string {
	if n <= 0 {
		return ""
	}
	if n > timelineUnreadLabelMax {
		return "99+ 条新消息"
	}
	return strconv.Itoa(n) + " 条新消息"
}

// observe 推进指定会话的未读状态并返回当前未读数。
// 回到时间线底部（atEnd 为 true）时清空该会话的基线并返回 0；
// 否则首次离开底部时以当前消息列表记录基线（这一刻未读为 0），之后按基线统计新增条数。
func (u *timelineUnreadState) observe(threadID string, messages []Message, atEnd bool) int {
	if u == nil || threadID == "" {
		return 0
	}
	if atEnd {
		delete(u.anchors, threadID)
		return 0
	}
	anchor, ok := u.anchors[threadID]
	if !ok {
		if u.anchors == nil {
			u.anchors = make(map[string]timelineUnreadAnchor)
		}
		u.anchors[threadID] = newTimelineUnreadAnchor(messages)
		return 0
	}
	return countUnreadMessages(messages, anchor)
}

// reset 丢弃指定会话的未读基线，让未读计数归零。
func (u *timelineUnreadState) reset(threadID string) {
	if u == nil {
		return
	}
	delete(u.anchors, threadID)
}

// observeTimelineUnread 在渲染时推进当前会话的未读计数（受 s.mu 保护）。
func (s *chatState) observeTimelineUnread(threadID string, messages []Message, atEnd bool) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timelineUnread.observe(threadID, messages, atEnd)
}

// clearTimelineUnread 清空指定会话的未读计数（受 s.mu 保护）。
func (s *chatState) clearTimelineUnread(threadID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timelineUnread.reset(threadID)
}
