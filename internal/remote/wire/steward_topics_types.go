package wire

// StewardOpenParams 对应 steward.open 请求参数。
type StewardOpenParams struct {
	ProjectID string `json:"projectID"`
}

// StewardOpenResult 对应 steward.open 成功返回。
type StewardOpenResult struct {
	ThreadID string `json:"threadID"`
}
