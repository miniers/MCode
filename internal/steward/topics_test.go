package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// fakeTopicKanban 是 Kanban 的测试实现：记录建目标、开工、重新规划、取消与中断请求，看板内容由测试摆布。
type fakeTopicKanban struct {
	goals       []store.Goal
	tasks       []kanban.TaskView
	created     []kanban.CreateGoalOptions
	started     []string
	replanned   []string
	cancelled   []string
	interrupted []string
	createErr   error
	boardErr    error
}

// Board 返回测试摆好的目标与任务列表。
func (f *fakeTopicKanban) Board(context.Context) (kanban.Board, error) {
	if f.boardErr != nil {
		return kanban.Board{}, f.boardErr
	}
	return kanban.Board{
		Goals: append([]store.Goal(nil), f.goals...),
		Tasks: append([]kanban.TaskView(nil), f.tasks...),
	}, nil
}

// CreateGoalWithOptions 记录建目标参数并返回一个规划中的目标。
func (f *fakeTopicKanban) CreateGoalWithOptions(_ context.Context, opts kanban.CreateGoalOptions) (store.Goal, error) {
	if f.createErr != nil {
		return store.Goal{}, f.createErr
	}
	f.created = append(f.created, opts)
	goal := store.Goal{
		ID:        "goal-1",
		ProjectID: opts.ProjectID,
		Prompt:    opts.Prompt,
		Status:    string(kanban.GoalStatusPlanning),
		Owner:     opts.Owner,
		Revision:  1,
	}
	if opts.PlanThreadID != "" {
		planThreadID := opts.PlanThreadID
		goal.PlanThreadID = &planThreadID
	}
	f.goals = append(f.goals, goal)
	return goal, nil
}

// StartGoal 记录开工请求。
func (f *fakeTopicKanban) StartGoal(_ context.Context, goalID string, _ int) (store.Goal, error) {
	f.started = append(f.started, goalID)
	return store.Goal{ID: goalID, Status: string(kanban.GoalStatusRunning)}, nil
}

// ReplanGoal 记录重新规划请求，并把目标改回规划中。
func (f *fakeTopicKanban) ReplanGoal(_ context.Context, goalID, _ string, _ int) (store.Goal, error) {
	f.replanned = append(f.replanned, goalID)
	for i := range f.goals {
		if f.goals[i].ID == goalID {
			f.goals[i].Status = string(kanban.GoalStatusPlanning)
			f.goals[i].Revision++
			return f.goals[i], nil
		}
	}
	return store.Goal{}, errors.New("目标不存在")
}

// CancelGoal 记录取消请求，并把目标改成已取消。
func (f *fakeTopicKanban) CancelGoal(_ context.Context, goalID string, _ int) (store.Goal, error) {
	f.cancelled = append(f.cancelled, goalID)
	for i := range f.goals {
		if f.goals[i].ID == goalID {
			f.goals[i].Status = string(kanban.GoalStatusCancelled)
			f.goals[i].Revision++
			return f.goals[i], nil
		}
	}
	return store.Goal{}, errors.New("目标不存在")
}

// InterruptTask 记录被中断的任务。
func (f *fakeTopicKanban) InterruptTask(_ context.Context, task store.Task) error {
	f.interrupted = append(f.interrupted, task.ID)
	return nil
}

// newTopicRig 构造一个接了项目目录与看板假实现的测试运行时。
func newTopicRig(t *testing.T) (*stewardRig, *fakeTopicKanban) {
	t.Helper()
	rig := newStewardRig(t, enabledConfig())
	board := &fakeTopicKanban{}
	rig.rt.kanban = board
	return rig, board
}

// seedTopic 直接把一条话题写进设置，模拟已有话题。
func seedTopic(t *testing.T, rig *stewardRig, topic Topic) {
	t.Helper()
	payload, err := json.Marshal([]Topic{topic})
	if err != nil {
		t.Fatalf("序列化话题失败: %v", err)
	}
	rig.settings.set(settingTopics, string(payload))
}

