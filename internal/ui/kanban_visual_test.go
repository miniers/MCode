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

func visualDragToColumn(t *testing.T, tt *ui.Tester, taskID, column string) {
	t.Helper()
	from, ok := tt.Find("kanban-task-card-" + taskID)
	if !ok {
		t.Fatal("拖拽源不存在")
	}
	to, ok := tt.Find("kanban-column-" + column)
	if !ok {
		t.Fatal("投放列不存在")
	}
	sx, sy := from.X+5, from.Y+5
	x, y := to.X+to.W/2, to.Y+to.H-20
	tt.Press(sx, sy)
	for step := 1; step <= 4; step++ {
		f := float32(step) / 4
		tt.Move(sx+(x-sx)*f, sy+(y-sy)*f)
	}
	tt.Release(x, y)
}

func visualColumnTester(t *testing.T, task KanbanTask, target string) (*ui.Tester, *chatState, *FakeClient) {
	t.Helper()
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	s := newChatState(fake)
	t.Cleanup(s.close)
	ks := getKanbanState(s)
	tt := ui.NewTesterWith(func(c *ui.Context) {
		ui.Row(c).FillWidth().FillHeight().Children(func() {
			for _, def := range kanbanColumnDefs {
				if def.key == task.Column || def.key == target {
					renderKanbanColumn(c, s, ks, def, []KanbanTask{task}, nil, false, 280, kanbanTaskCardState{})
				}
			}
		})
	}, 600, 400, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	return tt, s, fake
}

func TestKanbanVisualEmptyColumnDrop(t *testing.T) {
	for _, target := range []string{"review", "needs_you"} {
		t.Run(target, func(t *testing.T) {
			task := KanbanTask{ID: "drop", ProjectID: "p1", Title: "投放任务", Status: "todo", Column: "todo", CanMove: true, Revision: 3}
			tt, _, fake := visualColumnTester(t, task, target)
			col, ok := tt.Find("kanban-column-" + target)
			if !ok || col.H < KanbanEmptyDropMinHeight {
				t.Fatalf("空列高度不足: %v", col)
			}
			visualDragToColumn(t, tt, task.ID, target)
			if target == "review" {
				waitFor(t, tt, func() bool { return len(fake.MoveTaskCalls()) == 1 })
				call := fake.MoveTaskCalls()[0]
				if call.TaskID != task.ID || call.Status != target || call.Revision != task.Revision {
					t.Fatalf("错误的移动: %+v", call)
				}
			} else {
				tt.Frame()
				if calls := fake.MoveTaskCalls(); len(calls) != 0 {
					t.Fatalf("非法投放调用移动: %+v", calls)
				}
				fake.mu.Lock()
				got := fake.kanbanBoard.Tasks[0]
				fake.mu.Unlock()
				if got.Column != task.Column || got.Status != task.Status {
					t.Fatalf("非法投放改变任务: %+v", got)
				}
				if _, ok := tt.Find("kanban-task-card-" + task.ID); !ok {
					t.Fatal("源卡片消失")
				}
			}
		})
	}
}

func TestKanbanVisualDoneRailReviewDrop(t *testing.T) {
	task := KanbanTask{ID: "review-drop", ProjectID: "p1", Title: "审阅任务", Status: "review", Column: "review", CanMove: true, CanReview: true, Revision: 3}
	tt, s, fake := visualColumnTester(t, task, "done")
	visualDragToColumn(t, tt, task.ID, "done")
	ks := getKanbanState(s)
	kanbanMu.Lock()
	id, tab, expanded := ks.detailTaskID, ks.detailTab, ks.doneExpanded
	kanbanMu.Unlock()
	if id != task.ID || tab != "diff" || expanded {
		t.Fatalf("未在折叠轨打开审阅: id=%s tab=%s expanded=%v", id, tab, expanded)
	}
	if len(fake.MoveTaskCalls()) != 0 || len(fake.ReviewTaskCalls()) != 0 {
		t.Fatal("投放不应移动或接受审阅")
	}
}

func TestKanbanVisualColumnWidthStates(t *testing.T) {
	for _, width := range []float32{619, 900, 1400} {
		for _, collapsed := range []int{0, 1} {
			expanded := 5 - collapsed
			got := kanbanColumnWidthFor(width, expanded, collapsed)
			want := max(KanbanColumnMinWidth, (width-float32(collapsed)*KanbanDoneRailWidth-4*kanbanColumnSpacing)/float32(expanded))
			if got != want || got < KanbanColumnMinWidth {
				t.Errorf("width=%v collapsed=%d got=%v want=%v", width, collapsed, got, want)
			}
		}
	}
}

func TestKanbanVisualOpenFocusSelectedStates(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	ks := getKanbanState(s)
	task := KanbanTask{ID: "states", Title: "状态卡", Status: "todo", Column: "todo"}
	tt := ui.NewTesterWith(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	before, ok := tt.Find("kanban-task-card-states")
	if !ok {
		t.Fatal("缺少卡片")
	}
	kanbanMu.Lock()
	ks.detailTaskID = task.ID
	ks.selectedTaskIDs = map[string]bool{task.ID: true}
	kanbanMu.Unlock()
	tt.Key(0, ui.KeyTab)
	after, _ := tt.Find("kanban-task-card-states")
	if before != after {
		t.Fatalf("状态改变卡片几何: %v -> %v", before, after)
	}
	if !tt.Focused("kanban-task-card-states") {
		t.Fatal("卡片未聚焦")
	}
	for _, label := range []string{"kanban-task-open-states", "kanban-task-focus-states", "kanban-task-selection-states"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("缺少状态标记 %s", label)
		}
	}
}
