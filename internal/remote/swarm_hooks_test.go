package remote

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 1. 验证轮次结束时钩子以 completed=true 和正确的 assistantText 调用
func TestSwarmHooks_TurnFinishedHook(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	off := false
	backend.SetGeneralSettingsFunc(func() config.Config { return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &off}} })

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	type hookCall struct {
		threadID      string
		projectID     string
		assistantText string
		completed     bool
	}
	hookCh := make(chan hookCall, 1)
	backend.SetTurnFinishedHook(func(threadID, projectID, assistantText string, completed bool) {
		hookCh <- hookCall{
			threadID:      threadID,
			projectID:     projectID,
			assistantText: assistantText,
			completed:     completed,
		}
	})

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:        threadID,
		ProjectID: "proj-swarm",
		Title:     "Swarm Turn Test",
		Provider:  "controlled",
	}, nil)

	_, err := backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "please do task",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-startedCh
	releaseCh <- struct{}{}

	select {
	case call := <-hookCh:
		if call.threadID != threadID {
			t.Errorf("expected threadID %s, got %s", threadID, call.threadID)
		}
		if call.projectID != "proj-swarm" {
			t.Errorf("expected projectID proj-swarm, got %s", call.projectID)
		}
		if !call.completed {
			t.Errorf("expected completed=true, got false")
		}
		expectedText := "reply to: please do task"
		if call.assistantText != expectedText {
			t.Errorf("expected assistantText %q, got %q", expectedText, call.assistantText)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for turnFinishedHook")
	}
}

// 2. 验证 AppendNotice 后时间线可见且不存在时返回 store.ErrNotFound，以及 ThreadWorkDir 回落机制
func TestSwarmHooks_AppendNoticeAndThreadWorkDir(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()

	// 不存在的会话返回 store.ErrNotFound
	err := backend.AppendNotice("nonexistent-thread", "notice 1")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected store.ErrNotFound for nonexistent thread, got %v", err)
	}

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:        threadID,
		ProjectID: "proj-notice",
		Title:     "Notice Test",
	}, nil)

	err = backend.AppendNotice(threadID, "[Hydra] bee 1 dispatched")
	if err != nil {
		t.Fatalf("AppendNotice failed: %v", err)
	}

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	if len(hist.Messages) != 1 {
		t.Fatalf("expected 1 message in history, got %d", len(hist.Messages))
	}
	msg := hist.Messages[0]
	if msg.Role != "system" {
		t.Errorf("expected role 'system', got %q", msg.Role)
	}
	if msg.Kind == nil || *msg.Kind != "info" {
		t.Errorf("expected kind 'info', got %+v", msg.Kind)
	}
	if msg.Text != "[Hydra] bee 1 dispatched" {
		t.Errorf("expected text '[Hydra] bee 1 dispatched', got %q", msg.Text)
	}

	// 验证 ThreadWorkDir 回落到所属项目根目录
	backend.AddProject(wire.ProjectSummary{ID: "proj-notice", Name: "Project Notice", Path: "/work/project-root"})
	dir, ok := backend.ThreadWorkDir(threadID)
	if !ok || dir != "/work/project-root" {
		t.Errorf("expected project path /work/project-root, got %q (ok=%v)", dir, ok)
	}
}

// 3. 验证 QueueInterruptAndSend 在 sendToBee=true 时返回 sent_to_swarm 且不中断
func TestSwarmHooks_QueueInterruptAndSend_SentToSwarm(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "SentToSwarm Test", Provider: "controlled"}, nil)

	_, _ = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial task",
		ActionID: newUUID(),
	})
	<-startedCh

	sendToBeeCalled := make(chan string, 1)
	backend.SetSwarmQueueHooks(
		func(tid string) bool { return false },
		func(ctx context.Context, tid, text string) (string, error) {
			sendToBeeCalled <- text
			return "bee-thread", nil
		},
	)

	text := "message intended for bee"
	actID := newUUID()
	res, err := backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: threadID,
		Text:     &text,
		ActionID: actID,
	})
	if err != nil {
		t.Fatalf("QueueInterruptAndSend failed: %v", err)
	}
	if res.State != wire.ThreadsQueueInterruptStateSentToSwarm {
		t.Errorf("expected state sent_to_swarm, got %s", res.State)
	}
	if res.TurnID != nil {
		t.Errorf("expected TurnID nil, got %v", res.TurnID)
	}
	if res.FollowUpID != nil {
		t.Errorf("expected FollowUpID nil, got %v", res.FollowUpID)
	}

	select {
	case calledText := <-sendToBeeCalled:
		if calledText != text {
			t.Errorf("expected sendToBee received %q, got %q", text, calledText)
		}
	default:
		t.Fatal("sendToBee was not called")
	}

	releaseCh <- struct{}{}
}

