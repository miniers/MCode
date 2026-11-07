package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 运行中开启「插入当前回合」但 Provider 不支持 steer 时，发送的消息会降级成排队；
// 降级项必须带上 Intent=insert 并落库，普通 whenRunning=queue 的项不带 Intent，
// 模拟进程重启后两者仍能从 store 恢复到队列快照。
func TestFollowUpIntentInsertDegradeSurvivesReload(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "follow_up_intent.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	startedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{}, 1)
	// controlledTestHarness 不实现 harness.Steerer，插入请求必然降级为排队。
	h := &controlledTestHarness{startedCh: startedCh, releaseCh: releaseCh}
	backend := NewMemoryBackend(hub)
	backendClosed := false
	t.Cleanup(func() {
		if !backendClosed {
			backend.Close()
		}
	})
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{InsertIntoRunningTurn: true}}
	})
	backend.SetHarness(h, t.TempDir(), harness.ModeFullAccess)
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "p_intent", Path: t.TempDir()})
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p_intent"})
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

	// 插入请求无法注入，降级为排队并记下原始意图。
	degraded, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "想插进当前回合"})
	if err != nil {
		t.Fatalf("发送插入消息失败: %v", err)
	}
	if degraded.Queued == nil || !*degraded.Queued {
		t.Fatalf("插入请求应降级为排队，实际结果 %+v", degraded)
	}
	// 显式排队的消息保持普通排队语义。
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "普通排队", WhenRunning: "queue"}); err != nil {
		t.Fatalf("发送普通排队消息失败: %v", err)
	}

	snap, err := backend.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("QueueList 失败: %v", err)
	}
	if len(snap.FollowUps) != 2 {
		t.Fatalf("期望队列里 2 条，实际 %d 条: %+v", len(snap.FollowUps), snap.FollowUps)
	}
	if snap.FollowUps[0].Intent != "insert" {
		t.Fatalf("降级项 Intent 应为 insert，实际 %q", snap.FollowUps[0].Intent)
	}
	if snap.FollowUps[1].Intent != "" {
		t.Fatalf("普通排队项 Intent 应为空串，实际 %q", snap.FollowUps[1].Intent)
	}

	// 打断首轮，避免正常结束路径自动出队清空队列。
	if _, err := backend.InterruptCurrent(ctx, threadID); err != nil {
		t.Fatalf("InterruptCurrent 失败: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)
	backend.Close()
	backendClosed = true

	// 模拟进程重启：新后端只通过 store 恢复队列。
	reloaded := NewMemoryBackend(hub)
	t.Cleanup(reloaded.Close)
	if err := reloaded.AttachStore(ctx, st); err != nil {
		t.Fatalf("重启后 AttachStore 失败: %v", err)
	}
	reloadedSnap, err := reloaded.QueueList(ctx, threadID)
	if err != nil {
		t.Fatalf("重启后 QueueList 失败: %v", err)
	}
	if len(reloadedSnap.FollowUps) != 2 {
		t.Fatalf("重启后期望队列里 2 条，实际 %d 条: %+v", len(reloadedSnap.FollowUps), reloadedSnap.FollowUps)
	}
	if reloadedSnap.FollowUps[0].Intent != "insert" {
		t.Fatalf("重启后降级项 Intent 应保留 insert，实际 %q", reloadedSnap.FollowUps[0].Intent)
	}
	if reloadedSnap.FollowUps[1].Intent != "" {
		t.Fatalf("重启后普通排队项 Intent 应仍为空串，实际 %q", reloadedSnap.FollowUps[1].Intent)
	}
}
