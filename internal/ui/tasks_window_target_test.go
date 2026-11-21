package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestTasksWindowTargetTaskAndGoal(t *testing.T) {
	t.Run("数据已就绪时立即选中任务与目标", func(t *testing.T) {
		s, _ := buildTasksRoot(NewFakeClient(), WithTasksWindowMode("tasks"))
		defer s.close()

		ks := getKanbanState(s)
		kanbanMu.Lock()
		ks.initialized = true
		ks.scopeID = "p2"
		ks.board = KanbanBoard{
			Tasks: []KanbanTask{{ID: "task-1", ProjectID: "p2", Title: "修复深链", Column: "todo"}},
			Goals: []KanbanGoal{{ID: "goal-1", ProjectID: "p2", Title: "收件箱重构"}},
		}
		kanbanMu.Unlock()

		s.applyTasksWindowTarget(taskCenterTarget{
			Kind:      "task",
			ID:        "task-1",
			ProjectID: "p2",
			Title:     "修复深链",
		})
		kanbanMu.Lock()
		gotTaskID := ks.detailTaskID
		gotGoalOpen := ks.goalDetailOpen
		gotDest := ks.destination
		gotScope := ks.scopeID
		kanbanMu.Unlock()
		if gotDest != "tasks" || gotScope != "p2" || gotTaskID != "task-1" || gotGoalOpen {
			t.Fatalf("立即定位任务失败: dest=%q scope=%q detailTaskID=%q goalDetailOpen=%v", gotDest, gotScope, gotTaskID, gotGoalOpen)
		}

		s.applyTasksWindowTarget(taskCenterTarget{
			Kind:      "goal",
			ID:        "goal-1",
			ProjectID: "p2",
			Title:     "收件箱重构",
		})
		kanbanMu.Lock()
		gotGoalID := ks.goalID
		gotGoalOpen = ks.goalDetailOpen
		gotTaskID = ks.detailTaskID
		kanbanMu.Unlock()
		if gotGoalID != "goal-1" || !gotGoalOpen || gotTaskID != "" {
			t.Fatalf("立即定位目标失败: goalID=%q goalDetailOpen=%v detailTaskID=%q", gotGoalID, gotGoalOpen, gotTaskID)
		}
	})

	t.Run("异步加载完成后自动消费任务与目标深链", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "P1"}, {ID: "p2", Name: "P2"}}, nil)
		fake.SetKanbanBoard(KanbanBoard{
			Tasks: []KanbanTask{{ID: "task-p2", ProjectID: "p2", Title: "跨项目任务", Column: "review", Status: "review"}},
			Goals: []KanbanGoal{{ID: "goal-p2", ProjectID: "p2", Title: "跨项目目标"}},
		})

		s, view := buildTasksRoot(fake, WithTasksWindowTarget(taskCenterTarget{
			Kind:      "task",
			ID:        "task-p2",
			ProjectID: "p2",
			Title:     "跨项目任务",
		}))
		defer s.close()
		tt := ui.NewTester(view, 1000, 700)

		waitFor(t, tt, func() bool {
			ks := getKanbanState(s)
			kanbanMu.Lock()
			defer kanbanMu.Unlock()
			return ks.destination == "tasks" && ks.scopeID == "p2" && ks.detailTaskID == "task-p2"
		})

		s.applyTasksWindowTarget(taskCenterTarget{
			Kind:      "goal",
			ID:        "goal-p2",
			ProjectID: "p2",
			Title:     "跨项目目标",
		})
		waitFor(t, tt, func() bool {
			ks := getKanbanState(s)
			kanbanMu.Lock()
			defer kanbanMu.Unlock()
			return ks.goalID == "goal-p2" && ks.goalDetailOpen && ks.detailTaskID == ""
		})
	})
}

