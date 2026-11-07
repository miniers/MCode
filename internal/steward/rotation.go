package steward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mcode/internal/attention"
	"mcode/internal/kanban"
	"mcode/internal/store"
)

// 本文件提供主会话「换新」的判定与前言：总管主会话长期驻留，上下文会越攒越长，
// 空闲够久或上下文够大时由 remote 侧开一条新会话接班。接班会话不带旧对话，
// 靠账本与话题索引延续记忆；身份段由装饰器每轮注入，最近几条对话由 remote 侧追加，都不在前言里。

// 换新阈值的默认值；Deps.RotateIdle 与 Deps.RotateContextTokens 可覆盖，
// config.StewardConfig 的 RotateIdleMinutes 与 RotateContextTokens 优先级更高（运行时现读）。
const (
	// DefaultRotateIdle 是默认的空闲阈值：0 表示空闲换新默认关闭，只有配置了正数才启用。
	// 空闲一小时后就把上下文丢掉太激进，因此默认不按空闲换新，只按上下文大小换新。
	DefaultRotateIdle time.Duration = 0
	// DefaultRotateContextTokens 是默认的上下文阈值（按 token 计）。
	DefaultRotateContextTokens int64 = 120000
	// rotationBriefMaxRunes 是话题索引里 brief 摘要的长度上限。
	rotationBriefMaxRunes = 80
	// rotationEventsMax 是换新前言里列出的未消解事件条数上限。
	rotationEventsMax = 5
	// RotationBeesMax 是换新前言里列出的最近子代理条数上限；app 层构造子代理列表时也用它截断。
	RotationBeesMax = 5
	// rotationEventTitleRunes / rotationEventDetailRunes 是事件标题与详情的截断长度。
	rotationEventTitleRunes  = 60
	rotationEventDetailRunes = 80
	// rotationBeeTaskRunes 是子代理任务说明的截断长度。
	rotationBeeTaskRunes = 60
)

// RotationBee 是换新前言里「最近派出的子代理」一行所需的最少信息。
type RotationBee struct {
	// Name 是子代理名字。
	Name string
	// Task 是派发时给它的任务说明。
	Task string
	// Status 是状态的中文说明，如已完成、运行中、失败。
	Status string
	// LinesAdded / LinesDeleted 是它改动的增删行数，取不到时为 0。
	LinesAdded   int
	LinesDeleted int
}

// rotationIntro 是换新前言的开头说明。
const rotationIntro = "这是换新后的主会话：之前的对话已经归档，不在你的上下文里。" +
	"下面是账本与话题索引，宿主还会附上最近几条对话；需要更早的细节时用 thread_search 或 thread_fetch_messages 查归档。"

// splitIntro 是用户主动分割会话后的开头说明：分割线以上的内容不进上下文，
// 宿主也不会再附上最近几条对话。
const splitIntro = "这是分割后的新会话：用户主动分割了对话，分割线以上的内容已归档，不在你的上下文里，也不要假装记得。" +
	"下面是账本、话题索引和待处理事件，宿主不会再附上最近几条对话。"

// ShouldRotate 报告 threadID 指向的会话是否该换新：只对总管主会话（steward.thread_id 指向的会话）
// 可能返回 true。空闲换新只在阈值大于 0、idle 大于 0 且达到阈值时生效（默认关闭），
// 上下文 token 数达到上下文阈值时总是换新。
func (r *Runtime) ShouldRotate(threadID string, idle time.Duration, contextTokens int64) bool {
	if !r.isStewardThread(threadID) {
		return false
	}
	if threshold := r.rotateIdleThreshold(); threshold > 0 && idle > 0 && idle >= threshold {
		return true
	}
	return contextTokens >= r.rotateTokensThreshold()
}

// rotateIdleThreshold 返回当前使用的空闲阈值：运行时现读的配置优先（正数分钟数），
// 其次 Deps.RotateIdle；配置与 Deps 都不是正数时返回 0，表示关闭空闲换新。
func (r *Runtime) rotateIdleThreshold() time.Duration {
	if minutes := r.config().RotateIdleMinutes; minutes > 0 {
		return time.Duration(minutes) * time.Minute
	}
	if r.rotateIdle > 0 {
		return r.rotateIdle
	}
	return 0
}

// rotateTokensThreshold 返回当前使用的上下文阈值：运行时现读的配置优先（正数），
// 其次 Deps.RotateContextTokens，都为 0 或负数时用 DefaultRotateContextTokens。
func (r *Runtime) rotateTokensThreshold() int64 {
	if tokens := r.config().RotateContextTokens; tokens > 0 {
		return tokens
	}
	if r.rotateTokens > 0 {
		return r.rotateTokens
	}
	return DefaultRotateContextTokens
}

