package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type tasksAttentionFakeClient struct {
	*FakeClient

	mu        sync.Mutex
	decisions []StewardDecisionItem
	answered  []struct{ id, answer string }
}

func (c *tasksAttentionFakeClient) StewardOpenDecisions(_ context.Context) ([]StewardDecisionItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]StewardDecisionItem, len(c.decisions))
	copy(out, c.decisions)
	return out, nil
}

func (c *tasksAttentionFakeClient) AnswerStewardDecision(_ context.Context, id, answer string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answered = append(c.answered, struct{ id, answer string }{id: id, answer: answer})
	kept := c.decisions[:0]
	for _, d := range c.decisions {
		if d.ID != id {
			kept = append(kept, d)
		}
	}
	c.decisions = kept
	return nil
}

func TestTasksAttentionViewAggregatesAndDeduplicates(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	s.projects = []Project{
		{ID: "p1", Name: "核心项目"},
		{ID: "p2", Name: "辅助项目"},
	}

	now := time.Now()
	st := getStewardOpenState(s)
	st.mu.Lock()
	st.openDecisions = []StewardDecisionItem{
		{
			ID:          "D-0001",
			TaskID:      "t1",
			ThreadID:    "steward-1",
			Question:    "是否合并分支？",
			Context:     "所有测试已通过",
			Options:     []string{"合并", "暂缓"},
			Recommended: "合并",
			Status:      "open",
			CreatedAt:   now,
		},
	}
	st.mu.Unlock()

	board := KanbanBoard{
		Tasks: []KanbanTask{
			{ID: "t1", ProjectID: "p1", Title: "需要人工处理任务", Status: "needs_you", UpdatedAt: now.Add(-time.Minute)},
			{ID: "t2", ProjectID: "p2", Title: "被驳回任务", Status: "rejected", UpdatedAt: now.Add(-2 * time.Minute)},
			{ID: "t3", ProjectID: "p1", Title: "已完成任务", Status: "done", Column: "needs_you", UpdatedAt: now},
			{ID: "t4", ProjectID: "p1", Title: "已取消任务", Status: "cancelled", AttentionFlags: []string{"failed"}, UpdatedAt: now},
		},
		Goals: []KanbanGoal{
			{ID: "g1", ProjectID: "p1", Title: "目标待审", Status: "plan_review", UpdatedAt: now.Add(-3 * time.Minute)},
			{ID: "g2", ProjectID: "p2", Title: "目标规划失败", Status: "plan_failed", UpdatedAt: now.Add(-4 * time.Minute)},
			{ID: "g3", ProjectID: "p1", Title: "运行中目标", Status: "running", UpdatedAt: now},
		},
	}

	as := getAutomationState(s)
	automationMu.Lock()
	as.initialized = true
	as.unreadKnown = true
	as.autos = []Automation{
		{ID: "a1", ProjectID: "p1", Name: "夜间巡检", LastRunStatus: "failed", LastRunAt: now},
		{ID: "a2", ProjectID: "p2", Name: "日报汇总", LastRunStatus: "succeeded", LastRunAt: now},
		{ID: "a3", ProjectID: "p1", Name: "正常巡检", LastRunStatus: "succeeded", LastRunAt: now},
	}
	as.unread = map[string]int{"a1": 2, "a2": 3}
	automationMu.Unlock()

	att := getAttentionInboxState(s)
	att.mu.Lock()
	att.items = []AttentionItem{
		{ID: "ev-task", Kind: "kanban_needs_approval", SubjectType: "kanban_task", SubjectID: "t1", ProjectID: "p1", Title: "重复任务事件", CreatedAt: now},
		{ID: "ev-goal", Kind: "kanban_plan_failed", SubjectType: "kanban_goal", SubjectID: "g2", ProjectID: "p2", Title: "重复目标事件", CreatedAt: now},
		{ID: "ev-thread", Kind: "thread_failed", SubjectType: "thread", SubjectID: "th1", ProjectID: "p1", ThreadID: "th1", Title: "会话执行出错", Detail: "退出码 1", CreatedAt: now},
	}
	att.mu.Unlock()

	// 全量聚合：1 决策 + 2 任务 + 2 目标 + 2 自动化 + 1 独立收件箱事件 = 8（t1 与 g2 去重并保留 AttentionEventID）
	all := buildTaskCenterAttentionItems(s, board, "", "")
	if len(all) != 8 {
		t.Fatalf("聚合数量 = %d，期望 8: %+v", len(all), all)
	}
	if all[0].Kind != "decision" || all[0].EntityID != "D-0001" {
		t.Fatalf("最高优先级应为未答决策，实际 %+v", all[0])
	}
	var foundT1, foundG2 bool
	for _, it := range all {
		if it.Kind == "task" && it.EntityID == "t1" {
			foundT1 = true
			if it.AttentionEventID != "ev-task" {
				t.Fatalf("t1 未合并收件箱事件 ID: %+v", it)
			}
		}
		if it.Kind == "goal" && it.EntityID == "g2" {
			foundG2 = true
			if it.AttentionEventID != "ev-goal" {
				t.Fatalf("g2 未合并收件箱事件 ID: %+v", it)
			}
		}
		if it.EntityID == "t3" || it.EntityID == "t4" || it.EntityID == "g3" || it.EntityID == "a3" {
			t.Fatalf("不应包含已完成/已取消/正常项: %+v", it)
		}
	}
	if !foundT1 || !foundG2 {
		t.Fatalf("缺少去重后的实体: foundT1=%v foundG2=%v", foundT1, foundG2)
	}

	// 按 scopeID 过滤
	p1Items := buildTaskCenterAttentionItems(s, board, "p1", "")
	if len(p1Items) != 5 {
		t.Fatalf("p1 范围数量 = %d，期望 5: %+v", len(p1Items), p1Items)
	}
	p2Items := buildTaskCenterAttentionItems(s, board, "p2", "")
	if len(p2Items) != 3 {
		t.Fatalf("p2 范围数量 = %d，期望 3: %+v", len(p2Items), p2Items)
	}

	// 搜索过滤：匹配标题、副标题、统一状态中文名、项目名
	if got := buildTaskCenterAttentionItems(s, board, "", "退出码"); len(got) != 1 || got[0].EntityID != "ev-thread" {
		t.Fatalf("按副标题搜索失败: %+v", got)
	}
	if got := buildTaskCenterAttentionItems(s, board, "", "方案待审"); len(got) != 1 || got[0].EntityID != "g1" {
		t.Fatalf("按状态中文名搜索失败: %+v", got)
	}
	if got := buildTaskCenterAttentionItems(s, board, "", "辅助项目"); len(got) != 3 {
		t.Fatalf("按项目名搜索失败: %+v", got)
	}

	// 空态展示
	sEmpty, viewEmpty := buildTasksRoot(nil, WithTasksWindowMode("attention"))
	defer sEmpty.close()
	getKanbanState(sEmpty).initialized = true
	tt := ui.NewTester(viewEmpty, 1140, 720)
	tt.Frame()
	if !tt.HasText("没有待你处理的事项") {
		t.Fatalf("空态未显示「没有待你处理的事项」，当前文本: %v", tt.Texts())
	}
}

