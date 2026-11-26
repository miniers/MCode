package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// C-ST-07：超过 warmCacheLimit 的已归档冷会话在 ListThreads 后被淘汰（messages 置空），
// 再次读取时按需从 store 完整回载。
func TestThreadCache_EvictColdArchivedThreads(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "thread-cache.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "LRU 项目", Path: "/tmp/lru-project"})

	const total = 40
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("thread-%02d", i)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		msgs := []wire.ThreadMessage{
			{ID: id + "-user", Role: "user", Text: fmt.Sprintf("问题-%02d", i), CreatedAt: now},
			{ID: id + "-reply", Role: "assistant", Text: fmt.Sprintf("回答-%02d", i), CreatedAt: now},
		}
		backend.AddThread(wire.ThreadSummary{
			ID:         id,
			ProjectID:  "p1",
			Title:      "归档会话",
			Provider:   "memory",
			IsArchived: true,
		}, msgs)
	}

	if _, err := backend.ListThreads(ctx, wire.ThreadListParams{}); err != nil {
		t.Fatalf("list threads: %v", err)
	}

	backend.mu.Lock()
	evicted := 0
	var evictedID string
	for id, td := range backend.threads {
		if td.messagesEvicted {
			evicted++
			evictedID = id
			if td.messages != nil {
				t.Errorf("evicted thread %s still holds messages", id)
			}
		}
	}
	backend.mu.Unlock()

	if evicted != total-warmCacheLimit {
		t.Fatalf("expected %d evicted threads, got %d", total-warmCacheLimit, evicted)
	}

	// 被淘汰会话读取消息时按需回载，内容与顺序完整，且淘汰标记被清除。
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: evictedID})
	if err != nil {
		t.Fatalf("thread history: %v", err)
	}
	idx := strings.TrimPrefix(evictedID, "thread-")
	if len(hist.Messages) != 2 {
		t.Fatalf("expected 2 messages after reload, got %d: %+v", len(hist.Messages), hist.Messages)
	}
	if hist.Messages[0].Role != "user" || hist.Messages[0].Text != "问题-"+idx {
		t.Errorf("message 0 mismatch: %+v", hist.Messages[0])
	}
	if hist.Messages[1].Role != "assistant" || hist.Messages[1].Text != "回答-"+idx {
		t.Errorf("message 1 mismatch: %+v", hist.Messages[1])
	}

	backend.mu.Lock()
	stillEvicted := backend.threads[evictedID].messagesEvicted
	backend.mu.Unlock()
	if stillEvicted {
		t.Fatalf("messagesEvicted should be cleared after reload")
	}
}

func TestSessionStatusKeepsMessagesEvicted(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "p", Name: "project", Path: t.TempDir()})
	status := "interrupted"
	backend.AddThread(wire.ThreadSummary{ID: "lead", ProjectID: "p", LastStatus: &status}, []wire.ThreadMessage{
		{ID: "reply", Role: "assistant", Text: "hello", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	})
	backend.mu.Lock()
	td := backend.threads["lead"]
	td.messages = nil
	td.messagesEvicted = true
	td.pending["approval"] = &pendingRequest{req: wire.RemoteRequest{Kind: "question"}, done: make(chan struct{})}
	lastAccess := td.lastAccess
	backend.mu.Unlock()

	got, err := backend.SessionStatus(ctx, "lead")
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	if !td.messagesEvicted || len(td.messages) != 0 || td.lastAccess != lastAccess {
		t.Error("SessionStatus 不应回载消息或更新访问时间")
	}
	backend.mu.Unlock()
	if got.LastAssistantText != "" || got.Progress != "" || got.Running || !got.Interrupted || !got.AwaitingApproval || !got.AwaitingInput {
		t.Fatalf("状态不符合预期: %+v", got)
	}
	full, err := backend.SessionState(ctx, "lead")
	if err != nil {
		t.Fatal(err)
	}
	if full.LastAssistantText != "hello" || full.Progress == "" {
		t.Fatalf("SessionState 应回载消息并填充消息字段: %+v", full)
	}
	full.LastAssistantText, full.Progress = "", ""
	if got != full {
		t.Fatalf("状态不一致: status=%+v state=%+v", got, full)
	}
}

