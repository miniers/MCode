package steward

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"mcode/internal/attention"
)

// 本文件实现「话题事件直通」：属于某个话题的事件不再交给主会话分诊，直接投进该话题的会话，
// 主会话只收一行摘要。判定只依赖事件字段、话题表与看板三样，不依赖模型猜测。

const (
	// passthroughBuffer 是直通队列深度；满时这一次直通回落到主会话，事件仍在库里，兜底巡检会覆盖。
	passthroughBuffer = 64
	// passthroughMinGap 是同一个话题两次直通之间的最小间隔，窗口内的事件合并成一条。
	passthroughMinGap = 30 * time.Second
	// passthroughIndexTTL 是看板索引缓存的有效期。
	passthroughIndexTTL = 5 * time.Second
	// passthroughDedupeTTL 是直通去重键的保留时长。
	passthroughDedupeTTL = 24 * time.Hour
	// passthroughTick 是直通循环检查合并窗口的间隔。
	passthroughTick = time.Second
	// passthroughTitleMaxRunes 与 passthroughDetailMaxRunes 是直通正文的截断长度。
	passthroughTitleMaxRunes  = 60
	passthroughDetailMaxRunes = 120
	// passthroughNoteMaxRunes 是给主会话那一行摘要的最大字数。
	passthroughNoteMaxRunes = 120
)

// passthroughMode 说明一条事件属于某个话题之后怎么处理。
type passthroughMode int

const (
	// passthroughDeliver 投给话题会话，投递成功后按类型决定是否消解。
	passthroughDeliver passthroughMode = iota
	// passthroughAlready 话题侧已由 goal_review_wake 投过，只从主会话摘除，不重复投递。
	passthroughAlready
)

// passthroughItem 是一条待直通给话题会话的事件。
type passthroughItem struct {
	ThreadID string
	Topic    Topic
	Event    attention.Event
	// Resolve 表示投递成功后由宿主代为消解。
	Resolve bool
}

// passthroughIndex 是从一次话题表与看板快照派生出来的查找表。
type passthroughIndex struct {
	loadedAt time.Time
	// byThread 是话题会话 ID 到话题的映射；含已结项话题，用来挡住话题自身的事件。
	byThread map[string]Topic
	// byGoal 是目标 ID 到话题的映射：目标就是话题建的，一一对应；只收未结项话题。
	byGoal map[string]Topic
	// goalByTask 是任务 ID 到目标 ID 的映射，kanban_task 事件用它。
	goalByTask map[string]string
	// goalByThread 是执行线程（含评审线程）到目标 ID 的映射，thread 事件用它。
	goalByThread map[string]string
}

// passthroughCoveredKinds 是 goal_review_wake 已经投给话题会话的事件类型。
var passthroughCoveredKinds = map[string]bool{
	string(attention.KindKanbanReview):        true,
	string(attention.KindKanbanNeedsApproval): true,
}

// passthroughKeepUnresolved 报告该类型是否必须由用户亲手处理，不能由宿主代为消解。
func passthroughKeepUnresolved(kind string) bool {
	switch attention.Kind(kind) {
	case attention.KindThreadApproval, attention.KindThreadQuota:
		return true
	}
	return passthroughCoveredKinds[kind]
}

// loadPassthroughIndex 返回看板索引；超过 passthroughIndexTTL 时重读一次话题表与看板。
// 读失败时返回上一份缓存（可能为空），按「没有可直通的目标」处理，事件照常进主会话。
// 只由聚合循环调用，缓存字段不需要加锁。
func (r *Runtime) loadPassthroughIndex(ctx context.Context) passthroughIndex {
	now := r.now()
	if !r.passIndex.loadedAt.IsZero() && now.Sub(r.passIndex.loadedAt) < passthroughIndexTTL {
		return r.passIndex
	}

	topics, err := r.Topics(ctx)
	if err != nil {
		log.Printf("steward: 直通读取话题列表失败，沿用上一份索引: %v", err)
		return r.passIndex
	}
	index := passthroughIndex{
		loadedAt:     now,
		byThread:     make(map[string]Topic, len(topics)),
		byGoal:       make(map[string]Topic, len(topics)),
		goalByTask:   make(map[string]string),
		goalByThread: make(map[string]string),
	}
	for _, topic := range topics {
		index.byThread[topic.ThreadID] = topic
		// 已结项话题不再推进，不参与按目标反查；话题自身的事件另由 byThread 挡住。
		if topic.GoalID != "" && !topic.Closed {
			index.byGoal[topic.GoalID] = topic
		}
	}

	// 没有看板时只能识别话题自身与话题派出的 bee，任务与执行会话的映射留空。
	if r.kanban == nil || len(index.byGoal) == 0 {
		return index
	}
	board, err := r.kanban.Board(ctx)
	if err != nil {
		log.Printf("steward: 直通读取看板失败，沿用上一份索引: %v", err)
		return r.passIndex
	}
	for _, view := range board.Tasks {
		task := view.Task
		if task.GoalID == nil {
			continue
		}
		if _, ok := index.byGoal[*task.GoalID]; !ok {
			// 非话题目标的任务不参与直通。
			continue
		}
		index.goalByTask[task.ID] = *task.GoalID
		if task.ThreadID != nil && *task.ThreadID != "" {
			index.goalByThread[*task.ThreadID] = *task.GoalID
		}
	}
	// 执行线程优先，评审线程只补空缺，避免同一个会话同时是两者时被评审线程覆盖。
	for _, view := range board.Tasks {
		task := view.Task
		if task.GoalID == nil || task.ReviewThreadID == nil || *task.ReviewThreadID == "" {
			continue
		}
		if _, ok := index.byGoal[*task.GoalID]; !ok {
			continue
		}
		if _, exists := index.goalByThread[*task.ReviewThreadID]; !exists {
			index.goalByThread[*task.ReviewThreadID] = *task.GoalID
		}
	}
	return index
}

