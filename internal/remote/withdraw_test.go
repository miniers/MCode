package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

// withdrawHarness 是撤卡测试用的可控 harness：Interact 的结论写进 resultCh，Run 结束时关闭 runDone。
type withdrawHarness struct {
	confirms bool
	req      harness.ApprovalRequest
	resultCh chan interactResult
	runDone  chan struct{}
}

func newWithdrawHarness(confirms bool, req harness.ApprovalRequest) *withdrawHarness {
	return &withdrawHarness{
		confirms: confirms,
		req:      req,
		resultCh: make(chan interactResult, 1),
		runDone:  make(chan struct{}),
	}
}

func (h *withdrawHarness) Kind() harness.Kind {
	return harness.Kind("withdraw-test")
}

func (h *withdrawHarness) ConfirmsApprovalDelivery() bool {
	return h.confirms
}

func (h *withdrawHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	defer close(h.runDone)
	ia, ok := turn.Approver.(harness.Interactor)
	if !ok {
		return errors.New("approver does not implement Interactor")
	}
	resp, err := ia.Interact(ctx, h.req)
	h.resultCh <- interactResult{resp: resp, err: err}
	return nil
}

// withdrawEvents 收集撤卡相关推送，测试全部靠 channel 同步，不做轮询等待。
type withdrawEvents struct {
	requests chan wire.RequestsChangedParams
	threads  chan wire.ThreadsChanged
}

func newWithdrawBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })
	return backend
}

// startWithdrawThread 启动一轮对话并等待首个待处理卡片的推送。
func startWithdrawThread(t *testing.T, backend *MemoryBackend, threadID, actionID string, h *withdrawHarness) (*withdrawEvents, wire.RemoteRequest) {
	t.Helper()
	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "撤卡线程"}, nil)
	backend.SetHarness(h, "/workspace", harness.ModeAgent)

	ev := &withdrawEvents{
		requests: make(chan wire.RequestsChangedParams, 8),
		threads:  make(chan wire.ThreadsChanged, 8),
	}
	conn := newMockStreamConn()
	conn.notifyFn = func(method string, params any) error {
		switch method {
		case "requests.changed":
			if p, ok := params.(wire.RequestsChangedParams); ok {
				ev.requests <- p
			}
		case "threads.changed":
			if p, ok := params.(wire.ThreadsChanged); ok {
				ev.threads <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "发起审批",
		ActionID: actionID,
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	return ev, waitFirstCard(t, ev.requests)
}

func waitFirstCard(t *testing.T, ch <-chan wire.RequestsChangedParams) wire.RemoteRequest {
	t.Helper()
	select {
	case p := <-ch:
		if len(p.Requests) != 1 {
			t.Fatalf("期望 1 张待处理卡片，实际 %d", len(p.Requests))
		}
		return p.Requests[0]
	case <-time.After(2 * time.Second):
		t.Fatal("等待待处理卡片超时")
	}
	return wire.RemoteRequest{}
}

// waitIdleSignal 注册一个会话空闲等待通道，空闲时由后端关闭；已空闲则立即关闭。
func waitIdleSignal(t *testing.T, backend *MemoryBackend, threadID string) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{})
	backend.mu.Lock()
	td, ok := backend.threads[threadID]
	if !ok || !td.summary.IsRunning {
		backend.mu.Unlock()
		close(ch)
		return ch
	}
	td.idleWaiters = append(td.idleWaiters, ch)
	backend.mu.Unlock()
	return ch
}

// countThreadText 统计时间线中完全等于 text 的消息条数。
func countThreadText(t *testing.T, backend *MemoryBackend, threadID, text string) int {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	td, ok := backend.threads[threadID]
	if !ok {
		return 0
	}
	backend.ensureMessagesLoadedLocked(td)
	n := 0
	for _, m := range td.messages {
		if m.Text == text {
			n++
		}
	}
	return n
}

func assertNoRequests(t *testing.T, backend *MemoryBackend, threadID string) {
	t.Helper()
	list, err := backend.ListRequests(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 0 {
		t.Fatalf("撤卡后不应再有待处理请求，实际 %d", len(list.Requests))
	}
}

func assertNoInteractResult(t *testing.T, ch <-chan interactResult) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("卡片仍应等待答复，Interact 却提前返回：%+v", r)
	default:
	}
}

