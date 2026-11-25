package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/testenv"
)

// 原生 /compact 一律原样送达，其它消息保留「人设前缀 + 装饰正文」的既有拼法。
func TestHarnessTurnPrompt(t *testing.T) {
	if got := harnessTurnPrompt(true, "[persona]", "装饰后的 /compact"); got != compactCommandText {
		t.Fatalf("compact 轮次应原样送达命令, got %q", got)
	}
	if got, want := harnessTurnPrompt(false, "[persona]", "正文"), "[persona]正文"; got != want {
		t.Fatalf("普通轮次应拼人设前缀, got %q, want %q", got, want)
	}
}

// 斜杠命令轮次不能夹带人设前缀、decorator 与简洁回复引导，否则 Provider 认不出 /compact。
func TestMemoryBackend_CompactCommandSkipsDecorations(t *testing.T) {
	testenv.TempHome(t)
	project := t.TempDir()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "好的"})
		return nil
	}}
	backend.SetHarness(mock, project, harness.ModeAgent)
	backend.SetGuidanceBuilder(func(string) string { return "" })
	backend.SetPersonaTagResolver(func(provider, model, persona string) string {
		if persona == "" {
			return ""
		}
		return "[persona]"
	})
	backend.SetPromptDecorator(func(threadID, prompt string) string { return "[decorated]" + prompt })
	concise := true
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &concise}}
	})

	// 线程带人设，普通消息才会真的拼前缀，从而验证 compact 轮次确实跳过了它。
	threadID := "00000000-0000-0000-0000-0000000000e3"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Compact Thread", Persona: "reviewer"}, nil)

	sendGuidanceMessage(t, backend, threadID, "  /compact  ", "00000000-0000-0000-0000-000000000311")
	sendGuidanceMessage(t, backend, threadID, "普通消息", "00000000-0000-0000-0000-000000000312")

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}
	if got := calls[0].Prompt; got != compactCommandText {
		t.Fatalf("compact 轮次的 Prompt 应就是命令原文（首尾空白也算装饰）, got %q", got)
	}
	ordinary := calls[1].Prompt
	if !strings.HasPrefix(ordinary, "[persona]"+ConciseRepliesPrompt) {
		t.Fatalf("普通轮次仍应带人设前缀与简洁回复引导, got %q", ordinary)
	}
	if !strings.Contains(ordinary, "[decorated]普通消息") {
		t.Fatalf("普通轮次仍应经过 decorator, got %q", ordinary)
	}
}

// TestContextCompactedText 校验压缩边界行的三种文案：两个数字都有、只有压缩前、都没有。
func TestContextCompactedText(t *testing.T) {
	tests := []struct {
		name         string
		tokensBefore int64
		tokensAfter  int64
		want         string
	}{
		{name: "压缩前后都有", tokensBefore: 120000, tokensAfter: 12000, want: "已压缩上下文 · 120k → 12k"},
		{name: "只有压缩前", tokensBefore: 150000, tokensAfter: 0, want: "已压缩上下文 · 压缩前 150k"},
		{name: "都没有", tokensBefore: 0, tokensAfter: 0, want: "已压缩上下文"},
		{name: "四舍五入到 k", tokensBefore: 180700, tokensAfter: 1550, want: "已压缩上下文 · 181k → 2k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextCompactedText(tt.tokensBefore, tt.tokensAfter); got != tt.want {
				t.Fatalf("contextCompactedText(%d, %d) = %q, want %q", tt.tokensBefore, tt.tokensAfter, got, tt.want)
			}
		})
	}
}