// 4. 验证 hasWorkingBees=true 时返回 queued 且队列中多一条
func TestSwarmHooks_QueueInterruptAndSend_HasWorkingBees(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "HasWorkingBees Test", Provider: "controlled"}, nil)

	_, _ = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial task",
		ActionID: newUUID(),
	})
	<-startedCh

	backend.SetSwarmQueueHooks(
		func(tid string) bool { return true },
		nil,
	)

	text := "queued follow-up message"
	actID := newUUID()
	res, err := backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: threadID,
		Text:     &text,
		ActionID: actID,
	})
	if err != nil {
		t.Fatalf("QueueInterruptAndSend failed: %v", err)
	}
	if res.State != wire.ThreadsQueueInterruptStateQueued {
		t.Errorf("expected state queued, got %s", res.State)
	}
	if res.TurnID != nil {
		t.Errorf("expected TurnID nil, got %v", res.TurnID)
	}
	if res.FollowUpID == nil {
		t.Fatal("expected FollowUpID not nil")
	}

	qSnap, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(qSnap.FollowUps) != 1 {
		t.Fatalf("expected 1 follow-up in queue, got %d", len(qSnap.FollowUps))
	}
	if qSnap.FollowUps[0].ID != *res.FollowUpID || qSnap.FollowUps[0].Text != text {
		t.Errorf("unexpected follow-up in queue: %+v", qSnap.FollowUps[0])
	}

	releaseCh <- struct{}{}
}

// 5. 验证两个钩子都为 nil 时行为不变（走 stopping 路径）
func TestSwarmHooks_QueueInterruptAndSend_DefaultStopping(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend.RegisterHarness(h)

	threadID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "DefaultStopping Test", Provider: "controlled"}, nil)

	_, _ = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "initial task",
		ActionID: newUUID(),
	})
	<-startedCh

	backend.SetSwarmQueueHooks(nil, nil)

	text := "interrupt and send text"
	actID := newUUID()
	res, err := backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: threadID,
		Text:     &text,
		ActionID: actID,
	})
	if err != nil {
		t.Fatalf("QueueInterruptAndSend failed: %v", err)
	}
	if res.State != wire.ThreadsQueueInterruptStateStopping {
		t.Errorf("expected state stopping, got %s", res.State)
	}
	if res.TurnID == nil {
		t.Errorf("expected TurnID not nil")
	}
	if res.FollowUpID == nil {
		t.Errorf("expected FollowUpID not nil")
	}

	releaseCh <- struct{}{}
}

func TestSwarmHooks_QueueWhileLeadIdle(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.AddThread(wire.ThreadSummary{ID: "idle-lead"}, nil)
	b.SetSwarmQueueHooks(func(string) bool { return true }, nil)
	result, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: "idle-lead", Text: "wait for reports", WhenRunning: "queue",
	})
	if err != nil || result.Queued == nil || !*result.Queued {
		t.Fatalf("expected queued result, got %+v, %v", result, err)
	}
	text := "also wait"
	res, err := b.QueueInterruptAndSend(context.Background(), wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: "idle-lead", Text: &text,
	})
	if err != nil || res.State != wire.ThreadsQueueInterruptStateQueued {
		t.Fatalf("expected queued without interrupting idle lead: %+v, %v", res, err)
	}
	thread, _ := b.GetThread(context.Background(), "idle-lead")
	if thread.IsRunning || len(res.Queue.FollowUps) != 2 {
		t.Fatalf("lead ran queued messages before bee reports: %+v", res.Queue)
	}
}

func TestSwarmHooks_DispatchBeforeDequeuingAndIgnorePriorReplies(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	b.RegisterHarness(&controlledTestHarness{startedCh: started, releaseCh: release})
	b.AddThread(wire.ThreadSummary{ID: "lead", Provider: "controlled"}, nil)
	var working atomic.Bool
	b.SetSwarmQueueHooks(func(string) bool { return working.Load() }, nil)
	called := make(chan string, 2)
	b.SetTurnFinishedHook(func(_, _, text string, _ bool) {
		working.Store(true)
		called <- text
	})
	ctx := context.Background()
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "follow-up", WhenRunning: "queue"}); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("turn hook did not run")
	}
	waitThreadIdle(t, b, ctx, "lead")
	snap, _ := b.QueueList(ctx, "lead")
	if len(snap.FollowUps) != 1 {
		t.Fatal("follow-up was consumed before bees finished")
	}
	// 下一轮未产生 assistant 消息时，不能重新派发上一轮的委派块。
	b.RegisterHarness(&emptySwarmHarness{})
	b.mu.Lock()
	b.threads["lead"].summary.Provider = "swarm-empty"
	b.mu.Unlock()
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "no reply"}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-called:
		if text != "" {
			t.Fatalf("prior assistant reply leaked into current turn: %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("empty turn hook did not run")
	}
}

