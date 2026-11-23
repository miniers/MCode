package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestComposerDockFloatsOverTimeline 验证 Docked 模式下底部卡片与输入框悬浮于时间线之上：
// (a) 时间线容器（与 message-list）的底边低于输入框顶边，即时间线延伸到悬浮区下方；
// (b) 列表滚到末尾后，最后一行的底边不低于 chat-bottom-dock 的顶边（完整露在悬浮区上方）；
// (c) 推进时钟后不再请求下一帧（静止不重绘）。
func TestComposerDockFloatsOverTimeline(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)

	fake := NewFakeClient()
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("u%02d", i),
			Role:      "user",
			Text:      fmt.Sprintf("消息-%02d", i),
			TurnIndex: i + 1,
			IsFinal:   true,
		})
	}
	fake.SetMessages("t1", msgs)

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 700)

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		dockH := state.chatBottomDockH
		state.mu.Unlock()
		return dockH > 0 && state.chatListState.AtEnd() && tt.HasText("消息-79")
	})

	// 推进时钟等待入场过渡与迷你地图空闲变淡都落定。
	tt.Advance(3 * time.Second)
	tt.Frame()

	composerRect, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatalf("找不到消息输入框")
	}
	lastMsgRect, ok := tt.Find("消息-79")
	if !ok {
		t.Fatalf("找不到最后一条消息 消息-79")
	}

	state.mu.Lock()
	timelineRect := state.chatTimelineBounds
	listRect := state.chatListBounds
	dockRect := state.chatBottomDockBounds
	state.mu.Unlock()

	if timelineRect.H <= 0 || listRect.H <= 0 || dockRect.H <= 0 {
		t.Fatalf("几何矩形未就绪: timeline=%+v list=%+v dock=%+v", timelineRect, listRect, dockRect)
	}

	// (a) 时间线容器与 message-list 底边低于输入框顶边，延伸到悬浮区下方。
	timelineBottom := timelineRect.Y + timelineRect.H
	listBottom := listRect.Y + listRect.H
	if timelineBottom <= composerRect.Y {
		t.Fatalf("timeline-container 底边 %.1f 应低于输入框顶边 %.1f", timelineBottom, composerRect.Y)
	}
	if listBottom <= composerRect.Y {
		t.Fatalf("message-list 底边 %.1f 应低于输入框顶边 %.1f", listBottom, composerRect.Y)
	}

	// (b) 列表滚到末尾后，最后一行的底边不低于 chat-bottom-dock 顶边（在屏幕坐标中 <= dockRect.Y）。
	lastMsgBottom := lastMsgRect.Y + lastMsgRect.H
	if lastMsgBottom > dockRect.Y {
		t.Fatalf("滚到末尾时最后一行底边 %.1f 应不低于 chat-bottom-dock 顶边 %.1f", lastMsgBottom, dockRect.Y)
	}

	// 验证悬浮区两侧空隙不拦截时间线滚轮（可穿透到底下 message-list），且「跳到最新」保持在悬浮区上方。
	if composerRect.X > dockRect.X+8 {
		tt.Scroll(dockRect.X+4, composerRect.Y+composerRect.H/2, 0, -1200)
		tt.Frame()
		if state.chatListState.AtEnd() {
			t.Fatalf("在悬浮区左侧空隙滚动应穿透到时间线并离开底部")
		}
		jumpRect, ok := tt.Find("跳到最新")
		if !ok {
			t.Fatalf("离开底部后应显示跳到最新按钮")
		}
		if jumpRect.Y+jumpRect.H > dockRect.Y {
			t.Fatalf("跳到最新按钮底边 %.1f 应位于悬浮区顶边 %.1f 上方", jumpRect.Y+jumpRect.H, dockRect.Y)
		}
		if err := tt.Click("跳到最新"); err != nil {
			t.Fatalf("点击跳到最新失败: %v", err)
		}
		waitFor(t, tt, func() bool { return state.chatListState.AtEnd() })
	}

	// (c) 推进时钟后不再请求下一帧（静止不重绘）。
	tt.Advance(3 * time.Second)
	if tt.FramePending() {
		t.Fatalf("静止后不应再请求下一帧")
	}
}
