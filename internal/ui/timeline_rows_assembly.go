// 时间线项目的组装与折叠。
package mcodeui

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// runningIndicatorItem 构造时间线末尾的“回复中”指示项：会话运行期间常驻展示。
func runningIndicatorItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemRunning, id: runningIndicatorKey(threadID)}
}

// runningIndicatorKey 返回“回复中”指示元素的稳定 Key：按会话区分，避免切换会话时复用错误节点。
func runningIndicatorKey(threadID string) string {
	if threadID == "" {
		return "running-indicator"
	}
	return "running-indicator-" + threadID
}

// backgroundWaitItem 构造时间线末尾的“等待后台任务”指示项：回合已结束但后台任务仍在跑时展示。
func backgroundWaitItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemBackgroundWait, id: backgroundWaitKey(threadID), threadID: threadID}
}

// backgroundWaitKey 返回等待后台任务条的稳定 Key：按会话区分，避免切换会话时复用错误节点。
func backgroundWaitKey(threadID string) string {
	if threadID == "" {
		return "background-wait"
	}
	return "background-wait-" + threadID
}

// sessionUsageItem 构造时间线底部的会话统计入口行，按会话区分稳定 Key。
func sessionUsageItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemSessionUsage, id: "session-usage-" + threadID, threadID: threadID}
}

// forkOriginItem 构造时间线最顶部的 fork 来源条：只带源会话 ID，标题在渲染时现查。
func forkOriginItem(threadID, sourceThreadID string) timelineItem {
	return timelineItem{
		kind:             timelineItemForkOrigin,
		id:               "fork-origin-" + threadID,
		threadID:         threadID,
		forkFromThreadID: sourceThreadID,
	}
}

func forwardedContextItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemForwardedContext, id: "forwarded-context-" + threadID, threadID: threadID}
}

// isReasoningMessage 判断消息是否为思考过程消息。
func isReasoningMessage(m Message) bool {
	return m.Kind == "reasoning" || m.Role == "reasoning"
}

// isActivityMessage 判断消息是否属于时间线里的“活动”行（思考过程与工具调用）；
// 同一回合内连续的活动行会合并成一个可折叠的工具组。
func isActivityMessage(m Message) bool {
	return m.Role == "tool" || isReasoningMessage(m)
}

// buildTimelineItems 将扁平的消息列表聚合成时间线项，同一回合内连续的思考行与工具调用聚合为一个组。
// expanded 可选传入当前工具组的展开状态：折叠时只生成组头项，展开时再追加组内各节拍
// （一条思考加它后面紧跟的连续工具调用合成一个节拍）。
// 静态构建没有“进行中”的活动组，因此各工具组一律默认折叠。
func buildTimelineItems(messages []Message, expanded ...map[string]bool) []timelineItem {
	var exp map[string]bool
	if len(expanded) > 0 {
		exp = expanded[0]
	}
	return buildTimelineItemsLive(messages, exp, "")
}

// liveToolGroupID 返回运行中会话末尾活动段对应的工具组 ID：仅当会话仍在运行、
// 末尾消息是思考或工具行、且该段足够成组（至少两条且含工具调用）时才返回，否则返回空串。
func liveToolGroupID(messages []Message, running bool) string {
	if !running || len(messages) == 0 {
		return ""
	}
	last := messages[len(messages)-1]
	if !isActivityMessage(last) {
		return ""
	}
	start := len(messages) - 1
	for start > 0 && isActivityMessage(messages[start-1]) && messages[start-1].TurnIndex == last.TurnIndex {
		start--
	}
	run := messages[start:]
	if len(run) < 2 {
		return ""
	}
	for _, m := range run {
		if m.Role == "tool" {
			return "toolgroup-" + run[0].ID
		}
	}
	return ""
}

// buildTimelineItemsLive 与 buildTimelineItems 相同，另外接收当前正在进行的活动组 ID。
// 运行中的活动组同样默认折叠：折叠态在组头下方显示最近几条工具行的只读预览，组头用实时耗时
// 显示进度，避免流式过程中把整组节拍铺满时间轴；用户显式点开或收起后仍以用户的选择为准。
// liveGroupID 保留在签名里供调用方标记进行中的组（渲染期按消息 ID 判定 running）。
// 默认折叠的固定入口：关闭「自动折叠思考内容」时由 rebuildTimelineIncremental 改调
// buildTimelineItemsOpts 并传入 groupsOpenByDefault=true。
func buildTimelineItemsLive(messages []Message, exp map[string]bool, liveGroupID string) []timelineItem {
	return buildTimelineItemsOpts(messages, exp, liveGroupID, false)
}

// buildTimelineItemsOpts 是 buildTimelineItemsLive 的完整实现。groupsOpenByDefault 是用户在未
// 显式设置开合时工具组的默认值：关闭自动折叠思考时传 true，让思考与工具组成的组保持展开。
func buildTimelineItemsOpts(messages []Message, exp map[string]bool, liveGroupID string, groupsOpenByDefault bool) []timelineItem {
	return buildTimelineItemsFiltered(collapseTodoMessages(messages), exp, liveGroupID, groupsOpenByDefault)
}

