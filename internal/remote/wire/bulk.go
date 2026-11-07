package wire

import "time"

// --- 批量会话操作 (Bulk Thread Operations) ---

// ThreadsBulkPreviewParams 对应 threads.bulk.preview 请求参数
type ThreadsBulkPreviewParams struct {
	ProjectID      string     `json:"projectID"`
	Before         *time.Time `json:"before,omitempty"`
	IncludePinned  bool       `json:"includePinned"`
	IncludeDormant bool       `json:"includeDormant"`
}

// ThreadsBulkPreviewResult 对应 threads.bulk.preview 成功结果
type ThreadsBulkPreviewResult struct {
	ThreadIDs      []string `json:"threadIDs"`
	SkippedRunning int      `json:"skippedRunning"`
}

// ThreadsBulkArchiveParams 对应 threads.bulk.archive 请求参数
type ThreadsBulkArchiveParams struct {
	ProjectID string   `json:"projectID"`
	ThreadIDs []string `json:"threadIDs"`
}

// ThreadsBulkArchiveResult 对应 threads.bulk.archive 成功结果
type ThreadsBulkArchiveResult struct {
	UndoToken      string   `json:"undoToken"`
	SkippedRunning int      `json:"skippedRunning"`
	ThreadIDs      []string `json:"threadIDs"`
}

// ThreadsBulkMarkDormantParams 对应 threads.bulk.settle 请求参数
type ThreadsBulkMarkDormantParams struct {
	ProjectID string   `json:"projectID"`
	ThreadIDs []string `json:"threadIDs"`
}

// ThreadsBulkMarkDormantResult 对应 threads.bulk.settle 成功结果
type ThreadsBulkMarkDormantResult struct {
	UndoToken      string   `json:"undoToken"`
	SkippedRunning int      `json:"skippedRunning"`
	ThreadIDs      []string `json:"threadIDs"`
}

// ThreadsBulkMarkReadParams 对应 threads.bulk.markRead 请求参数
type ThreadsBulkMarkReadParams struct {
	ProjectID string   `json:"projectID"`
	ThreadIDs []string `json:"threadIDs"`
}

// ThreadsBulkMarkReadResult 对应 threads.bulk.markRead 成功结果
type ThreadsBulkMarkReadResult struct {
	UndoToken string   `json:"undoToken"`
	ThreadIDs []string `json:"threadIDs"`
}

// ThreadsBulkUndoParams 对应 threads.bulk.undo 请求参数
type ThreadsBulkUndoParams struct {
	ProjectID string `json:"projectID"`
	Token     string `json:"token"`
}

// ThreadsBulkUndoResult 对应 threads.bulk.undo 成功结果
type ThreadsBulkUndoResult struct {
	OK bool `json:"ok"`
}
