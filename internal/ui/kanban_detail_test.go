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