// TestOpenTopicStartsSessionAndRecordsTopic 校验开话题时建会话的参数与话题记录。
func TestOpenTopicStartsSessionAndRecordsTopic(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()

	threadID, err := rig.rt.OpenTopic(ctx, "proj-1", "整理登录流程", "把登录流程理顺，顺便补上错误提示")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	if threadID != "steward-thread-1" {
		t.Fatalf("话题会话 ID 不对: %s", threadID)
	}

	starts := rig.sessions.startRequests()
	if len(starts) != 1 {
		t.Fatalf("应当只建一个会话，实际 %d 个", len(starts))
	}
	req := starts[0]
	if req.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q，期望 proj-1", req.ProjectID)
	}
	if req.WorkDir != filepath.Join(rig.dataDir, "proj-1") {
		t.Errorf("WorkDir = %q，期望项目根目录", req.WorkDir)
	}
	if req.Title != "总管 · 整理登录流程" {
		t.Errorf("Title = %q，期望「总管 · 整理登录流程」", req.Title)
	}
	if req.Prompt != "" {
		t.Errorf("Prompt 应当为空，实际 %q", req.Prompt)
	}
	if req.InteractionMode != "plan" {
		t.Errorf("InteractionMode = %q，期望 plan", req.InteractionMode)
	}
	if req.RuntimeMode != StewardRuntimeMode {
		t.Errorf("RuntimeMode = %q，期望 %q", req.RuntimeMode, StewardRuntimeMode)
	}
	if req.Provider != "anthropic" || req.Model != "claude-test-model" || req.Effort != "high" {
		t.Errorf("模型配置没有跟着总管走: %+v", req)
	}

	// 需求原文以宿主事件发出：话题里不该出现一条冒充用户的提问气泡。
	messages := rig.sessions.messages()
	if len(messages) != 1 {
		t.Fatalf("应当发出一条宿主事件，实际 %d 条", len(messages))
	}
	if messages[0].ThreadID != threadID {
		t.Errorf("宿主事件发给了 %q，期望 %q", messages[0].ThreadID, threadID)
	}
	if !strings.Contains(messages[0].Text, "把登录流程理顺") || !strings.Contains(messages[0].Text, "请开始追问") {
		t.Errorf("宿主事件内容不对: %s", messages[0].Text)
	}

	topic, ok, err := rig.rt.Topic(ctx, threadID)
	if err != nil || !ok {
		t.Fatalf("话题没有记录: ok=%v err=%v", ok, err)
	}
	if topic.ProjectID != "proj-1" || topic.Title != "整理登录流程" {
		t.Errorf("话题记录不对: %+v", topic)
	}
	if topic.CreatedAt.IsZero() || !topic.CreatedAt.Equal(topic.UpdatedAt) {
		t.Errorf("话题时间戳不对: created=%v updated=%v", topic.CreatedAt, topic.UpdatedAt)
	}
	if !rig.rt.IsTopic(threadID) {
		t.Error("IsTopic 应当为真")
	}
	if rig.rt.IsTopic("steward-thread-2") {
		t.Error("普通会话不该被当成话题")
	}
}

// TestOpenTopicFallsBackToRequestTitle 校验没给标题时取需求首行。
func TestOpenTopicFallsBackToRequestTitle(t *testing.T) {
	rig, _ := newTopicRig(t)
	if _, err := rig.rt.OpenTopic(context.Background(), "proj-1", "", "  第一行标题\n第二行细节"); err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	topic, ok, err := rig.rt.Topic(context.Background(), "steward-thread-1")
	if err != nil || !ok {
		t.Fatalf("话题没有记录: ok=%v err=%v", ok, err)
	}
	if topic.Title != "第一行标题" {
		t.Errorf("标题 = %q，期望「第一行标题」", topic.Title)
	}
}

// TestOpenTopicRejectsEmptyRequest 校验空需求不开话题。
func TestOpenTopicRejectsEmptyRequest(t *testing.T) {
	rig, _ := newTopicRig(t)
	if _, err := rig.rt.OpenTopic(context.Background(), "proj-1", "标题", "   "); err == nil {
		t.Fatal("空需求应当报错")
	}
	if len(rig.sessions.startRequests()) != 0 {
		t.Error("空需求不该建会话")
	}
}

// TestUpdateBriefPersistsBriefAndOpenQuestions 校验任务书写入与待定问题条数。
func TestUpdateBriefPersistsBriefAndOpenQuestions(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	threadID, err := rig.rt.OpenTopic(ctx, "proj-1", "标题", "需求")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}

	brief := "## 目标\n把登录流程理顺\n## 待定问题\n- 要不要保留旧接口（推荐：保留）"
	if err := rig.rt.UpdateBrief(ctx, threadID, brief, 1); err != nil {
		t.Fatalf("写任务书失败: %v", err)
	}
	topic, ok, err := rig.rt.Topic(ctx, threadID)
	if err != nil || !ok {
		t.Fatalf("读话题失败: ok=%v err=%v", ok, err)
	}
	if topic.Brief != brief {
		t.Errorf("任务书 = %q，期望原样保存", topic.Brief)
	}
	if topic.OpenQuestions != 1 {
		t.Errorf("待定问题 = %d，期望 1", topic.OpenQuestions)
	}

	if err := rig.rt.UpdateBrief(ctx, threadID, "  ", 0); err == nil {
		t.Error("空任务书应当报错")
	}
	if err := rig.rt.UpdateBrief(ctx, "not-a-topic", brief, 0); err == nil {
		t.Error("非话题会话写任务书应当报错")
	}
}

