package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestRenderStewardReplyPlainLine 验证回复正文不带助手头部与操作栏。
func TestRenderStewardReplyPlainLine(t *testing.T) {
	const text = "我会盯着这件事，有结果再告诉你"
	s := newChatState(NewFakeClient())
	msg := Message{ID: "sr1", Role: "assistant", Kind: stewardReplyKind, Text: text, IsFinal: true}
	s.selectedThreadID = "thread"
	s.threads = []Thread{{ID: "thread", Provider: "claude", UpdatedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local)}}
	s.messages = []Message{msg}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("steward-reply-test-col")).Width(timelineContentMaxWidth(s)).Children(func() {
			// 容器的 key 只是身份，不是 Tester.Find 的查找标签；给整行补一个测试用 label。
			row := renderMessageItem(c, s, msg, "", "", true, false, TokenUsage{}, 0, false)
			row.Label("steward-reply-" + msg.ID)
		})
	}, 800, 200)

	if !tt.HasText(text) {
		t.Fatalf("steward_reply 正文未渲染，当前文本：%v", tt.Texts())
	}
	if _, ok := tt.Find("steward-reply-sr1"); !ok {
		t.Fatalf("未找到 steward_reply 行，当前文本：%v", tt.Texts())
	}
	// 上面按普通助手路径传入 IsFinal 与 isLastAssistant，若落错分支就会渲染头部时间与操作栏。
	if tt.HasText("03:04") {
		t.Fatal("steward_reply 不应出现助手头部元信息")
	}
	if _, ok := tt.Find("复制"); ok {
		t.Fatal("steward_reply 不应出现消息操作栏")
	}
}

func TestStewardReplyWrapsInNarrowWindow(t *testing.T) {
	const text = "这是一段较长的回复，用于确认窄窗口中正文能够正常换行，并且最后的内容仍然完整显示。https://example.com/result"
	s := newChatState(NewFakeClient())
	msg := Message{ID: "long", Role: "assistant", Kind: stewardReplyKind, Text: text}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("narrow")).Width(180).Children(func() {
			renderStewardReply(c, s, msg).Label("long-reply")
		})
	}, 180, 400)
	if !tt.HasText(text) {
		t.Fatalf("长回复正文不完整: %q", tt.Texts())
	}
	row, ok := tt.Find("long-reply")
	if !ok || row.H <= scaledFont(TypeBody)*2 {
		t.Fatalf("窄窗口长回复没有换行: %+v", row)
	}
}
