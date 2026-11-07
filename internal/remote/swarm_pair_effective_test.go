package remote

import (
	"context"
	"testing"

	"mcode/internal/config"
	"mcode/internal/remote/wire"
)

// newSwarmPairTestBackend 建一个内存后端与一条会话，供生效配对的计算测试使用。
func newSwarmPairTestBackend(t *testing.T) (*MemoryBackend, string) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	proj, err := backend.AddProjectPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}
	created, err := backend.CreateThread(context.Background(), wire.ThreadCreateParams{ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	return backend, created.ThreadID
}

// setThreadLeadModel 直接改写会话摘要里的主导 provider/model，避免依赖 provider 目录的模型校验。
func setThreadLeadModel(t *testing.T, backend *MemoryBackend, threadID, provider, model string) {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	td, ok := backend.threads[threadID]
	if !ok {
		t.Fatalf("thread %s not found", threadID)
	}
	td.summary.Provider = provider
	td.summary.Model = optionalString(model)
}

// 未显式选配对时按会话的主导与子代理模型推断出生效配对。
func TestSwarmThreadContext_InfersPairFromModels(t *testing.T) {
	backend, threadID := newSwarmPairTestBackend(t)
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			Pairs: []config.SwarmPair{
				{ID: "pair-claude", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "claude", WorkerModel: "sonnet"},
				{ID: "pair-codex", LeadProvider: "codex", LeadModel: "gpt-5", WorkerProvider: "codex", WorkerModel: "gpt-5-mini"},
			},
		}
	})
	setThreadLeadModel(t, backend, threadID, "codex", "gpt-5")
	if _, err := backend.SetThreadComposerPrefs(context.Background(), threadID, ThreadComposerPrefs{
		InteractionMode: "build", BeesProvider: "codex", BeesModel: "gpt-5-mini",
	}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}

	_, pairID, _, _, _, _ := backend.SwarmThreadContext(threadID)
	if pairID != "pair-codex" {
		t.Errorf("expected inferred pair %q, got %q", "pair-codex", pairID)
	}
}

// 显式选的配对仍与当前模型一致时优先于推断：同组合的两条配对可以按选择切换。
func TestSwarmThreadContext_ExplicitPairWinsOverInference(t *testing.T) {
	backend, threadID := newSwarmPairTestBackend(t)
	pairA := config.SwarmPair{ID: "pair-a", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "claude", WorkerModel: "sonnet"}
	pairB := config.SwarmPair{ID: "pair-b", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "claude", WorkerModel: "sonnet"}
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{Pairs: []config.SwarmPair{pairA, pairB}}
	})
	setThreadLeadModel(t, backend, threadID, "claude", "opus")
	if _, err := backend.SetThreadComposerPrefs(context.Background(), threadID, ThreadComposerPrefs{
		InteractionMode: "build", BeesProvider: "claude", BeesModel: "sonnet",
	}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}
	if err := backend.SetThreadSwarmPairID(context.Background(), threadID, "pair-b"); err != nil {
		t.Fatalf("SetThreadSwarmPairID failed: %v", err)
	}

	_, pairID, _, _, _, _ := backend.SwarmThreadContext(threadID)
	if pairID != "pair-b" {
		t.Errorf("expected explicit pair %q, got %q", "pair-b", pairID)
	}
}

// 显式配对与当前模型不一致时回退到按模型推断。
func TestSwarmThreadContext_ExplicitPairMismatchFallsBackToInference(t *testing.T) {
	backend, threadID := newSwarmPairTestBackend(t)
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			Pairs: []config.SwarmPair{
				{ID: "pair-claude", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "claude", WorkerModel: "sonnet"},
				{ID: "pair-codex", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "codex", WorkerModel: "gpt-5-mini"},
			},
		}
	})
	setThreadLeadModel(t, backend, threadID, "claude", "opus")
	if _, err := backend.SetThreadComposerPrefs(context.Background(), threadID, ThreadComposerPrefs{
		InteractionMode: "build", BeesProvider: "codex", BeesModel: "gpt-5-mini",
	}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}
	if err := backend.SetThreadSwarmPairID(context.Background(), threadID, "pair-claude"); err != nil {
		t.Fatalf("SetThreadSwarmPairID failed: %v", err)
	}

	_, pairID, _, _, _, _ := backend.SwarmThreadContext(threadID)
	if pairID != "pair-codex" {
		t.Errorf("expected inferred pair %q after mismatch, got %q", "pair-codex", pairID)
	}
}

// 会话没选子代理模型时按 swarm 设置的默认 provider/model 推断配对。
func TestSwarmThreadContext_InferenceUsesSwarmDefaults(t *testing.T) {
	backend, threadID := newSwarmPairTestBackend(t)
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			DefaultProvider: "codex",
			DefaultModel:    "gpt-5",
			Pairs: []config.SwarmPair{
				{ID: "pair-default", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "codex", WorkerModel: "gpt-5"},
			},
		}
	})
	setThreadLeadModel(t, backend, threadID, "claude", "opus")

	_, pairID, _, _, _, _ := backend.SwarmThreadContext(threadID)
	if pairID != "pair-default" {
		t.Errorf("expected pair inferred from swarm defaults %q, got %q", "pair-default", pairID)
	}
}

// 没有任何配对与当前模型匹配时返回空串，表示会话不套用配对。
func TestSwarmThreadContext_NoPairMatchReturnsEmpty(t *testing.T) {
	backend, threadID := newSwarmPairTestBackend(t)
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			Pairs: []config.SwarmPair{
				{ID: "pair-claude", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "claude", WorkerModel: "sonnet"},
			},
		}
	})
	setThreadLeadModel(t, backend, threadID, "codex", "gpt-5")

	_, pairID, _, _, _, _ := backend.SwarmThreadContext(threadID)
	if pairID != "" {
		t.Errorf("expected empty pair ID, got %q", pairID)
	}
}
