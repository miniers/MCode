package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// 本文件实现总管的话题（topic）：用户提出的一件事就是一个话题，话题背后是一个专门的会话，
// 总管在该会话里追问、写任务书、规划与验收。话题表整体以 JSON 数组存在应用级设置里，
// 一条话题只记宿主需要跨轮记住的东西：会话 ID、项目、标题、任务书、待定问题数、目标 ID。

// settingTopics 是话题列表的持久化设置键：整张表存在一个键下，一次读改写就是一次原子替换，
// 不会出现「会话建好了但话题只写了一半」的中间态。
const settingTopics = "steward.topics"

// TopicGoalOwner 是总管创建目标时写入的 Owner 标记，看板据此认出「总管的话题」。
const TopicGoalOwner = "steward"

// DefaultAutoCloseAfter 是话题回执后自动结项的等待时长：回执满三天的老话题会自动进历史区，
// 免得话题只增不减。AutoCloseReceipted 的阈值小于或等于 0 时用这个值。
const DefaultAutoCloseAfter = 72 * time.Hour

// 话题相关的长度上限与超时。
const (
	// topicTitleMaxRunes 限制话题标题长度：标题会进会话名，一行短语才看得清。
	topicTitleMaxRunes = 30
	// topicRequestMaxRunes 限制投递给话题会话的需求原文长度，更长的需求让用户分几句说。
	topicRequestMaxRunes = 4000
	// topicLookupTimeout 是身份段与工具可见性判定里读话题表的超时：这两条路径每一轮
	// 提示词与每次 tools/list 都会走到，不能因为存储慢而拖住一轮对话。
	topicLookupTimeout = 2 * time.Second
)

// 话题阶段：由「有没有目标」和「目标处于什么状态」推出来，宿主据此注入不同阶段的指令。
// 取值同时被界面复用（internal/ui/steward_topic_types.go 的同名字符串），不要随意改名。
const (
	// PhaseDrafting 追问中：还没有目标，总管正在把需求问清楚并写任务书。
	PhaseDrafting = "drafting"
	// PhasePlanning 规划中：目标已建，宿主要总管按规划请求输出任务拆分。
	PhasePlanning = "planning"
	// PhasePlanFailed 规划失败：规划没有产出任务拆分，要等用户点话题卡片上的「重新规划」。
	PhasePlanFailed = "plan_failed"
	// PhasePlanReview 计划待审：总管自审拆分后决定开工还是问用户。
	PhasePlanReview = "plan_review"
	// PhaseRunning 执行中：任务在跑，总管等宿主的任务待验收事件卡。
	PhaseRunning = "running"
	// PhasePaused 已暂停：等用户恢复。
	PhasePaused = "paused"
	// PhaseCompleted 已完成：总管写结项报告并请用户确认结项。
	PhaseCompleted = "completed"
	// PhaseCancelled 已取消：话题不再推进。
	PhaseCancelled = "cancelled"
)