// 空闲会话上的待处理卡片（已答复、等送达确认）在归档时被撤下，且不写时间线提示。
func TestArchiveWithdrawsPendingCardOnIdleThread(t *testing.T) {
	testenv.TempHome(t)
	backend := newWithdrawBackend(t)
	backend.SetApprovalDeliveryTimeout(10 * time.Millisecond)

	threadID := "00000000-0000-0000-0000-000000000901"
	h := newWithdrawHarness(true, harness.ApprovalRequest{
		ToolID:   "call_withdraw_idle",
		ToolName: "bash",
		Summary:  "run echo hello",
	})
	_, req := startWithdrawThread(t, backend, threadID, "00000000-0000-0000-0000-000000000902", h)

	idleCh := waitIdleSignal(t, backend, threadID)
	optAllow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err == nil {
		t.Fatal("未送达确认时 RespondRequest 应返回错误")
	}
	select {
	case <-idleCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待会话空闲超时")
	}

	th, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !th.HasPendingRequest {
		t.Fatal("归档前应仍有待送达确认的卡片")
	}

	archived := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}

	assertNoRequests(t, backend, threadID)
	th, err = backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.HasPendingRequest {
		t.Error("归档撤卡后 HasPendingRequest 应为 false")
	}
	if th.Attention == "approval" {
		t.Error("归档撤卡后 Attention 不应仍为 approval")
	}
	if !th.IsArchived {
		t.Error("会话应已归档")
	}
	if n := countThreadText(t, backend, threadID, withdrawNoticeText(withdrawArchive)); n != 0 {
		t.Errorf("已答复等送达确认的卡片被撤下时不应写提示，实际 %d 条", n)
	}
}

// 运行中的会话归档时先撤卡再中断，卡片立刻消失并写一条归档提示。
func TestArchiveWithdrawsWhileRunning(t *testing.T) {
	backend := newWithdrawBackend(t)
	threadID := "00000000-0000-0000-0000-000000000911"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_withdraw_running",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	startWithdrawThread(t, backend, threadID, "00000000-0000-0000-0000-000000000912", h)

	archived := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}

	r := waitInteract(t, h.resultCh)
	if !errors.Is(r.err, errRequestWithdrawn) {
		t.Fatalf("归档撤卡后 Interact 应返回 errRequestWithdrawn，实际 %v", r.err)
	}
	assertNoRequests(t, backend, threadID)
	th, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.HasPendingRequest {
		t.Error("归档撤卡后 HasPendingRequest 应为 false")
	}
	if !th.IsArchived {
		t.Error("会话应已归档")
	}
	if n := countThreadText(t, backend, threadID, withdrawNoticeText(withdrawArchive)); n != 1 {
		t.Errorf("归档撤卡应恰好写一条提示，实际 %d 条", n)
	}
	if n := countThreadText(t, backend, threadID, withdrawNoticeText(withdrawInterrupt)); n != 0 {
		t.Errorf("归档先撤卡，不应再写中断提示，实际 %d 条", n)
	}
}

// 删除会话时撤卡：卡片消失、会话从列表移除，不写时间线提示。
func TestDeleteThreadWithdrawsAndClearsAttentionSubject(t *testing.T) {
	backend := newWithdrawBackend(t)
	threadID := "00000000-0000-0000-0000-000000000921"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_withdraw_delete",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	ev, _ := startWithdrawThread(t, backend, threadID, "00000000-0000-0000-0000-000000000922", h)

	if err := backend.DeleteThread(context.Background(), threadID); err != nil {
		t.Fatalf("DeleteThread failed: %v", err)
	}

	r := waitInteract(t, h.resultCh)
	if !errors.Is(r.err, errRequestWithdrawn) {
		t.Fatalf("删除撤卡后 Interact 应返回 errRequestWithdrawn，实际 %v", r.err)
	}
	_, err := backend.ListRequests(context.Background(), threadID)
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("删除后 ListRequests 应返回资源不存在，实际 %v", err)
	}
	removed := false
	timeout := time.After(2 * time.Second)
	for !removed {
		select {
		case change := <-ev.threads:
			for _, id := range change.RemovedThreadIDs {
				if id == threadID {
					removed = true
				}
			}
		case <-timeout:
			t.Fatal("等待 threads.changed 超时")
		}
	}
}

