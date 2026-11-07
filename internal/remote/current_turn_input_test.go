package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// waitTurnInputStarted 轮询等待指定会话出现运行中的轮次，避免在开轮前读取 CurrentTurnInput。
func waitTurnInputStarted(t *testing.T, backend *MemoryBackend, threadID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, active := backend.CurrentTurnInput(threadID); active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待会话 %s 开轮超时", threadID)
}

// 用户消息开的轮：CurrentTurnInput 返回空 Kind 与该条消息文本；轮次结束后 active 变 false，
// 已记录的输入保留到下一轮开始。
func TestMemoryBackend_CurrentTurnInputUserMessage(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	release := make(chan struct{})
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	const threadID = "00000000-0000-0000-0000-0000000000d1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "Turn Input"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "用户亲自发的提问"}); err != nil {
		t.Fatalf("SendMessage 失败: %v", err)
	}
	waitTurnInputStarted(t, backend, threadID)

	kind, text, active := backend.CurrentTurnInput(threadID)
	if !active {
		t.Fatalf("轮次运行中应返回 active=true")
	}
	if kind != "" {
		t.Errorf("用户消息开的轮 Kind 应为空串，实际 %q", kind)
	}
	if text != "用户亲自发的提问" {
		t.Errorf("开轮文本 = %q, want %q", text, "用户亲自发的提问")
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)

	kind, text, active = backend.CurrentTurnInput(threadID)
	if active {
		t.Errorf("轮次结束后应返回 active=false")
	}
	if kind != "" || text != "用户亲自发的提问" {
		t.Errorf("轮次结束后应保留最近一轮输入，实际 kind=%q text=%q", kind, text)
	}
}

// 宿主事件开的轮：Kind 为 host_event；不存在的会话返回零值与 active=false。
func TestMemoryBackend_CurrentTurnInputHostEvent(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	release := make(chan struct{})
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	const threadID = "00000000-0000-0000-0000-0000000000d2"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "Host Event"}, nil)

	const eventText = "宿主汇总了 3 条待处理事件"
	if err := backend.SendHostEvent(ctx, threadID, eventText); err != nil {
		t.Fatalf("SendHostEvent 失败: %v", err)
	}
	waitTurnInputStarted(t, backend, threadID)

	kind, text, active := backend.CurrentTurnInput(threadID)
	if !active {
		t.Fatalf("宿主事件开的轮应返回 active=true")
	}
	if kind != threadMessageKindHostEvent {
		t.Errorf("宿主事件开的轮 Kind = %q, want %q", kind, threadMessageKindHostEvent)
	}
	if text != eventText {
		t.Errorf("开轮文本 = %q, want %q", text, eventText)
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)

	if kind, text, active := backend.CurrentTurnInput("不存在的会话"); kind != "" || text != "" || active {
		t.Errorf("不存在的会话应返回零值，实际 kind=%q text=%q active=%v", kind, text, active)
	}
}

// 运行中排队的宿主事件走「队列立即发送」路径开轮时，开轮输入的 Kind 与落库消息的 Kind
// 都必须保持 host_event，不能退化成普通用户消息。
func TestMemoryBackend_CurrentTurnInputSendNowKeepsHostEventKind(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "current_turn_input.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	release := make(chan struct{})
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}}
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "p_input", Path: t.TempDir()})
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p_input"})
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	threadID := created.ThreadID

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "先跑一轮"}); err != nil {
		t.Fatalf("发送首条消息失败: %v", err)
	}
	waitTurnInputStarted(t, backend, threadID)

	const eventText = "宿主汇总了 2 条待处理事件"
	if err := backend.SendHostEvent(ctx, threadID, eventText); err != nil {
		t.Fatalf("SendHostEvent 失败: %v", err)
	}
	queued, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList 失败: %v", err)
	}
	if len(queued.FollowUps) != 1 || queued.FollowUps[0].Kind != threadMessageKindHostEvent {
		t.Fatalf("入队项应保留 Kind=host_event，实际 %+v", queued.FollowUps)
	}

	// 打断首轮，让队列留在原地，再由「立即发送」出队开轮。
	if _, err := backend.InterruptCurrent(ctx, threadID); err != nil {
		t.Fatalf("InterruptCurrent 失败: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	if _, err := backend.QueueSendNow(ctx, wire.ThreadsQueueSendNowParams{
		ThreadID:   threadID,
		FollowUpID: queued.FollowUps[0].ID,
	}); err != nil {
		t.Fatalf("QueueSendNow 失败: %v", err)
	}

	kind, text, active := backend.CurrentTurnInput(threadID)
	if !active {
		t.Fatalf("立即发送开出的轮应返回 active=true")
	}
	if kind != threadMessageKindHostEvent {
		t.Errorf("立即发送开出的轮 Kind = %q, want %q", kind, threadMessageKindHostEvent)
	}
	if text != eventText {
		t.Errorf("立即发送开出的轮文本 = %q, want %q", text, eventText)
	}

	history, _, err := st.ListMessages(ctx, threadID, 0, 100)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	var found bool
	for _, msg := range history {
		if msg.Role == "user" && msg.Text == eventText {
			found = true
			if msg.Kind != threadMessageKindHostEvent {
				t.Fatalf("立即发送落库的用户消息 Kind 应为 host_event，实际 %q", msg.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("落库消息里找不到立即发送的宿主事件消息: %+v", history)
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)
}

// 运行中 steer 插入的用户消息不改写 CurrentTurnInput 的结果：仍是开轮那条输入的 Kind 与文本。
func TestMemoryBackend_CurrentTurnInputIgnoresSteer(t *testing.T) {
	h := &steerStateHarness{started: make(chan struct{}), release: make(chan struct{}), text: make(chan string, 1)}
	backend := runtimeBackend(t, h)
	ctx := context.Background()
	threadID := runtimeThread(backend)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "第一轮提问"}); err != nil {
		t.Fatalf("SendMessage 失败: %v", err)
	}
	select {
	case <-h.started:
	case <-time.After(3 * time.Second):
		t.Fatal("等待首轮开始超时")
	}

	steered, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "插话补充"})
	if err != nil {
		t.Fatalf("steer 发送失败: %v", err)
	}
	if steered.TurnID == nil {
		t.Fatalf("steer 应复用当前轮次，实际 %+v", steered)
	}

	kind, text, active := backend.CurrentTurnInput(threadID)
	if !active {
		t.Fatalf("steer 后轮次仍在运行，应返回 active=true")
	}
	if kind != "" || text != "第一轮提问" {
		t.Errorf("steer 不应改写开轮输入，实际 kind=%q text=%q", kind, text)
	}

	backend.InterruptCurrent(ctx, threadID)
	waitThreadIdle(t, backend, ctx, threadID)
}
