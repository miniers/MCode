package remote

// 本文件覆盖「自发轮次」的后端接法：常驻进程在后台任务结束后自己跑一段（没有用户帧），
// 由 harness 通过 UnsolicitedTurnNotifier 回调通知，后端开一轮 Turn.Attach 把这段输出接住。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// unsolicitedTestHarness 是能主动上报自发轮次的假 harness：Run 记下每轮的 Turn 并回吐正文，
// notify 模拟常驻进程自己又跑了一段。字段都受 mu 保护。
type unsolicitedTestHarness struct {
	mu      sync.Mutex
	handler func(threadID string)
	turns   []harness.Turn
	release chan struct{}
}

func (h *unsolicitedTestHarness) Kind() harness.Kind { return "unsolicited-mock" }

func (h *unsolicitedTestHarness) SetUnsolicitedTurnHandler(fn func(threadID string)) {
	h.mu.Lock()
	h.handler = fn
	h.mu.Unlock()
}

// notify 模拟进程侧的自发输出通知。
func (h *unsolicitedTestHarness) notify(threadID string) {
	h.mu.Lock()
	handler := h.handler
	h.mu.Unlock()
	if handler != nil {
		handler(threadID)
	}
}

func (h *unsolicitedTestHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	h.mu.Lock()
	h.turns = append(h.turns, turn)
	release := h.release
	h.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	text := "用户轮回复"
	if turn.Attach {
		text = "接住的自发输出"
	}
	_ = harness.Emit(emit, harness.Event{Type: harness.EventText, Text: text})
	_ = harness.Emit(emit, harness.Event{Type: harness.EventDone, StopReason: "end_turn"})
	return nil
}

// turnsSeen 返回假 harness 收到的轮次快照。
func (h *unsolicitedTestHarness) turnsSeen() []harness.Turn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]harness.Turn(nil), h.turns...)
}

// handlerRegistered 判断后端是否登记过自发轮次回调。
func (h *unsolicitedTestHarness) handlerRegistered() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.handler != nil
}

// waitTurns 等到假 harness 收到至少 n 轮。
func waitTurns(t *testing.T, h *unsolicitedTestHarness, n int) []harness.Turn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if turns := h.turnsSeen(); len(turns) >= n {
			return turns
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待第 %d 轮开始超时，当前只有 %d 轮", n, len(h.turnsSeen()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// unsolicitedThreadMessages 取线程消息快照（steward_events_test.go 里已有同名逻辑，这里改名避免重复声明）。
func unsolicitedThreadMessages(t *testing.T, b *MemoryBackend, threadID string) []wire.ThreadMessage {
	t.Helper()
	history, err := b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory 失败: %v", err)
	}
	return history.Messages
}

// TestUnsolicitedTurn_RegistersHandler 覆盖 SetHarness：harness 支持自发轮次通知时，
// 后端要把自己的回调登记上去，否则常驻进程那边只能把输出丢掉。
func TestUnsolicitedTurn_RegistersHandler(t *testing.T) {
	h := &unsolicitedTestHarness{}
	runtimeBackend(t, h)
	if !h.handlerRegistered() {
		t.Fatal("SetHarness 应把自发轮次回调登记到 harness 上")
	}
}