func setupThreadCacheTestBackend(t *testing.T, count int, clockStart time.Time) (*MemoryBackend, *StreamHub, *time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "thread-cache.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	cur := clockStart
	backend.SetClock(func() time.Time { return cur })
	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "缓存测试项目", Path: "/tmp/cache-project"})

	for i := 0; i < count; i++ {
		id := fmt.Sprintf("thread-%02d", i)
		ts := cur.UTC().Format(time.RFC3339Nano)
		msgs := []wire.ThreadMessage{
			{ID: id + "-user", Role: "user", Text: fmt.Sprintf("问题-%02d", i), CreatedAt: ts, TurnIndex: 0},
			{ID: id + "-reply", Role: "assistant", Text: fmt.Sprintf("回答-%02d", i), CreatedAt: ts, TurnIndex: 0},
		}
		backend.AddThread(wire.ThreadSummary{
			ID:        id,
			ProjectID: "p1",
			Title:     fmt.Sprintf("未归档会话-%02d", i),
			Provider:  "memory",
		}, msgs)
		cur = cur.Add(time.Second)
		backend.mu.Lock()
		backend.touchThreadLocked(backend.threads[id])
		backend.mu.Unlock()
	}
	return backend, hub, &cur
}

func TestEvictColdThreads_UnarchivedEvictsOverLimitAndReloads(t *testing.T) {
	ctx := context.Background()
	const total = 40
	backend, _, _ := setupThreadCacheTestBackend(t, total, time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))

	if _, err := backend.ListThreads(ctx, wire.ThreadListParams{}); err != nil {
		t.Fatalf("list threads: %v", err)
	}

	evictedCount := total - warmCacheLimit
	backend.mu.Lock()
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("thread-%02d", i)
		td := backend.threads[id]
		wantEvicted := i < evictedCount
		if td.messagesEvicted != wantEvicted {
			t.Errorf("%s messagesEvicted = %v, want %v", id, td.messagesEvicted, wantEvicted)
		}
		if wantEvicted && td.messages != nil {
			t.Errorf("%s should have nil messages after eviction", id)
		}
		if !wantEvicted && len(td.messages) != 2 {
			t.Errorf("%s should keep 2 warm messages, got %d", id, len(td.messages))
		}
	}
	backend.mu.Unlock()

	// 验证读取冷会话历史时按需完整回载。
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "thread-00"})
	if err != nil {
		t.Fatalf("thread history: %v", err)
	}
	if len(hist.Messages) != 2 || hist.Messages[0].Text != "问题-00" || hist.Messages[1].Text != "回答-00" {
		t.Fatalf("unexpected reloaded history for thread-00: %+v", hist.Messages)
	}
	backend.mu.Lock()
	if backend.threads["thread-00"].messagesEvicted {
		t.Fatalf("thread-00 messagesEvicted should be cleared after ThreadHistory")
	}
	backend.mu.Unlock()

	// 验证向被淘汰的未归档会话发送新消息时旧消息回载且不丢。
	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: "thread-01",
		Text:     "追问-01",
	})
	if err != nil {
		t.Fatalf("send message to evicted thread: %v", err)
	}
	if sendRes.TurnID == nil {
		t.Fatalf("expected non-nil TurnID")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		summary, err := backend.GetThread(ctx, "thread-01")
		if err != nil {
			t.Fatalf("get thread: %v", err)
		}
		if !summary.IsRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for thread-01 turn completion")
		}
		time.Sleep(5 * time.Millisecond)
	}

	afterSend, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "thread-01"})
	if err != nil {
		t.Fatalf("thread history after send: %v", err)
	}
	if len(afterSend.Messages) < 4 {
		t.Fatalf("expected at least 4 messages after send (2 old + 2 new), got %d: %+v", len(afterSend.Messages), afterSend.Messages)
	}
	if afterSend.Messages[0].Text != "问题-01" || afterSend.Messages[1].Text != "回答-01" {
		t.Fatalf("old messages lost or reordered: %+v", afterSend.Messages[:2])
	}
	if afterSend.Messages[2].Role != "user" || afterSend.Messages[2].Text != "追问-01" {
		t.Fatalf("new user message mismatch: %+v", afterSend.Messages[2])
	}
	if afterSend.Messages[3].Role != "assistant" || afterSend.Messages[3].Text != "echo: 追问-01" {
		t.Fatalf("new assistant message mismatch: %+v", afterSend.Messages[3])
	}
}

