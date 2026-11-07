package remote

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// personaTestRig 组装带假 harness 与真实 store 的后端，用于人设注入与持久化测试。
type personaTestRig struct {
	backend *MemoryBackend
	mock    *mockHarness
	store   *store.Store
}

func newPersonaTestRig(t *testing.T) *personaTestRig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("store.Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	if err := b.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "reply"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	b.SetHarness(mock, t.TempDir(), harness.ModeAgent)
	// 固定首轮指导词，使各会话首轮 Prompt 可逐字比较。
	b.SetGuidanceBuilder(func(string) string { return "GUIDANCE" })
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})
	return &personaTestRig{backend: b, mock: mock, store: st}
}

// newThread 建一个绑定到固定 project 的会话，model 用于断言 resolver 入参。
func (r *personaTestRig) newThread(t *testing.T, model string) string {
	t.Helper()
	res, err := r.backend.CreateThread(context.Background(), wire.ThreadCreateParams{ProjectID: "p1", Model: &model})
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return res.ThreadID
}

// sendTurn 发一条普通消息并返回本轮假 harness 收到的 Turn。
func (r *personaTestRig) sendTurn(t *testing.T, threadID, text string) harness.Turn {
	t.Helper()
	ctx := context.Background()
	if _, err := r.backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: text}); err != nil {
		t.Fatalf("SendMessage 失败: %v", err)
	}
	waitThreadIdle(t, r.backend, ctx, threadID)
	calls := r.mock.getTurnCalls()
	if len(calls) == 0 {
		t.Fatal("假 harness 未收到任何 Turn")
	}
	return calls[len(calls)-1]
}

// setPersona 走 composer 偏好链路写入人设。
func (r *personaTestRig) setPersona(t *testing.T, threadID, persona string) {
	t.Helper()
	if _, err := r.backend.SetThreadComposerPrefs(context.Background(), threadID, ThreadComposerPrefs{InteractionMode: "build", Persona: persona}); err != nil {
		t.Fatalf("SetThreadComposerPrefs 失败: %v", err)
	}
}

// TestPersonaTagPrefixAppliedToTurnPrompt 验证人设前缀拼在发给 harness 的 Prompt 最前面，
// 原始提示词完整保留在其后，且时间线里的用户消息不含前缀。
func TestPersonaTagPrefixAppliedToTurnPrompt(t *testing.T) {
	rig := newPersonaTestRig(t)
	const wantPrefix = "[[persona:reviewer]]\n\n"
	var gotProvider, gotModel, gotPersona string
	rig.backend.SetPersonaTagResolver(func(provider, model, persona string) string {
		gotProvider, gotModel, gotPersona = provider, model, persona
		if persona == "" || persona == "none" {
			return ""
		}
		return "[[persona:" + persona + "]]\n\n"
	})

	threadID := rig.newThread(t, "sonnet")
	rig.setPersona(t, threadID, "reviewer")

	th, err := rig.backend.GetThread(context.Background(), threadID)
	if err != nil || th.Persona != "reviewer" {
		t.Fatalf("人设未写入会话摘要: %+v, %v", th, err)
	}

	turn := rig.sendTurn(t, threadID, "原始提示词")
	if !strings.HasPrefix(turn.Prompt, wantPrefix) {
		t.Fatalf("Turn.Prompt 未以人设前缀开头: %q", turn.Prompt)
	}
	if !strings.HasSuffix(turn.Prompt, "原始提示词") || strings.Count(turn.Prompt, "[[persona:") != 1 {
		t.Fatalf("原始提示词未完整保留在前缀之后或被重复注入: %q", turn.Prompt)
	}
	if gotProvider != "mock" || gotModel != "sonnet" || gotPersona != "reviewer" {
		t.Fatalf("resolver 入参不符: provider=%q model=%q persona=%q", gotProvider, gotModel, gotPersona)
	}

	msgs, _, err := rig.store.ListMessages(context.Background(), threadID, 0, 100)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	foundUser := false
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		foundUser = true
		if m.Text != "原始提示词" {
			t.Fatalf("时间线里的用户消息被前缀污染: %q", m.Text)
		}
	}
	if !foundUser {
		t.Fatal("未找到用户消息")
	}
}