// passthroughTarget 判定一条事件是否属于某个话题。
// 返回待投递条目、处理方式与 ok；ok 为 false 表示这条事件不直通，调用方照旧投给主会话。
func (r *Runtime) passthroughTarget(ctx context.Context, ev attention.Event) (passthroughItem, passthroughMode, bool) {
	var none passthroughItem
	if ev.ResolvedAt != nil || strings.TrimSpace(ev.ID) == "" {
		return none, passthroughDeliver, false
	}

	index := r.loadPassthroughIndex(ctx)
	r.passIndex = index

	var (
		topic Topic
		ok    bool
	)
	switch attention.SubjectType(ev.SubjectType) {
	case attention.SubjectThread:
		// 话题会话自己的事件留在主会话：审批与额度必须由用户亲手处理，
		// 卡住与失败也要让主会话看见（话题自己失败会再产生一条 thread_failed）。
		if _, isTopic := index.byThread[ev.ThreadID]; isTopic {
			return none, passthroughDeliver, false
		}
		var goalID string
		goalID, ok = index.goalByThread[ev.ThreadID]
		if ok {
			topic, ok = index.byGoal[goalID]
		}
	case attention.SubjectKanbanTask:
		var goalID string
		goalID, ok = index.goalByTask[ev.SubjectID]
		if ok {
			topic, ok = index.byGoal[goalID]
		}
	case attention.SubjectBee:
		// bee 事件的 ThreadID 是派发它的 lead 会话，命中话题即「这个话题派出的 bee」。
		topic, ok = index.byThread[ev.ThreadID]
	default:
		// automation_run 等类型与话题没有映射，不做按项目猜测。
		return none, passthroughDeliver, false
	}
	if !ok || topic.Closed || topic.GoalID == "" {
		return none, passthroughDeliver, false
	}

	mode := passthroughDeliver
	if passthroughCoveredKinds[ev.Kind] {
		mode = passthroughAlready
	}
	return passthroughItem{
		ThreadID: topic.ThreadID,
		Topic:    topic,
		Event:    ev,
		Resolve:  !passthroughKeepUnresolved(ev.Kind),
	}, mode, true
}

// routeEvent 在 acceptEvent 通过之后判定直通：属于某个话题时把事件交给直通队列并返回 true，
// 调用方不再把它放进主会话的待处理批次。只由聚合循环调用。
func (r *Runtime) routeEvent(ctx context.Context, ev attention.Event) bool {
	if !r.config().Passthrough() {
		return false
	}

	now := r.now()
	// 去重键为空时退回事件 ID：同一件事不该被直通两次，尤其是保留未消解的类型。
	key := strings.TrimSpace(ev.DedupeKey)
	if key == "" {
		key = "id:" + ev.ID
	}
	for seenKey, seenAt := range r.passSeen {
		if now.Sub(seenAt) > passthroughDedupeTTL {
			delete(r.passSeen, seenKey)
		}
	}
	if _, seen := r.passSeen[key]; seen {
		return true
	}

	item, mode, ok := r.passthroughTarget(ctx, ev)
	if !ok {
		return false
	}
	if mode == passthroughAlready {
		// 已经由 goal_review_wake 投过：只从主会话摘除，不重复投递、也不消解。
		log.Printf("steward: 事件 %s（%s）已由 goal_review_wake 投给话题，不再重复直通", ev.ID, ev.Kind)
		r.passSeen[key] = now
		return true
	}

	select {
	case r.passCh <- item:
		r.passSeen[key] = now
		return true
	default:
		// 队列满时宁可多一次主会话唤醒，也不丢事件。
		log.Printf("steward: 直通队列已满，事件 %s 回落到主会话", ev.ID)
		return false
	}
}

// passthroughLoop 消费直通队列：按话题攒 passthroughMinGap 窗口，到点合并投递。
func (r *Runtime) passthroughLoop(ctx context.Context) {
	defer r.wg.Done()
	tick := time.NewTicker(passthroughTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-r.passCh:
			r.passPending[item.ThreadID] = append(r.passPending[item.ThreadID], item)
		case <-tick.C:
			r.flushPassthrough(ctx, r.now())
		}
	}
}

