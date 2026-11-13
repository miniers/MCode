package remote

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/testenv"
)

type blockableStateHarness struct {
	startedCh chan struct{}
	blockCh   chan struct{}
	emitText  string
	runErr    error
}

func (h *blockableStateHarness) Kind() harness.Kind {
	return "mock-state"
}

func (h *blockableStateHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if h.emitText != "" {
		emit(harness.Event{
			Type: harness.EventText,
			Text: h.emitText,
		})
	}
	if h.startedCh != nil {
		select {
		case h.startedCh <- struct{}{}:
		default:
		}
	}
	if h.blockCh != nil {
		select {
		case <-h.blockCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.runErr
}

// 1. phase 依次经过 idle -> starting -> running -> idle
func TestRuntimeState_PhaseTransitions(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	blockCh := make(chan struct{})
	mockH := &blockableStateHarness{
		startedCh: startedCh,
		blockCh:   blockCh,
		emitText:  "running text",
	}
	backend.SetHarness(mockH, t.TempDir(), harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-000000000001"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Phase Transitions"}, nil)

	// 初始状态应为 phaseIdle
	backend.mu.Lock()
	td := backend.threads[threadID]
	if td.phase != phaseIdle {
		t.Fatalf("expected initial phase to be phaseIdle, got %v", td.phase)
	}

	// 注入一个未完成的 historyOp，使启动卡在 phaseStarting
	pauseHistory := make(chan struct{})
	td.historyOp = pauseHistory
	backend.mu.Unlock()

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "test phase message",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// 验证处于 phaseStarting
	backend.mu.Lock()
	if td.phase != phaseStarting {
		backend.mu.Unlock()
		t.Fatalf("expected phase to be phaseStarting while historyOp is blocking, got %v", td.phase)
	}
	backend.mu.Unlock()

	// 释放 historyOp，允许进入 running
	close(pauseHistory)

	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for harness to start")
	}

	// 验证进入 phaseRunning
	backend.mu.Lock()
	if td.phase != phaseRunning {
		backend.mu.Unlock()
		t.Fatalf("expected phase to be phaseRunning, got %v", td.phase)
	}
	backend.mu.Unlock()

	// 释放 harness 阻塞，完成本轮
	close(blockCh)

	waitThreadIdle(t, backend, ctx, threadID)

	backend.mu.Lock()
	if td.phase != phaseIdle {
		backend.mu.Unlock()
		t.Fatalf("expected phase to return to phaseIdle, got %v", td.phase)
	}
	backend.mu.Unlock()
}

// 2. starting 阶段打断会立即结束，不需要看门狗
func TestRuntimeState_InterruptStarting(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	mockH := &blockableStateHarness{
		startedCh: startedCh,
		emitText:  "should not run",
	}
	backend.SetHarness(mockH, t.TempDir(), harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-000000000002"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Interrupt Starting"}, nil)

	// 卡在 phaseStarting
	pauseHistory := make(chan struct{})
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.historyOp = pauseHistory
	backend.mu.Unlock()

	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "fast cancel prompt",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	activeTurnID := *sendRes.TurnID

	// 在 starting 阶段直接调用 Interrupt
	stopRes, err := backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   activeTurnID,
	})
	if err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}
	if stopRes.State != wire.InterruptStateStopping {
		t.Fatalf("expected stopping state, got: %s", stopRes.State)
	}

	// 立即检查：线程已回到 idle，且 sessionEpoch 已递增
	backend.mu.Lock()
	if td.summary.IsRunning {
		t.Fatalf("expected IsRunning to be false immediately after starting interrupt")
	}
	if td.phase != phaseIdle {
		t.Fatalf("expected phase to be phaseIdle, got %v", td.phase)
	}
	if td.runTurnID != "" {
		t.Fatalf("expected runTurnID to be empty, got %q", td.runTurnID)
	}
	if td.sessionEpoch <= 0 {
		t.Fatalf("expected sessionEpoch to be incremented, got %d", td.sessionEpoch)
	}
	backend.mu.Unlock()

	// 释放暂停，确认 harness 绝未执行
	close(pauseHistory)
	select {
	case <-startedCh:
		t.Fatal("harness should not have run after starting interrupt")
	case <-time.After(50 * time.Millisecond):
	}
}

