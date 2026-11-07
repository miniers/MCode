package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"mcode/internal/attention"
	"mcode/internal/kanban"
	"mcode/internal/steward"
	mcodeui "mcode/internal/ui"
)

// Client 在总管运行时注入后实现界面的总管入口接口；未注入时
// 相关方法按未接线处理，界面不会显示总管入口。
var _ mcodeui.StewardClient = (*Client)(nil)

// 话题卡片用同一份实现：话题列表、确认开工与关闭都只能由界面发起，模型没有对应工具。
var _ mcodeui.StewardTopicsClient = (*Client)(nil)

// stewardThreadRuntime 是界面打开总管会话所需的运行时能力，用接口声明
// 便于测试替换成假运行时，也用不着为界面引入整个 steward 包。
type stewardThreadRuntime interface {
	// ThreadID 返回当前总管会话 ID；尚未创建或已失效时为空串。
	ThreadID() string
	// Ensure 确保总管会话存在并返回其 ID；配置未启用时返回空串且不报错。
	Ensure(ctx context.Context) (string, error)
}

// stewardWakeRuntime 是运行时可选提供的今日提醒次数能力，与打开会话分开声明：
// 旧替身只实现 stewardThreadRuntime 也能继续用，界面拿不到时只是不显示那一句。
type stewardWakeRuntime interface {
	// WakesRemainingToday 返回今天已主动找过用户的次数与每日上限。
	WakesRemainingToday() (used, limit int)
}

// stewardTopicRuntime 是界面需要的话题能力，与 stewardThreadRuntime 分开声明：
// 打开会话与话题管理互不依赖，旧替身只实现前者也不影响。
type stewardTopicRuntime interface {
	// Topics 返回全部话题，按最近变化倒序。
	Topics(ctx context.Context) ([]steward.Topic, error)
	// OpenTopic 为一条需求开话题并返回话题会话 ID。
	OpenTopic(ctx context.Context, projectID, title, request string) (string, error)
	// ConfirmTopic 是用户点任务书卡片上的「直接开工」时才走的路径：模型没自动开工或用户想提前开工时的兜底。
	ConfirmTopic(ctx context.Context, threadID string) error
	// CloseTopic 关闭话题。
	CloseTopic(ctx context.Context, threadID string) error
	// DeleteTopic 删除话题（连同它的账本条目），返回是否真的删了一条。
	DeleteTopic(ctx context.Context, threadID string) (bool, error)
	// ReplanTopic 在规划失败后重新发起一轮规划。
	ReplanTopic(ctx context.Context, threadID string) error
	// StartTopicGoal 让 plan_review 的话题跳过等审直接开工；其余状态由运行时拒绝。
	StartTopicGoal(ctx context.Context, threadID, reason string) error
	// WatchTopics 在话题变化时回调，返回取消订阅的函数。
	WatchTopics(fn func()) (cancel func())
}

// SetSteward 注入总管运行时。与看板、自动化等可选服务共用同一把锁。
func (c *Client) SetSteward(rt stewardThreadRuntime) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	c.steward = rt
}

// stewardRuntime 取当前注入的总管运行时，未注入时返回 nil。
func (c *Client) stewardRuntime() stewardThreadRuntime {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	return c.steward
}

// StewardThreadID 返回当前总管会话 ID；未接入或尚未创建时为空串。
func (c *Client) StewardThreadID() string {
	rt := c.stewardRuntime()
	if rt == nil {
		return ""
	}
	return rt.ThreadID()
}

// StewardEnabled 报告配置里是否已启用总管：侧栏据此在未启用时仍留一行置灰入口，
// 点一下直接去设置里开启。配置读不到时按未启用处理。
func (c *Client) StewardEnabled() bool {
	cfg, ok := c.loadConfig()
	if !ok {
		return false
	}
	return cfg.Orchestration.Steward.Enabled
}

// StewardWakesToday 返回今天已主动提醒的次数与每日上限，供设置页显示今日剩余次数。
// 未启用总管或拿不到运行时时 ok 为 false，界面据此不显示这一句。
func (c *Client) StewardWakesToday() (used, limit int, ok bool) {
	if !c.StewardEnabled() {
		return 0, 0, false
	}
	rt, supported := c.stewardRuntime().(stewardWakeRuntime)
	if !supported || rt == nil {
		return 0, 0, false
	}
	used, limit = rt.WakesRemainingToday()
	return used, limit, true
}

// StewardIdentity 返回总管的昵称与头像原文，两者都可能为空：昵称为空时由界面显示「总管」，
// 头像为空时由界面取昵称首字或「总」。配置读不到时按无昵称、无头像处理。
func (c *Client) StewardIdentity() (name, avatar string) {
	cfg, ok := c.loadConfig()
	if !ok {
		return "", ""
	}
	return strings.TrimSpace(cfg.Orchestration.Steward.Name), strings.TrimSpace(cfg.Orchestration.Steward.Avatar)
}

