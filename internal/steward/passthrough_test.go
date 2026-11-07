package steward

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mcode/internal/attention"
	"mcode/internal/kanban"
	"mcode/internal/store"
)

// 本文件覆盖话题事件直通：判定、去重、队列回落、合并窗口、投递与消解、主会话摘要。

// ptTopic 构造一条话题；closed 表示已结项，goalID 为空表示还没开工。
func ptTopic(threadID, goalID string, closed bool) Topic {
	return Topic{
		ThreadID:  threadID,
		ProjectID: "proj-1",
		Title:     "重构登录",
		GoalID:    goalID,
		Closed:    closed,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// ptGoal 构造一条总管建的目标，规划会话就是话题会话。
func ptGoal(goalID, threadID string) store.Goal {
	plan := threadID
	return store.Goal{
		ID:           goalID,
		ProjectID:    "proj-1",
		Title:        "重构登录",
		Status:       string(kanban.GoalStatusRunning),
		Owner:        TopicGoalOwner,
		PlanThreadID: &plan,
	}
}

// ptTask 构造一条看板任务；threadID 与 reviewThreadID 为空时不填。
func ptTask(taskID, goalID, threadID, reviewThreadID string) kanban.TaskView {
	task := store.Task{
		ID:        taskID,
		ProjectID: ptStringPtr("proj-1"),
		Title:     "任务 " + taskID,
		Status:    "running",
	}
	if goalID != "" {
		task.GoalID = ptStringPtr(goalID)
	}
	if threadID != "" {
		task.ThreadID = ptStringPtr(threadID)
	}
	if reviewThreadID != "" {
		task.ReviewThreadID = ptStringPtr(reviewThreadID)
	}
	return kanban.TaskView{Task: task}
}

// ptStringPtr 返回字符串指针，供可空字段使用。
func ptStringPtr(s string) *string { return &s }

// ptEvent 构造一条指定来源的事件。
func ptEvent(kind attention.Kind, id, subjectType, subjectID, threadID string) attention.Event {
	return attention.Event{
		ID:          id,
		Kind:        string(kind),
		SubjectType: subjectType,
		SubjectID:   subjectID,
		ProjectID:   "proj-1",
		ThreadID:    threadID,
		Title:       "标题 " + id,
		Detail:      "详情 " + id,
		EnteredAt:   time.Now(),
		DedupeKey:   "dedupe-" + id,
	}
}

// ptRig 构造一个带话题与看板的测试运行时：一条话题、一个目标、一条任务。
func ptRig(t *testing.T, topic Topic, task kanban.TaskView) (*stewardRig, *fakeTopicKanban) {
	t.Helper()
	h, board := newTopicRig(t)
	seedTopic(t, h, topic)
	board.goals = []store.Goal{ptGoal(topic.GoalID, topic.ThreadID)}
	if task.Task.ID != "" {
		board.tasks = []kanban.TaskView{task}
	}
	return h, board
}

// mustIndex 调一次索引加载并写回缓存，模拟 routeEvent 的行为。
func (h *stewardRig) mustIndex() passthroughIndex {
	index := h.rt.loadPassthroughIndex(context.Background())
	h.rt.passIndex = index
	return index
}

// TestLoadPassthroughIndex_BuildsThreeTables 验证话题表与看板派生出三张查找表。
func TestLoadPassthroughIndex_BuildsThreeTables(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, board := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", "th-review"))
	board.tasks = append(board.tasks, ptTask("task-other", "goal-other", "th-other", ""))

	index := h.mustIndex()
	if got := index.byThread["th-topic"].ThreadID; got != "th-topic" {
		t.Fatalf("byThread 未收录话题: %+v", index.byThread)
	}
	if got := index.byGoal["goal-1"].ThreadID; got != "th-topic" {
		t.Fatalf("byGoal 未收录目标: %+v", index.byGoal)
	}
	if got := index.goalByTask["task-1"]; got != "goal-1" {
		t.Fatalf("goalByTask 未收录任务: %+v", index.goalByTask)
	}
	if got := index.goalByThread["th-exec"]; got != "goal-1" {
		t.Fatalf("goalByThread 未收录执行线程: %+v", index.goalByThread)
	}
	if got := index.goalByThread["th-review"]; got != "goal-1" {
		t.Fatalf("goalByThread 未收录评审线程: %+v", index.goalByThread)
	}
	if _, ok := index.goalByTask["task-other"]; ok {
		t.Fatalf("非话题目标的任务不应进索引: %+v", index.goalByTask)
	}
}

// TestLoadPassthroughIndex_ReusesCacheWithinTTL 验证 TTL 内不重读看板。
func TestLoadPassthroughIndex_ReusesCacheWithinTTL(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, board := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))
	h.mustIndex()

	board.tasks = nil
	h.clk.Advance(time.Second)
	index := h.mustIndex()
	if got := index.goalByTask["task-1"]; got != "goal-1" {
		t.Fatalf("TTL 内应复用缓存，实际索引: %+v", index.goalByTask)
	}
}

