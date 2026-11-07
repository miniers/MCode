package app

import (
	"context"

	"mcode/internal/attention"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// Client 在待处理事件服务注入后实现界面的收件箱读取接口；未注入时
// 各方法返回空结果，界面按类型断言决定是否显示收件箱。
var _ mcodeui.AttentionClient = (*Client)(nil)

// SetAttention 注入待处理事件服务。与看板、自动化服务共用同一把锁，
// 避免为每个可选服务各加一把只读一次的锁。
func (c *Client) SetAttention(svc *attention.Service) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	c.attention = svc
}

// attentionService 取当前注入的待处理事件服务，未注入时返回 nil。
func (c *Client) attentionService() *attention.Service {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	return c.attention
}

// attentionKinds 返回收件箱当前应列出的事件类型白名单：总管启用时只列必须用户亲手处理的四类，
// 未启用时返回 nil（不按类型过滤，保持原行为）。配置读取失败按未启用处理。
// 列表与角标计数都取这一个函数，保证两边口径一致。
func (c *Client) attentionKinds() []string {
	cfg, ok := c.loadConfig()
	if !ok || !cfg.Orchestration.Steward.Enabled {
		return nil
	}
	return attention.OwnerActionKindNames()
}

// ListAttention 按创建时间倒序列出未消解事件。
func (c *Client) ListAttention(ctx context.Context, limit int) ([]mcodeui.AttentionItem, error) {
	svc := c.attentionService()
	if svc == nil {
		return nil, nil
	}
	events, err := svc.List(ctx, store.AttentionEventFilter{
		UnresolvedOnly: true,
		Kinds:          c.attentionKinds(),
		Limit:          limit,
	})
	if err != nil {
		return nil, err
	}
	suggestions := c.pendingApprovalSuggestions(ctx)
	items := make([]mcodeui.AttentionItem, 0, len(events))
	for _, ev := range events {
		item := attentionEventToUI(ev)
		if ev.Kind == string(attention.KindThreadApproval) {
			item.Suggestions = suggestions[ev.ThreadID]
		}
		items = append(items, item)
	}
	return items, nil
}

// ResolveAttention 按用户手动消解一条事件。
func (c *Client) ResolveAttention(ctx context.Context, id string) error {
	svc := c.attentionService()
	if svc == nil || id == "" {
		return nil
	}
	return svc.Resolve(ctx, id, attention.ResolvedByUser)
}

// AttentionUnresolvedCount 返回未消解事件总数，口径与 ListAttention 一致。
func (c *Client) AttentionUnresolvedCount(ctx context.Context) (int, error) {
	svc := c.attentionService()
	if svc == nil {
		return 0, nil
	}
	return svc.UnresolvedCountByKinds(ctx, c.attentionKinds())
}

// WatchAttention 订阅待处理事件变更：新增、自动消解、手动消解以及总管审批建议的增减都会回调 onChange。
func (c *Client) WatchAttention(onChange func()) (stop func()) {
	svc := c.attentionService()
	if svc == nil || onChange == nil {
		return func() {}
	}
	stopEvents := svc.Watch(func(attention.Event) {
		onChange()
	})
	stopSuggestions := c.suggestions.watch(onChange)
	return func() {
		stopEvents()
		stopSuggestions()
	}
}

// attentionEventToUI 把存储层事件转成界面条目。
func attentionEventToUI(ev store.AttentionEvent) mcodeui.AttentionItem {
	return mcodeui.AttentionItem{
		ID:          ev.ID,
		Kind:        ev.Kind,
		SubjectType: ev.SubjectType,
		SubjectID:   ev.SubjectID,
		ProjectID:   ev.ProjectID,
		ThreadID:    ev.ThreadID,
		Title:       ev.Title,
		Detail:      ev.Detail,
		CreatedAt:   ev.CreatedAt,
	}
}
