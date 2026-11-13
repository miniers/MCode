package remote

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

type notifyTestConn struct {
	mu            sync.Mutex
	info          ClientInfo
	closed        bool
	notifications []struct {
		method string
		params any
	}
}

func newNotifyTestConn(deviceID, label string) *notifyTestConn {
	return &notifyTestConn{
		info: ClientInfo{
			DeviceID:      deviceID,
			Label:         label,
			Authenticated: true,
		},
	}
}

func (c *notifyTestConn) Info() ClientInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

func (c *notifyTestConn) MarkAuthenticated(deviceID, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *notifyTestConn) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, struct {
		method string
		params any
	}{method: method, params: params})
	return nil
}

func (c *notifyTestConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func TestStreamHub_OversizedSummaryDisconnects(t *testing.T) {
	testenv.TempHome(t)
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	conn := newNotifyTestConn("device", "client")
	threadID := newUUID()
	hub.Subscribe(conn, threadID)
	hub.Publish(threadID, nil, nil, wire.ThreadSummary{ID: threadID, Title: strings.Repeat("a", maxThreadEventFrameBytes)})
	hub.Flush(threadID)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if !conn.closed || len(conn.notifications) != 0 {
		t.Fatal("oversized resync must disconnect instead of sending an unsafe frame")
	}
}

func TestDispatcherMoveNotifiesReorderedNeighbors(t *testing.T) {
	testenv.TempHome(t)
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	projectID := newUUID()
	first, second := newUUID(), newUUID()
	backend.AddThread(wire.ThreadSummary{ID: first, ProjectID: projectID, Title: "first", UpdatedAt: "2026-10-18T00:00:02Z"}, nil)
	backend.AddThread(wire.ThreadSummary{ID: second, ProjectID: projectID, Title: "second", UpdatedAt: "2026-10-18T00:00:01Z"}, nil)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := newNotifyTestConn("device", "client")
	defer d.RemoveConn(conn)
	request := `{"jsonrpc":"2.0","method":"threads.move","params":{"threadID":"` + second + `","direction":"up"},"id":1}`
	raw := d.Handle(context.Background(), conn, []byte(request))
	var response wire.Response
	if err := json.Unmarshal(raw, &response); err != nil || response.Error != nil {
		t.Fatalf("move failed: %s, err=%v", raw, err)
	}
	notifications := conn.getNotifications()
	if len(notifications) != 1 {
		t.Fatalf("expected one notification, got %d", len(notifications))
	}
	changed := notifications[0].params.(wire.ThreadsChanged)
	if len(changed.Threads) != 2 {
		t.Fatalf("reordered neighbor missing: %+v", changed.Threads)
	}
}

func TestDispatcherSummaryDedupAndAuthentication(t *testing.T) {
	testenv.TempHome(t)
	d := NewDispatcher(&fakeAuth{}, &fakeBackend{})
	conn := newNotifyTestConn("device", "client")
	d.addAuthedConn(conn)
	defer d.RemoveConn(conn)
	thread := wire.ThreadSummary{ID: newUUID(), IsRunning: true, Title: "running"}
	d.broadcastThreadsChanged(wire.ThreadsChanged{Threads: []wire.ThreadSummary{thread}})
	thread.UpdatedAt = "streaming"
	activity := "another token"
	thread.LastActivityText = &activity
	d.broadcastThreadsChanged(wire.ThreadsChanged{Threads: []wire.ThreadSummary{thread}})
	if len(conn.getNotifications()) != 1 {
		t.Fatal("streaming text must not flood thread list notifications")
	}
	conn.mu.Lock()
	conn.info.Authenticated = false
	conn.mu.Unlock()
	thread.Title = "secret"
	d.broadcastThreadsChanged(wire.ThreadsChanged{Threads: []wire.ThreadSummary{thread}})
	if len(conn.getNotifications()) != 1 {
		t.Fatal("unauthenticated connection received thread metadata")
	}
}

func (c *notifyTestConn) getNotifications() []struct {
	method string
	params any
} {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]struct {
		method string
		params any
	}, len(c.notifications))
	copy(out, c.notifications)
	return out
}