func buildTimelineItemsFiltered(messages []Message, exp map[string]bool, liveGroupID string, groupsOpenByDefault bool) []timelineItem {
	var items []timelineItem
	n := len(messages)
	for i := 0; i < n; {
		m := messages[i]
		if isHiddenTimelineMessage(m) {
			// 隐藏触发与历史静默消息在构建层直接跳过。
			i++
			continue
		}
		if isActivityMessage(m) {
			start := i
			turnIndex := m.TurnIndex
			for i < n && isActivityMessage(messages[i]) && messages[i].TurnIndex == turnIndex {
				i++
			}
			run := messages[start:i]
			hasTool := false
			for _, rm := range run {
				if rm.Role == "tool" {
					hasTool = true
					break
				}
			}
			if len(run) >= 2 && hasTool {
				groupID := "toolgroup-" + run[0].ID
				// 各组按调用方给的默认值开合（通常折叠；关闭自动折叠思考时默认展开）；
				// 用户显式设置过开合状态时仍以设置为准。
				groupExpanded := toolGroupOpen(exp, groupID, groupsOpenByDefault)
				// 组头项在折叠与展开状态都存在，展开时再追加组内各段行
				items = append(items, timelineItem{
					kind:          timelineItemToolGroup,
					tools:         run,
					id:            groupID,
					turnIndex:     run[len(run)-1].TurnIndex,
					finishedAt:    messageFinishedAt(run[len(run)-1]),
					groupID:       groupID,
					groupExpanded: groupExpanded,
					msgIndex:      start,
				})
				if groupExpanded {
					items = appendGroupSegmentItems(items, run, start, groupID)
				}
			} else {
				// 不足两条或全是思考行：逐条单独展示，保持原有单行渲染
				for j, msg := range run {
					items = append(items, timelineItem{
						kind:       timelineItemSingle,
						msg:        msg,
						id:         msg.ID,
						turnIndex:  msg.TurnIndex,
						finishedAt: messageFinishedAt(msg),
						msgIndex:   start + j,
					})
				}
			}
			continue
		}

		if isSwarmReportMessage(m) {
			// 相邻的 Swarm 汇报消息合并成一个时间线条目：这些胶囊在同一行里横向流式排列，
			// 不再各自占一整行。条目 Key 固定为首条消息 ID，后续到达的汇报只增加行内胶囊。
			start := i
			for i < n && isSwarmReportMessage(messages[i]) {
				i++
			}
			run := messages[start:i]
			items = append(items, timelineItem{
				kind:       timelineItemSwarmReport,
				msg:        run[0],
				reportMsgs: run,
				id:         run[0].ID,
				turnIndex:  run[0].TurnIndex,
				msgIndex:   start,
			})
			continue
		}

		if isPlanDecisionMessage(m) {
			if plan, ok := parsePlanDecisionMessage(m); ok {
				items = append(items, timelineItem{kind: timelineItemResolvedPlan, msg: m,
					id: "resolved-plan-" + plan.ID, turnIndex: m.TurnIndex,
					resolvedPlan: plan, finishedAt: messageFinishedAt(m), msgIndex: i})
			}
			// 无效记录也不显示协议 JSON。
			i++
			continue
		}

		if isQuestionAnswerMessage(m) {
			// 后端写入的问答记录：直接渲染成问答卡片，位置就是这条消息在时间线里的位置，
			// 既不与工具消息合组，也不当成普通系统消息。正文不是合法 JSON 时退回下面的
			// 普通单条渲染（系统消息显示原文）。
			if q, ok := parseQuestionAnswerMessage(m); ok {
				items = append(items, timelineItem{
					kind:             timelineItemAnsweredQuestion,
					msg:              m,
					id:               answeredQuestionCardID(q.ID, m.ID),
					turnIndex:        m.TurnIndex,
					answeredQuestion: q,
					finishedAt:       messageFinishedAt(m),
					msgIndex:         i,
				})
				i++
				continue
			}
		}

		items = append(items, timelineItem{
			kind:       timelineItemSingle,
			msg:        m,
			id:         m.ID,
			turnIndex:  m.TurnIndex,
			finishedAt: messageFinishedAt(m),
			msgIndex:   i,
		})
		i++
	}
	return items
}

// groupSegments 把一组活动消息切成展开后的节拍：一条思考消息加上它后面紧跟的所有连续工具调用为一段，
// 直到下一条思考消息（或组结束）为止；组开头、没有前置思考的工具调用单独成一段（该段的思考为空）。
func groupSegments(run []Message) []timelineGroupSegment {
	segs := make([]timelineGroupSegment, 0, len(run))
	for i := 0; i < len(run); {
		start := i
		if isReasoningMessage(run[i]) {
			i++
		}
		for i < len(run) && !isReasoningMessage(run[i]) && run[i].Role == "tool" {
			i++
		}
		if i == start {
			// 既不是思考行也不是工具行（不应出现）：单独成段，避免死循环
			i++
		}
		segs = append(segs, timelineGroupSegment{start: start, end: i})
	}
	return segs
}

