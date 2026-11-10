package mcodeui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func TestKanbanHeaderButtonsStayWithinWindow(t *testing.T) {
	for _, size := range []struct{ width, height int }{{840, 520}, {1140, 720}, {1440, 900}} {
		for _, view := range []string{config.KanbanViewKanban, config.KanbanViewList, kanbanViewAutomations} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.width, size.height, view), func(t *testing.T) {
				state, root := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
				state.projects = []Project{{ID: "project", Path: "/project", Name: strings.Repeat("很长的项目名称", 20)}}
				ks := getKanbanState(state)
				ks.initialized, ks.scopeID = true, "project"
				ks.viewMode = config.KanbanViewKanban
				if view == kanbanViewAutomations {
					ks.destination = "automations"
				} else {
					ks.viewMode = view
				}
				tt := ui.NewTester(root, size.width, size.height)
				for i := 0; i < 5; i++ {
					tt.Frame()
				}
				header, ok := tt.Find("tasks-window-toolbar")
				wantHeight := TasksToolbarHeight
				if !ok || header.H != wantHeight {
					t.Errorf("头部高度不符：bounds=%+v，found=%v，want=%v", header, ok, wantHeight)
				}
				labels := []string{"kanban-destination-tasks", "kanban-destination-automations", "筛选任务"}
				if view != kanbanViewAutomations {
					labels = append(labels, "kanban-view-switch-kanban", "kanban-view-switch-list", "新建任务", "新建菜单")
				}
				sidebar, found := tt.Find("tasks-sidebar")
				if !found || sidebar.W != TasksSidebarWidth {
					t.Errorf("侧栏宽度不符：%+v", sidebar)
				}
				state.close()
				for _, label := range labels {
					r, ok := tt.Find(label)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > float32(size.width) {
						t.Errorf("%s 超出窗口或不可见：bounds=%+v，found=%v", label, r, ok)
					}
				}
			})
		}
	}
}
