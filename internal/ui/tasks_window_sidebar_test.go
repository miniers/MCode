package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTasksSidebarSectionsAndDuplicateProjects(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "one", Name: "同名"}, {ID: "two", Name: "同名"}}
	ks := getKanbanState(s)
	ks.initialized = true
	ks.destination = "tasks"
	tt := ui.NewTester(func(c *ui.Context) {
		kanbanMu.Lock()
		scope := ks.scopeID
		kanbanMu.Unlock()
		renderTasksWindowSidebar(c, s, ks, kanbanModalBodyProps{destination: "tasks", scopeID: scope})
	}, 840, 720)
	tt.Frame()
	tasks, ok := tt.Find("kanban-destination-tasks")
	if !ok {
		t.Fatal("缺少任务目的")
	}
	autos, ok := tt.Find("kanban-destination-automations")
	if !ok {
		t.Fatal("缺少自动化目的")
	}
	all, ok := tt.Find("全部项目")
	if !ok {
		t.Fatal("缺少全部项目")
	}
	if gap := autos.Y - (tasks.Y + tasks.H); gap >= Space5 {
		t.Fatalf("目的行间距过大: %v", gap)
	}
	if gap := all.Y - (autos.Y + autos.H); gap < Space5 {
		t.Fatalf("区段间距过小: %v", gap)
	}
	if tt.HasText("目标") {
		t.Fatal("无目标时显示分组")
	}
	for i, id := range []string{"one", "two"} {
		tt.ClickAt(all.X+Space3, all.Y+all.H+RowHeight*float32(i)+RowHeight/2)
		kanbanMu.Lock()
		got := ks.scopeID
		kanbanMu.Unlock()
		if got != id {
			t.Fatalf("点击项目 %s 后范围为 %s", id, got)
		}
		tt.Frame()
	}
}

func TestTasksSidebarPlanReviewOpensDetail(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := getKanbanState(s)
	ks.destination = "tasks"
	board := KanbanBoard{Goals: []KanbanGoal{{ID: "g", Title: "计划", Status: "plan_review"}}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderTasksWindowSidebar(c, s, ks, kanbanModalBodyProps{destination: "tasks", board: board})
	}, 840, 720)
	tt.Frame()
	if err := tt.Click("确认目标计划 计划"); err != nil {
		t.Fatal(err)
	}
	kanbanMu.Lock()
	id, open := ks.goalID, ks.goalDetailOpen
	kanbanMu.Unlock()
	if id != "g" || !open {
		t.Fatalf("目标详情状态: %q %v", id, open)
	}
}

func TestTasksSidebarAutomationUnreadBadge(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	t.Cleanup(func() {
		automationMu.Lock()
		delete(automationStates, s)
		automationMu.Unlock()
	})
	ks := getKanbanState(s)
	ks.destination = "tasks"
	tt := ui.NewTester(func(c *ui.Context) {
		renderTasksWindowSidebar(c, s, ks, kanbanModalBodyProps{destination: "tasks"})
	}, 840, 720)
	for _, tc := range []struct {
		name        string
		initialized bool
		known       bool
		unread      int
		want        bool
	}{
		{"未读", true, true, 7, true},
		{"无未读", true, true, 0, false},
		{"未知", true, false, 7, false},
		{"未初始化", false, true, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			automationMu.Lock()
			automationStates[s] = &automationState{initialized: tc.initialized, unreadKnown: tc.known, unread: map[string]int{"a": tc.unread}}
			automationMu.Unlock()
			tt.Frame()
			if got := tt.HasText("7"); got != tc.want {
				t.Fatalf("未读数字显示 = %v，期望 %v", got, tc.want)
			}
			if tc.want {
				badge, ok := tt.Find("7")
				auto, autoOK := tt.Find("kanban-destination-automations")
				if !ok || !autoOK || badge.Y < auto.Y || badge.Y >= auto.Y+auto.H {
					t.Fatal("未读数字不在自动化行内")
				}
			}
		})
	}
}

func TestTasksSidebarTextColors(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		if tasksSidebarTextColor(c, true) != ThemePrimaryText(c) || tasksSidebarTextColor(c, false) != ThemeSecondaryText(c) {
			t.Error("侧栏文字颜色不正确")
		}
		if tasksSidebarIconColor(c, true) != ThemeAccent(c) || tasksSidebarIconColor(c, false) != ThemeSecondaryText(c) {
			t.Error("侧栏图标颜色不正确")
		}
	}, 100, 100)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
	}
}