// appendGroupSegmentItems 生成展开后工具组的各节拍条目：每段生成一个 beat 条目，
// 条目里带上段内的思考消息与它触发的工具调用；组开头没有思考的那段只带工具，思考为空。
func appendGroupSegmentItems(items []timelineItem, run []Message, start int, groupID string) []timelineItem {
	segs := groupSegments(run)
	for si, seg := range segs {
		segMsgs := run[seg.start:seg.end]
		var thought Message
		toolStart := seg.start
		if isReasoningMessage(segMsgs[0]) {
			thought = segMsgs[0]
			toolStart = seg.start + 1
		}
		items = append(items, timelineItem{
			kind:       timelineItemToolBeat,
			msg:        thought,
			tools:      run,
			beatTools:  run[toolStart:seg.end],
			id:         groupID + "/beat-" + segMsgs[0].ID,
			turnIndex:  segMsgs[0].TurnIndex,
			finishedAt: messageFinishedAt(segMsgs[len(segMsgs)-1]),
			groupID:    groupID,
			isFirst:    si == 0,
			isLast:     si == len(segs)-1,
			msgIndex:   start + seg.start,
		})
	}
	return items
}

// containsMessageID 判断消息列表里是否有指定 ID 的消息；空 ID 一律为 false。
func containsMessageID(messages []Message, id string) bool {
	if id == "" {
		return false
	}
	for _, m := range messages {
		if m.ID == id {
			return true
		}
	}
	return false
}

// itemHoldsTurnEnd 判断该时间线项是否承载本回合的回合完成块：即它渲染了本回合最后一条助手消息。
// 折叠的工具组头、展开后组内末段节拍、以及单条消息都可以承担；其余行不承担。
func itemHoldsTurnEnd(item timelineItem, lastAssistantPerTurn map[int]string) bool {
	id := lastAssistantPerTurn[item.turnIndex]
	if id == "" {
		return false
	}
	switch item.kind {
	case timelineItemSingle:
		return item.msg.ID == id
	case timelineItemToolGroup:
		// 展开的组不由组头承担：完成块跟在组内可见的最后一行后面
		return !item.groupExpanded && containsMessageID(item.tools, id)
	case timelineItemToolBeat:
		return item.isLast && containsMessageID(item.tools, id)
	case timelineItemTurnWork:
		// 回合折叠项不承载完成块：最终答复留在折叠项之外，完成块跟在它后面。
		return false
	default:
		return false
	}
}

// turnRunSuppressesEnd 判断「仍在进行中的回合」是否要压掉本回合的完成块：
// running 表示会话仍在跑这一轮，latestTurn 是消息里的最新回合号（-1 表示没有消息）。
// 回合进行中每一步（每次模型调用）结束都会先写一次该回合用量，只看用量有值就会在步骤之间
// 的间隙提前显示「已完成」；这里只压最新回合，历史回合在会话运行中也照常显示完成块。
func turnRunSuppressesEnd(running bool, latestTurn, turnIndex int) bool {
	return running && latestTurn >= 0 && turnIndex >= latestTurn
}

// groupTurnEndMessage 从组内消息里挑出代表本回合的助手消息，用作完成块的 Key 与用量来源：
// 取最后一条 assistant 消息，没有时退回最后一条消息。
func groupTurnEndMessage(tools []Message) (Message, bool) {
	if len(tools) == 0 {
		return Message{}, false
	}
	for i := len(tools) - 1; i >= 0; i-- {
		if tools[i].Role == "assistant" {
			return tools[i], true
		}
	}
	return tools[len(tools)-1], true
}

// isGroupRowKind 判断是否为工具组的组头行或组内节拍行。
func isGroupRowKind(kind timelineItemKind) bool {
	switch kind {
	case timelineItemToolGroup, timelineItemToolBeat:
		return true
	default:
		return false
	}
}

// turnWorkFoldID 返回一个回合折叠项的 ID。
func turnWorkFoldID(headMsgID string) string { return turnWorkFoldPrefix + headMsgID }

// turnWorkInnerGap 返回折叠区里第 idx 个条目上方的间距：首项贴近组头，其余按普通列表间距。
func turnWorkInnerGap(idx int) float32 {
	if idx <= 0 {
		return 4
	}
	return 10
}

// itemLastMsgIndex 返回条目覆盖的最后一条消息下标：组头项覆盖整组消息，其余只有一条。
// 未设置 msgLastIndex 时退回 msgIndex，两者取大。
func itemLastMsgIndex(item timelineItem) int {
	last := item.msgLastIndex
	if last < item.msgIndex {
		last = item.msgIndex
	}
	if item.kind == timelineItemToolGroup && len(item.tools) > 1 {
		if end := item.msgIndex + len(item.tools) - 1; end > last {
			last = end
		}
	}
	return last
}

// itemIsActivity 判断条目在回合折叠里算不算「工作过程」：思考、工具组、工具节拍或单条工具调用。
func itemIsActivity(item timelineItem) bool {
	switch item.kind {
	case timelineItemToolGroup, timelineItemToolBeat:
		return true
	case timelineItemSingle:
		return isActivityMessage(item.msg)
	}
	return false
}

