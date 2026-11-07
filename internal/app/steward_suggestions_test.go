package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"mcode/internal/attention"
	"mcode/internal/harness"
	"mcode/internal/mcp"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// suggestionMockHarness 一轮内同时发起一条命令审批与一条计划审批并挂起，答复后结束。
type suggestionMockHarness struct {
	decisions chan harness.Decision
}

func (h *suggestionMockHarness) Kind() harness.Kind { return "suggestion-mock" }

func (h *suggestionMockHarness) Run(ctx context.Context, turn harness.Turn, _ func(harness.Event)) error {
	interactor, ok := turn.Approver.(harness.Interactor)
	if !ok {
		return errors.New("approver does not implement Interactor")
	}
	ask := func(req harness.ApprovalRequest) {
		resp, err := interactor.Interact(ctx, req)
		if err == nil {
			h.decisions <- resp.Decision
		}
	}
	go ask(harness.ApprovalRequest{ToolName: "Bash", Summary: "rm -rf build", Kind: harness.RequestKindPlan, Plan: "计划正文"})
	ask(harness.ApprovalRequest{ToolName: "Bash", Summary: "rm -rf build", Kind: harness.RequestKindCommand})
	<-ctx.Done()
	return ctx.Err()
}

// suggestionFixture 是挂着一条命令审批与一条计划审批的真实后端。
type suggestionFixture struct {
	client  *Client
	backend *remote.MemoryBackend
	svc     *attention.Service
	mock    *suggestionMockHarness
	command string // 命令审批的请求 ID
	plan    string // 计划审批的请求 ID
}

// newSuggestionFixture 构造后端、待处理事件服务与客户端，并等到两条审批都登记。
func newSuggestionFixture(t *testing.T) *suggestionFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := remote.NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	mock := &suggestionMockHarness{decisions: make(chan harness.Decision, 2)}
	b.SetHarness(mock, dir, harness.ModeSupervised)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "t", ProjectID: "p", Provider: "suggestion-mock"}, nil)

	st, err := store.Open(filepath.Join(t.TempDir(), "suggestions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := attention.New(st, attention.Options{})

	client := NewClient(b)
	client.SetAttention(svc)

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "t", Text: "开始"}); err != nil {
		t.Fatalf("send message: %v", err)
	}
	fx := &suggestionFixture{client: client, backend: b, svc: svc, mock: mock}
	waitSwarm(t, func() bool {
		res, err := b.ListRequests(ctx, "t")
		if err != nil || len(res.Requests) != 2 {
			return false
		}
		for _, req := range res.Requests {
			if req.Kind == string(harness.RequestKindCommand) {
				fx.command = req.RequestID
			} else {
				fx.plan = req.RequestID
			}
		}
		return true
	})
	return fx
}

// recordApprovalEvent 写一条该会话的审批事件，建议要挂在它下面才能展示给用户。
func (fx *suggestionFixture) recordApprovalEvent(t *testing.T) {
	t.Helper()
	_, _, err := fx.svc.Record(context.Background(), attention.Input{
		Kind:        attention.KindThreadApproval,
		SubjectType: attention.SubjectThread,
		SubjectID:   "t",
		ThreadID:    "t",
		Title:       "等待审批",
	})
	if err != nil {
		t.Fatalf("record event: %v", err)
	}
}

// 收件箱没有审批事件时拒绝建议：用户看不到它，总管不能以为已经展示过。
func TestSuggestApprovalRequiresInboxEvent(t *testing.T) {
	fx := newSuggestionFixture(t)
	err := fx.client.SuggestApproval(context.Background(), "t", fx.command, "allow", "只删构建产物")
	if !errors.Is(err, errSuggestionNoInboxEvent) {
		t.Fatalf("没有事件时应拒绝，实际: %v", err)
	}
}

