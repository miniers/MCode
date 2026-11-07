package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"mcode/internal/attention"
	"mcode/internal/harness"
	"mcode/internal/mcp"
	"mcode/internal/remote"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

const (
	// stewardRequestDetailMaxRunes 是 thread_status 里单条请求详情的长度上限，
	// 命令或补丁可能很长，全量塞给总管既费额度也没有必要。
	stewardRequestDetailMaxRunes = 1000
	// stewardSuggestionEventScanLimit 是确认审批事件存在时扫描的未消解事件条数。
	stewardSuggestionEventScanLimit = 500
)

// 建议被拒绝时的固定错误；文本会原样交给总管模型，所以要说明下一步怎么做。
var (
	// errSuggestionRequestGone 表示请求已不在待审批列表：可能已被答复或所在一轮已结束。
	errSuggestionRequestGone = errors.New("这条请求已不在待审批列表，不需要再提建议；请重新用 thread_status 确认")
	// errSuggestionNoInboxEvent 表示收件箱没有对应事件：建议只在事件下展示，这时用户看不到它。
	errSuggestionNoInboxEvent = errors.New("收件箱里没有这个会话的审批事项，建议无法展示给用户；请改用 notify_owner 说明情况")
)

// 用户确认建议失败时的错误；文本会直接展示给用户，所以要说明下一步怎么做。
var (
	// errSuggestionGone 表示这条建议已不存在：请求已被答复、所在一轮已结束或已被清理。
	errSuggestionGone = errors.New("这条建议已失效，请刷新收件箱")
	// errSuggestionChanged 表示总管已重新建议了别的答复：绝不能按新建议替用户执行。
	errSuggestionChanged = errors.New("总管已更新这条建议，请刷新收件箱后重新确认")
)

// approvalSuggestion 是总管对一条请求的建议。
type approvalSuggestion struct {
	optionID string
	reason   string
}

// approvalSuggestionStore 保存总管的审批建议，按会话 ID、请求 ID 两级索引。
// 只放内存：待审批请求本身不落盘，重启后两者一起消失。零值可用，并发安全。
type approvalSuggestionStore struct {
	mu       sync.Mutex
	byThread map[string]map[string]approvalSuggestion
	nextID   int
	watchers map[int]func()
}

// put 登记建议，同一请求重复建议时覆盖旧建议。
func (s *approvalSuggestionStore) put(threadID, requestID string, suggestion approvalSuggestion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byThread == nil {
		s.byThread = make(map[string]map[string]approvalSuggestion)
	}
	if s.byThread[threadID] == nil {
		s.byThread[threadID] = make(map[string]approvalSuggestion)
	}
	s.byThread[threadID][requestID] = suggestion
}

// remove 删除一条建议，报告它之前是否存在。
func (s *approvalSuggestionStore) remove(threadID, requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byThread[threadID][requestID]; !ok {
		return false
	}
	delete(s.byThread[threadID], requestID)
	if len(s.byThread[threadID]) == 0 {
		delete(s.byThread, threadID)
	}
	return true
}

// get 读取一条建议。
func (s *approvalSuggestionStore) get(threadID, requestID string) (approvalSuggestion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	suggestion, ok := s.byThread[threadID][requestID]
	return suggestion, ok
}

// threadIDs 列出有建议的会话。
func (s *approvalSuggestionStore) threadIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.byThread))
	for threadID := range s.byThread {
		ids = append(ids, threadID)
	}
	return ids
}

// requestIDs 列出会话下有建议的请求。
func (s *approvalSuggestionStore) requestIDs(threadID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.byThread[threadID]))
	for requestID := range s.byThread[threadID] {
		ids = append(ids, requestID)
	}
	return ids
}

// watch 注册变更回调，返回可重复调用的取消函数。
func (s *approvalSuggestionStore) watch(fn func()) (stop func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchers == nil {
		s.watchers = make(map[int]func())
	}
	s.nextID++
	id := s.nextID
	s.watchers[id] = fn
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.watchers, id)
	}
}