// itemTouchesIDs 判断条目涉及的消息里是否有 skipIDs 中的 ID。
func itemTouchesIDs(item timelineItem, ids map[string]bool) bool {
	if item.msg.ID != "" && ids[item.msg.ID] {
		return true
	}
	for _, list := range [][]Message{item.tools, item.beatTools, item.reportMsgs} {
		for _, m := range list {
			if m.ID != "" && ids[m.ID] {
				return true
			}
		}
	}
	return false
}

// findTurnWorkFold 在同一个回合的条目里找出可折叠的一段，并检查全部放行条件：
// 回合条目以用户消息开头、有非空的最终助手正文、正文之前至少有一条活动条目；
// 回合要求不在运行中、不含查找命中或待跳转目标、还没有折叠过。
// lastAssistant 是「回合号 -> 该回合最后一条助手消息 ID」，与渲染时的回合完成块同源。
func findTurnWorkFold(segment []timelineItem, lastAssistant map[int]string, runningTurn int, skipIDs map[string]bool) (timelineTurnWorkFold, bool) {
	foldStart := 1
	head := segment[0]
	if len(segment) < 3 {
		return timelineTurnWorkFold{}, false
	}
	if head.kind != timelineItemSingle || head.msg.Role != "user" {
		return timelineTurnWorkFold{}, false
	}
	turn := head.turnIndex
	// 运行中的回合不折叠：还在写的内容随时会变长，折叠起来反而让人猜不到进度。
	if turn == runningTurn {
		return timelineTurnWorkFold{}, false
	}
	answerID := lastAssistant[turn]
	if answerID == "" {
		return timelineTurnWorkFold{}, false
	}
	answerIdx := -1
	for i := foldStart; i < len(segment); i++ {
		if segment[i].kind == timelineItemTurnWork {
			// 已经折叠过的回合保持原样：折叠必须幂等，增量重建会把旧折叠项拼回来。
			return timelineTurnWorkFold{}, false
		}
		if segment[i].kind == timelineItemSingle && segment[i].msg.ID == answerID {
			answerIdx = i
		}
	}
	if answerIdx <= foldStart {
		return timelineTurnWorkFold{}, false
	}
	// 没有正文的回合不折叠：本来就没有答复可留着看，收起后会只剩一个空壳组头。
	if strings.TrimSpace(segment[answerIdx].msg.Text) == "" {
		return timelineTurnWorkFold{}, false
	}
	hasActivity := false
	for _, it := range segment[foldStart:answerIdx] {
		// 同一段里再出现用户消息，说明这不是一个规整的回合（例如多条提问挤在同一个 turnIndex），
		// 整段对折会把后面的提问一并藏起来，所以直接放弃折叠。
		if it.kind == timelineItemSingle && it.msg.Role == "user" {
			return timelineTurnWorkFold{}, false
		}
		if itemTouchesIDs(it, skipIDs) {
			// 查找命中或待跳转目标被收起来，高亮与跳转就落在看不见的行上。
			return timelineTurnWorkFold{}, false
		}
		if itemIsActivity(it) {
			hasActivity = true
		}
	}
	if !hasActivity {
		return timelineTurnWorkFold{}, false
	}
	return timelineTurnWorkFold{foldStart: foldStart, answerIdx: answerIdx}, true
}

// buildTurnWorkItem 把折叠区里的条目拼成回合折叠项：工具组按当前开合状态重建节拍（折叠区内默认展开），
// 展开折叠区后就能看到每一条命令；组头耗时取回合首尾时间，存在失败工具时置组头警示。
// 折叠区只覆盖最终答复之前的工作过程。
func buildTurnWorkItem(s *chatState, threadID string, segment []timelineItem, fold timelineTurnWorkFold) timelineItem {
	region := segment[fold.foldStart:fold.answerIdx]
	head := segment[0]

	var exp map[string]bool
	s.mu.Lock()
	if len(s.expandedTool) > 0 {
		exp = make(map[string]bool, len(s.expandedTool))
		for k, v := range s.expandedTool {
			exp[k] = v
		}
	}
	s.mu.Unlock()

	// 顶层已经展开过的工具组，它的节拍条目本来就在 region 里；这里若再按开合状态重建一遍，
	// 同一个节拍会出现两次（同一父节点下的 Key 重复，渲染直接 panic）。
	// 所以先记下 region 自带节拍的组，这些组只保留原有节拍，不再重建。
	regionBeatGroups := make(map[string]bool, len(region))
	for _, it := range region {
		if it.kind == timelineItemToolBeat && it.groupID != "" {
			regionBeatGroups[it.groupID] = true
		}
	}

	work := make([]timelineItem, 0, len(region))
	msgs := make([]Message, 0, len(region))
	for _, it := range region {
		switch it.kind {
		case timelineItemToolGroup:
			it.groupExpanded = toolGroupOpen(exp, it.groupID, true)
			work = append(work, it)
			if it.groupExpanded && !regionBeatGroups[it.groupID] {
				work = appendGroupSegmentItems(work, it.tools, it.msgIndex, it.groupID)
			}
			// 组头项的 tools 覆盖整组消息，节拍的 beatTools 都在里面，不重复计数。
			msgs = append(msgs, it.tools...)
		case timelineItemToolBeat:
			work = append(work, it)
		default:
			work = append(work, it)
			// 中途的助手正文不是工具调用，不进组头计数；思考行仍然算「思考 N 次」。
			if it.msg.Role != "assistant" || isReasoningMessage(it.msg) {
				msgs = append(msgs, it.msg)
			}
		}
	}
	attention := false
	for _, m := range msgs {
		if isToolFailed(m) {
			attention = true
			break
		}
	}

	// 折叠区可能一直铺到段尾（静默回合没有答复）：这时没有答复可用算耗时，完成时间取折叠区最后一项。
	elapsed := ""
	var finishedAt time.Time
	if fold.answerIdx < len(segment) {
		start, spanEnd := turnFinishedSpanFor(s, segment[fold.answerIdx].msg)
		if d := spanEnd.Sub(start); d >= time.Second {
			elapsed = formatShortDuration(d)
		}
		finishedAt = spanEnd
	} else if len(region) > 0 {
		finishedAt = region[len(region)-1].finishedAt
	}

	return timelineItem{
		kind:          timelineItemTurnWork,
		id:            turnWorkFoldID(head.msg.ID),
		threadID:      threadID,
		turnIndex:     head.turnIndex,
		tools:         msgs,
		workItems:     work,
		workElapsed:   elapsed,
		workAttention: attention,
		// msgIndex 指向折叠区覆盖的第一条消息（用户消息留在折叠项之外），
		// 这样增量重建的切点与段尾下标都落在折叠项自己身上，不会重复生成用户消息。
		msgIndex:     region[0].msgIndex,
		msgLastIndex: itemLastMsgIndex(region[len(region)-1]),
		finishedAt:   finishedAt,
	}
}

