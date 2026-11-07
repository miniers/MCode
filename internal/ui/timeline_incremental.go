package mcodeui

// messageFingerprint 保存单条消息的廉价变更指纹。
// 选取 ID、角色、类型、回合、内容长度、完成状态与工具执行状态/输入长度，
// 以 O(1) 复杂度捕捉流式打字、工具调用完成及内容变更。
type messageFingerprint struct {
	id              string
	role            string
	kind            string
	turnIndex       int
	turnID          string
	turnStatus      string
	turnStartedAt   string
	turnCompletedAt string
	textLen         int
	isFinal         bool
	toolStatus      string
	toolInputLen    int
	toolName        string
	toolCompletedAt string
}

// makeMessageFingerprint 为单条消息生成变更指纹。
func makeMessageFingerprint(m Message) messageFingerprint {
	return messageFingerprint{
		id:              m.ID,
		role:            m.Role,
		kind:            m.Kind,
		turnIndex:       m.TurnIndex,
		turnID:          m.TurnID,
		turnStatus:      m.TurnStatus,
		turnStartedAt:   m.TurnStartedAt,
		turnCompletedAt: m.TurnCompletedAt,
		textLen:         len(m.Text),
		isFinal:         m.IsFinal,
		toolStatus:      m.ToolStatus,
		toolInputLen:    len(m.ToolInput),
		toolName:        m.ToolName,
		toolCompletedAt: m.ToolCompletedAt,
	}
}

// computeMessageFingerprints 为完整消息切片计算指纹列表。
func computeMessageFingerprints(messages []Message) []messageFingerprint {
	fps := make([]messageFingerprint, len(messages))
	for i, m := range messages {
		fps[i] = makeMessageFingerprint(m)
	}
	return fps
}

// hasExpandedChanged 比较工具组展开快照与当前展开状态是否发生变化。
func hasExpandedChanged(prev, curr map[string]bool) bool {
	if len(prev) == 0 && len(curr) == 0 {
		return false
	}
	for k, v := range curr {
		if prev[k] != v {
			return true
		}
	}
	for k, v := range prev {
		if curr[k] != v {
			return true
		}
	}
	return false
}

