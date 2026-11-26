package remote

import (
	"context"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func TestTurnInteractionMode_ActiveTurnOverridesThreadPref(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(harness.Event{Type: harness.EventText, Text: "reply"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	b.SetHarness(mock, t.TempDir(), harness.ModeAgent)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := res.ThreadID

	if _, err := b.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{InteractionMode: "plan"}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}

	hookMode := ""
	b.SetTurnFinishedHook(func(id, _, _ string, _ bool) {
		if id == threadID {
			hookMode = b.TurnInteractionMode(id)
		}
	})

	build := "build"
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run build turn",
		ActionID: "act-1",
		Options:  &wire.ComposerOptions{InteractionMode: &build},
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-started

	if got := b.TurnInteractionMode(threadID); got != "build" {
		close(release)
		t.Fatalf("回合进行中 TurnInteractionMode 期望 build，实际 %q", got)
	}
	th, err := b.GetThread(ctx, threadID)
	if err != nil {
		close(release)
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.InteractionMode != "plan" {
		close(release)
		t.Fatalf("GetThread.InteractionMode 应保持为 plan，实际 %q", th.InteractionMode)
	}

	close(release)
	waitThreadIdle(t, b, ctx, threadID)

	if hookMode != "build" {
		t.Fatalf("回合收尾钩子期间 TurnInteractionMode 期望 build，实际 %q", hookMode)
	}
	if got := b.TurnInteractionMode(threadID); got != "plan" {
		t.Fatalf("回合结束后 TurnInteractionMode 应回落为会话偏好 plan，实际 %q", got)
	}
}

func TestTurnInteractionMode_IdleReturnsThreadPref(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := res.ThreadID

	if got := b.TurnInteractionMode(threadID); got != "build" {
		t.Fatalf("默认空闲会话 TurnInteractionMode 期望 build，实际 %q", got)
	}

	if _, err := b.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{InteractionMode: "plan"}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}
	if got := b.TurnInteractionMode(threadID); got != "plan" {
		t.Fatalf("设为 plan 后空闲会话 TurnInteractionMode 期望 plan，实际 %q", got)
	}
}

func TestTurnInteractionMode_UnknownThreadReturnsBuild(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	if got := b.TurnInteractionMode("missing-thread"); got != "build" {
		t.Fatalf("未知会话 TurnInteractionMode 期望 build，实际 %q", got)
	}
}

// setupBlockedTurnForModeTest 创建带阻塞 mockHarness 的会话，便于在回合进行中检验与切换交互模式。
func setupBlockedTurnForModeTest(t *testing.T) (*MemoryBackend, string, chan struct{}, chan struct{}) {
	t.Helper()
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(harness.Event{Type: harness.EventText, Text: "reply"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	b.SetHarness(mock, t.TempDir(), harness.ModeAgent)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	return b, res.ThreadID, started, release
}

func TestTurnInteractionMode_PlanApproval_UpdatesActiveTurnAndThreadPref(t *testing.T) {
	ctx := context.Background()
	b, threadID, started, release := setupBlockedTurnForModeTest(t)

	if _, err := b.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{InteractionMode: "plan"}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run plan turn",
		ActionID: "act-1",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	<-started
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	if got := b.TurnInteractionMode(threadID); got != "plan" {
		t.Fatalf("审批前 TurnInteractionMode 期望 plan，实际 %q", got)
	}

	approver := &threadApprover{b: b, threadID: threadID}
	approver.switchToBuild()

	if got := b.TurnInteractionMode(threadID); got != "build" {
		t.Fatalf("审批后 TurnInteractionMode 期望 build，实际 %q", got)
	}
	th, err := b.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.InteractionMode != "build" {
		t.Fatalf("审批后 GetThread.InteractionMode 期望 build，实际 %q", th.InteractionMode)
	}

	close(release)
	released = true
	waitThreadIdle(t, b, ctx, threadID)
}

func TestTurnInteractionMode_PlanApproval_ResetsOptionsOverride(t *testing.T) {
	ctx := context.Background()
	b, threadID, started, release := setupBlockedTurnForModeTest(t)

	plan := "plan"
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run overridden plan turn",
		ActionID: "act-1",
		Options:  &wire.ComposerOptions{InteractionMode: &plan},
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	<-started
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	if got := b.TurnInteractionMode(threadID); got != "plan" {
		t.Fatalf("审批前 TurnInteractionMode 期望 plan，实际 %q", got)
	}

	approver := &threadApprover{b: b, threadID: threadID}
	approver.switchToBuild()

	if got := b.TurnInteractionMode(threadID); got != "build" {
		t.Fatalf("审批后 TurnInteractionMode 期望 build，实际 %q", got)
	}
	th, err := b.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.InteractionMode != "build" {
		t.Fatalf("审批后 GetThread.InteractionMode 期望 build，实际 %q", th.InteractionMode)
	}

	close(release)
	released = true
	waitThreadIdle(t, b, ctx, threadID)
}