func TestTasksAttentionViewSelectOpensRightDetail(t *testing.T) {
	fake := &tasksAttentionFakeClient{
		FakeClient: NewFakeClient(),
		decisions: []StewardDecisionItem{
			{
				ID:          "D-0009",
				ThreadID:    "steward-feed",
				Question:    "是否采用方案 B？",
				Context:     "方案 B 改动更小",
				Options:     []string{"采用方案 B", "保留方案 A"},
				Recommended: "采用方案 B",
				Status:      "open",
				CreatedAt:   time.Now(),
			},
		},
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "主项目"}}, nil)
	fake.SetKanbanBoard(KanbanBoard{
		Tasks: []KanbanTask{{ID: "task-att", ProjectID: "p1", Title: "待处理任务卡", Status: "needs_you", Column: "needs_you"}},
		Goals: []KanbanGoal{{ID: "goal-att", ProjectID: "p1", Title: "待审目标卡", Status: "plan_review"}},
	})

	s, view := buildTasksRoot(fake, WithTasksWindowMode("attention"))
	defer s.close()
	tt := ui.NewTester(view, 1140, 720)

	waitFor(t, tt, func() bool {
		return tt.HasText("是否采用方案 B？") && tt.HasText("待处理任务卡") && tt.HasText("待审目标卡")
	})

	// 1. 点击任务打开右侧任务详情
	if err := tt.Click("待处理任务卡"); err != nil {
		t.Fatalf("点击任务失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		ks := getKanbanState(s)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		_, ok := tt.Find("kanban-task-detail")
		return ok && ks.detailTaskID == "task-att" && ks.detailDecisionID == "" && !ks.goalDetailOpen
	})

	// 2. 点击目标打开右侧目标详情
	if err := tt.Click("待审目标卡"); err != nil {
		t.Fatalf("点击目标失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		ks := getKanbanState(s)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		_, ok := tt.Find("kanban-goal-detail")
		return ok && ks.goalDetailOpen && ks.goalID == "goal-att" && ks.detailTaskID == "" && ks.detailDecisionID == ""
	})

	// 3. 点击未答决策打开右侧决策详情，直接点击选项作答并乐观移除
	if err := tt.Click("是否采用方案 B？"); err != nil {
		t.Fatalf("点击决策失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		ks := getKanbanState(s)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		_, ok := tt.Find("kanban-decision-detail")
		_, hasOrigin := tt.Find("kanban-decision-detail-header-origin")
		_, hasStatus := tt.Find("kanban-decision-detail-header-status")
		_, hasClose := tt.Find("关闭决策详情")
		return ok && hasOrigin && hasStatus && hasClose && ks.detailDecisionID == "D-0009" &&
			tt.HasText("决策详情") && tt.HasText("打开会话") &&
			tt.HasText("方案 B 改动更小") && tt.HasText("推荐")
	})

	if err := tt.Click("采用方案 B"); err != nil {
		t.Fatalf("点击决策选项失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		answered := len(fake.answered) == 1 && fake.answered[0].id == "D-0009" && fake.answered[0].answer == "采用方案 B"
		fake.mu.Unlock()
		return answered && tt.HasText("决策已处理") && !tt.HasText("是否采用方案 B？")
	})

	// 4. Esc 关闭决策详情
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	ks := getKanbanState(s)
	kanbanMu.Lock()
	decisionID := ks.detailDecisionID
	kanbanMu.Unlock()
	if decisionID != "" {
		t.Fatalf("Esc 未关闭决策详情: %q", decisionID)
	}
}