// TestConfirmTopicCreatesGoal 校验确认开工时建目标的参数与写回的 GoalID。
func TestConfirmTopicCreatesGoal(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	threadID, err := rig.rt.OpenTopic(ctx, "proj-1", "标题", "需求")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	if err := rig.rt.ConfirmTopic(ctx, threadID); err == nil {
		t.Fatal("任务书为空时应当拒绝确认")
	}
	if len(board.created) != 0 {
		t.Fatal("任务书为空时不该建目标")
	}

	brief := "## 目标\n把登录流程理顺"
	if err := rig.rt.UpdateBrief(ctx, threadID, brief, 0); err != nil {
		t.Fatalf("写任务书失败: %v", err)
	}
	if err := rig.rt.ConfirmTopic(ctx, threadID); err != nil {
		t.Fatalf("确认开工失败: %v", err)
	}
	if len(board.created) != 1 {
		t.Fatalf("应当建一个目标，实际 %d 个", len(board.created))
	}
	opts := board.created[0]
	if opts.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q，期望 proj-1", opts.ProjectID)
	}
	if opts.Prompt != brief {
		t.Errorf("Prompt = %q，期望任务书全文", opts.Prompt)
	}
	if opts.PlanThreadID != threadID {
		t.Errorf("PlanThreadID = %q，期望话题会话 %q", opts.PlanThreadID, threadID)
	}
	if opts.Owner != TopicGoalOwner {
		t.Errorf("Owner = %q，期望 %q", opts.Owner, TopicGoalOwner)
	}
	if opts.Policy["previewPlan"] != true || opts.Policy["autoAccept"] != false {
		t.Errorf("Policy = %+v，期望 previewPlan=true autoAccept=false", opts.Policy)
	}

	topic, ok, err := rig.rt.Topic(ctx, threadID)
	if err != nil || !ok {
		t.Fatalf("读话题失败: ok=%v err=%v", ok, err)
	}
	if topic.GoalID != "goal-1" {
		t.Errorf("GoalID = %q，期望 goal-1", topic.GoalID)
	}
	if phase := rig.rt.TopicPhase(ctx, topic); phase != PhasePlanning {
		t.Errorf("阶段 = %q，期望 %q", phase, PhasePlanning)
	}

	// 重复确认要拦住，否则会给同一个话题再建一个目标。
	if err := rig.rt.ConfirmTopic(ctx, threadID); err == nil {
		t.Error("重复确认应当报错")
	}
	if len(board.created) != 1 {
		t.Errorf("重复确认不该再建目标，实际 %d 个", len(board.created))
	}

	messages := rig.sessions.messages()
	last := messages[len(messages)-1]
	if last.ThreadID != threadID || !strings.Contains(last.Text, "用户已确认任务书") {
		t.Errorf("确认后应通知话题会话，实际: %+v", last)
	}
}

// TestStartTopicGoalOnlyInPlanReview 校验只有计划待审时才能开工。
func TestStartTopicGoalOnlyInPlanReview(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", GoalID: "goal-1"})
	board.goals = []store.Goal{{ID: "goal-1", Status: string(kanban.GoalStatusPlanning), Revision: 1}}

	if err := rig.rt.StartTopicGoal(ctx, "topic-1", "拆分没问题"); err == nil {
		t.Fatal("规划中不该允许开工")
	}
	if len(board.started) != 0 {
		t.Fatal("规划中不该调用 StartGoal")
	}

	board.goals[0].Status = string(kanban.GoalStatusPlanReview)
	if err := rig.rt.StartTopicGoal(ctx, "topic-1", "拆分没问题"); err != nil {
		t.Fatalf("计划待审时应当允许开工: %v", err)
	}
	if len(board.started) != 1 || board.started[0] != "goal-1" {
		t.Errorf("开工调用不对: %+v", board.started)
	}

	if err := rig.rt.StartTopicGoal(ctx, "not-a-topic", "理由"); err == nil {
		t.Error("非话题会话开工应当报错")
	}
}

