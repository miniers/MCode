package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const stewardTopicsMainThreadID = "steward-main"

type stewardStartCall struct {
	projectID string
	request   string
}

// stewardTopicsFakeClient 在总管假客户端上补齐话题接口：话题列表可配置，
// 确认、结项与新建都记录调用，方便断言按钮真的走到了客户端。
type stewardTopicsFakeClient struct {
	*stewardFakeClient

	topicsMu  sync.Mutex
	topics    []StewardTopic
	topicsErr error

	confirmCalls   []string
	closeCalls     []string
	deleteCalls    []string
	replanCalls    []string
	startGoalCalls []string
	startCalls     []stewardStartCall
	startThreadID  string
	startErr       error
	deleteErr      error
	confirmErr     error

	watchMu  sync.Mutex
	watchFns []func()
}

var _ StewardTopicsClient = (*stewardTopicsFakeClient)(nil)

func newStewardTopicsFake(topics ...StewardTopic) *stewardTopicsFakeClient {
	fake := &stewardTopicsFakeClient{stewardFakeClient: newStewardFakeClient(stewardTopicsMainThreadID)}
	fake.topics = topics
	fake.startThreadID = "topic-new"
	return fake
}

// newStewardTopicsFixture 造两个项目：p-home 放总管主会话，p1 放普通会话与话题会话。
// 话题会话在项目树里的标题故意不同，用来验证它不会从项目树里漏出来。
func newStewardTopicsFixture(topics ...StewardTopic) *stewardTopicsFakeClient {
	now := time.Now()
	fake := newStewardTopicsFake(topics...)
	threads := map[string][]Thread{
		"p-home": {{ID: stewardTopicsMainThreadID, ProjectID: "p-home", Title: "总管会话", UpdatedAt: now.Add(-2 * time.Hour)}},
		"p1": {
			{ID: "t-plain", ProjectID: "p1", Title: "普通会话", UpdatedAt: now.Add(-3 * time.Hour)},
			{ID: "topic-new", ProjectID: "p1", Title: "刚建好的话题会话", UpdatedAt: now.Add(-4 * time.Hour)},
		},
	}
	for i, topic := range topics {
		if topic.ThreadID == "" {
			continue
		}
		threads["p1"] = append(threads["p1"], Thread{
			ID:        topic.ThreadID,
			ProjectID: "p1",
			Title:     "项目树里的" + topic.ThreadID,
			UpdatedAt: now.Add(-time.Duration(i+5) * time.Minute),
		})
	}
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p-home", Name: "总管的家", Path: "/path/home"},
			{ID: "p1", Name: "普通项目", Path: "/path/p1"},
		},
		threads,
	)
	return fake
}

func (f *stewardTopicsFakeClient) StewardTopics(_ context.Context) ([]StewardTopic, error) {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	if f.topicsErr != nil {
		return nil, f.topicsErr
	}
	return append([]StewardTopic(nil), f.topics...), nil
}

func (f *stewardTopicsFakeClient) StartStewardTopic(_ context.Context, projectID, request string) (string, error) {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.startCalls = append(f.startCalls, stewardStartCall{projectID: projectID, request: request})
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.startThreadID, nil
}

func (f *stewardTopicsFakeClient) ConfirmStewardTopic(_ context.Context, threadID string) error {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.confirmCalls = append(f.confirmCalls, threadID)
	return f.confirmErr
}

func (f *stewardTopicsFakeClient) CloseStewardTopic(_ context.Context, threadID string) error {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.closeCalls = append(f.closeCalls, threadID)
	return nil
}

func (f *stewardTopicsFakeClient) DeleteStewardTopic(_ context.Context, threadID string) error {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.deleteCalls = append(f.deleteCalls, threadID)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	// 删除成功时同步把它从列表里拿掉，重拉之后话题就从界面消失。
	kept := make([]StewardTopic, 0, len(f.topics))
	for _, topic := range f.topics {
		if topic.ThreadID != threadID {
			kept = append(kept, topic)
		}
	}
	f.topics = kept
	return nil
}

func (f *stewardTopicsFakeClient) ReplanStewardTopic(_ context.Context, threadID string) error {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.replanCalls = append(f.replanCalls, threadID)
	return nil
}

func (f *stewardTopicsFakeClient) StartStewardTopicGoal(_ context.Context, threadID string) error {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.startGoalCalls = append(f.startGoalCalls, threadID)
	return nil
}

// failDelete 让后续的删除话题调用返回错误，用来验证失败时弹窗不关。
func (f *stewardTopicsFakeClient) failDelete(err error) {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.deleteErr = err
}

// failConfirm 让后续的确认开工调用返回错误，用来验证失败文案。
func (f *stewardTopicsFakeClient) failConfirm(err error) {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.confirmErr = err
}

// failTopicsLoad 让后续的话题列表拉取失败，用来验证失败提示与重试。
func (f *stewardTopicsFakeClient) failTopicsLoad(err error) {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.topicsErr = err
}

// healTopicsLoad 恢复话题列表拉取成功。
func (f *stewardTopicsFakeClient) healTopicsLoad() {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	f.topicsErr = nil
}

func (f *stewardTopicsFakeClient) deletedThreads() []string {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	return append([]string(nil), f.deleteCalls...)
}

func (f *stewardTopicsFakeClient) replannedThreads() []string {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	return append([]string(nil), f.replanCalls...)
}

func (f *stewardTopicsFakeClient) startedGoalThreads() []string {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	return append([]string(nil), f.startGoalCalls...)
}