// 3. running 阶段 harness 忽略 ctx 时，手动触发注入的 afterFunc 后强制结束，晚到的旧事件不会写回
func TestRuntimeState_InterruptWatchdog_IgnoresCtx(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	var watchdogMu sync.Mutex
	var watchdogCallback func()
	backend.SetAfterFunc(func(d time.Duration, f func()) func() bool {
		watchdogMu.Lock()
		watchdogCallback = f
		watchdogMu.Unlock()
		return func() bool { return true }
	})

	var lateEmit func(harness.Event)
	startedCh := make(chan struct{}, 1)
	ignoreCtxH := &customRunHarness{
		kind: "ignore-ctx",
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "first response chunk"})
			lateEmit = emit
			startedCh <- struct{}{}
			// 模拟忽略 ctx 取消，持续阻塞
			time.Sleep(1 * time.Second)
			return nil
		},
	}
	backend.SetHarness(ignoreCtxH, t.TempDir(), harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-000000000003"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Watchdog Test"}, nil)

	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "watchdog prompt",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-startedCh

	// 发起打断，触发看门狗
	_, err = backend.Interrupt(ctx, wire.InterruptParams{
		ThreadID: threadID,
		TurnID:   *sendRes.TurnID,
	})
	if err != nil {
		t.Fatalf("Interrupt failed: %v", err)
	}

	// 手动触发注入的看门狗
	watchdogMu.Lock()
	cb := watchdogCallback
	watchdogMu.Unlock()
	if cb == nil {
		t.Fatal("expected watchdog callback to be registered")
	}
	cb()

	// 强制结束，验证 Notice 与 IsRunning 状态
	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.IsRunning {
		t.Fatalf("expected thread to be stopped by watchdog, but IsRunning is true")
	}

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var hasWatchdogNotice bool
	for _, m := range hist.Messages {
		if strings.Contains(m.Text, "打断超时，已强制结束本轮") {
			hasWatchdogNotice = true
			break
		}
	}
	if !hasWatchdogNotice {
		t.Fatalf("expected notice '打断超时，已强制结束本轮' in history")
	}

	// 旧 goroutine 晚到的事件必须被丢弃
	msgCountBefore := len(hist.Messages)
	if lateEmit != nil {
		lateEmit(harness.Event{
			Type: harness.EventText,
			Text: "late text from zombie goroutine",
		})
	}
	histAfter, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	if len(histAfter.Messages) != msgCountBefore {
		t.Fatalf("late event was not discarded: message count changed from %d to %d", msgCountBefore, len(histAfter.Messages))
	}
}

// 4. recall 返回原 prompt，且该轮消息被移除，turns 表标记为 taken_back
func TestRuntimeState_Recall(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	dbPath := filepath.Join(t.TempDir(), "recall.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	startedCh := make(chan struct{}, 1)
	blockCh := make(chan struct{})
	mockH := &blockableStateHarness{
		startedCh: startedCh,
		blockCh:   blockCh,
		emitText:  "partial assistant answer",
	}
	backend.SetHarness(mockH, t.TempDir(), harness.ModeAgent)

	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "Project 1"})
	thRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := thRes.ThreadID

	userPrompt := "请帮我重构一下网络模块"
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     userPrompt,
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-startedCh

	// 正在运行时调用 RecallTurn
	recallRes, err := backend.RecallTurn(ctx, threadID)
	if err != nil {
		t.Fatalf("RecallTurn failed: %v", err)
	}

	// 验证返回原 Prompt
	if recallRes.Prompt != userPrompt {
		t.Fatalf("expected prompt %q, got %q", userPrompt, recallRes.Prompt)
	}
	if recallRes.ThreadID != threadID {
		t.Fatalf("expected threadID %q, got %q", threadID, recallRes.ThreadID)
	}

	// 验证该轮消息被从历史中移除
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	if len(hist.Messages) != 0 {
		t.Fatalf("expected messages to be removed, got: %v", hist.Messages)
	}

	// 验证 turns 表中该轮的状态变为 taken_back
	turnRec, err := st.GetTurn(ctx, recallRes.TurnID)
	if err != nil {
		t.Fatalf("GetTurn failed: %v", err)
	}
	if turnRec.Status != "taken_back" {
		t.Fatalf("expected turn status 'taken_back', got: %s", turnRec.Status)
	}
}

// 5. epoch 递增后旧 goroutine 的事件被丢弃
func TestRuntimeState_EpochDiscardOldEvents(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := "00000000-0000-0000-0000-000000000005"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Epoch Test"}, nil)

	var capturedEmit func(harness.Event)
	startedCh := make(chan struct{}, 1)
	blockCh := make(chan struct{})
	h := &customRunHarness{
		kind: "epoch-harness",
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			capturedEmit = emit
			startedCh <- struct{}{}
			<-blockCh
			return nil
		},
	}
	backend.SetHarness(h, t.TempDir(), harness.ModeAgent)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial turn",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-startedCh

	// 发出一条正常事件
	capturedEmit(harness.Event{
		Type: harness.EventText,
		Text: "chunk 1",
	})

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	if len(hist.Messages) == 0 {
		t.Fatal("expected message in history before epoch bump")
	}

	// 递增 sessionEpoch
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.sessionEpoch++
	backend.mu.Unlock()

	// 旧 goroutine 试图发出事件，应被直接丢弃
	capturedEmit(harness.Event{
		Type: harness.EventText,
		Text: "chunk 2 after epoch increment",
	})

	histAfter, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	for _, m := range histAfter.Messages {
		if strings.Contains(m.Text, "chunk 2 after epoch increment") {
			t.Fatalf("event after epoch bump should have been discarded")
		}
	}

	close(blockCh)
}

