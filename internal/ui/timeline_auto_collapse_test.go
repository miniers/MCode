package mcodeui

import "testing"

// newAutoCollapseTestState 返回一个注册了设置状态的会话，并把「自动折叠思考内容」设为 autoCollapse。
func newAutoCollapseTestState(t *testing.T, autoCollapse bool) *chatState {
	t.Helper()
	s := &chatState{}
	st := &SettingsState{}
	v := autoCollapse
	st.cfg.Session.AutoCollapseReasoning = &v
	setChatSettings(s, st)
	t.Cleanup(func() {
		settingsMu.Lock()
		delete(settingsList, s)
		settingsMu.Unlock()
	})
	return s
}

// setAutoCollapse 在测试中途切换「自动折叠思考内容」开关，模拟用户在设置页改完立即生效。
func setAutoCollapse(s *chatState, enabled bool) {
	st := getChatSettings(s)
	v := enabled
	st.mu.Lock()
	st.cfg.Session.AutoCollapseReasoning = &v
	st.mu.Unlock()
}

// autoCollapseTestMessages 是一段同回合的「用户提问 + 思考 + 两条工具调用」，足够聚成一个工具组。
func autoCollapseTestMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "帮我看看", TurnIndex: 1},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先列目录", TurnIndex: 1},
		{ID: "t1", Role: "tool", ToolName: "ls", Text: "a.go", TurnIndex: 1},
		{ID: "t2", Role: "tool", ToolName: "cat", Text: "package main", TurnIndex: 1},
	}
}

// findToolGroupItem 在时间线项里找出指定 ID 的工具组头项。
func findToolGroupItem(items []timelineItem, groupID string) (timelineItem, bool) {
	for _, it := range items {
		if it.kind == timelineItemToolGroup && it.groupID == groupID {
			return it, true
		}
	}
	return timelineItem{}, false
}

// countGroupBeats 统计指定工具组在展开后追加的节拍项数量。
func countGroupBeats(items []timelineItem, groupID string) int {
	n := 0
	for _, it := range items {
		if it.kind == timelineItemToolBeat && it.groupID == groupID {
			n++
		}
	}
	return n
}

// TestBuildTimelineItemsOpts_GroupDefaultOpen 验证工具组默认值由 groupsOpenByDefault 决定，
// 且用户显式设置过开合状态时仍以设置为准。
func TestBuildTimelineItemsOpts_GroupDefaultOpen(t *testing.T) {
	msgs := autoCollapseTestMessages()
	groupID := "toolgroup-r1"

	// 默认展开：组头展开，并追加组内节拍项。
	openItems := buildTimelineItemsOpts(msgs, nil, "", true)
	group, ok := findToolGroupItem(openItems, groupID)
	if !ok {
		t.Fatalf("expected tool group item %q", groupID)
	}
	if !group.groupExpanded {
		t.Fatalf("groupsOpenByDefault=true 时工具组应默认展开")
	}
	if beats := countGroupBeats(openItems, groupID); beats == 0 {
		t.Fatalf("展开时应在组头后追加节拍项")
	}

	// 默认折叠：只剩组头项，没有节拍项。
	closedItems := buildTimelineItemsOpts(msgs, nil, "", false)
	group, ok = findToolGroupItem(closedItems, groupID)
	if !ok {
		t.Fatalf("expected tool group item %q", groupID)
	}
	if group.groupExpanded {
		t.Fatalf("groupsOpenByDefault=false 时工具组应默认折叠")
	}
	if beats := countGroupBeats(closedItems, groupID); beats != 0 {
		t.Fatalf("折叠时不应生成节拍项，实际 %d", beats)
	}

	// 用户显式收起：即使默认展开也折叠。
	explicitItems := buildTimelineItemsOpts(msgs, map[string]bool{groupID: false}, "", true)
	group, ok = findToolGroupItem(explicitItems, groupID)
	if !ok {
		t.Fatalf("expected tool group item %q", groupID)
	}
	if group.groupExpanded {
		t.Fatalf("显式收起后即使默认展开也应折叠")
	}
	if beats := countGroupBeats(explicitItems, groupID); beats != 0 {
		t.Fatalf("显式收起时不应生成节拍项，实际 %d", beats)
	}
}

