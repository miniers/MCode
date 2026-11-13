package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

// fakeDeliveryHarness 用于测试审批送达确认链路的模拟 harness。
type fakeDeliveryHarness struct {
	confirmsDelivery bool
	runFn            func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error
}

func (f *fakeDeliveryHarness) Kind() harness.Kind {
	return harness.Kind("fake-delivery")
}

func (f *fakeDeliveryHarness) ConfirmsApprovalDelivery() bool {
	return f.confirmsDelivery
}

func (f *fakeDeliveryHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if f.runFn != nil {
		return f.runFn(ctx, turn, emit)
	}
	return nil
}

// TestApprovalDelivery_Success 覆盖：送达成功时移除卡片。
func TestApprovalDelivery_Success(t *testing.T) {
	testenv.TempHome(t)

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000101"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Delivery Success Thread"}, nil)

	reqSent := harness.ApprovalRequest{
		ToolID:   "call_success_001",
		ToolName: "bash",
		Summary:  "run echo hello",
	}

	fake := &fakeDeliveryHarness{
		confirmsDelivery: true,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			dec, err := turn.Approver.Approve(ctx, reqSent)
			if err != nil {
				return err
			}
			if dec == harness.DecisionAllow {
				// 模拟写入 CLI 成功后通知送达确认
				if acker, ok := turn.Approver.(interface {
					AckDelivery(harness.ApprovalRequest, error)
				}); ok {
					acker.AckDelivery(reqSent, nil)
				}
			}
			return nil
		},
	}
	backend.SetHarness(fake, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行命令",
		ActionID: "00000000-0000-0000-0000-000000000102",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	var firstChange wire.RequestsChangedParams
	select {
	case firstChange = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	if len(firstChange.Requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(firstChange.Requests))
	}
	req := firstChange.Requests[0]

	optAllow := "allow"
	res, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err != nil {
		t.Fatalf("expected RespondRequest success, got error: %v", err)
	}
	if res.Status != wire.RequestStatusAccepted {
		t.Fatalf("expected status accepted, got %s", res.Status)
	}

	// 确认送达成功后卡片被移除
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 0 {
		t.Fatalf("expected 0 requests after delivery success, got %d", len(list.Requests))
	}

	waitThreadIdle(t, backend, ctx, threadID)
}