// OpenStewardThread 确保总管会话存在并返回其 ID。已有会话直接返回，
// 否则现建一次：首次建会话要走一轮完整的 CLI 首轮启动，可能耗时几十秒，
// 所以按 stewardEnsureTimeout 限时，避免界面一直等下去。
func (c *Client) OpenStewardThread(ctx context.Context) (string, error) {
	rt := c.stewardRuntime()
	if rt == nil {
		return "", errors.New("总管未接入")
	}
	if id := rt.ThreadID(); id != "" {
		return id, nil
	}
	ensureCtx, cancel := context.WithTimeout(ctx, stewardEnsureTimeout)
	defer cancel()
	threadID, err := rt.Ensure(ensureCtx)
	if err != nil {
		return "", fmt.Errorf("准备总管会话失败: %w", err)
	}
	// 未启用时 Ensure 按约定返回空串且不报错，界面要据此提示用户去设置里开启。
	if threadID == "" {
		return "", errors.New("总管未启用，请先在设置 → 总管中启用")
	}
	return threadID, nil
}

// stewardTopicRuntimeOf 取注入运行时的话题能力；未接线或运行时还不支持话题时返回 nil。
func (c *Client) stewardTopicRuntimeOf() stewardTopicRuntime {
	rt, _ := c.stewardRuntime().(stewardTopicRuntime)
	return rt
}

// StewardTopics 返回全部话题卡片数据。阶段与任务计数来自看板：没有目标就是追问中，
// 有目标就按目标状态与任务状态算。看板不可用时仍返回话题，只是没有阶段与计数，
// 因为话题本身存在比进度重要；未接入总管时返回空列表而不是错误。
func (c *Client) StewardTopics(ctx context.Context) ([]mcodeui.StewardTopic, error) {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return nil, nil
	}
	topics, err := rt.Topics(ctx)
	if err != nil {
		return nil, err
	}
	if len(topics) == 0 {
		return nil, nil
	}

	goalStatus, doneTasks, totalTasks, reviewTasks := c.stewardTopicTaskCounts(ctx)
	names := c.stewardProjectNames(ctx)
	out := make([]mcodeui.StewardTopic, 0, len(topics))
	for _, topic := range topics {
		item := mcodeui.StewardTopic{
			ThreadID:      topic.ThreadID,
			ProjectID:     topic.ProjectID,
			ProjectName:   names[topic.ProjectID],
			Title:         topic.Title,
			Brief:         topic.Brief,
			OpenQuestions: topic.OpenQuestions,
			Phase:         mcodeui.StewardTopicDrafting,
			GoalID:        topic.GoalID,
			Closed:        topic.Closed,
			UpdatedAt:     topic.UpdatedAt,
		}
		if status, ok := goalStatus[topic.GoalID]; ok {
			item.Phase = steward.PhaseFromGoalStatus(status)
			item.TasksDone = doneTasks[topic.GoalID]
			item.TasksTotal = totalTasks[topic.GoalID]
		}
		item.PendingUser = stewardTopicPendingUser(item.Phase, item.Brief, item.OpenQuestions, reviewTasks[topic.GoalID], item.Closed)
		out = append(out, item)
	}
	return out, nil
}

// stewardTopicTaskCounts 从看板取出每个目标的阶段与任务计数。看板未接入或读失败时返回空表，
// 调用方据此退化成「只有话题、没有进度」，不把看板的临时故障变成话题列表的错误。
func (c *Client) stewardTopicTaskCounts(ctx context.Context) (goalStatus map[string]string, done, total, review map[string]int) {
	goalStatus = map[string]string{}
	done = map[string]int{}
	total = map[string]int{}
	review = map[string]int{}
	c.kanbanMu.Lock()
	k := c.kanban
	c.kanbanMu.Unlock()
	if k == nil {
		return goalStatus, done, total, review
	}
	board, err := k.Board(ctx)
	if err != nil {
		return goalStatus, done, total, review
	}
	for _, goal := range board.Goals {
		goalStatus[goal.ID] = goal.Status
	}
	for _, view := range board.Tasks {
		if view.Task.GoalID == nil {
			continue
		}
		goalID := *view.Task.GoalID
		total[goalID]++
		switch view.Task.Status {
		case string(kanban.TaskStatusDone):
			done[goalID]++
		case string(kanban.TaskStatusReview):
			review[goalID]++
		}
	}
	return goalStatus, done, total, review
}

// stewardProjectNames 取项目 ID 到名字的映射，供话题卡片显示归属。
// 取不到时返回空表：卡片少一行项目名，不影响列表本身。
func (c *Client) stewardProjectNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if c.backend == nil {
		return names
	}
	projects, err := c.Projects(ctx)
	if err != nil {
		return names
	}
	for _, project := range projects {
		names[project.ID] = project.Name
	}
	return names
}

