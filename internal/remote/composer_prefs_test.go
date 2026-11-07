package remote

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// SetThreadComposerPrefs 正常更新 summary，空字符串的指针字段置 nil
func TestSetThreadComposerPrefs_UpdatesSummary(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	th, err := b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.InteractionMode != "build" {
		t.Errorf("expected default interactionMode 'build', got %q", th.InteractionMode)
	}

	sum, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, ThreadComposerPrefs{
		InteractionMode: "plan",
		Effort:          "high",
		BeesProvider:    "codex",
		BeesModel:       "gpt-5",
		BeesEffort:      "low",
	})
	if err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}
	if sum.InteractionMode != "plan" {
		t.Errorf("expected interactionMode 'plan', got %q", sum.InteractionMode)
	}
	if sum.Effort == nil || *sum.Effort != "high" {
		t.Errorf("expected effort 'high', got %v", sum.Effort)
	}
	if sum.SwarmBeesProvider == nil || *sum.SwarmBeesProvider != "codex" ||
		sum.SwarmBeesModel == nil || *sum.SwarmBeesModel != "gpt-5" ||
		sum.SwarmBeesEffort == nil || *sum.SwarmBeesEffort != "low" {
		t.Errorf("unexpected bees prefs: %+v", sum)
	}

	th, err = b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.InteractionMode != "plan" || th.Effort == nil || *th.Effort != "high" {
		t.Errorf("summary not updated: %+v", th)
	}

	// 空 interactionMode 视为 build，空字符串字段清空为 nil
	sum, err = b.SetThreadComposerPrefs(ctx, res.ThreadID, ThreadComposerPrefs{})
	if err != nil {
		t.Fatalf("SetThreadComposerPrefs reset failed: %v", err)
	}
	if sum.InteractionMode != "build" || sum.Effort != nil || sum.SwarmBeesProvider != nil || sum.SwarmBeesModel != nil || sum.SwarmBeesEffort != nil {
		t.Errorf("expected reset prefs, got %+v", sum)
	}
}

// 非法 interactionMode/effort 被拒，不存在的线程返回 ResourceNotFound
func TestSetThreadComposerPrefs_InvalidParams(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", ActionID: "a1"})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	cases := []ThreadComposerPrefs{
		{InteractionMode: "chat"},
		{Effort: "extreme"},
		{BeesEffort: "extreme"},
	}
	for _, p := range cases {
		_, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, p)
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
			t.Errorf("prefs %+v: expected InvalidParams error, got: %v", p, err)
		}
	}

	th, _ := b.GetThread(ctx, res.ThreadID)
	if th.InteractionMode != "build" || th.Effort != nil {
		t.Errorf("invalid prefs must not modify summary, got %+v", th)
	}

	_, err = b.SetThreadComposerPrefs(ctx, "missing-thread", ThreadComposerPrefs{InteractionMode: "plan"})
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ResourceNotFound error, got: %v", err)
	}
}

// 发送消息时 Turn.InteractionMode/Effort 取自会话偏好，Options 只覆盖本轮
func TestSetThreadComposerPrefs_AppliedToTurn(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
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

	if _, err := b.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{InteractionMode: "plan", Effort: "medium"}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "turn 1", ActionID: "act-1"}); err != nil {
		t.Fatalf("SendMessage turn 1 failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	// 第二轮通过 Options 覆盖，仅作用于本轮
	build := "build"
	high := "high"
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "turn 2",
		ActionID: "act-2",
		Options:  &wire.ComposerOptions{InteractionMode: &build, Effort: &high},
	}); err != nil {
		t.Fatalf("SendMessage turn 2 failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 turn calls, got %d", len(calls))
	}
	if calls[0].InteractionMode != "plan" || calls[0].Effort != "medium" {
		t.Errorf("turn 1 expected plan/medium, got %q/%q", calls[0].InteractionMode, calls[0].Effort)
	}
	if calls[0].Mode != harness.ModeReadOnly || !strings.Contains(calls[0].Prompt, "仅分析并制定计划") {
		t.Errorf("Plan 必须向所有供应商传递只读档位和规划指令: %+v", calls[0])
	}
	if calls[1].Mode == harness.ModeReadOnly {
		t.Error("Build 不应继承上一轮的 Plan 只读档位")
	}
	if calls[1].InteractionMode != "build" || calls[1].Effort != "high" {
		t.Errorf("turn 2 expected build/high, got %q/%q", calls[1].InteractionMode, calls[1].Effort)
	}

	th, _ := b.GetThread(ctx, threadID)
	if th.InteractionMode != "plan" || th.Effort == nil || *th.Effort != "medium" {
		t.Errorf("Options must not modify thread prefs, got %+v", th)
	}
}

func TestComposerPrefs_PersistReloadAndFailure(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "prefs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	prefs := ThreadComposerPrefs{InteractionMode: "plan", Effort: "high", BeesProvider: "codex", BeesModel: "gpt-5.5", BeesEffort: "low"}
	if _, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, prefs); err != nil {
		t.Fatal(err)
	}
	reloaded := NewMemoryBackend(hub)
	defer reloaded.Close()
	if err := reloaded.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.GetThread(ctx, res.ThreadID)
	if err != nil || got.InteractionMode != "plan" || derefString(got.Effort) != "high" ||
		derefString(got.SwarmBeesProvider) != "codex" || derefString(got.SwarmBeesModel) != "gpt-5.5" || derefString(got.SwarmBeesEffort) != "low" {
		t.Fatalf("重载偏好不一致: %+v, %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, ThreadComposerPrefs{}); err == nil {
		t.Fatal("落盘失败必须返回错误")
	}
	got, _ = b.GetThread(ctx, res.ThreadID)
	if got.InteractionMode != "plan" || derefString(got.Effort) != "high" {
		t.Fatalf("落盘失败不得更新内存: %+v", got)
	}
}

