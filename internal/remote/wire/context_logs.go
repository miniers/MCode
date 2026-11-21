package wire

// 项目知识库只读文档与实时命令/脚本日志订阅流的 JSON-RPC 线协议类型定义。
// 与 docs/remote-control.schema.json 中的 RemoteContext* / RemoteLogs* 定义保持一致。

// RemoteContextDocSummary 描述项目知识库中单篇文档的摘要信息。
type RemoteContextDocSummary struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Standard  bool   `json:"standard"`
	Exists    bool   `json:"exists"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// RemoteContextListDocsParams 对应 context.listDocs 请求参数。
type RemoteContextListDocsParams struct {
	ProjectID string `json:"projectID"`
}

// RemoteContextListDocsResult 对应 context.listDocs 成功返回。
type RemoteContextListDocsResult struct {
	ProjectID string                    `json:"projectID"`
	Docs      []RemoteContextDocSummary `json:"docs"`
}

// RemoteContextReadDocParams 对应 context.readDoc 请求参数。
type RemoteContextReadDocParams struct {
	ProjectID string `json:"projectID"`
	Name      string `json:"name"`
}

// RemoteContextReadDocResult 对应 context.readDoc 成功返回。
type RemoteContextReadDocResult struct {
	ProjectID string `json:"projectID"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Standard  bool   `json:"standard"`
	Exists    bool   `json:"exists"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// RemoteLogStreamInfo 描述单条工具调用或后台脚本的日志流元信息。
// Source 取值 "tool" 或 "script"；Status 取值 "running" / "done" / "error"。
type RemoteLogStreamInfo struct {
	StreamID   string `json:"streamID"`
	Source     string `json:"source"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// RemoteLogChunk 是一条增量日志片段。
type RemoteLogChunk struct {
	Seq       int    `json:"seq"`
	StreamID  string `json:"streamID"`
	Source    string `json:"source"`
	Text      string `json:"text"`
	Timestamp string `json:"ts"`
}

// RemoteLogsSubscribeParams 对应 logs.subscribe 请求参数。
type RemoteLogsSubscribeParams struct {
	ThreadID string `json:"threadID"`
}

// RemoteLogsSubscribeResult 对应 logs.subscribe 成功返回。
type RemoteLogsSubscribeResult struct {
	ThreadID  string                `json:"threadID"`
	Sequence  int                   `json:"sequence"`
	Streams   []RemoteLogStreamInfo `json:"streams"`
	Chunks    []RemoteLogChunk      `json:"chunks"`
	Truncated bool                  `json:"truncated"`
}

// RemoteLogsUnsubscribeParams 对应 logs.unsubscribe 请求参数。
type RemoteLogsUnsubscribeParams struct {
	ThreadID string `json:"threadID"`
}

// RemoteLogsUnsubscribeResult 对应 logs.unsubscribe 成功返回。
type RemoteLogsUnsubscribeResult struct {
	Unsubscribed bool `json:"unsubscribed"`
}

// RemoteLogsEventNotificationParams 是 logs.event 服务端推送通知的载荷。
type RemoteLogsEventNotificationParams struct {
	ThreadID string               `json:"threadID"`
	Sequence int                  `json:"sequence"`
	Stream   *RemoteLogStreamInfo `json:"stream,omitempty"`
	Chunks   []RemoteLogChunk     `json:"chunks"`
}
