package wire

// 撤销某一轮文件改动的远程扩展方法参数与结果类型。
// 这两个方法由 internal/remote/revert_ext_methods.go 注册，对应桌面端
// Client.PreviewTurnRevert / Client.ExecuteTurnRevert（内部 ui.RevertPreview）。
// 结果按前端展示需要重新组织：不直接暴露 git.RestorePreview，只给出路径、行数、
// 是否会被保留以及数量汇总。

// ThreadsRevertParams 对应 threads.revert.preview 与 threads.revert.apply 的请求参数。
// threadID 与 turnID 都必须是合法 UUID。
type ThreadsRevertParams struct {
	ThreadID string `json:"threadID"`
	TurnID   string `json:"turnID"`
}

// ThreadsRevertFile 是撤销范围内单个文件的状态。
type ThreadsRevertFile struct {
	// Path 是仓库相对路径。
	Path string `json:"path"`
	// Additions 是区间内新增行数，二进制文件记 0。
	Additions int `json:"additions"`
	// Deletions 是区间内删除行数，二进制文件记 0。
	Deletions int `json:"deletions"`
	// Kept 为真表示该文件在轮次结束后又被改过，回退时原样保留。
	Kept bool `json:"kept"`
	// InBase 为假表示该文件是区间内新增的，回退时会被删除。
	InBase bool `json:"inBase"`
	// Restorable 为真表示该文件会参与本次回退。
	Restorable bool `json:"restorable"`
}

// ThreadsRevertPreviewResult 对应 threads.revert.preview 成功返回。
type ThreadsRevertPreviewResult struct {
	ThreadID string `json:"threadID"`
	TurnID   string `json:"turnID"`
	// Base 是比较基准检查点，Finish 是比较终点检查点，便于前端排查范围。
	Base   string `json:"base,omitempty"`
	Finish string `json:"finish,omitempty"`
	// Total 是涉及文件总数，Restorable 与 KeptCount 分别是会回退与会被保留的文件数。
	Total      int `json:"total"`
	Restorable int `json:"restorableCount"`
	KeptCount  int `json:"keptCount"`
	// Kept 是被保留的路径，对应桌面端预览里的「冲突」列表。
	Kept  []string            `json:"kept"`
	Files []ThreadsRevertFile `json:"files"`
}

// ThreadsRevertApplyResult 对应 threads.revert.apply 成功返回。
// 结构与预览一致（回退实际执行的那次预览），另带 ok 标记。
type ThreadsRevertApplyResult struct {
	ThreadsRevertPreviewResult
	OK bool `json:"ok"`
}