func TestComposerPrefs_BeesAppliedBeforeFirstTurn(t *testing.T) {
	for _, tc := range []struct {
		name, beesProvider, beesModel, wantProvider, wantModel string
	}{
		{"跟随 Lead", "", "", "claude", "sonnet"},
		{"独立模型", "codex", "gpt-5.4", "codex", "gpt-5.4"},
		{"独立供应商默认模型", "codex", "", "codex", "gpt-5.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			b := NewMemoryBackend(hub)
			defer b.Close()
			leadHarness := &mockHarness{kind: "claude"}
			beeHarness := &mockHarness{kind: "codex"}
			b.SetHarness(leadHarness, t.TempDir(), harness.ModeFullAccess)
			b.RegisterHarness(beeHarness)
			b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
			model := "sonnet"
			res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", Model: &model})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, ThreadComposerPrefs{
				InteractionMode: "build", Effort: "low",
				BeesProvider: tc.beesProvider, BeesModel: tc.beesModel, BeesEffort: "high",
			}); err != nil {
				t.Fatal(err)
			}
			beeID, err := b.StartSession(ctx, kanban.SessionRequest{
				LeadThreadID: res.ThreadID, ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务",
			})
			if err != nil {
				t.Fatal(err)
			}
			waitThreadIdle(t, b, ctx, beeID)
			bee, _ := b.GetThread(ctx, beeID)
			if bee.Provider != tc.wantProvider || derefString(bee.Model) != tc.wantModel || derefString(bee.Effort) != "high" {
				t.Fatalf("Bees 未应用设置: %+v", bee)
			}
			calls := beeHarness.getTurnCalls()
			if tc.wantProvider == "claude" {
				calls = leadHarness.getTurnCalls()
			}
			if len(calls) != 1 || calls[0].Model != tc.wantModel || calls[0].Effort != "high" {
				t.Fatalf("首轮未使用 Bees 设置: %+v", calls)
			}
		})
	}
}

// 新建会话传入的 Coder 映射须落盘，并在重载后用于首个 Bee。
func TestCreateThread_BeesOptionsPersistAndApply(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	model, beesProvider, beesModel := "sonnet", "codex", "gpt-5.4"
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		Options: &wire.ComposerOptions{
			Model: &model, SwarmBeesProvider: &beesProvider, SwarmBeesModel: &beesModel,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded := NewMemoryBackend(hub)
	defer reloaded.Close()
	if err := reloaded.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	beeHarness := &mockHarness{kind: "codex"}
	reloaded.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	reloaded.RegisterHarness(beeHarness)
	got, err := reloaded.GetThread(ctx, res.ThreadID)
	if err != nil || derefString(got.SwarmBeesProvider) != beesProvider || derefString(got.SwarmBeesModel) != beesModel {
		t.Fatalf("新建会话的 Coder 映射未持久化: %+v, %v", got, err)
	}
	beeID, err := reloaded.StartSession(ctx, kanban.SessionRequest{
		LeadThreadID: res.ThreadID, ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, reloaded, ctx, beeID)
	calls := beeHarness.getTurnCalls()
	if len(calls) != 1 || calls[0].Model != beesModel {
		t.Fatalf("首个 Bee 未使用 Coder 映射: %+v", calls)
	}
}

func TestComposerPrefs_TurnSnapshotAndNoPlanDelegation(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	mock := &mockHarness{}
	b.SetHarness(mock, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, ThreadComposerPrefs{InteractionMode: "plan", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	st := b.startTurnLocked(b.threads[res.ThreadID], "制定计划")
	b.mu.Unlock()
	if _, err := b.SetThreadComposerPrefs(ctx, res.ThreadID, ThreadComposerPrefs{InteractionMode: "build", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	reported := false
	b.SetTurnFinishedHook(func(string, string, string, bool) { reported = true })
	b.runHarness(st.runCtx, res.ThreadID, st.turnID, "制定计划", st.turnIndex, nil, nil, st.interactionMode, st.effort)
	calls := mock.getTurnCalls()
	if len(calls) != 1 || calls[0].InteractionMode != "plan" || calls[0].Effort != "low" || reported {
		t.Fatalf("启动前的切换不得改变本轮偏好，Plan 不应触发委派: calls=%+v, reported=%v", calls, reported)
	}
	delegated := false
	b.SetSwarmQueueHooks(nil, func(context.Context, string, string) (string, error) {
		delegated = true
		return "", nil
	})
	plan, emptyEffort := "plan", ""
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: res.ThreadID, Text: "本轮只规划",
		Options: &wire.ComposerOptions{InteractionMode: &plan, Effort: &emptyEffort},
	}); err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, b, ctx, res.ThreadID)
	calls = mock.getTurnCalls()
	if delegated || len(calls) != 2 || calls[1].Effort != "" {
		t.Fatalf("本轮 Plan 覆盖不得转发到 Bee，空 effort 应覆盖已保存值: delegated=%v, calls=%+v", delegated, calls)
	}
}