func TestTasksAttentionViewDestinationNav(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("attention"))
	defer s.close()
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	ks := getKanbanState(s)
	ks.initialized = true

	tt := ui.NewTester(view, 1140, 720)
	tt.Frame()

	for _, key := range []string{"kanban-destination-attention", "kanban-destination-tasks", "kanban-destination-automations"} {
		if _, ok := tt.Find(key); !ok {
			t.Fatalf("缺少顶层目的项 %q", key)
		}
	}
	if _, ok := tt.Find("看板"); ok {
		t.Fatal("待你处理视图不应显示看板/列表切换")
	}
	if _, ok := tt.Find("新建任务"); ok {
		t.Fatal("待你处理视图不应显示新建任务菜单")
	}

	if err := tt.Click("kanban-destination-tasks"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	kanbanMu.Lock()
	dest := ks.destination
	kanbanMu.Unlock()
	if dest != "tasks" {
		t.Fatalf("切换任务后 destination = %q", dest)
	}
	if _, ok := tt.Find("看板"); !ok {
		t.Fatal("任务视图应恢复看板/列表切换")
	}

	if err := tt.Click("kanban-destination-automations"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	kanbanMu.Lock()
	dest = ks.destination
	kanbanMu.Unlock()
	if dest != "automations" {
		t.Fatalf("切换自动化后 destination = %q", dest)
	}

	if err := tt.Click("kanban-destination-attention"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	kanbanMu.Lock()
	dest = ks.destination
	kanbanMu.Unlock()
	if dest != "attention" {
		t.Fatalf("切回待你处理后 destination = %q", dest)
	}
}

func TestTasksAttentionViewNarrowDetailSheet(t *testing.T) {
	fake := &tasksAttentionFakeClient{
		FakeClient: NewFakeClient(),
		decisions: []StewardDecisionItem{
			{
				ID:          "D-narrow-1",
				ThreadID:    "steward-feed",
				Question:    "窄屏是否执行迁移？",
				Context:     "预计耗时 3 分钟",
				Options:     []string{"执行", "跳过"},
				Recommended: "执行",
				Status:      "open",
				CreatedAt:   time.Now(),
			},
		},
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "主项目"}}, nil)
	fake.SetKanbanBoard(KanbanBoard{})

	s, view := buildTasksRoot(fake, WithTasksWindowMode("attention"))
	defer s.close()
	tt := ui.NewTester(view, 640, 480)

	waitFor(t, tt, func() bool {
		return tt.HasText("窄屏是否执行迁移？")
	})

	if err := tt.Click("窄屏是否执行迁移？"); err != nil {
		t.Fatalf("点击决策失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, hasSheet := tt.Find("kanban-decision-sheet")
		_, hasOrigin := tt.Find("kanban-decision-detail-header-origin")
		_, hasStatus := tt.Find("kanban-decision-detail-header-status")
		return hasSheet && hasOrigin && hasStatus && tt.HasText("决策详情") && tt.HasText("预计耗时 3 分钟")
	})

	// 点击遮罩关闭窄屏详情弹层
	tt.ClickAt(4, 4)
	tt.Frame()
	ks := getKanbanState(s)
	kanbanMu.Lock()
	closedByBackdrop := ks.detailDecisionID == ""
	kanbanMu.Unlock()
	if !closedByBackdrop {
		t.Fatal("点击遮罩未关闭窄屏决策详情")
	}
	if _, ok := tt.Find("kanban-decision-sheet"); ok {
		t.Fatal("关闭后不应再渲染 kanban-decision-sheet")
	}

	// 再次打开并用 Esc 关闭
	if err := tt.Click("窄屏是否执行迁移？"); err != nil {
		t.Fatalf("再次点击决策失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, hasSheet := tt.Find("kanban-decision-sheet")
		return hasSheet
	})
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	kanbanMu.Lock()
	closedByEsc := ks.detailDecisionID == ""
	kanbanMu.Unlock()
	if !closedByEsc {
		t.Fatal("Esc 未关闭窄屏决策详情")
	}
}
