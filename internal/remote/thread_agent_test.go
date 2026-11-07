package remote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 1. providers.list 带模型
func TestThreadAgent_ProvidersListWithModels(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "claude"})
	backend.RegisterHarness(&mockHarness{kind: "codex"})
	backend.RegisterHarness(&mockHarness{kind: "memory"})

	ctx := context.Background()
	res, err := backend.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}

	foundClaude := false
	foundCodex := false
	foundMemory := false

	for _, p := range res.Providers {
		switch p.ID {
		case "claude":
			foundClaude = true
			if len(p.Models) == 0 {
				t.Fatalf("expected claude models not empty")
			}
			hasDefault := false
			for _, m := range p.Models {
				if m.ID == "default" && m.IsDefault {
					hasDefault = true
				}
			}
			if !hasDefault {
				t.Errorf("expected claude to have default model with ID \"default\"")
			}
		case "codex":
			foundCodex = true
			if len(p.Models) == 0 {
				t.Fatalf("expected codex models not empty")
			}
			hasDefault := false
			for _, m := range p.Models {
				if m.ID == "gpt-5.5" && m.IsDefault {
					hasDefault = true
				}
			}
			if !hasDefault {
				t.Errorf("expected codex to have default model gpt-5.5")
			}
		case "memory":
			foundMemory = true
			if len(p.Models) != 1 || p.Models[0].ID != "echo" || !p.Models[0].IsDefault {
				t.Errorf("unexpected memory models: %+v", p.Models)
			}
		}
	}

	if !foundClaude || !foundCodex || !foundMemory {
		t.Errorf("missing providers: claude=%v, codex=%v, memory=%v", foundClaude, foundCodex, foundMemory)
	}
}

// 2. create 指定模型
func TestThreadAgent_CreateWithSpecifiedModel(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "claude"})
	backend.RegisterHarness(&mockHarness{kind: "codex"})

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "p", Path: "/tmp"}
	backend.AddProject(proj)

	// 指定 model 为 sonnet
	modelSonnet := "sonnet"
	cRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Model:     &modelSonnet,
		Options: &wire.ComposerOptions{
			Provider: strPtr("claude"),
		},
	})
	if err != nil {
		t.Fatalf("CreateThread with model failed: %v", err)
	}

	th, err := backend.GetThread(ctx, cRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.Model == nil || *th.Model != "sonnet" {
		t.Fatalf("expected model to be sonnet, got %v", th.Model)
	}

	// 不指定 model 时，使用 provider 默认模型
	cResDef, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Options: &wire.ComposerOptions{
			Provider: strPtr("codex"),
		},
	})
	if err != nil {
		t.Fatalf("CreateThread with default model failed: %v", err)
	}
	thDef, err := backend.GetThread(ctx, cResDef.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thDef.Model == nil || *thDef.Model != "gpt-5.5" {
		t.Fatalf("expected codex default model gpt-5.5, got %v", thDef.Model)
	}
}

// 3. update 改模型后下一轮 Turn.Model 变化
func TestThreadAgent_UpdateModelReflectedInNextTurn(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	h := &mockHarness{
		kind: "claude",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{
				Type: harness.EventText,
				Text: "reply",
			})
			emit(harness.Event{
				Type: harness.EventDone,
			})
			return nil
		},
	}
	backend.RegisterHarness(h)

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "p", Path: "/tmp"}
	backend.AddProject(proj)

	cRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Options: &wire.ComposerOptions{
			Provider: strPtr("claude"),
		},
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := cRes.ThreadID

	// 第一轮发送消息，Turn.Model 应当为 claude 默认值 "default"
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "message 1",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage 1 failed: %v", err)
	}
	waitForTurnDone(t, backend, threadID)

	calls := h.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 turn call, got %d", len(calls))
	}
	if calls[0].Model != "default" {
		t.Fatalf("expected turn 0 Model to be \"default\", got %q", calls[0].Model)
	}

	// 更新模型为 opus
	modelOpus := "opus"
	uRes, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Model:    &modelOpus,
	})
	if err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}
	if uRes.Thread.Model == nil || *uRes.Thread.Model != "opus" {
		t.Fatalf("expected thread model updated to opus, got %v", uRes.Thread.Model)
	}

	// 第二轮发送消息，Turn.Model 应当更新为 "opus"
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "message 2",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage 2 failed: %v", err)
	}
	waitForTurnDone(t, backend, threadID)

	calls = h.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 turn calls, got %d", len(calls))
	}
	if calls[1].Model != "opus" {
		t.Fatalf("expected turn 1 Model to be opus, got %q", calls[1].Model)
	}
}

