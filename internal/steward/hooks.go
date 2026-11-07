package steward

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"mcode/internal/attention"
	"mcode/internal/mcp"
	"mcode/internal/store"
)

// Hooks 构造 MCP 层使用的总管钩子：总管会话通过 attention_list、attention_resolve、
// thread_status、notify_owner 四个专属工具完成分诊与汇报；总管启用时还可通过四个动作工具
// 催促会话、中断一轮、重跑自动化、移动看板任务，这些动作受宿主护栏约束。threadStatus 与 notify
// 由接线方提供（分别是会话状态查询与向用户发通知的实现）；threadStatus 原样透传，notify 包一层
// 安静时段拦截：时段内只放行与审批、额度、失败事件相关的通知，其余拒绝发送并告知总管不要重试，
// 宿主会在时段结束后再唤醒它汇总。
func (r *Runtime) Hooks(
	threadStatus func(ctx context.Context, projectID string) ([]mcp.ThreadStatusItem, error),
	notify func(ctx context.Context, title, body, threadID string) error,
) *mcp.StewardHooks {
	return &mcp.StewardHooks{
		IsSteward: r.IsSteward,
		ListAttention: func(ctx context.Context, unresolvedOnly bool, projectID string, limit int) ([]mcp.AttentionItem, error) {
			events, err := r.events.List(ctx, store.AttentionEventFilter{
				UnresolvedOnly: unresolvedOnly,
				ProjectID:      strings.TrimSpace(projectID),
				Limit:          limit,
			})
			if err != nil {
				return nil, err
			}
			items := make([]mcp.AttentionItem, 0, len(events))
			for _, ev := range events {
				items = append(items, toAttentionItem(ev))
			}
			return items, nil
		},
		ResolveAttention: func(ctx context.Context, id string) error {
			return r.events.Resolve(ctx, id, attention.ResolvedBySteward)
		},
		ThreadStatus: threadStatus,
		NotifyOwner:  r.quietNotify(notify),
		// 话题相关：主会话用 IsTopic/OpenTopic 开话题，话题会话用 UpdateBrief/StartTopicGoal 推进。
		IsTopic:          r.IsTopic,
		TopicOwnsSubject: r.TopicOwnsSubject,
		OpenTopic:        r.OpenTopic,
		UpdateBrief:      r.UpdateBrief,
		StartTopicGoal:   r.StartTopicGoal,
		// 自动开工与界面的「直接开工」按钮走同一条路径，不依赖编排是否构造。
		ConfirmTopic: r.ConfirmTopic,
		// 账本只给主会话：读全部条目与渲染文本，按 ID 增、改、删。
		LedgerGet: func(ctx context.Context) ([]mcp.LedgerItem, string, error) {
			entries, err := r.Ledger(ctx)
			if err != nil {
				return nil, "", err
			}
			items := make([]mcp.LedgerItem, 0, len(entries))
			for _, entry := range entries {
				items = append(items, toLedgerItem(entry))
			}
			return items, renderLedger(entries, ledgerRenderMaxRunes), nil
		},
		LedgerAdd: func(ctx context.Context, section, text, topicThreadID, subjectType, subjectID string) (mcp.LedgerItem, error) {
			entry, err := r.AddLedgerEntry(ctx, section, text, topicThreadID, LedgerSubject{Type: subjectType, ID: subjectID})
			return toLedgerItem(entry), err
		},
		LedgerEdit: func(ctx context.Context, id, section, text string) (mcp.LedgerItem, error) {
			entry, err := r.UpdateLedgerEntry(ctx, id, section, text)
			return toLedgerItem(entry), err
		},
		LedgerRemove: r.RemoveLedgerEntry,
	}
}

// toLedgerItem 把账本条目转换成 MCP 工具的结果条目；时间格式与 toAttentionItem 一致。
func toLedgerItem(entry LedgerEntry) mcp.LedgerItem {
	return mcp.LedgerItem{
		ID:            entry.ID,
		Section:       entry.Section,
		Text:          entry.Text,
		TopicThreadID: entry.TopicThreadID,
		SubjectType:   entry.SubjectType,
		SubjectID:     entry.SubjectID,
		CreatedAt:     formatTime(entry.CreatedAt),
		UpdatedAt:     formatTime(entry.UpdatedAt),
	}
}

// quietNotifyRejected 是安静时段拦下普通通知时的文案；%s 是安静时段的结束时刻。
const quietNotifyRejected = "当前是安静时段（至 %s），这条先不发；需要你处理的审批、额度和失败会照常提醒。"

// quietNotify 给 notify 包一层安静时段拦截：每次调用现读配置，时段内只放行审批、额度与失败
// 这三类必须立刻知道的通知，其余返回错误且不调用 notify（错误文案让总管不要重试）。
// notify 为 nil 时仍返回 nil，让工具层按未启用处理。
//
// notify_owner 的参数里没有事件类型，而这三类恰好都在待处理事件里有对应事件，
// 所以按工具给的会话 ID 反查该会话当前未消解事件的类型：命中审批、额度或失败才放行，
// 查不到就按普通通知拦下——宁可攒到时段结束，也不在安静时段打扰用户。
func (r *Runtime) quietNotify(notify func(ctx context.Context, title, body, threadID string) error) func(ctx context.Context, title, body, threadID string) error {
	if notify == nil {
		return nil
	}
	return func(ctx context.Context, title, body, threadID string) error {
		if inQuiet, end := r.config().QuietWindow(r.now()); inQuiet {
			if !r.threadHasQuietPassEvent(ctx, threadID) {
				return fmt.Errorf(quietNotifyRejected, end.Format("15:04"))
			}
		}
		return notify(ctx, title, body, threadID)
	}
}

// threadHasQuietPassEvent 报告该会话当前是否有可以在安静时段照常提醒的未消解事件
// （等待审批、额度用尽、执行失败）。threadID 为空或查不到时返回 false。
func (r *Runtime) threadHasQuietPassEvent(ctx context.Context, threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || r.events == nil {
		return false
	}
	events, err := r.events.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true})
	if err != nil {
		log.Printf("steward: 安静时段内查询会话 %s 的未消解事件失败: %v", threadID, err)
		return false
	}
	for _, ev := range events {
		if isQuietPassKind(ev.Kind) && stewardEventThread(ev) == threadID {
			return true
		}
	}
	return false
}

// stewardEventThread 返回事件所属会话：优先 ThreadID，早期事件可能只写了 SubjectType/SubjectID。
func stewardEventThread(ev attention.Event) string {
	if id := strings.TrimSpace(ev.ThreadID); id != "" {
		return id
	}
	if ev.SubjectType == "thread" {
		return strings.TrimSpace(ev.SubjectID)
	}
	return ""
}

// toAttentionItem 把事件转换成 MCP 工具的结果条目；时间统一用 UTC RFC3339，零值输出空串。
func toAttentionItem(ev attention.Event) mcp.AttentionItem {
	return mcp.AttentionItem{
		ID:          ev.ID,
		Kind:        ev.Kind,
		SubjectType: ev.SubjectType,
		SubjectID:   ev.SubjectID,
		ProjectID:   ev.ProjectID,
		ThreadID:    ev.ThreadID,
		Title:       ev.Title,
		Detail:      ev.Detail,
		EnteredAt:   formatTime(ev.EnteredAt),
		CreatedAt:   formatTime(ev.CreatedAt),
		Resolved:    ev.ResolvedAt != nil,
	}
}

// formatTime 把时间格式化为 UTC RFC3339；零值时间返回空串，避免暴露 0001-01-01。
func formatTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
