package mcodeui

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func TestKanbanActionsTargets(t *testing.T) {
	for _, tc := range []struct {
		status, column string
		want           []string
	}{
		{"todo", "todo", []string{"done", "in_progress", "review"}},
		{"in_progress", "in_progress", []string{"done", "review", "todo"}},
		{"needs_approval", "needs_you", []string{"done", "review", "todo"}},
		{"review", "review", []string{"done", "todo"}},
		{"waiting", "needs_you", []string{"todo"}},
		{"failed", "needs_you", []string{"todo"}},
		{"cancelled", "done", []string{"todo"}},
		{"done", "done", []string{"todo"}},
		{"rejected", "needs_you", []string{"todo"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			task := KanbanTask{Status: tc.status, Column: tc.column, CanMove: true, CanStart: tc.status == "todo", AttentionFlags: []string{"failed"}}
			actions := kanbanActions(task, false)
			var got, menu []string
			for target := range actions.targets {
				got = append(got, target)
			}
			for _, option := range actions.menu {
				menu = append(menu, option.status)
			}
			sort.Strings(got)
			sort.Strings(menu)
			if !reflect.DeepEqual(got, tc.want) || !reflect.DeepEqual(menu, got) {
				t.Fatalf("targets=%v menu=%v want=%v", got, menu, tc.want)
			}
			if _, ok := actions.targets["needs_you"]; ok {
				t.Fatal("needs_you must be read-only")
			}
			busy := kanbanActions(task, true)
			if busy.primary != "" || len(busy.targets) != 0 || len(busy.menu) != 0 {
				t.Fatalf("busy actions: %+v", busy)
			}
		})
	}
}

func TestKanbanDragReviewOpensDetail(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "review-drag", ProjectID: "p1", Title: "拖动审阅", Status: "review", Column: "review", CanMove: true, CanReview: true, Revision: 3}}})
	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("拖动审阅") })
	from, ok := tt.Find("kanban-task-card-review-drag")
	if !ok {
		t.Fatal("task card missing")
	}
	to, ok := tt.Find("kanban-column-done")
	if !ok {
		t.Fatal("done column missing")
	}
	tt.Press(from.X+5, from.Y+5)
	for step := 1; step <= 4; step++ {
		fraction := float32(step) / 4
		tt.Move(from.X+5+(to.X+20-from.X-5)*fraction, from.Y+5+(to.Y+20-from.Y-5)*fraction)
	}
	tt.Release(to.X+20, to.Y+20)
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-detail"); return ok })
	if len(fake.MoveTaskCalls()) != 0 || len(fake.ReviewTaskCalls()) != 0 {
		t.Fatal("drag must only open the review")
	}
}

func TestKanbanTargetPreservesStaleRevision(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "stale", ProjectID: "p1", Title: "过期任务", Status: "todo", Column: "todo", CanMove: true, Revision: 3}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	fake.SetMoveTaskError(fmt.Errorf("%w", ErrRevisionConflict))
	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	from, ok := tt.Find("kanban-task-card-stale")
	if !ok {
		t.Fatal("task card missing")
	}
	to, ok := tt.Find("kanban-column-review")
	if !ok {
		t.Fatal("review column missing")
	}
	tt.Press(from.X+5, from.Y+5)
	for step := 1; step <= 4; step++ {
		fraction := float32(step) / 4
		tt.Move(from.X+5+(to.X+20-from.X-5)*fraction, from.Y+5+(to.Y+20-from.Y-5)*fraction)
	}
	tt.Release(to.X+20, to.Y+20)
	waitFor(t, tt, func() bool { return len(fake.MoveTaskCalls()) == 1 })
	if call := fake.MoveTaskCalls()[0]; call.Revision != 3 {
		t.Fatalf("revision=%d", call.Revision)
	}
}

func TestKanbanBlockedDependencyOpensTask(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{
		{ID: "blocked", ProjectID: "p1", Title: "阻塞任务", Status: "todo", Column: "todo", CanStart: true, Blocked: true, DependsOn: []string{"dependency"}},
		{ID: "dependency", ProjectID: "p1", Title: "前置工作", Status: "todo", Column: "todo"},
	}})
	tt, state := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-card-blocked"); return ok })
	if err := tt.Click("kanban-task-card-blocked"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("前置工作"); return ok })
	if err := tt.Click("前置工作"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		ks := getKanbanState(state)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		return ks.detailTaskID == "dependency"
	})
}

func TestKanbanActionsReviewAndBusy(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "review-1", ProjectID: "p1", Title: "审阅任务", Status: "review", Column: "review", CanMove: true, CanReview: true, Revision: 7}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	tt, state := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	ks := getKanbanState(state)
	if !beginKanbanTask(ks, task.ID) {
		t.Fatal("could not mark task busy")
	}
	state.performKanbanTarget(task, "done")
	kanbanMu.Lock()
	detail := ks.detailTaskID
	kanbanMu.Unlock()
	if detail != "" || len(fake.MoveTaskCalls()) != 0 {
		t.Fatal("busy task performed an action")
	}
	endKanbanTask(ks, task.ID)
	state.performKanbanTarget(task, "done")
	kanbanMu.Lock()
	detail = ks.detailTaskID
	kanbanMu.Unlock()
	if detail != task.ID || len(fake.MoveTaskCalls()) != 0 || len(fake.ReviewTaskCalls()) != 0 {
		t.Fatalf("detail=%q moves=%v reviews=%v", detail, fake.MoveTaskCalls(), fake.ReviewTaskCalls())
	}
}

type kanbanScopeClient struct {
	*FakeClient
	scopes chan string
}

func (f *kanbanScopeClient) KanbanBoard(ctx context.Context, projectID string) (KanbanBoard, error) {
	select {
	case f.scopes <- projectID:
	default:
	}
	return f.FakeClient.KanbanBoard(ctx, projectID)
}

func TestKanbanGoalRefreshesCurrentScope(t *testing.T) {
	fake := &kanbanScopeClient{FakeClient: NewFakeClient(), scopes: make(chan string, 32)}
	fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{{ID: "goal-1", ProjectID: "p1"}}})
	tt, state := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool {
		ks := getKanbanState(state)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		return ks.initialized && !ks.loading
	})
	state.setKanbanScope("", true)
	waitFor(t, tt, func() bool {
		ks := getKanbanState(state)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		return !ks.loading && ks.scopeID == ""
	})
	for len(fake.scopes) > 0 {
		<-fake.scopes
	}
	state.startGoal("goal-1", 1)
	waitFor(t, tt, func() bool { return len(fake.scopes) > 0 })
	if scope := <-fake.scopes; scope != "" {
		t.Fatalf("refresh scope=%q, want all projects", scope)
	}
	waitFor(t, tt, func() bool {
		ks := getKanbanState(state)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		return !ks.loading
	})
	for len(fake.scopes) > 0 {
		<-fake.scopes
	}
	state.moveTask("task-1", "review", 2)
	waitFor(t, tt, func() bool { return len(fake.scopes) > 0 })
	if scope := <-fake.scopes; scope != "" {
		t.Fatalf("task refresh scope=%q, want all projects", scope)
	}
}
