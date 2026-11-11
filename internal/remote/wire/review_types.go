package wire

// ReviewScope 是审阅快照的比较范围。
type ReviewScope string

const (
	ReviewScopeTurn        ReviewScope = "turn"
	ReviewScopeSession     ReviewScope = "session"
	ReviewScopeUncommitted ReviewScope = "uncommitted"
)

// ThreadsReviewGetParams 不带 Path 时只获取文件元数据。
type ThreadsReviewGetParams struct {
	ThreadID string      `json:"threadID"`
	Scope    ReviewScope `json:"scope"`
	TurnID   string      `json:"turnID,omitempty"`
	Path     string      `json:"path,omitempty"`
}

// ReviewFileMeta 的大小单位为字节，哈希指向固定快照中的 Git blob。
type ReviewFileMeta struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath"`
	Status   string `json:"status"`
	Language string `json:"language"`
	OldSize  int64  `json:"oldSize"`
	NewSize  int64  `json:"newSize"`
	OldHash  string `json:"oldHash"`
	NewHash  string `json:"newHash"`
	Binary   bool   `json:"binary"`
	TooLarge bool   `json:"tooLarge"`
	Missing  bool   `json:"missing"`
}

// ReviewFileContent 只在两侧均可完整读取时返回文本；新增和删除的空侧仍为空文本。
type ReviewFileContent struct {
	Path     string `json:"path"`
	OldText  string `json:"oldText"`
	NewText  string `json:"newText"`
	OldHash  string `json:"oldHash"`
	NewHash  string `json:"newHash"`
	Binary   bool   `json:"binary"`
	TooLarge bool   `json:"tooLarge"`
	Missing  bool   `json:"missing"`
}

// ThreadsReviewGetResult 用固定版本区分运行中快照，Unavailable 保留不可用原因。
type ThreadsReviewGetResult struct {
	Scope        ReviewScope        `json:"scope"`
	TurnID       string             `json:"turnID"`
	BaseRevision string             `json:"baseRevision"`
	HeadRevision string             `json:"headRevision"`
	Running      bool               `json:"running"`
	Unavailable  string             `json:"unavailable"`
	Files        []ReviewFileMeta   `json:"files"`
	File         *ReviewFileContent `json:"file,omitempty"`
}
