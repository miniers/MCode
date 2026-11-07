package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// newRememberSelectionBackend 建一个带 claude/codex 两个 harness 与一个项目的后端。
func newRememberSelectionBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	// SetHarness 同时把 claude 设为默认 provider，codex 作为子代理侧的备选。
	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.RegisterHarness(&mockHarness{kind: "codex"})
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
	return b
}

// TestCreateThreadNeverWritesMemory 覆盖旧 bug 的根因：建会话只读记忆、不写记忆，
// 不再把新会话的默认回退值（编排开关恒 false、子代理字段为 nil）回写成用户选择。
func TestCreateThreadNeverWritesMemory(t *testing.T) {
	ctx := context.Background()
	b := newRememberSelectionBackend(t)

	writes := 0
	b.SetRememberSelectionFunc(func(wire.ProvidersRememberSelectionParams) { writes++ })
	model := DefaultModel("claude")
	b.SetDraftDefaultsFunc(func() wire.ComposerDraftDefaults {
		return wire.ComposerDraftDefaults{
			Provider:     "claude",
			Model:        &model,
			SwarmEnabled: true,
		}
	})

	// 用户主动新建（threads.create）。
	if _, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"}); err != nil {
		t.Fatalf("用户新建会话失败: %v", err)
	}
	// 系统路径（看板任务、编排子代理、自动化）。
	if _, err := b.createThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"}, false); err != nil {
		t.Fatalf("系统会话创建失败: %v", err)
	}
	if writes != 0 {
		t.Fatalf("建会话不应写回记忆，实际写回 %d 次", writes)
	}
}

// TestCreateThreadFillsDefaultsFromMemory 验证用户主动新建且没传 Options 时，
// 主导组、子代理组与编排开关全部取注入的记忆默认值，且编排在创建立即开启。
func TestCreateThreadFillsDefaultsFromMemory(t *testing.T) {
	ctx := context.Background()
	b := newRememberSelectionBackend(t)

	leadModel := DefaultModel("codex")
	effort := "high"
	beesProvider, beesModel, beesEffort := "codex", DefaultModel("codex"), "low"
	b.SetDraftDefaultsFunc(func() wire.ComposerDraftDefaults {
		return wire.ComposerDraftDefaults{
			Provider:          "codex",
			Model:             &leadModel,
			Effort:            &effort,
			FastMode:          true,
			SwarmEnabled:      true,
			SwarmBeesProvider: &beesProvider,
			SwarmBeesModel:    &beesModel,
			SwarmBeesEffort:   &beesEffort,
		}
	})
	var enabled []string
	b.SetSwarmEnabler(func(threadID string, on bool) {
		if on {
			enabled = append(enabled, threadID)
		}
	})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("用户新建会话失败: %v", err)
	}
	th, err := b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.Provider != "codex" {
		t.Errorf("provider 应来自记忆，实际 %q", th.Provider)
	}
	if got := derefString(th.Model); got != leadModel {
		t.Errorf("model 应来自记忆，实际 %q", got)
	}
	if got := derefString(th.Effort); got != effort {
		t.Errorf("effort 应来自记忆，实际 %q", got)
	}
	if !th.FastMode {
		t.Errorf("fastMode 应来自记忆")
	}
	if got := derefString(th.SwarmBeesProvider); got != beesProvider {
		t.Errorf("swarmBeesProvider 应来自记忆，实际 %q", got)
	}
	if got := derefString(th.SwarmBeesModel); got != beesModel {
		t.Errorf("swarmBeesModel 应来自记忆，实际 %q", got)
	}
	if got := derefString(th.SwarmBeesEffort); got != beesEffort {
		t.Errorf("swarmBeesEffort 应来自记忆，实际 %q", got)
	}
	if len(enabled) != 1 || enabled[0] != res.ThreadID {
		t.Errorf("补齐得到编排开关的新会话应在创建时开启编排，实际开启 %v", enabled)
	}

	// 系统路径不读记忆：仍是默认 provider、无推理强度、不开启编排。
	sysRes, err := b.createThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"}, false)
	if err != nil {
		t.Fatalf("系统会话创建失败: %v", err)
	}
	sysThread, err := b.GetThread(ctx, sysRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if sysThread.Provider != "claude" || sysThread.Effort != nil || sysThread.FastMode {
		t.Errorf("系统会话不应读记忆，实际 %+v", sysThread)
	}
	if len(enabled) != 1 {
		t.Errorf("系统会话不应开启编排，实际 %v", enabled)
	}
}

