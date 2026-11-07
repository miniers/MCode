package wire

// --- 消息全文搜索（扩展方法 messages.search）---

// MessageSearchParams 对应 messages.search 请求参数；Limit 省略时默认 30，上限 100。
type MessageSearchParams struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

// MessageSearchHit 是一条命中的消息，snippet 为命中位置附近的片段。
type MessageSearchHit struct {
	ThreadID    string `json:"threadID"`
	ThreadTitle string `json:"threadTitle"`
	MessageID   string `json:"messageID"`
	Role        string `json:"role"`
	Snippet     string `json:"snippet"`
	CreatedAt   string `json:"createdAt"`
}

// MessageSearchResult 对应 messages.search 成功结果。
type MessageSearchResult struct {
	Hits []MessageSearchHit `json:"hits"`
}