// cloneBoolMap 深度拷贝布尔 map 快照。
func cloneBoolMap(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// timelineSettingsChanged 报告「自动折叠思考内容」开关是否与上次时间线构建时不一致。
// 切换该开关会改变工具组与回合折叠的默认开合，渲染入口据此把 needRebuild 置为真，
// 静止会话（无新消息）也能立即生效。调用方不能持有 s.mu：
// isAutoCollapseReasoningEnabled 内部会锁 settingsMu 与设置自己的 mu。
func timelineSettingsChanged(s *chatState) bool {
	return !isAutoCollapseReasoningEnabled(s) != s.timelineSnapshotGroupsOpen
}

// rebuildTimelineIncremental 执行时间线增量重建。
// 找出第一个发生变化的消息下标，从包含该下标的那一项开始重建，之前的 items 原样复用；
// 当快照不可用、线程切换、展开状态变化或工具组默认展开值变化时退回全量重建。
// liveGroupID 可选传入当前正在进行的活动组 ID（仅用于标记进行中的组，开合仍由用户设置与
// 「自动折叠思考内容」开关决定）。
// 关闭自动折叠思考时工具组默认展开：这一默认值随开关变化，所以也纳入缓存快照。
func (s *chatState) rebuildTimelineIncremental(threadID string, messages []Message, expanded map[string]bool, liveGroupID ...string) ([]timelineItem, []messageFingerprint) {
	var liveID string
	if len(liveGroupID) > 0 {
		liveID = liveGroupID[0]
	}
	// 关闭「自动折叠思考内容」时，用户未显式设置过的工具组默认展开。
	groupsOpen := !isAutoCollapseReasoningEnabled(s)
	newFps := computeMessageFingerprints(messages)

	// 失效退回全量重建条件：
	// 1. 快照不可用（历史 items 为空、历史指纹为空，或历史消息多于当前消息）；
	// 2. 线程发生切换；
	// 3. 工具组展开/折叠状态发生变化；
	// 4. 工具组的默认展开值发生变化（切换「自动折叠思考内容」后首帧必须全量重建）。
	if len(s.timelineCacheItems) == 0 ||
		len(s.timelineSnapshotMsgs) == 0 ||
		s.timelineSnapshotThreadID != threadID ||
		len(s.timelineSnapshotMsgs) > len(messages) ||
		s.timelineSnapshotGroupsOpen != groupsOpen ||
		hasExpandedChanged(s.timelineSnapshotExpanded, expanded) {
		items := buildTimelineItemsOpts(messages, expanded, liveID, groupsOpen)
		// 全量重建是折叠的唯一出口：增量、全量与测试黄金结果都过这里，两边才能逐项相等。
		items = applyTurnWorkFold(s, threadID, items)
		s.timelineSnapshotGroupsOpen = groupsOpen
		return items, newFps
	}

	// 找出第一个发生变化的消息下标
	diffMsgIdx := -1
	for i := 0; i < len(s.timelineSnapshotMsgs); i++ {
		if s.timelineSnapshotMsgs[i] != newFps[i] {
			diffMsgIdx = i
			break
		}
	}

	if diffMsgIdx == -1 {
		// 历史指纹全部吻合
		if len(s.timelineSnapshotMsgs) == len(messages) {
			// 消息列表无任何变化，直接复用上一次的 items
			s.timelineSnapshotGroupsOpen = groupsOpen
			return s.timelineCacheItems, newFps
		}
		// 纯末尾追加消息
		diffMsgIdx = len(s.timelineSnapshotMsgs)
	}

	// 计算安全的独立重建起点：
	// 活动消息（工具/推理）会与同回合活动消息聚合为组项，
	// 只要变更点前一条仍是活动消息，就需持续向前回退到整段活动消息的起始下标，
	// 否则缓存前缀会把本应同组的推理/工具项拆开。
	safeMsgStart := diffMsgIdx
	for safeMsgStart > 0 && isActivityMessage(messages[safeMsgStart-1]) {
		safeMsgStart--
	}

	prevItems := s.timelineCacheItems
	safeMsgStart = stepBackHiddenMessages(messages, safeMsgStart)
	itemCutIdx, safeMsgStart := cutTimelineAt(prevItems, safeMsgStart)
	safeMsgStart = stepBackHiddenMessages(messages, safeMsgStart)

	// 切点落在某个回合中间、而前缀末尾正好是这个回合的折叠项时，整个回合重建：
	// 「只有思考与工具」的静默回合一旦尾巴长出答复，摘要会从「后台巡视」变回「用时」，
	// 复用旧折叠项就会与全量构建不一致。这里只扩大重建范围，不改折叠语义。
	for safeMsgStart < len(messages) && itemCutIdx > 0 {
		cut := prevItems[itemCutIdx-1]
		if cut.kind != timelineItemTurnWork || cut.turnIndex != messages[safeMsgStart].TurnIndex {
			break
		}
		turnStart := safeMsgStart
		for turnStart > 0 && messages[turnStart-1].TurnIndex == cut.turnIndex {
			turnStart--
		}
		nextCut, nextStart := cutTimelineAt(prevItems, stepBackHiddenMessages(messages, turnStart))
		nextStart = stepBackHiddenMessages(messages, nextStart)
		if nextCut >= itemCutIdx && nextStart >= safeMsgStart {
			// 已经退无可退：再算一遍还是同一处，直接收手。
			break
		}
		itemCutIdx, safeMsgStart = nextCut, nextStart
	}

	// 复制复用前半部分 items
	reusedItems := make([]timelineItem, itemCutIdx)
	copy(reusedItems, prevItems[:itemCutIdx])

	// 对 safeMsgStart 之后的后缀消息生成新 items
	suffixMessages := messages[safeMsgStart:]
	suffixItems := buildTimelineItemsOpts(suffixMessages, expanded, liveID, groupsOpen)
	for i := range suffixItems {
		suffixItems[i].msgIndex += safeMsgStart
	}

	result := append(reusedItems, suffixItems...)
	s.timelineSnapshotGroupsOpen = groupsOpen
	return applyTurnWorkFold(s, threadID, result), newFps
}

// cutTimelineAt 在缓存条目里定位第一个覆盖到 msgStart 的切点，返回切点下标与（可能被拉回的）起点。
// 看的是 itemLastMsgIndex 而不是 msgIndex：工具组头项与回合折叠项覆盖一整段消息，
// 改动落在段内部时只切在项之前会把整组/整回合拆开。
// 切到回合折叠项时再把它前面的回合用户消息也纳入重建，否则后缀重建缺了回合开头，折不起来。
func cutTimelineAt(prevItems []timelineItem, msgStart int) (itemCutIdx, safeMsgStart int) {
	itemCutIdx = len(prevItems)
	safeMsgStart = msgStart
	for i, it := range prevItems {
		if itemLastMsgIndex(it) < safeMsgStart {
			continue
		}
		itemCutIdx = i
		if it.kind == timelineItemTurnWork && it.msgIndex < safeMsgStart {
			safeMsgStart = it.msgIndex
		}
		break
	}
	// 起点回退后前缀最后一项可能也覆盖了新起点：这项一起交回重建，避免同一段消息生成两遍。
	for itemCutIdx > 0 && itemLastMsgIndex(prevItems[itemCutIdx-1]) >= safeMsgStart {
		itemCutIdx--
	}
	return itemCutIdx, safeMsgStart
}

// stepBackHiddenMessages 把重建起点回退到紧邻的隐藏消息之前。
// 回合开头的隐藏唤醒消息不产生条目，因此不影响切点下标；但它要跟着后缀一起重建：
// 否则重建后的回合头没有唤醒标记，折叠项就丢了唤醒原因，也认不出静默回合。
func stepBackHiddenMessages(messages []Message, start int) int {
	for start > 0 && isHiddenTimelineMessage(messages[start-1]) {
		start--
	}
	return start
}
