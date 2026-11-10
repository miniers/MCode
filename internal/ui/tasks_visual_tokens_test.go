package mcodeui

import (
	"go/parser"
	"testing"
)

func TestTasksVisualTokens(t *testing.T) {
	values := map[string]float32{"TasksSidebarWidth": TasksSidebarWidth, "TasksToolbarHeight": TasksToolbarHeight, "TasksSearchWidth": TasksSearchWidth, "TasksSearchMinWidth": TasksSearchMinWidth, "KanbanColumnMinWidth": KanbanColumnMinWidth, "KanbanDoneRailWidth": KanbanDoneRailWidth, "KanbanEmptyDropMinHeight": KanbanEmptyDropMinHeight, "KanbanCardActionSlotWidth": KanbanCardActionSlotWidth, "KanbanFocusRingWidth": KanbanFocusRingWidth, "KanbanDragSourceOpacity": KanbanDragSourceOpacity, "KanbanBoardPaneMinWidth": KanbanBoardPaneMinWidth, "KanbanDetailMinWidth": KanbanDetailMinWidth, "KanbanDetailDefaultWidth": KanbanDetailDefaultWidth, "KanbanDetailMaxWidth": KanbanDetailMaxWidth}
	for name, value := range values {
		if value <= 0 || !designTokenNames[name] {
			t.Errorf("%s: value=%v registered=%v", name, value, designTokenNames[name])
		}
		expr, err := parser.ParseExpr(name + " / 2")
		if err != nil || !tokenArithmeticArg(expr) {
			t.Errorf("%s arithmetic not detected", name)
		}
	}
	if TasksSearchMinWidth > TasksSearchWidth || KanbanDoneRailWidth >= KanbanColumnMinWidth || KanbanColumnMinWidth >= KanbanBoardPaneMinWidth || KanbanDetailMinWidth > KanbanDetailDefaultWidth || KanbanDetailDefaultWidth > KanbanDetailMaxWidth || KanbanDragSourceOpacity >= 1 {
		t.Fatal("invalid geometry")
	}
}