// TestCloseTopicMarksClosed 校验关闭话题后仍然可读，只是标记为已关闭。
func TestCloseTopicMarksClosed(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	threadID, err := rig.rt.OpenTopic(ctx, "proj-1", "标题", "需求")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	if err := rig.rt.CloseTopic(ctx, threadID); err != nil {
		t.Fatalf("关闭话题失败: %v", err)
	}
	topic, ok, err := rig.rt.Topic(ctx, threadID)
	if err != nil || !ok {
		t.Fatalf("关闭后话题应当还在: ok=%v err=%v", ok, err)
	}
	if !topic.Closed {
		t.Error("话题应当标记为已关闭")
	}
	if err := rig.rt.CloseTopic(ctx, "not-a-topic"); err == nil {
		t.Error("非话题会话关闭应当报错")
	}
}

// TestTopicsOrderedByUpdatedAt 校验列表按最近变化倒序。
func TestTopicsOrderedByUpdatedAt(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	first, err := rig.rt.OpenTopic(ctx, "proj-1", "第一件", "需求一")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	rig.clk.Advance(1)
	if _, err := rig.rt.OpenTopic(ctx, "proj-1", "第二件", "需求二"); err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	rig.clk.Advance(1)
	if err := rig.rt.UpdateBrief(ctx, first, "## 目标\n需求一", 0); err != nil {
		t.Fatalf("写任务书失败: %v", err)
	}

	topics, err := rig.rt.Topics(ctx)
	if err != nil {
		t.Fatalf("读话题列表失败: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("应当有两条话题，实际 %d 条", len(topics))
	}
	if topics[0].ThreadID != first {
		t.Errorf("最近改过的话题应当排第一，实际 %q", topics[0].ThreadID)
	}
}

// TestWatchTopicsNotifiesOnChange 校验订阅者只在话题变化时被通知，且取消后不再通知。
func TestWatchTopicsNotifiesOnChange(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	var calls int
	cancel := rig.rt.WatchTopics(func() { calls++ })
	if cancel == nil {
		t.Fatal("订阅应当返回取消函数")
	}
	threadID, err := rig.rt.OpenTopic(ctx, "proj-1", "标题", "需求")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("开话题后应当通知一次，实际 %d 次", calls)
	}
	if err := rig.rt.UpdateBrief(ctx, threadID, "## 目标\n需求", 0); err != nil {
		t.Fatalf("写任务书失败: %v", err)
	}
	if calls != 2 {
		t.Fatalf("写任务书后应当再通知一次，实际 %d 次", calls)
	}
	cancel()
	if err := rig.rt.CloseTopic(ctx, threadID); err != nil {
		t.Fatalf("关闭话题失败: %v", err)
	}
	if calls != 2 {
		t.Errorf("取消订阅后不该再通知，实际 %d 次", calls)
	}
}

// TestTopicIdentitySectionPhases 校验各阶段的身份段带着该阶段的指令。
func TestTopicIdentitySectionPhases(t *testing.T) {
	rig, board := newTopicRig(t)
	seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", Title: "整理登录流程"})

	cases := []struct {
		name    string
		goal    string
		want    string
		notWant string
	}{
		{name: "追问中", want: "## 当前阶段：追问中", notWant: "## 当前阶段：执行中"},
		{name: "规划中", goal: string(kanban.GoalStatusPlanning), want: "## 当前阶段：规划中", notWant: "## 当前阶段：追问中"},
		{name: "计划待审", goal: string(kanban.GoalStatusPlanReview), want: "steward_start_goal", notWant: "## 当前阶段：追问中"},
		{name: "执行中", goal: string(kanban.GoalStatusRunning), want: "kanban_task_diff", notWant: "## 当前阶段：追问中"},
		{name: "已完成", goal: string(kanban.GoalStatusCompleted), want: "结项报告", notWant: "## 当前阶段：追问中"},
		{name: "已暂停", goal: string(kanban.GoalStatusPaused), want: "## 当前阶段：已暂停", notWant: "## 当前阶段：追问中"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			board.goals = nil
			if c.goal != "" {
				board.goals = []store.Goal{{ID: "goal-1", Status: c.goal, Revision: 1}}
				seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", Title: "整理登录流程", GoalID: "goal-1"})
			} else {
				seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", Title: "整理登录流程"})
			}
			section := rig.rt.IdentitySection("topic-1")
			if !strings.Contains(section, c.want) {
				t.Errorf("身份段缺少 %q:\n%s", c.want, section)
			}
			if c.notWant != "" && strings.Contains(section, c.notWant) {
				t.Errorf("身份段不该包含 %q", c.notWant)
			}
			if !strings.Contains(section, "整理登录流程") || !strings.Contains(section, "proj-1") {
				t.Errorf("身份段应当说明话题与项目:\n%s", section)
			}
		})
	}
	if section := rig.rt.IdentitySection("other-thread"); section != "" {
		t.Errorf("普通会话不该有身份段，实际: %s", section)
	}
	if section := rig.rt.IdentitySection(""); section != "" {
		t.Errorf("空会话 ID 不该有身份段，实际: %s", section)
	}
}

