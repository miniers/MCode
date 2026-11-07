package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// advanceAnchorClock 推进一帧测试时钟，让底部垫高动画（ui.Smooth）往前走一步。
// Tester 的时钟默认静止，只有 Advance 才会推进，不推进动画永远停不稳、锚定也就收不了口。
func advanceAnchorClock(tt *ui.Tester) {
	tt.Advance(50 * time.Millisecond)
}

// testChatAnchor 拷贝一份会话的锚定状态，调用方无需持有 chatAnchorMu。
func testChatAnchor(s *chatState) chatAnchor {
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	return *getChatAnchorState(s)
}

// testThreadSending 判断会话是否还在发送中。
func testThreadSending(s *chatState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sending
}

// echoUserMessage 模拟后端把刚发出的用户消息回推进会话增量，发送后锚定全靠这一帧落地。
// 会话摘要必须带上 ID/ProjectID/Title 且不是运行中：onWatchUpdate 会用摘要整条覆盖会话，
// 运行中的话时间线末尾还会多一行运行指示，锚点行就不再是最后一行了。
func echoUserMessage(fake *FakeClient, id, text string, turn int) {
	fake.TriggerUpdate(ThreadUpdate{
		ThreadID: "t1",
		Thread:   Thread{ID: "t1", ProjectID: "p1", Title: "测试会话1"},
		Messages: []Message{{ID: id, Role: "user", Text: text, TurnIndex: turn, IsFinal: true}},
	})
}

// TestAnchorArmed 覆盖“刚发出消息”窗口的判定。
func TestAnchorArmed(t *testing.T) {
	cases := []struct {
		name       string
		sending    bool
		optimistic bool
		want       bool
	}{
		{"发送中", true, false, true},
		{"只有乐观运行标记", false, true, true},
		{"两者都有", true, true, true},
		{"都不在", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorArmed(tc.sending, tc.optimistic); got != tc.want {
				t.Fatalf("anchorArmed(%v, %v) = %v, want %v", tc.sending, tc.optimistic, got, tc.want)
			}
		})
	}
}

// TestArmChatAnchorBuildsAnchorWithoutSendingFrame 验证发送时直接记下的窗口足以建锚：
// 后端回推比渲染还快、没有任何一帧处在发送中时，回推后的下一帧也能建立锚定。
func TestArmChatAnchorBuildsAnchorWithoutSendingFrame(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	s.messages = []Message{
		{ID: "u12", Role: "user", Text: "问题12"},
		{ID: "a12", Role: "assistant", Text: "回答12"},
	}
	defer func() {
		chatAnchorMu.Lock()
		delete(chatAnchorStates, s)
		chatAnchorMu.Unlock()
	}()

	// 发出消息：只记窗口，全程不渲染发送中的帧（sending 立刻复位，乐观标记也不会出现）。
	s.armChatAnchor("t1")
	s.mu.Lock()
	s.messages = UpsertMessages(s.messages, []Message{{ID: "u13", Role: "user", Text: "新问题"}}, nil)
	hold := s.syncChatAnchorLocked("t1", true)
	s.mu.Unlock()
	if !hold {
		t.Fatalf("发送后回推用户消息的下一帧应当保持锚定")
	}
	a := testChatAnchor(s)
	if !a.holding || a.pending || a.messageID != "u13" {
		t.Fatalf("锚定状态不对: holding=%v pending=%v messageID=%q", a.holding, a.pending, a.messageID)
	}
}

// TestSyncChatAnchorDisabledDoesNotHold 验证关掉「发送后置顶消息」后不再建锚：
// 即使刚发出消息，也不进入 holding，且锚点消息推进到最新一条用户消息（重开开关不补锚定）。
func TestSyncChatAnchorDisabledDoesNotHold(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	s.messages = []Message{
		{ID: "u12", Role: "user", Text: "问题12"},
		{ID: "a12", Role: "assistant", Text: "回答12"},
	}
	defer func() {
		chatAnchorMu.Lock()
		delete(chatAnchorStates, s)
		chatAnchorMu.Unlock()
	}()

	s.armChatAnchor("t1")
	s.mu.Lock()
	s.messages = UpsertMessages(s.messages, []Message{{ID: "u13", Role: "user", Text: "新问题"}}, nil)
	hold := s.syncChatAnchorLocked("t1", false)
	s.mu.Unlock()
	if hold {
		t.Fatalf("关掉置顶开关后不应当保持锚定")
	}
	a := testChatAnchor(s)
	if a.holding || a.pending {
		t.Fatalf("关掉置顶开关后不应进入保持状态: holding=%v pending=%v", a.holding, a.pending)
	}
	if a.messageID != "u13" {
		t.Fatalf("关掉开关时应把锚点消息推进到最新用户消息，实际 %q", a.messageID)
	}
}