// 校验请求是否存在、类别与选项；被拒绝的建议不进入收件箱。
func TestSuggestApprovalValidation(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)
	ctx := context.Background()

	if err := fx.client.SuggestApproval(ctx, "t", "no-such-request", "allow", "理由"); !errors.Is(err, errSuggestionRequestGone) {
		t.Fatalf("不存在的请求应拒绝，实际: %v", err)
	}
	if err := fx.client.SuggestApproval(ctx, "t", fx.plan, "allow", "理由"); err == nil || !strings.Contains(err.Error(), "不支持审批建议") {
		t.Fatalf("计划类请求应拒绝，实际: %v", err)
	}
	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "reject", "理由"); err == nil || !strings.Contains(err.Error(), "没有 reject 选项") {
		t.Fatalf("不存在的选项应拒绝，实际: %v", err)
	}
	if err := fx.client.SuggestApproval(ctx, "missing", fx.command, "allow", "理由"); err == nil {
		t.Fatal("不存在的会话应拒绝")
	}
	items, err := fx.client.ListAttention(ctx, 10)
	if err != nil {
		t.Fatalf("list attention: %v", err)
	}
	if len(items) != 1 || len(items[0].Suggestions) != 0 {
		t.Fatalf("被拒绝的建议不应出现: %+v", items)
	}
}

// 完整链路：建议只展示不执行，用户确认后才答复请求，随后建议消失。
func TestSuggestApprovalConfirmFlow(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)
	ctx := context.Background()

	var changes atomic.Int32
	stop := fx.client.WatchAttention(func() { changes.Add(1) })
	defer stop()

	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "deny", "旧理由"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "allow", "只删构建产物"); err != nil {
		t.Fatalf("suggest again: %v", err)
	}
	if got := changes.Load(); got != 2 {
		t.Fatalf("每次建议都应通知收件箱刷新，实际 %d 次", got)
	}

	// 只登记建议：两条请求都还在等用户。
	res, err := fx.backend.ListRequests(ctx, "t")
	if err != nil || len(res.Requests) != 2 {
		t.Fatalf("建议不应答复请求: %+v, %v", res.Requests, err)
	}

	items, err := fx.client.ListAttention(ctx, 10)
	if err != nil {
		t.Fatalf("list attention: %v", err)
	}
	if len(items) != 1 || len(items[0].Suggestions) != 1 {
		t.Fatalf("审批事件应带一条建议: %+v", items)
	}
	sug := items[0].Suggestions[0]
	if sug.OptionID != "allow" || sug.Reason != "只删构建产物" || sug.Approval.RequestID != fx.command || sug.Approval.RunID == "" {
		t.Fatalf("建议内容不对: %+v", sug)
	}

	pending, err := fx.client.stewardPendingRequests(ctx, "t")
	if err != nil {
		t.Fatalf("pending requests: %v", err)
	}
	command := slices.IndexFunc(pending, func(p mcp.StewardPendingRequest) bool { return p.RequestID == fx.command })
	if command < 0 || pending[command].Suggestion != "allow" || pending[command].Kind != "command" {
		t.Fatalf("thread_status 应带上建议: %+v", pending)
	}
	if !slices.Contains(pending[command].Options, "allow_always") {
		t.Fatalf("thread_status 应列出全部选项: %+v", pending[command])
	}

	if err := fx.client.Respond(ctx, sug.Approval, sug.OptionID); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if decision := <-fx.mock.decisions; decision != harness.DecisionAllow {
		t.Fatalf("确认后应按建议答复，实际 %v", decision)
	}
	if got := changes.Load(); got != 3 {
		t.Fatalf("确认后应通知收件箱刷新，实际 %d 次", got)
	}
	if _, ok := fx.client.suggestions.get("t", fx.command); ok {
		t.Fatal("确认后建议应被清除")
	}
}

// 网页端确认建议：后端按建议答复请求，随后建议与收件箱一起刷新。
func TestConfirmApprovalSuggestionFlow(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)
	ctx := context.Background()

	var changes atomic.Int32
	stop := fx.client.WatchAttention(func() { changes.Add(1) })
	defer stop()

	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "allow", "只删构建产物"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if err := fx.client.ConfirmApprovalSuggestion(ctx, "t", fx.command, "allow"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if decision := <-fx.mock.decisions; decision != harness.DecisionAllow {
		t.Fatalf("确认后应按建议答复，实际 %v", decision)
	}
	if got := changes.Load(); got != 2 {
		t.Fatalf("建议与确认应各通知一次，实际 %d 次", got)
	}
	if _, ok := fx.client.suggestions.get("t", fx.command); ok {
		t.Fatal("确认后建议应被清除")
	}
}