// Topic 是总管的一个话题：用户提出的一件事，背后是总管与这条需求的一对一会话。
type Topic struct {
	// ThreadID 是话题会话 ID，也是话题的唯一标识。
	ThreadID string `json:"threadId"`
	// ProjectID 是话题所属项目；话题会话的工作目录就是该项目的根目录。
	ProjectID string `json:"projectId"`
	// Title 是话题标题，默认取用户需求的第一行。
	Title string `json:"title"`
	// Brief 是任务书全文（markdown，7 节）；用户确认后它成为目标的 Prompt。
	Brief string `json:"brief,omitempty"`
	// OpenQuestions 是任务书里还没定下来的问题条数，清零即表示可以请用户确认开工。
	OpenQuestions int `json:"openQuestions"`
	// GoalID 是确认开工后创建的目标 ID；为空表示还在追问阶段。
	GoalID string `json:"goalId,omitempty"`
	// Closed 表示话题已结项（用户确认过结项报告）；会话与记录都保留，只不再推进。
	Closed bool `json:"closed,omitempty"`
	// ReceiptAt 是话题结论回执进账本的时刻；非空表示已回执，同一个话题只回执一次。
	ReceiptAt *time.Time `json:"receiptAt,omitempty"`
	// CreatedAt 是话题建立时刻；UpdatedAt 是最近一次改动的时刻。
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PhaseFromGoalStatus 把目标状态映射成话题阶段；界面与宿主共用这一张映射表。
// plan_failed 是独立阶段：宿主不会自动重新规划，要等用户点「重新规划」才再来一轮。
func PhaseFromGoalStatus(status string) string {
	switch kanban.GoalStatus(strings.TrimSpace(status)) {
	case kanban.GoalStatusPlanning:
		return PhasePlanning
	case kanban.GoalStatusPlanFailed:
		return PhasePlanFailed
	case kanban.GoalStatusPlanReview:
		return PhasePlanReview
	case kanban.GoalStatusRunning:
		return PhaseRunning
	case kanban.GoalStatusPaused:
		return PhasePaused
	case kanban.GoalStatusCompleted:
		return PhaseCompleted
	case kanban.GoalStatusCancelled:
		return PhaseCancelled
	default:
		return PhaseDrafting
	}
}

// Topics 返回全部话题，最近改动的排在前面；界面与宿主都用它读话题表。
func (r *Runtime) Topics(ctx context.Context) ([]Topic, error) {
	topics, err := r.loadTopics(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(topics, func(i, j int) bool {
		return topics[i].UpdatedAt.After(topics[j].UpdatedAt)
	})
	return topics, nil
}

// Topic 按会话 ID 查一条话题；ok 为 false 表示该会话不是话题。
func (r *Runtime) Topic(ctx context.Context, threadID string) (Topic, bool, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return Topic{}, false, nil
	}
	topics, err := r.loadTopics(ctx)
	if err != nil {
		return Topic{}, false, err
	}
	for _, topic := range topics {
		if topic.ThreadID == threadID {
			return topic, true, nil
		}
	}
	return Topic{}, false, nil
}

// IsTopic 报告 threadID 是否是某个话题的会话。MCP 层每轮都会问，因此命中缓存时不读存储；
// 缓存还没加载过时现读一次，读失败按「不是话题」处理（最差退化成普通会话的工具集）。
func (r *Runtime) IsTopic(threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	r.topicsMu.Lock()
	cached, loaded := r.topicsCache, r.topicsLoaded
	r.topicsMu.Unlock()
	if !loaded {
		ctx, cancel := context.WithTimeout(context.Background(), topicLookupTimeout)
		defer cancel()
		topics, err := r.loadTopics(ctx)
		if err != nil {
			log.Printf("steward: 读取话题列表失败，按普通会话处理: %v", err)
			return false
		}
		cached = topics
	}
	for _, topic := range cached {
		if topic.ThreadID == threadID {
			return true
		}
	}
	return false
}

// WatchTopics 订阅话题变更，返回取消订阅的函数；界面据此在看板变化之外再刷新一次话题列表。
func (r *Runtime) WatchTopics(fn func()) (cancel func()) {
	if fn == nil {
		return func() {}
	}
	r.topicsMu.Lock()
	defer r.topicsMu.Unlock()
	if r.topicWatchers == nil {
		r.topicWatchers = make(map[int]func())
	}
	r.nextTopicWatch++
	id := r.nextTopicWatch
	r.topicWatchers[id] = fn
	return func() {
		r.topicsMu.Lock()
		defer r.topicsMu.Unlock()
		delete(r.topicWatchers, id)
	}
}

// TopicPhase 返回话题当前所处的阶段：没有目标就是追问中，有目标按目标状态映射。
func (r *Runtime) TopicPhase(ctx context.Context, topic Topic) string {
	if strings.TrimSpace(topic.GoalID) == "" {
		if topic.Closed {
			return PhaseCancelled
		}
		return PhaseDrafting
	}
	status, ok := r.goalStatus(ctx, topic.GoalID)
	if !ok {
		return PhaseDrafting
	}
	return PhaseFromGoalStatus(status)
}

// OpenTopic 为一条需求开一个话题会话并记下话题。
//
// 会话建在项目根目录下，标题是「总管 · 用户给的名字」，权限模式跟随全局默认档位（与总管一致），
// 交互模式为 plan。Prompt 留空、需求原文走宿主事件：这样话题里不会凭空多出一条用户气泡，
// 用户看到的是自己在主对话里说的那句话，总管在这里接着追问。
func (r *Runtime) OpenTopic(ctx context.Context, projectID, title, request string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", errors.New("steward: 缺少项目 ID，无法开话题")
	}
	request = strings.TrimSpace(request)
	if request == "" {
		return "", errors.New("steward: 缺少需求原文，无法开话题")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = firstLine(request, topicTitleMaxRunes)
	}
	if r.projectRoot == nil {
		return "", errors.New("steward: 未提供项目目录解析，无法开话题")
	}
	if r.sessions == nil {
		return "", errors.New("steward: 未提供会话能力，无法开话题")
	}
	// project 允许写项目 ID 或目录，这里统一解析成项目 ID 与项目根目录。
	resolvedID, dir, err := r.projectRoot(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("steward: 解析项目目录失败: %w", err)
	}
	projectID = resolvedID

	cfg := r.config()
	threadID, err := r.sessions.StartSession(ctx, kanban.SessionRequest{
		ProjectID:       projectID,
		WorkDir:         dir,
		Title:           cfg.DisplayName() + " · " + title,
		Prompt:          "",
		InteractionMode: "plan",
		RuntimeMode:     StewardRuntimeMode,
		Provider:        cfg.Provider,
		Model:           cfg.Model,
		Effort:          cfg.Effort,
	})
	if err != nil {
		return "", fmt.Errorf("steward: 创建话题会话失败: %w", err)
	}

	now := r.now()
	topic := Topic{
		ThreadID:  threadID,
		ProjectID: projectID,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// 只把「读表到写表」这一段放进写锁：建会话与投递宿主事件都可能耗时，不能占着锁。
	r.topicWriteMu.Lock()
	topics, err := r.loadTopics(ctx)
	if err != nil {
		r.topicWriteMu.Unlock()
		return "", err
	}
	topics = append(topics, topic)
	err = r.saveTopics(ctx, topics)
	r.topicWriteMu.Unlock()
	if err != nil {
		return "", err
	}
	// 需求投递失败不影响话题可用：用户仍可在话题会话里直接说需求，宿主也会照常注入身份段。
	if err := r.sessions.SendHostEvent(ctx, threadID, topicKickoffText(request)); err != nil {
		log.Printf("steward: 向话题会话投递需求失败: %v", err)
	}
	return threadID, nil
}

// UpdateBrief 写入话题的任务书全文与待定问题条数；总管每轮追问结束都整体重写一次任务书。
func (r *Runtime) UpdateBrief(ctx context.Context, threadID, brief string, openQuestions int) error {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return errors.New("steward: 任务书不能为空")
	}
	if openQuestions < 0 {
		openQuestions = 0
	}
	_, err := r.updateTopic(ctx, threadID, func(topic *Topic) {
		topic.Brief = brief
		topic.OpenQuestions = openQuestions
	})
	return err
}