// TestApprovalDelivery_Failure_CardRetained 覆盖：送达失败时保留卡片并带 deliveryError，RespondRequest 返回错误。
func TestApprovalDelivery_Failure_CardRetained(t *testing.T) {
	testenv.TempHome(t)

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000201"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Delivery Fail Thread"}, nil)

	reqSent := harness.ApprovalRequest{
		ToolID:   "call_fail_001",
		ToolName: "bash",
		Summary:  "run dangerous command",
	}

	turnEnded := make(chan struct{})
	fake := &fakeDeliveryHarness{
		confirmsDelivery: true,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			defer close(turnEnded)
			_, err := turn.Approver.Approve(ctx, reqSent)
			if err != nil {
				return err
			}
			// 模拟写入 CLI 失败，上报管道关闭
			if acker, ok := turn.Approver.(interface {
				AckDelivery(harness.ApprovalRequest, error)
			}); ok {
				acker.AckDelivery(reqSent, errors.New("pipe closed"))
			}
			// 保持 turn 运行避免提前退出
			<-ctx.Done()
			return ctx.Err()
		},
	}
	backend.SetHarness(fake, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行失败测试",
		ActionID: "00000000-0000-0000-0000-000000000202",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	var firstChange wire.RequestsChangedParams
	select {
	case firstChange = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	req := firstChange.Requests[0]
	optAllow := "allow"
	_, err = backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err == nil {
		t.Fatal("expected RespondRequest to fail when delivery fails, got nil")
	}
	if !strings.Contains(err.Error(), "pipe closed") {
		t.Fatalf("expected error message to contain 'pipe closed', got: %v", err)
	}

	// 验证卡片保留且带有 deliveryError
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 1 {
		t.Fatalf("expected 1 retained request, got %d", len(list.Requests))
	}
	if list.Requests[0].DeliveryError == nil || !strings.Contains(*list.Requests[0].DeliveryError, "pipe closed") {
		t.Fatalf("expected DeliveryError 'pipe closed', got %v", list.Requests[0].DeliveryError)
	}
}

// TestApprovalDelivery_Timeout_CardRetained 覆盖：超时同样保留卡片并记录 deliveryError。
func TestApprovalDelivery_Timeout_CardRetained(t *testing.T) {
	testenv.TempHome(t)

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()
	// 设置微小超时时间以便快速触发
	backend.SetApprovalDeliveryTimeout(50 * time.Millisecond)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000301"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Delivery Timeout Thread"}, nil)

	reqSent := harness.ApprovalRequest{
		ToolID:   "call_timeout_001",
		ToolName: "bash",
		Summary:  "run hanging write",
	}

	fake := &fakeDeliveryHarness{
		confirmsDelivery: true,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			_, err := turn.Approver.Approve(ctx, reqSent)
			if err != nil {
				return err
			}
			// 故意不调用 AckDelivery，触发超时
			<-ctx.Done()
			return ctx.Err()
		},
	}
	backend.SetHarness(fake, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "超时测试",
		ActionID: "00000000-0000-0000-0000-000000000302",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	var firstChange wire.RequestsChangedParams
	select {
	case firstChange = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	req := firstChange.Requests[0]
	optAllow := "allow"
	_, err = backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err == nil {
		t.Fatal("expected RespondRequest timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected error message to contain 'timed out', got: %v", err)
	}

	// 验证超时后卡片依然保留
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 1 {
		t.Fatalf("expected 1 request retained after timeout, got %d", len(list.Requests))
	}
	if list.Requests[0].DeliveryError == nil || !strings.Contains(*list.Requests[0].DeliveryError, "timed out") {
		t.Fatalf("expected DeliveryError with 'timed out', got %v", list.Requests[0].DeliveryError)
	}
}

// TestApprovalDelivery_Retry_Success 覆盖：再次答复后成功。
func TestApprovalDelivery_Retry_Success(t *testing.T) {
	testenv.TempHome(t)

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000401"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Delivery Retry Thread"}, nil)

	reqSent := harness.ApprovalRequest{
		ToolID:   "call_retry_001",
		ToolName: "bash",
		Summary:  "run retry command",
	}

	attempt := 0
	fake := &fakeDeliveryHarness{
		confirmsDelivery: true,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			for {
				_, err := turn.Approver.Approve(ctx, reqSent)
				if err != nil {
					return err
				}
				acker, _ := turn.Approver.(interface {
					AckDelivery(harness.ApprovalRequest, error)
				})
				if attempt == 0 {
					attempt++
					// 第一次模拟失败
					if acker != nil {
						acker.AckDelivery(reqSent, errors.New("pipe closed"))
					}
					continue
				}
				// 第二次模拟成功
				if acker != nil {
					acker.AckDelivery(reqSent, nil)
				}
				break
			}
			return nil
		},
	}
	backend.SetHarness(fake, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "重试测试",
		ActionID: "00000000-0000-0000-0000-000000000402",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	var firstChange wire.RequestsChangedParams
	select {
	case firstChange = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	req := firstChange.Requests[0]
	optAllow := "allow"

	// 第一次答复：预期失败
	_, err = backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err == nil {
		t.Fatal("expected first attempt to fail")
	}

	listAfterFirst, err := backend.ListRequests(ctx, threadID)
	if err != nil || len(listAfterFirst.Requests) != 1 || listAfterFirst.Requests[0].DeliveryError == nil {
		t.Fatalf("expected 1 request with DeliveryError after first attempt, got %+v", listAfterFirst)
	}

	// 送达失败后 Provider 会再次 Approve 重新登记同一个请求。等这次重新登记的通知到了再重试，
	// 否则答复会抢在 Provider 重新进入等待之前被判为 stale（卡片被删除）。
	// 失败分支自己也推一条通知，所以读到两条即可确定重新登记已经发生。
	for i := 0; i < 2; i++ {
		select {
		case <-changedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for retry re-registration")
		}
	}

	// 第二次答复（重试）：预期成功
	res, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err != nil {
		t.Fatalf("expected second attempt (retry) to succeed, got: %v", err)
	}
	if res.Status != wire.RequestStatusAccepted {
		t.Fatalf("expected accepted status, got %s", res.Status)
	}

	// 验证重试成功后卡片已移除
	listAfterSecond, err := backend.ListRequests(ctx, threadID)
	if err != nil || len(listAfterSecond.Requests) != 0 {
		t.Fatalf("expected 0 requests after retry success, got %+v", listAfterSecond)
	}

	waitThreadIdle(t, backend, ctx, threadID)
}

// TestApprovalDelivery_NoConfirmation_Fallback 覆盖：不支持确认的 harness 保持原行为。
func TestApprovalDelivery_NoConfirmation_Fallback(t *testing.T) {
	testenv.TempHome(t)

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000501"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "No Confirmation Thread"}, nil)

	reqSent := harness.ApprovalRequest{
		ToolID:   "call_legacy_001",
		ToolName: "bash",
		Summary:  "run legacy command",
	}

	fake := &fakeDeliveryHarness{
		confirmsDelivery: false,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			_, err := turn.Approver.Approve(ctx, reqSent)
			// 不支持确认的 harness 不需要调用 AckDelivery
			return err
		},
	}
	backend.SetHarness(fake, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "老版本测试",
		ActionID: "00000000-0000-0000-0000-000000000502",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	var firstChange wire.RequestsChangedParams
	select {
	case firstChange = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	req := firstChange.Requests[0]
	optAllow := "allow"
	res, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err != nil {
		t.Fatalf("expected RespondRequest success immediately, got: %v", err)
	}
	if res.Status != wire.RequestStatusAccepted {
		t.Fatalf("expected accepted status, got %s", res.Status)
	}

	list, err := backend.ListRequests(ctx, threadID)
	if err != nil || len(list.Requests) != 0 {
		t.Fatalf("expected 0 requests immediately after response, got %+v", list)
	}

	waitThreadIdle(t, backend, ctx, threadID)
}

func TestApprovalDelivery_StaleApprover(t *testing.T) {
	turns := make(chan harness.Turn, 1)
	b := runtimeBackend(t, &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		turns <- turn
		<-ctx.Done()
		return ctx.Err()
	}})
	id := runtimeThread(b)
	ctx := context.Background()
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "run"}); err != nil {
		t.Fatal(err)
	}
	turn := <-turns
	b.mu.Lock()
	b.threads[id].sessionEpoch++
	b.mu.Unlock()
	approvalCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err := turn.Approver.Approve(approvalCtx, harness.ApprovalRequest{ToolID: "stale", ToolName: "bash"})
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("old epoch approval must be rejected before publishing a card, got %v", err)
	}
	list, err := b.ListRequests(ctx, id)
	if err != nil || len(list.Requests) != 0 {
		t.Fatalf("stale approval left a card: %+v %v", list, err)
	}
}

