package mcodeui

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

type tasksAcceptanceClient struct{ *FakeClient }

func (c *tasksAcceptanceClient) Projects(context.Context) ([]Project, error) {
	return []Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}, nil
}

func TestTasksAcceptanceToolbarGeometry(t *testing.T) {
	for _, width := range []int{840, 1140, 1440} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
			defer s.close()
			s.projects = []Project{{ID: "p1", Name: "项目一"}}
			getKanbanState(s).initialized = true
			tt := ui.NewTester(view, width, 720)
			tt.Frame()
			sidebar, ok := tt.Find("tasks-sidebar")
			if !ok || sidebar.W != TasksSidebarWidth {
				t.Fatalf("侧栏: %+v", sidebar)
			}
			bar, ok := tt.Find("tasks-window-toolbar")
			if !ok || bar.X != TasksSidebarWidth || bar.H != TasksToolbarHeight || bar.X+bar.W > float32(width) {
				t.Fatalf("顶栏: %+v", bar)
			}
			if _, ok := tt.Find("kanban-modal-header"); ok {
				t.Fatal("出现旧顶栏")
			}
			for _, label := range []string{"筛选任务", "kanban-view-switch-kanban", "kanban-view-switch-list", "新建任务", "新建菜单"} {
				r, ok := tt.Find(label)
				if !ok || r.X < bar.X || r.X+r.W > bar.X+bar.W || r.Y < bar.Y || r.Y+r.H > bar.Y+bar.H {
					t.Errorf("%s: %+v", label, r)
				}
			}
		})
	}
}

func TestTasksAcceptanceAttentionIgnoresSearch(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	defer s.close()
	ks := getKanbanState(s)
	ks.initialized = true
	ks.board = KanbanBoard{Tasks: []KanbanTask{{ID: "a", Title: "审批任务", Status: "failed"}}, Goals: []KanbanGoal{{ID: "g", Title: "目标", Status: "plan_review"}}}
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if !tt.HasText("2") {
		t.Fatal("未显示待处理计数")
	}
	ks.searchText = "不匹配"
	tt.Frame()
	if !tt.HasText("2") {
		t.Fatal("搜索影响待处理计数")
	}
	ks.board.Tasks[0].Status = "done"
	tt.Frame()
	if !tt.HasText("1") || tt.HasText("2") {
		t.Fatal("处理后计数未下降")
	}
	ks.board.Goals[0].Status = "running"
	tt.Frame()
	if tt.HasText("0") || tt.HasText("1") {
		t.Fatal("零计数不应显示")
	}
}

func TestTasksAcceptanceDetailWidthPersistence(t *testing.T) {
	resetLastSelectionMergeState(t)
	initial := &kanbanState{persistTasksDetailWidth: true}
	rememberTasksDetailWidth(initial, 500)
	waitUIStateWrites()
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	defer s.close()
	ks := getKanbanState(s)
	ks.initialized = true
	ks.detailTaskID = "a"
	if ks.detailWidth != 500 {
		t.Fatalf("恢复宽度 %v", ks.detailWidth)
	}
	tt := ui.NewTester(view, 1440, 900)
	tt.Frame()
	r, ok := tt.Find("调整详情宽度")
	if !ok {
		t.Fatal("找不到分栏手柄")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x-40, y)
	tt.Release(x-40, y)
	waitUIStateWrites()
	if got := loadTasksDetailWidth(); got != 540 {
		t.Fatalf("保存宽度 %v", got)
	}
}

func TestTasksAcceptanceSearchScopeAndCreate(t *testing.T) {
	client := &tasksAcceptanceClient{NewFakeClient()}
	s, view := buildTasksRoot(client, WithTasksWindowMode("tasks"))
	defer s.close()
	s.projects = []Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}
	ks := getKanbanState(s)
	ks.initialized = true
	ks.searchText = "任务查询"
	ks.automationSearch = "自动查询"
	tt := ui.NewTester(view, 1140, 720)
	tt.Frame()
	s.setTasksWindowView("automations")
	s.setKanbanScope("p2", false)
	s.setTasksWindowView("tasks")
	s.setKanbanScope("", true)
	s.setTasksWindowView("automations")
	tt.Frame()
	if ks.searchText != "任务查询" || ks.automationSearch != "自动查询" {
		t.Fatal("切换后搜索丢失")
	}
	if err := tt.Click("新建自动化"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	as := getAutomationState(s)
	if !as.chooseProjectOpen || as.editorOpen {
		t.Fatal("未打开项目选择")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if as.chooseProjectOpen || as.editorOpen {
		t.Fatal("取消后打开编辑器")
	}
	if err := tt.Click("新建自动化"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("选择项目 项目二"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !as.editorOpen || as.editorProjectID != "p2" || ks.scopeID != "" {
		t.Fatalf("选择项目后状态错误: %v %q %q", as.editorOpen, as.editorProjectID, ks.scopeID)
	}
	as.name = "未保存"
	s.setTasksWindowView("tasks")
	tt.Frame()
	if ks.destination != "automations" || !as.confirmDiscard {
		t.Fatal("未保护草稿")
	}
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ks.destination != "tasks" {
		t.Fatal("放弃后未切换")
	}
}