// TestMainIdentitySectionKeepsStewardBrief 校验总管主会话的身份段仍然包含收需求与开话题工具。
func TestMainIdentitySectionKeepsStewardBrief(t *testing.T) {
	rig, _ := newTopicRig(t)
	if _, err := rig.rt.Ensure(context.Background()); err != nil {
		t.Fatalf("创建总管会话失败: %v", err)
	}
	section := rig.rt.IdentitySection(rig.rt.ThreadID())
	if !strings.Contains(section, "steward_open_topic") {
		t.Errorf("主会话身份段应当提到 steward_open_topic:\n%s", section)
	}
	if !strings.Contains(section, "收需求") {
		t.Errorf("主会话身份段应当包含收需求职责:\n%s", section)
	}
	if strings.Contains(section, "## 当前阶段") {
		t.Error("主会话身份段不该带话题阶段指令")
	}
}

// TestTopicPhaseFallsBackWithoutGoal 校验读不到目标时按追问中处理。
func TestTopicPhaseFallsBackWithoutGoal(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	board.boardErr = errors.New("看板不可用")
	topic := Topic{ThreadID: "topic-1", GoalID: "goal-1"}
	if phase := rig.rt.TopicPhase(ctx, topic); phase != PhaseDrafting {
		t.Errorf("阶段 = %q，期望 %q", phase, PhaseDrafting)
	}
}

// countingTopicSettings 在 fakeSettings 上记录写入次数，用来断言「无改动时不写回」。
type countingTopicSettings struct {
	*fakeSettings
	writes int
}

// SetSetting 记录一次写入后再交给 fakeSettings。
func (c *countingTopicSettings) SetSetting(ctx context.Context, key, value string) error {
	c.writes++
	return c.fakeSettings.SetSetting(ctx, key, value)
}

// TestAutoCloseReceiptedClosesStaleReceiptedTopics 校验自动结项只关闭「已回执且超过阈值」的话题，
// 返回值等于关闭条数，没有改动时不整表写回，阈值参数生效。
func TestAutoCloseReceiptedClosesStaleReceiptedTopics(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	counted := &countingTopicSettings{fakeSettings: rig.settings}
	rig.rt.settings = counted

	now := rig.clk.Now()
	stale := now.Add(-DefaultAutoCloseAfter - time.Hour)
	fresh := now.Add(-time.Hour)
	topics := []Topic{
		{ThreadID: "t-stale", ProjectID: "proj-1", Title: "久已回执", ReceiptAt: &stale, CreatedAt: stale, UpdatedAt: stale},
		{ThreadID: "t-fresh", ProjectID: "proj-1", Title: "刚回执", ReceiptAt: &fresh, CreatedAt: fresh, UpdatedAt: fresh},
		{ThreadID: "t-none", ProjectID: "proj-1", Title: "未回执", CreatedAt: now, UpdatedAt: now},
		{ThreadID: "t-closed", ProjectID: "proj-1", Title: "已结项", Closed: true, ReceiptAt: &stale, CreatedAt: stale, UpdatedAt: stale},
	}
	payload, err := json.Marshal(topics)
	if err != nil {
		t.Fatalf("序列化话题失败: %v", err)
	}
	rig.settings.set(settingTopics, string(payload))
	counted.writes = 0

	closed, err := rig.rt.AutoCloseReceipted(ctx, 0)
	if err != nil {
		t.Fatalf("自动结项失败: %v", err)
	}
	if closed != 1 {
		t.Fatalf("应当只关闭 1 条话题，实际 %d", closed)
	}
	if counted.writes != 1 {
		t.Fatalf("有关闭动作时应当整表写回一次，实际写 %d 次", counted.writes)
	}

	got, err := rig.rt.Topics(ctx)
	if err != nil {
		t.Fatalf("读话题失败: %v", err)
	}
	byThread := make(map[string]Topic, len(got))
	for _, topic := range got {
		byThread[topic.ThreadID] = topic
	}
	if !byThread["t-stale"].Closed {
		t.Error("超过阈值的话题应当自动结项")
	}
	if !byThread["t-stale"].UpdatedAt.Equal(now) {
		t.Errorf("自动结项后 UpdatedAt = %v，期望 %v", byThread["t-stale"].UpdatedAt, now)
	}
	if byThread["t-fresh"].Closed {
		t.Error("刚回执的话题不应结项")
	}
	if byThread["t-none"].Closed {
		t.Error("未回执的话题不应结项")
	}

	// 再跑一次没有可关闭的话题：返回 0 且不再写回。
	closed, err = rig.rt.AutoCloseReceipted(ctx, 0)
	if err != nil {
		t.Fatalf("再次自动结项失败: %v", err)
	}
	if closed != 0 {
		t.Fatalf("没有可关闭的话题时应返回 0，实际 %d", closed)
	}
	if counted.writes != 1 {
		t.Fatalf("无改动时不应写回，实际写 %d 次", counted.writes)
	}

	// 阈值缩短到 30 分钟：刚回执 1 小时的话题也进入自动结项范围。
	closed, err = rig.rt.AutoCloseReceipted(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("缩短阈值后自动结项失败: %v", err)
	}
	if closed != 1 {
		t.Fatalf("缩短阈值后应当关闭 1 条话题，实际 %d", closed)
	}
	if topic, ok, err := rig.rt.Topic(ctx, "t-fresh"); err != nil || !ok || !topic.Closed {
		t.Errorf("刚回过 1 小时的话题在 30 分钟阈值下应当结项: ok=%v err=%v closed=%v", ok, err, topic.Closed)
	}
}