// 撤卡是幂等的：重复撤卡不阻塞、不 panic，也不会重复写提示。
func TestWithdrawIsIdempotent(t *testing.T) {
	backend := newWithdrawBackend(t)
	threadID := "00000000-0000-0000-0000-000000000931"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_withdraw_idem",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	_, req := startWithdrawThread(t, backend, threadID, "00000000-0000-0000-0000-000000000932", h)

	// 直接重复触发撤回信号：Once 保证只投递一次，也不会关闭两次 done。
	backend.mu.Lock()
	pr := backend.threads[threadID].pending[req.RequestID]
	pr.withdraw(withdrawArchive)
	pr.withdraw(withdrawArchive)
	backend.mu.Unlock()

	archived := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}
	// 再次归档：此时已无待处理卡片，撤卡应是空操作。
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("第二次 UpdateThread failed: %v", err)
	}

	r := waitInteract(t, h.resultCh)
	if !errors.Is(r.err, errRequestWithdrawn) {
		t.Fatalf("撤卡后 Interact 应返回 errRequestWithdrawn，实际 %v", r.err)
	}
	assertNoInteractResult(t, h.resultCh)
	assertNoRequests(t, backend, threadID)
	if n := countThreadText(t, backend, threadID, withdrawNoticeText(withdrawArchive)); n != 1 {
		t.Errorf("重复归档撤卡应只写一条提示，实际 %d 条", n)
	}
}

// 休眠不撤卡：卡片保持待答，唤醒后仍能答复。
func TestDormantKeepsPendingCard(t *testing.T) {
	backend := newWithdrawBackend(t)
	threadID := "00000000-0000-0000-0000-000000000941"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_withdraw_dormant",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	_, req := startWithdrawThread(t, backend, threadID, "00000000-0000-0000-0000-000000000942", h)

	dormant := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Dormant:  &dormant,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}

	list, err := backend.ListRequests(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 1 || list.Requests[0].RequestID != req.RequestID {
		t.Fatalf("休眠后卡片应保留，实际 %+v", list.Requests)
	}
	th, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !th.HasPendingRequest {
		t.Error("休眠后 HasPendingRequest 应保持 true")
	}
	assertNoInteractResult(t, h.resultCh)

	optAllow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("休眠会话的卡片应仍可答复：%v", err)
	}
	r := waitInteract(t, h.resultCh)
	if r.err != nil || r.resp.Decision != harness.DecisionAllow {
		t.Fatalf("休眠会话的答复应生效，实际 %+v", r)
	}
	if n := countThreadText(t, backend, threadID, withdrawNoticeText(withdrawArchive)); n != 0 {
		t.Errorf("休眠不应写撤卡提示，实际 %d 条", n)
	}
}

// 中断入口撤卡不依赖 runCancel：Interact 立刻拿到 errRequestWithdrawn，时间线恰好一条中断提示。
func TestInterruptWithdrawsPendingCard(t *testing.T) {
	backend := newWithdrawBackend(t)
	threadID := "00000000-0000-0000-0000-000000000951"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_withdraw_interrupt",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	startWithdrawThread(t, backend, threadID, "00000000-0000-0000-0000-000000000952", h)

	if _, err := backend.Interrupt(context.Background(), wire.InterruptParams{ThreadID: threadID}); err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}

	r := waitInteract(t, h.resultCh)
	if !errors.Is(r.err, errRequestWithdrawn) {
		t.Fatalf("中断撤卡后 Interact 应返回 errRequestWithdrawn，实际 %v", r.err)
	}
	assertNoRequests(t, backend, threadID)
	th, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.HasPendingRequest {
		t.Error("中断撤卡后 HasPendingRequest 应为 false")
	}
	if n := countThreadText(t, backend, threadID, withdrawNoticeText(withdrawInterrupt)); n != 1 {
		t.Errorf("中断撤卡应恰好写一条提示，实际 %d 条", n)
	}
}