// turnWorkFoldSkipIDs 收集不能收进折叠区的消息 ID：会话内查找的命中项，以及待消费的跨会话跳转目标。
func (s *chatState) turnWorkFoldSkipIDs(threadID string) map[string]bool {
	if s == nil {
		return nil
	}
	var ids map[string]bool
	if st := getChatFindState(s); st != nil {
		query := strings.TrimSpace(st.query)
		if query != "" {
			s.mu.Lock()
			msgs := append([]Message(nil), s.messages...)
			s.mu.Unlock()
			for _, id := range findMessageMatches(msgs, query, st.caseSensitive) {
				if ids == nil {
					ids = make(map[string]bool)
				}
				ids[id] = true
			}
		}
	}
	messageJumpMu.Lock()
	jump, hasJump := messageJumps[s]
	messageJumpMu.Unlock()
	if hasJump && messageJumpApplies(jump, time.Now(), threadID) {
		if ids == nil {
			ids = make(map[string]bool)
		}
		ids[jump.messageID] = true
	}
	return ids
}

// turnWorkLastAnswers 按本份 items 统计每个回合最后一条助手正文消息 ID。
// 不读 s.messages：bee 浮窗会拿自己的消息走同一套重建逻辑，只有 items 才是本次折叠的真实数据。
func turnWorkLastAnswers(items []timelineItem) map[int]string {
	answers := make(map[int]string)
	for _, it := range items {
		if it.kind != timelineItemSingle || it.msg.Role != "assistant" || isReasoningMessage(it.msg) {
			continue
		}
		if strings.TrimSpace(it.msg.Text) == "" {
			continue
		}
		answers[it.turnIndex] = it.msg.ID
	}
	return answers
}

// isSplitSessionItem 判断条目的是不是分割线消息：分割线是界面上的界线，
// 只作为独立一行渲染，永远不进回合折叠区。
func isSplitSessionItem(item timelineItem) bool {
	return item.msg.Kind == splitSessionKind
}

// isContextCompactedItem 判断条目的是不是「上下文已压缩」边界消息：与分割线同一定位，
// 只作为独立一行渲染，不进回合折叠区。
func isContextCompactedItem(item timelineItem) bool {
	return item.msg.Kind == contextCompactedKind
}

// isTimelineBoundaryItem 判断条目是不是时间线上的界线（分割线、压缩边界）：
// 它们自己另起一段，既不会被收进折叠区，也不会把界线藏起来。
func isTimelineBoundaryItem(item timelineItem) bool {
	return isSplitSessionItem(item) || isContextCompactedItem(item)
}

// inlineRunningTurnHeader 把运行态折叠头摊回它收起的过程条目，供下一次重建复用；
// 已结束的折叠项不是运行态，原样保留。运行态头固定插在用户消息之后，所以只看 segment[1]。
func inlineRunningTurnHeader(segment []timelineItem) []timelineItem {
	if len(segment) < 2 || segment[1].kind != timelineItemTurnWork || !segment[1].workRunning {
		return segment
	}
	out := make([]timelineItem, 0, len(segment)-1+len(segment[1].workItems))
	out = append(out, segment[0])
	out = append(out, segment[1].workItems...)
	out = append(out, segment[2:]...)
	return out
}