// TestLoadPassthroughIndex_BoardErrorKeepsPrevious 验证看板读失败时沿用上一份索引。
func TestLoadPassthroughIndex_BoardErrorKeepsPrevious(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, board := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))
	first := h.mustIndex()

	board.boardErr = errors.New("看板不可用")
	h.clk.Advance(passthroughIndexTTL + time.Second)
	index := h.mustIndex()
	if got := index.goalByTask["task-1"]; got != "goal-1" {
		t.Fatalf("读失败时应沿用上一份索引，实际索引: %+v", index.goalByTask)
	}
	if !index.loadedAt.Equal(first.loadedAt) {
		t.Fatalf("读失败时不应刷新 loadedAt: 之前 %v，之后 %v", first.loadedAt, index.loadedAt)
	}
}

// TestPassthroughTarget_TopicOwnThreadNotRouted 验证话题会话自己的事件留在主会话。
func TestPassthroughTarget_TopicOwnThreadNotRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindThreadFailed, "ev-1", string(attention.SubjectThread), "th-topic", "th-topic")
	if _, _, ok := h.rt.passthroughTarget(context.Background(), event); ok {
		t.Fatal("话题自身的事件不应直通")
	}
}

// TestPassthroughTarget_TaskThreadRouted 验证任务执行会话的事件直通给话题。
func TestPassthroughTarget_TaskThreadRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindThreadFailed, "ev-1", string(attention.SubjectThread), "th-exec", "th-exec")
	item, mode, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughDeliver {
		t.Fatalf("任务执行会话的事件应直通，得到 ok=%v mode=%v", ok, mode)
	}
	if item.ThreadID != "th-topic" || !item.Resolve {
		t.Fatalf("直通目标或消解标记不正确: %+v", item)
	}
}

// TestPassthroughTarget_ReviewThreadRouted 验证评审会话的事件也直通给话题。
func TestPassthroughTarget_ReviewThreadRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", "th-review"))

	event := ptEvent(attention.KindThreadStalled, "ev-1", string(attention.SubjectThread), "th-review", "th-review")
	item, _, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || item.ThreadID != "th-topic" {
		t.Fatalf("评审会话的事件应直通给话题，得到 ok=%v item=%+v", ok, item)
	}
}

// TestPassthroughTarget_KanbanFailedRouted 验证 kanban_failed 直通并投递。
func TestPassthroughTarget_KanbanFailedRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	item, mode, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughDeliver || !item.Resolve {
		t.Fatalf("kanban_failed 应直通并消解，得到 ok=%v mode=%v item=%+v", ok, mode, item)
	}
}

