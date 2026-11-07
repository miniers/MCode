package mcodeui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func TestKanbanHeaderButtonsStayWithinWindow(t *testing.T) {
	for _, size := range []struct{ width, height int }{{840, 560}, {840, 520}, {1280, 800}} {
		for _, view := range []string{config.KanbanViewKanban, config.KanbanViewList, kanbanViewAutomations} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.width, size.height, view), func(t *testing.T) {
				state := newChatState(NewFakeClient())
				state.tasksWindowView = "tasks"
				state.projects = []Project{{ID: "project", Name: strings.Repeat("很长的项目名称", 20)}}
				ks := &kanbanState{}
				props := kanbanModalBodyProps{projectID: "project", viewMode: view}
				tt := ui.NewTester(func(c *ui.Context) {
					ui.Column(c).Fill().Children(func() { renderKanbanModalHeader(c, state, ks, props, float32(size.width)) })
				}, size.width, size.height)
				for i := 0; i < 5; i++ {
					tt.Frame()
				}
				header, ok := tt.Find("kanban-modal-header")
				wantHeight := kanbanTopBarHeight
				if size.width == 840 {
					wantHeight *= 2
				}
				if !ok || header.H != wantHeight {
					t.Errorf("头部高度不符：bounds=%+v，found=%v，want=%v", header, ok, wantHeight)
				}
				task, ok := tt.Find("新建任务")
				if !ok || (size.width == 1280 && task.W <= ControlMD) || (size.width == 840 && task.W != ControlMD) {
					t.Errorf("新建任务按钮布局不符：bounds=%+v，found=%v", task, ok)
				}
				for _, label := range []string{"刷新", "关闭任务与看板", "新建任务", "新建目标"} {
					r, ok := tt.Find(label)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > float32(size.width) {
						t.Errorf("%s 超出窗口或不可见：bounds=%+v，found=%v", label, r, ok)
					}
				}
			})
		}
	}
}
