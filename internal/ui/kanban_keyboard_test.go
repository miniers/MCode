package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func TestKanbanKeyboardFindFocusesSearch(t *testing.T) {
	for _, destination := range []string{"tasks", "automations"} {
		t.Run(destination, func(t *testing.T) {
			tt, s := newWorkspaceTester(t, NewFakeClient(), "tasks")
			s.setTasksWindowView(destination)
			tt.Frame()
			tt.Key(ui.Cmd, ui.KeyF)
			waitFor(t, tt, func() bool { return tt.Focused("筛选任务") })
		})
	}
}

func TestKanbanKeyboardWindowFindFocusesSearch(t *testing.T) {
	for _, destination := range []string{"tasks", "automations"} {
		t.Run(destination, func(t *testing.T) {
			s, view := buildRoot(NewFakeClient(), WithTasksWindowMode(destination))
			t.Cleanup(s.close)
			tt := ui.NewTester(view, 1320, 860)
			waitFor(t, tt, func() bool { _, ok := tt.Find("筛选任务"); return ok })
			tt.Key(ui.Cmd, ui.KeyF)
			waitFor(t, tt, func() bool { return tt.Focused("筛选任务") })
		})
	}
}

func TestKanbanKeyboardVisibleOrderAndFocus(t *testing.T) {
	s := &chatState{}
	defer func() { kanbanKeyboardMu.Lock(); delete(kanbanKeyboardStates, s); kanbanKeyboardMu.Unlock() }()
	tasks := []KanbanTask{{ID: "c", Status: "done"}, {ID: "a", Status: "todo"}, {ID: "b", Status: "todo"}}
	kanbanKeyboardUpdate(s, tasks, config.KanbanViewKanban)
	st := kanbanKeyboardStates[s]
	if len(st.tasks) != 3 || st.tasks[0].ID != "a" || st.tasks[2].ID != "c" {
		t.Fatalf("unexpected order: %+v", st.tasks)
	}
	kanbanKeyboardMove(s, "a", -1)
	if st.pendingID != "" {
		t.Fatalf("moved past first task: %q", st.pendingID)
	}
	kanbanKeyboardMove(s, "a", 1)
	if st.focusID != "b" {
		t.Fatalf("wrong next task: %q", st.focusID)
	}
	kanbanKeyboardMoveColumn(s, "b", 1)
	if st.focusID != "c" {
		t.Fatalf("did not skip empty columns: %q", st.focusID)
	}
	st.focusID = "b"
	kanbanKeyboardUpdate(s, []KanbanTask{tasks[0], tasks[2]}, config.KanbanViewKanban)
	if st.focusID != "b" {
		t.Fatalf("focus not preserved: %q", st.focusID)
	}
	kanbanKeyboardUpdate(s, []KanbanTask{tasks[0]}, config.KanbanViewKanban)
	if st.focusID != "c" || st.pendingID != "c" {
		t.Fatalf("focus not moved to neighbour: %+v", st)
	}
	kanbanKeyboardUpdate(s, nil, config.KanbanViewKanban)
	if st.focusID != "" {
		t.Fatalf("focus retained with no tasks: %q", st.focusID)
	}
}

func TestKanbanKeyboardSelection(t *testing.T) {
	s := &chatState{}
	defer func() { kanbanKeyboardMu.Lock(); delete(kanbanKeyboardStates, s); kanbanKeyboardMu.Unlock() }()
	tasks := []KanbanTask{{ID: "a", Status: "todo"}, {ID: "b", Status: "todo"}, {ID: "c", Status: "todo"}}
	kanbanKeyboardUpdate(s, tasks, config.KanbanViewKanban)
	kanbanKeyboardSelect(s, "a", "toggle")
	kanbanKeyboardExtend(s, "a", 1)
	if !kanbanKeyboardSelected(s, "a") || !kanbanKeyboardSelected(s, "b") || kanbanKeyboardSelected(s, "c") {
		t.Fatal("range selection did not follow visible order")
	}
	kanbanKeyboardUpdate(s, tasks[1:], config.KanbanViewKanban)
	kanbanKeyboardSelect(s, "b", "all")
	if kanbanKeyboardSelected(s, "a") || !kanbanKeyboardSelected(s, "b") || !kanbanKeyboardSelected(s, "c") {
		t.Fatal("select all included a hidden task")
	}
	kanbanKeyboardProject(s, "another-project")
	if kanbanKeyboardSelected(s, "b") || kanbanKeyboardSelected(s, "c") {
		t.Fatal("selection survived project change")
	}
}

func TestKanbanKeyboardEscapeClearsSelectionFirst(t *testing.T) {
	s := &chatState{}
	defer func() { kanbanKeyboardMu.Lock(); delete(kanbanKeyboardStates, s); kanbanKeyboardMu.Unlock() }()
	kanbanKeyboardUpdate(s, []KanbanTask{{ID: "a", Status: "todo"}}, config.KanbanViewKanban)
	kanbanKeyboardSelect(s, "a", "toggle")
	ks := getKanbanState(s)
	kanbanMu.Lock()
	ks.detailTaskID = "a"
	kanbanMu.Unlock()
	if !consumeKanbanEscape(s) || kanbanKeyboardSelected(s, "a") {
		t.Fatal("escape did not clear selection")
	}
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	if ks.detailTaskID != "a" {
		t.Fatal("escape closed detail before clearing selection")
	}
}
