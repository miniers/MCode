package frontdesk

import "context"

// MemoryHit 描述一条记忆。
type MemoryHit struct{ ID, Content, Title string }

// ProposeRequest 描述记忆写入请求。
type ProposeRequest struct {
	ProjectPath, Content, TargetMemoryID string
	UserRequested                        bool
}

// ProposeResult 描述记忆提议的结果。
type ProposeResult struct{ ID, Status string }

// MemoryService 提供记忆读写操作。
type MemoryService interface {
	Search(ctx context.Context, projectPath, query string, limit int) ([]MemoryHit, error)
	Get(ctx context.Context, projectPath, id string) (MemoryHit, error)
	Propose(ctx context.Context, req ProposeRequest) (ProposeResult, error)
	Commit(ctx context.Context, id string) (ProposeResult, error)
}