// TestPassthroughTarget_KanbanReviewSuppressedOnly 验证待复核只从主会话摘除，不重复投递。
func TestPassthroughTarget_KanbanReviewSuppressedOnly(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindKanbanReview, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	item, mode, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughAlready {
		t.Fatalf("kanban_review 应只在主会话摘除，得到 ok=%v mode=%v", ok, mode)
	}
	if item.Resolve {
		t.Fatal("kanban_review 不应由宿主代为消解")
	}

	event = ptEvent(attention.KindKanbanNeedsApproval, "ev-2", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	_, mode, ok = h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughAlready {
		t.Fatalf("kanban_needs_approval 应只在主会话摘除，得到 ok=%v mode=%v", ok, mode)
	}
}

// TestPassthroughTarget_BeeLeadRouted 验证话题派出的 bee 失败直通给话题。
func TestPassthroughTarget_BeeLeadRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindBeeFailed, "ev-1", string(attention.SubjectBee), "bee-1", "th-topic")
	item, mode, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughDeliver || item.ThreadID != "th-topic" || !item.Resolve {
		t.Fatalf("话题派出的 bee_failed 应直通并消解，得到 ok=%v mode=%v item=%+v", ok, mode, item)
	}
}

// TestPassthroughThreadNoProgressToTopic 验证任务执行会话的无进展事件直通给话题并消解。
func TestPassthroughThreadNoProgressToTopic(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindThreadNoProgress, "ev-1", string(attention.SubjectThread), "th-exec", "th-exec")
	item, mode, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughDeliver {
		t.Fatalf("任务执行会话的无进展事件应直通，得到 ok=%v mode=%v", ok, mode)
	}
	if item.ThreadID != "th-topic" || !item.Resolve {
		t.Fatalf("直通目标或消解标记不正确: %+v", item)
	}
}

// TestPassthroughBeeNoProgressToTopic 验证话题派出的 bee 无进展事件经 lead 会话直通给话题并消解。
func TestPassthroughBeeNoProgressToTopic(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindThreadNoProgress, "ev-1", string(attention.SubjectBee), "bee-1", "th-topic")
	item, mode, ok := h.rt.passthroughTarget(context.Background(), event)
	if !ok || mode != passthroughDeliver || item.ThreadID != "th-topic" || !item.Resolve {
		t.Fatalf("话题派出的 bee 无进展事件应直通并消解，得到 ok=%v mode=%v item=%+v", ok, mode, item)
	}
}

// TestPassthroughTarget_AutomationNotRouted 验证 automation 事件不按项目猜话题。
func TestPassthroughTarget_AutomationNotRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindAutomationFailed, "ev-1", string(attention.SubjectAutomationRun), "run-1", "th-topic")
	if _, _, ok := h.rt.passthroughTarget(context.Background(), event); ok {
		t.Fatal("automation 事件不应直通")
	}
}

// TestPassthroughTarget_ClosedTopicNotRouted 验证已结项话题不直通。
func TestPassthroughTarget_ClosedTopicNotRouted(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", true)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	if _, _, ok := h.rt.passthroughTarget(context.Background(), event); ok {
		t.Fatal("已结项话题的事件不应直通")
	}

	event = ptEvent(attention.KindBeeFailed, "ev-2", string(attention.SubjectBee), "bee-1", "th-topic")
	if _, _, ok := h.rt.passthroughTarget(context.Background(), event); ok {
		t.Fatal("已结项话题派出的 bee 事件不应直通")
	}
}

// TestPassthroughTarget_DraftingTopicNotRouted 验证未开工话题不直通。
func TestPassthroughTarget_DraftingTopicNotRouted(t *testing.T) {
	topic := ptTopic("th-topic", "", false)
	h, board := newTopicRig(t)
	seedTopic(t, h, topic)
	board.goals = nil
	board.tasks = nil

	event := ptEvent(attention.KindBeeFailed, "ev-1", string(attention.SubjectBee), "bee-1", "th-topic")
	if _, _, ok := h.rt.passthroughTarget(context.Background(), event); ok {
		t.Fatal("未开工话题的事件不应直通")
	}
}

// TestRouteEvent_DisabledByConfig 验证关掉开关后事件仍走原来的主会话路径。
func TestRouteEvent_DisabledByConfig(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))
	cfg := enabledConfig()
	cfg.PassthroughOff = true
	h.cfg.set(cfg)

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	if h.rt.routeEvent(context.Background(), event) {
		t.Fatal("关闭直通后 routeEvent 应返回 false")
	}
	if len(h.rt.passCh) != 0 {
		t.Fatalf("关闭直通后不应入队，队列长度 %d", len(h.rt.passCh))
	}
}