// notify 在锁外调用全部订阅者。
func (s *approvalSuggestionStore) notify() {
	s.mu.Lock()
	watchers := make([]func(), 0, len(s.watchers))
	for _, fn := range s.watchers {
		watchers = append(watchers, fn)
	}
	s.mu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

// suggestableRequestKind 报告该类请求能否接受审批建议：这几类都只是允许或拒绝一次操作；
// 计划、提交与提问各有自己的答复内容，不能用一个 allow 或 deny 代替用户判断。
func suggestableRequestKind(kind string) bool {
	switch harness.RequestKind(kind) {
	case harness.RequestKindCommand, harness.RequestKindFileChange, harness.RequestKindTool, harness.RequestKindPermissions:
		return true
	}
	return false
}

// SuggestApproval 是总管 approval_suggest 工具的实现：校验后登记建议并刷新收件箱，
// 不执行审批，用户在收件箱确认后才答复请求。
func (c *Client) SuggestApproval(ctx context.Context, threadID, requestID, optionID, reason string) error {
	approvals, err := c.Approvals(ctx, threadID)
	if err != nil {
		return fmt.Errorf("读取待审批请求失败: %w", err)
	}
	index := slices.IndexFunc(approvals, func(a mcodeui.Approval) bool { return a.RequestID == requestID })
	if index < 0 {
		return errSuggestionRequestGone
	}
	approval := approvals[index]
	if !suggestableRequestKind(approval.Kind) {
		return fmt.Errorf("%s 类请求不支持审批建议，请改用 notify_owner 说明情况", approval.Kind)
	}
	if !slices.ContainsFunc(approval.Options, func(o mcodeui.ApprovalOption) bool { return o.ID == optionID }) {
		return fmt.Errorf("这条请求没有 %s 选项", optionID)
	}
	found, err := c.hasThreadApprovalEvent(ctx, threadID)
	if err != nil {
		return fmt.Errorf("读取收件箱失败: %w", err)
	}
	if !found {
		return errSuggestionNoInboxEvent
	}
	c.suggestions.put(threadID, requestID, approvalSuggestion{optionID: optionID, reason: reason})
	traceSuggestionRegistered(c, approval, optionID)
	c.suggestions.notify()
	return nil
}

// ConfirmApprovalSuggestion 是网页端确认建议的实现：按建议里的答复答复请求，
// 不接受调用方自带的选项，避免建议已被更新后按新建议执行。
// 答复成功后建议由 Client.Respond 清除并通知收件箱刷新。
func (c *Client) ConfirmApprovalSuggestion(ctx context.Context, threadID, requestID, optionID string) error {
	suggestion, ok := c.suggestions.get(threadID, requestID)
	if !ok {
		return errSuggestionGone
	}
	if suggestion.optionID != optionID {
		return errSuggestionChanged
	}
	approvals, err := c.Approvals(ctx, threadID)
	if err != nil {
		return fmt.Errorf("读取待审批请求失败: %w", err)
	}
	index := slices.IndexFunc(approvals, func(a mcodeui.Approval) bool { return a.RequestID == requestID })
	if index < 0 {
		// 请求可能已被远程端或一轮结束带走，这条建议已经没有意义，顺手清掉。
		if c.suggestions.remove(threadID, requestID) {
			c.suggestions.notify()
		}
		return errSuggestionGone
	}
	ctx = remote.WithAnswerSource(ctx, remote.AnswerSourceConfirm)
	return c.Respond(ctx, approvals[index], suggestion.optionID)
}

// hasThreadApprovalEvent 报告收件箱里是否有该会话未消解的审批事件。
func (c *Client) hasThreadApprovalEvent(ctx context.Context, threadID string) (bool, error) {
	svc := c.attentionService()
	if svc == nil {
		return false, nil
	}
	events, err := svc.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true, Limit: stewardSuggestionEventScanLimit})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(events, func(ev store.AttentionEvent) bool {
		return ev.Kind == string(attention.KindThreadApproval) && ev.ThreadID == threadID
	}), nil
}