// runningTurnHeaderItem 为运行中的回合造一个临时折叠头：把用户消息之后、第一条助手正文之前的
// 思考与工具调用收进头里，只留一行「已处理 Ns」实时计时；回合结束后这个头会被摘掉，再由
// applyTurnWorkFold 正常折叠整段。segment 里可能已经有一个上一帧的运行态头，先摊平再重建，
// 保证重复调用得到同样的结果。
func runningTurnHeaderItem(s *chatState, threadID string, segment []timelineItem) (timelineItem, []timelineItem, bool) {
	segment = inlineRunningTurnHeader(segment)
	if len(segment) == 0 || segment[0].kind != timelineItemSingle || segment[0].msg.Role != "user" {
		return timelineItem{}, segment, false
	}
	// 最终答复（或中途正文）出现后就留在头外：还在长出来的正文不能被藏起来。
	stop := len(segment)
	for i := 1; i < len(segment); i++ {
		it := segment[i]
		if it.kind == timelineItemSingle && it.msg.Role == "assistant" && !isReasoningMessage(it.msg) {
			stop = i
			break
		}
	}
	if stop <= 1 {
		return timelineItem{}, segment, false
	}
	region := segment[1:stop]
	msgs := make([]Message, 0, len(region))
	activity := false
	for _, it := range region {
		switch it.kind {
		case timelineItemToolGroup:
			msgs = append(msgs, it.tools...)
			activity = activity || len(it.tools) > 0
		case timelineItemToolBeat:
			msgs = append(msgs, it.beatTools...)
			if isReasoningMessage(it.msg) {
				msgs = append(msgs, it.msg)
			}
			activity = true
		default:
			if it.msg.Role != "assistant" || isReasoningMessage(it.msg) {
				msgs = append(msgs, it.msg)
			}
			activity = activity || itemIsActivity(it)
		}
	}
	if !activity {
		return timelineItem{}, segment, false
	}
	// rest 只保留用户消息与运行态头之外的条目（首条助手正文及其后的内容）：
	// 被头收住的过程条目已经在 workItems 里，不能再追加一次，否则同一条消息会渲染两遍。
	rest := make([]timelineItem, 0, 1+len(segment)-stop)
	rest = append(rest, segment[0])
	rest = append(rest, segment[stop:]...)
	return timelineItem{
		kind:          timelineItemTurnWork,
		id:            turnWorkFoldID(segment[0].msg.ID),
		threadID:      threadID,
		turnIndex:     segment[0].turnIndex,
		workRunning:   true,
		workStartedAt: turnStartTimeFor(s, segment[0].turnIndex),
		tools:         msgs,
		workItems:     append([]timelineItem(nil), region...),
		// 下标覆盖被收起的那段过程（不含用户消息）：增量切点落在过程内部时整段重建，
		// 用户消息留在已复用的前缀里，重建后再在它后面接上新的运行态头。
		msgIndex:     region[0].msgIndex,
		msgLastIndex: itemLastMsgIndex(region[len(region)-1]),
	}, rest, true
}

// applyTurnWorkFold 把已结束回合的思考、工具调用与中途正文收进一个折叠项，最终答复仍留在折叠项之外。
// 只在时间线重建的末尾调用：条目已按消息顺序生成，这里按 turnIndex 把它们分回各个回合。
func applyTurnWorkFold(s *chatState, threadID string, items []timelineItem) []timelineItem {
	if s == nil || len(items) == 0 {
		return items
	}
	runningTurn := -1
	if running, turn := s.runningTurn(threadID); running {
		runningTurn = turn
	}
	lastAssistant := turnWorkLastAnswers(items)
	skipIDs := s.turnWorkFoldSkipIDs(threadID)

	out := make([]timelineItem, 0, len(items))
	for i := 0; i < len(items); {
		end := i + 1
		for end < len(items) && items[end].turnIndex == items[i].turnIndex {
			// 分割线与压缩边界在这里结束当前回合的折叠区间：它们自己另起一段，既不会被收进折叠区，
			// 也不会把上一轮答复之后的界线藏起来（TurnIndex 与上一轮相同时尤其要紧）。
			if isTimelineBoundaryItem(items[end]) {
				break
			}
			end++
		}
		segment := items[i:end]
		isRunningTurn := runningTurn >= 0 && items[i].turnIndex == runningTurn
		// 上一帧的运行态头先摊回过程条目：运行中的回合要按最新消息重建，结束的回合要正常折叠。
		segment = inlineRunningTurnHeader(segment)
		if isRunningTurn {
			// 运行中的回合不折叠整段，只在用户消息之后放一行计时头，收住已经跑过的过程消息。
			// 已经出现的答复或中途正文留在头外，仍在生成的文字不能被藏起来。
			if header, rest, ok := runningTurnHeaderItem(s, threadID, segment); ok {
				out = append(out, rest[0])
				out = append(out, header)
				out = append(out, rest[1:]...)
			} else {
				out = append(out, segment...)
			}
			i = end
			continue
		}
		if fold, ok := findTurnWorkFold(segment, lastAssistant, runningTurn, skipIDs); ok {
			folded := buildTurnWorkItem(s, threadID, segment, fold)
			tail := append([]timelineItem(nil), segment[fold.answerIdx:]...)
			if len(tail) > 0 {
				// 完成信息已经由折叠头承载：负责收尾的答复只保留改动文件卡片，不再画第二条完成行。
				tail[0].workHeaderID = folded.id
			}
			out = append(out, segment[:fold.foldStart]...)
			out = append(out, folded)
			out = append(out, tail...)
		} else {
			out = append(out, segment...)
		}
		i = end
	}
	return out
}

