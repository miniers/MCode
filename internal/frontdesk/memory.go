package frontdesk

import "context"

type MemoryHit struct { ID, Content, Title string }
type ProposeRequest struct { ProjectPath, Content, TargetMemoryID string; UserRequested bool }
type ProposeResult struct { ID, Status string }
type MemoryService interface {
 Search(ctx context.Context, projectPath, query string, limit int) ([]MemoryHit, error)
 Get(ctx context.Context, projectPath, id string) (MemoryHit, error)
 Propose(ctx context.Context, req ProposeRequest) (ProposeResult, error)
 Commit(ctx context.Context, id string) (ProposeResult, error)
 Forget(ctx context.Context, projectPath, id string) (bool, error)
}