func (f *stewardTopicsFakeClient) WatchStewardTopics(fn func()) func() {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	f.watchFns = append(f.watchFns, fn)
	return func() {}
}

func (f *stewardTopicsFakeClient) confirmedThreads() []string {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	return append([]string(nil), f.confirmCalls...)
}

func (f *stewardTopicsFakeClient) closedThreads() []string {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	return append([]string(nil), f.closeCalls...)
}

func (f *stewardTopicsFakeClient) startedCalls() []stewardStartCall {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	return append([]stewardStartCall(nil), f.startCalls...)
}

func menuContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// 侧栏状态小字按阶段区分；drafting 有追问时显示剩余问题数。
func TestStewardTopicSidebarStatusText(t *testing.T) {
	now := time.Now()
	fake := newStewardTopicsFixture(
		StewardTopic{ThreadID: "t-draft", ProjectID: "p1", ProjectName: "普通项目", Title: "起草中的话题", Brief: "写个任务书", OpenQuestions: 2, Phase: StewardTopicDrafting, UpdatedAt: now},
		StewardTopic{ThreadID: "t-plan", ProjectID: "p1", ProjectName: "普通项目", Title: "规划中的话题", Phase: StewardTopicPlanning, UpdatedAt: now.Add(-time.Minute)},
		StewardTopic{ThreadID: "t-review", ProjectID: "p1", ProjectName: "普通项目", Title: "审计划的话题", Phase: StewardTopicPlanReview, UpdatedAt: now.Add(-2 * time.Minute)},
		StewardTopic{ThreadID: "t-run", ProjectID: "p1", ProjectName: "普通项目", Title: "执行中的话题", Phase: StewardTopicRunning, TasksDone: 3, TasksTotal: 5, PendingUser: 1, UpdatedAt: now.Add(-3 * time.Minute)},
		StewardTopic{ThreadID: "t-pause", ProjectID: "p1", ProjectName: "普通项目", Title: "暂停的话题", Phase: StewardTopicPaused, UpdatedAt: now.Add(-4 * time.Minute)},
		StewardTopic{ThreadID: "t-done", ProjectID: "p1", ProjectName: "普通项目", Title: "做完的话题", Phase: StewardTopicCompleted, UpdatedAt: now.Add(-5 * time.Minute)},
		StewardTopic{ThreadID: "t-cancel", ProjectID: "p1", ProjectName: "普通项目", Title: "取消的话题", Phase: StewardTopicCancelled, UpdatedAt: now.Add(-6 * time.Minute)},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	want := []string{
		"还有 2 个问题要问你",
		"正在出方案",
		"方案等你过目",
		"3/5 · 1 项待你确认",
		"已暂停",
		"做完了，等你验收",
		"已取消",
	}
	waitFor(t, tt, func() bool {
		for _, text := range want {
			if !tt.HasText(text) {
				return false
			}
		}
		return true
	})
	if tt.HasText("任务书待确认") {
		t.Fatalf("OpenQuestions>0 时不应再显示任务书待确认，当前文本: %q", tt.Texts())
	}
}

// 已结项的话题收进「历史 N」一行，点一下展开、再点收起。
// 自动打开只发生在「用户正停在总管主对话」且有新话题时：首次加载、其他会话、已关闭话题都不跳。
func TestStewardTopicToAutoOpen(t *testing.T) {
	prev := []StewardTopic{{ThreadID: "t-old"}}
	cases := []struct {
		name      string
		prev      []StewardTopic
		next      []StewardTopic
		loaded    bool
		selected  string
		stewardID string
		want      string
	}{
		{name: "首次加载不跳", next: []StewardTopic{{ThreadID: "t-new"}}, selected: "steward-main", stewardID: "steward-main"},
		{name: "不在主对话不跳", prev: prev, next: []StewardTopic{{ThreadID: "t-old"}, {ThreadID: "t-new"}}, loaded: true, selected: "t-old", stewardID: "steward-main"},
		{name: "在主对话且有新话题时跳", prev: prev, next: []StewardTopic{{ThreadID: "t-old"}, {ThreadID: "t-new"}}, loaded: true, selected: "steward-main", stewardID: "steward-main", want: "t-new"},
		{name: "新话题已关闭不跳", prev: prev, next: []StewardTopic{{ThreadID: "t-old"}, {ThreadID: "t-new", Closed: true}}, loaded: true, selected: "steward-main", stewardID: "steward-main"},
		{name: "没有新话题不跳", prev: prev, next: []StewardTopic{{ThreadID: "t-old"}}, loaded: true, selected: "steward-main", stewardID: "steward-main"},
		{name: "总管会话未建立不跳", prev: prev, next: []StewardTopic{{ThreadID: "t-old"}, {ThreadID: "t-new"}}, loaded: true, selected: "steward-main"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stewardTopicToAutoOpen(c.prev, c.next, c.loaded, c.selected, c.stewardID); got != c.want {
				t.Fatalf("自动打开 = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestStewardTopicHistoryCollapses(t *testing.T) {
	now := time.Now()
	fake := newStewardTopicsFixture(
		StewardTopic{ThreadID: "t-run", ProjectID: "p1", ProjectName: "普通项目", Title: "在办话题", Phase: StewardTopicRunning, TasksDone: 1, TasksTotal: 2, UpdatedAt: now},
		StewardTopic{ThreadID: "t-old", ProjectID: "p1", ProjectName: "普通项目", Title: "已结项话题", Phase: StewardTopicCompleted, Closed: true, UpdatedAt: now.Add(-time.Hour)},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("在办话题") && tt.HasText("历史 1")
	})
	if tt.HasText("已结项话题") {
		t.Fatalf("历史默认应收起，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("历史 1"); err != nil {
		t.Fatalf("展开历史失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已结项话题") })
	if err := tt.Click("历史 1"); err != nil {
		t.Fatalf("收起历史失败: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("已结项话题") })
}

// 话题会话只出现在总管段下：项目树里不再重复列出，但项目本身保留。
func TestStewardTopicThreadsHiddenFromProjectTree(t *testing.T) {
	fake := newStewardTopicsFixture(
		StewardTopic{ThreadID: "t-topic", ProjectID: "p1", ProjectName: "普通项目", Title: "侧栏话题", Phase: StewardTopicRunning, UpdatedAt: time.Now()},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool { return tt.HasText("侧栏话题") })
	// 会话行挂的是「打开会话 标题」无障碍标签；标题本身会出现在聊天页标题栏，
	// 所以只能按行标签判断项目树里有没有这一行。
	if tt.HasText("打开会话 项目树里的t-topic") {
		t.Fatalf("话题会话不应出现在项目树里，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("打开会话 普通会话") {
		t.Fatalf("项目树应正常渲染普通会话行，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("普通项目") {
		t.Fatalf("话题所在项目本身应保留，当前文本: %q", tt.Texts())
	}
	if tt.HasText("总管的家") {
		t.Fatalf("总管所在项目不应出现在项目树里，当前文本: %q", tt.Texts())
	}
}

// drafting 阶段的任务书卡片带主按钮；有未定问题时文案改成「按推荐答案开工」，
// 点击先弹确认，确认才真的开工。（确认弹窗的细节断言在 steward_topic_card_test.go）
func TestStewardTopicCardConfirmDrafting(t *testing.T) {
	fake := newStewardTopicsFixture(
		StewardTopic{
			ThreadID:      "t-draft",
			ProjectID:     "p1",
			ProjectName:   "普通项目",
			Title:         "写导出功能",
			Brief:         "## 任务书\n\n把导出做成 CSV。",
			OpenQuestions: 2,
			Phase:         StewardTopicDrafting,
			UpdatedAt:     time.Now(),
		},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-draft" && tt.HasText("按推荐答案开工")
	})
	if !tt.HasText("还有 2 个待定问题，将按推荐答案处理") {
		t.Fatalf("有追问时应显示按推荐答案处理的说明，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("按推荐答案开工"); err != nil {
		t.Fatalf("点击按推荐答案开工失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("按推荐答案开工？") })
	if calls := fake.confirmedThreads(); len(calls) != 0 {
		t.Fatalf("确认前不该开工，实际已调用 %v", calls)
	}
}

// 「审查计划」切到任务板后顶部给一条返回条；点一下回到原来的视图与会话，并清掉记录。
func TestStewardTopicPlanReviewReturnBar(t *testing.T) {
	fake := planReviewFixture(t, StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "待审计划的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicPlanReview,
		GoalID:      "g-plan",
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t-plan" })
	// 换到另一个会话，返回条要能回到这里，而不是审查计划带过去的那个会话。
	state.selectThread(stewardTopicsMainThreadID)
	tt.Frame()

	state.openStewardTopicPlanReview(StewardTopic{ThreadID: "t-plan", ProjectID: "p1", GoalID: "g-plan"})
	if _, _, _, wsView := planReviewState(state); wsView != "tasks" {
		t.Fatalf("审查计划应切到任务视图，实际 %q", wsView)
	}
	returnView, returnThread := stewardPlanReviewOrigin(state)
	if returnView != "chat" || returnThread != stewardTopicsMainThreadID {
		t.Fatalf("应记下原视图与原会话，实际 view=%q thread=%q", returnView, returnThread)
	}

	// 看完计划关掉审查弹窗，任务板顶部就露出返回条。
	state.closeGoalPlanReviewSheet()
	waitFor(t, tt, func() bool { return tt.HasText("← 回到总管的方案") })
	if err := tt.Click("← 回到总管的方案"); err != nil {
		t.Fatalf("点击返回条失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, _, _, wsView := planReviewState(state)
		return wsView == "chat" && selectedThreadForTest(state) == stewardTopicsMainThreadID
	})
	if v, th := stewardPlanReviewOrigin(state); v != "" || th != "" {
		t.Fatalf("返回后应清掉来处记录，实际 view=%q thread=%q", v, th)
	}
}

// 用户自己切走任务板（而不是点返回条）时也清掉来处记录，下一趟打开任务板不会又冒出返回条。
func TestStewardTopicPlanReviewReturnClearedOnLeave(t *testing.T) {
	fake := planReviewFixture(t, StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "待审计划的话题",
		Phase:       StewardTopicPlanReview,
		GoalID:      "g-plan",
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t-plan" })
	state.openStewardTopicPlanReview(StewardTopic{ThreadID: "t-plan", ProjectID: "p1", GoalID: "g-plan"})
	if v, _ := stewardPlanReviewOrigin(state); v == "" {
		t.Fatalf("切到任务板时应记下来处")
	}

	state.closeWorkspace()
	tt.Frame()
	if v, th := stewardPlanReviewOrigin(state); v != "" || th != "" {
		t.Fatalf("离开任务板后应清掉来处记录，实际 view=%q thread=%q", v, th)
	}
}

// completed 且未结项时，卡片显示「确认结项」，点击调用 CloseStewardTopic。
func TestStewardTopicCardCloseCompleted(t *testing.T) {
	fake := newStewardTopicsFixture(
		StewardTopic{
			ThreadID:    "t-done",
			ProjectID:   "p1",
			ProjectName: "普通项目",
			Title:       "收尾的话题",
			Phase:       StewardTopicCompleted,
			UpdatedAt:   time.Now(),
		},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-done" && tt.HasText("确认结项")
	})
	if err := tt.Click("确认结项"); err != nil {
		t.Fatalf("点击确认结项失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.closedThreads()
		return len(calls) == 1 && calls[0] == "t-done"
	})
}

// planReviewFixture 造一个 p1 里的 plan_review 话题，并在看板上配好它对应的目标与任务。
func planReviewFixture(t *testing.T, phaseTopic StewardTopic) *stewardTopicsFakeClient {
	t.Helper()
	fake := newStewardTopicsFixture(phaseTopic)
	fake.SetKanbanBoard(KanbanBoard{
		Goals: []KanbanGoal{{ID: "g-plan", ProjectID: "p1", Title: "导出功能", Status: "plan_review", Revision: 1}},
		Tasks: []KanbanTask{{ID: "task-1", ProjectID: "p1", GoalID: "g-plan", Title: "先写导出", Status: "todo", Column: "todo"}},
	})
	return fake
}

// planReviewState 在锁内读取审查弹窗指向的目标与看板当前项目，避免与后台加载竞争。
func planReviewState(s *chatState) (goalID, projectID string, loading bool, view string) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	goalID, projectID, loading = ks.planReviewGoalID, ks.projectID, ks.loading
	kanbanMu.Unlock()
	ws := getWorkspaceState(s)
	workspaceMu.Lock()
	view = ws.view
	workspaceMu.Unlock()
	return goalID, projectID, loading, view
}

// chooseStewardTopicMenu 点卡片右上角的「更多操作」，再从菜单里选一项。
func chooseStewardTopicMenu(t *testing.T, tt *ui.Tester, item string) {
	t.Helper()
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("点击卡片更多操作失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), item) })
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatalf("选择菜单项 %q 失败: %v", item, err)
	}
}

// plan_review 阶段的卡片只有一个主按钮「直接开工」；「审查计划」收进更多操作菜单，
// 从菜单点开后审查弹窗指向话题的目标。
func TestStewardTopicCardPlanReviewOpensSheet(t *testing.T) {
	fake := planReviewFixture(t, StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "待审计划的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicPlanReview,
		GoalID:      "g-plan",
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-plan" && tt.HasText("直接开工")
	})
	for _, other := range []string{"确认结项", "通过", "打回"} {
		if tt.HasText(other) {
			t.Fatalf("plan_review 卡片不应出现 %q，当前文本: %q", other, tt.Texts())
		}
	}
	if tt.HasText("审查计划") {
		t.Fatalf("审查计划应收进更多操作菜单，不该直接出现在卡片上，当前文本: %q", tt.Texts())
	}
	chooseStewardTopicMenu(t, tt, "审查计划")
	waitFor(t, tt, func() bool { return tt.HasText("第 1 层 · 1 项") && tt.HasText("开始执行") })

	goalID, projectID, _, wsView := planReviewState(state)
	if goalID != "g-plan" {
		t.Fatalf("审查弹窗目标应为 g-plan，实际 %q", goalID)
	}
	if projectID != "p1" || wsView != "tasks" {
		t.Fatalf("应切到 p1 的任务视图，实际 project=%q view=%q", projectID, wsView)
	}
}

// 还没有目标的话题（GoalID 为空）既不渲染主按钮，菜单里也没有「审查计划」。
func TestStewardTopicCardPlanReviewWithoutGoal(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "没有目标的话题",
		Phase:       StewardTopicPlanReview,
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-plan" && tt.HasText("方案等你过目")
	})
	if tt.HasText("审查计划") {
		t.Fatalf("GoalID 为空时不应出现审查计划按钮，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("点击卡片更多操作失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), "删除话题…") })
	if menuContains(tt.Menu(), "审查计划") {
		t.Fatalf("GoalID 为空时菜单里也不该有审查计划，当前菜单: %v", tt.Menu())
	}
}

// 话题正忙时从菜单选「审查计划」也不会打开弹窗。
func TestStewardTopicCardPlanReviewDisabledWhileBusy(t *testing.T) {
	fake := planReviewFixture(t, StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "忙碌中的话题",
		Phase:       StewardTopicPlanReview,
		GoalID:      "g-plan",
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-plan" && tt.HasText("直接开工")
	})
	st := getStewardTopicsState(state)
	st.mu.Lock()
	st.busy["t-plan"] = true
	st.mu.Unlock()
	state.invalidate()
	tt.Frame()

	chooseStewardTopicMenu(t, tt, "审查计划")
	tt.Frame()
	if goalID, _, _, _ := planReviewState(state); goalID != "" {
		t.Fatalf("忙碌时点击不应打开审查弹窗，实际目标 %q", goalID)
	}
}

// plan_review 卡片上点「直接开工」直接调用客户端，不必先打开审查弹窗。
func TestStewardTopicCardPlanReviewStartGoal(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "待审计划的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicPlanReview,
		GoalID:      "g-plan",
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-plan" && tt.HasText("直接开工")
	})
	if err := tt.Click("直接开工"); err != nil {
		t.Fatalf("点击直接开工失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.startedGoalThreads()
		return len(calls) == 1 && calls[0] == "t-plan"
	})
	if goalID, _, _, _ := planReviewState(state); goalID != "" {
		t.Fatalf("直接开工不应打开审查弹窗，实际目标 %q", goalID)
	}
}

// 话题在另一个项目时，从菜单点「审查计划」也要打开该目标的弹窗：切项目会重建看板状态，
// 目标 ID 不能被重建和加载过程清掉。
func TestStewardTopicCardPlanReviewKeepsGoalAcrossProjectSwitch(t *testing.T) {
	fake := planReviewFixture(t, StewardTopic{
		ThreadID:    "t-plan",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "跨项目的话题",
		Phase:       StewardTopicPlanReview,
		GoalID:      "g-plan",
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-plan" && tt.HasText("直接开工")
	})
	// 当前工作区停在另一个项目（总管的家），与话题所在项目不同。
	ws := getWorkspaceState(state)
	workspaceMu.Lock()
	ws.projectID = "p-home"
	workspaceMu.Unlock()
	state.invalidate()
	tt.Frame()

	chooseStewardTopicMenu(t, tt, "审查计划")
	waitFor(t, tt, func() bool { return tt.HasText("第 1 层 · 1 项") && tt.HasText("先写导出") })

	goalID, projectID, loading, wsView := planReviewState(state)
	if goalID != "g-plan" {
		t.Fatalf("跨项目点开后审查弹窗目标仍应为 g-plan，实际 %q", goalID)
	}
	if projectID != "p1" || wsView != "tasks" {
		t.Fatalf("应切到 p1 的任务视图，实际 project=%q view=%q", projectID, wsView)
	}
	if loading {
		t.Fatal("看板应已加载完成")
	}
}

// 项目右键菜单里有「交给总管」，提交后调用 StartStewardTopic 并打开返回的话题会话。
func TestStewardStartTopicMenuAndDialog(t *testing.T) {
	fake := newStewardTopicsFixture()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool { return tt.HasText("普通项目") })
	if err := tt.RightClick("普通项目"); err != nil {
		t.Fatalf("右键项目失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), "交给总管") })
	if err := tt.ChooseMenuItem("交给总管"); err != nil {
		t.Fatalf("选择交给总管失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("交给它") })

	if err := tt.Click("steward-start-input"); err != nil {
		t.Fatalf("点击输入框失败: %v", err)
	}
	tt.Type("把导出做成 CSV")
	if err := tt.Click("交给它"); err != nil {
		t.Fatalf("点击交给它失败: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.startedCalls()) == 1 })
	call := fake.startedCalls()[0]
	if call.projectID != "p1" {
		t.Fatalf("应带上项目 ID p1，实际 %q", call.projectID)
	}
	if call.request != "把导出做成 CSV" {
		t.Fatalf("应提交输入的文本，实际 %q", call.request)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "topic-new" })
}

// 客户端不支持话题接口时，侧栏与聊天页保持原样：没有话题段、没有「交给总管」菜单。
func TestStewardTopicsHiddenWithoutClient(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText("任务书待确认") {
		t.Fatalf("不支持话题接口时不应渲染话题状态，当前文本: %q", tt.Texts())
	}
	if err := tt.RightClick("mcode-test"); err != nil {
		t.Fatalf("右键项目失败: %v", err)
	}
	waitFor(t, tt, func() bool { return len(tt.Menu()) > 0 })
	if menuContains(tt.Menu(), "交给总管") {
		t.Fatalf("不支持话题接口时不应出现交给总管菜单，当前菜单: %q", tt.Menu())
	}
}

// 1. 话题多到超过滚动区上限时，话题行收进独立滚动区，下面的项目树照常留在窗口里。
func TestStewardTopicSidebarScrollKeepsProjectTree(t *testing.T) {
	now := time.Now()
	topics := make([]StewardTopic, 0, stewardTopicsScrollRows*2)
	for i := 0; i < stewardTopicsScrollRows*2; i++ {
		topics = append(topics, StewardTopic{
			ThreadID:    fmt.Sprintf("t-scroll-%d", i),
			ProjectID:   "p1",
			ProjectName: "普通项目",
			Title:       fmt.Sprintf("滚动话题 %d", i),
			Phase:       StewardTopicRunning,
			UpdatedAt:   now.Add(-time.Duration(i) * time.Minute),
		})
	}
	fake := newStewardTopicsFixture(topics...)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	// 窗口故意开矮：话题不收进滚动区时，十行话题会把项目列表挤出可见区域。
	const height = 520
	tt := ui.NewTester(view, 1000, height)

	waitFor(t, tt, func() bool { return tt.HasText("滚动话题 0") })
	waitFor(t, tt, func() bool { return tt.HasText("打开会话 普通会话") })
	row, ok := tt.Find("打开会话 普通会话")
	if !ok {
		t.Fatalf("话题很多时项目树里的普通会话行应仍然存在，当前文本: %q", tt.Texts())
	}
	if row.Y < 0 || row.Y+row.H > float32(height) {
		t.Fatalf("普通会话行被话题挤出窗口：y=%v h=%v 窗口高 %d", row.Y, row.H, height)
	}
}

// 2. 交给总管弹窗按 Esc 关闭：不新建话题，草稿与错误提示一起清掉。
func TestStewardStartTopicDialogClosesOnEscape(t *testing.T) {
	fake := newStewardTopicsFixture()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool { return tt.HasText("普通项目") })
	if err := tt.RightClick("普通项目"); err != nil {
		t.Fatalf("右键项目失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), "交给总管") })
	if err := tt.ChooseMenuItem("交给总管"); err != nil {
		t.Fatalf("选择交给总管失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("交给它") })

	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool { return !tt.HasText("交给它") })
	if calls := fake.startedCalls(); len(calls) != 0 {
		t.Fatalf("Esc 关闭不应发起新建，实际调用 %d 次", len(calls))
	}
	if st := getStewardTopicsState(state); st != nil {
		st.mu.Lock()
		open, draft, errText := st.startOpen, st.startText, st.startErr
		st.mu.Unlock()
		if open || draft != "" || errText != "" {
			t.Fatalf("Esc 关闭应清空弹窗状态，实际 open=%v 草稿=%q 错误=%q", open, draft, errText)
		}
	}
}

// 3. 打回意见弹窗按 Esc 关闭：卡片回到原样，不发起打回。
func TestStewardRejectDialogClosesOnEscape(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-run",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "执行中的话题",
		Phase:       StewardTopicRunning,
		GoalID:      "g1",
		TasksTotal:  1,
		UpdatedAt:   time.Now(),
	})
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID:        "task-1",
		ProjectID: "p1",
		GoalID:    "g1",
		Title:     "写 CSV 导出",
		Status:    "review",
		Revision:  1,
	}}})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-run" && tt.HasText("打回 写 CSV 导出")
	})
	if err := tt.Click("打回 写 CSV 导出"); err != nil {
		t.Fatalf("点击打回失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认打回") })

	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool { return !tt.HasText("确认打回") })
	if !tt.HasText("打回 写 CSV 导出") {
		t.Fatalf("关掉打回弹窗后卡片应回到原样，当前文本: %q", tt.Texts())
	}
}