// TestPersonaTagNotApplied 验证未设置 resolver、resolver 返回空串、人设为 none 三种情况下
// Turn.Prompt 与未设置人设的对照轮完全一致。
func TestPersonaTagNotApplied(t *testing.T) {
	rig := newPersonaTestRig(t)
	const text = "原始提示词"

	// 对照组：未设置人设的会话，作为“改动前”的基准 Prompt。
	control := rig.sendTurn(t, rig.newThread(t, "sonnet"), text)
	if strings.Contains(control.Prompt, "[[persona:") || !strings.HasSuffix(control.Prompt, text) {
		t.Fatalf("对照轮不应带人设前缀: %q", control.Prompt)
	}

	t.Run("未设置 resolver", func(t *testing.T) {
		threadID := rig.newThread(t, "sonnet")
		rig.setPersona(t, threadID, "reviewer")
		if turn := rig.sendTurn(t, threadID, text); turn.Prompt != control.Prompt {
			t.Fatalf("未设置 resolver 时 Prompt 必须与改动前一致: want %q, got %q", control.Prompt, turn.Prompt)
		}
	})

	t.Run("resolver 返回空串", func(t *testing.T) {
		rig.backend.SetPersonaTagResolver(func(provider, model, persona string) string { return "" })
		threadID := rig.newThread(t, "sonnet")
		rig.setPersona(t, threadID, "reviewer")
		if turn := rig.sendTurn(t, threadID, text); turn.Prompt != control.Prompt {
			t.Fatalf("resolver 返回空串时 Prompt 必须与改动前一致: want %q, got %q", control.Prompt, turn.Prompt)
		}
	})

	t.Run("人设 none", func(t *testing.T) {
		var gotPersona string
		rig.backend.SetPersonaTagResolver(func(provider, model, persona string) string {
			gotPersona = persona
			if persona == "none" {
				return ""
			}
			return "[[persona:" + persona + "]]\n\n"
		})
		threadID := rig.newThread(t, "sonnet")
		rig.setPersona(t, threadID, "none")
		if turn := rig.sendTurn(t, threadID, text); turn.Prompt != control.Prompt {
			t.Fatalf("人设为 none 时不应注入前缀: want %q, got %q", control.Prompt, turn.Prompt)
		}
		if gotPersona != "none" {
			t.Fatalf("resolver 应收到 none，实际为 %q", gotPersona)
		}
	})
}

// TestSetThreadComposerPrefsPersonaValidation 验证非法人设被拒、合法值与空串可持久化。
func TestSetThreadComposerPrefsPersonaValidation(t *testing.T) {
	rig := newPersonaTestRig(t)
	ctx := context.Background()
	threadID := rig.newThread(t, "sonnet")

	for _, bad := range []string{"bad id", "bad/id", "<script>", strings.Repeat("a", 65)} {
		_, err := rig.backend.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{InteractionMode: "build", Persona: bad})
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
			t.Fatalf("非法人设 %q 期望 InvalidParams，实际: %v", bad, err)
		}
	}
	th, err := rig.backend.GetThread(ctx, threadID)
	if err != nil || th.Persona != "" {
		t.Fatalf("非法人设不得写入摘要: %+v, %v", th, err)
	}

	// 合法 id、字面量 none、空串（从未设置）都能落盘并读回。
	for _, good := range []string{"reviewer", "none", ""} {
		if _, err := rig.backend.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{InteractionMode: "build", Persona: good}); err != nil {
			t.Fatalf("合法人设 %q 被拒: %v", good, err)
		}
		th, err := rig.backend.GetThread(ctx, threadID)
		if err != nil || th.Persona != good {
			t.Fatalf("人设 %q 读回不一致: %+v, %v", good, th, err)
		}
		stored, err := rig.store.GetThread(ctx, threadID)
		if err != nil || stored.Persona != good {
			t.Fatalf("人设 %q 未持久化: %+v, %v", good, stored, err)
		}
	}
}
