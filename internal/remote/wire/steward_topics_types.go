package wire

// StewardOpenParams 对应 steward.open 请求参数。
type StewardOpenParams struct {
	ProjectID string `json:"projectID"`
}

// StewardOpenResult 对应 steward.open 成功返回。
type StewardOpenResult struct {
	ThreadID string `json:"threadID"`
}

// StewardResetParams 对应 steward.reset 请求参数。
type StewardResetParams = StewardOpenParams

// StewardResetResult 对应 steward.reset 成功返回。
type StewardResetResult = StewardOpenResult