// 跨项目时按项目分组：组内保持输入顺序，组之间按各组第一条话题排序，缺名回退「未知项目」。
func TestGroupStewardTopicsByProject(t *testing.T) {
	topics := []StewardTopic{
		{ThreadID: "t1", ProjectID: "p1", Title: "一"},
		{ThreadID: "t2", ProjectID: "p2", Title: "二"},
		{ThreadID: "t3", ProjectID: "p1", Title: "三"},
		{ThreadID: "t4", ProjectID: "p-missing", Title: "四"},
	}
	groups := groupStewardTopicsByProject(topics, map[string]string{
		"p1": "第一个项目",
		"p2": "第二个项目",
	})
	if len(groups) != 3 {
		t.Fatalf("分组数 = %d，期望 3: %+v", len(groups), groups)
	}
	if groups[0].ProjectID != "p1" || groups[1].ProjectID != "p2" || groups[2].ProjectID != "p-missing" {
		t.Fatalf("组顺序应按各组第一条话题: %+v", groups)
	}
	if groups[0].ProjectName != "第一个项目" {
		t.Errorf("组名 = %q，期望「第一个项目」", groups[0].ProjectName)
	}
	if groups[2].ProjectName != "未知项目" {
		t.Errorf("找不到项目名时应显示「未知项目」，实际 %q", groups[2].ProjectName)
	}
	if len(groups[0].Topics) != 2 || groups[0].Topics[0].ThreadID != "t1" || groups[0].Topics[1].ThreadID != "t3" {
		t.Errorf("组内应保持输入顺序: %+v", groups[0].Topics)
	}
	if got := groupStewardTopicsByProject(nil, nil); len(got) != 0 {
		t.Errorf("空输入应返回空分组，实际 %+v", got)
	}
}