// TestUnsolicitedTurn_OpensAttachTurn 覆盖自发轮次开轮：空闲线程收到通知就开一轮 Turn.Attach，
// 开轮消息 Kind 是 host_event，助手正文照常落进线程消息，轮次结束后清掉 attachTurnID。
func TestUnsolicitedTurn_OpensAttachTurn(t *testing.T) {
	h := &unsolicitedTestHarness{}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)

	h.notify(id)
	turns := waitTurns(t, h, 1)
	if !turns[0].Attach {
		t.Fatalf("自发轮次的 Turn.Attach 应为真，实际 %+v", turns[0])
	}
	waitThreadIdle(t, b, context.Background(), id)

	var opened, replied bool
	for _, msg := range unsolicitedThreadMessages(t, b, id) {
		if msg.Role == "user" && msg.Kind != nil && *msg.Kind == threadMessageKindHostEvent {
			opened = true
			if msg.Text != unsolicitedTurnText {
				t.Fatalf("开轮文本应为 %q，实际 %q", unsolicitedTurnText, msg.Text)
			}
		}
		if msg.Role == "assistant" && msg.Text == "接住的自发输出" {
			replied = true
		}
	}
	if !opened {
		t.Fatalf("自发轮次的开轮消息 Kind 应为 host_event，实际 %+v", unsolicitedThreadMessages(t, b, id))
	}
	if !replied {
		t.Fatalf("自发轮次的助手正文应落进线程消息，实际 %+v", unsolicitedThreadMessages(t, b, id))
	}

	// 轮次结束后必须清掉 attachTurnID，否则后面会一直把普通轮当成接续轮。
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		remaining := ""
		if td, ok := b.threads[id]; ok {
			remaining = td.attachTurnID
		}
		b.mu.Unlock()
		if remaining == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("轮次结束后 attachTurnID 应清空，实际仍是 %q", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestUnsolicitedTurn_IgnoredWhileThreadRunning 覆盖线程正在跑时收到通知：不开新轮，
// 免得和正在进行的轮次抢同一个进程。
func TestUnsolicitedTurn_IgnoredWhileThreadRunning(t *testing.T) {
	h := &unsolicitedTestHarness{release: make(chan struct{})}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)

	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "先跑一轮"}); err != nil {
		t.Fatalf("发送首条消息失败: %v", err)
	}
	waitTurns(t, h, 1)
	h.notify(id)
	time.Sleep(100 * time.Millisecond)
	if turns := h.turnsSeen(); len(turns) != 1 {
		t.Fatalf("线程运行中不该为自发输出再开一轮，实际 %d 轮", len(turns))
	}

	close(h.release)
	waitThreadIdle(t, b, context.Background(), id)
}

// TestUnsolicitedTurn_UnknownThreadIsNoop 覆盖线程不存在（或已被删掉）的通知：直接返回，不 panic。
func TestUnsolicitedTurn_UnknownThreadIsNoop(t *testing.T) {
	h := &unsolicitedTestHarness{}
	b := runtimeBackend(t, h)
	b.handleUnsolicitedTurn("thread-does-not-exist")
	if turns := h.turnsSeen(); len(turns) != 0 {
		t.Fatalf("线程不存在时不该开轮，实际 %d 轮", len(turns))
	}
}

// TestUnsolicitedTurn_NextUserTurnNotAttached 覆盖自发轮次之后的普通消息：Turn.Attach 必须为假，
// 否则普通轮会去接上一段输出而不发用户帧。
func TestUnsolicitedTurn_NextUserTurnNotAttached(t *testing.T) {
	h := &unsolicitedTestHarness{}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)

	h.notify(id)
	waitTurns(t, h, 1)
	waitThreadIdle(t, b, context.Background(), id)

	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "普通消息"}); err != nil {
		t.Fatalf("发送普通消息失败: %v", err)
	}
	turns := waitTurns(t, h, 2)
	if turns[1].Attach {
		t.Fatalf("普通用户消息的 Turn.Attach 应为假，实际 %+v", turns[1])
	}
	// harness 默认会在 Prompt 前拼上简洁回复引导前缀，所以只断言包含用户原文，不要求完全相等。
	if !strings.Contains(turns[1].Prompt, "普通消息") {
		t.Fatalf("普通轮的 Prompt 应包含用户原文，实际 %q", turns[1].Prompt)
	}
	if strings.Contains(turns[1].Prompt, unsolicitedTurnText) {
		t.Fatalf("普通轮的 Prompt 不该带上自发轮次的开轮文本 %q，实际 %q", unsolicitedTurnText, turns[1].Prompt)
	}
	waitThreadIdle(t, b, context.Background(), id)
}
