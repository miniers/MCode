package mcodeui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type kanbanBatchTestClient struct {
	*FakeClient
	mu      sync.Mutex
	calls   []string
	move    func(context.Context, string) error
	deleted []string
}

func (c *kanbanBatchTestClient) MoveTask(ctx context.Context, id, status string, revision int) (KanbanTask, error) {
	c.mu.Lock()
	c.calls = append(c.calls, id)
	c.mu.Unlock()
	if c.move != nil {
		if err := c.move(ctx, id); err != nil {
			return KanbanTask{}, err
		}
	}
	return KanbanTask{ID: id, Status: status}, nil
}
func (c *kanbanBatchTestClient) DeleteTask(_ context.Context, id string, _ int) error {
	c.mu.Lock()
	c.deleted = append(c.deleted, id)
	c.mu.Unlock()
	return nil
}
func (c *kanbanBatchTestClient) NudgeTask(_ context.Context, id string, revision int) (KanbanTask, error) {
	return KanbanTask{ID: id, Revision: revision}, nil
}
func batchTestTask(id, status string) KanbanTask {
	return KanbanTask{ID: id, Title: "任务" + id, Status: status, CanMove: true, Revision: 1}
}
func awaitBatch(t *testing.T, s *chatState) *kanbanBatchState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		kanbanBatchMu.Lock()
		st := kanbanBatchStates[s]
		done := st != nil && !st.running
		kanbanBatchMu.Unlock()
		if done {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("batch did not finish")
	return nil
}
func TestKanbanBatchEligibility(t *testing.T) {
	client := &kanbanBatchTestClient{FakeClient: NewFakeClient()}
	s := &chatState{client: client}
	ks := getKanbanState(s)
	ks.taskBusy = map[string]bool{"busy": true}
	inMove, del := kanbanBatchEligible(s, []KanbanTask{batchTestTask("run", "in_progress"), batchTestTask("busy", "review"), batchTestTask("ok", "review")})
	if len(inMove) != 2 || len(del) != 1 || del[0].ID != "ok" {
		t.Fatalf("move=%v delete=%v", inMove, del)
	}
	if _, ok := kanbanActions(KanbanTask{Status: "done", CanMove: true}, false).targets["todo"]; !ok {
		t.Fatal("expected done task to allow todo")
	}
}
func TestKanbanBatchPartialFailure(t *testing.T) {
	client := &kanbanBatchTestClient{FakeClient: NewFakeClient(), move: func(_ context.Context, id string) error {
		if id == "bad" {
			return errors.New("拒绝")
		}
		return nil
	}}
	s := &chatState{client: client}
	for _, task := range []KanbanTask{batchTestTask("bad", "review"), batchTestTask("ok", "review")} {
		kanbanKeyboardMu.Lock()
		keyboard := kanbanKeyboardStates[s]
		if keyboard == nil {
			keyboard = &kanbanKeyboardState{selected: map[string]bool{}}
			kanbanKeyboardStates[s] = keyboard
		}
		keyboard.selected[task.ID] = true
		keyboard.tasks = append(keyboard.tasks, task)
		kanbanKeyboardMu.Unlock()
	}
	kanbanBatchRun(s, "todo", []KanbanTask{batchTestTask("bad", "review"), batchTestTask("ok", "review")})
	st := awaitBatch(t, s)
	if len(st.failures) != 1 || st.failures[0].Title != "任务bad" || st.failures[0].Reason != "拒绝" {
		t.Fatalf("failures=%+v", st.failures)
	}
	if !kanbanKeyboardSelected(s, "bad") || kanbanKeyboardSelected(s, "ok") {
		t.Fatal("selection did not retain only failed task")
	}
}
func TestKanbanBatchNoDuplicateRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := &kanbanBatchTestClient{FakeClient: NewFakeClient(), move: func(context.Context, string) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	}}
	s := &chatState{client: client}
	task := batchTestTask("one", "review")
	kanbanBatchRun(s, "todo", []KanbanTask{task})
	<-started
	kanbanBatchRun(s, "todo", []KanbanTask{task})
	close(release)
	awaitBatch(t, s)
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 1 {
		t.Fatalf("calls=%v", client.calls)
	}
}
func TestKanbanBatchCancelStopsRequests(t *testing.T) {
	started := make(chan struct{})
	client := &kanbanBatchTestClient{FakeClient: NewFakeClient(), move: func(ctx context.Context, _ string) error { close(started); <-ctx.Done(); return ctx.Err() }}
	s := &chatState{client: client}
	kanbanBatchRun(s, "todo", []KanbanTask{batchTestTask("one", "review"), batchTestTask("two", "review")})
	<-started
	kanbanBatchCancel(s)
	time.Sleep(20 * time.Millisecond)
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 1 {
		t.Fatalf("calls=%v", client.calls)
	}
}
func TestKanbanBatchDeleteNeedsConfirm(t *testing.T) {
	client := &kanbanBatchTestClient{FakeClient: NewFakeClient()}
	s := &chatState{client: client}
	task := batchTestTask("one", "review")
	keyboard := &kanbanKeyboardState{tasks: []KanbanTask{task}, selected: map[string]bool{"one": true}}
	kanbanKeyboardMu.Lock()
	kanbanKeyboardStates[s] = keyboard
	kanbanKeyboardMu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderKanbanSelectionBar(c, s, getKanbanState(s)) }, 900, 400)
	if err := tt.Click("kanban-selection-delete"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("删除 1 项任务？") {
		t.Fatal("delete confirmation not shown")
	}
	if err := tt.Click("confirm-kanban-batch-delete-cancel"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	deleted := len(client.deleted)
	client.mu.Unlock()
	if deleted != 0 {
		t.Fatal("cancel issued a delete")
	}
	if err := tt.Click("kanban-selection-delete"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("confirm-kanban-batch-delete-confirm"); err != nil {
		t.Fatal(err)
	}
	awaitBatch(t, s)
	client.mu.Lock()
	deleted = len(client.deleted)
	client.mu.Unlock()
	if deleted != 1 {
		t.Fatalf("confirmed deletion count=%d", deleted)
	}
	kanbanBatchCancel(s)
	kanbanKeyboardMu.Lock()
	delete(kanbanKeyboardStates, s)
	kanbanKeyboardMu.Unlock()
	if len(client.calls) != 0 {
		t.Fatalf("delete unexpectedly moved tasks: %v", client.calls)
	}
}
func TestKanbanBatchBarNoHelperText(t *testing.T) {
	client := &kanbanBatchTestClient{FakeClient: NewFakeClient()}
	s := &chatState{client: client}
	task := batchTestTask("one", "review")
	kanbanKeyboardMu.Lock()
	kanbanKeyboardStates[s] = &kanbanKeyboardState{tasks: []KanbanTask{task}, selected: map[string]bool{"one": true}}
	kanbanKeyboardMu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderKanbanSelectionBar(c, s, getKanbanState(s)) }, 900, 400)
	for _, text := range []string{"已选 1 项", "移回待办 1/1", "删除 1/1", "取消选择"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q", text)
		}
	}
	for _, text := range []string{"说明", "点击", "批量", "选中后"} {
		if tt.HasText(text) {
			t.Fatalf("unexpected helper text %q", text)
		}
	}
	kanbanBatchCancel(s)
	kanbanKeyboardMu.Lock()
	delete(kanbanKeyboardStates, s)
	kanbanKeyboardMu.Unlock()
}
