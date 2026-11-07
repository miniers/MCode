package app

// 本文件把桌面端的待处理收件箱接到远程扩展方法上（internal/remote/attention_ext_methods.go）：
// attention.list / attention.resolve / attention.confirmSuggestion 只依赖这里注入的 "attention" 服务。
// 列表里的审批建议来自总管（approval_suggest），确认时再按建议答复请求。
// attention.changed 推送不在这里，见 RemoteService.SetAttentionWatcher。

import (
	"context"
	"errors"
	"time"

	"mcode/internal/attention"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// attentionResolvedByRemote 是网页端手动消解时记录的消解者；attention 包没有对应常量。
const attentionResolvedByRemote = "remote"

// remoteAttentionExt 把 *Client 上的待处理事件服务适配为远程协议接口。
type remoteAttentionExt struct {
	client *Client
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		client := a.remoteClient()
		if client == nil || client.attentionService() == nil {
			return
		}
		a.backend.SetService("attention", remoteAttentionExt{client: client})
	})
}

// List 按项目列出事件（按创建时间倒序）；IncludeResolved 为假时只列未处理事件。
// 等待审批的事件会带上总管的建议，用户确认后走 ConfirmSuggestion。
func (x remoteAttentionExt) List(ctx context.Context, params wire.AttentionExtListParams) (wire.AttentionExtListResult, error) {
	svc := x.client.attentionService()
	if svc == nil {
		return wire.AttentionExtListResult{}, errors.New("待处理事件服务不可用")
	}
	events, err := svc.List(ctx, store.AttentionEventFilter{
		UnresolvedOnly: !params.IncludeResolved,
		ProjectID:      params.ProjectID,
		Kinds:          x.client.attentionKinds(),
		Limit:          params.Limit,
	})
	if err != nil {
		return wire.AttentionExtListResult{}, err
	}
	suggestions := x.client.pendingApprovalSuggestions(ctx)
	items := make([]wire.AttentionExtItem, 0, len(events))
	for _, ev := range events {
		item := attentionEventToWire(ev)
		if ev.Kind == string(attention.KindThreadApproval) {
			item.Suggestions = attentionSuggestionsToWire(suggestions[ev.ThreadID])
		}
		items = append(items, item)
	}
	return wire.AttentionExtListResult{Items: items}, nil
}

// Resolve 以网页端身份手动消解一条事件。
func (x remoteAttentionExt) Resolve(ctx context.Context, id string) error {
	svc := x.client.attentionService()
	if svc == nil {
		return errors.New("待处理事件服务不可用")
	}
	return svc.Resolve(ctx, id, attentionResolvedByRemote)
}

// ConfirmSuggestion 用户确认总管的审批建议：后端校验建议仍然存在且未被更新后才答复请求。
func (x remoteAttentionExt) ConfirmSuggestion(ctx context.Context, threadID, requestID, optionID string) error {
	return x.client.ConfirmApprovalSuggestion(ctx, threadID, requestID, optionID)
}

// UnresolvedCount 返回未处理事件条数，口径与 List 一致（总管启用时只算需要用户亲手处理的类型）。
func (x remoteAttentionExt) UnresolvedCount(ctx context.Context) (int, error) {
	svc := x.client.attentionService()
	if svc == nil {
		return 0, errors.New("待处理事件服务不可用")
	}
	return svc.UnresolvedCountByKinds(ctx, x.client.attentionKinds())
}

// attentionEventToWire 把存储层事件转成协议类型；时间统一为 RFC3339（UTC），未消解时 ResolvedAt 为 nil。
func attentionEventToWire(ev store.AttentionEvent) wire.AttentionExtItem {
	item := wire.AttentionExtItem{
		ID:          ev.ID,
		Kind:        ev.Kind,
		SubjectType: ev.SubjectType,
		SubjectID:   ev.SubjectID,
		ProjectID:   ev.ProjectID,
		ThreadID:    ev.ThreadID,
		Title:       ev.Title,
		Detail:      ev.Detail,
		EnteredAt:   ev.EnteredAt.UTC().Format(time.RFC3339),
	}
	if ev.ResolvedAt != nil {
		resolved := ev.ResolvedAt.UTC().Format(time.RFC3339)
		item.ResolvedAt = &resolved
	}
	return item
}

// attentionSuggestionsToWire 把总管的建议转成协议类型；
// 详情不截断，用户要能看到完整的请求内容，没有建议时返回 nil（字段随之省略）。
func attentionSuggestionsToWire(suggestions []mcodeui.AttentionSuggestion) []wire.AttentionExtSuggestion {
	if len(suggestions) == 0 {
		return nil
	}
	out := make([]wire.AttentionExtSuggestion, 0, len(suggestions))
	for _, sug := range suggestions {
		out = append(out, wire.AttentionExtSuggestion{
			RequestID: sug.Approval.RequestID,
			Kind:      sug.Approval.Kind,
			Title:     sug.Approval.Title,
			Detail:    sug.Approval.Detail,
			OptionID:  sug.OptionID,
			Reason:    sug.Reason,
		})
	}
	return out
}
