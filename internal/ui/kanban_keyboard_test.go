package mcodeui

import (
	"testing"

	"mcode/internal/config"
)

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