// StartTopicGoal 由话题会话在自审拆分后调用：目标处于 plan_review 时才允许开工。
// 状态检查放在这里而不是只靠提示词，避免模型在别的阶段把计划直接推进执行。
func (r *Runtime) StartTopicGoal(ctx context.Context, threadID, reason string) error {
	topic, ok, err := r.Topic(ctx, threadID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("steward: 该会话不是话题，无法开工")
	}
	if strings.TrimSpace(topic.GoalID) == "" {
		return errors.New("steward: 话题还没有目标，等用户确认任务书后才会建目标")
	}
	if r.kanban == nil {
		return errors.New("steward: 未接入看板服务，无法开工")
	}
	goal, err := r.findGoal(ctx, topic.GoalID)
	if err != nil {
		return err
	}
	if kanban.GoalStatus(strings.TrimSpace(goal.Status)) != kanban.GoalStatusPlanReview {
		return fmt.Errorf("steward: 目标当前状态是 %s，只有计划待审（plan_review）时才能开工", goal.Status)
	}
	if _, err := r.kanban.StartGoal(ctx, goal.ID, goal.Revision); err != nil {
		return fmt.Errorf("steward: 开工失败: %w", err)
	}
	log.Printf("steward: 话题 %s 已开工（%s）", topic.ThreadID, strings.TrimSpace(reason))
	return nil
}

