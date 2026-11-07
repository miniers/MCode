package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// runRetractTurn 用给定事件序列跑一轮，返回该轮落定后的 assistant 正文消息。
func runRetractTurn(t *testing.T, threadID string, events []harness.Event) []wire.ThreadMessage {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.SetHarness(&mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			for _, e := range events {
				emit(e)
			}
			return nil
		},
	}, t.TempDir(), harness.ModeAgent)

	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "流中断撤回"}, nil)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "开始",
		ActionID: threadID[:len(threadID)-1] + "f",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var out []wire.ThreadMessage
	for _, m := range hist.Messages {
		if m.Role == "assistant" && m.Kind != nil && *m.Kind == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

// 撤回的残文只占当前正文消息的尾部：裁掉残文（写入时做过破折号清洗，撤回时同样清洗），
// 重写的正文接在已确认部分之后，整轮仍是一条消息。
func TestStreamRetract_TrimsTailOfCurrentMessage(t *testing.T) {
	msgs := runRetractTurn(t, "00000000-0000-0000-0000-0000000000d1", []harness.Event{
		{Type: harness.EventText, Text: "已确认。"},
		{Type: harness.EventText, Text: "残文—写到一半「"},
		{Type: harness.EventTextRetract, Text: "残文—写到一半「"},
		{Type: harness.EventText, Text: "重写的结论"},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 assistant message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Text != "已确认。重写的结论" {
		t.Errorf("text = %q, want %q", msgs[0].Text, "已确认。重写的结论")
	}
}

// 整条正文消息都是残文时移除该消息（新建时去掉的前导换行不影响判定），重写的正文另起一条。
func TestStreamRetract_RemovesWholeMessage(t *testing.T) {
	msgs := runRetractTurn(t, "00000000-0000-0000-0000-0000000000d3", []harness.Event{
		{Type: harness.EventText, Text: "\n# 第一版，写到一半「"},
		{Type: harness.EventTextRetract, Text: "\n# 第一版，写到一半「"},
		{Type: harness.EventText, Text: "# 重写版"},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 assistant message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Text != "# 重写版" {
		t.Errorf("text = %q, want %q", msgs[0].Text, "# 重写版")
	}
	if msgs[0].IsFinalResponse == nil || !*msgs[0].IsFinalResponse {
		t.Errorf("rewritten message should be the final response")
	}
}

// 撤回内容对不上当前消息尾部时保持原样，不误删正文。
func TestStreamRetract_MismatchKeepsText(t *testing.T) {
	msgs := runRetractTurn(t, "00000000-0000-0000-0000-0000000000d5", []harness.Event{
		{Type: harness.EventText, Text: "完整正文"},
		{Type: harness.EventTextRetract, Text: "别的内容"},
	})
	if len(msgs) != 1 || msgs[0].Text != "完整正文" {
		t.Fatalf("messages = %+v, want untouched text", msgs)
	}
}