// 4. 空闲时允许更换 provider，下一轮携带历史交接。
func TestThreadAgent_ChangeProviderAfterTurns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{
		kind: "claude",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "reply"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	})
	backend.RegisterHarness(&mockHarness{kind: "codex"})

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "p", Path: "/tmp"}
	backend.AddProject(proj)

	cRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Options:   &wire.ComposerOptions{Provider: strPtr("claude")},
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := cRes.ThreadID

	// 无轮次时修改 provider 应该成功
	provCodex := "codex"
	uRes, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Provider: &provCodex,
	})
	if err != nil {
		t.Fatalf("UpdateThread before turns failed: %v", err)
	}
	if uRes.Thread.Provider != "codex" {
		t.Fatalf("expected provider to be codex, got %s", uRes.Thread.Provider)
	}

	// 发生第一轮对话
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "hello",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitForTurnDone(t, backend, threadID)

	// 已有历史但处于空闲态时允许切换，不能复用旧 Provider 的 session。
	provClaude := "claude"
	_, err = backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Provider: &provClaude,
	})
	if err != nil {
		t.Fatalf("UpdateThread after turns: %v", err)
	}
	backend.mu.Lock()
	td := backend.threads[threadID]
	session, handoff := td.sessionID, td.needsHandoff
	backend.mu.Unlock()
	if session != "" || !handoff {
		t.Fatalf("provider switch must reset session and prepare history handoff")
	}
}

// 5. 未知模型被拒
func TestThreadAgent_UnknownModelRejected(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "claude"})

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "p", Path: "/tmp"}
	backend.AddProject(proj)

	// Create 时传入不存在的模型
	badModel := "gpt-999-super"
	_, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Model:     &badModel,
		Options:   &wire.ComposerOptions{Provider: strPtr("claude")},
	})
	if err == nil {
		t.Fatalf("expected CreateThread with unknown model to fail")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams error on create, got: %v", err)
	}

	// 正常创建
	cRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Options:   &wire.ComposerOptions{Provider: strPtr("claude")},
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// Update 时传入不存在的模型
	_, err = backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: cRes.ThreadID,
		Model:    &badModel,
	})
	if err == nil {
		t.Fatalf("expected UpdateThread with unknown model to fail")
	}
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams error on update, got: %v", err)
	}
}

// 6. 重载 store 后保留
func TestThreadAgent_PersistAndReloadModelAndProvider(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode_agent.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store 1: %v", err)
	}

	backend1.RegisterHarness(&mockHarness{kind: "codex"})
	backend1.RegisterHarness(&mockHarness{kind: "claude"})

	proj := wire.ProjectSummary{
		ID:   "proj-agent-1",
		Name: "Agent Test Proj",
		Path: "/test/agent",
	}
	backend1.AddProject(proj)

	// 创建带指定模型的 codex 线程
	modelVal := "gpt-5.4"
	cRes, err := backend1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Model:     &modelVal,
		Options:   &wire.ComposerOptions{Provider: strPtr("codex")},
	})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	threadID := cRes.ThreadID

	// 关闭第一套实例
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	// 重新打开数据库并由 backend2 恢复
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	th, err := backend2.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("get thread on reload: %v", err)
	}

	if th.Provider != "codex" {
		t.Errorf("expected provider codex, got %s", th.Provider)
	}
	if th.Model == nil || *th.Model != "gpt-5.4" {
		t.Errorf("expected model gpt-5.4, got %v", th.Model)
	}
}