// flushPassthrough 投递所有已过合并窗口的待发事件；同一话题的多条事件合并成一条。
func (r *Runtime) flushPassthrough(ctx context.Context, now time.Time) {
	type batch struct {
		threadID string
		topic    Topic
		items    []passthroughItem
	}
	var ready []batch
	for threadID, items := range r.passPending {
		if len(items) == 0 {
			delete(r.passPending, threadID)
			continue
		}
		if last, ok := r.passLast[threadID]; ok && now.Sub(last) < passthroughMinGap {
			continue
		}
		ready = append(ready, batch{threadID: threadID, topic: items[0].Topic, items: items})
		delete(r.passPending, threadID)
		r.passLast[threadID] = now
	}
	for _, b := range ready {
		if r.deliverPassthrough(ctx, b.threadID, b.topic, b.items) {
			continue
		}
		// 投递失败：条目放回队列，保留窗口记录，等满 passthroughMinGap 后再重试，
		// 避免持续失败时每秒重试。
		r.passPending[b.threadID] = append(b.items, r.passPending[b.threadID]...)
	}
}

// deliverPassthrough 把一批事件投给话题会话；成功后按条目上的 Resolve 消解，并给主会话留一行顺风车摘要。
// 返回 false 表示这次没投出去，调用方需要把条目留在队列里。
func (r *Runtime) deliverPassthrough(ctx context.Context, threadID string, topic Topic, items []passthroughItem) bool {
	if strings.TrimSpace(threadID) == "" || len(items) == 0 {
		return false
	}
	text := buildPassthroughMessage(topic, items)
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := r.sessions.SendStewardWake(sendCtx, threadID, text)
	cancel()
	if err != nil {
		log.Printf("steward: 向话题 %s 的会话直通事件失败: %v", topic.ThreadID, err)
		return false
	}

	for _, item := range items {
		if !item.Resolve {
			continue
		}
		if err := r.events.Resolve(ctx, item.Event.ID, attention.ResolvedBySteward); err != nil {
			log.Printf("steward: 直通后消解事件 %s 失败: %v", item.Event.ID, err)
		}
	}
	r.enqueueRideNote(passthroughNote(topic, items))
	return true
}

// enqueueRideNote 把一行顺风车通知交给聚合循环：它不触发唤醒，只随下一次事件唤醒顺带送达；
// 缓冲满时记日志丢弃，摘要内容已体现在话题会话里。
func (r *Runtime) enqueueRideNote(note string) {
	select {
	case r.rideNotes <- note:
	default:
		log.Printf("steward: 顺风车通知缓冲已满，丢弃一条直通摘要: %s", note)
	}
}

// buildPassthroughMessage 组装直通消息正文。
func buildPassthroughMessage(topic Topic, items []passthroughItem) string {
	var b strings.Builder
	b.WriteString("这条来自宿主的事件直通，不需要再向用户确认这件事本身。\n")
	fmt.Fprintf(&b, "话题「%s」（%s）\n\n", passthroughTopicTitle(topic), topic.ThreadID)
	for i, item := range items {
		ev := item.Event
		fmt.Fprintf(&b, "%d. 事件 %s（%s）\n", i+1, ev.ID, kindLabel(ev.Kind))
		if title := clipRunes(strings.Join(strings.Fields(ev.Title), " "), passthroughTitleMaxRunes); title != "" {
			fmt.Fprintf(&b, "   标题：%s\n", title)
		}
		if detail := strings.Join(strings.Fields(ev.Detail), " "); detail != "" {
			fmt.Fprintf(&b, "   详情：%s\n", truncateRunes(detail, passthroughDetailMaxRunes))
		}
		if threadID := strings.TrimSpace(ev.ThreadID); threadID != "" {
			fmt.Fprintf(&b, "   相关会话：%s\n", threadID)
		}
	}
	b.WriteString("\n请结合看板与相关会话处理，处理完继续推进本次话题。")
	return b.String()
}

// passthroughNote 生成给主会话的一行摘要。
func passthroughNote(topic Topic, items []passthroughItem) string {
	counts := make(map[string]int, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		label := kindLabel(item.Event.Kind)
		if _, ok := counts[label]; !ok {
			order = append(order, label)
		}
		counts[label]++
	}
	parts := make([]string, 0, len(order))
	for _, label := range order {
		parts = append(parts, fmt.Sprintf("%s %d 条", label, counts[label]))
	}
	note := fmt.Sprintf("已直通话题「%s」：%s", passthroughTopicTitle(topic), strings.Join(parts, "、"))
	return clipRunes(note, passthroughNoteMaxRunes)
}

// passthroughTopicTitle 返回话题标题，空标题回落到「未命名话题」。
func passthroughTopicTitle(topic Topic) string {
	if title := strings.Join(strings.Fields(topic.Title), " "); title != "" {
		return clipRunes(title, passthroughTitleMaxRunes)
	}
	return "未命名话题"
}
