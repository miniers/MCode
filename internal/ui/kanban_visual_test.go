package mcodeui

import "testing"

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