// 只有一个项目时只返回一组，侧栏据此不加项目名标题。
func TestGroupStewardTopicsByProjectSingleProject(t *testing.T) {
	topics := []StewardTopic{
		{ThreadID: "t1", ProjectID: "p1", Title: "一"},
		{ThreadID: "t2", ProjectID: "p1", Title: "二"},
	}
	groups := groupStewardTopicsByProject(topics, map[string]string{"p1": "唯一项目"})
	if len(groups) != 1 {
		t.Fatalf("单项目应只返回一组，实际 %d 组", len(groups))
	}
	if groups[0].ProjectName != "唯一项目" || len(groups[0].Topics) != 2 {
		t.Fatalf("单项目分组不对: %+v", groups[0])
	}
}

// 侧栏状态小字对规划失败的话题显示「方案没出来」。
func TestStewardTopicStatusTextPlanFailed(t *testing.T) {
	got := stewardTopicStatusText(StewardTopic{Phase: StewardTopicPlanFailed})
	if got != "方案没出来" {
		t.Fatalf("plan_failed 的状态文案 = %q，期望「方案没出来」", got)
	}
}

// 六个阶段的新文案走一张表：改文案时一眼能看出进了哪一句。
func TestStewardTopicStatusTextPhases(t *testing.T) {
	cases := []struct {
		name  string
		topic StewardTopic
		want  string
	}{
		{"追问中", StewardTopic{Phase: StewardTopicDrafting, OpenQuestions: 3}, "还有 3 个问题要问你"},
		{"待确认任务", StewardTopic{Phase: StewardTopicDrafting, Brief: "写点什么"}, "等你确认任务"},
		{"规划中", StewardTopic{Phase: StewardTopicPlanning}, "正在出方案"},
		{"规划失败", StewardTopic{Phase: StewardTopicPlanFailed}, "方案没出来"},
		{"审计划", StewardTopic{Phase: StewardTopicPlanReview}, "方案等你过目"},
		{"待结项", StewardTopic{Phase: StewardTopicCompleted}, "做完了，等你验收"},
	}
	for _, tc := range cases {
		if got := stewardTopicStatusText(tc.topic); got != tc.want {
			t.Fatalf("%s 的状态文案 = %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// 未知阶段兜底展示「处理中」，不把内部枚举原样吐给用户；空阶段按「等你确认任务」。
func TestStewardTopicStatusTextUnknownPhase(t *testing.T) {
	if got := stewardTopicStatusText(StewardTopic{Phase: "some_internal_phase"}); got != "处理中" {
		t.Fatalf("未知阶段的状态文案 = %q，期望「处理中」", got)
	}
	if got := stewardTopicStatusText(StewardTopic{}); got != "等你确认任务" {
		t.Fatalf("空阶段的状态文案 = %q，期望「等你确认任务」", got)
	}
}

// plan_failed 阶段的卡片显示「这次没出方案」与「重新规划」，点击调用 ReplanStewardTopic。
func TestStewardTopicCardPlanFailedReplans(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-fail",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "规划失败的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicPlanFailed,
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-fail" && tt.HasText("方案没出来")
	})
	if !tt.HasText("这次没出方案") {
		t.Fatalf("plan_failed 卡片应说明方案没出来，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("重新规划") {
		t.Fatalf("plan_failed 卡片应有「重新规划」按钮，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("重新规划"); err != nil {
		t.Fatalf("点击重新规划失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.replannedThreads()
		return len(calls) == 1 && calls[0] == "t-fail"
	})
}

// 卡片右上角的「更多操作」能打开删除话题弹窗；确认后调用 DeleteStewardTopic，
// 话题会话是当前会话时回到空状态。
func TestStewardTopicDeleteFromCardClearsSelection(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-del",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "要删掉的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicRunning,
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-del" && tt.HasText("要删掉的话题")
	})
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("点击卡片更多操作失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), "删除话题…") })
	if err := tt.ChooseMenuItem("删除话题…"); err != nil {
		t.Fatalf("选择删除话题失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除话题") })
	if !tt.HasText("话题「要删掉的话题」删除后无法恢复") {
		t.Fatalf("删除弹窗应带上话题标题，当前文本: %q", tt.Texts())
	}

	if err := tt.Click("删除"); err != nil {
		t.Fatalf("点击删除失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.deletedThreads()
		return len(calls) == 1 && calls[0] == "t-del"
	})
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "" })
	waitFor(t, tt, func() bool { return !tt.HasText("删除话题") })
	// 重拉话题列表后，标题行与卡片都跟着消失。
	waitFor(t, tt, func() bool { return !tt.HasText("要删掉的话题") })
	if st := getStewardTopicsState(state); st != nil {
		st.mu.Lock()
		open, busy := st.deleteOpen, st.deleteBusy
		st.mu.Unlock()
		if open || busy {
			t.Fatalf("删除成功后弹窗应关闭，实际 open=%v busy=%v", open, busy)
		}
	}
}