func TestEvictColdThreads_SubscribedNotEvicted(t *testing.T) {
	ctx := context.Background()
	const total = 40
	backend, _, _ := setupThreadCacheTestBackend(t, total, time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC))

	conn := newMockStreamConn()
	if _, err := backend.Subscribe(ctx, conn, wire.SubscribeParams{ThreadID: "thread-00"}); err != nil {
		t.Fatalf("subscribe thread-00: %v", err)
	}
	// 恢复 thread-00 为最早访问时间，验证保护生效来自订阅而不是 lastAccess。
	backend.mu.Lock()
	backend.threads["thread-00"].lastAccess = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	backend.mu.Unlock()

	if _, err := backend.ListThreads(ctx, wire.ThreadListParams{}); err != nil {
		t.Fatalf("list threads: %v", err)
	}

	backend.mu.Lock()
	td0 := backend.threads["thread-00"]
	if td0.messagesEvicted || len(td0.messages) != 2 {
		t.Errorf("subscribed thread-00 should not be evicted, evicted=%v len=%d", td0.messagesEvicted, len(td0.messages))
	}
	evicted := 0
	for i := 1; i < total; i++ {
		id := fmt.Sprintf("thread-%02d", i)
		if backend.threads[id].messagesEvicted {
			evicted++
		}
	}
	backend.mu.Unlock()
	if want := (total - 1) - warmCacheLimit; evicted != want {
		t.Fatalf("expected %d unsubscribed threads evicted, got %d", want, evicted)
	}

	if err := backend.Unsubscribe(ctx, conn, "thread-00"); err != nil {
		t.Fatalf("unsubscribe thread-00: %v", err)
	}
	if _, err := backend.ListThreads(ctx, wire.ThreadListParams{}); err != nil {
		t.Fatalf("list threads after unsubscribe: %v", err)
	}
	backend.mu.Lock()
	if !backend.threads["thread-00"].messagesEvicted {
		t.Fatalf("thread-00 should be evicted after unsubscribe")
	}
	backend.mu.Unlock()
}

func TestEvictColdThreads_RunningAndUnpersistedNotEvicted(t *testing.T) {
	ctx := context.Background()
	const total = 36
	backend, _, _ := setupThreadCacheTestBackend(t, total, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))

	backend.mu.Lock()
	// 最冷的两个设为不可淘汰：thread-00 运行中，thread-01 有未持久化消息。
	runningTd := backend.threads["thread-00"]
	runningTd.summary.IsRunning = true
	runningTd.runTurnID = "turn-running"

	unpersistedTd := backend.threads["thread-01"]
	unpersistedTd.messages = append(unpersistedTd.messages, wire.ThreadMessage{
		ID:        "unpersisted-msg",
		Role:      "user",
		Text:      "未落库消息",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	backend.mu.Unlock()

	if _, err := backend.ListThreads(ctx, wire.ThreadListParams{}); err != nil {
		t.Fatalf("list threads: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if runningTd.messagesEvicted || len(runningTd.messages) != 2 {
		t.Errorf("running thread-00 should not be evicted, evicted=%v len=%d", runningTd.messagesEvicted, len(runningTd.messages))
	}
	if unpersistedTd.messagesEvicted || len(unpersistedTd.messages) != 3 {
		t.Errorf("unpersisted thread-01 should not be evicted, evicted=%v len=%d", unpersistedTd.messagesEvicted, len(unpersistedTd.messages))
	}
	// 其余 34 个空闲已持久化会话中，最冷的 thread-02 和 thread-03 被淘汰。
	for i := 2; i < total; i++ {
		id := fmt.Sprintf("thread-%02d", i)
		wantEvicted := i < 4
		if backend.threads[id].messagesEvicted != wantEvicted {
			t.Errorf("%s messagesEvicted = %v, want %v", id, backend.threads[id].messagesEvicted, wantEvicted)
		}
	}
}