// TestRouteEvent_DedupesByDedupeKey 验证同一条去重键只直通一次。
func TestRouteEvent_DedupesByDedupeKey(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	first := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	second := first
	second.ID = "ev-2"
	second.DedupeKey = first.DedupeKey

	if !h.rt.routeEvent(context.Background(), first) {
		t.Fatal("第一条事件应直通")
	}
	if !h.rt.routeEvent(context.Background(), second) {
		t.Fatal("命中去重键时应直接返回 true")
	}
	if len(h.rt.passCh) != 1 {
		t.Fatalf("同一条去重键只应入队一次，实际 %d 条", len(h.rt.passCh))
	}
}

// TestRouteEvent_QueueFullFallsBackToMainSession 验证队列满时回落主会话。
func TestRouteEvent_QueueFullFallsBackToMainSession(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))
	for i := 0; i < passthroughBuffer; i++ {
		h.rt.passCh <- passthroughItem{}
	}

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	if h.rt.routeEvent(context.Background(), event) {
		t.Fatal("队列满时 routeEvent 应返回 false，让事件回到主会话")
	}
}

// TestRouteEvent_AlreadyModeDoesNotEnqueue 验证已由 goal_review_wake 投过的类型不重复入队。
func TestRouteEvent_AlreadyModeDoesNotEnqueue(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindKanbanReview, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	if !h.rt.routeEvent(context.Background(), event) {
		t.Fatal("kanban_review 应从主会话摘除")
	}
	if len(h.rt.passCh) != 0 {
		t.Fatalf("kanban_review 不应入直通队列，实际 %d 条", len(h.rt.passCh))
	}
}