// TestStreamHub_ResyncOnOversizedFrame 测试超大帧下发时降级为 resync 事件 (D-REM-063)。
func TestStreamHub_ResyncOnOversizedFrame(t *testing.T) {
	testenv.TempHome(t)
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	conn := newNotifyTestConn("dev-1", "Client")
	threadID := "00000000-0000-0000-0000-000000000001"
	hub.Subscribe(conn, threadID)

	summary := wire.ThreadSummary{ID: threadID, Title: "Test Thread"}

	// 1. 构造超过 512KB 的大帧
	largeText := strings.Repeat("A", 600*1024)
	largeMsg := wire.ThreadMessage{ID: "m-large", Text: largeText}

	hub.Publish(threadID, []wire.ThreadMessage{largeMsg}, nil, summary)
	hub.Flush(threadID)

	notifs := conn.getNotifications()
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifs))
	}

	if notifs[0].method != "threads.event" {
		t.Fatalf("expected method 'threads.event', got %q", notifs[0].method)
	}

	ev, ok := notifs[0].params.(wire.ThreadEvent)
	if !ok {
		t.Fatalf("expected wire.ThreadEvent, got %T", notifs[0].params)
	}

	if ev.Kind != "resync" {
		t.Fatalf("expected kind 'resync' on frame overflow, got %q", ev.Kind)
	}

	frameBytes, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("failed to marshal resync event: %v", err)
	}

	if len(frameBytes) > maxThreadEventFrameBytes {
		t.Fatalf("resync event frame size %d exceeds threshold %d", len(frameBytes), maxThreadEventFrameBytes)
	}

	if ev.Payload.Thread.Title != "Test Thread" {
		t.Fatalf("expected thread summary retained in resync, got %q", ev.Payload.Thread.Title)
	}

	// 2. 正常大小帧照常下发为 projection
	normalMsg := wire.ThreadMessage{ID: "m-normal", Text: "Hello world"}
	hub.Publish(threadID, []wire.ThreadMessage{normalMsg}, nil, summary)
	hub.Flush(threadID)

	notifs = conn.getNotifications()
	if len(notifs) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifs))
	}

	evNormal, ok := notifs[1].params.(wire.ThreadEvent)
	if !ok {
		t.Fatalf("expected wire.ThreadEvent, got %T", notifs[1].params)
	}

	if evNormal.Kind != "projection" {
		t.Fatalf("expected kind 'projection' for normal frame, got %q", evNormal.Kind)
	}
}

func TestStreamHub_FrameLimitIncludesEnvelope(t *testing.T) {
	testenv.TempHome(t)
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	conn := newNotifyTestConn("device", "client")
	threadID := newUUID()
	hub.Subscribe(conn, threadID)
	summary := wire.ThreadSummary{ID: threadID, Title: "boundary"}
	message := wire.ThreadMessage{ID: newUUID()}
	hub.Publish(threadID, []wire.ThreadMessage{message}, nil, summary)
	event := wire.ThreadEvent{
		ThreadID: threadID, RunID: hub.CurrentRunID(threadID), Sequence: 1, Kind: "projection",
		Payload: wire.ThreadEventPayload{Messages: []wire.ThreadMessage{message}, Thread: summary},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	message.Text = strings.Repeat("a", maxThreadEventFrameBytes-len(raw))
	hub.Publish(threadID, []wire.ThreadMessage{message}, nil, summary)
	hub.Flush(threadID)
	notifications := conn.getNotifications()
	if len(notifications) != 1 {
		t.Fatalf("expected one notification, got %d", len(notifications))
	}
	if ev := notifications[0].params.(wire.ThreadEvent); ev.Kind != "resync" {
		t.Fatalf("event fits but envelope exceeds limit: got %q", ev.Kind)
	}
}

func TestThreadEventPayloadContract(t *testing.T) {
	testenv.TempHome(t)
	for _, kind := range []string{"projection", "resync"} {
		t.Run(kind, func(t *testing.T) {
			event := wire.ThreadEvent{Kind: kind}
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Payload map[string]json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"messages", "removedMessageIDs"} {
				value, exists := envelope.Payload[key]
				if kind == "projection" && string(value) != "[]" {
					t.Errorf("projection requires %s: [], got %s", key, value)
				}
				if kind == "resync" && exists {
					t.Errorf("resync forbids %s", key)
				}
			}
		})
	}
}

