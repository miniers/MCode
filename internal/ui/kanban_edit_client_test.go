package mcodeui

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// kanbanEditCall 记录看板编辑接口测试替身收到的一次调用。
type kanbanEditCall struct {
	TaskID   string
	Revision int
}

// kanbanEditFakeClient 在 FakeClient 之上补上看板编辑接口，记录调用参数并可按需返回错误。
type kanbanEditFakeClient struct {
	*FakeClient

	mu          sync.Mutex
	deleteCalls []kanbanEditCall
	nudgeCalls  []kanbanEditCall
	deleteErr   error
	nudgeErr    error
}

func newKanbanEditFakeClient() *kanbanEditFakeClient {
	return &kanbanEditFakeClient{FakeClient: NewFakeClient()}
}

// DeleteTask 记录删除调用；deleteErr 非空时原样返回。
func (f *kanbanEditFakeClient) DeleteTask(ctx context.Context, taskID string, revision int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, kanbanEditCall{TaskID: taskID, Revision: revision})
	return f.deleteErr
}

// NudgeTask 记录提醒调用；nudgeErr 非空时原样返回，否则返回修订号加一的任务。
func (f *kanbanEditFakeClient) NudgeTask(ctx context.Context, taskID string, revision int) (KanbanTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nudgeCalls = append(f.nudgeCalls, kanbanEditCall{TaskID: taskID, Revision: revision})
	if f.nudgeErr != nil {
		return KanbanTask{}, f.nudgeErr
	}
	return KanbanTask{ID: taskID, Revision: revision + 1}, nil
}

// nudgeCallSnapshot 返回已记录的提醒调用副本。
func (f *kanbanEditFakeClient) nudgeCallSnapshot() []kanbanEditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kanbanEditCall(nil), f.nudgeCalls...)
}

// deleteCallSnapshot 返回已记录的删除调用副本。
func (f *kanbanEditFakeClient) deleteCallSnapshot() []kanbanEditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kanbanEditCall(nil), f.deleteCalls...)
}

// TestKanbanNudgeTaskAgentCallsClient：救援菜单里的「提醒 Agent」应把任务 ID 与修订号
// 原样交给客户端的 NudgeTask，成功后提示「已提醒 Agent」。
func TestKanbanNudgeTaskAgentCallsClient(t *testing.T) {
	fake := newKanbanEditFakeClient()
	task := KanbanTask{
		ID:        "task-nudge",
		ProjectID: "p1",
		Title:     "提醒任务",
		Status:    "in_progress",
		Column:    "in_progress",
		Revision:  4,
	}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })

	if err := tt.Click("kanban-task-" + task.ID + "-more"); err != nil {
		t.Fatalf("click rescue menu failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("提醒 Agent") })
	if err := tt.Click("kanban-task-rescue-nudge-" + task.ID); err != nil {
		t.Fatalf("click nudge failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return len(fake.nudgeCallSnapshot()) == 1 && tt.HasText("已提醒 Agent")
	})
	calls := fake.nudgeCallSnapshot()
	if calls[0].TaskID != task.ID || calls[0].Revision != task.Revision {
		t.Fatalf("NudgeTask 调用参数 = %+v, want {TaskID:%q Revision:%d}", calls[0], task.ID, task.Revision)
	}
	if len(fake.deleteCallSnapshot()) != 0 {
		t.Fatalf("提醒 Agent 不应触发删除调用: %+v", fake.deleteCallSnapshot())
	}
}

// TestKanbanDeleteConfirmCallsClient：救援菜单的删除二次确认应调用 DeleteTask，
// 成功后刷新看板并提示「已删除任务」。
func TestKanbanDeleteConfirmCallsClient(t *testing.T) {
	fake := newKanbanEditFakeClient()
	task := KanbanTask{
		ID:        "task-delete",
		ProjectID: "p1",
		Title:     "待删除任务",
		Status:    "in_progress",
		Column:    "in_progress",
		Revision:  7,
	}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })

	if err := tt.Click("kanban-task-" + task.ID + "-more"); err != nil {
		t.Fatalf("click rescue menu failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除") })
	for _, action := range []string{"nudge", "interrupt", "delete"} {
		if _, ok := tt.Find("kanban-task-rescue-" + action + "-" + task.ID); !ok {
			t.Fatalf("进行中任务缺少 %s 按钮", action)
		}
	}
	if err := tt.Click("kanban-task-rescue-delete-" + task.ID); err != nil {
		t.Fatalf("click delete failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认删除该任务？此操作不可撤销") })
	callsBefore := fake.KanbanBoardCalls()
	if err := tt.Click("kanban-task-delete-confirm-yes-" + task.ID); err != nil {
		t.Fatalf("click delete confirm failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return len(fake.deleteCallSnapshot()) == 1 && tt.HasText("已删除任务") &&
			fake.KanbanBoardCalls() > callsBefore
	})
	calls := fake.deleteCallSnapshot()
	if calls[0].TaskID != task.ID || calls[0].Revision != task.Revision {
		t.Fatalf("DeleteTask 调用参数 = %+v, want {TaskID:%q Revision:%d}", calls[0], task.ID, task.Revision)
	}
}