// TestPassthroughLoop_CoalescesWithinWindow 验证同一话题 30 秒内的事件合并成一条。
func TestPassthroughLoop_CoalescesWithinWindow(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	items := []passthroughItem{
		{ThreadID: topic.ThreadID, Topic: topic, Event: ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")},
		{ThreadID: topic.ThreadID, Topic: topic, Event: ptEvent(attention.KindThreadStalled, "ev-2", string(attention.SubjectThread), "th-exec", "th-exec")},
	}
	h.rt.passPending[topic.ThreadID] = items
	now := h.clk.Now()
	h.rt.flushPassthrough(context.Background(), now)

	wakes := h.sessions.wakeMessages()
	if len(wakes) != 1 {
		t.Fatalf("窗口内应合并成一条投递，实际 %d 条", len(wakes))
	}
	if !strings.Contains(wakes[0].Text, "ev-1") || !strings.Contains(wakes[0].Text, "ev-2") {
		t.Fatalf("合并消息缺少事件: %s", wakes[0].Text)
	}

	// 窗口内的后续事件继续攒着，不足 30 秒不投。
	h.rt.passPending[topic.ThreadID] = items[:1]
	h.rt.flushPassthrough(context.Background(), now.Add(10*time.Second))
	if got := len(h.sessions.wakeMessages()); got != 1 {
		t.Fatalf("窗口内不应再投递，实际 %d 条", got)
	}
	// 超过 30 秒后再投一次。
	h.rt.flushPassthrough(context.Background(), now.Add(passthroughMinGap+time.Second))
	if got := len(h.sessions.wakeMessages()); got != 2 {
		t.Fatalf("超过合并窗口后应再投一条，实际 %d 条", got)
	}
}

// TestDeliverPassthrough_ResolvesStewardKinds 验证失败类事件投递后由宿主消解。
func TestDeliverPassthrough_ResolvesStewardKinds(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	kinds := []attention.Kind{attention.KindKanbanFailed, attention.KindThreadFailed, attention.KindBeeFailed}
	items := make([]passthroughItem, 0, len(kinds))
	for i, kind := range kinds {
		event := ptEvent(kind, "ev-"+string(rune('1'+i)), string(attention.SubjectThread), "th-exec", "th-exec")
		items = append(items, passthroughItem{ThreadID: topic.ThreadID, Topic: topic, Event: event, Resolve: !passthroughKeepUnresolved(string(kind))})
	}
	if !h.rt.deliverPassthrough(context.Background(), topic.ThreadID, topic, items) {
		t.Fatal("投递应成功")
	}

	calls := h.events.resolveCalls()
	if len(calls) != len(kinds) {
		t.Fatalf("应消解 %d 条事件，实际 %d 条", len(kinds), len(calls))
	}
	for _, call := range calls {
		if call.By != attention.ResolvedBySteward {
			t.Fatalf("消解者应为 steward，实际 %q", call.By)
		}
	}
}

// TestDeliverPassthrough_KeepsOwnerActionKindsUnresolved 验证审批、额度、待复核类投递后保持未消解。
func TestDeliverPassthrough_KeepsOwnerActionKindsUnresolved(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	kinds := []attention.Kind{
		attention.KindThreadApproval,
		attention.KindThreadQuota,
		attention.KindKanbanNeedsApproval,
		attention.KindKanbanReview,
	}
	items := make([]passthroughItem, 0, len(kinds))
	for i, kind := range kinds {
		event := ptEvent(kind, "ev-"+string(rune('1'+i)), string(attention.SubjectThread), "th-exec", "th-exec")
		items = append(items, passthroughItem{ThreadID: topic.ThreadID, Topic: topic, Event: event, Resolve: !passthroughKeepUnresolved(string(kind))})
	}
	if !h.rt.deliverPassthrough(context.Background(), topic.ThreadID, topic, items) {
		t.Fatal("投递应成功")
	}
	if calls := h.events.resolveCalls(); len(calls) != 0 {
		t.Fatalf("需要用户亲手处理的事件不应被消解，实际消解 %d 条", len(calls))
	}
}

// TestDeliverPassthrough_AddsNoteForMainSession 验证直通后给主会话排一行顺风车摘要（不直接唤醒）。
func TestDeliverPassthrough_AddsNoteForMainSession(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	item := passthroughItem{ThreadID: topic.ThreadID, Topic: topic, Event: event, Resolve: true}
	if !h.rt.deliverPassthrough(context.Background(), topic.ThreadID, topic, []passthroughItem{item}) {
		t.Fatal("投递应成功")
	}

	select {
	case note := <-h.rt.rideNotes:
		if !strings.Contains(note, "已直通话题「重构登录」") || !strings.Contains(note, "看板任务失败 1 条") {
			t.Fatalf("摘要内容不正确: %s", note)
		}
	default:
		t.Fatal("应给主会话排一行顺风车摘要")
	}
	select {
	case note := <-h.rt.notes:
		t.Fatalf("直通摘要不应进入会触发唤醒的通知队列: %s", note)
	default:
	}
}

// TestDeliverPassthrough_SendFailureKeepsPending 验证投递失败后条目与窗口记录都保留，
// 等满 passthroughMinGap 才重试，避免持续失败时每秒重试。
func TestDeliverPassthrough_SendFailureKeepsPending(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))
	h.sessions.sendErr = errors.New("投递失败")

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	items := []passthroughItem{{ThreadID: topic.ThreadID, Topic: topic, Event: event, Resolve: true}}
	h.rt.passPending[topic.ThreadID] = items
	now := h.clk.Now()
	h.rt.flushPassthrough(context.Background(), now)

	if got := len(h.rt.passPending[topic.ThreadID]); got != 1 {
		t.Fatalf("投递失败后条目应保留，实际 %d 条", got)
	}
	if _, ok := h.rt.passLast[topic.ThreadID]; !ok {
		t.Fatal("投递失败后应保留窗口记录，等满合并窗口再重试")
	}
	if calls := h.events.resolveCalls(); len(calls) != 0 {
		t.Fatalf("投递失败不应消解，实际消解 %d 条", len(calls))
	}

	// 先撤掉故障，才能从发送记录看出窗口未满时有没有重试。
	h.sessions.sendErr = nil
	h.rt.flushPassthrough(context.Background(), now)
	if got := len(h.sessions.messages()); got != 0 {
		t.Fatalf("窗口未满不应重试投递，实际发送 %d 条", got)
	}
	if got := len(h.rt.passPending[topic.ThreadID]); got != 1 {
		t.Fatalf("窗口未满条目应继续保留，实际 %d 条", got)
	}

	// 等满 passthroughMinGap 后再 flush 应重试并投递成功。
	h.rt.flushPassthrough(context.Background(), now.Add(passthroughMinGap))
	if got := len(h.sessions.messages()); got != 1 {
		t.Fatalf("窗口到期应重试投递，实际发送 %d 条", got)
	}
	if got := len(h.rt.passPending[topic.ThreadID]); got != 0 {
		t.Fatalf("投递成功后条目应清空，实际 %d 条", got)
	}
}

