package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

type blockingInterruptHarness struct {
	startedCh chan struct{}
}

func (h *blockingInterruptHarness) Kind() harness.Kind {
	return "mock"
}

func (h *blockingInterruptHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	emit(harness.Event{
		Type: harness.EventText,
		Text: "Hello, partial text before interrupt.",
	})
	if h.startedCh != nil {
		select {
		case h.startedCh <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestMemoryBackend_Interrupt(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	mockH := &blockingInterruptHarness{startedCh: startedCh}
	backend.SetHarness(mockH, "/tmp", harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-000000000001"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Test Interrupt Thread"}, nil)

	// 1. SendMessage 返回非空 TurnID
	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行一个长任务",
		ActionID: "00000000-0000-0000-0000-000000000010",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if sendRes.TurnID == nil || *sendRes.TurnID == "" {
		t.Fatalf("expected non-empty TurnID in SendMessageResult, got: %+v", sendRes.TurnID)
	}
	activeTurnID := *sendRes.TurnID

	// 等待 harness 启动并输出第一段文本
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for harness to start")
	}

	// 2. 用错误的 turnID 调用 Interrupt 得到 -32013
	wrongTurnID := "00000000-0000-0000-0000-000000000099"
	_, err = backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   wrongTurnID,
	})
	if err == nil {
		t.Fatal("expected error for mismatched turnID, got nil")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *wire.RPCError, got: %T (%v)", err, err)
	}
	if rpcErr.Code != wire.ErrCodeTurnMismatch {
		t.Fatalf("expected ErrCodeTurnMismatch (%d), got: %d", wire.ErrCodeTurnMismatch, rpcErr.Code)
	}

	// 3. 用正确的 turnID 调用得到 stopping，之后线程在 5 秒内变为空闲（用 waitThreadIdle）
	stopRes, err := backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   activeTurnID,
	})
	if err != nil {
		t.Fatalf("Interrupt with activeTurnID failed: %v", err)
	}
	if stopRes.State != wire.InterruptStateStopping {
		t.Fatalf("expected state %q, got: %q", wire.InterruptStateStopping, stopRes.State)
	}
	if stopRes.ThreadID != threadID || stopRes.TurnID != activeTurnID {
		t.Fatalf("unexpected stopRes: %+v", stopRes)
	}

	// 线程应变为空闲
	summary := waitThreadIdle(t, backend, ctx, threadID)
	if summary.IsRunning {
		t.Errorf("expected thread IsRunning to be false, got true")
	}

	// 已输出的那段 text 仍在历史中，且历史中没有错误消息
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var foundText bool
	for _, m := range hist.Messages {
		if strings.Contains(m.Text, "Hello, partial text before interrupt.") {
			foundText = true
		}
		if m.Kind != nil && *m.Kind == "error" {
			t.Errorf("expected no error message in history, found: %+v", m)
		}
		if strings.HasPrefix(m.Text, "出错：") {
			t.Errorf("expected no error prefix message, found: %s", m.Text)
		}
	}
	if !foundText {
		t.Error("expected emitted text to be preserved in thread history")
	}

	// 4. 再次调用 Interrupt 得到 already_stopped
	againRes, err := backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   activeTurnID,
	})
	if err != nil {
		t.Fatalf("second Interrupt call failed: %v", err)
	}
	if againRes.State != wire.InterruptStateAlreadyStopped {
		t.Fatalf("expected state %q, got: %q", wire.InterruptStateAlreadyStopped, againRes.State)
	}

	// 5. InterruptCurrent 能中断一轮新的运行
	newSendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "第二轮任务",
		ActionID: "00000000-0000-0000-0000-000000000011",
	})
	if err != nil {
		t.Fatalf("second SendMessage failed: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second harness run to start")
	}

	currentRes, err := backend.InterruptCurrent(ctx, threadID)
	if err != nil {
		t.Fatalf("InterruptCurrent failed: %v", err)
	}
	if currentRes.State != wire.InterruptStateStopping {
		t.Fatalf("expected InterruptCurrent state %q, got: %q", wire.InterruptStateStopping, currentRes.State)
	}
	if newSendRes.TurnID != nil && currentRes.TurnID != *newSendRes.TurnID {
		t.Errorf("expected InterruptCurrent turnID %s, got: %s", *newSendRes.TurnID, currentRes.TurnID)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	// 对空闲线程调用 InterruptCurrent 得到 already_stopped
	idleRes, err := backend.InterruptCurrent(ctx, threadID)
	if err != nil {
		t.Fatalf("InterruptCurrent on idle thread failed: %v", err)
	}
	if idleRes.State != wire.InterruptStateAlreadyStopped {
		t.Fatalf("expected already_stopped, got: %q", idleRes.State)
	}
}

func TestMemoryBackend_InterruptSimulatedReply(t *testing.T) {
	origDelay := simulatedReplyStepDelay
	simulatedReplyStepDelay = 200 * time.Millisecond
	t.Cleanup(func() {
		simulatedReplyStepDelay = origDelay
	})

	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	// 不设置 harness，走模拟回复
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := "00000000-0000-0000-0000-000000000002"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Simulated Reply Thread"}, nil)

	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "这是一段用于测试模拟回复中断的长文本内容",
		ActionID: "00000000-0000-0000-0000-000000000020",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if sendRes.TurnID == nil || *sendRes.TurnID == "" {
		t.Fatalf("expected non-empty TurnID, got: %+v", sendRes.TurnID)
	}

	// 稍作等待后调用 Interrupt 中断模拟回复
	time.Sleep(50 * time.Millisecond)

	res, err := backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   *sendRes.TurnID,
	})
	if err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}
	if res.State != wire.InterruptStateStopping {
		t.Fatalf("expected state stopping, got: %q", res.State)
	}

	summary := waitThreadIdle(t, backend, ctx, threadID)
	if summary.IsRunning {
		t.Errorf("expected thread IsRunning to be false, got true")
	}

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	for _, m := range hist.Messages {
		if m.Kind != nil && *m.Kind == "error" {
			t.Errorf("expected no error message, got: %+v", m)
		}
	}
}