func TestKanbanDoneTaskDeleteConfirmCallsClient(t *testing.T) {
	fake := newKanbanEditFakeClient()
	task := KanbanTask{
		ID:        "task-done-delete",
		ProjectID: "p1",
		Title:     "已完成任务",
		Status:    "done",
		Column:    "done",
		Revision:  8,
	}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	if _, ok := tt.Find("kanban-task-" + task.ID + "-more"); !ok {
		t.Fatal("已完成任务缺少更多按钮")
	}
	if err := tt.Click("kanban-task-" + task.ID + "-more"); err != nil {
		t.Fatalf("click menu failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-task-rescue-delete-" + task.ID)
		return ok
	})
	for _, action := range []string{"nudge", "interrupt"} {
		if _, ok := tt.Find("kanban-task-rescue-" + action + "-" + task.ID); ok {
			t.Fatalf("已完成任务不应显示 %s 按钮", action)
		}
	}
	if err := tt.Click("kanban-task-rescue-delete-" + task.ID); err != nil {
		t.Fatalf("click delete failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认删除该任务？此操作不可撤销") })
	if err := tt.Click("kanban-task-delete-confirm-yes-" + task.ID); err != nil {
		t.Fatalf("click delete confirm failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.deleteCallSnapshot()) == 1 })
	calls := fake.deleteCallSnapshot()
	if calls[0].TaskID != task.ID || calls[0].Revision != task.Revision {
		t.Fatalf("DeleteTask 调用参数 = %+v, want {TaskID:%q Revision:%d}", calls[0], task.ID, task.Revision)
	}
}

// TestKanbanDeleteFailureToast：删除失败时保留二次确认并提示「删除任务失败：」加错误信息。
func TestKanbanDeleteFailureToast(t *testing.T) {
	fake := newKanbanEditFakeClient()
	fake.deleteErr = errors.New("看板暂时不可用")
	task := KanbanTask{
		ID:        "task-delete-fail",
		ProjectID: "p1",
		Title:     "删除失败任务",
		Status:    "in_progress",
		Column:    "in_progress",
		Revision:  2,
	}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })

	if err := tt.Click("kanban-task-" + task.ID + "-more"); err != nil {
		t.Fatalf("click rescue menu failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除") })
	if err := tt.Click("kanban-task-rescue-delete-" + task.ID); err != nil {
		t.Fatalf("click delete failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认删除该任务？此操作不可撤销") })
	if err := tt.Click("kanban-task-delete-confirm-yes-" + task.ID); err != nil {
		t.Fatalf("click delete confirm failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return len(fake.deleteCallSnapshot()) == 1 && tt.HasText("删除任务失败：看板暂时不可用")
	})
	if tt.HasText("已删除任务") {
		t.Fatal("删除失败不应提示「已删除任务」")
	}
}
