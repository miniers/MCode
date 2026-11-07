package wire

type RuntimeDraft struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments"`
}

type QueueEditParams struct {
	ThreadID   string        `json:"threadID"`
	Action     string        `json:"action"`
	FollowUpID string        `json:"followUpID,omitempty"`
	Draft      *RuntimeDraft `json:"draft,omitempty"`
}

type QueueEditResult struct {
	ThreadID   string       `json:"threadID"`
	Draft      RuntimeDraft `json:"draft"`
	FollowUpID string       `json:"followUpID,omitempty"`
	Vanished   bool         `json:"vanished"`
}

type LimitTextResult struct {
	ThreadID string `json:"threadID"`
	Text     string `json:"text"`
}

type CompactSuggestion struct {
	ThreadID   string `json:"threadID"`
	UsedTokens int64  `json:"usedTokens"`
	IdleSince  string `json:"idleSince"`
}