// ConfirmTopic 把任务书变成目标，并让规划在话题会话里进行：
// 现在由 steward_update_brief 在待定问题清零时自动调用，界面的「直接开工」按钮是手动兜底。
func (r *Runtime) ConfirmTopic(ctx context.Context, threadID string) error {
	// 整段持锁：用户连点时第二次能看到上一次写回的 GoalID，走到「已开工」分支而不是再建一个目标。
	r.topicConfirmMu.Lock()
	defer r.topicConfirmMu.Unlock()
	topic, ok, err := r.Topic(ctx, threadID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("steward: 该会话不是话题，无法确认开工")
	}
	if strings.TrimSpace(topic.Brief) == "" {
		return errors.New("steward: 任务书还是空的，等总管把任务书写完再确认")
	}
	if strings.TrimSpace(topic.GoalID) != "" {
		return errors.New("steward: 这个话题已经开工，无需再次确认")
	}
	if r.kanban == nil {
		return errors.New("steward: 未接入看板服务，无法开工")
	}

	goal, found, err := r.findTopicGoal(ctx, topic.ThreadID)
	if err != nil {
		return err
	}
	if !found {
		goal, err = r.kanban.CreateGoalWithOptions(ctx, kanban.CreateGoalOptions{
			ProjectID:    topic.ProjectID,
			Prompt:       topic.Brief,
			PlanThreadID: topic.ThreadID,
			Owner:        TopicGoalOwner,
			// 计划先给用户看、不自动接受：开工由总管在话题里自审后调用 steward_start_goal 决定。
			Policy: map[string]any{"previewPlan": true, "autoAccept": false},
		})
		if err != nil {
			return fmt.Errorf("steward: 创建目标失败: %w", err)
		}
	}
	if _, err := r.updateTopic(ctx, topic.ThreadID, func(t *Topic) { t.GoalID = goal.ID }); err != nil {
		return err
	}
	// 先写回 GoalID 再发事件：话题会话收到事件时，身份段已经能读到「规划中」阶段。
	if err := r.sessions.SendHostEvent(ctx, topic.ThreadID, topicConfirmedText()); err != nil {
		log.Printf("steward: 通知话题会话任务书已确认失败: %v", err)
	}
	return nil
}

// findTopicGoal 在话题的规划会话名下去找已经建过的目标：上次写完目标但写回 GoalID 失败时，
// 重试靠它接回原目标，不会给同一个话题再建一个目标。已取消的目标不算。
func (r *Runtime) findTopicGoal(ctx context.Context, threadID string) (store.Goal, bool, error) {
	if r.kanban == nil {
		return store.Goal{}, false, errors.New("steward: 未接入看板服务，无法读取目标")
	}
	board, err := r.kanban.Board(ctx)
	if err != nil {
		return store.Goal{}, false, fmt.Errorf("steward: 读取看板失败: %w", err)
	}
	for _, goal := range board.Goals {
		if goal.Owner != TopicGoalOwner || goal.PlanThreadID == nil || *goal.PlanThreadID != threadID {
			continue
		}
		if kanban.GoalStatus(strings.TrimSpace(goal.Status)) == kanban.GoalStatusCancelled {
			continue
		}
		return goal, true, nil
	}
	return store.Goal{}, false, nil
}