// stewardTopicPendingUser 数一件话题里有几件事等用户拍板：待验收的任务、已就绪待确认的任务书、
// 规划失败待重试、已完成待确认的结项。四件互不包含，所以直接相加；已关闭的话题不再计入。
// 计划待审不算待办：设计文档规定总管自审后直接开工，拿不准才来问，阶段本身不该亮红点。
func stewardTopicPendingUser(phase, brief string, openQuestions, reviewTasks int, closed bool) int {
	if closed {
		return 0
	}
	pending := reviewTasks
	if phase == mcodeui.StewardTopicDrafting && openQuestions == 0 && strings.TrimSpace(brief) != "" {
		pending++
	}
	// 规划失败也要等用户点「重新规划」，不能让失败的话题静静卡住。
	if phase == steward.PhasePlanFailed {
		pending++
	}
	if phase == mcodeui.StewardTopicCompleted {
		pending++
	}
	return pending
}

// StartStewardTopic 为用户在主对话里提出的一句需求开话题，返回话题会话 ID。
// 标题取需求首行截 30 个字：话题列表与卡片只放得下一行，完整需求随需求原文一起传下去。
func (c *Client) StartStewardTopic(ctx context.Context, projectID, request string) (string, error) {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return "", errors.New("总管未接入")
	}
	request = strings.TrimSpace(request)
	if request == "" {
		return "", errors.New("需求不能为空")
	}
	return rt.OpenTopic(ctx, projectID, stewardTopicTitle(request), request)
}

// stewardTopicTitle 取需求首行并截到 30 个字，作为话题标题。
func stewardTopicTitle(request string) string {
	line := strings.TrimSpace(strings.SplitN(request, "\n", 2)[0])
	runes := []rune(line)
	if len(runes) <= 30 {
		return line
	}
	return strings.TrimSpace(string(runes[:30]))
}

// ConfirmStewardTopic 是用户点任务书卡片上的「直接开工」时走的路径：建 Goal 并把话题推进到规划。
func (c *Client) ConfirmStewardTopic(ctx context.Context, threadID string) error {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return errors.New("总管未接入")
	}
	return rt.ConfirmTopic(ctx, threadID)
}

// CloseStewardTopic 关闭一条话题：关掉后它不再计入待处理，会话与目标保持原样。
func (c *Client) CloseStewardTopic(ctx context.Context, threadID string) error {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return errors.New("总管未接入")
	}
	return rt.CloseTopic(ctx, threadID)
}

// ReplanStewardTopic 让规划失败的话题重新发起一轮规划；其余状态由运行时拒绝。
func (c *Client) ReplanStewardTopic(ctx context.Context, threadID string) error {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return errors.New("总管未接入")
	}
	return rt.ReplanTopic(ctx, threadID)
}

// StartStewardTopicGoal 是用户在话题卡片上点「直接开工」时走的路径：
// 计划本该由总管自审后开工，用户点这里就是替它把这一步补上。
func (c *Client) StartStewardTopicGoal(ctx context.Context, threadID string) error {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return errors.New("总管未接入")
	}
	return rt.StartTopicGoal(ctx, threadID, "用户在话题卡片上点了开工")
}

// DeleteStewardTopic 删除一条话题：先删话题行（顺带取消目标、中断在跑的任务），再删会话；
// 顺序不能换，否则会话没了就再也不知道该删哪条话题。
func (c *Client) DeleteStewardTopic(ctx context.Context, threadID string) error {
	rt := c.stewardTopicRuntimeOf()
	if rt == nil {
		return errors.New("总管未接入")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("缺少话题会话 ID")
	}
	if _, err := rt.DeleteTopic(ctx, threadID); err != nil {
		return err
	}
	// DeleteThread 自带上下文，不需要调用方的 ctx。
	if err := c.DeleteThread(threadID); err != nil {
		return err
	}
	// 会话已经没了，该会话上的待处理事件也一并消解；失败只记日志。
	if svc := c.attentionService(); svc != nil {
		if _, err := svc.ResolveSubject(ctx, attention.SubjectThread, threadID, attention.ResolvedByUser); err != nil {
			log.Printf("app: 消解已删除话题 %s 的待处理事件失败: %v", threadID, err)
		}
	}
	return nil
}

// WatchStewardTopics 同时订阅话题与看板变化：话题变化改标题与任务书，
// 看板变化改阶段与任务计数，任一变化都要刷新卡片。取消订阅只生效一次。
func (c *Client) WatchStewardTopics(fn func()) (cancel func()) {
	if fn == nil {
		return func() {}
	}
	cancels := make([]func(), 0, 2)
	if rt := c.stewardTopicRuntimeOf(); rt != nil {
		if stop := rt.WatchTopics(fn); stop != nil {
			cancels = append(cancels, stop)
		}
	}
	if stop := c.WatchKanban(fn); stop != nil {
		cancels = append(cancels, stop)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, stop := range cancels {
				stop()
			}
		})
	}
}
