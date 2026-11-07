package mcodeui

import (
	"testing"
	"time"
)

// 测试跨会话消息跳转的纯判定：会话要对上、登记不能过期。
func TestMessageJumpApplies(t *testing.T) {
	now := time.Now()

	if messageJumpApplies(nil, now, "t1") {
		t.Fatal("空登记不应被消费")
	}

	j := &messageJump{threadID: "t1", messageID: "m1", at: now}

	if !messageJumpApplies(j, now, "t1") {
		t.Fatal("同一会话的新登记应被消费")
	}
	if messageJumpApplies(j, now, "t2") {
		t.Fatal("会话没切过去时不该消费")
	}
	if messageJumpApplies(j, now, "") {
		t.Fatal("没有选中会话时不该消费")
	}
	if messageJumpApplies(j, now.Add(messageJumpTTL+time.Second), "t1") {
		t.Fatal("超过有效期的登记不该消费")
	}
	// 边界：刚好等于有效期仍算有效。
	if !messageJumpApplies(j, now.Add(messageJumpTTL), "t1") {
		t.Fatal("恰好到有效期的登记仍应消费")
	}
}

// 测试时间线里能否找到目标消息 ID。
func TestTimelineHasMessageID(t *testing.T) {
	items := []timelineItem{
		{kind: timelineItemSingle, msg: Message{ID: "m1"}},
		{kind: timelineItemSingle, msg: Message{ID: "m2"}},
	}
	if !timelineHasMessageID(items, "m2") {
		t.Fatal("应能找到 m2")
	}
	if timelineHasMessageID(items, "m3") {
		t.Fatal("不该找到 m3")
	}
	if timelineHasMessageID(items, "") {
		t.Fatal("空 ID 不该命中")
	}
	if timelineHasMessageID(nil, "m1") {
		t.Fatal("空时间线不该命中")
	}
}

// 测试消费跳转：目标消息不在时间线里时保留登记，落地后滚动并清掉快照的 scrollToEnd。
func TestConsumeMessageJump(t *testing.T) {
	state := newChatState(NewFakeClient())
	t.Cleanup(func() {
		messageJumpMu.Lock()
		delete(messageJumps, state)
		messageJumpMu.Unlock()
	})

	state.mu.Lock()
	state.scrollToEnd = true
	state.mu.Unlock()

	state.requestMessageJump("t1", "m1")

	// 会话还没切到目标会话：不该消费。
	items := []timelineItem{{kind: timelineItemSingle, msg: Message{ID: "m1"}}}
	consumeMessageJump(state, "t0", items)
	messageJumpMu.Lock()
	_, kept := messageJumps[state]
	messageJumpMu.Unlock()
	if !kept {
		t.Fatal("会话没切过去时不该清掉登记")
	}

	// 会话切过去了但目标消息还没加载出来：仍不该消费。
	consumeMessageJump(state, "t1", []timelineItem{{kind: timelineItemSingle, msg: Message{ID: "m2"}}})
	messageJumpMu.Lock()
	_, kept = messageJumps[state]
	messageJumpMu.Unlock()
	if !kept {
		t.Fatal("目标消息没到时不改清掉登记")
	}

	// 条件满足：消费掉并清空 scrollToEnd，免得下一帧又被抢回底部。
	consumeMessageJump(state, "t1", items)
	messageJumpMu.Lock()
	_, kept = messageJumps[state]
	messageJumpMu.Unlock()
	if kept {
		t.Fatal("命中后应清掉登记")
	}
	state.mu.Lock()
	scrollToEnd := state.scrollToEnd
	state.mu.Unlock()
	if scrollToEnd {
		t.Fatal("命中后应清掉 scrollToEnd")
	}
}

// 测试过期的登记会被顺手清掉，不会一直留在包里。
func TestConsumeMessageJumpDropsExpired(t *testing.T) {
	state := newChatState(NewFakeClient())
	t.Cleanup(func() {
		messageJumpMu.Lock()
		delete(messageJumps, state)
		messageJumpMu.Unlock()
	})

	messageJumpMu.Lock()
	messageJumps[state] = &messageJump{
		threadID:  "t1",
		messageID: "m1",
		at:        time.Now().Add(-2 * messageJumpTTL),
	}
	messageJumpMu.Unlock()

	items := []timelineItem{{kind: timelineItemSingle, msg: Message{ID: "m1"}}}
	consumeMessageJump(state, "t1", items)

	messageJumpMu.Lock()
	_, kept := messageJumps[state]
	messageJumpMu.Unlock()
	if kept {
		t.Fatal("过期登记应被清掉")
	}
}
