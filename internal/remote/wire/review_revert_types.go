package wire

// ThreadsReviewRevertHunkParams identifies one hunk in a fixed review snapshot.
type ThreadsReviewRevertHunkParams struct {
	ThreadID         string      `json:"threadID"`
	Scope            ReviewScope `json:"scope"`
	TurnID           string      `json:"turnID,omitempty"`
	Path             string      `json:"path"`
	BaseRevision     string      `json:"baseRevision"`
	HeadRevision     string      `json:"headRevision"`
	StartLine        int         `json:"startLine"`
	LineCount        int         `json:"lineCount"`
	HunkHash         string      `json:"hunkHash"`
	ExpectedFileHash string      `json:"expectedFileHash"`
}

type ThreadsReviewRevertHunkResult struct {
	FileHash string `json:"fileHash"`
}