// TestTurnWorkItemOpen_FollowsAutoCollapse 验证回合折叠项在无显式设置时跟随「自动折叠思考内容」开关，
// 显式设置始终优先。
func TestTurnWorkItemOpen_FollowsAutoCollapse(t *testing.T) {
	item := timelineItem{kind: timelineItemTurnWork, id: "turnwork-1"}

	off := newAutoCollapseTestState(t, false)
	if !turnWorkItemOpen(off, item) {
		t.Fatalf("关闭自动折叠时回合折叠项应默认展开")
	}
	off.mu.Lock()
	off.expandedTool = map[string]bool{item.id: false}
	off.mu.Unlock()
	if turnWorkItemOpen(off, item) {
		t.Fatalf("用户显式收起后应保持折叠")
	}

	on := newAutoCollapseTestState(t, true)
	if turnWorkItemOpen(on, item) {
		t.Fatalf("开启自动折叠时回合折叠项应默认折叠")
	}
	on.mu.Lock()
	on.expandedTool = map[string]bool{item.id: true}
	on.mu.Unlock()
	if !turnWorkItemOpen(on, item) {
		t.Fatalf("用户显式展开后应保持展开")
	}
}

// TestRebuildTimelineIncremental_GroupOpenFollowsAutoCollapse 验证消息不变、只切换开关时，
// 增量重建仍会退回全量重建，让工具组默认开合立即跟随开关。
func TestRebuildTimelineIncremental_GroupOpenFollowsAutoCollapse(t *testing.T) {
	s := newAutoCollapseTestState(t, true)
	threadID := "thread-1"
	msgs := autoCollapseTestMessages()
	groupID := "toolgroup-r1"

	items, fps := s.rebuildTimelineIncremental(threadID, msgs, nil)
	s.timelineCacheItems = items
	s.timelineSnapshotThreadID = threadID
	s.timelineSnapshotMsgs = fps
	s.timelineSnapshotExpanded = nil

	group, ok := findToolGroupItem(items, groupID)
	if !ok {
		t.Fatalf("expected tool group item %q", groupID)
	}
	if group.groupExpanded {
		t.Fatalf("开关开启时工具组应默认折叠")
	}
	if s.timelineSnapshotGroupsOpen {
		t.Fatalf("开关开启时快照中的默认展开值应为 false")
	}

	// 关闭开关，消息列表完全不变。
	setAutoCollapse(s, false)
	items, _ = s.rebuildTimelineIncremental(threadID, msgs, nil)

	group, ok = findToolGroupItem(items, groupID)
	if !ok {
		t.Fatalf("expected tool group item %q", groupID)
	}
	if !group.groupExpanded {
		t.Fatalf("开关关闭后工具组应立即默认展开")
	}
	if beats := countGroupBeats(items, groupID); beats == 0 {
		t.Fatalf("展开后应带回组内节拍项")
	}
	if !s.timelineSnapshotGroupsOpen {
		t.Fatalf("开关关闭后快照中的默认展开值应为 true")
	}
}

// TestTimelineSettingsChanged_FollowsAutoCollapse 验证渲染入口的 needRebuild 补充条件：
// 设置不变时不引入额外重建，切换开关后立即为真，重建一次后又回到假。
func TestTimelineSettingsChanged_FollowsAutoCollapse(t *testing.T) {
	s := newAutoCollapseTestState(t, true)
	threadID := "thread-1"
	msgs := autoCollapseTestMessages()

	items, fps := s.rebuildTimelineIncremental(threadID, msgs, nil)
	s.timelineCacheItems = items
	s.timelineSnapshotThreadID = threadID
	s.timelineSnapshotMsgs = fps
	s.timelineSnapshotExpanded = nil

	if timelineSettingsChanged(s) {
		t.Fatalf("刚构建完且设置未变时不应触发重建")
	}

	// 用户切换开关后，即使没有新消息也要重建。
	setAutoCollapse(s, false)
	if !timelineSettingsChanged(s) {
		t.Fatalf("切换开关后应立即触发重建")
	}

	items, fps = s.rebuildTimelineIncremental(threadID, msgs, nil)
	s.timelineCacheItems = items
	s.timelineSnapshotThreadID = threadID
	s.timelineSnapshotMsgs = fps
	s.timelineSnapshotExpanded = nil
	if timelineSettingsChanged(s) {
		t.Fatalf("重建后快照已记录新的默认展开值，不应再触发重建")
	}

	// 反向切换同样生效。
	setAutoCollapse(s, true)
	if !timelineSettingsChanged(s) {
		t.Fatalf("再次切换开关后应立即触发重建")
	}
}