// CloseTopic 结项：把话题标记为已关闭。会话与目标都保留，只不再推进。
// 结项后把结论回执进主会话的账本；回执失败只记日志，不影响结项本身。
func (r *Runtime) CloseTopic(ctx context.Context, threadID string) error {
	if _, err := r.updateTopic(ctx, threadID, func(topic *Topic) { topic.Closed = true }); err != nil {
		return err
	}
	if _, err := r.ReceiptTopic(ctx, threadID); err != nil {
		log.Printf("steward: 话题 %s 结项回执失败: %v", threadID, err)
	}
	return nil
}

// ReplanTopic 在规划失败后重新发起一轮规划：看板清掉待办任务、把目标改回 planning。
// 只有 plan_failed 才允许重新规划，其余状态下由用户另做决定。
func (r *Runtime) ReplanTopic(ctx context.Context, threadID string) error {
	topic, ok, err := r.Topic(ctx, threadID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("steward: 该会话不是话题，无法重新规划")
	}
	if strings.TrimSpace(topic.GoalID) == "" {
		return errors.New("steward: 话题还没有目标，无需重新规划")
	}
	if r.kanban == nil {
		return errors.New("steward: 未接入看板服务，无法重新规划")
	}
	goal, err := r.findGoal(ctx, topic.GoalID)
	if err != nil {
		return err
	}
	if kanban.GoalStatus(strings.TrimSpace(goal.Status)) != kanban.GoalStatusPlanFailed {
		return fmt.Errorf("steward: 目标当前状态是 %s，只有规划失败（plan_failed）时才能重新规划", goal.Status)
	}
	if _, err := r.kanban.ReplanGoal(ctx, goal.ID, "", goal.Revision); err != nil {
		return fmt.Errorf("steward: 重新规划失败: %w", err)
	}
	return nil
}

// DeleteTopic 删除一条话题：先取消未完成的目标并中断还在跑的任务，再剔掉话题行与账本条目。
// 返回 true 表示真的删了一条；会话不存在时返回 (false, nil)，由调用方决定要不要报错。
func (r *Runtime) DeleteTopic(ctx context.Context, threadID string) (bool, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false, errors.New("steward: 缺少话题会话 ID")
	}
	topic, ok, err := r.Topic(ctx, threadID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	// 删除是用户的明确意图：取消和中断失败只记日志，不能因此把话题留在列表里。
	if goalID := strings.TrimSpace(topic.GoalID); goalID != "" {
		if r.kanban == nil {
			log.Printf("steward: 删除话题 %s 时未接入看板，目标 %s 未取消", topic.ThreadID, goalID)
		} else if goal, findErr := r.findGoal(ctx, goalID); findErr != nil {
			log.Printf("steward: 删除话题 %s 时读取目标 %s 失败: %v", topic.ThreadID, goalID, findErr)
		} else if status := kanban.GoalStatus(strings.TrimSpace(goal.Status)); status != kanban.GoalStatusCompleted && status != kanban.GoalStatusCancelled {
			r.stopTopicGoal(ctx, goal)
		}
	}

	err = func() error {
		r.topicWriteMu.Lock()
		defer r.topicWriteMu.Unlock()
		topics, loadErr := r.loadTopics(ctx)
		if loadErr != nil {
			return loadErr
		}
		kept := make([]Topic, 0, len(topics))
		removed := false
		for _, item := range topics {
			if item.ThreadID == threadID {
				removed = true
				continue
			}
			kept = append(kept, item)
		}
		if !removed {
			return nil
		}
		return r.saveTopics(ctx, kept)
	}()
	if err != nil {
		return false, err
	}
	if err := r.removeLedgerForTopic(ctx, threadID); err != nil {
		log.Printf("steward: 删除话题 %s 的账本条目失败: %v", threadID, err)
	}
	return true, nil
}