func TestTasksWindowTargetAutomationAndRun(t *testing.T) {
	t.Run("按自动化 ID 选中", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "P1"}, {ID: "p2", Name: "P2"}}, nil)
		fake.SetAutomations([]Automation{
			{ID: "auto-1", ProjectID: "p1", Name: "旧项目任务"},
			{ID: "auto-2", ProjectID: "p2", Name: "每日巡检"},
		})

		s, view := buildTasksRoot(fake, WithTasksWindowTarget(taskCenterTarget{
			Kind:      "automation",
			ID:        "auto-2",
			ProjectID: "p2",
			Title:     "每日巡检",
		}))
		defer s.close()
		tt := ui.NewTester(view, 1000, 700)

		waitFor(t, tt, func() bool {
			ks := getKanbanState(s)
			kanbanMu.Lock()
			dest, scope := ks.destination, ks.scopeID
			kanbanMu.Unlock()

			as := getAutomationState(s)
			automationMu.Lock()
			defer automationMu.Unlock()
			return dest == "automations" && scope == "p2" && as.projectID == "p2" &&
				as.selectedID == "auto-2" && as.narrowDetail && as.detailTab == 0 && as.openRunID == ""
		})
	})

	t.Run("automation_run 按 ProjectID 与 Title 匹配并展开运行记录", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "P1"}, {ID: "p2", Name: "P2"}}, nil)
		fake.SetAutomations([]Automation{
			{ID: "auto-p1", ProjectID: "p1", Name: "同名巡检"},
			{ID: "auto-p2", ProjectID: "p2", Name: "同名巡检"},
		})
		fake.SetAutomationRuns("auto-p2", []AutomationRun{
			{ID: "run-99", AutomationID: "auto-p2", Status: "failed"},
		})

		s, view := buildTasksRoot(fake, WithTasksWindowTarget(taskCenterTarget{
			Kind:      "automation_run",
			ID:        "run-99",
			ProjectID: "p2",
			Title:     "同名巡检",
		}))
		defer s.close()
		tt := ui.NewTester(view, 1000, 700)

		waitFor(t, tt, func() bool {
			as := getAutomationState(s)
			automationMu.Lock()
			defer automationMu.Unlock()
			return as.projectID == "p2" && as.selectedID == "auto-p2" &&
				as.narrowDetail && as.detailTab == 0 && as.openRunID == "run-99" && len(as.runs) == 1
		})
	})
}

func TestTasksWindowTargetFromAttentionInbox(t *testing.T) {
	t.Run("收件箱点击携带完整深链参数", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		defer s.close()

		var got []taskCenterTarget
		WithOpenTasksWindowTarget(func(target taskCenterTarget) {
			got = append(got, target)
		})(s)

		s.openAttentionItem(AttentionItem{
			ID:          "att-task",
			Kind:        "kanban_failed",
			SubjectType: "kanban_task",
			SubjectID:   "task-1",
			ProjectID:   "proj-1",
			Title:       "修复任务",
		})
		s.openAttentionItem(AttentionItem{
			ID:          "att-goal",
			Kind:        "kanban_plan_failed",
			SubjectType: "kanban_goal",
			SubjectID:   "goal-1",
			ProjectID:   "proj-2",
			Title:       "目标规划",
		})
		s.openAttentionItem(AttentionItem{
			ID:          "att-run",
			Kind:        "automation_failed",
			SubjectType: "automation_run",
			SubjectID:   "run-1",
			ProjectID:   "proj-3",
			Title:       "夜间巡检",
		})

		if len(got) != 3 {
			t.Fatalf("期望收到 3 次深链跳转，实际 %d 次: %+v", len(got), got)
		}
		want := []taskCenterTarget{
			{Kind: "task", ID: "task-1", ProjectID: "proj-1", Title: "修复任务"},
			{Kind: "goal", ID: "goal-1", ProjectID: "proj-2", Title: "目标规划"},
			{Kind: "automation_run", ID: "run-1", ProjectID: "proj-3", Title: "夜间巡检"},
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("第 %d 次跳转参数 = %+v, 期望 %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("合并待审阅与等待审批分组标题正确", func(t *testing.T) {
		fake := newAttentionFakeClient(
			attentionItem("a1", "thread_approval", "thread", "t1", "会话等待审批项", time.Minute),
			attentionItem("a2", "merge_review_pending", "kanban_task", "task-2", "合并审阅项", 2*time.Minute),
		)
		state, view := buildRoot(fake)
		defer state.close()
		tt := ui.NewTester(view, 1000, 700)

		waitFor(t, tt, func() bool { return tt.HasText("待你处理 2 条") })
		if err := tt.Click("待你处理"); err != nil {
			t.Fatalf("点击收件箱入口失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			return tt.HasText("会话等待审批项") && tt.HasText("合并审阅项")
		})
		if !hasExactText(tt, "等待审批") {
			t.Fatalf("thread_approval 分组标题应为「等待审批」，当前文本: %q", tt.Texts())
		}
		if !hasExactText(tt, "合并待审阅") {
			t.Fatalf("merge_review_pending 分组标题应为「合并待审阅」，当前文本: %q", tt.Texts())
		}
		if hasExactText(tt, "其他") {
			t.Fatalf("已登记类型不应归入「其他」，当前文本: %q", tt.Texts())
		}
	})
}
