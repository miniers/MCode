package remote

import (
	"context"

	"mcode/internal/remote/wire"
)

// Backend 定义远程服务后端逻辑接口。
// 各方法首个参数为 context.Context，出错时可返回 *wire.RPCError 指定特定错误码，其他错误将被自动映射为 InternalError。
type Backend interface {
	Capabilities(ctx context.Context) (wire.Capabilities, error)
	KanbanBoard(ctx context.Context) (wire.KanbanBoard, error)
	ListProjects(ctx context.Context) (wire.ProjectList, error)
	ListThreads(ctx context.Context, params wire.ThreadListParams) (wire.ThreadList, error)
	GetThread(ctx context.Context, threadID string) (wire.ThreadSummary, error)
	ThreadHistory(ctx context.Context, params wire.HistoryParams) (wire.HistoryPage, error)
	Subscribe(ctx context.Context, c Conn, threadID string) (wire.SubscribeResult, error)
	Unsubscribe(ctx context.Context, c Conn, threadID string) error
	SendMessage(ctx context.Context, params wire.SendMessageParams) (wire.SendMessageResult, error)
	Interrupt(ctx context.Context, params wire.InterruptParams) (wire.InterruptResult, error)
	CreateThread(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error)
	UpdateThread(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error)
	ActionStatus(ctx context.Context, actionID string) (wire.ActionStatusResult, error)
	ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error)
	RespondRequest(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error)
	ListProviders(ctx context.Context) (wire.ProvidersListResult, error)
	GetDiff(ctx context.Context, params wire.ThreadsGetDiffParams) (wire.ThreadsGetDiffResult, error)
	RemoveConn(c Conn)
}