// TestBuildPassthroughMessage_TruncatesDetail 验证正文带事件 ID 与类型，并截断过长详情。
func TestBuildPassthroughMessage_TruncatesDetail(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	event.Detail = strings.Repeat("长", 300)
	text := buildPassthroughMessage(topic, []passthroughItem{{ThreadID: topic.ThreadID, Topic: topic, Event: event}})

	if !strings.Contains(text, "不需要再向用户确认这件事本身") {
		t.Fatalf("正文缺少直通说明: %s", text)
	}
	if !strings.Contains(text, "ev-1") || !strings.Contains(text, kindLabel(string(attention.KindKanbanFailed))) {
		t.Fatalf("正文缺少事件 ID 或类型: %s", text)
	}
	if strings.Contains(text, strings.Repeat("长", passthroughDetailMaxRunes+1)) {
		t.Fatal("正文详情未按上限截断")
	}
	if !strings.Contains(text, "…") {
		t.Fatal("被截断的详情应带省略号")
	}
}

// TestPassthroughNote_ClipsAndCounts 验证摘要按类型计数并限制长度。
func TestPassthroughNote_ClipsAndCounts(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	topic.Title = strings.Repeat("长标题", 40)
	items := []passthroughItem{
		{Topic: topic, Event: ptEvent(attention.KindKanbanFailed, "ev-1", "", "", "")},
		{Topic: topic, Event: ptEvent(attention.KindKanbanFailed, "ev-2", "", "", "")},
		{Topic: topic, Event: ptEvent(attention.KindThreadStalled, "ev-3", "", "", "")},
	}
	note := passthroughNote(topic, items)
	if !strings.Contains(note, "看板任务失败 2 条") || !strings.Contains(note, "会话卡住 1 条") {
		t.Fatalf("摘要计数不正确: %s", note)
	}
	if utf8.RuneCountInString(note) > passthroughNoteMaxRunes {
		t.Fatalf("摘要超长: %d 个字符", utf8.RuneCountInString(note))
	}
}

// TestLoop_RoutedEventNotInMainBatch 端到端验证话题事件只投给话题会话，不进主会话批次。
func TestLoop_RoutedEventNotInMainBatch(t *testing.T) {
	topic := ptTopic("th-topic", "goal-1", false)
	h, _ := ptRig(t, topic, ptTask("task-1", "goal-1", "th-exec", ""))
	h.startSteward()

	event := ptEvent(attention.KindKanbanFailed, "ev-1", string(attention.SubjectKanbanTask), "task-1", "th-exec")
	h.events.emit(event)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.sessions.wakeMessages()) > 0 && len(h.events.resolveCalls()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	wakes := h.sessions.wakeMessages()
	if len(wakes) == 0 {
		t.Fatal("话题会话应在直通窗口后收到事件")
	}
	for _, wake := range wakes {
		if wake.ThreadID != topic.ThreadID {
			t.Fatalf("主会话不应收到被直通的事件，实际投给 %s: %s", wake.ThreadID, wake.Text)
		}
	}
	if len(h.events.resolveCalls()) != 1 {
		t.Fatalf("直通投递成功后应消解 1 条事件，实际 %d 条", len(h.events.resolveCalls()))
	}
}