// seedTopics 把多条话题一次性写进设置。
func seedTopics(t *testing.T, rig *stewardRig, topics ...Topic) {
	t.Helper()
	payload, err := json.Marshal(topics)
	if err != nil {
		t.Fatalf("序列化话题失败: %v", err)
	}
	rig.settings.set(settingTopics, string(payload))
}

// TestPhaseFromGoalStatusPlanFailed 校验规划失败是独立阶段，不再混进规划中。
func TestPhaseFromGoalStatusPlanFailed(t *testing.T) {
	if got := PhaseFromGoalStatus(string(kanban.GoalStatusPlanFailed)); got != PhasePlanFailed {
		t.Fatalf("阶段 = %q，期望 %q", got, PhasePlanFailed)
	}
	if got := PhaseFromGoalStatus(string(kanban.GoalStatusPlanning)); got != PhasePlanning {
		t.Fatalf("规划中阶段 = %q，期望 %q", got, PhasePlanning)
	}
	if label := phaseLabel(PhasePlanFailed); label != "规划失败" {
		t.Fatalf("阶段中文名 = %q，期望「规划失败」", label)
	}
}

// TestConfirmTopicAdoptsExistingGoal 校验看板上已有同 PlanThreadID 的目标时直接接回，不重复建目标。
func TestConfirmTopicAdoptsExistingGoal(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	planThreadID := "topic-1"
	seedTopic(t, rig, Topic{ThreadID: planThreadID, ProjectID: "proj-1", Title: "标题", Brief: "## 目标\n做事"})
	board.goals = []store.Goal{{
		ID:           "goal-existing",
		ProjectID:    "proj-1",
		Status:       string(kanban.GoalStatusPlanning),
		Owner:        TopicGoalOwner,
		PlanThreadID: &planThreadID,
		Revision:     1,
	}}

	if err := rig.rt.ConfirmTopic(ctx, planThreadID); err != nil {
		t.Fatalf("确认开工失败: %v", err)
	}
	if len(board.created) != 0 {
		t.Fatalf("已有目标时不该再建目标，实际建了 %d 个", len(board.created))
	}
	topic, ok, err := rig.rt.Topic(ctx, planThreadID)
	if err != nil || !ok {
		t.Fatalf("读话题失败: ok=%v err=%v", ok, err)
	}
	if topic.GoalID != "goal-existing" {
		t.Errorf("GoalID = %q，期望接回 goal-existing", topic.GoalID)
	}
	if messages := rig.sessions.messages(); len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Text, "用户已确认任务书") {
		t.Error("接回已有目标后仍应通知话题会话")
	}
}

