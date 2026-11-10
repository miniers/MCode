package mcodeui

import "testing"

func TestKanbanGoalOutputsFilterByGoalID(t *testing.T) {
	board := KanbanBoard{Tasks: []KanbanTask{{ID: "a", GoalID: "one"}, {ID: "b", GoalID: "two"}}}
	outputs := goalOutputs(board, "one")
	if len(outputs) != 1 || outputs[0].Task.ID != "a" {
		t.Fatalf("目标产物不匹配: %+v", outputs)
	}
}
