package mcodeui

import (
	"fmt"
	"testing"
	"time"
)

// TestShouldAnimateMessageEnter 验证入场动画只在「第一次出现、且时间戳距今不到 1.5 秒的完成态消息」上播放。
func TestShouldAnimateMessageEnter(t *testing.T) {
	now := time.Date(2026, 11, 3, 14, 5, 0, 0, time.Local)
	cases := []struct {
		name      string
		seen      bool
		msgTime   time.Time
		streaming bool
		want      bool
	}{
		{name: "新到达的用户消息", seen: false, msgTime: now.Add(-500 * time.Millisecond), want: true},
		{name: "加载历史时的旧消息", seen: false, msgTime: now.Add(-3 * time.Second), want: false},
		{name: "已经播过的消息", seen: true, msgTime: now.Add(-500 * time.Millisecond), want: false},
		{name: "流式输出中的助手消息", seen: false, msgTime: now.Add(-200 * time.Millisecond), streaming: true, want: false},
		{name: "没有时间戳的消息", seen: false, msgTime: time.Time{}, want: false},
		{name: "刚好超出时间窗", seen: false, msgTime: now.Add(-messageEnterWindow), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAnimateMessageEnter(tc.seen, tc.msgTime, now, tc.streaming); got != tc.want {
				t.Fatalf("shouldAnimateMessageEnter(seen=%v, streaming=%v) = %v, want %v", tc.seen, tc.streaming, got, tc.want)
			}
		})
	}
}

// TestBeginMessageEnterOnceAndStreamingPromotion 验证登记表：同一条消息只播一次，
// 流式中的助手消息先不播，进入完成态时补播一次。
func TestBeginMessageEnterOnceAndStreamingPromotion(t *testing.T) {
	s := newChatState(NewFakeClient())
	now := time.Now()
	streamingMsg := Message{ID: "a1", Role: "assistant", IsFinal: false, CreatedAt: now.Add(-200 * time.Millisecond).Format(time.RFC3339)}

	if s.beginMessageEnter(streamingMsg, true, now) {
		t.Fatalf("流式输出中的助手消息不应播放入场动画")
	}
	if s.beginMessageEnter(streamingMsg, true, now.Add(50*time.Millisecond)) {
		t.Fatalf("流式消息重复渲染不应播放入场动画")
	}
	finalMsg := streamingMsg
	finalMsg.IsFinal = true
	if !s.beginMessageEnter(finalMsg, false, now) {
		t.Fatalf("流式结束后应在完成态补播一次入场动画")
	}
	if s.beginMessageEnter(finalMsg, false, now) {
		t.Fatalf("同一条消息的入场动画只应播放一次")
	}

	oldMsg := Message{ID: "u1", Role: "user", CreatedAt: now.Add(-time.Minute).Format(time.RFC3339)}
	if s.beginMessageEnter(oldMsg, false, now) {
		t.Fatalf("历史消息不应播放入场动画")
	}
	freshMsg := Message{ID: "u2", Role: "user", CreatedAt: now.Format(time.RFC3339)}
	if !s.beginMessageEnter(freshMsg, false, now) {
		t.Fatalf("刚到达的用户消息应播放入场动画")
	}
	if s.beginMessageEnter(freshMsg, false, now) {
		t.Fatalf("用户消息重复渲染不应播放入场动画")
	}
}

// TestBeginMessageEnterRegistryCap 验证登记表超过上限后整表清空重建，不会无限增长。
func TestBeginMessageEnterRegistryCap(t *testing.T) {
	s := newChatState(NewFakeClient())
	now := time.Now()
	old := now.Add(-time.Minute).Format(time.RFC3339)
	for i := 0; i < messageEnterCap+50; i++ {
		s.beginMessageEnter(Message{ID: fmt.Sprintf("m%05d", i), Role: "user", CreatedAt: old}, false, now)
	}
	s.mu.Lock()
	seen := len(s.seenMsgIDs)
	s.mu.Unlock()
	if seen > messageEnterCap {
		t.Fatalf("入场登记表超过上限仍在增长: %d", seen)
	}
}

// TestSystemNoticeActionable 验证只有带可操作入口的系统通知才判定为可操作。
func TestSystemNoticeActionable(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{text: "请求失败，请重试", want: true},
		{text: "连接中断，可重新发送", want: true},
		{text: "Retry the request", want: true},
		{text: "已切换到模型 Opus", want: false},
		{text: "已压缩上下文 · 120k → 12k", want: false},
		{text: "", want: false},
	}
	for _, tc := range cases {
		if got := systemNoticeActionable(tc.text); got != tc.want {
			t.Fatalf("systemNoticeActionable(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestTimelineRows_SystemMessageKeepsTextAndClick 验证系统消息改成发丝线药丸后文案照旧，
// 带「重试」的可操作通知 hover 与点击都不改变文案。
func TestTimelineRows_SystemMessageKeepsTextAndClick(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "构建失败了", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "我看看", TurnIndex: 1, IsFinal: true},
		{ID: "s1", Role: "system", Text: "已切换到模型 Opus", TurnIndex: 2},
		{ID: "s2", Role: "system", Text: "构建命令失败，请重试", TurnIndex: 2},
	}
	tt := newTimelineTester(t, msgs, "已切换到模型 Opus")
	if !tt.HasText("构建命令失败，请重试") {
		t.Fatalf("系统消息原文案应保留，实际: %q", tt.Texts())
	}

	hoverText(t, tt, "构建命令失败，请重试")
	clickText(t, tt, "构建命令失败，请重试")
	if !tt.HasText("构建命令失败，请重试") || !tt.HasText("已切换到模型 Opus") {
		t.Fatalf("hover 或点击系统消息后文案不应变化: %q", tt.Texts())
	}
}

// TestTimelineRows_ToolGroupShowsClock 验证工具组头右侧显示组内首条工具的时刻（HH:mm）。
func TestTimelineRows_ToolGroupShowsClock(t *testing.T) {
	start := time.Date(2026, 11, 3, 14, 5, 0, 0, time.Local)
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看看仓库状态", TurnIndex: 1, CreatedAt: start.Format(time.RFC3339)},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, CreatedAt: start.Format(time.RFC3339), ToolCompletedAt: start.Add(2 * time.Second).Format(time.RFC3339)},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, CreatedAt: start.Add(2 * time.Second).Format(time.RFC3339), ToolCompletedAt: start.Add(3 * time.Second).Format(time.RFC3339)},
	}
	tt := newTimelineTester(t, msgs, "思考 · 命令")
	if !tt.HasText("14:05") {
		t.Fatalf("工具组头应显示首条工具的时刻 14:05，实际: %q", tt.Texts())
	}
}