// stopTopicGoal 取消目标并中断它名下还在跑的任务；失败只记日志，删除话题不能因为看板报错而停下。
func (r *Runtime) stopTopicGoal(ctx context.Context, goal store.Goal) {
	if _, err := r.kanban.CancelGoal(ctx, goal.ID, goal.Revision); err != nil {
		log.Printf("steward: 取消目标 %s 失败: %v", goal.ID, err)
	}
	board, err := r.kanban.Board(ctx)
	if err != nil {
		log.Printf("steward: 读取看板失败，未能中断目标 %s 的任务: %v", goal.ID, err)
		return
	}
	for _, view := range board.Tasks {
		task := view.Task
		if task.GoalID == nil || *task.GoalID != goal.ID || !topicTaskRunning(task.Status) {
			continue
		}
		if err := r.kanban.InterruptTask(ctx, task); err != nil {
			log.Printf("steward: 中断任务 %s 失败: %v", task.ID, err)
		}
	}
}

// topicTaskRunning 报告任务是否还占着一轮执行：进行中与等审批的任务都要先中断。
func topicTaskRunning(status string) bool {
	switch kanban.TaskStatus(strings.TrimSpace(status)) {
	case kanban.TaskStatusInProgress, kanban.TaskStatusNeedsApproval:
		return true
	default:
		return false
	}
}

// removeLedgerForTopic 删掉账本里属于该话题的全部条目；没有匹配的条目时不写回。
func (r *Runtime) removeLedgerForTopic(ctx context.Context, topicThreadID string) error {
	r.ledgerMu.Lock()
	defer r.ledgerMu.Unlock()
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return err
	}
	kept := make([]LedgerEntry, 0, len(entries))
	removed := false
	for _, entry := range entries {
		if entry.TopicThreadID == topicThreadID {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return nil
	}
	return r.saveLedger(ctx, kept)
}

// AutoCloseReceipted 把「已回执且回执时间超过 olderThan」的未结项话题自动结项。
// 只读一次话题表、只在真的有关闭动作时整表写回一次，返回关闭的条数。
// olderThan 小于或等于 0 时用 DefaultAutoCloseAfter。
func (r *Runtime) AutoCloseReceipted(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = DefaultAutoCloseAfter
	}
	r.topicWriteMu.Lock()
	defer r.topicWriteMu.Unlock()
	topics, err := r.loadTopics(ctx)
	if err != nil {
		return 0, err
	}
	now := r.now()
	closed := 0
	for i := range topics {
		topic := &topics[i]
		if topic.Closed || topic.ReceiptAt == nil {
			continue
		}
		if now.Sub(*topic.ReceiptAt) < olderThan {
			continue
		}
		topic.Closed = true
		topic.UpdatedAt = now
		closed++
	}
	if closed == 0 {
		return 0, nil
	}
	if err := r.saveTopics(ctx, topics); err != nil {
		return 0, err
	}
	return closed, nil
}

// loadTopics 从设置里读整张话题表并刷新缓存；键不存在或为空时返回空表。
func (r *Runtime) loadTopics(ctx context.Context) ([]Topic, error) {
	if r.settings == nil {
		return nil, errors.New("steward: 未提供设置存储，无法读取话题")
	}
	raw, ok, err := r.settings.GetSetting(ctx, settingTopics)
	if err != nil {
		return nil, fmt.Errorf("steward: 读取话题列表失败: %w", err)
	}
	topics := make([]Topic, 0)
	if ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &topics); err != nil {
			return nil, fmt.Errorf("steward: 解析话题列表失败: %w", err)
		}
	}
	if topics == nil {
		topics = make([]Topic, 0)
	}
	r.topicsMu.Lock()
	r.topicsCache = topics
	r.topicsLoaded = true
	r.topicsMu.Unlock()
	return append([]Topic(nil), topics...), nil
}

// saveTopics 把整张话题表写回设置并刷新缓存，最后通知订阅者。
func (r *Runtime) saveTopics(ctx context.Context, topics []Topic) error {
	if r.settings == nil {
		return errors.New("steward: 未提供设置存储，无法保存话题")
	}
	if topics == nil {
		topics = make([]Topic, 0)
	}
	payload, err := json.Marshal(topics)
	if err != nil {
		return fmt.Errorf("steward: 序列化话题列表失败: %w", err)
	}
	if err := r.settings.SetSetting(ctx, settingTopics, string(payload)); err != nil {
		return fmt.Errorf("steward: 保存话题列表失败: %w", err)
	}
	r.topicsMu.Lock()
	r.topicsCache = append([]Topic(nil), topics...)
	r.topicsLoaded = true
	r.topicsMu.Unlock()
	r.notifyTopicsChanged()
	return nil
}

