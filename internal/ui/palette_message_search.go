package mcodeui

import (
	"context"
	"strings"
	"time"
)

// 命令面板里的跨会话消息正文搜索：查询串满足最短长度后防抖 200ms 后台搜一次，
// 结果作为「消息」类条目追加在所有会话/项目条目之后，点击后跳到该会话并定位到该消息。

// MessageSearchHit 是一条消息正文搜索命中，字段与 store.MessageSearchHit 一致。
type MessageSearchHit struct {
	ThreadID    string
	ThreadTitle string
	ProjectID   string
	MessageID   string
	Role        string
	Snippet     string
	Seq         int64
	CreatedAt   time.Time
}

// messageSearchClient 由 app.Client 实现；UI 通过类型断言取得，
// 因此不必扩充 internal/ui/client.go 里的 Client 接口。不实现时不显示消息结果。
type messageSearchClient interface {
	// SearchMessages 跨所有未归档会话搜索消息正文。
	SearchMessages(ctx context.Context, query string, limit int) ([]MessageSearchHit, error)
}

const (
	// paletteMessageSearchDebounce 是输入停止后到真正发起搜索的等待时间。
	paletteMessageSearchDebounce = 200 * time.Millisecond
	// paletteMessageSearchLimit 是命令面板一次展示的消息结果条数。
	paletteMessageSearchLimit = 20
	// paletteMessageSearchMinRunes 是触发消息搜索的最短查询长度（按 rune 计）。
	paletteMessageSearchMinRunes = 2
)

// shouldSearchMessages 判断查询串是否值得发起消息正文搜索：去掉首尾空白后至少 2 个 rune。
func shouldSearchMessages(query string) bool {
	q := []rune(strings.TrimSpace(query))
	return len(q) >= paletteMessageSearchMinRunes
}

// scheduleMessageSearch 在查询变化后调度一次防抖搜索。
// 查询没变时直接返回；查询过短、或客户端不支持消息搜索时立即清空结果，
// 同时推进序号让仍在飞的旧结果作废。
func scheduleMessageSearch(s *chatState, p *paletteState, query string) {
	if s == nil || p == nil {
		return
	}
	if p.msgPending == query {
		return
	}
	p.msgErr = ""
	p.msgPending = query

	if p.msgTimer != nil {
		p.msgTimer.Stop()
		p.msgTimer = nil
	}
	p.msgSeq++
	seq := p.msgSeq

	q := strings.TrimSpace(query)
	client, supported := s.client.(messageSearchClient)
	if !supported || q == "" || !shouldSearchMessages(query) {
		p.msgHits = nil
		p.msgQuery = ""
		return
	}

	p.msgTimer = time.AfterFunc(paletteMessageSearchDebounce, func() {
		hits, err := client.SearchMessages(context.Background(), q, paletteMessageSearchLimit)
		if err != nil {
			hits = nil
		}
		// 搜索结果只能由 UI 线程写入面板状态：后台结果在下一次构建前应用。
		s.updateWorkspace(func() {
			if p.msgSeq != seq {
				return
			}
			p.msgTimer = nil
			p.msgQuery = q
			p.msgHits = hits
			p.msgErr = ""
			if err != nil {
				p.msgErr = err.Error()
			}
		})
	})
}

// clearMessageSearch 清空消息搜索结果并让在飞的请求作废（面板关闭或查询变短时调用）。
func clearMessageSearch(p *paletteState) {
	if p == nil {
		return
	}
	if p.msgTimer != nil {
		p.msgTimer.Stop()
		p.msgTimer = nil
	}
	p.msgErr = ""
	p.msgPending = ""
	p.msgSeq++
	p.msgQuery = ""
	p.msgHits = nil
}

// paletteMessageItems 把已到位的消息搜索结果转成面板条目。
// 结果必须对应当前查询串才纳入，避免输入变快时闪出上一次查询的旧结果。
func paletteMessageItems(p *paletteState, query string) []paletteItem {
	if p == nil || len(p.msgHits) == 0 {
		return nil
	}
	if !shouldSearchMessages(query) || p.msgQuery != strings.TrimSpace(query) {
		return nil
	}

	items := make([]paletteItem, 0, len(p.msgHits))
	for _, hit := range p.msgHits {
		h := hit
		if h.MessageID == "" || h.ThreadID == "" {
			continue
		}
		// subtitle 是命中所属的会话标题，正文片段放在 title 上。
		subtitle := h.ThreadTitle
		if strings.TrimSpace(subtitle) == "" {
			subtitle = "未命名会话"
		}
		threadID := h.ThreadID
		messageID := h.MessageID
		items = append(items, paletteItem{
			id:       "msg-" + messageID,
			kind:     "message",
			title:    h.Snippet,
			subtitle: subtitle,
			run: func(cs *chatState) {
				cs.closeWorkspace()
				cs.selectThread(threadID)
				cs.requestMessageJump(threadID, messageID)
			},
		})
	}
	return items
}