// 6. 会话失效时重建并重跑一次
func TestRuntimeState_LostSessionRecovery(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	var runCount atomic.Int32
	lostH := &customRunHarness{
		kind: "lost-session",
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			count := runCount.Add(1)
			if count == 1 {
				return errors.New("404 session not found")
			}
			emit(harness.Event{Type: harness.EventText, Text: "recovered successfully"})
			return nil
		},
	}
	backend.SetHarness(lostH, t.TempDir(), harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-000000000006"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Lost Session Test"}, nil)

	// 设置一个非空的已有 sessionID
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.sessionID = "old-expired-session"
	backend.mu.Unlock()

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "hello after session lost",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	if runCount.Load() != 2 {
		t.Fatalf("expected harness to be retried once on lost session, ran %d times", runCount.Load())
	}

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}

	var hasNotice bool
	for _, m := range hist.Messages {
		if strings.Contains(m.Text, "原会话已失效，已用本地历史重建上下文") {
			hasNotice = true
			break
		}
	}
	if !hasNotice {
		t.Fatalf("expected notice '原会话已失效，已用本地历史重建上下文' in history")
	}
}

// 7. 配置了 store 的情况下，ListTurns 能读到一行，且 Status 和 tokens 正确
func TestRuntimeState_Store_ListTurns(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	dbPath := filepath.Join(t.TempDir(), "turns_test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	turnHarness := &customRunHarness{
		kind: "usage-turn",
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "Turn completed successfully"})
			emit(harness.Event{
				Type: harness.EventUsage,
				Usage: &harness.Usage{
					InputTokens:  128,
					OutputTokens: 64,
				},
			})
			return nil
		},
	}
	backend.SetHarness(turnHarness, t.TempDir(), harness.ModeAgent)

	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "Project 1"})
	thRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := thRes.ThreadID

	userPrompt := "请测试 turns 落库"
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     userPrompt,
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	turns, err := st.ListTurns(ctx, threadID)
	if err != nil {
		t.Fatalf("ListTurns failed: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn in store, got %d", len(turns))
	}
	t0 := turns[0]
	if t0.Status != "completed" {
		t.Fatalf("expected turn status 'completed', got %q", t0.Status)
	}
	if t0.UserPrompt != userPrompt {
		t.Fatalf("expected user prompt %q, got %q", userPrompt, t0.UserPrompt)
	}
	if t0.InputTokens != 128 || t0.OutputTokens != 64 {
		t.Fatalf("expected tokens 128/64, got input=%d, output=%d", t0.InputTokens, t0.OutputTokens)
	}
}

// 8. 瞬态网络错误重试仍然生效
func TestRuntimeState_TransientNetworkRetry(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetAfterFunc(func(_ time.Duration, f func()) func() bool {
		f()
		return func() bool { return true }
	})

	var runCount atomic.Int32
	retryH := &customRunHarness{
		kind: "network-retry",
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			count := runCount.Add(1)
			if count == 1 {
				return errors.New("connection reset by peer")
			}
			emit(harness.Event{Type: harness.EventText, Text: "success after retry"})
			return nil
		},
	}
	backend.SetHarness(retryH, t.TempDir(), harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-000000000008"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Transient Retry Test"}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "test network drop",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	if runCount.Load() != 2 {
		t.Fatalf("expected harness to run twice (1 retry), ran %d times", runCount.Load())
	}

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var hasNotice bool
	for _, m := range hist.Messages {
		if strings.Contains(m.Text, "网络中断，正在重新发送") {
			hasNotice = true
			break
		}
	}
	if !hasNotice {
		t.Fatalf("expected notice '网络中断，正在重新发送' in history")
	}
}

type customRunHarness struct {
	kind  harness.Kind
	runFn func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error
}

func (c *customRunHarness) Kind() harness.Kind {
	if c.kind != "" {
		return c.kind
	}
	return "custom-mock"
}

func (c *customRunHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if c.runFn != nil {
		return c.runFn(ctx, turn, emit)
	}
	return nil
}
