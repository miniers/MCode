package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestKanbanShellDestinationsAndSearch(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	s.projects = []Project{{ID: "p1", Name: "项目", Path: "/project"}}
	ks := getKanbanState(s)
	ks.initialized = true
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if err := tt.Click("筛选任务"); err != nil {
		t.Fatal(err)
	}
	tt.Type("任务查询")
	tt.Frame()
	if err := tt.Click("kanban-destination-automations"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("kanban-modal-task-count"); ok {
		t.Fatal("自动化显示任务数徽标")
	}
	if err := tt.Click("筛选任务"); err != nil {
		t.Fatal(err)
	}
	tt.Type("自动查询")
	tt.Frame()
	kanbanMu.Lock()
	destination, taskSearch, autoSearch := ks.destination, ks.searchText, ks.automationSearch
	kanbanMu.Unlock()
	if destination != "automations" || taskSearch != "任务查询" || autoSearch != "自动查询" {
		t.Fatalf("状态不符: %q %q %q", destination, taskSearch, autoSearch)
	}
	if err := tt.Click("kanban-destination-tasks"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	kanbanMu.Lock()
	destination = ks.destination
	kanbanMu.Unlock()
	if destination != "tasks" {
		t.Fatalf("任务目的 = %q", destination)
	}
	s.close()
}

func TestTasksWindowEntrancesPreserveView(t *testing.T) {
	s := newChatState(NewFakeClient())
	ks := getKanbanState(s)
	ks.viewMode = config.KanbanViewList
	s.setTasksWindowView("automations")
	if ks.destination != "automations" || ks.viewMode != config.KanbanViewList {
		t.Fatal("自动化入口改变了任务展示方式")
	}
	s.setTasksWindowView("tasks")
	if ks.destination != "tasks" || ks.viewMode != config.KanbanViewList {
		t.Fatal("任务入口未保留列表视图")
	}
}

func TestTasksWindowPreservesListScrollAcrossDestinations(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Name: "项目", Path: "/project"}}
	ks := getKanbanState(s)
	ks.viewMode = config.KanbanViewList
	ks.destination = "tasks"
	for i := 0; i < 40; i++ {
		ks.board.Tasks = append(ks.board.Tasks, KanbanTask{ID: fmt.Sprintf("t%d", i), ProjectID: "p1", Title: fmt.Sprintf("任务 %02d", i), Column: "todo"})
	}
	tt := ui.NewTester(func(c *ui.Context) {
		p := kanbanModalBodyProps{destination: ks.destination, viewMode: ks.viewMode, board: ks.board, visibleBoard: ks.board}
		ui.Column(c).Fill().Children(func() { renderKanbanModalBody(c, s, ks, p) })
	}, 840, 520)
	tt.Frame()
	tt.Scroll(400, 300, 0, 600)
	tt.Advance(time.Second)
	before := ks.listScroll.Y
	if before <= 0 {
		t.Fatal("列表未滚动")
	}
	s.setTasksWindowView("automations")
	tt.Frame()
	if _, ok := tt.Find("kanban-list-group-todo"); ok {
		t.Fatal("切换后仍显示任务列表")
	}
	s.setTasksWindowView("tasks")
	tt.Frame()
	if _, ok := tt.Find("kanban-list-group-todo"); !ok {
		t.Fatal("切回后未显示任务列表")
	}
	if ks.listScroll.Y != before {
		t.Fatalf("切换目的后列表位置从 %v 变为 %v", before, ks.listScroll.Y)
	}
}

func TestKanbanShellAllProjects(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	s.projects = []Project{{ID: "p1", Name: "项目", Path: "/project"}}
	ks := getKanbanState(s)
	ks.scopeID = "p1"
	ks.initialized = true
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if err := tt.Click("全部项目"); err != nil {
		t.Fatal(err)
	}
	kanbanMu.Lock()
	all, scope := ks.overview, ks.scopeID
	kanbanMu.Unlock()
	if !all || scope != "" {
		t.Fatalf("范围错误: all=%v scope=%q", all, scope)
	}
	s.close()
}

func TestKanbanShellNoGoals(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if tt.HasText("目标") {
		t.Fatal("没有目标时出现目标分组")
	}
	s.close()
}

func TestTasksWindowEscapeKeepsWindow(t *testing.T) {
	fake := NewFakeClient()
	var s *chatState
	tt := ui.NewTester(Root(fake, WithTasksWindowMode("tasks"), func(state *chatState) { s = state }), 840, 520)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.tasksWindowView == "" || !kanbanModalOpen(s) {
		t.Fatal("Esc 关闭了独立任务窗口")
	}
}

func TestKanbanShellAutomationSearch(t *testing.T) {
	s := newChatState(NewFakeClient())
	as := getAutomationState(s)
	as.autos = []Automation{testAutomation("apple", "p1"), testAutomation("banana", "p1")}
	as.initialized = true
	ks := getKanbanState(s)
	tt := ui.NewTester(func(c *ui.Context) {
		renderKanbanModalBody(c, s, ks, kanbanModalBodyProps{destination: "automations", automationSearch: "apple"})
	}, 1140, 720)
	tt.Frame()
	if !tt.HasText("apple") || tt.HasText("banana") {
		t.Fatal("自动化搜索没有过滤列表")
	}
}

func TestKanbanShellGoalFilterAndDetail(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	s.projects = []Project{{ID: "p1", Name: "项目"}}
	ks := getKanbanState(s)
	ks.initialized = true
	ks.board = KanbanBoard{
		Goals: []KanbanGoal{{ID: "goal1", ProjectID: "p1", Title: "目标一", Status: "running", Done: 1, Total: 2}},
		Tasks: []KanbanTask{{ID: "one", ProjectID: "p1", GoalID: "goal1", Title: "目标任务", Column: "todo"}, {ID: "other", ProjectID: "p1", Title: "其他任务", Column: "todo"}},
	}
	tt := ui.NewTester(view, 1140, 720)
	tt.Frame()
	waitFor(t, tt, func() bool { return tt.HasText("目标任务") && tt.HasText("其他任务") })
	waitFor(t, tt, func() bool { return tt.HasText("目标一") })
	if err := tt.Click("目标一"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("目标一 · 1/2") && !tt.HasText("其他任务") })
	tt.Advance(DurBase + 50*time.Millisecond)
	if err := tt.Click("目标详情 目标一"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-goal-detail"); return ok && tt.HasText("产物") })
	s.close()
}