type emptySwarmHarness struct{}

func (*emptySwarmHarness) Kind() harness.Kind { return "swarm-empty" }
func (*emptySwarmHarness) Run(context.Context, harness.Turn, func(harness.Event)) error {
	return nil
}

func TestSwarmReportPrecedesQueuedUserMessages(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	b.SetHarness(&controlledTestHarness{startedCh: started, releaseCh: release}, t.TempDir(), "")
	b.AddThread(wire.ThreadSummary{ID: "lead", Provider: "controlled"}, nil)
	ctx := context.Background()
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "initial"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "user follow-up", WhenRunning: "queue"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SendSwarmReport(ctx, "lead", "[Hydra] report"); err != nil {
		t.Fatal(err)
	}
	queue, err := b.QueueList(ctx, "lead")
	if err != nil || len(queue.FollowUps) != 2 || queue.FollowUps[0].Text != "[Hydra] report" {
		t.Fatalf("report must have priority: %+v, %v", queue, err)
	}
}

// lead 空闲、但仍有 bee 在运行或待审查时，swarm 汇报也必须直接开一轮回复，不能排进 followUps。
func TestSwarmReportDeliveredWhileIdleEvenWithWorkingBees(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	b.RegisterHarness(&controlledTestHarness{startedCh: started, releaseCh: release})
	b.AddThread(wire.ThreadSummary{ID: "lead", Provider: "controlled"}, nil)
	b.SetSwarmQueueHooks(func(string) bool { return true }, nil)
	ctx := context.Background()

	if err := b.SendSwarmReport(ctx, "lead", "[Hydra] report"); err != nil {
		t.Fatalf("SendSwarmReport failed: %v", err)
	}
	// SendSwarmReport 只回传错误：队列为空即说明结果没有走 Queued 分支。
	queue, err := b.QueueList(ctx, "lead")
	if err != nil {
		t.Fatalf("QueueList failed: %v", err)
	}
	if len(queue.FollowUps) != 0 {
		t.Fatalf("swarm report must not be queued while lead is idle: %+v", queue.FollowUps)
	}
	thread, err := b.GetThread(ctx, "lead")
	if err != nil || !thread.IsRunning {
		t.Fatalf("expected lead thread running, got %+v, %v", thread, err)
	}
	release <- struct{}{}
}

// 主代理首轮正常结束后，priority 汇报立即开新一轮，即使仍有 bee 在工作；
// 队列已空时再调用 DrainQueuedFollowUps 不会重复开轮。
func TestPriorityReportStartsAfterLeadTurnEvenWithBees(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	b.RegisterHarness(&controlledTestHarness{startedCh: started, releaseCh: release})
	b.AddThread(wire.ThreadSummary{ID: "lead", Provider: "controlled"}, nil)
	b.SetSwarmQueueHooks(func(string) bool { return true }, nil)
	ctx := context.Background()

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "initial"}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial turn did not start")
	}

	if err := b.SendSwarmReport(ctx, "lead", "[Hydra] report"); err != nil {
		t.Fatalf("SendSwarmReport failed: %v", err)
	}
	queue, err := b.QueueList(ctx, "lead")
	if err != nil || len(queue.FollowUps) != 1 {
		t.Fatalf("expected one queued swarm report, got %+v, %v", queue, err)
	}
	select {
	case <-started:
		t.Fatal("queued report must not open a turn while lead is running")
	default:
	}

	release <- struct{}{}
	// 首轮结束后 priority 汇报不受 hasWorkingSwarmBees 限制，立即开新一轮。
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("priority report did not open a new turn after lead turn ended")
	}
	queue, err = b.QueueList(ctx, "lead")
	if err != nil || len(queue.FollowUps) != 0 {
		t.Fatalf("expected report dequeued by the new turn, got %+v, %v", queue, err)
	}

	release <- struct{}{}
	waitThreadIdle(t, b, ctx, "lead")

	// 队列已空，再 drain 不应再开轮。
	if err := b.DrainQueuedFollowUps(ctx, "lead"); err != nil {
		t.Fatalf("DrainQueuedFollowUps failed: %v", err)
	}
	select {
	case <-started:
		t.Fatal("DrainQueuedFollowUps must not open a turn with an empty queue")
	default:
	}
}