// TestCreateThreadDoesNotFillEffortWhenModelGiven 验证只要给了 provider 或 model，
// 就不再补 effort/fastMode，避免把属于别的模型的推理强度套到用户显式选的模型上。
func TestCreateThreadDoesNotFillEffortWhenModelGiven(t *testing.T) {
	ctx := context.Background()
	b := newRememberSelectionBackend(t)

	draftModel := DefaultModel("codex")
	effort := "high"
	beesModel := DefaultModel("codex")
	b.SetDraftDefaultsFunc(func() wire.ComposerDraftDefaults {
		return wire.ComposerDraftDefaults{
			Provider:       "codex",
			Model:          &draftModel,
			Effort:         &effort,
			FastMode:       true,
			SwarmBeesModel: &beesModel,
		}
	})

	chosen := DefaultModel("claude")
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1", Model: &chosen})
	if err != nil {
		t.Fatalf("用户新建会话失败: %v", err)
	}
	th, err := b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.Provider != "claude" || derefString(th.Model) != chosen {
		t.Errorf("显式选择的 provider/model 不应被覆盖，实际 %q/%q", th.Provider, derefString(th.Model))
	}
	if th.Effort != nil {
		t.Errorf("显式选了模型时不应补 effort，实际 %q", derefString(th.Effort))
	}
	if th.FastMode {
		t.Errorf("显式选了模型时不应补 fastMode")
	}
	if got := derefString(th.SwarmBeesModel); got != beesModel {
		t.Errorf("子代理组三字段都为 nil 时应整组补齐，实际 %q", got)
	}
}

// TestRememberSelectionForwardsUserSelection 验证 providers.rememberSelection 在各种字段组合下
// 把用户真正给出的选择原样交给注入的写回函数，未给字段保持 nil。
func TestRememberSelectionForwardsUserSelection(t *testing.T) {
	ctx := context.Background()
	b := newRememberSelectionBackend(t)

	var got []wire.ProvidersRememberSelectionParams
	b.SetRememberSelectionFunc(func(p wire.ProvidersRememberSelectionParams) { got = append(got, p) })

	provider, model, effort := "claude", DefaultModel("claude"), "high"
	fast := true
	beesModel := DefaultModel("codex")
	swarm := true
	if err := b.RememberSelection(ctx, wire.ProvidersRememberSelectionParams{
		Provider:       &provider,
		Model:          &model,
		Effort:         &effort,
		FastMode:       &fast,
		SwarmBeesModel: &beesModel,
		SwarmEnabled:   &swarm,
	}); err != nil {
		t.Fatalf("RememberSelection 失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应写回 1 次，实际 %d", len(got))
	}
	first := got[0]
	if derefString(first.Provider) != provider || derefString(first.Model) != model || derefString(first.Effort) != effort {
		t.Errorf("主导字段写入错误: %+v", first)
	}
	if first.FastMode == nil || !*first.FastMode {
		t.Errorf("fastMode 写入错误: %+v", first)
	}
	if derefString(first.SwarmBeesModel) != beesModel || first.SwarmBeesProvider != nil || first.SwarmBeesEffort != nil {
		t.Errorf("子代理字段写入错误: %+v", first)
	}
	if first.SwarmEnabled == nil || !*first.SwarmEnabled {
		t.Errorf("swarmEnabled 写入错误: %+v", first)
	}

	// 只改编排开关：主导与子代理字段保持 nil，表示不写这两组记忆。
	off := false
	if err := b.RememberSelection(ctx, wire.ProvidersRememberSelectionParams{SwarmEnabled: &off}); err != nil {
		t.Fatalf("RememberSelection 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应写回 2 次，实际 %d", len(got))
	}
	second := got[1]
	if second.Provider != nil || second.Model != nil || second.Effort != nil || second.FastMode != nil {
		t.Errorf("未给出的主导字段应保持 nil: %+v", second)
	}
	if second.SwarmBeesProvider != nil || second.SwarmBeesModel != nil || second.SwarmBeesEffort != nil {
		t.Errorf("未给出的子代理字段应保持 nil: %+v", second)
	}
	if second.SwarmEnabled == nil || *second.SwarmEnabled {
		t.Errorf("swarmEnabled 写入错误: %+v", second)
	}
}

// TestRememberSelectionRejectsInvalidSelection 验证非法 provider/model 返回 InvalidParams 且什么都不写。
func TestRememberSelectionRejectsInvalidSelection(t *testing.T) {
	ctx := context.Background()
	b := newRememberSelectionBackend(t)

	writes := 0
	b.SetRememberSelectionFunc(func(wire.ProvidersRememberSelectionParams) { writes++ })

	provider, badModel := "claude", "not-a-real-model"
	err := b.RememberSelection(ctx, wire.ProvidersRememberSelectionParams{Provider: &provider, Model: &badModel})
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("非法模型应返回 InvalidParams，实际 %v", err)
	}

	unknownProvider, model := "unknown", "some-model"
	err = b.RememberSelection(ctx, wire.ProvidersRememberSelectionParams{Provider: &unknownProvider, Model: &model})
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("未注册的 provider 应返回 InvalidParams，实际 %v", err)
	}

	if writes != 0 {
		t.Fatalf("校验失败时不应写回记忆，实际写回 %d 次", writes)
	}
}

// TestRememberSelectionWithoutCallbackIsNoop 验证未注入写回函数时只做校验、正常返回。
func TestRememberSelectionWithoutCallbackIsNoop(t *testing.T) {
	ctx := context.Background()
	b := newRememberSelectionBackend(t)

	provider, model := "claude", DefaultModel("claude")
	if err := b.RememberSelection(ctx, wire.ProvidersRememberSelectionParams{Provider: &provider, Model: &model}); err != nil {
		t.Fatalf("未注入写回函数时应正常返回，实际 %v", err)
	}
}