// TestConfirmTopicConcurrentCreatesOneGoal 校验连点两次确认只产生一个目标，第二次被拒绝。
func TestConfirmTopicConcurrentCreatesOneGoal(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	threadID, err := rig.rt.OpenTopic(ctx, "proj-1", "标题", "需求")
	if err != nil {
		t.Fatalf("开话题失败: %v", err)
	}
	if err := rig.rt.UpdateBrief(ctx, threadID, "## 目标\n做事", 0); err != nil {
		t.Fatalf("写任务书失败: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = rig.rt.ConfirmTopic(ctx, threadID)
		}(i)
	}
	wg.Wait()

	okCount := 0
	for _, err := range errs {
		if err == nil {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("应当只有一次确认成功，实际 %d 次（errs=%v）", okCount, errs)
	}
	if len(board.created) != 1 {
		t.Fatalf("连点两次只应建一个目标，实际 %d 个", len(board.created))
	}
}

// TestUpdateTopicConcurrentKeepsAllWrites 校验并发改不同话题时全部改动都保留。
func TestUpdateTopicConcurrentKeepsAllWrites(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	const n = 8
	topics := make([]Topic, 0, n)
	for i := 0; i < n; i++ {
		topics = append(topics, Topic{ThreadID: fmt.Sprintf("topic-%d", i), ProjectID: "proj-1", Title: "标题"})
	}
	seedTopics(t, rig, topics...)

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = rig.rt.UpdateBrief(ctx, fmt.Sprintf("topic-%d", i), fmt.Sprintf("## 目标\n任务 %d", i), 0)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 条并发写入失败: %v", i, err)
		}
	}

	got, err := rig.rt.Topics(ctx)
	if err != nil {
		t.Fatalf("读话题失败: %v", err)
	}
	if len(got) != n {
		t.Fatalf("应当保留 %d 条话题，实际 %d 条", n, len(got))
	}
	for _, topic := range got {
		want := fmt.Sprintf("## 目标\n任务 %s", strings.TrimPrefix(topic.ThreadID, "topic-"))
		if topic.Brief != want {
			t.Errorf("话题 %s 的任务书 = %q，期望 %q（并发写丢了改动）", topic.ThreadID, topic.Brief, want)
		}
	}
}

// TestReplanTopicRejectsWrongStatus 校验只有规划失败才允许重新规划，成功后目标回到规划中。
func TestReplanTopicRejectsWrongStatus(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", GoalID: "goal-1"})
	board.goals = []store.Goal{{ID: "goal-1", Status: string(kanban.GoalStatusRunning), Revision: 1}}

	if err := rig.rt.ReplanTopic(ctx, "topic-1"); err == nil {
		t.Fatal("执行中的目标应当拒绝重新规划")
	}
	if len(board.replanned) != 0 {
		t.Fatalf("状态不对时不该调用 ReplanGoal，实际 %+v", board.replanned)
	}

	board.goals[0].Status = string(kanban.GoalStatusPlanFailed)
	if err := rig.rt.ReplanTopic(ctx, "topic-1"); err != nil {
		t.Fatalf("规划失败时应当允许重新规划: %v", err)
	}
	if len(board.replanned) != 1 || board.replanned[0] != "goal-1" {
		t.Errorf("重新规划调用不对: %+v", board.replanned)
	}

	seedTopic(t, rig, Topic{ThreadID: "topic-2", ProjectID: "proj-1"})
	if err := rig.rt.ReplanTopic(ctx, "topic-2"); err == nil {
		t.Error("没有目标的话题应当拒绝重新规划")
	}
	if err := rig.rt.ReplanTopic(ctx, "not-a-topic"); err == nil {
		t.Error("非话题会话应当拒绝重新规划")
	}
}

// TestDeleteTopicUnknownThreadIsNoop 校验删不存在的话题不算错，空 ID 报错。
func TestDeleteTopicUnknownThreadIsNoop(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1"})

	deleted, err := rig.rt.DeleteTopic(ctx, "topic-不存在")
	if err != nil || deleted {
		t.Fatalf("不存在的话题应当返回 (false, nil)，实际 deleted=%v err=%v", deleted, err)
	}
	if len(board.cancelled) != 0 {
		t.Errorf("不存在的话题不该碰看板: %+v", board.cancelled)
	}
	if topics, err := rig.rt.Topics(ctx); err != nil || len(topics) != 1 {
		t.Errorf("已有话题不该被改动: len=%d err=%v", len(topics), err)
	}
	if _, err := rig.rt.DeleteTopic(ctx, "   "); err == nil {
		t.Error("空话题 ID 应当报错")
	}
}

