package mcodeui

import (
	"context"

	"mcode/internal/git"
)

// ReviewSnapshot 包含固定版本的文件列表，以及按需获取的完整文本。
type ReviewSnapshot struct {
	Scope        string
	TurnID       string
	Number       int
	BaseRevision string
	HeadRevision string
	Running      bool
	Unavailable  string
	Files        []ReviewFile
	File         *ReviewFileContent
}

// ReviewFile 是审阅文件的元数据，大小单位为字节。
type ReviewFile struct {
	Path     string
	OrigPath string
	Status   string
	Language string
	OldSize  int64
	NewSize  int64
	OldHash  string
	NewHash  string
	Binary   bool
	TooLarge bool
	Missing  bool
	// Additions 与 Deletions 是增删行数，旧服务端不提供时为 0。
	Additions, Deletions int
}

// ReviewFileContent 的不可用标记为真时不包含正文。
type ReviewFileContent struct {
	Path     string
	OldText  string
	NewText  string
	OldHash  string
	NewHash  string
	Binary   bool
	TooLarge bool
	Missing  bool
}

// reviewClient 是可选能力，旧 Client 实现不必提供审阅接口。
type reviewClient interface {
	ReviewGet(ctx context.Context, threadID, scope, turnID, path string) (ReviewSnapshot, error)
}

// reviewAtClient 读取单个文件时沿用列表的 head；工作区范围不必每个文件重新快照。
type reviewAtClient interface {
	ReviewGetAt(ctx context.Context, threadID, scope, turnID, head, path string) (ReviewSnapshot, error)
}

type reviewPRClient interface {
	ReviewGetPR(ctx context.Context, threadID string, number int, path string) (ReviewSnapshot, error)
	GitPRSubmitReview(ctx context.Context, threadID string, number int, event, body string, comments []git.PRReviewComment) error
}

// reviewPRAIClient 生成行内审查草稿，旧客户端不必提供。
type reviewPRAIClient interface {
	GitPRAIReview(ctx context.Context, threadID string, number int) ([]git.PRReviewComment, error)
}

func getReview(ctx context.Context, client reviewClient, threadID, scope, turnID string, number int, path string) (ReviewSnapshot, error) {
	return getReviewAt(ctx, client, threadID, scope, turnID, number, "", path)
}

// reviewBackendScope 将 UI 审阅范围映射回后端/远程协议范围名（"all" 对应后端的 "uncommitted"）。
func reviewBackendScope(scope string) string {
	if scope == "all" {
		return "uncommitted"
	}
	return scope
}

// getReviewAt 的 head 非空时固定读取列表时的快照，用于卡片正文。
func getReviewAt(ctx context.Context, client reviewClient, threadID, scope, turnID string, number int, head, path string) (ReviewSnapshot, error) {
	backendScope := reviewBackendScope(scope)
	if at, ok := client.(reviewAtClient); ok && head != "" && path != "" && scope != "pr" {
		return at.ReviewGetAt(ctx, threadID, backendScope, turnID, head, path)
	}
	if scope == "pr" {
		if pr, ok := client.(reviewPRClient); ok {
			return pr.ReviewGetPR(ctx, threadID, number, path)
		}
		return ReviewSnapshot{Scope: scope, Number: number, Unavailable: "PR 审阅不可用"}, nil
	}
	return client.ReviewGet(ctx, threadID, backendScope, turnID, path)
}

type ReviewRevertHunkParams struct {
	ThreadID, Scope, TurnID, Path string
	BaseRevision, HeadRevision    string
	StartLine, LineCount          int
	HunkHash, ExpectedFileHash    string
}

type ReviewRevertHunkResult struct{ FileHash string }

type reviewHunkReverter interface {
	ReviewRevertHunk(context.Context, ReviewRevertHunkParams) (ReviewRevertHunkResult, error)
}
