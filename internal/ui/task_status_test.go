package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestTaskStatusDictCompleteness(t *testing.T) {
	colCases := []struct {
		key      string
		label    string
		semantic semanticStatus
	}{
		{"todo", "待办", semanticNeutral},
		{"in_progress", "进行中", semanticAccent},
		{"needs_you", "待你处理", semanticWarning},
		{"review", "待审阅", semanticAccent},
		{"done", "已完成", semanticSuccess},
	}
	for _, tc := range colCases {
		if got := kanbanColumnLabel(tc.key); got != tc.label {
			t.Errorf("kanbanColumnLabel(%q) = %q, want %q", tc.key, got, tc.label)
		}
		if got := taskStatusSemantic(tc.key); got != tc.semantic {
			t.Errorf("taskStatusSemantic(%q) = %v, want %v", tc.key, got, tc.semantic)
		}
	}

	taskCases := []struct {
		status   string
		label    string
		semantic semanticStatus
	}{
		{"todo", "待办", semanticNeutral},
		{"in_progress", "进行中", semanticAccent},
		{"needs_approval", "待审批", semanticWarning},
		{"waiting", "等待中", semanticNeutral},
		{"review", "待审阅", semanticAccent},
		{"done", "已完成", semanticSuccess},
		{"rejected", "已驳回", semanticWarning},
		{"failed", "失败", semanticDanger},
		{"cancelled", "已取消", semanticNeutral},
		{"needs_you", "待你处理", semanticWarning},
	}
	for _, tc := range taskCases {
		if got := taskStatusLabel(tc.status); got != tc.label {
			t.Errorf("taskStatusLabel(%q) = %q, want %q", tc.status, got, tc.label)
		}
		if got := taskBadgeLabel(KanbanTask{Status: tc.status}); got != tc.label {
			t.Errorf("taskBadgeLabel(%q) = %q, want %q", tc.status, got, tc.label)
		}
		if got := taskStatusSemantic(tc.status); got != tc.semantic {
			t.Errorf("taskStatusSemantic(%q) = %v, want %v", tc.status, got, tc.semantic)
		}
	}

	goalCases := []struct {
		status   string
		label    string
		semantic semanticStatus
	}{
		{"planning", "规划中", semanticAccent},
		{"plan_failed", "规划失败", semanticDanger},
		{"plan_review", "方案待审", semanticWarning},
		{"running", "进行中", semanticAccent},
		{"paused", "已暂停", semanticNeutral},
		{"completed", "已完成", semanticSuccess},
		{"cancelled", "已取消", semanticNeutral},
	}
	for _, tc := range goalCases {
		if got := goalStatusLabel(tc.status); got != tc.label {
			t.Errorf("goalStatusLabel(%q) = %q, want %q", tc.status, got, tc.label)
		}
		if got := goalStatusSemantic(tc.status); got != tc.semantic {
			t.Errorf("goalStatusSemantic(%q) = %v, want %v", tc.status, got, tc.semantic)
		}
	}

	autoCases := []struct {
		status string
		label  string
		kind   string
	}{
		{"running", "运行中", "running"},
		{"succeeded", "成功", "succeeded"},
		{"failed", "失败", "failed"},
		{"skipped", "已跳过", "stopped"},
		{"stopped", "已停止", "stopped"},
	}
	for _, tc := range autoCases {
		gotLabel, gotKind := automationRunStatusDict(tc.status)
		if gotLabel != tc.label || gotKind != tc.kind {
			t.Errorf("automationRunStatusDict(%q) = (%q, %q), want (%q, %q)", tc.status, gotLabel, gotKind, tc.label, tc.kind)
		}
	}

	inboxCases := []struct {
		kind  string
		title string
	}{
		{"thread_stalled", "会话卡住"},
		{"thread_failed", "会话出错"},
		{"thread_quota", "额度用尽"},
		{"thread_approval", "等待审批"},
		{"kanban_failed", "看板失败"},
		{"kanban_needs_approval", "看板待审批"},
		{"kanban_review", "看板待验收"},
		{"kanban_plan_failed", "规划失败"},
		{"automation_failed", "自动化失败"},
		{"automation_finished", "自动化完成"},
		{"bee_failed", "子代理失败"},
		{"merge_review_pending", "合并待审阅"},
		{"thread_no_progress", "任务无进展"},
	}
	for _, tc := range inboxCases {
		if got := attentionKindTitle(tc.kind); got != tc.title {
			t.Errorf("attentionKindTitle(%q) = %q, want %q", tc.kind, got, tc.title)
		}
	}

	tt := ui.NewTester(func(c *ui.Context) {
		for _, tc := range colCases {
			if col := kanbanStatusColor(c, tc.key); col.A == 0 {
				t.Errorf("kanbanStatusColor(%q) returned transparent color", tc.key)
			}
		}
		for _, tc := range taskCases {
			if col := semanticStatusColor(c, taskStatusSemantic(tc.status)); col.A == 0 {
				t.Errorf("task semantic color(%q) returned transparent color", tc.status)
			}
		}
		for _, tc := range goalCases {
			if col := goalStatusBadgeColor(c, tc.status); col.A == 0 {
				t.Errorf("goalStatusBadgeColor(%q) returned transparent color", tc.status)
			}
		}
	}, 100, 100)
	tt.Frame()

	if !isStewardTaskOrigin("owner") || isStewardTaskOrigin("steward") || isStewardTaskOrigin("") {
		t.Fatal("isStewardTaskOrigin 判定不符合约定")
	}
	if !isStewardAutomationOrigin("steward") || isStewardAutomationOrigin("owner") || isStewardAutomationOrigin("") {
		t.Fatal("isStewardAutomationOrigin 判定不符合约定")
	}
}

