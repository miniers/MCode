package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKanbanDetailCancelTask(t *testing.T) {
	fake := NewFakeClient()
	inProgress := KanbanTask{ID: "task-run", ProjectID: "p1", Title: "执行中任务", Status: "in_progress", Column: "in_progress", Revision: 4}
	review := KanbanTask{ID: "task-rev", ProjectID: "p1", Title: "待审阅任务", Status: "review", Column: "review", CanReview: true, Revision: 2}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{inProgress, review}})
	tt, s := newWorkspaceTester(t, fake, "tasks")

	waitFor(t, tt, func() bool { return tt.HasText(inProgress.Title) })
	s.openTaskDetail(inProgress)
	waitFor(t, tt, func() bool { _, ok := tt.Find("任务更多操作"); return ok })
	if err := tt.Click("任务更多操作"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-detail-cancel"); return ok })
	if err := tt.Click("kanban-detail-cancel"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-detail-cancel-dialog-confirm")
		return ok && tt.HasText("会停止正在运行的执行会话")
	})
	if err := tt.Click("kanban-detail-cancel-dialog-confirm"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.MoveTaskCalls()) == 1 })
	call := fake.MoveTaskCalls()[0]
	if call.TaskID != inProgress.ID || call.Status != "cancelled" || call.Revision != inProgress.Revision {
		t.Fatalf("MoveTask 调用参数不匹配: %+v", call)
	}

	s.openTaskDetail(review)
	waitFor(t, tt, func() bool { return tt.HasText(review.Title) })
	if err := tt.Click("任务更多操作"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-detail-delete"); return ok })
	if _, ok := tt.Find("kanban-detail-cancel"); ok {
		t.Fatal("review 任务不应出现取消任务按钮")
	}
}

func TestStewardTaskBandCancel(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "task-1", ThreadID: "t1", Status: "in_progress", Column: "in_progress", Revision: 5}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.stewardBoard.tasks = []KanbanTask{task}
	s.stewardTaskThreadID = "t1"
	s.stewardTask = stewardTaskSnapshot{ID: "task-1", Status: "in_progress"}
	s.mu.Unlock()

	tt := ui.NewTesterWith(func(c *ui.Context) { renderStewardTaskBand(c, s, "t1") }, 800, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	if _, ok := tt.Find("取消任务"); !ok {
		t.Fatal("in_progress 状态总管任务状态带应显示取消任务按钮")
	}
	if err := tt.Click("取消任务"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("steward-task-cancel-dialog-task-1-confirm")
		return ok && tt.HasText("会停止正在运行的执行会话")
	})
	if err := tt.Click("steward-task-cancel-dialog-task-1-confirm"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.MoveTaskCalls()) == 1 })
	call := fake.MoveTaskCalls()[0]
	if call.TaskID != "task-1" || call.Status != "cancelled" || call.Revision != 5 {
		t.Fatalf("MoveTask 调用参数不匹配: %+v", call)
	}

	s.mu.Lock()
	s.stewardTask = stewardTaskSnapshot{ID: "task-1", Status: "done"}
	s.mu.Unlock()
	s.invalidate()
	tt.Frame()
	if _, ok := tt.Find("取消任务"); ok {
		t.Fatal("done 状态总管任务状态带不应显示取消任务按钮")
	}
}
