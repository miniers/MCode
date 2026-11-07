package mcodeui

import "testing"

// hasTimelineMessage 报告时间线条目里是否有指定消息 ID 的单条消息行。
func hasTimelineMessage(items []timelineItem, id string) bool {
	for _, it := range items {
		if it.kind == timelineItemSingle && it.msg.ID == id {
			return true
		}
	}
	return false
}

// TestTimelineAssembledCacheUsesSnapshotRev 覆盖首帧与历史消息加载抢跑的场景：
// 拼装时时间线条目还是按空快照拼的，而 messagesRev 已经先涨到了新值。
// 之后按新消息重建条目，拼装缓存必须失效，不能继续复用那份没有消息的时间线。
func TestTimelineAssembledCacheUsesSnapshotRev(t *testing.T) {
	s := newChatState(nil)
	s.messages = []Message{{ID: "m1", Role: "assistant", Text: "加载后的正文", IsFinal: true}}
	s.messagesRev = 2

	// 抢跑帧：时间线条目仍是空快照（timelineCacheRev 还是 0），messagesRev 已是 2。
	if items := s.assembledTimelineItems(nil); hasTimelineMessage(items, "m1") {
		t.Fatalf("空快照不应拼出消息行: %+v", items)
	}

	// 下一帧按当前消息重建时间线条目。
	s.timelineCacheRev = s.messagesRev
	s.timelineCacheItems = buildTimelineItemsLive(s.messages, nil, "")

	items := s.assembledTimelineItems(nil)
	if !hasTimelineMessage(items, "m1") {
		t.Fatalf("时间线条目重建后仍复用了旧拼装缓存，渲染出的条目: %+v", items)
	}
}

// TestTimelineCachesScopedPerChatState 验证同一进程里先后两个 chatState（会话 ID、消息 ID、
// messagesRev 初值都相同）不会互相复用时间线缓存，也不会命中上一份会话数据。
func TestTimelineCachesScopedPerChatState(t *testing.T) {
	texts := []string{"第一个会话的正文", "第二个会话的正文"}
	for i, text := range texts {
		s := newChatState(nil)
		s.selectedThreadID = "t1"
		s.messages = []Message{{ID: "m1", Role: "assistant", Text: text, IsFinal: true}}
		s.messagesRev = 2
		s.timelineCacheRev = s.messagesRev
		s.timelineCacheItems = buildTimelineItemsLive(s.messages, nil, "")

		found := false
		for _, it := range s.assembledTimelineItems(nil) {
			if it.kind == timelineItemSingle && it.msg.ID == "m1" && it.msg.Text == text {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("第 %d 个 chatState 的时间线没有渲染自己的正文 %q", i+1, text)
		}
	}
}

// TestParseSwarmReportCachedDistinguishesSameLengthText 验证汇报解析缓存不会把“消息 ID 与长度相同、
// 正文不同”的另一条消息的解析结果返回给当前消息。
func TestParseSwarmReportCachedDistinguishesSameLengthText(t *testing.T) {
	a := swarmNoticePrefix + "Aspen 的汇报\n\n## Aspen：实现浮窗卡片 (已完成)\n- 档位：标准 · 模型：x/y\n- 汇报总结：\nAAAA\n"
	b := swarmNoticePrefix + "Birch 的汇报\n\n## Birch：实现浮窗卡片 (已完成)\n- 档位：标准 · 模型：x/y\n- 汇报总结：\nBBBB\n"
	if len(a) != len(b) {
		t.Fatalf("样本长度必须相同才能覆盖该回归：%d vs %d", len(a), len(b))
	}
	if _, entries := parseSwarmReportCached("m1", a); len(entries) != 1 || entries[0].Name != "Aspen" {
		t.Fatalf("第一次解析结果不对: %+v", entries)
	}
	if _, entries := parseSwarmReportCached("m1", b); len(entries) != 1 || entries[0].Name != "Birch" {
		t.Fatalf("同 ID 同长度但正文不同的消息复用了旧解析结果: %+v", entries)
	}
}