// 侧栏话题行的右键菜单也有「删除话题…」；删掉不是当前会话的话题时，选中会话保持不变。
func TestStewardTopicDeleteFromSidebarRowKeepsOtherSelection(t *testing.T) {
	now := time.Now()
	fake := newStewardTopicsFixture(
		StewardTopic{ThreadID: "t-a", ProjectID: "p1", ProjectName: "普通项目", Title: "侧栏话题甲", Phase: StewardTopicRunning, UpdatedAt: now},
		StewardTopic{ThreadID: "t-b", ProjectID: "p1", ProjectName: "普通项目", Title: "侧栏话题乙", Phase: StewardTopicRunning, UpdatedAt: now.Add(-time.Minute)},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-a" && tt.HasText("侧栏话题甲")
	})
	// 切到另一条话题：卡片只渲染乙，甲只剩侧栏那一行。
	state.selectThread("t-b")
	tt.Frame()

	if err := tt.RightClick("侧栏话题甲"); err != nil {
		t.Fatalf("右键侧栏话题失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), "删除话题…") })
	if err := tt.ChooseMenuItem("删除话题…"); err != nil {
		t.Fatalf("选择删除话题失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除话题") })
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("点击删除失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.deletedThreads()
		return len(calls) == 1 && calls[0] == "t-a"
	})
	if got := selectedThreadForTest(state); got != "t-b" {
		t.Fatalf("删掉的不是当前会话时不应改选中，实际 %q", got)
	}
}

