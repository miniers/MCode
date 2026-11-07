package remote

import (
	"errors"
	"sync"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

type mockStreamConn struct {
	mu       sync.Mutex
	info     ClientInfo
	events   []wire.ThreadEvent
	notifyFn func(method string, params any) error
	notifyCh chan wire.ThreadEvent
}

func newMockStreamConn() *mockStreamConn {
	return &mockStreamConn{
		notifyCh: make(chan wire.ThreadEvent, 100),
	}
}

func (c *mockStreamConn) Info() ClientInfo {
	return c.info
}

func (c *mockStreamConn) MarkAuthenticated(deviceID, label string) {
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *mockStreamConn) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.notifyFn != nil {
		if err := c.notifyFn(method, params); err != nil {
			return err
		}
	}

	if method == "threads.event" {
		if ev, ok := params.(wire.ThreadEvent); ok {
			c.events = append(c.events, ev)
			select {
			case c.notifyCh <- ev:
			default:
			}
		}
	}
	return nil
}

func (c *mockStreamConn) Close() error {
	return nil
}

func (c *mockStreamConn) getEvents() []wire.ThreadEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make([]wire.ThreadEvent, len(c.events))
	copy(res, c.events)
	return res
}

func TestStreamHub_PublishMergeAndFlush(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	conn := newMockStreamConn()
	threadID := "00000000-0000-0000-0000-000000000001"
	hub.Subscribe(conn, threadID)

	summary := wire.ThreadSummary{ID: threadID, Title: "Test Thread"}
	msg1v1 := wire.ThreadMessage{ID: "m1", Text: "Hello"}
	msg2v1 := wire.ThreadMessage{ID: "m2", Text: "World"}
	msg1v2 := wire.ThreadMessage{ID: "m1", Text: "Hello updated"}

	hub.Publish(threadID, []wire.ThreadMessage{msg1v1}, nil, summary)
	hub.Publish(threadID, []wire.ThreadMessage{msg2v1}, nil, summary)
	hub.Publish(threadID, []wire.ThreadMessage{msg1v2}, nil, summary)

	hub.Flush(threadID)

	events := conn.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after flush, got %d", len(events))
	}

	ev := events[0]
	if ev.Sequence != 1 {
		t.Errorf("expected sequence 1, got %d", ev.Sequence)
	}
	if ev.Kind != "projection" {
		t.Errorf("expected kind 'projection', got %s", ev.Kind)
	}
	if len(ev.Payload.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(ev.Payload.Messages))
	}

	// 验证保留首次出现的顺序 (m1, m2)，且 m1 是最新内容
	if ev.Payload.Messages[0].ID != "m1" || ev.Payload.Messages[0].Text != "Hello updated" {
		t.Errorf("unexpected first message: %+v", ev.Payload.Messages[0])
	}
	if ev.Payload.Messages[1].ID != "m2" || ev.Payload.Messages[1].Text != "World" {
		t.Errorf("unexpected second message: %+v", ev.Payload.Messages[1])
	}
}

func TestStreamHub_MultipleFlushesAndBeginRun(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	conn := newMockStreamConn()
	threadID := "00000000-0000-0000-0000-000000000002"
	hub.Subscribe(conn, threadID)

	summary := wire.ThreadSummary{ID: threadID}

	// 第一次 Flush，sequence 应为 1
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m1", Text: "step 1"}}, nil, summary)
	hub.Flush(threadID)

	// 第二次 Flush，sequence 应为 2
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m2", Text: "step 2"}}, nil, summary)
	hub.Flush(threadID)

	events := conn.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Errorf("expected sequences 1, 2, got %d, %d", events[0].Sequence, events[1].Sequence)
	}

	runID1 := events[0].RunID
	if events[1].RunID != runID1 {
		t.Errorf("runID should match in same run: %s vs %s", runID1, events[1].RunID)
	}

	// BeginRun 重置执行轮次
	runID2 := hub.BeginRun(threadID)
	if runID2 == runID1 {
		t.Errorf("new runID should differ from old runID: %s", runID2)
	}

	// BeginRun 后第一次 Flush，sequence 应重置为 1
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m3", Text: "new run step 1"}}, nil, summary)
	hub.Flush(threadID)

	eventsAfter := conn.getEvents()
	if len(eventsAfter) != 3 {
		t.Fatalf("expected 3 events total, got %d", len(eventsAfter))
	}
	if eventsAfter[2].Sequence != 1 {
		t.Errorf("expected sequence 1 after BeginRun, got %d", eventsAfter[2].Sequence)
	}
	if eventsAfter[2].RunID != runID2 {
		t.Errorf("expected runID %s, got %s", runID2, eventsAfter[2].RunID)
	}
}

