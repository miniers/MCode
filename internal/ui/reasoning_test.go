package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLiveReasoningClickDoesNotChangeState(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "live-reasoning", Role: "assistant", Kind: "reasoning", Text: "正在推理的正文"}
	tt := ui.NewTester(func(c *ui.Context) {
		renderReasoningMessage(c, s, m, m.ID, false, false, TokenUsage{}, 0)
	}, 800, 600)
	clickText(t, tt, "收起思考过程")
	s.mu.Lock()
	open := s.expandedReasoning[m.ID]
	s.mu.Unlock()
	if open {
		t.Fatal("流式思考点击不应改变展开状态")
	}
	if !tt.HasText("正在推理的正文") {
		t.Fatal("流式思考正文应继续显示")
	}
}
