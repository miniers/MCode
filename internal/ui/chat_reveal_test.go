package mcodeui

import "testing"

// TestTakeStreamReveal_ExistingMessageShowsAtOnce 验证打开会话时已存在的正文
// 不会走逐字揭示：重新打开一个正在运行的会话，历史回答应立刻完整显示。
func TestTakeStreamReveal_ExistingMessageShowsAtOnce(t *testing.T) {
	s := &chatState{}
	existing := Message{ID: "m1", Role: "assistant", Text: "打开会话前就已经写好的完整回答"}
	s.markRevealBaselineLocked([]Message{existing})

	if s.takeStreamReveal(existing) {
		t.Fatal("打开会话时已有的正文不应逐字揭示")
	}
	// 没有新增内容时反复渲染仍保持静态。
	if s.takeStreamReveal(existing) {
		t.Fatal("正文未增长时不应切换为逐字揭示")
	}
}

// TestTakeStreamReveal_NewMessageStreams 验证打开会话之后才出现的消息照常逐字展示。
func TestTakeStreamReveal_NewMessageStreams(t *testing.T) {
	s := &chatState{}
	s.markRevealBaselineLocked(nil)

	first := Message{ID: "m2", Role: "assistant", Text: "新"}
	if !s.takeStreamReveal(first) {
		t.Fatal("打开后新出现的消息应逐字揭示")
	}
	grown := Message{ID: "m2", Role: "assistant", Text: "新到达的一段"}
	if !s.takeStreamReveal(grown) {
		t.Fatal("新消息的后续增量应继续逐字揭示")
	}
}

// TestTakeStreamReveal_IncrementAfterOpen 验证打开会话后的第一次增量先整段铺满，
// 避免把打开时已有的历史内容从头重播；之后的增量才逐字揭示。
func TestTakeStreamReveal_IncrementAfterOpen(t *testing.T) {
	s := &chatState{}
	s.markRevealBaselineLocked([]Message{{ID: "m3", Role: "assistant", Text: "打开时已有"}})

	firstIncrement := Message{ID: "m3", Role: "assistant", Text: "打开时已有 + 新到的第一部分"}
	if s.takeStreamReveal(firstIncrement) {
		t.Fatal("打开后的第一次增量应整段铺满，避免从头重播")
	}
	nextIncrement := Message{ID: "m3", Role: "assistant", Text: "打开时已有 + 新到的第一部分，继续增长"}
	if !s.takeStreamReveal(nextIncrement) {
		t.Fatal("之后的增量应逐字揭示")
	}
}

// TestTakeStreamReveal_EmptyIDStreams 验证没有消息 ID 时保持原有流式行为。
func TestTakeStreamReveal_EmptyIDStreams(t *testing.T) {
	s := &chatState{}
	if !s.takeStreamReveal(Message{Role: "assistant", Text: "无 ID"}) {
		t.Fatal("缺少消息 ID 时应保持流式渲染")
	}
}

// TestTakeStreamReveal_RemountShowsAtOnce 验证流式消息滚出视口被卸载再滚回重新挂载时，
// 首帧返回 false（走 View 铺满当前全文），下一帧恢复返回 true 继续逐字揭示；
// 连续每帧渲染时不会触发帧间隔检测。
func TestTakeStreamReveal_RemountShowsAtOnce(t *testing.T) {
	s := &chatState{}
	advanceFrame := func() {
		s.mu.Lock()
		s.revealFrame++
		s.mu.Unlock()
	}

	msg := Message{ID: "m4", Role: "assistant", Text: "流式"}
	// 首帧：新消息逐字揭示。
	advanceFrame()
	if !s.takeStreamReveal(msg) {
		t.Fatal("新消息首帧应返回 true")
	}

	// 连续多帧渲染：一直返回 true，不会误判为重新挂载。
	for i := 0; i < 10; i++ {
		advanceFrame()
		msg.Text += "增量"
		if !s.takeStreamReveal(msg) {
			t.Fatalf("第 %d 个连续帧不应误判为重新挂载", i+1)
		}
	}

	// 模拟滚出视口：只推进帧计数，不调用 takeStreamReveal。
	advanceFrame()
	advanceFrame()

	// 滚回视口重新挂载那一帧：返回 false，由 View 一次铺满当前全文。
	advanceFrame()
	msg.Text += "滚出期间新增"
	if s.takeStreamReveal(msg) {
		t.Fatal("跳帧后重新挂载首帧应返回 false 以整段铺满")
	}

	// 再下一帧：恢复返回 true 继续逐字揭示新增内容。
	advanceFrame()
	msg.Text += "后续"
	if !s.takeStreamReveal(msg) {
		t.Fatal("重新挂载的下一帧应恢复返回 true")
	}
}