// TestDeleteTopicRemovesRowAndLedgerEntries 校验话题行与账本条目一起消失。
func TestDeleteTopicRemovesRowAndLedgerEntries(t *testing.T) {
	rig, _ := newTopicRig(t)
	ctx := context.Background()
	seedTopics(t, rig,
		Topic{ThreadID: "topic-1", ProjectID: "proj-1", Title: "要删的"},
		Topic{ThreadID: "topic-2", ProjectID: "proj-1", Title: "留下的"},
	)
	now := rig.clk.Now()
	if err := rig.rt.saveLedger(ctx, []LedgerEntry{
		{ID: "e1", Section: LedgerInProgress, Text: "要删的一条", TopicThreadID: "topic-1", CreatedAt: now, UpdatedAt: now},
		{ID: "e2", Section: LedgerDecision, Text: "要删的另一条", TopicThreadID: "topic-1", CreatedAt: now, UpdatedAt: now},
		{ID: "e3", Section: LedgerInProgress, Text: "留下的", TopicThreadID: "topic-2", CreatedAt: now, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("写账本失败: %v", err)
	}

	deleted, err := rig.rt.DeleteTopic(ctx, "topic-1")
	if err != nil || !deleted {
		t.Fatalf("删除话题失败: deleted=%v err=%v", deleted, err)
	}

	topics, err := rig.rt.Topics(ctx)
	if err != nil {
		t.Fatalf("读话题失败: %v", err)
	}
	if len(topics) != 1 || topics[0].ThreadID != "topic-2" {
		t.Fatalf("话题行应当只剩 topic-2，实际 %+v", topics)
	}
	if rig.rt.IsTopic("topic-1") {
		t.Error("已删除的话题不该再被当成话题")
	}
	entries, err := rig.rt.Ledger(ctx)
	if err != nil {
		t.Fatalf("读账本失败: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "e3" {
		t.Fatalf("账本应当只剩 e3，实际 %+v", entries)
	}
}

// TestDeleteTopicCancelsGoalAndInterruptsRunningTasks 校验删除话题时取消目标并中断在跑的任务。
func TestDeleteTopicCancelsGoalAndInterruptsRunningTasks(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", GoalID: "goal-1"})
	board.goals = []store.Goal{{ID: "goal-1", Status: string(kanban.GoalStatusRunning), Revision: 3}}

	goalID := "goal-1"
	otherGoalID := "goal-2"
	threadID := "task-thread-1"
	board.tasks = []kanban.TaskView{
		{Task: store.Task{ID: "task-running", GoalID: &goalID, Status: string(kanban.TaskStatusInProgress), ThreadID: &threadID}},
		{Task: store.Task{ID: "task-approval", GoalID: &goalID, Status: string(kanban.TaskStatusNeedsApproval)}},
		{Task: store.Task{ID: "task-done", GoalID: &goalID, Status: string(kanban.TaskStatusDone)}},
		{Task: store.Task{ID: "task-other", GoalID: &otherGoalID, Status: string(kanban.TaskStatusInProgress)}},
	}

	deleted, err := rig.rt.DeleteTopic(ctx, "topic-1")
	if err != nil || !deleted {
		t.Fatalf("删除话题失败: deleted=%v err=%v", deleted, err)
	}
	if len(board.cancelled) != 1 || board.cancelled[0] != "goal-1" {
		t.Fatalf("应当取消 goal-1，实际 %+v", board.cancelled)
	}
	if len(board.interrupted) != 2 || board.interrupted[0] != "task-running" || board.interrupted[1] != "task-approval" {
		t.Fatalf("应当中断在跑与等审批的两个任务，实际 %+v", board.interrupted)
	}
}

// TestDeleteTopicKeepsCompletedGoal 校验已完成的目标不取消，只删话题。
func TestDeleteTopicKeepsCompletedGoal(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	seedTopic(t, rig, Topic{ThreadID: "topic-1", ProjectID: "proj-1", GoalID: "goal-1"})
	board.goals = []store.Goal{{ID: "goal-1", Status: string(kanban.GoalStatusCompleted), Revision: 2}}

	if _, err := rig.rt.DeleteTopic(ctx, "topic-1"); err != nil {
		t.Fatalf("删除话题失败: %v", err)
	}
	if len(board.cancelled) != 0 {
		t.Errorf("已完成的目标不该被取消: %+v", board.cancelled)
	}
}
