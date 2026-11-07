package mcodeui

import (
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestWindowOptions(t *testing.T) {
	s := &chatState{}

	WithInitialThread("thread-123")(s)
	if s.initialThreadID != "thread-123" {
		t.Errorf("expected initialThreadID thread-123, got %q", s.initialThreadID)
	}

	var dummyWin *mygo.Window
	WithWindow(func() *mygo.Window { return dummyWin })(s)
	if s.window == nil || s.window() != dummyWin {
		t.Errorf("expected s.window() == dummyWin")
	}

	var openedID string
	WithOpenThreadWindow(func(id string) { openedID = id })(s)
	if s.openThreadWindow == nil {
		t.Fatal("expected s.openThreadWindow to be non-nil")
	}
	s.openThreadWindow("test-id")
	if openedID != "test-id" {
		t.Errorf("expected openedID test-id, got %q", openedID)
	}
}

func TestRevealThreadRoutesFromTasksWindowToMain(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.tasksWindowView = "tasks"
	var revealedID string
	WithRevealThreadInMain(func(id string) { revealedID = id })(s)

	s.revealThread("thread-123")

	if revealedID != "thread-123" {
		t.Fatalf("expected thread to be routed to main window, got %q", revealedID)
	}
}

func TestRevealThreadFallsBackToLocalState(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
	}, 100, 100)

	s.revealThread("thread-456")

	if s.selectedThreadID != "thread-456" {
		t.Fatalf("expected local selected thread thread-456, got %q", s.selectedThreadID)
	}
}

func TestBuildRoot(t *testing.T) {
	client := NewFakeClient()
	s, view := buildRoot(client, WithInitialThread("init-tid"))
	if s == nil || view == nil {
		t.Fatal("expected non-nil state and view from buildRoot")
	}
	if s.initialThreadID != "init-tid" {
		t.Errorf("expected initialThreadID init-tid, got %q", s.initialThreadID)
	}
}

func TestWindows_SharedClientIndependentStreams(t *testing.T) {
	client := NewFakeClient()
	client.SetProjectsAndThreads([]Project{{ID: "p"}}, map[string][]Thread{
		"p": {{ID: "one", ProjectID: "p"}, {ID: "two", ProjectID: "p"}},
	})
	one, viewOne := buildRoot(client, WithInitialThread("one"))
	two, viewTwo := buildRoot(client, WithInitialThread("two"))
	t.Cleanup(one.close)
	t.Cleanup(two.close)
	ttOne := ui.NewTester(viewOne, 1200, 800)
	ttTwo := ui.NewTester(viewTwo, 1200, 800)
	ready := func(s *chatState, id string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == id && s.watchStop != nil
	}
	waitFor(t, ttOne, func() bool { return ready(one, "one") })
	waitFor(t, ttTwo, func() bool { return ready(two, "two") })
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.TriggerUpdate(ThreadUpdate{
				ThreadID: id, Thread: Thread{ID: id, ProjectID: "p"},
				Messages: []Message{{ID: id, Text: "stream-" + id}},
			})
		}()
	}
	wg.Wait()
	for _, state := range []*chatState{one, two} {
		state.mu.Lock()
		if len(state.messages) != 1 || state.messages[0].Text != "stream-"+state.selectedThreadID {
			t.Errorf("messages crossed windows: %+v", state.messages)
		}
		state.mu.Unlock()
	}
	one.close()
	paletteMu.Lock()
	_, retained := paletteStates[one]
	paletteMu.Unlock()
	if retained {
		t.Fatal("closed window retained in UI state registry")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.watchers["one"][0].stopped || client.watchers["two"][0].stopped {
		t.Fatal("closing one window must stop only its subscription")
	}
}

type delayedWatchClient struct {
	*FakeClient
	started, release, stopped chan struct{}
}

func (c *delayedWatchClient) Watch(string, func(ThreadUpdate), func([]Approval)) func() {
	close(c.started)
	<-c.release
	return func() { close(c.stopped) }
}

func TestWindowClose_StopsLateSubscription(t *testing.T) {
	client := &delayedWatchClient{NewFakeClient(), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	s := newChatState(client)
	s.selectThread("one")
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not start")
	}
	s.close()
	close(client.release)
	select {
	case <-client.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("subscription installed after close was not stopped")
	}
}

// 关窗时要清掉 aiWorktreeStates：自建 worktree 识别缓存按 chatState 存包级 map，
// 只增不删会在每次关窗后泄漏一份会话状态及其识别结果。
func TestWindowClose_ReleasesAIWorktreeState(t *testing.T) {
	s := newChatState(NewFakeClient())
	if getAIWorktreeState(s) == nil {
		t.Fatal("expected aiWorktreeState to be created")
	}
	s.close()
	aiWorktreeMu.Lock()
	_, retained := aiWorktreeStates[s]
	aiWorktreeMu.Unlock()
	if retained {
		t.Fatal("closed window retained in aiWorktreeStates")
	}
}
