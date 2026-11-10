package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

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

func TestKanbanGoalPlanningCompletionDoesNotOpenDetail(t *testing.T) {
	fake := NewFakeClient()
	goal := KanbanGoal{ID: "g", ProjectID: "p1", Title: "后台目标", Status: "planning"}
	fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{goal}})
	tt, s := newWorkspaceTester(t, fake, "tasks")
	ks := getKanbanState(s)
	waitFor(t, tt, func() bool { kanbanMu.Lock(); defer kanbanMu.Unlock(); return len(ks.board.Goals) > 0 })
	goal.Status = "plan_review"
	fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{goal}})
	s.refreshKanban()
	waitFor(t, tt, func() bool {
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		return len(ks.board.Goals) > 0 && ks.board.Goals[0].Status == "plan_review"
	})
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	if ks.goalDetailOpen || ks.planReviewGoalID != "" {
		t.Fatal("后台规划完成不应打开详情或审阅")
	}
}

func TestKanbanGoalRunningAndPausedActions(t *testing.T) {
	for _, status := range []string{"running", "paused"} {
		t.Run(status, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{{ID: "g", ProjectID: "p1", Title: "状态目标", Status: status}}})
			tt, s := newWorkspaceTester(t, fake, "tasks")
			ks := getKanbanState(s)
			waitFor(t, tt, func() bool { kanbanMu.Lock(); defer kanbanMu.Unlock(); return len(ks.board.Goals) > 0 })
			kanbanMu.Lock()
			ks.goalID, ks.goalDetailOpen = "g", true
			kanbanMu.Unlock()
			s.invalidate()
			waitFor(t, tt, func() bool { return tt.HasText("状态目标") })
			if status == "running" && tt.HasText("重新规划") {
				t.Fatal("运行中不能重新规划")
			}
			if status == "paused" && tt.HasText("已停止会话") {
				t.Fatal("暂停不应声称会话已停止")
			}
		})
	}
}

func TestKanbanGoalScrollRestoredAfterTask(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := getKanbanState(s)
	ks.goalID, ks.goalDetailOpen = "g", true
	ks.board.Goals = []KanbanGoal{{ID: "g", Title: "目标", Status: "paused"}}
	for i := 0; i < 30; i++ {
		ks.board.Tasks = append(ks.board.Tasks, KanbanTask{ID: fmt.Sprint(i), GoalID: "g", Title: fmt.Sprintf("任务 %d", i), Column: "todo"})
	}
	tt := ui.NewTester(func(c *ui.Context) {
		if ks.detailTaskID != "" {
			renderTaskDetailContent(c, s, ks)
		} else {
			renderGoalDetailContent(c, s, ks)
		}
	}, 500, 500)
	tt.Frame()
	tt.Scroll(250, 250, 0, 600)
	tt.Advance(time.Second)
	before := ks.goalDetailScroll.Y
	if before <= 0 {
		t.Fatal("目标详情未滚动")
	}
	s.openTaskDetailFromGoal(ks.board.Tasks[0], true)
	tt.Frame()
	s.closeTaskDetail()
	tt.Frame()
	if ks.goalDetailScroll.Y != before {
		t.Fatalf("返回后滚动位置从 %v 变为 %v", before, ks.goalDetailScroll.Y)
	}
}
