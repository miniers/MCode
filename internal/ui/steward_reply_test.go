package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestRenderStewardReplyPlainLine 验证 steward_reply 消息只渲染一行淡色文本，
// 不落入普通助手消息的头部与操作栏。
func TestRenderStewardReplyPlainLine(t *testing.T) {
	const text = "我会盯着这件事，有结果再告诉你"
	s := &chatState{}
	msg := Message{ID: "sr1", Role: "assistant", Kind: stewardReplyKind, Text: text}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("steward-reply-test-col")).Width(timelineContentMaxWidth(s)).Children(func() {
			renderMessageItem(c, s, msg, "", "", false, false, TokenUsage{}, 0, false)
		})
	}, 800, 200)

	if !tt.HasText(text) {
		t.Fatalf("steward_reply 正文未渲染，当前文本：%v", tt.Texts())
	}
	if _, ok := tt.Find("steward-reply-sr1"); !ok {
		t.Fatalf("未找到 steward_reply 行，当前文本：%v", tt.Texts())
	}
	if _, ok := tt.Find("msg-meta-sr1"); ok {
		t.Fatal("steward_reply 不应出现助手头部元信息")
	}
	if _, ok := tt.Find("msg-actions-sr1"); ok {
		t.Fatal("steward_reply 不应出现消息操作栏")
	}
}