// 删除失败时弹窗保持打开、错误显示在弹窗里，当前会话与话题都还在。
func TestStewardTopicDeleteFailureKeepsModalOpen(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-del",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "删不掉的话题",
		Phase:       StewardTopicRunning,
		UpdatedAt:   time.Now(),
	})
	fake.failDelete(errors.New("话题还在运行，稍后再删"))
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-del" && tt.HasText("删不掉的话题")
	})
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("点击卡片更多操作失败: %v", err)
	}
	waitFor(t, tt, func() bool { return menuContains(tt.Menu(), "删除话题…") })
	if err := tt.ChooseMenuItem("删除话题…"); err != nil {
		t.Fatalf("选择删除话题失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除话题") })
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("点击删除失败: %v", err)
	}

	waitFor(t, tt, func() bool { return tt.HasText("话题还在运行，稍后再删") })
	if !tt.HasText("删除话题") {
		t.Fatalf("删除失败时弹窗应保持打开，当前文本: %q", tt.Texts())
	}
	st := getStewardTopicsState(state)
	st.mu.Lock()
	open, busy, errText := st.deleteOpen, st.deleteBusy, st.deleteErr
	st.mu.Unlock()
	if !open || busy || errText == "" {
		t.Fatalf("失败后弹窗应开着且不再忙碌，实际 open=%v busy=%v err=%q", open, busy, errText)
	}
	if got := selectedThreadForTest(state); got != "t-del" {
		t.Fatalf("删除失败不应清掉当前会话，实际 %q", got)
	}
}