func TestDispatcher_LocalMutationsAndTurnCompletion(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&controlledTestHarness{})
	projectID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projectID, Path: t.TempDir()})
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := newNotifyTestConn("device", "client")
	d.addAuthedConn(conn)
	defer d.RemoveConn(conn)

	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projectID, ActionID: newUUID()})
	if err != nil {
		t.Fatal(err)
	}
	title := "renamed locally"
	if _, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: created.ThreadID, Title: &title}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: created.ThreadID, Text: "hello", ActionID: newUUID()}); err != nil {
		t.Fatal(err)
	}
	backend.wg.Wait()
	if err := backend.DeleteThread(ctx, created.ThreadID); err != nil {
		t.Fatal(err)
	}
	notifications := conn.getNotifications()
	if len(notifications) < 4 {
		t.Fatalf("expected create, rename, turn completion and deletion, got %d", len(notifications))
	}
	previous := 0
	finished := false
	for _, notification := range notifications {
		if notification.method != "threads.changed" {
			t.Fatalf("unexpected method %s", notification.method)
		}
		changed := notification.params.(wire.ThreadsChanged)
		if changed.Revision <= previous {
			t.Fatalf("revision did not advance: %d <= %d", changed.Revision, previous)
		}
		previous = changed.Revision
		for _, summary := range changed.Threads {
			if summary.Title == title && !summary.IsRunning && changed.Revision > 2 {
				finished = true
			}
		}
	}
	if !finished {
		t.Fatal("missing turn completion notification")
	}
	last := notifications[len(notifications)-1].params.(wire.ThreadsChanged)
	if len(last.RemovedThreadIDs) != 1 || last.RemovedThreadIDs[0] != created.ThreadID {
		t.Fatalf("missing removal: %+v", last)
	}
	d.RemoveConn(conn)
	if d.stopThreadObserver != nil {
		t.Fatal("observer retained after last connection removed")
	}
}

