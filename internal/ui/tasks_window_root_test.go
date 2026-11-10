package mcodeui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type tasksRootCountingClient struct {
	*FakeClient
	projects, threads, queue, swarm, foreground atomic.Int32
}

func (c *tasksRootCountingClient) Projects(ctx context.Context) ([]Project, error) {
	c.projects.Add(1)
	return []Project{{ID: "p1", Name: "项目"}}, nil
}
func (c *tasksRootCountingClient) Threads(ctx context.Context, id string) ([]Thread, error) {
	c.threads.Add(1)
	return c.FakeClient.Threads(ctx, id)
}
func (c *tasksRootCountingClient) WatchQueue(fn func(string, []QueuedFollowUp)) func() {
	c.queue.Add(1)
	return c.FakeClient.WatchQueue(fn)
}
func (c *tasksRootCountingClient) WatchSwarm(fn func(string)) func() {
	c.swarm.Add(1)
	return c.FakeClient.WatchSwarm(fn)
}
func (c *tasksRootCountingClient) SetForegroundThread(string) { c.foreground.Add(1) }

func TestTasksRootDoesNotInitializeChat(t *testing.T) {
	client := &tasksRootCountingClient{FakeClient: NewFakeClient()}
	s, view := buildRoot(client, WithTasksWindowMode("tasks"))
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		s.mu.Lock()
		loaded := len(s.projects) == 1
		s.mu.Unlock()
		if loaded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	loaded := len(s.projects) == 1
	s.mu.Unlock()
	if !loaded || client.projects.Load() != 1 {
		t.Fatal("任务根未加载项目")
	}
	if client.threads.Load() != 0 || client.queue.Load() != 0 || client.swarm.Load() != 0 || client.foreground.Load() != 0 {
		t.Fatal("任务根启动了聊天初始化")
	}
	s.close()
}

func TestTasksAutomationLeaveProtectsDestinationAndScope(t *testing.T) {
	s, _ := buildTasksRoot(nil, WithTasksWindowMode("automations"))
	ks := getKanbanState(s)
	ks.initialized = true
	ks.scopeID = "p1"
	as := getAutomationState(s)
	as.editorOpen = true
	as.name = "修改"
	s.setTasksWindowView("tasks")
	if ks.destination != "automations" || !as.confirmDiscard || as.pendingLeave == nil {
		t.Fatal("切换目的时未保护未保存的修改")
	}
	handleAutomationEscape(s)
	s.setKanbanScope("p2", false)
	if ks.scopeID != "p1" || !as.confirmDiscard || as.pendingLeave == nil {
		t.Fatal("切换项目时未保护未保存的修改")
	}
	s.close()
}

func TestTasksAttentionCountDeduplicatesAndTracksGoals(t *testing.T) {
	board := KanbanBoard{Tasks: []KanbanTask{{ID: "a", Status: "failed"}, {ID: "a", Status: "review"}, {ID: "b", Status: "waiting", WaitKind: "machine"}}, Goals: []KanbanGoal{{ID: "g", Status: "plan_review"}}}
	if got := tasksAttentionCount(board); got != 2 {
		t.Fatalf("待处理计数 = %d", got)
	}
	board.Tasks = board.Tasks[2:]
	board.Goals[0].Status = "running"
	if got := tasksAttentionCount(board); got != 0 {
		t.Fatalf("处理后计数 = %d", got)
	}
}