// RotationPreamble 返回换新后主会话的中文前言：换新说明、交接摘要、账本、未结项话题的索引、
// 未消解事件与最近派出的子代理。各节读不到时只在对应位置写明读取失败，前言其余部分照常给出。
// 全部是本地读取，不调模型：换新钩子只有 2 秒时限。
func (r *Runtime) RotationPreamble(ctx context.Context) string {
	return r.rotationPreamble(ctx, rotationIntro)
}

// SplitPreamble 返回用户主动分割后的中文前言：说明换成分割版，其余段落与换新前言完全共用。
func (r *Runtime) SplitPreamble(ctx context.Context) string {
	return r.rotationPreamble(ctx, splitIntro)
}

// HandoffCounts 是分割线文案里的三个数字：账本总条数、未结项话题数、未消解事件数。
type HandoffCounts struct {
	Ledger  int
	Topics  int
	Pending int
}

// HandoffCounts 统计本会话将带走的长期信息条数，口径与换新前言的交接摘要行一致。
// 三项都读成功才返回 ok=true：分割线是对用户的断言，任何一项读不到就不写计数，
// 避免出现半真半假的数字。只做本地读取、不调模型，调用方给 2 秒预算。
func (r *Runtime) HandoffCounts(ctx context.Context) (HandoffCounts, bool) {
	entries, err := r.Ledger(ctx)
	if err != nil {
		return HandoffCounts{}, false
	}
	topicList, err := r.Topics(ctx)
	if err != nil {
		return HandoffCounts{}, false
	}
	if r.events == nil {
		return HandoffCounts{}, false
	}
	pending, err := r.events.UnresolvedCount(ctx)
	if err != nil {
		return HandoffCounts{}, false
	}
	return HandoffCounts{Ledger: len(entries), Topics: countOpenTopics(topicList), Pending: pending}, true
}

// countOpenTopics 统计未结项话题；换新前言与分割线计数共用同一口径。
func countOpenTopics(topicList []Topic) int {
	open := 0
	for _, topic := range topicList {
		if !topic.Closed {
			open++
		}
	}
	return open
}

// rotationPreamble 按给定开头说明拼出前言正文；换新与分割只差这一段。
func (r *Runtime) rotationPreamble(ctx context.Context, intro string) string {
	// 账本条目同时用于交接摘要的计数，所以先取条目再自己渲染，渲染参数与原 RenderLedger 一致。
	entries, ledgerErr := r.Ledger(ctx)
	topicList, topicsErr := r.Topics(ctx)
	if topicsErr != nil {
		topicList = nil
	}
	var events []attention.Event
	var eventsErr error
	if r.events == nil {
		eventsErr = fmt.Errorf("事件能力未接线")
	} else {
		events, eventsErr = r.events.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true, Limit: rotationEventsMax})
	}
	var bees []RotationBee
	if r.recentBees != nil {
		bees = r.recentBees(r.ThreadID())
		if len(bees) > RotationBeesMax {
			bees = bees[:RotationBeesMax]
		}
	}
	openTopics := countOpenTopics(topicList)

	// 摘要行的未消解事件数要用总数：List 只取 rotationEventsMax 条，超过时本页条数会偏小。
	// 计数失败时退回本页条数，前言其余部分照常给出。
	unresolvedEvents := len(events)
	if r.events != nil {
		if count, err := r.events.UnresolvedCount(ctx); err == nil {
			unresolvedEvents = count
		}
	}
	var b strings.Builder
	b.WriteString(intro)
	b.WriteString("\n\n## 交接摘要\n")
	inProgress, decisions, pendingUser := ledgerSectionCounts(entries)
	fmt.Fprintf(&b, "- 账本 %d 条（进行中 %d、已定决策 %d、等用户确认 %d）、未结项话题 %d 个、未消解事件 %d 条、最近子代理 %d 只",
		len(entries), inProgress, decisions, pendingUser, openTopics, unresolvedEvents, len(bees))
	b.WriteString("\n\n## 账本\n")
	if ledgerErr != nil {
		fmt.Fprintf(&b, "（账本读取失败：%v）", ledgerErr)
	} else {
		b.WriteString(renderLedger(entries, ledgerRenderMaxRunes))
	}
	b.WriteString("\n\n## 话题索引\n")
	b.WriteString(r.renderTopicIndex(ctx))
	b.WriteString("\n\n## 未消解事件\n")
	b.WriteString(renderHandoffEvents(events, eventsErr))
	if section := renderHandoffBees(bees); section != "" {
		b.WriteString("\n\n## 最近派出的子代理\n")
		b.WriteString(section)
	}
	return b.String()
}