// 话题列表拉取失败：侧栏给出说明与「重试」，旧话题不丢；重试成功后提示消失。
func TestStewardTopicsLoadFailureOffersRetry(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-keep",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "起草中的话题",
		Brief:       "写点什么。",
		Phase:       StewardTopicDrafting,
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool { return tt.HasText("起草中的话题") })

	st := getStewardTopicsState(state)
	fake.failTopicsLoad(errors.New("connection refused"))
	state.retryStewardTopics()
	waitFor(t, tt, func() bool { return tt.HasText("话题列表没加载出来") })
	if !tt.HasText("重试") {
		t.Fatalf("加载失败时应给「重试」按钮，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("起草中的话题") {
		t.Fatalf("加载失败时旧话题应继续展示，当前文本: %q", tt.Texts())
	}
	st.mu.Lock()
	loadErr, kept := st.loadErr, len(st.topics)
	st.mu.Unlock()
	if loadErr == "" || kept != 1 {
		t.Fatalf("失败后应记下错误且保留旧话题，实际 loadErr=%q topics=%d", loadErr, kept)
	}

	fake.healTopicsLoad()
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点击重试失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.loadErr == "" && !tt.HasText("话题列表没加载出来")
	})
}

// 确认开工成功后卡片底部给一行「已交给总管开工」，并清掉上一次的错误。
func TestStewardTopicConfirmShowsSuccessNote(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-confirm",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "待确认的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicDrafting,
		UpdatedAt:   time.Now(),
	})
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-confirm" && tt.HasText("直接开工")
	})
	if err := tt.Click("直接开工"); err != nil {
		t.Fatalf("点击直接开工失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st := getStewardTopicsState(state)
		st.mu.Lock()
		note, errText := st.cardNotes["t-confirm"], st.cardErrs["t-confirm"]
		st.mu.Unlock()
		return note == "已交给总管开工" && errText == ""
	})
	waitFor(t, tt, func() bool { return tt.HasText("已交给总管开工") })
}