func TestKanbanOriginFilterAndBadge(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Goals: []KanbanGoal{
			{ID: "g1", ProjectID: "p1", Title: "目标一", Status: "running"},
		},
		Tasks: []KanbanTask{
			{ID: "owner-1", ProjectID: "p1", Title: "总管派发任务", Origin: "owner", Status: "todo", Column: "todo"},
			{ID: "goal-1", ProjectID: "p1", GoalID: "g1", Title: "目标分解任务", Status: "todo", Column: "todo"},
			{ID: "plain-1", ProjectID: "p1", Title: "手动创建任务", Status: "todo", Column: "todo"},
		},
	})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool {
		return tt.HasText("总管派发任务") && tt.HasText("目标分解任务") && tt.HasText("手动创建任务")
	})

	if _, ok := tt.Find("kanban-task-origin-owner-1"); !ok {
		t.Fatal("Origin == owner 的任务卡片应渲染总管来源标识")
	}
	if _, ok := tt.Find("kanban-task-origin-goal-1"); ok {
		t.Fatal("非总管任务卡片不应渲染总管来源标识")
	}
	if _, ok := tt.Find("kanban-task-origin-plain-1"); ok {
		t.Fatal("普通任务卡片不应渲染总管来源标识")
	}

	if err := tt.Click("kanban-origin-owner"); err != nil {
		t.Fatalf("点击总管筛选失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("总管派发任务") && !tt.HasText("目标分解任务") && !tt.HasText("手动创建任务")
	})

	if err := tt.Click("kanban-origin-goal"); err != nil {
		t.Fatalf("点击目标筛选失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("目标分解任务") && !tt.HasText("总管派发任务") && !tt.HasText("手动创建任务")
	})

	if err := tt.Click("kanban-origin-all"); err != nil {
		t.Fatalf("点击全部筛选失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("总管派发任务") && tt.HasText("目标分解任务") && tt.HasText("手动创建任务")
	})

	// 验证自动化列表的总管来源标识
	autos := []Automation{
		{ID: "a-steward", Name: "总管巡检", Origin: "steward", Enabled: true, Schedule: AutomationSchedule{Type: "daily"}},
		{ID: "a-plain", Name: "常规巡检", Origin: "", Enabled: true, Schedule: AutomationSchedule{Type: "daily"}},
	}
	autoTT := ui.NewTester(func(c *ui.Context) {
		renderAutomationList(c, automationListProps{Visible: autos, Now: time.Now()})
	}, 360, 320)
	autoTT.Frame()
	if _, ok := autoTT.Find("automation-origin-a-steward"); !ok {
		t.Fatal("Origin == steward 的自动化列表项应渲染总管来源标识")
	}
	if _, ok := autoTT.Find("automation-origin-a-plain"); ok {
		t.Fatal("普通自动化列表项不应渲染总管来源标识")
	}
	if !autoTT.HasText("总管") {
		t.Fatal("自动化列表项应显示总管文案")
	}
}
