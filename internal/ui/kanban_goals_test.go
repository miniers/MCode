package mcodeui

import "testing"

func TestKanbanGoalOutputsFilterByGoalID(t *testing.T) {
	board := KanbanBoard{Tasks: []KanbanTask{{ID: "a", GoalID: "one"}, {ID: "b", GoalID: "two"}}}
	outputs := goalOutputs(board, "one")
	if len(outputs) != 1 || outputs[0].Task.ID != "a" {
		t.Fatalf("目标产物不匹配: %+v", outputs)
	}
}

func TestKanbanGoalTaskReturnAndDirectOpen(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "goal-task", ProjectID: "p1", GoalID: "g", Title: "目标任务", Status: "todo", Column: "todo"}
	fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{{ID: "g", ProjectID: "p1", Title: "目标名称", Status: "plan_review"}}, Tasks: []KanbanTask{task}})
	tt, s := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	ks := getKanbanState(s)
	kanbanMu.Lock()
	ks.goalID, ks.goalDetailOpen, ks.planReviewGoalID = "g", true, "g"
	kanbanMu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool { _, ok := tt.Find("任务卡片 目标任务"); return ok })
	if err := tt.Click("任务卡片 目标任务"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("← 目标名称") })
	s.closeTaskDetail()
	waitFor(t, tt, func() bool { _, ok := tt.Find("确认并开始"); return ok })
	s.openTaskDetail(task)
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-detail-close"); return ok })
	if tt.HasText("← 目标名称") {
		t.Fatal("直接打开任务不应返回目标")
	}
}