// 建议的选项与确认参数不一致时拒绝执行：不能按总管改过的新建议替用户做主。
func TestConfirmApprovalSuggestionChanged(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)
	ctx := context.Background()

	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "deny", "先别放行"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	err := fx.client.ConfirmApprovalSuggestion(ctx, "t", fx.command, "allow")
	if !errors.Is(err, errSuggestionChanged) {
		t.Fatalf("选项不一致应返回 errSuggestionChanged，实际: %v", err)
	}
	res, listErr := fx.backend.ListRequests(ctx, "t")
	if listErr != nil || len(res.Requests) != 2 {
		t.Fatalf("不该答复请求，实际剩 %d 条：%v", len(res.Requests), listErr)
	}
	if got := len(fx.mock.decisions); got != 0 {
		t.Fatalf("不该送出任何决定，实际 %d 条", got)
	}
	if sug, ok := fx.client.suggestions.get("t", fx.command); !ok || sug.optionID != "deny" {
		t.Fatalf("建议应保持原样：%+v, %v", sug, ok)
	}
}

// 没有建议时确认直接失效，避免网页端拿着旧列表乱点。
func TestConfirmApprovalSuggestionGone(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)

	err := fx.client.ConfirmApprovalSuggestion(context.Background(), "t", fx.command, "allow")
	if !errors.Is(err, errSuggestionGone) {
		t.Fatalf("没有建议时应返回 errSuggestionGone，实际: %v", err)
	}
}

// 请求绕过 Client.Respond 消失后确认：返回失效并顺手清掉建议。
func TestConfirmApprovalSuggestionClearedWhenRequestGone(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)
	ctx := context.Background()

	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "allow", "理由"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	approvals, err := fx.client.Approvals(ctx, "t")
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	index := slices.IndexFunc(approvals, func(a mcodeui.Approval) bool { return a.RequestID == fx.command })
	if index < 0 {
		t.Fatalf("应能找到待审批请求：%+v", approvals)
	}
	deny := "deny"
	if _, err := fx.backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID: "t", RunID: approvals[index].RunID, RequestID: fx.command, OptionID: &deny,
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if err := fx.client.ConfirmApprovalSuggestion(ctx, "t", fx.command, "allow"); !errors.Is(err, errSuggestionGone) {
		t.Fatalf("请求已消失应返回 errSuggestionGone，实际: %v", err)
	}
	if _, ok := fx.client.suggestions.get("t", fx.command); ok {
		t.Fatal("请求消失后建议应被清除")
	}
}

// 请求绕过 Client.Respond 消失时（例如远程端答复），下次列出收件箱时清掉建议。
func TestPendingSuggestionsPrunedWhenRequestGone(t *testing.T) {
	fx := newSuggestionFixture(t)
	fx.recordApprovalEvent(t)
	ctx := context.Background()

	if err := fx.client.SuggestApproval(ctx, "t", fx.command, "allow", "理由"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	approvals, err := fx.client.Approvals(ctx, "t")
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	index := slices.IndexFunc(approvals, func(a mcodeui.Approval) bool { return a.RequestID == fx.command })
	deny := "deny"
	if _, err := fx.backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID: "t", RunID: approvals[index].RunID, RequestID: fx.command, OptionID: &deny,
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if _, err := fx.client.ListAttention(ctx, 10); err != nil {
		t.Fatalf("list attention: %v", err)
	}
	if got := fx.client.suggestions.threadIDs(); len(got) != 0 {
		t.Fatalf("请求消失后建议应被清除: %v", got)
	}
}

// 详情按字符截断，不切坏多字节字符。
func TestTruncateStewardDetail(t *testing.T) {
	long := strings.Repeat("中", stewardRequestDetailMaxRunes+5)
	got := truncateStewardDetail(long)
	if utf8.RuneCountInString(got) != stewardRequestDetailMaxRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("截断结果不对，字符数 %d", utf8.RuneCountInString(got))
	}
	if short := truncateStewardDetail("短"); short != "短" {
		t.Fatalf("短文本不应改动: %q", short)
	}
}
