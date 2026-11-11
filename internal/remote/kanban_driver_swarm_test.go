package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
)

// StartSession 的 Provider/Model/Effort 覆盖：非空时覆盖从 Lead 读取的 Bee 偏好，
// 全为空时完全沿用 Lead 偏好（原有行为不变）。
func TestStartSession_ProviderModelEffortOverride(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.RegisterHarness(&mockHarness{kind: "codex"})
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	// Lead：claude/sonnet，Bees effort 为 medium
	model := "sonnet"
	lead, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", Model: &model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetThreadComposerPrefs(ctx, lead.ThreadID, ThreadComposerPrefs{
		InteractionMode: "build", Effort: "low", BeesEffort: "medium",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. 不带覆盖字段：沿用 Lead 偏好
	inheritID, err := b.StartSession(ctx, kanban.SessionRequest{
		LeadThreadID: lead.ThreadID, ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务",
	})
	if err != nil {
		t.Fatal(err)
	}
	inherit := waitThreadIdle(t, b, ctx, inheritID)
	if inherit.Provider != "claude" || derefString(inherit.Model) != "sonnet" || derefString(inherit.Effort) != "medium" {
		t.Fatalf("未指定覆盖字段时应沿用 Lead 偏好: %+v", inherit)
	}
	if inherit.FastMode {
		t.Fatalf("未指定 FastMode 时不应开启快速模式")
	}

	// 2. 显式覆盖 provider/model/effort，并开启快速模式
	overrideID, err := b.StartSession(ctx, kanban.SessionRequest{
		LeadThreadID: lead.ThreadID, ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务",
		Provider: "codex", Model: "gpt-5.4", Effort: "high", FastMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	override := waitThreadIdle(t, b, ctx, overrideID)
	if override.Provider != "codex" || derefString(override.Model) != "gpt-5.4" || derefString(override.Effort) != "high" {
		t.Fatalf("覆盖字段未生效: %+v", override)
	}
	if !override.FastMode {
		t.Fatalf("FastMode 未生效: %+v", override)
	}
}

// Provider 非空而 Model 为空时使用该 provider 的默认模型；无 Lead 时同样生效。
func TestStartSession_ProviderWithoutModelUsesDefault(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	beeHarness := &mockHarness{kind: "codex"}
	b.RegisterHarness(beeHarness)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务",
		Provider: "codex", Effort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	th := waitThreadIdle(t, b, ctx, threadID)
	if th.Provider != "codex" || derefString(th.Model) != DefaultModel("codex") || derefString(th.Effort) != "low" {
		t.Fatalf("provider 默认模型或 effort 不符合预期: %+v", th)
	}
	if calls := beeHarness.getTurnCalls(); len(calls) != 1 || calls[0].Model != DefaultModel("codex") {
		t.Fatalf("首轮应使用 provider 默认模型: %+v", calls)
	}
}

// StartSession 带 Swarm 时在建会话后立即开启编排，不带时不开。
func TestStartSession_SwarmEnablesBeforeFirstTurn(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	var enabled []string
	b.SetSwarmEnabler(func(threadID string, on bool) {
		if on {
			enabled = append(enabled, threadID)
		}
	})

	swarmID, err := b.StartSession(ctx, kanban.SessionRequest{ProjectID: "p1", WorkDir: t.TempDir(), Swarm: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartSession(ctx, kanban.SessionRequest{ProjectID: "p1", WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0] != swarmID {
		t.Fatalf("只有带 Swarm 的会话应开启编排, got %v want [%s]", enabled, swarmID)
	}
}

// StartSession 带编排配对时写入子代理偏好与配对 ID。
func TestStartSession_PairWritesBeesAndPairID(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.RegisterHarness(&mockHarness{kind: "codex"})
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	threadID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Swarm: true,
		BeesProvider: "codex", BeesModel: "gpt-5.4", BeesEffort: "high", PairID: "pair-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	th, err := b.GetThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if derefString(th.SwarmBeesProvider) != "codex" || derefString(th.SwarmBeesModel) != "gpt-5.4" ||
		derefString(th.SwarmBeesEffort) != "high" || derefString(th.SwarmPairID) != "pair-1" {
		t.Fatalf("配对未写入会话: %+v", th)
	}
}
