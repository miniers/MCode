package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// newBeeRevealState 构造一个子代理会话状态：带一条助手正文，并按“打开会话”的方式记好揭示基线。
func newBeeRevealState(text string) *chatState {
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.selectedThreadID = "h-birch"
	s.messages = []Message{{ID: "a1", Role: "assistant", Kind: "text", Text: text, TurnIndex: 1}}
	s.markRevealBaselineLocked(s.messages)
	s.mu.Unlock()
	return s
}

// growBeeMessage 模拟子代理又写了一段：正文变长，时间线缓存作废。
func growBeeMessage(s *chatState, text string) {
	s.mu.Lock()
	s.messages[0].Text = text
	s.messagesRev++
	s.mu.Unlock()
}

// TestBeeTimelineRevealNotReplayedAfterViewRecreated 验证浮窗视图重建后，
// 已经逐字显示过的正文不会从头再播一遍打字机动画。
func TestBeeTimelineRevealNotReplayedAfterViewRecreated(t *testing.T) {
	base := "子代理已经写好的一段正文" + strings.Repeat("，继续写下去", 20)
	grown := base + "，又写了一段"
	s := newBeeRevealState(base)
	bee := SwarmBee{ThreadID: "h-birch", Name: "Birch", Status: "running"}

	// visible 模拟浮窗的显示与否：关掉再打开时视图会被销毁重建。
	visible := true
	tt := ui.NewTester(func(c *ui.Context) {
		if !visible {
			return
		}
		renderBeeTimeline(c, s, bee)
	}, 900, 700)

	// 视图 A：正文逐字显示完。
	tt.Advance(3 * time.Second)
	if !tt.HasText(base) {
		t.Fatalf("逐字显示完成后应能看到完整正文，实际看到：%v", tt.Texts())
	}

	// 子代理又写了一段：会话状态进入“流式中”，视图里的正文继续逐字揭示。
	growBeeMessage(s, grown)
	tt.Frame()
	tt.Frame()
	tt.Advance(3 * time.Second)
	if !tt.HasText(grown) {
		t.Fatalf("增量写完后应能看到完整正文，实际看到：%v", tt.Texts())
	}

	// 关掉浮窗：视图被销毁，Markdown 的揭示进度随之丢失。
	visible = false
	tt.Frame()

	// 重新打开浮窗：视图重建，已显示过的正文应立即完整出现，不再重播打字机动画。
	visible = true
	tt.Frame()
	if !tt.HasText(grown) {
		t.Fatalf("视图重建后正文应立即完整显示，不应重播打字机动画，实际看到：%v", tt.Texts())
	}
}

// TestBeeTimelineReasoningNotReplayedAfterViewRecreated 验证浮窗视图重建后，
// 已经显示过的思考正文也不会重播打字机动画。
func TestBeeTimelineReasoningNotReplayedAfterViewRecreated(t *testing.T) {
	text := "子代理正在想一件事" + strings.Repeat("，还在想", 20)
	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.selectedThreadID = "h-birch"
	s.messages = []Message{{ID: "r-bee-reveal", Role: "assistant", Kind: "reasoning", Text: text, TurnIndex: 1}}
	s.markRevealBaselineLocked(s.messages)
	s.mu.Unlock()
	// 思考行的 live 记账是全局的，测试结束后清掉，免得影响别的用例。
	t.Cleanup(func() {
		reasoningWasLiveMu.Lock()
		delete(reasoningWasLive, "r-bee-reveal")
		reasoningWasLiveMu.Unlock()
	})
	bee := SwarmBee{ThreadID: "h-birch", Name: "Birch", Status: "running"}

	visible := true
	tt := ui.NewTester(func(c *ui.Context) {
		if !visible {
			return
		}
		renderBeeTimeline(c, s, bee)
	}, 900, 700)
	tt.Advance(3 * time.Second)
	if !tt.HasText(text) {
		t.Fatalf("逐字显示完成后应能看到完整思考正文，实际看到：%v", tt.Texts())
	}

	// 子代理又写了一段：会话状态进入“流式中”。
	growBeeMessage(s, text+"，又想了一段")
	tt.Frame()
	tt.Frame()
	tt.Advance(3 * time.Second)

	// 关掉浮窗再打开：视图重建，思考正文应立即完整出现。
	visible = false
	tt.Frame()
	visible = true
	tt.Frame()
	if !tt.HasText(text) {
		t.Fatalf("视图重建后思考正文应立即完整显示，不应重播打字机动画，实际看到：%v", tt.Texts())
	}
}

// TestBeeTimelineRevealStillAnimatesNewText 验证新到达的正文仍然逐字揭示。
func TestBeeTimelineRevealStillAnimatesNewText(t *testing.T) {
	base := "子代理已经写好的一段正文" + strings.Repeat("，继续写下去", 20)
	fresh := "子代理又写出来的一段新正文" + strings.Repeat("，还在写", 20)
	s := newBeeRevealState(base)
	bee := SwarmBee{ThreadID: "h-birch", Name: "Birch", Status: "running"}

	tt := ui.NewTester(func(c *ui.Context) {
		renderBeeTimeline(c, s, bee)
	}, 900, 700)
	tt.Advance(3 * time.Second)
	if !tt.HasText(base) {
		t.Fatalf("逐字显示完成后应能看到完整正文，实际看到：%v", tt.Texts())
	}

	// 新消息到达：逐字揭示，不应整段蹦出。
	s.mu.Lock()
	s.messages = append(s.messages, Message{ID: "a2", Role: "assistant", Kind: "text", Text: fresh, TurnIndex: 2})
	s.messagesRev++
	s.mu.Unlock()
	tt.Frame()
	if tt.HasText(fresh) {
		t.Fatalf("新到达的正文应逐字揭示，不应整段蹦出，实际看到：%v", tt.Texts())
	}
	tt.Advance(3 * time.Second)
	if !tt.HasText(fresh) {
		t.Fatalf("逐字揭示完成后应能看到完整正文，实际看到：%v", tt.Texts())
	}
}