// sortedApprovals 列出会话的待审批请求并按请求 ID 排序，后端存在 map 里，顺序不固定。
func (c *Client) sortedApprovals(ctx context.Context, threadID string) ([]mcodeui.Approval, error) {
	approvals, err := c.Approvals(ctx, threadID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(approvals, func(a, b mcodeui.Approval) int {
		return strings.Compare(a.RequestID, b.RequestID)
	})
	return approvals, nil
}

// stewardPendingRequests 列出会话的待审批请求，供 thread_status 带给总管。
func (c *Client) stewardPendingRequests(ctx context.Context, threadID string) ([]mcp.StewardPendingRequest, error) {
	approvals, err := c.sortedApprovals(ctx, threadID)
	if err != nil {
		return nil, err
	}
	pending := make([]mcp.StewardPendingRequest, 0, len(approvals))
	for _, approval := range approvals {
		options := make([]string, 0, len(approval.Options))
		for _, option := range approval.Options {
			options = append(options, option.ID)
		}
		item := mcp.StewardPendingRequest{
			RequestID: approval.RequestID,
			Kind:      approval.Kind,
			Title:     approval.Title,
			Detail:    truncateStewardDetail(approval.Detail),
			Options:   options,
		}
		if suggestion, ok := c.suggestions.get(threadID, approval.RequestID); ok {
			item.Suggestion = suggestion.optionID
		}
		pending = append(pending, item)
	}
	return pending, nil
}

// truncateStewardDetail 按字符截断请求详情，避免切坏多字节字符。
func truncateStewardDetail(detail string) string {
	if utf8.RuneCountInString(detail) <= stewardRequestDetailMaxRunes {
		return detail
	}
	return string([]rune(detail)[:stewardRequestDetailMaxRunes]) + "…"
}

// pendingApprovalSuggestions 按会话列出仍待审批的建议，顺带清掉请求已不在的记录：
// 请求也可能经远程端或随一轮结束而消失，这些路径不经过 Client.Respond。
func (c *Client) pendingApprovalSuggestions(ctx context.Context) map[string][]mcodeui.AttentionSuggestion {
	threadIDs := c.suggestions.threadIDs()
	if len(threadIDs) == 0 {
		return nil
	}
	result := make(map[string][]mcodeui.AttentionSuggestion, len(threadIDs))
	for _, threadID := range threadIDs {
		// 取不到请求只会是会话已不存在，这时建议也一并作废。
		approvals, _ := c.sortedApprovals(ctx, threadID)
		for _, approval := range approvals {
			if suggestion, ok := c.suggestions.get(threadID, approval.RequestID); ok {
				result[threadID] = append(result[threadID], mcodeui.AttentionSuggestion{
					Approval: approval,
					OptionID: suggestion.optionID,
					Reason:   suggestion.reason,
				})
			}
		}
		for _, requestID := range c.suggestions.requestIDs(threadID) {
			if !slices.ContainsFunc(approvals, func(a mcodeui.Approval) bool { return a.RequestID == requestID }) {
				c.suggestions.remove(threadID, requestID)
			}
		}
	}
	return result
}

// traceSuggestionRegistered 登记一条总管建议，供之后统计"建议与用户实际选择是否一致"。
// 写库是尽力而为：失败只记日志，绝不影响 SuggestApproval 的返回值。
func traceSuggestionRegistered(c *Client, approval mcodeui.Approval, optionID string) {
	if c == nil || c.backend == nil {
		return
	}
	remote.TraceSuggestionRegistered(c.backend, store.StewardApprovalScore{
		RequestID:       approval.RequestID,
		ThreadID:        approval.ThreadID,
		TurnID:          approval.TurnID,
		Kind:            approval.Kind,
		SuggestedOption: optionID,
		SuggestedRole:   remote.ApprovalRoleFamily(stewardOptionRole(approval.Options, optionID), optionID),
	})
}

// stewardOptionRole 从审批选项里找出建议选项的 Role；找不到返回空串，交给归一函数按选项 ID 判断。
func stewardOptionRole(options []mcodeui.ApprovalOption, optionID string) string {
	for _, option := range options {
		if option.ID == optionID {
			return option.Role
		}
	}
	return ""
}
