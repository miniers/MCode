package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestChatListAppendDoesNotSlideRows 验证主聊天列表禁用 MoveAnimation：
// 正常动效下向溢出视口并贴底跟随的列表追加用户消息后，MoveAnimation 必须为 nil，
// 列表不再为行位移动画请求后续帧，且原有可见行直接位于最终位置。
func TestChatListAppendDoesNotSlideRows(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	setAppReduceMotion(t, false)

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}

	var msgs []Message
	for i := 1; i <= 20; i++ {
		msgs = append(msgs,
			Message{ID: fmt.Sprintf("u%d", i), Role: "user", Text: fmt.Sprintf("问题-%02d", i), TurnIndex: i, IsFinal: true},
			Message{ID: fmt.Sprintf("a%d", i), Role: "assistant", Text: fmt.Sprintf("回答-%02d", i), TurnIndex: i, IsFinal: true},
		)
	}
	s.mu.Lock()
	s.messages = msgs
	s.messagesRev = 1
	s.markRevealBaselineLocked(msgs)
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderChat(c, s) }, 900, 700)
	waitFor(t, tt, func() bool { return s.chatListState.AtEnd() && tt.HasText("回答-20") })
	tt.Advance(3 * time.Second)
	tt.Frame()

	if !s.chatListState.FollowEnd || !s.chatListState.AtEnd() {
		t.Fatalf("初始应处于溢出并贴底跟随状态：FollowEnd=%v AtEnd=%v", s.chatListState.FollowEnd, s.chatListState.AtEnd())
	}
	if tt.HasText("问题-01") {
		t.Fatal("初始消息列表应溢出视口，最早期消息不应仍在视口内")
	}
	if tt.FramePending() {
		t.Fatal("静止状态下不应请求下一帧")
	}
	beforeRect, ok := tt.Find("回答-20")
	if !ok {
		t.Fatal("找不到追加前的最后一条助手消息 回答-20")
	}

	// 追加新用户消息后渲染一帧：禁用 MoveAnimation 后既不请求动画续帧，旧行也立即落到最终位置。
	s.mu.Lock()
	s.messages = append(s.messages, Message{
		ID:        "u21",
		Role:      "user",
		Text:      "新问题-21",
		TurnIndex: 21,
		IsFinal:   true,
	})
	s.messagesRev++
	s.mu.Unlock()
	tt.Frame()

	if s.chatListState.MoveAnimation != nil {
		t.Fatal("主聊天列表 chatListState.MoveAnimation 必须为 nil")
	}
	if tt.FramePending() {
		t.Fatal("追加消息后不应为行位移动画请求下一帧")
	}
	if !tt.HasText("新问题-21") {
		t.Fatal("追加后应立即显示新消息 新问题-21")
	}
	afterRect, ok := tt.Find("回答-20")
	if !ok {
		t.Fatal("追加后应仍能找到 回答-20")
	}
	if afterRect.Y >= beforeRect.Y {
		t.Fatalf("贴底跟随追加后原有可见行应上移：beforeY=%v afterY=%v", beforeRect.Y, afterRect.Y)
	}

	tt.Advance(3 * time.Second)
	tt.Frame()
	settledRect, ok := tt.Find("回答-20")
	if !ok {
		t.Fatal("推进时钟后应仍能找到 回答-20")
	}
	if afterRect.Y != settledRect.Y {
		t.Fatalf("原有可见行在追加首帧应直接位于最终位置：afterY=%v settledY=%v", afterRect.Y, settledRect.Y)
	}
}
