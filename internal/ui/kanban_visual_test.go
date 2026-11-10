package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKanbanColumnWidthForViewport(t *testing.T) {
	if got := kanbanColumnWidthFor(400, 4, 1); got != KanbanColumnMinWidth {
		t.Fatalf("narrow viewport: %v", got)
	}
	want := (float32(1600) - KanbanDoneRailWidth - 4*kanbanColumnSpacing) / 4
	if got := kanbanColumnWidthFor(1600, 4, 1); got != want {
		t.Fatalf("wide viewport: got %v, want %v", got, want)
	}
	if got := kanbanColumnWidthFor(1600, 0, 1); got != KanbanColumnMinWidth {
		t.Fatalf("no expanded columns: %v", got)
	}
}

func TestKanbanCardActionSlotStable(t *testing.T) {
	for _, width := range []int{280, 400} {
		for _, signal := range []bool{false, true} {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/signal=%t/mixed=%t", width, signal, mixed), func(t *testing.T) {
					s := newChatState(NewFakeClient())
					defer s.close()
					s.projects = []Project{{ID: "p1", Name: "项目一"}}
					ks := &kanbanState{scopeID: "p1"}
					if mixed {
						ks.scopeID = ""
					}
					task := KanbanTask{ID: "long", ProjectID: "p1", Title: "一个很长很长很长很长很长很长的任务标题", Status: "todo", Column: "todo", CanStart: true}
					if signal {
						task.Blocked = true
						task.DependsOn = []string{"dep"}
					}
					tt := ui.NewTesterWith(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, width, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
					before, ok := tt.Find("kanban-task-card-long")
					if !ok {
						t.Fatal("卡片不存在")
					}
					title, _ := tt.Find(task.Title)
					if _, ok := tt.Find("kanban-task-long-more"); ok {
						t.Fatal("未悬停时不应显示动作")
					}
					hoverKanbanCard(t, tt, task.ID)
					after, _ := tt.Find("kanban-task-card-long")
					titleAfter, _ := tt.Find(task.Title)
					if before != after || title != titleAfter {
						t.Fatalf("动作改变布局: card %v -> %v, title %v -> %v", before, after, title, titleAfter)
					}
					if _, ok := tt.Find("kanban-task-long-more"); !ok {
						t.Fatal("悬停时缺少动作")
					}
				})
			}
		}
	}
}

func TestKanbanMorePopoverStableAndEscape(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	ks := &kanbanState{scopeID: "p1"}
	task := KanbanTask{ID: "menu", Title: "菜单任务", Status: "todo", Column: "todo"}
	tt := ui.NewTesterWith(func(c *ui.Context) {
		kanbanMu.Lock()
		st := kanbanTaskCardState{rescueMenuTaskID: ks.rescueMenuTaskID, moveMenuTaskID: ks.moveMenuTaskID}
		kanbanMu.Unlock()
		renderTaskCard(c, s, ks, task, st)
	}, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	before, _ := tt.Find("kanban-task-card-menu")
	hoverKanbanCard(t, tt, task.ID)
	if err := tt.Click("kanban-task-menu-more"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("kanban-task-rescue-delete-menu"); !ok {
		t.Fatal("菜单未打开")
	}
	after, _ := tt.Find("kanban-task-card-menu")
	if before != after {
		t.Fatalf("菜单改变卡片布局: %v -> %v", before, after)
	}
	tt.Key(0, ui.KeyEscape)
	if _, ok := tt.Find("kanban-task-rescue-delete-menu"); ok {
		t.Fatal("Esc 未关闭菜单")
	}
}

func TestKanbanDoneRailReduceMotion(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	ks := &kanbanState{}
	var def kanbanColumnDef
	for _, col := range kanbanColumnDefs {
		if col.key == "done" {
			def = col
		}
	}
	tt := ui.NewTesterWith(func(c *ui.Context) { renderKanbanColumn(c, s, ks, def, nil, nil, false, 280, kanbanTaskCardState{}) }, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	if err := tt.Click("kanban-column-done"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("收起已完成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("减少动效时折叠不应续帧")
	}
}

func TestKanbanCardFocusAndSelectionHaveSeparateMarkers(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	ks := &kanbanState{scopeID: "p1", selectedTaskIDs: map[string]bool{"state": true}, detailTaskID: "state"}
	task := KanbanTask{ID: "state", Title: "状态卡", Status: "todo", Column: "todo"}
	tt := ui.NewTesterWith(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{selecting: true}) }, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("kanban-task-card-state") {
		t.Fatal("卡片未获得焦点")
	}
	if _, ok := tt.Find("kanban-task-focus-state"); !ok {
		t.Fatal("缺少焦点轮廓标记")
	}
	if _, ok := tt.Find("kanban-task-selection-state"); !ok {
		t.Fatal("缺少多选标记")
	}
	if _, ok := tt.Find("kanban-task-card-state"); !ok {
		t.Fatal("详情打开状态丢失")
	}
}