// TestDispatcher_ThreadsChangedBroadcast 测试创建和更新会话后向所有已认证连接广播 threads.changed (D-REM-064)。
func TestDispatcher_ThreadsChangedBroadcast(t *testing.T) {
	testenv.TempHome(t)

	ctx := context.Background()
	conn1 := newNotifyTestConn("dev-1", "Client 1")
	conn2 := newNotifyTestConn("dev-2", "Client 2")

	createdThreadID := "11111111-1111-1111-1111-111111111111"
	projectID := "22222222-2222-2222-2222-222222222222"
	actionID := "33333333-3333-3333-3333-333333333333"

	currentSummary := wire.ThreadSummary{
		ID:        createdThreadID,
		ProjectID: projectID,
		Title:     "初始会话",
	}

	mockBackend := &fakeBackend{
		createThreadFunc: func(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error) {
			return wire.ThreadCreateResult{
				ActionID: params.ActionID,
				ThreadID: createdThreadID,
			}, nil
		},
		getThreadFunc: func(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
			return currentSummary, nil
		},
		updateThreadFunc: func(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error) {
			if params.Title != nil {
				currentSummary.Title = *params.Title
			}
			return wire.ThreadUpdateResult{
				Thread: currentSummary,
			}, nil
		},
	}

	auth := &fakeAuth{}
	d := NewDispatcher(auth, mockBackend)

	// 先让两个已认证连接通过简单请求被 Dispatcher 注册到 authedConns
	authReq := `{"jsonrpc":"2.0","method":"threads.list","params":{"projectID":"` + projectID + `"},"id":1}`
	_ = d.Handle(ctx, conn1, []byte(authReq))
	_ = d.Handle(ctx, conn2, []byte(authReq))

	// 清空初次调用的通知
	conn1.mu.Lock()
	conn1.notifications = nil
	conn1.mu.Unlock()
	conn2.mu.Lock()
	conn2.notifications = nil
	conn2.mu.Unlock()

	// 1. 调用 threads.create
	createReq := `{"jsonrpc":"2.0","method":"threads.create","params":{"projectID":"` + projectID + `","actionID":"` + actionID + `"},"id":2}`
	resBytes := d.Handle(ctx, conn1, []byte(createReq))

	var createResp wire.Response
	if err := json.Unmarshal(resBytes, &createResp); err != nil || createResp.Error != nil {
		t.Fatalf("threads.create failed: %v, resp: %s", err, string(resBytes))
	}

	// 验证两个连接都收到 threads.changed 通知
	for i, c := range []*notifyTestConn{conn1, conn2} {
		notifs := c.getNotifications()
		if len(notifs) != 1 {
			t.Fatalf("conn %d expected 1 notification after create, got %d", i+1, len(notifs))
		}
		if notifs[0].method != "threads.changed" {
			t.Fatalf("conn %d expected method 'threads.changed', got %q", i+1, notifs[0].method)
		}

		raw, err := json.Marshal(notifs[0].params)
		if err != nil {
			t.Fatalf("marshal params failed: %v", err)
		}

		var changed wire.ThreadsChanged
		if err := json.Unmarshal(raw, &changed); err != nil {
			t.Fatalf("unmarshal threads.changed params failed: %v", err)
		}

		if changed.Revision <= 0 {
			t.Fatalf("expected revision > 0, got %d", changed.Revision)
		}
		if len(changed.Threads) != 1 || changed.Threads[0].ID != createdThreadID {
			t.Fatalf("unexpected threads in changed notification: %+v", changed.Threads)
		}
		if changed.RemovedThreadIDs == nil {
			t.Fatalf("expected removedThreadIDs array, got nil")
		}
	}

	// 2. 调用 threads.update
	newTitle := "更新后的标题"
	updateReq := `{"jsonrpc":"2.0","method":"threads.update","params":{"threadID":"` + createdThreadID + `","title":"` + newTitle + `"},"id":3}`
	updateResBytes := d.Handle(ctx, conn2, []byte(updateReq))

	var updateResp wire.Response
	if err := json.Unmarshal(updateResBytes, &updateResp); err != nil || updateResp.Error != nil {
		t.Fatalf("threads.update failed: %v, resp: %s", err, string(updateResBytes))
	}

	// 验证两个连接都收到第二条 threads.changed 通知
	for i, c := range []*notifyTestConn{conn1, conn2} {
		notifs := c.getNotifications()
		if len(notifs) != 2 {
			t.Fatalf("conn %d expected 2 notifications after update, got %d", i+1, len(notifs))
		}
		if notifs[1].method != "threads.changed" {
			t.Fatalf("conn %d expected method 'threads.changed', got %q", i+1, notifs[1].method)
		}

		raw, err := json.Marshal(notifs[1].params)
		if err != nil {
			t.Fatalf("marshal params failed: %v", err)
		}

		var changed wire.ThreadsChanged
		if err := json.Unmarshal(raw, &changed); err != nil {
			t.Fatalf("unmarshal threads.changed params failed: %v", err)
		}

		if len(changed.Threads) != 1 || changed.Threads[0].Title != newTitle {
			t.Fatalf("unexpected updated title in changed notification: %+v", changed.Threads)
		}
	}
}
