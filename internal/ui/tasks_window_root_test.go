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