// attachResolvedPlans 把已裁决的计划历史态、派发占位行与子线程胶囊行挂到时间线里。
// chat.go 每帧只调用这一个挂载入口，所以胶囊行也在这里挂载：insertSwarmCapsuleRows 在每条
// 用户消息之前插入代表上一回合的胶囊行（上一轮的胶囊留在它所属回合的末尾），
// appendSwarmCapsuleRow 再在末尾追加代表最新回合的胶囊行。
// 胶囊行的实际内容由 renderSwarmTimelineCapsules 按当前 Swarm 状态实时渲染，没有子线程时整行渲染为空。
// 派发占位行同样由渲染时按当前状态决定是否显示。
// attachResolvedPlans 把已裁决的计划历史态、派发占位行与子线程胶囊行拼进时间线。
// 主会话每帧都要重建一遍：这里按本帧最多要追加的条目数一次分配到位，
// 后面的 slices.Insert 与 append 都落在这块内存里，不再一帧把整表复制四五遍。
func attachResolvedPlans(items []timelineItem, plans []resolvedPlan) []timelineItem {
	return attachPlanAndQuestionItems(items, plans, nil)
}

// attachPlanAndQuestionItems 把已裁决的计划历史态、已答复的提问历史态、派发占位行与子线程胶囊行拼进时间线。
// 问答条目接在同回合的计划卡之后，两者共用同一套插入规则。
func attachPlanAndQuestionItems(items []timelineItem, plans []resolvedPlan, answered []answeredQuestion) []timelineItem {
	extra := len(plans) + len(answered) + 2 // 已裁决计划条 + 已答复提问条 + 派发占位行 + 末尾胶囊行
	if turns := swarmCapsuleRowTurns(items); len(turns) > 1 {
		extra += len(turns) - 1 // 每条用户消息之前插一行胶囊
	}
	out := make([]timelineItem, len(items), len(items)+extra)
	copy(out, items)
	return appendSwarmCapsuleRow(insertSwarmCapsuleRows(appendSwarmDispatchRow(attachAnsweredQuestionItems(attachResolvedPlanItems(out, plans), answered))))
}