// 确认开工失败：底层 RPC 错误归成「没连上宿主，请稍后再试」，不把原文丢给用户。
func TestStewardTopicConfirmFailureText(t *testing.T) {
	fake := newStewardTopicsFixture(StewardTopic{
		ThreadID:    "t-confirm",
		ProjectID:   "p1",
		ProjectName: "普通项目",
		Title:       "待确认的话题",
		Brief:       "把导出做成 CSV。",
		Phase:       StewardTopicDrafting,
		UpdatedAt:   time.Now(),
	})
	fake.failConfirm(errors.New("rpc error: code = Unavailable desc = connection refused"))
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-confirm" && tt.HasText("直接开工")
	})
	if err := tt.Click("直接开工"); err != nil {
		t.Fatalf("点击直接开工失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st := getStewardTopicsState(state)
		st.mu.Lock()
		errText := st.cardErrs["t-confirm"]
		st.mu.Unlock()
		return errText == "没连上宿主，请稍后再试"
	})
	if !tt.HasText("没连上宿主，请稍后再试") {
		t.Fatalf("卡片应显示归类后的失败文案，当前文本: %q", tt.Texts())
	}
}

// 进行中的按钮文案与错误归类各走一张表。
func TestStewardTopicBusyAndErrorText(t *testing.T) {
	busy := map[string]string{
		"steward-topic-confirm":        "开工中…",
		"steward-topic-start":          "开工中…",
		"steward-topic-replan":         "重新规划中…",
		"steward-topic-close":          "结项中…",
		"steward-topic-task-accept":    "通过中…",
		"steward-topic-task-something": "",
	}
	for key, want := range busy {
		if got := stewardTopicBusyText(key); got != want {
			t.Fatalf("busy 文案 %s = %q，期望 %q", key, got, want)
		}
	}
	if got := stewardTopicActionButtonText("steward-topic-confirm", "直接开工", true); got != "开工中…" {
		t.Fatalf("busy 时按钮应显示「开工中…」，实际 %q", got)
	}
	if got := stewardTopicActionButtonText("steward-topic-confirm", "直接开工", false); got != "直接开工" {
		t.Fatalf("空闲时按钮应显示原名，实际 %q", got)
	}
	for _, raw := range []string{"rpc error: host gone", "connection reset", "context deadline exceeded", "unexpected EOF"} {
		if got := stewardTopicActionErrorText(errors.New(raw)); got != "没连上宿主，请稍后再试" {
			t.Fatalf("%q 应归成连不上宿主，实际 %q", raw, got)
		}
	}
	if got := stewardTopicActionErrorText(errors.New("话题已经结项")); got != "操作没成功：话题已经结项" {
		t.Fatalf("其他错误应保留原文，实际 %q", got)
	}
}
