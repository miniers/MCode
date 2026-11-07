package wire

// 待处理收件箱（attention）的远程扩展方法参数与结果类型。
// 这些方法由 internal/remote/attention_ext_methods.go 注册（attention.list / attention.resolve /
// attention.confirmSuggestion），变更推送为 attention.changed。
//
// 存储层 store.AttentionEvent 的 json 标签是 snake_case，且时间是 time.Time，
// 这里另写一份 camelCase 的协议类型，时间统一转成 RFC3339（UTC）字符串。

// AttentionExtItem 是一条待处理事件。
// ResolvedAt 为空指针表示未处理，序列化为 null（不省略字段）。
type AttentionExtItem struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	SubjectType string  `json:"subjectType"`
	SubjectID   string  `json:"subjectID"`
	ProjectID   string  `json:"projectID"`
	ThreadID    string  `json:"threadID"`
	Title       string  `json:"title"`
	Detail      string  `json:"detail"`
	EnteredAt   string  `json:"enteredAt"`
	ResolvedAt  *string `json:"resolvedAt"`
	// Suggestions 是总管对这条审批事项的答复建议，只有等待审批的事件才可能非空。
	Suggestions []AttentionExtSuggestion `json:"suggestions,omitempty"`
}

// AttentionExtSuggestion 是总管对一条待审批请求的建议：只展示给用户，
// 用户在网页端确认后才真正答复请求（attention.confirmSuggestion）。
// 只带展示需要的文字，请求本身仍由 attention.list 的条目给出。
type AttentionExtSuggestion struct {
	// RequestID 是建议对应的待审批请求，确认时原样回传。
	RequestID string `json:"requestID"`
	// Kind 是请求类别（command、file_change、tool、permissions）。
	Kind string `json:"kind"`
	// Title 是请求标题。
	Title string `json:"title"`
	// Detail 是请求详情，不截断，用户要能看全。
	Detail string `json:"detail"`
	// OptionID 是总管建议的答复，如 allow 或 deny。
	OptionID string `json:"optionID"`
	// Reason 是总管给出的理由。
	Reason string `json:"reason"`
}

// AttentionExtListParams 对应 attention.list 请求参数。
// ProjectID 为空表示全部项目；IncludeResolved 为假时只返回未处理事件；
// Limit 缺省或为负时取 100，上限 500。
type AttentionExtListParams struct {
	ProjectID       string `json:"projectID,omitempty"`
	IncludeResolved bool   `json:"includeResolved,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

// AttentionExtListResult 对应 attention.list 成功返回，Items 按创建时间倒序。
type AttentionExtListResult struct {
	Items           []AttentionExtItem `json:"items"`
	UnresolvedCount int                `json:"unresolvedCount"`
}

// AttentionExtResolveParams 对应 attention.resolve 请求参数。
type AttentionExtResolveParams struct {
	ID string `json:"id"`
}

// AttentionExtResolveResult 对应 attention.resolve 成功返回。
type AttentionExtResolveResult struct {
	OK bool `json:"ok"`
}

// AttentionExtConfirmSuggestionParams 对应 attention.confirmSuggestion 请求参数。
// 三项都要与列表里下发的建议一致，其后端只按建议里的答复执行。
type AttentionExtConfirmSuggestionParams struct {
	ThreadID  string `json:"threadID"`
	RequestID string `json:"requestID"`
	OptionID  string `json:"optionID"`
}

// AttentionExtConfirmSuggestionResult 对应 attention.confirmSuggestion 成功返回。
type AttentionExtConfirmSuggestionResult struct {
	OK bool `json:"ok"`
}

// AttentionExtChangedNotification 是 attention.changed 通知的载荷：
// 告诉客户端当前未处理条数，收到后按需重新拉列表。
type AttentionExtChangedNotification struct {
	UnresolvedCount int `json:"unresolvedCount"`
}
