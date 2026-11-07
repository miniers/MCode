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

// 宿主事件在会话忙碌时入队，Kind 必须随排队项一起落库；模拟进程重启后从 store
// 重新加载队列再出队，落库的那条用户消息仍应保留 host_event，而不是退化成普通用户消息。
func TestHostEventKindSurvivesQueueReload(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "host_event_kind.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend := NewMemoryBackend(hub)
	backendClosed := false
	t.Cleanup(func() {
		if !backendClosed {
			backend.Close()
		}
	})
	backend.SetHarness(h, t.TempDir(), harness.ModeFullAccess)
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "p_kind", Path: t.TempDir()})
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p_kind"})
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	threadID := created.ThreadID

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "先跑一轮"}); err != nil {
		t.Fatalf("发送首条消息失败: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待首轮开始超时")
	}

	const eventText = "宿主汇总了 2 条待处理事件"
	if err := backend.SendHostEvent(ctx, threadID, eventText); err != nil {
		t.Fatalf("SendHostEvent 失败: %v", err)
	}

	queued, err := st.ListFollowUps(ctx, threadID)
	if err != nil {
		t.Fatalf("ListFollowUps 失败: %v", err)
	}
	if len(queued) != 1 || queued[0].Kind != threadMessageKindHostEvent {
		t.Fatalf("入队后应把 Kind=host_event 落库，实际 %+v", queued)
	}

	// 打断首轮，强制打断路径不会自动开 priority 队列，事件留在队列里等重启恢复。
	if _, err := backend.InterruptCurrent(ctx, threadID); err != nil {
		t.Fatalf("InterruptCurrent 失败: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)
	backend.Close()
	backendClosed = true

	// 模拟进程重启：新后端只通过 store 恢复队列，不再有内存里的 map。
	reloaded := NewMemoryBackend(hub)
	t.Cleanup(reloaded.Close)
	reloaded.SetHarness(&mockHarness{kind: "controlled"}, t.TempDir(), harness.ModeFullAccess)
	if err := reloaded.AttachStore(ctx, st); err != nil {
		t.Fatalf("重启后 AttachStore 失败: %v", err)
	}
	snap, err := reloaded.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("重启后 QueueList 失败: %v", err)
	}
	if len(snap.FollowUps) != 1 || snap.FollowUps[0].Kind != threadMessageKindHostEvent {
		t.Fatalf("重启后排队项应保留 Kind=host_event，实际 %+v", snap.FollowUps)
	}
	if err := reloaded.DrainQueuedFollowUps(ctx, threadID); err != nil {
		t.Fatalf("重启后出队失败: %v", err)
	}
	waitThreadIdle(t, reloaded, ctx, threadID)

	history, _, err := st.ListMessages(ctx, threadID, 0, 100)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	var found bool
	for _, msg := range history {
		if msg.Role == "user" && msg.Text == eventText {
			found = true
			if msg.Kind != threadMessageKindHostEvent {
				t.Fatalf("重启后出队的用户消息落库 Kind 应为 host_event，实际 %q", msg.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("落库消息里找不到出队后的宿主事件消息: %+v", history)
	}
}