// resolvedPlansKey 返回已裁决计划的指纹：拼装结果只在计划集合变化时需要重建。
// 计划卡片只渲染 ID、正文、裁决结果与所属回合，正文对同一个 ID 不会变，裁决时间不参与渲染，
// 所以指纹只要盖住 ID、裁决结果与回合号就够了。
func resolvedPlansKey(plans []resolvedPlan) string {
	if len(plans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range plans {
		b.WriteString(p.ID)
		if p.Approved {
			b.WriteByte('+')
		} else {
			b.WriteByte('-')
		}
		b.WriteString(strconv.Itoa(p.TurnIndex))
		b.WriteString(strconv.Quote(p.Plan))
		b.WriteString(strconv.Quote(p.Feedback))
		b.WriteByte(';')
	}
	return b.String()
}

// timelineAssemblyKey 把计划指纹与问答指纹拼成一个缓存键；两者都为空时返回空串（与旧行为一致）。
func timelineAssemblyKey(plans []resolvedPlan, answered []answeredQuestion) string {
	planKey := resolvedPlansKey(plans)
	answerKey := answeredQuestionsKey(answered)
	switch {
	case answerKey == "":
		return planKey
	case planKey == "":
		return "q|" + answerKey
	default:
		return planKey + "|q|" + answerKey
	}
}

// assembledTimelineItems 返回主会话时间线的拼装结果（含已裁决计划条、派发占位行与各回合胶囊行）。
func (s *chatState) assembledTimelineItems(plans []resolvedPlan) []timelineItem {
	return s.assembledTimelineItemsWithQuestions(plans, nil)
}

// assembledTimelineItemsWithQuestions 在 assembledTimelineItems 的基础上再拼入已答复提问的历史态。
// 结果只取决于会话条目快照、展开状态、已裁决计划/已答复提问与选中会话，几者都没变时直接复用上一帧的结果：
// 静止时每帧重新拼一份几十到几百条的切片（每条 500 多字节）纯属浪费。
// 缓存键同时盖住计划指纹与问答指纹，新增问答记录时时间线自动重建。
func (s *chatState) assembledTimelineItemsWithQuestions(plans []resolvedPlan, answered []answeredQuestion) []timelineItem {
	if s == nil {
		return nil
	}
	key := timelineAssemblyKey(plans, answered)
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	isSteward := selected != "" && selected == s.stewardThreadID()
	var boardRev uint64
	if isSteward {
		boardRev = s.stewardBoardRev()
		key += "|steward|" + strconv.FormatUint(boardRev, 10)
	}

	s.mu.Lock()
	if selected == s.selectedThreadID && (!isSteward || boardRev == s.stewardBoard.rev) && s.timelineAssembledValid &&
		s.timelineAssembledSel == s.selectedThreadID &&
		s.timelineAssembledRev == s.timelineCacheRev &&
		s.timelineAssembledExpand == s.expandedRev &&
		s.timelineAssembledPlans == key {
		items := s.timelineAssembledItems
		s.mu.Unlock()
		return s.withForkOriginItem(items)
	}
	// 拼装期间不持锁（外部消息增量可能正在落地）；拼完再确认这几个输入没变才存缓存，
	// 否则只把本次结果交给调用方，下一帧重建。
	// 版本号必须取时间线缓存自己的版本（timelineCacheRev），不能取 messagesRev：
	// 首帧的时间线条目是按当时的空消息快照拼的，历史消息加载又先递增 messagesRev 再落地消息，
	// 两者抢跑时 base 是旧的、messagesRev 已是新值；按 messagesRev 记账会把这份没有消息的时间线
	// 记成“新版本”的结果，之后消息版本不再变化，缓存就一直命中，时间线永远空白。
	sel := s.selectedThreadID
	baseRev := s.timelineCacheRev
	expandedRev := s.expandedRev
	base := s.timelineCacheItems
	s.mu.Unlock()

	items := attachPlanAndQuestionItems(base, plans, answered)
	if isSteward && sel == selected {
		var taskIndex stewardTaskIndex
		taskIndex, boardRev = s.stewardTaskIndex()
		key = timelineAssemblyKey(plans, answered) + "|steward|" + strconv.FormatUint(boardRev, 10)
		items = insertStewardTaskRows(items, taskIndex)
		items = insertStewardTimeRows(items)
	}

	s.mu.Lock()
	if selected == sel && s.selectedThreadID == sel && s.timelineCacheRev == baseRev && s.expandedRev == expandedRev && (!isSteward || s.stewardBoard.rev == boardRev) {
		s.timelineAssembledSel = sel
		s.timelineAssembledRev = baseRev
		s.timelineAssembledExpand = expandedRev
		s.timelineAssembledPlans = key
		s.timelineAssembledItems = items
		s.timelineAssembledValid = true
	}
	s.mu.Unlock()
	return s.withForkOriginItem(items)
}

// withForkOriginItem 把带入的历史或 fork 来源条插到时间线最顶部。
// 故意不存进拼装缓存：来源字段随会话列表到来，与“会话 ID + 消息版本 + 展开状态 + 计划指纹”
// 这个缓存键不同步，现插现用才能保证来源条不会因缓存命中而缺失。
func (s *chatState) withForkOriginItem(items []timelineItem) []timelineItem {
	if s == nil {
		return items
	}
	s.mu.Lock()
	tid := s.selectedThreadID
	sourceThreadID := ""
	sourceMessageID := ""
	for _, th := range s.threads {
		if th.ID == tid {
			sourceThreadID = th.ForkedFromThreadID
			sourceMessageID = th.ForkedFromMessageID
			break
		}
	}
	s.mu.Unlock()
	if fc, loaded := s.loadedForwardedContext(tid); loaded && strings.TrimSpace(fc.Content) != "" {
		if _, ok := continuationStartInfoFor(s, tid); ok {
			if len(items) > 0 && items[0].kind == timelineItemForwardedContext {
				return items
			}
			return append([]timelineItem{forwardedContextItem(tid)}, items...)
		}
	}
	if tid == "" || sourceThreadID == "" {
		return items
	}
	// 转发会话的上下文卡片已经展示来源，也有「打开原会话」，再插来源条会重复。
	if sourceMessageID == "" {
		return items
	}
	if len(items) > 0 && items[0].kind == timelineItemForkOrigin {
		return items
	}
	return append([]timelineItem{forkOriginItem(tid, sourceThreadID)}, items...)
}

// attachResolvedPlanItems 将已裁决的计划历史态作为时间线条目，插在对应 turn 的末尾；如果难以定位 turn，追加在时间线末尾。
func attachResolvedPlanItems(items []timelineItem, plans []resolvedPlan) []timelineItem {
	if len(plans) == 0 {
		return items
	}
	res := items
	for _, p := range plans {
		planItem := timelineItem{
			kind:         timelineItemResolvedPlan,
			id:           "resolved-plan-" + p.ID,
			turnIndex:    p.TurnIndex,
			resolvedPlan: p,
		}
		if p.TurnIndex >= 0 {
			lastTurnIdx := -1
			for i := len(res) - 1; i >= 0; i-- {
				if res[i].turnIndex == p.TurnIndex {
					lastTurnIdx = i
					break
				}
			}
			if lastTurnIdx >= 0 {
				res = slices.Insert(res, lastTurnIdx+1, planItem)
				continue
			}
		}
		res = append(res, planItem)
	}
	return res
}

// isShowReasoningEnabled 检查当前配置中是否开启了显示思考过程。
func isShowReasoningEnabled(s *chatState) bool {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.ShowReasoningEnabled()
	}
	return true
}

// isAutoCollapseReasoningEnabled 检查通用会话设置中的思考自动折叠开关。
func isAutoCollapseReasoningEnabled(s *chatState) bool {
	st := getChatSettings(s)
	if st == nil {
		return true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cfg.Session.AutoCollapseReasoningEnabled()
}
