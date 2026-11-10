package mcodeui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
)

type mergeFailedClient struct{ *FakeClient }

func (f *mergeFailedClient) ReviewTask(_ context.Context, id, decision, feedback string, revision int) (KanbanTask, error) {
	task := KanbanTask{ID: id, ProjectID: "p1", Title: "合并任务", Status: "review", Column: "review", CanReview: true, CanApprove: true, ReviewStatus: "merge_failed", LastFailureReason: "存在冲突", Revision: revision}
	f.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	return task, nil
}

func TestKanbanDetailMergeFailureKeepsPanel(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "merge", ProjectID: "p1", Title: "合并任务", Status: "review", Column: "review", CanReview: true, CanApprove: true}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	tt, s := newWorkspaceTester(t, &mergeFailedClient{fake}, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	s.openTaskDetail(task)
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-detail-accept"); return ok })
	if err := tt.Click("kanban-task-detail-accept"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("合并失败: 存在冲突") })
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	if kanbanStates[s].detailTaskID != task.ID {
		t.Fatal("合并失败时详情不应关闭")
	}
}

func TestKanbanDetailMissingTaskIsVisible(t *testing.T) {
	fake := NewFakeClient()
	tt, s := newWorkspaceTester(t, fake, "tasks")
	s.openTaskDetail(KanbanTask{ID: "missing"})
	waitFor(t, tt, func() bool { return tt.HasText("任务已不存在") })
}

func TestKanbanDetailSheetAndPanelShareContent(t *testing.T) {
	for _, width := range []int{800, 1400} {
		fake := NewFakeClient()
		task := KanbanTask{ID: "shared", ProjectID: "p1", Title: "共同内容", AcceptanceCriteria: "[你验] 人工验收", Status: "todo", Column: "todo"}
		fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
		var s *chatState
		tt := ui.NewTester(Root(fake, func(state *chatState) { s = state }), width, 800)
		waitFor(t, tt, func() bool { return s != nil })
		s.openWorkspace("tasks")
		waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
		s.openTaskDetail(task)
		waitFor(t, tt, func() bool { return tt.HasText("验收标准: [你验] 人工验收") })
	}
}

func TestKanbanDetailCloseReturnsFocus(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "focus", ProjectID: "p1", Title: "焦点任务", Status: "todo", Column: "todo"}}})
	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("焦点任务") })
	if err := tt.Click("kanban-task-card-focus"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-detail-close"); return ok })
	if err := tt.Click("kanban-task-detail-close"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.Focused("kanban-task-card-focus") })
}

func TestKanbanDetailNoApproveOrReviewThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "review", ProjectID: "p1", Title: "无批准权限", Status: "review", Column: "review", CanReview: true}}})
	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("无批准权限") })
	if err := tt.Click("kanban-task-card-review"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-detail-accept"); return ok })
	_ = tt.Click("kanban-task-detail-accept")
	tt.Frame()
	if len(fake.ReviewTaskCalls()) != 0 {
		t.Fatal("没有批准权限时不应提交")
	}
	if err := tt.Click("kanban-detail-tab-session"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("评审会话") {
		t.Fatal("没有评审会话 ID 时不应显示入口")
	}
}

func TestKanbanDetailMoreMenuAndDeleteConfirmation(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "menu", ProjectID: "p1", Title: "菜单任务", Status: "done", Column: "done", CanMove: true}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	tt, s := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	s.openTaskDetail(task)
	waitFor(t, tt, func() bool { _, ok := tt.Find("任务更多操作"); return ok })
	if err := tt.Click("任务更多操作"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-detail-delete"); return ok })
	if _, ok := tt.Find("kanban-detail-move-todo"); !ok {
		t.Fatal("应提供重新打开")
	}
	if _, ok := tt.Find("kanban-detail-move-review"); ok {
		t.Fatal("不允许的动作不应出现")
	}
	if err := tt.Click("kanban-detail-delete"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-detail-delete-confirm"); return ok })
	if err := tt.Click("kanban-detail-delete-cancel"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-detail-delete-confirm"); return !ok })
}

func TestKanbanDetailCloseMissingCard(t *testing.T) {
	tt, s := newWorkspaceTester(t, NewFakeClient(), "tasks")
	s.openTaskDetail(KanbanTask{ID: "deleted"})
	waitFor(t, tt, func() bool { return tt.HasText("任务已不存在") })
	if err := tt.Click("kanban-task-detail-close"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}
