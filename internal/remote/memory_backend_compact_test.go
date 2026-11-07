package remote

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
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
	t.Setenv("HOME", t.TempDir())
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