func TestStreamHub_MergeUpsertAndRemove(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	conn := newMockStreamConn()
	threadID := "00000000-0000-0000-0000-000000000003"
	hub.Subscribe(conn, threadID)
	summary := wire.ThreadSummary{ID: threadID}

	// 1. 先 upsert 后 remove：从 messages 中删掉，只保留在 removed
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m1", Text: "msg1"}, {ID: "m2", Text: "msg2"}}, nil, summary)
	hub.Publish(threadID, nil, []string{"m1"}, summary)
	hub.Flush(threadID)

	events := conn.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev1 := events[0]
	if len(ev1.Payload.Messages) != 1 || ev1.Payload.Messages[0].ID != "m2" {
		t.Errorf("expected only m2 in messages, got: %+v", ev1.Payload.Messages)
	}
	if len(ev1.Payload.RemovedMessageIDs) != 1 || ev1.Payload.RemovedMessageIDs[0] != "m1" {
		t.Errorf("expected m1 in removedIDs, got: %+v", ev1.Payload.RemovedMessageIDs)
	}

	// 2. 先 remove 后 upsert：从 removed 中删掉，保留在 messages
	hub.BeginRun(threadID)
	hub.Publish(threadID, nil, []string{"m3"}, summary)
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m3", Text: "msg3 revived"}}, nil, summary)
	hub.Flush(threadID)

	events2 := conn.getEvents()
	if len(events2) != 2 {
		t.Fatalf("expected 2 events total, got %d", len(events2))
	}
	ev2 := events2[1]
	if len(ev2.Payload.Messages) != 1 || ev2.Payload.Messages[0].ID != "m3" {
		t.Errorf("expected m3 in messages, got: %+v", ev2.Payload.Messages)
	}
	if len(ev2.Payload.RemovedMessageIDs) != 0 {
		t.Errorf("expected empty removedIDs, got: %+v", ev2.Payload.RemovedMessageIDs)
	}
}

func TestStreamHub_SubscribeWatermark(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	threadID := "00000000-0000-0000-0000-000000000004"
	conn1 := newMockStreamConn()

	// 未开始过 run 的线程，返回空 runID 与 0
	runID0, seq0 := hub.Subscribe(conn1, threadID)
	if runID0 != "" || seq0 != 0 {
		t.Errorf("expected empty runID and seq 0 for fresh thread, got %q, %d", runID0, seq0)
	}

	summary := wire.ThreadSummary{ID: threadID}
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m1", Text: "1"}}, nil, summary)
	hub.Flush(threadID) // sequence 变为 1
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m2", Text: "2"}}, nil, summary)
	hub.Flush(threadID) // sequence 变为 2

	// 此时新连接订阅，返回当前水位 sequence == 2
	conn2 := newMockStreamConn()
	runID, seq := hub.Subscribe(conn2, threadID)
	if seq != 2 {
		t.Errorf("expected sequence 2, got %d", seq)
	}
	if runID == "" {
		t.Errorf("expected non-empty runID")
	}
}