// TestLatestUserMessageID 验证取最后一条带 ID 的用户消息。
func TestLatestUserMessageID(t *testing.T) {
	cases := []struct {
		name string
		msgs []Message
		want string
	}{
		{"空列表", nil, ""},
		{"只有助手消息", []Message{{ID: "a1", Role: "assistant", Text: "好的"}}, ""},
		{"最后一条用户消息", []Message{
			{ID: "u1", Role: "user", Text: "一"},
			{ID: "a1", Role: "assistant", Text: "二"},
			{ID: "u2", Role: "user", Text: "三"},
		}, "u2"},
		{"跳过没有 ID 的用户消息", []Message{
			{ID: "u1", Role: "user", Text: "一"},
			{ID: "", Role: "user", Text: "二"},
		}, "u1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := latestUserMessageID(tc.msgs); got != tc.want {
				t.Fatalf("latestUserMessageID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAnchorRowIndex 验证按消息 ID 找时间线条目下标。
func TestAnchorRowIndex(t *testing.T) {
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", msg: Message{ID: "u1"}},
		{kind: timelineItemToolGroup, id: "toolgroup-t1"},
		{kind: timelineItemSingle, id: "a1", msg: Message{ID: "a1"}},
	}
	cases := []struct {
		name      string
		messageID string
		want      int
	}{
		{"命中普通消息", "a1", 2},
		{"命中工具组 ID", "toolgroup-t1", 1},
		{"找不到", "missing", -1},
		{"空 ID", "", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorRowIndex(items, tc.messageID); got != tc.want {
				t.Fatalf("anchorRowIndex(%q) = %d, want %d", tc.messageID, got, tc.want)
			}
		})
	}
}

// TestAnchorShouldRelease 验证回复超过一屏后解除锚定。
func TestAnchorShouldRelease(t *testing.T) {
	cases := []struct {
		name        string
		anchorRow   int
		lastItem    int
		visibleRows int
		want        bool
	}{
		{"还没有锚点行", -1, 20, 10, false},
		{"没有条目", 3, -1, 10, false},
		{"正好一屏", 0, 10, 10, false},
		{"超过一屏", 0, 11, 10, true},
		{"行数未知时按估算值", 0, 13, 0, true},
		{"行数未知且没超过估算值", 0, 12, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := anchorShouldRelease(tc.anchorRow, tc.lastItem, tc.visibleRows)
			if got != tc.want {
				t.Fatalf("anchorShouldRelease(%d, %d, %d) = %v, want %v",
					tc.anchorRow, tc.lastItem, tc.visibleRows, got, tc.want)
			}
		})
	}
}

// TestAnchorLeadPlaceholders 验证锚点行上方连续占位行的计数。
func TestAnchorLeadPlaceholders(t *testing.T) {
	cases := []struct {
		name  string
		items []timelineItem
		row   int
		want  int
	}{
		{"上方紧邻一个胶囊行", []timelineItem{
			{kind: timelineItemSingle, id: "a12"},
			{kind: timelineItemSwarmCapsules, id: "capsules-1"},
			{kind: timelineItemSingle, id: "u13"},
		}, 2, 1},
		{"上方连续胶囊行加派发占位行", []timelineItem{
			{kind: timelineItemSingle, id: "a12"},
			{kind: timelineItemSwarmDispatch, id: "dispatch-1"},
			{kind: timelineItemSwarmCapsules, id: "capsules-1"},
			{kind: timelineItemSingle, id: "u13"},
		}, 3, 2},
		{"上方是普通消息", []timelineItem{
			{kind: timelineItemSingle, id: "a12"},
			{kind: timelineItemSingle, id: "u13"},
		}, 1, 0},
		{"row 为 0 时没有上方行", []timelineItem{
			{kind: timelineItemSwarmCapsules, id: "capsules-1"},
		}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorLeadPlaceholders(tc.items, tc.row); got != tc.want {
				t.Fatalf("anchorLeadPlaceholders() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAnchorPinnedAtTopWithLead 验证锚点行上方的空占位行计入贴顶余量。
func TestAnchorPinnedAtTopWithLead(t *testing.T) {
	a := &chatAnchor{lead: 1}
	if !a.pinnedAtTop(34, 38, 36) {
		t.Fatalf("锚点行上方只有 1 个空占位行时应当算贴顶")
	}
	if a.pinnedAtTop(33, 38, 36) {
		t.Fatalf("首行再往上 1 行（超出余量）时不应算贴顶")
	}
	a.row = 36
	if a.scrolledByUser(34) {
		t.Fatalf("首行落在占位行上时不应算用户滚走")
	}
	if !a.scrolledByUser(33) {
		t.Fatalf("首行越过占位行时应当算用户滚走")
	}
}

// TestAnchorScrolledByUser 验证停稳后可见首行离开锚点行就算用户滚走了。
func TestAnchorScrolledByUser(t *testing.T) {
	a := &chatAnchor{row: 5}
	cases := []struct {
		first int
		want  bool
	}{
		{5, false},
		{4, false},
		{6, false},
		{3, true},
		{7, true},
	}
	for _, tc := range cases {
		if got := a.scrolledByUser(tc.first); got != tc.want {
			t.Fatalf("scrolledByUser(%d) = %v, want %v", tc.first, got, tc.want)
		}
	}
}

// TestChatAnchorPinsSentMessage 验证发送后新消息停在视口顶部、不再跟随到底，
// 用户手动滚走后解除锚定，点回到底部后恢复跟随并能再次锚定。
func TestChatAnchorPinsSentMessage(t *testing.T) {
	fake := NewFakeClient()
	var msgs []Message
	for i := 1; i <= 12; i++ {
		msgs = append(msgs,
			Message{ID: fmt.Sprintf("u%d", i), Role: "user", Text: fmt.Sprintf("问题%d", i), TurnIndex: i, IsFinal: true},
			Message{ID: fmt.Sprintf("a%d", i), Role: "assistant", Text: fmt.Sprintf("回答%d", i), TurnIndex: i, IsFinal: true},
		)
	}
	fake.SetMessages("t1", msgs)

	var s *chatState
	tt := ui.NewTester(Root(fake, func(cs *chatState) { s = cs }), 1000, 700)
	waitFor(t, tt, func() bool { return s != nil && tt.HasText("回答12") })

	if !s.chatListState.FollowEnd {
		t.Fatalf("没发送时应当跟随到底")
	}

	s.sendMessage("新问题")
	// sendMessage 不会自己往本地列表里塞乐观消息：用户消息由后端增量回推，
	// 那一帧还会顺手撤掉乐观运行标记，所以先渲染一帧记下“刚发出”的窗口再回推。
	waitFor(t, tt, func() bool { return !testThreadSending(s) })
	echoUserMessage(fake, "u13", "新问题", 13)
	waitFor(t, tt, func() bool {
		advanceAnchorClock(tt)
		a := testChatAnchor(s)
		return a.holding && a.pinned
	})

	a := testChatAnchor(s)
	if !s.chatAnchorHolding("t1") {
		t.Fatalf("发送后应处在锚定状态")
	}
	if s.chatListState.FollowEnd {
		t.Fatalf("锚定期间不应跟随到底")
	}
	first, last := s.chatListState.Visible()
	if a.scrolledByUser(first) {
		t.Fatalf("锚点行 %d 应当停在视口顶部，实际首行 %d", a.row, first)
	}
	if last != a.row {
		// 刚发出的消息之下只允许出现高度为 0 的占位行（子线程胶囊行、派发占位行）：
		// 它们不渲染内容，所以锚点行仍然是唯一可见的新行。
		items := s.assembledTimelineItems(nil)
		if last < a.row {
			t.Fatalf("刚发出的消息应当可见，实际末行 %d，锚点行 %d", last, a.row)
		}
		for i := a.row + 1; i <= last && i < len(items); i++ {
			if !anchorPlaceholderItem(items[i]) {
				t.Fatalf("刚发出的消息之下不应有可见的新内容行：末行 %d，锚点行 %d，漏出的条目 %q", last, a.row, items[i].id)
			}
		}
	}

	// 用户自己往上滚：锚定解除，但不再把视口拽回底部。
	tt.Scroll(400, 300, 0, -900)
	tt.Frame()
	waitFor(t, tt, func() bool { return !s.chatAnchorHolding("t1") })
	if !s.chatListState.FollowEnd {
		t.Fatalf("用户滚动解除锚定后应恢复跟随")
	}
	if s.chatListState.AtEnd() {
		t.Fatalf("用户滚动解除锚定后不应被强行拉回底部")
	}

	// 点「跳到最新」走的分支：解除锚定并滚到底。
	s.releaseChatAnchor("t1", true)
	waitFor(t, tt, func() bool { return s.chatListState.AtEnd() })

	// 回到底部后再发一条：还能重新锚定。
	waitFor(t, tt, func() bool { return !testThreadSending(s) })
	prevAnchor := testChatAnchor(s).messageID
	s.sendMessage("再问一次")
	waitFor(t, tt, func() bool { return !testThreadSending(s) })
	echoUserMessage(fake, "u14", "再问一次", 14)
	waitFor(t, tt, func() bool {
		advanceAnchorClock(tt)
		a := testChatAnchor(s)
		return a.holding && a.pinned && a.messageID != "" && a.messageID != prevAnchor
	})
	if s.chatListState.FollowEnd {
		t.Fatalf("第二次发送后同样不应跟随到底")
	}
}

// TestAnchorPadTarget 验证垫高目标：正常值、几何量缺失、已在顶部附近、超过一屏时封顶。
func TestAnchorPadTarget(t *testing.T) {
	cases := []struct {
		name string
		geom anchorGeometry
		want float32
	}{
		{"普通密度", anchorGeometry{rowTop: 600, listTop: 100, padTop: 16}, 484},
		{"紧凑密度", anchorGeometry{rowTop: 600, listTop: 100, padTop: 10}, 490},
		{"锚点行还没排过版", anchorGeometry{listTop: 100, padTop: 16}, 0},
		{"列表还没排过版", anchorGeometry{rowTop: 600, padTop: 16}, 0},
		{"两个都没排过版", anchorGeometry{}, 0},
		{"锚点行已在顶部附近", anchorGeometry{rowTop: 108, listTop: 100, padTop: 16}, 0},
		{"正好顶到顶", anchorGeometry{rowTop: 116, listTop: 100, padTop: 16}, 0},
		{"超过一屏时封顶", anchorGeometry{rowTop: 2000, listTop: 100, padTop: 16}, chatAnchorBottomPad},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorPadTarget(tc.geom); got != tc.want {
				t.Fatalf("anchorPadTarget(%+v) = %v, want %v", tc.geom, got, tc.want)
			}
		})
	}
}

// TestAnchorGlideActionFor 验证“还没贴顶”这一帧的分派：退化即时跳转、收口、推进、
// 等几何量、等待超限转即时、目标不大于 0、启动。
func TestAnchorGlideActionFor(t *testing.T) {
	ready := anchorGeometry{rowTop: 600, listTop: 100, padTop: 16}
	reduced := ready
	reduced.reduceMotion = true
	missing := anchorGeometry{padTop: 16}
	cases := []struct {
		name     string
		a        chatAnchor
		geom     anchorGeometry
		sameList bool
		want     anchorGlideAction
	}{
		{"减少动态效果走即时跳转", chatAnchor{}, reduced, true, anchorGlideInstant},
		{"垫高已到位就收口", chatAnchor{padHeld: true}, ready, true, anchorGlideSettle},
		{"动画推进中继续推进", chatAnchor{padAnimating: true}, ready, true, anchorGlideHold},
		{"动画中条目数变了提前收口", chatAnchor{padAnimating: true, padItems: 3}, ready, false, anchorGlideSettle},
		{"动画中用户滚走提前收口", chatAnchor{padAnimating: true, padItems: 5}, ready, false, anchorGlideSettle},
		{"几何量缺失先等", chatAnchor{}, missing, false, anchorGlideWait},
		{"等到上限转即时", chatAnchor{tries: anchorGlideWaitFrames}, missing, false, anchorGlideInstant},
		{"目标不大于 0 直接跳", chatAnchor{}, anchorGeometry{rowTop: 110, listTop: 100, padTop: 16}, false, anchorGlideInstant},
		{"几何量齐了就启动", chatAnchor{}, ready, false, anchorGlideStart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.a
			if got := anchorGlideActionFor(&a, tc.geom, tc.sameList); got != tc.want {
				t.Fatalf("anchorGlideActionFor() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestChatAnchorReleaseClearsPad 验证解除锚定时把垫高动画的状态全部清零：留着 padHeld
// 下一帧列表底部还会垫一屏空白，留着 padJumped 下一段动画会直接停在目标值上。
func TestChatAnchorReleaseClearsPad(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	defer func() {
		chatAnchorMu.Lock()
		delete(chatAnchorStates, s)
		chatAnchorMu.Unlock()
	}()

	chatAnchorMu.Lock()
	a := getChatAnchorState(s)
	a.threadID = "t1"
	a.holding = true
	a.padAnimating = true
	a.padJumped = true
	a.padHeld = true
	a.padTarget = chatAnchorBottomPad
	a.padItems = 7
	a.release()
	got := *a
	chatAnchorMu.Unlock()

	if got.padAnimating || got.padJumped || got.padHeld {
		t.Fatalf("解除锚定后垫高状态应清零: animating=%v jumped=%v held=%v",
			got.padAnimating, got.padJumped, got.padHeld)
	}
	if got.padTarget != 0 || got.padItems != 0 {
		t.Fatalf("解除锚定后垫高目标与条目数应清零: target=%v items=%d", got.padTarget, got.padItems)
	}
}
