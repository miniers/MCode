package remote

import (
	"context"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func TestWithoutEmDashes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"无目标字符时原样返回", "普通文本 with plain hyphen - and en dash – here", "普通文本 with plain hyphen - and en dash – here"},
		{"em dash 保留空格替换", "works — every time", "works - every time"},
		{"em dash 无空格替换", "ships—sometimes", "ships-sometimes"},
		{"horizontal bar 替换", "a―b", "a-b"},
		{"two-em dash 替换", "a⸺b", "a-b"},
		{"three-em dash 替换", "a⸻b", "a-b"},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		if got := withoutEmDashes(tc.in); got != tc.want {
			t.Errorf("%s: withoutEmDashes(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestIsBlankDelta(t *testing.T) {
	for _, s := range []string{"", " ", "\n", "\r\n", " \t\r\n "} {
		if !isBlankDelta(s) {
			t.Errorf("isBlankDelta(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"你", "\n你好", "  x  ", "-", "。"} {
		if isBlankDelta(s) {
			t.Errorf("isBlankDelta(%q) = true, want false", s)
		}
	}
}

// C-RT-21：新建消息前的前导空增量（空格、换行）被跳过，首个非空增量去掉前导换行后建消息，
// 整轮只生成一条 assistant 消息。
func TestStreamTypography_SkipLeadingBlankDeltas(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: " "})
			emit(harness.Event{Type: harness.EventText, Text: "\n"})
			emit(harness.Event{Type: harness.EventText, Text: "\n你好-世界"})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000c1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "流式清洗"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "开始",
		ActionID: "00000000-0000-0000-0000-0000000000c2",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var assistant []wire.ThreadMessage
	for _, m := range hist.Messages {
		if m.Role == "assistant" {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("expected exactly one assistant message, got %d: %+v", len(assistant), hist.Messages)
	}
	if assistant[0].Text != "你好-世界" {
		t.Errorf("assistant text = %q, want %q", assistant[0].Text, "你好-世界")
	}
}

// C-RT-20：正文增量里的 em dash 在写入消息与透传流式钩子之前都被替换为普通连字符。
func TestStreamTypography_EmDashCleanedInStream(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "你好—世界"})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	var mu sync.Mutex
	var deltas []string
	backend.SetAssistantStreamHook(func(threadID, projectID string, reset bool, delta string) {
		if reset {
			return
		}
		mu.Lock()
		deltas = append(deltas, delta)
		mu.Unlock()
	})

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000c3"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "破折号清洗"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "开始",
		ActionID: "00000000-0000-0000-0000-0000000000c4",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var assistant []wire.ThreadMessage
	for _, m := range hist.Messages {
		if m.Role == "assistant" {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("expected exactly one assistant message, got %d: %+v", len(assistant), hist.Messages)
	}
	if assistant[0].Text != "你好-世界" {
		t.Errorf("assistant text = %q, want %q", assistant[0].Text, "你好-世界")
	}

	mu.Lock()
	gotDeltas := append([]string(nil), deltas...)
	mu.Unlock()
	if len(gotDeltas) != 1 || gotDeltas[0] != "你好-世界" {
		t.Errorf("stream deltas = %q, want [%q]", gotDeltas, "你好-世界")
	}
}