func TestStreamHub_NotifyErrorRemovesConn(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	threadID := "00000000-0000-0000-0000-000000000005"
	badConn := newMockStreamConn()
	badConn.notifyFn = func(method string, params any) error {
		return errors.New("simulated pipe broken")
	}

	hub.Subscribe(badConn, threadID)
	summary := wire.ThreadSummary{ID: threadID}

	// 第一次下发，badConn 报错被移除
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m1", Text: "hello"}}, nil, summary)
	hub.Flush(threadID)

	// 将 notifyFn 恢复正常
	badConn.mu.Lock()
	badConn.notifyFn = nil
	badConn.mu.Unlock()

	// 第二次下发，badConn 已被移除，不应再收到新通知
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m2", Text: "world"}}, nil, summary)
	hub.Flush(threadID)

	if len(badConn.getEvents()) != 0 {
		t.Errorf("removed conn should not receive subsequent events, got %d events", len(badConn.getEvents()))
	}
}

func TestStreamHub_AutomaticFlush(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	threadID := "00000000-0000-0000-0000-000000000006"
	conn := newMockStreamConn()
	hub.Subscribe(conn, threadID)

	summary := wire.ThreadSummary{ID: threadID}
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "auto-msg", Text: "auto flush text"}}, nil, summary)

	// 带超时的等待，不调用 Flush 也能自动收到事件
	select {
	case ev := <-conn.notifyCh:
		if ev.Sequence != 1 {
			t.Errorf("expected sequence 1 from automatic flush, got %d", ev.Sequence)
		}
		if len(ev.Payload.Messages) != 1 || ev.Payload.Messages[0].ID != "auto-msg" {
			t.Errorf("unexpected payload from automatic flush: %+v", ev.Payload)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for automatic flush event")
	}
}

func TestStreamHub_NotifyAndCurrentRunID(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	threadID := "00000000-0000-0000-0000-000000000099"

	// 1. 测试未开始 run 时的 CurrentRunID
	if runID := hub.CurrentRunID(threadID); runID != "" {
		t.Errorf("expected empty runID before BeginRun, got %q", runID)
	}

	// 2. 测试 BeginRun 后的 CurrentRunID
	expectedRunID := hub.BeginRun(threadID)
	if runID := hub.CurrentRunID(threadID); runID != expectedRunID {
		t.Errorf("expected runID %q, got %q", expectedRunID, runID)
	}

	// 3. 测试 Notify：先 Flush 待发的 threads.event，然后广播自定义通知
	var (
		mu           sync.Mutex
		notifMethods []string
	)
	conn := newMockStreamConn()
	conn.notifyFn = func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		notifMethods = append(notifMethods, method)
		return nil
	}
	hub.Subscribe(conn, threadID)

	// 发布一条消息（放入待发缓冲，未显式 Flush）
	summary := wire.ThreadSummary{ID: threadID, Title: "Notify Test"}
	hub.Publish(threadID, []wire.ThreadMessage{{ID: "m-notify", Text: "pending"}}, nil, summary)

	dummyParams := map[string]string{"foo": "bar"}
	hub.Notify(threadID, "requests.changed", dummyParams)

	// 检查事件是否已经先 Flush
	events := conn.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 flushed threads.event before requests.changed, got %d", len(events))
	}
	if len(events[0].Payload.Messages) != 1 || events[0].Payload.Messages[0].ID != "m-notify" {
		t.Errorf("unexpected event payload: %+v", events[0].Payload)
	}

	// 检查是否收到了 requests.changed 通知
	mu.Lock()
	methods := append([]string(nil), notifMethods...)
	mu.Unlock()

	found := false
	for _, m := range methods {
		if m == "requests.changed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to receive 'requests.changed' notification, got methods: %v", methods)
	}

	// 4. 测试 Notify 时发送失败的连接被自动移除
	errConn := newMockStreamConn()
	errConn.notifyFn = func(method string, params any) error {
		if method == "test.fail" {
			return errors.New("connection broken")
		}
		return nil
	}
	hub.Subscribe(errConn, threadID)
	hub.Notify(threadID, "test.fail", nil)

	// 再次 Notify，errConn 不应再收到通知
	errConnReceived := false
	errConn.notifyFn = func(method string, params any) error {
		errConnReceived = true
		return nil
	}
	hub.Notify(threadID, "test.after", nil)
	if errConnReceived {
		t.Errorf("expected failed connection to be removed from subscribers")
	}
}
