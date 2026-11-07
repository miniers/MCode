package remote

import "time"

// 同一工具条目增量输出的合并参数：距上次下发不足 toolOutputFlushInterval、且累计未达
// toolOutputFlushBytes 时先攒在缓冲里，等下一次事件（tool_end 必定会到）再一并刷出，
// 避免每个片段都触发一次消息发布。
const (
	toolOutputFlushInterval = 100 * time.Millisecond
	toolOutputFlushBytes    = 4096
	// toolOutputStreamMaxRunes 与 tool_end 的截断口径保持一致：一个工具条目最多显示 2000 个字符。
	toolOutputStreamMaxRunes = 2000
)

// toolOutputStream 汇总同一个工具条目在运行期间收到的增量输出。
// provider 推送的片段是追加语义；tool_end 的最终 output 会覆盖已流出的内容，
// 这样 provider 既推增量又在结束时重复一次全量时，条目里也不会出现两遍文本。
type toolOutputStream struct {
	base      string // 条目固定正文：工具名加输入摘要，与 tool_start 建条目时写的一致
	received  string // 已经下发到消息上的增量（按上限截断）
	pending   string // 已合并但尚未下发到消息上的增量
	lastFlush time.Time
}

// newToolOutputStream 以 tool_start 建条目时的固定正文初始化一个增量输出缓冲。
func newToolOutputStream(base string) *toolOutputStream {
	return &toolOutputStream{base: base}
}

// append 追加一个增量片段，返回 true 表示应该把缓冲里的内容下发到消息上。
func (s *toolOutputStream) append(chunk string, now time.Time) bool {
	s.pending += chunk
	return s.due(now)
}

// due 报告缓冲里的增量现在能否下发：攒够字节数，或距上次下发已经超过间隔。
func (s *toolOutputStream) due(now time.Time) bool {
	if s.pending == "" {
		return false
	}
	if len(s.pending) >= toolOutputFlushBytes {
		return true
	}
	return now.Sub(s.lastFlush) >= toolOutputFlushInterval
}

// markFlushed 把缓冲里的增量并入已下发内容，并记下这次下发时间。
func (s *toolOutputStream) markFlushed(now time.Time) {
	s.received = toolOutputTruncateRunes(s.received+s.pending, toolOutputStreamMaxRunes)
	s.pending = ""
	s.lastFlush = now
}

// render 返回条目此刻应当显示的正文：固定正文加上已下发的增量输出。
func (s *toolOutputStream) render() string {
	switch {
	case s.received == "":
		return s.base
	case s.base == "":
		return s.received
	default:
		return s.base + "\n" + s.received
	}
}

// override 用工具最终输出覆盖已流出的增量并返回条目正文，尚未下发的残余片段同时作废。
// final 为空说明 provider 只在运行中推增量，此时把残余片段并入后保留已收到的内容。
func (s *toolOutputStream) override(final string) string {
	if final != "" {
		s.received = toolOutputTruncateRunes(final, toolOutputStreamMaxRunes)
	} else {
		s.received = toolOutputTruncateRunes(s.received+s.pending, toolOutputStreamMaxRunes)
	}
	s.pending = ""
	return s.render()
}

// toolOutputTruncateRunes 按字符数截断文本，超出部分直接丢弃。
func toolOutputTruncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