func TestApprovalDelivery_CancelBeforeReceiptRetainsError(t *testing.T) {
	b := runtimeBackend(t, &fakeDeliveryHarness{
		confirmsDelivery: true,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			req := harness.ApprovalRequest{ToolID: "delayed", ToolName: "bash"}
			_, err := turn.Approver.Approve(ctx, req)
			harness.AckDelivery(turn.Approver, req, err)
			return err
		},
	})
	id := runtimeThread(b)
	requests := make(chan wire.RemoteRequest, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	b.hub.AddObserver(func(_ string, method string, params any) {
		if method != "requests.changed" {
			return
		}
		p := params.(wire.RequestsChangedParams)
		if len(p.Requests) == 1 && p.Requests[0].DeliveryError == nil {
			requests <- p.Requests[0]
			<-release
		}
	})
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "run"}); err != nil {
		t.Fatal(err)
	}
	var req wire.RemoteRequest
	select {
	case req = <-requests:
	case <-time.After(time.Second):
		t.Fatal("no approval request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	allow := "allow"
	_, err := b.RespondRequest(ctx, wire.RequestsRespondParams{ThreadID: id, RequestID: req.RequestID, OptionID: &allow})
	if err == nil {
		t.Fatal("expected cancellation before receipt")
	}
	list, err := b.ListRequests(context.Background(), id)
	if err != nil || len(list.Requests) != 1 || list.Requests[0].DeliveryError == nil {
		t.Fatalf("unconfirmed response must retain a delivery error: %+v %v", list, err)
	}
	for _, runID := range []string{"stale-run", req.RunID} {
		_, err := b.RespondRequest(context.Background(), wire.RequestsRespondParams{
			ThreadID: id, RequestID: req.RequestID, RunID: runID, OptionID: &allow,
		})
		if err == nil {
			t.Fatal("expected stale run or full response queue to reject the retry")
		}
		list, err := b.ListRequests(context.Background(), id)
		if err != nil || len(list.Requests) != 1 || list.Requests[0].DeliveryError == nil {
			t.Fatalf("rejected retry cleared the delivery error: %+v %v", list, err)
		}
	}
}
