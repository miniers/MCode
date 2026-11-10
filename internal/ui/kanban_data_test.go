package mcodeui

import (
	"context"
	"testing"
	"time"
)

type boardRequest struct {
	project string
	reply   chan KanbanBoard
}
type controlledKanbanClient struct {
	*FakeClient
	boards chan boardRequest
	moves  chan chan struct{}
}

func (c *controlledKanbanClient) KanbanBoard(ctx context.Context, project string) (KanbanBoard, error) {
	if c.boards == nil {
		return c.FakeClient.KanbanBoard(ctx, project)
	}
	reply := make(chan KanbanBoard, 1)
	c.boards <- boardRequest{project, reply}
	return <-reply, nil
}
func (c *controlledKanbanClient) MoveTask(ctx context.Context, id, status string, revision int) (KanbanTask, error) {
	if c.moves != nil {
		release := make(chan struct{})
		c.moves <- release
		<-release
	}
	return c.FakeClient.MoveTask(ctx, id, status, revision)
}
func awaitKanban(t *testing.T, s *chatState, pred func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		s.applyWorkspaceUpdates()
		if pred() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("看板状态未及时更新")
}
func nextBoard(t *testing.T, ch <-chan boardRequest) boardRequest {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("未收到看板请求")
		return boardRequest{}
	}
}
func TestKanbanScope_AllProjects(t *testing.T) {
	f := NewFakeClient()
	f.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "a", ProjectID: "p1"}, {ID: "b", ProjectID: "p2"}, {ID: "c"}}})
	s := newChatState(f)
	defer stopKanbanWatch(s)
	s.syncKanbanProject("p1")
	s.setKanbanScope("", true)
	ks := getKanbanState(s)
	awaitKanban(t, s, func() bool { kanbanMu.Lock(); defer kanbanMu.Unlock(); return len(ks.board.Tasks) == 3 })
	seen := map[string]bool{}
	for _, task := range ks.board.Tasks {
		if seen[task.ID] {
			t.Fatal("任务重复")
		}
		seen[task.ID] = true
	}
}
func TestKanbanScope_NoProjectDefaultsToAll(t *testing.T) {
	f := NewFakeClient()
	f.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "no-project"}}})
	s := newChatState(f)
	defer stopKanbanWatch(s)
	s.syncKanbanProject("")
	ks := getKanbanState(s)
	awaitKanban(t, s, func() bool { kanbanMu.Lock(); defer kanbanMu.Unlock(); return ks.overview && len(ks.board.Tasks) == 1 })
}
func TestKanbanScope_LateResponseIgnored(t *testing.T) {
	c := &controlledKanbanClient{FakeClient: NewFakeClient(), boards: make(chan boardRequest, 4)}
	s := newChatState(c)
	defer stopKanbanWatch(s)
	s.syncKanbanProject("p1")
	old := nextBoard(t, c.boards)
	s.syncKanbanProject("p2")
	current := nextBoard(t, c.boards)
	current.reply <- KanbanBoard{Tasks: []KanbanTask{{ID: "new"}}}
	ks := getKanbanState(s)
	awaitKanban(t, s, func() bool { return len(ks.board.Tasks) == 1 })
	old.reply <- KanbanBoard{Tasks: []KanbanTask{{ID: "old"}}}
	deadline := time.Now().Add(3 * time.Second)
	for {
		workspaceMu.Lock()
		queued := len(workspaceStates[s].updates) > 0
		workspaceMu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("旧请求未返回")
		}
		time.Sleep(time.Millisecond)
	}
	s.applyWorkspaceUpdates()
	if ks.board.Tasks[0].ID != "new" {
		t.Fatal("旧请求覆盖新范围")
	}
}
func TestKanbanScope_WatchClosed(t *testing.T) {
	f := NewFakeClient()
	s := newChatState(f)
	s.syncKanbanProject("p1")
	stopKanbanWatch(s)
	f.mu.Lock()
	count := len(f.kanbanWatchers)
	f.mu.Unlock()
	if count != 0 {
		t.Fatalf("仍有 %d 个订阅", count)
	}
}
func TestKanbanScope_WatchRefreshCoalesced(t *testing.T) {
	c := &controlledKanbanClient{FakeClient: NewFakeClient(), boards: make(chan boardRequest, 4)}
	s := newChatState(c)
	defer stopKanbanWatch(s)
	s.syncKanbanProject("p1")
	first := nextBoard(t, c.boards)
	for i := 0; i < 10; i++ {
		c.TriggerKanbanChange()
	}
	select {
	case <-c.boards:
		t.Fatal("同时启动多个请求")
	default:
	}
	first.reply <- KanbanBoard{}
	ks := getKanbanState(s)
	awaitKanban(t, s, func() bool { return len(c.boards) > 0 })
	second := nextBoard(t, c.boards)
	second.reply <- KanbanBoard{}
	awaitKanban(t, s, func() bool { kanbanMu.Lock(); defer kanbanMu.Unlock(); return !ks.loading })
	select {
	case <-c.boards:
		t.Fatal("触发了多余刷新")
	default:
	}
}
func TestKanbanScope_ProjectSwitchKeepsDraft(t *testing.T) {
	f := NewFakeClient()
	s := newChatState(f)
	defer stopKanbanWatch(s)
	s.syncKanbanProject("p1")
	ks := getKanbanState(s)
	ks.newTaskTitle = "draft"
	ks.newTaskProjectID = "p1"
	ks.newGoalPrompt = "goal"
	s.syncKanbanProject("p2")
	if ks.newTaskTitle != "draft" || ks.newTaskProjectID != "p1" || ks.newGoalPrompt != "goal" {
		t.Fatal("切项目丢失草稿")
	}
}
func TestKanbanTaskBusy_DedupesInflight(t *testing.T) {
	c := &controlledKanbanClient{FakeClient: NewFakeClient(), moves: make(chan chan struct{}, 2)}
	s := newChatState(c)
	ks := getKanbanState(s)
	s.moveTask("t", "done", 1)
	var release chan struct{}
	select {
	case release = <-c.moves:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到移动请求")
	}
	if !kanbanTaskBusy(ks, "t") {
		t.Fatal("未标记忙碌")
	}
	s.startTask("t", 1)
	s.moveTask("t", "done", 1)
	select {
	case <-c.moves:
		t.Fatal("重复发送请求")
	default:
	}
	close(release)
	awaitKanban(t, s, func() bool { return !kanbanTaskBusy(ks, "t") })
	if len(c.StartTaskCalls()) != 0 || len(c.MoveTaskCalls()) != 1 {
		t.Fatal("重复发送写入")
	}
}
