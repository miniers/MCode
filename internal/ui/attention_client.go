package mcodeui

import (
	"context"
	"time"
)

// AttentionItem 是收件箱里的一条待处理事件，字段与 store.AttentionEvent 一一对应。
// Kind 取 attention.Kind 的字符串形式（如 thread_failed、kanban_review），
// SubjectType 取 attention.SubjectType 的字符串形式（thread、kanban_task、
// automation_run、bee），界面按这两列决定图标与跳转目标。
type AttentionItem struct {
	ID          string
	Kind        string
	SubjectType string
	SubjectID   string
	ProjectID   string
	// ThreadID 用于点击事件后跳转到对应会话，可能为空（例如没有关联会话的自动化运行）。
	ThreadID string
	Title    string
	Detail   string
	// CreatedAt 是事件落库时间，列表按它倒序。
	CreatedAt time.Time
}

// AttentionClient 是待处理事件（收件箱）的读取接口。界面层用类型断言
// client.(AttentionClient) 取用：Client 未实现该接口时不显示收件箱，
// 因此这里的实现可以随装配进度分批落地，界面侧不需要跟着改。
type AttentionClient interface {
	// ListAttention 按创建时间倒序列出未消解事件，limit 小于等于 0 时由实现决定默认条数。
	ListAttention(ctx context.Context, limit int) ([]AttentionItem, error)
	// ResolveAttention 由用户手动消解一条事件，已消解或不存在时返回 nil。
	ResolveAttention(ctx context.Context, id string) error
	// AttentionUnresolvedCount 返回未消解事件总数，用于角标。
	AttentionUnresolvedCount(ctx context.Context) (int, error)
	// WatchAttention 在事件新增或消解时回调，可能来自任意 goroutine；返回注销函数。
	WatchAttention(onChange func()) (stop func())
}