// TestCompactedEventUpdatesContextTokens 验证 EventContextCompacted 携带 CompactTokensAfter > 0 时，
// 立即把当前轮次的 ContextTokens 更新为该值（未记录该轮用量时从最大已有轮次拷贝 ContextWindow），
// 落库并重新发布摘要；之后的 EventUsage 仍整条覆盖。
func TestCompactedEventUpdatesContextTokens(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	project := t.TempDir()

	st, err := store.Open(filepath.Join(t.TempDir(), "compact.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	compactedObserved := make(chan struct{})
	releaseUsage := make(chan struct{})
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{
			Type:                harness.EventContextCompacted,
			CompactTokensBefore: 160_000,
			CompactTokensAfter:  18_000,
		})
		close(compactedObserved)
		<-releaseUsage
		emit(harness.Event{
			Type: harness.EventUsage,
			Usage: &harness.Usage{
				InputTokens:   20_000,
				OutputTokens:  500,
				ContextTokens: 20_500,
				ContextWindow: 200_000,
			},
		})
		return nil
	}}
	backend.SetHarness(mock, project, harness.ModeAgent)

	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "proj", Path: project})
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	threadID := created.ThreadID

	// 预置历史轮次用量（turn 0 窗口 128k，turn 1 窗口 200k）并把 nextTurnIndex 设为 2，
	// 使本次 SendMessage 以尚无用量记录的 turnIndex=2 执行压缩。
	backend.mu.Lock()
	backend.threads[threadID].nextTurnIndex = 2
	backend.usage = map[string]map[int]harness.Usage{
		threadID: {
			0: {InputTokens: 10_000, ContextTokens: 10_000, ContextWindow: 128_000},
			1: {InputTokens: 160_000, ContextTokens: 160_000, ContextWindow: 200_000},
		},
	}
	backend.mu.Unlock()

	publishedCh := make(chan wire.ThreadSummary, 16)
	stopObs := hub.AddObserver(func(id, method string, params any) {
		if id != threadID || method != "thread.summary" {
			return
		}
		if sum, ok := params.(wire.ThreadSummary); ok {
			publishedCh <- sum
		}
	})
	t.Cleanup(stopObs)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "继续",
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	<-compactedObserved
	// 等推送批次至少包含 ContextUsedTokens == 18_000 的摘要。
	deadline := time.Now().Add(2 * time.Second)
	var gotSummary wire.ThreadSummary
	for time.Now().Before(deadline) {
		select {
		case s := <-publishedCh:
			if s.ContextUsedTokens == 18_000 {
				gotSummary = s
			}
		default:
			time.Sleep(5 * time.Millisecond)
		}
		if gotSummary.ContextUsedTokens == 18_000 {
			break
		}
	}
	if gotSummary.ContextUsedTokens != 18_000 || gotSummary.ContextWindow != 200_000 {
		t.Fatalf("压缩后推送摘要应立即降为 18000/200000，实际 used=%d window=%d",
			gotSummary.ContextUsedTokens, gotSummary.ContextWindow)
	}

	// 验证内存用量与 store 落库已同步更新为 turn 2，且 ContextWindow 从最大历史轮次（turn 1）拷贝为 200_000。
	turns, _, err := backend.ThreadUsage(ctx, threadID)
	if err != nil {
		t.Fatalf("ThreadUsage: %v", err)
	}
	if got := turns[2]; got.ContextTokens != 18_000 || got.ContextWindow != 200_000 {
		t.Fatalf("turn 2 内存用量应为 ContextTokens=18000 ContextWindow=200000，实际 %+v", got)
	}
	dbTurns, err := st.ListTurnUsage(ctx, threadID)
	if err != nil {
		t.Fatalf("ListTurnUsage: %v", err)
	}
	var foundPersisted bool
	for _, row := range dbTurns {
		if row.TurnIndex == 2 && row.ContextTokens == 18_000 && row.ContextWindow == 200_000 {
			foundPersisted = true
			break
		}
	}
	if !foundPersisted {
		t.Fatalf("压缩后 turn 2（18000/200000）应已落库，实际 %+v", dbTurns)
	}

	// 放行后续 EventUsage：应按原逻辑整条覆盖。
	close(releaseUsage)
	waitThreadIdle(t, backend, ctx, threadID)

	afterSummary, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if afterSummary.ContextUsedTokens != 20_500 || afterSummary.ContextWindow != 200_000 {
		t.Fatalf("后续 EventUsage 应覆盖为 20500/200000，实际 used=%d window=%d",
			afterSummary.ContextUsedTokens, afterSummary.ContextWindow)
	}
}