// ledgerSectionCounts 按分区统计账本条数，顺序与账本渲染的分区顺序一致。
func ledgerSectionCounts(entries []LedgerEntry) (inProgress, decisions, pendingUser int) {
	for _, entry := range entries {
		switch entry.Section {
		case LedgerInProgress:
			inProgress++
		case LedgerDecision:
			decisions++
		case LedgerPendingUser:
			pendingUser++
		}
	}
	return inProgress, decisions, pendingUser
}

// renderHandoffEvents 列出未消解事件：事件 ID、类型、标题与一行详情；没有时写「（没有未消解事件）」。
// 标题与详情先压成单行再截断，避免把前言的小节结构打散。
func renderHandoffEvents(events []attention.Event, err error) string {
	if err != nil {
		return fmt.Sprintf("（未消解事件读取失败：%v）", err)
	}
	if len(events) == 0 {
		return "（没有未消解事件）"
	}
	var b strings.Builder
	for _, ev := range events {
		title := clipRunes(strings.Join(strings.Fields(ev.Title), " "), rotationEventTitleRunes)
		fmt.Fprintf(&b, "- %s（%s）%s", ev.ID, kindLabel(ev.Kind), title)
		if detail := clipRunes(strings.Join(strings.Fields(ev.Detail), " "), rotationEventDetailRunes); detail != "" {
			fmt.Fprintf(&b, "；%s", detail)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderHandoffBees 列出最近派出的子代理：名字、任务、状态与净改动；为空时返回空串（整节不出现）。
func renderHandoffBees(bees []RotationBee) string {
	if len(bees) == 0 {
		return ""
	}
	var b strings.Builder
	for _, bee := range bees {
		task := clipRunes(strings.Join(strings.Fields(bee.Task), " "), rotationBeeTaskRunes)
		fmt.Fprintf(&b, "- %s：%s（%s；+%d/-%d）\n", bee.Name, task, bee.Status, bee.LinesAdded, bee.LinesDeleted)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTopicIndex 列出未结项的话题，每个一行：标题、阶段、任务进度与限长的 brief 摘要。
// 看板只读一次，阶段与进度都从这一次读取里算，不按话题逐个查看板。
func (r *Runtime) renderTopicIndex(ctx context.Context) string {
	topics, err := r.Topics(ctx)
	if err != nil {
		return fmt.Sprintf("（话题读取失败：%v）", err)
	}
	var board *kanban.Board
	if r.kanban != nil {
		if got, err := r.kanban.Board(ctx); err == nil {
			board = &got
		}
	}
	var b strings.Builder
	for _, topic := range topics {
		if topic.Closed {
			continue
		}
		phase, progress := topicIndexState(board, topic)
		fmt.Fprintf(&b, "- 「%s」（会话 %s）阶段：%s；任务：%s", topic.Title, topic.ThreadID, phaseLabel(phase), progress)
		if brief := clipRunes(strings.Join(strings.Fields(topic.Brief), " "), rotationBriefMaxRunes); brief != "" {
			fmt.Fprintf(&b, "；brief：%s", brief)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "（没有未结项的话题）"
	}
	return strings.TrimRight(b.String(), "\n")
}

// topicIndexState 从一次看板读取里推出话题的阶段与任务进度文本；board 为 nil 表示看板不可用。
func topicIndexState(board *kanban.Board, topic Topic) (phase, progress string) {
	if strings.TrimSpace(topic.GoalID) == "" {
		return PhaseDrafting, "未建目标"
	}
	if board == nil {
		return PhaseDrafting, "看板不可用"
	}
	status, found := "", false
	for _, goal := range board.Goals {
		if goal.ID == topic.GoalID {
			status, found = goal.Status, true
			break
		}
	}
	if !found {
		return PhaseDrafting, "看板上找不到目标"
	}
	done, total := 0, 0
	for _, view := range board.Tasks {
		if view.Task.GoalID == nil || *view.Task.GoalID != topic.GoalID {
			continue
		}
		total++
		if view.Task.Status == string(kanban.TaskStatusDone) {
			done++
		}
	}
	return PhaseFromGoalStatus(status), fmt.Sprintf("%d/%d 已完成", done, total)
}

// isStewardThread 报告 threadID 是否就是 steward.thread_id 指向的主会话：
// 优先用内存缓存，缓存还空着（运行时尚未 Start）时现读一次设置。
func (r *Runtime) isStewardThread(threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	if current := r.ThreadID(); current != "" {
		return current == threadID
	}
	if r.settings == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), topicLookupTimeout)
	defer cancel()
	stored, err := r.storedThreadID(ctx)
	return err == nil && stored != "" && stored == threadID
}