// updateTopic 按会话 ID 读改写一条话题：mutate 在副本上改，成功后整表写回并刷新 UpdatedAt。
func (r *Runtime) updateTopic(ctx context.Context, threadID string, mutate func(*Topic)) (Topic, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return Topic{}, errors.New("steward: 缺少话题会话 ID")
	}
	// 整个读改写都在写锁里：mutate 只在副本上改，不得回头调用任何话题方法，否则会自锁。
	r.topicWriteMu.Lock()
	defer r.topicWriteMu.Unlock()
	topics, err := r.loadTopics(ctx)
	if err != nil {
		return Topic{}, err
	}
	for i := range topics {
		if topics[i].ThreadID != threadID {
			continue
		}
		mutate(&topics[i])
		topics[i].UpdatedAt = r.now()
		if err := r.saveTopics(ctx, topics); err != nil {
			return Topic{}, err
		}
		return topics[i], nil
	}
	return Topic{}, fmt.Errorf("steward: 会话 %s 不是话题", threadID)
}

// notifyTopicsChanged 在锁外回调订阅者：订阅者可能回头读话题，持锁回调会自锁。
func (r *Runtime) notifyTopicsChanged() {
	r.topicsMu.Lock()
	watchers := make([]func(), 0, len(r.topicWatchers))
	for _, fn := range r.topicWatchers {
		watchers = append(watchers, fn)
	}
	r.topicsMu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

// goalStatus 从看板读目标状态；未接入看板或目标不存在时 ok 为 false。
func (r *Runtime) goalStatus(ctx context.Context, goalID string) (string, bool) {
	goal, err := r.findGoal(ctx, goalID)
	if err != nil {
		return "", false
	}
	return goal.Status, true
}

// findGoal 从看板里取出一个目标；看板未接入、读失败或目标不存在时返回错误。
func (r *Runtime) findGoal(ctx context.Context, goalID string) (goal store.Goal, err error) {
	if r.kanban == nil {
		return goal, errors.New("steward: 未接入看板服务，无法读取目标")
	}
	board, err := r.kanban.Board(ctx)
	if err != nil {
		return goal, fmt.Errorf("steward: 读取看板失败: %w", err)
	}
	for _, item := range board.Goals {
		if item.ID == goalID {
			return item, nil
		}
	}
	return goal, fmt.Errorf("steward: 看板上找不到目标 %s", goalID)
}

// topicKickoffText 是话题会话收到的第一条宿主事件：用户在主对话里的原话，加一句开始追问的指令。
func topicKickoffText(request string) string {
	req := strings.TrimSpace(request)
	if runes := []rune(req); len(runes) > topicRequestMaxRunes {
		req = string(runes[:topicRequestMaxRunes]) + "……（原文过长已截断）"
	}
	return "用户在主对话里提出的需求原文：\n\n" + req + "\n\n请开始追问：先读代码再问，一次只问一个问题。"
}

// topicConfirmedText 是用户确认任务书后发给话题会话的宿主事件。
func topicConfirmedText() string {
	return "用户已确认任务书，宿主即将发出规划请求。按规划请求输出任务拆分，不要再追问用户。"
}

// firstLine 取文本的第一行并按 rune 截断，用于从需求里推出话题标题。
func firstLine(text string, max int) string {
	line := strings.TrimSpace(text)
	if idx := strings.IndexAny(line, "\r\n"); idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}
	if line == "" {
		line = strings.TrimSpace(text)
	}
	if max <= 0 {
		return line
	}
	runes := []rune(line)
	if len(runes) > max {
		return strings.TrimSpace(string(runes[:max]))
	}
	return line
}
