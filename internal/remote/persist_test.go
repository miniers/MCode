package remote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestPersist_SimulateAndReload(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store 1: %v", err)
	}

	projID := "proj-sim-1"
	proj := wire.ProjectSummary{
		ID:   projID,
		Name: "Test Project",
		Path: "/test/path/project1",
	}
	backend1.AddProject(proj)

	thID := "thread-sim-1"
	summary := wire.ThreadSummary{
		ID:        thID,
		ProjectID: projID,
		Title:     "Sim Thread",
		Provider:  "memory",
	}
	initMsg := wire.ThreadMessage{
		ID:        "msg-init-1",
		Role:      "assistant",
		Text:      "welcome message",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	backend1.AddThread(summary, []wire.ThreadMessage{initMsg})

	// 发送消息并等待模拟回复完成
	_, err = backend1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "hello world",
		ActionID: "act-sim-1",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	waitThreadIdle(t, backend1, ctx, thID)

	// 关闭第一组实例
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	// 重新打开数据库与新 Backend
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	// 验证项目恢复
	pList, err := backend2.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(pList.Projects) != 1 || pList.Projects[0].ID != projID || pList.Projects[0].Name != proj.Name {
		t.Fatalf("unexpected projects: %+v", pList.Projects)
	}

	// 验证线程恢复
	th, err := backend2.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if th.ID != thID || th.ProjectID != projID || th.IsRunning {
		t.Fatalf("unexpected thread: %+v", th)
	}

	// 验证消息历史及顺序
	hist, err := backend2.ThreadHistory(ctx, wire.HistoryParams{ThreadID: thID})
	if err != nil {
		t.Fatalf("thread history: %v", err)
	}
	if len(hist.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(hist.Messages), hist.Messages)
	}

	if hist.Messages[0].ID != "msg-init-1" || hist.Messages[0].Text != "welcome message" {
		t.Errorf("msg 0 mismatch: %+v", hist.Messages[0])
	}
	if hist.Messages[1].Role != "user" || hist.Messages[1].Text != "hello world" {
		t.Errorf("msg 1 mismatch: %+v", hist.Messages[1])
	}
	if hist.Messages[2].Role != "assistant" || hist.Messages[2].Text != "echo: hello world" {
		t.Errorf("msg 2 mismatch: %+v", hist.Messages[2])
	}
	if hist.Messages[2].IsFinalResponse == nil || !*hist.Messages[2].IsFinalResponse {
		t.Errorf("expected final response to be true on assistant reply: %+v", hist.Messages[2])
	}
}

func TestPersist_SessionResumeAndReload(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	expectedSessionID := "sess-persisted-xyz-999"
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventSession, SessionID: expectedSessionID})
			emit(harness.Event{Type: harness.EventText, Text: "harness reply"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend1.SetHarness(mock, "/mock/workdir", harness.ModeAgent)

	projID := "proj-sess-1"
	backend1.AddProject(wire.ProjectSummary{ID: projID, Name: "Proj Sess", Path: "/mock/workdir"})

	thID := "thread-sess-1"
	backend1.AddThread(wire.ThreadSummary{ID: thID, ProjectID: projID, Title: "Sess Thread"}, nil)

	_, err = backend1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "trigger session",
		ActionID: "act-sess-1",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	waitThreadIdle(t, backend1, ctx, thID)

	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	// 重启验证 sessionID
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	backend2.mu.Lock()
	memTh, ok := backend2.threads[thID]
	var loadedSessionID string
	if ok {
		loadedSessionID = memTh.sessionID
	}
	backend2.mu.Unlock()

	if !ok {
		t.Fatalf("thread %s not found in reloaded backend", thID)
	}
	if loadedSessionID != expectedSessionID {
		t.Fatalf("expected session ID %q, got %q", expectedSessionID, loadedSessionID)
	}
}

func TestPersist_CreateThread(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	projID := "proj-ct-1"
	backend1.AddProject(wire.ProjectSummary{ID: projID, Name: "Proj CT", Path: "/path/ct"})

	// 1. 项目不存在时报错
	badRes, err := backend1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "bad-project-id",
		ActionID:  "act-fail-1",
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent project, got result: %+v", badRes)
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound, got: %v", err)
	}

	// 2. 项目存在时创建成功
	actionID := "act-ct-create-1"
	res1, err := backend1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  actionID,
	})
	if err != nil {
		t.Fatalf("create thread 1 failed: %v", err)
	}
	if res1.ThreadID == "" || res1.ActionID != actionID {
		t.Fatalf("unexpected create thread result: %+v", res1)
	}

	// 3. 幂等性：同一 actionID 重复调用返回相同 threadID
	res2, err := backend1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  actionID,
	})
	if err != nil {
		t.Fatalf("create thread 2 idempotent failed: %v", err)
	}
	if res2.ThreadID != res1.ThreadID {
		t.Fatalf("idempotency violated: got %s, expected %s", res2.ThreadID, res1.ThreadID)
	}

	// 4. 重开后线程仍在
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	th, err := backend2.GetThread(ctx, res1.ThreadID)
	if err != nil {
		t.Fatalf("failed to get created thread after reload: %v", err)
	}
	if th.ID != res1.ThreadID || th.ProjectID != projID || th.Title != "新会话" {
		t.Fatalf("reloaded thread mismatch: %+v", th)
	}
}

func TestPersist_MultipleProjectsReload(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode.db")

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store 1: %v", err)
	}

	p1, err := backend1.AddProjectPath(ctx, dir1)
	if err != nil {
		t.Fatalf("AddProjectPath dir1: %v", err)
	}
	p2, err := backend1.AddProjectPath(ctx, dir2)
	if err != nil {
		t.Fatalf("AddProjectPath dir2: %v", err)
	}

	_, err = backend1.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p1.ID})
	if err != nil {
		t.Fatalf("CreateThread in p1: %v", err)
	}
	_, err = backend1.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p2.ID})
	if err != nil {
		t.Fatalf("CreateThread in p2: %v", err)
	}

	// 关闭第一组实例
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	// 重新打开数据库与新 Backend
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	pList, err := backend2.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list projects after reload: %v", err)
	}
	if len(pList.Projects) != 2 {
		t.Fatalf("expected 2 projects after reload, got %d", len(pList.Projects))
	}

	foundP1, foundP2 := false, false
	for _, p := range pList.Projects {
		if p.ID == p1.ID {
			foundP1 = true
			if p.Path != p1.Path || p.Name != p1.Name || p.ThreadCount != 1 {
				t.Errorf("p1 reload mismatch: %+v", p)
			}
		}
		if p.ID == p2.ID {
			foundP2 = true
			if p.Path != p2.Path || p.Name != p2.Name || p.ThreadCount != 1 {
				t.Errorf("p2 reload mismatch: %+v", p)
			}
		}
	}
	if !foundP1 || !foundP2 {
		t.Errorf("not all projects found after reload: foundP1=%v, foundP2=%v", foundP1, foundP2)
	}
}

// TestPersist_LazyLoadMessages 验证 AttachStore 启动时消息惰性加载：
// 启动时只加载项目与线程元数据（messagesLoaded=false），
// 首次调用读取消息的公开接口（如 ThreadHistory）时才按需载入完整且顺序正确的历史；
// 对未加载消息的线程追加新消息后，再读取时历史消息与新消息均完整保留。
func TestPersist_LazyLoadMessages(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "lazy_load.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	defer s.Close()

	// 1. 预置项目与两个线程到 store
	projID := "proj-lazy"
	if err := s.UpsertProject(ctx, store.Project{
		ID:        projID,
		Name:      "Lazy Project",
		Path:      "/tmp/lazy-proj",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	t1ID := "thread-lazy-1"
	t2ID := "thread-lazy-2"
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, store.Thread{
		ID:        t1ID,
		ProjectID: projID,
		Title:     "Thread Lazy 1",
		Provider:  "memory",
		SortOrder: 10,
		HasUnread: true,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	if err := s.CreateThread(ctx, store.Thread{
		ID:        t2ID,
		ProjectID: projID,
		Title:     "Thread Lazy 2",
		Provider:  "memory",
		SortOrder: 20,
		HasUnread: false,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create thread 2: %v", err)
	}

	// 2. 为两个线程写入历史消息
	t1Msgs := []store.Message{
		{ID: "m1-1", ThreadID: t1ID, Role: "user", Text: "T1 User 1", CreatedAt: now.Add(1 * time.Second)},
		{ID: "m1-2", ThreadID: t1ID, Role: "assistant", Text: "T1 Assistant 1", CreatedAt: now.Add(2 * time.Second)},
		{ID: "m1-3", ThreadID: t1ID, Role: "user", Text: "T1 User 2", CreatedAt: now.Add(3 * time.Second)},
	}
	for _, m := range t1Msgs {
		if _, err := s.AppendMessage(ctx, m); err != nil {
			t.Fatalf("append message to t1: %v", err)
		}
	}

	t2Msgs := []store.Message{
		{ID: "m2-1", ThreadID: t2ID, Role: "user", Text: "T2 User 1", CreatedAt: now.Add(1 * time.Second)},
		{ID: "m2-2", ThreadID: t2ID, Role: "assistant", Text: "T2 Assistant 1", CreatedAt: now.Add(2 * time.Second)},
	}
	for _, m := range t2Msgs {
		if _, err := s.AppendMessage(ctx, m); err != nil {
			t.Fatalf("append message to t2: %v", err)
		}
	}

	// 3. 启动 MemoryBackend 并挂载 store
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	// 4. 断言启动后消息未加载，内存中 messages 为空或未就绪
	backend.mu.Lock()
	td1, ok1 := backend.threads[t1ID]
	td2, ok2 := backend.threads[t2ID]
	if !ok1 || !ok2 {
		backend.mu.Unlock()
		t.Fatalf("threads not found in backend: ok1=%v, ok2=%v", ok1, ok2)
	}
	if td1.messagesLoaded || len(td1.messages) > 0 {
		backend.mu.Unlock()
		t.Fatalf("expected t1 messages not loaded initially, got messagesLoaded=%v, len(messages)=%d", td1.messagesLoaded, len(td1.messages))
	}
	if td2.messagesLoaded || len(td2.messages) > 0 {
		backend.mu.Unlock()
		t.Fatalf("expected t2 messages not loaded initially, got messagesLoaded=%v, len(messages)=%d", td2.messagesLoaded, len(td2.messages))
	}
	backend.mu.Unlock()

	// 5. 断言线程列表元数据在消息未加载时仍正常可用
	thList, err := backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	if len(thList.Threads) != 2 {
		t.Fatalf("expected 2 threads in list, got %d", len(thList.Threads))
	}
	metaChecked := 0
	for _, th := range thList.Threads {
		if th.ID == t1ID {
			metaChecked++
			if th.Title != "Thread Lazy 1" || !th.HasUnread || th.ProjectID != projID {
				t.Errorf("t1 metadata mismatch: %+v", th)
			}
		}
		if th.ID == t2ID {
			metaChecked++
			if th.Title != "Thread Lazy 2" || th.HasUnread || th.ProjectID != projID {
				t.Errorf("t2 metadata mismatch: %+v", th)
			}
		}
	}
	if metaChecked != 2 {
		t.Errorf("expected both threads metadata checked, got %d", metaChecked)
	}

	// 再次确认 ListThreads 没有误触发消息加载
	backend.mu.Lock()
	if td1.messagesLoaded || td2.messagesLoaded {
		backend.mu.Unlock()
		t.Fatalf("ListThreads should not trigger message loading")
	}
	backend.mu.Unlock()

	// 6. 调用读取消息公开方法 ThreadHistory，验证按需加载且顺序完整
	page1, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: t1ID})
	if err != nil {
		t.Fatalf("ThreadHistory t1 failed: %v", err)
	}
	if len(page1.Messages) != 3 {
		t.Fatalf("expected 3 messages for t1, got %d", len(page1.Messages))
	}
	if page1.Messages[0].Text != "T1 User 1" ||
		page1.Messages[1].Text != "T1 Assistant 1" ||
		page1.Messages[2].Text != "T1 User 2" {
		t.Errorf("t1 messages text or order mismatch: %+v", page1.Messages)
	}

	// 确认 t1 已标记 loaded，而 t2 仍未 loaded
	backend.mu.Lock()
	if !td1.messagesLoaded {
		backend.mu.Unlock()
		t.Errorf("expected t1 messagesLoaded=true after ThreadHistory")
	}
	if td2.messagesLoaded {
		backend.mu.Unlock()
		t.Errorf("expected t2 messagesLoaded=false still")
	}
	backend.mu.Unlock()

	// 7. 对未加载的线程 t2 追加一条新消息，验证历史与新消息都在
	if err := backend.AppendNotice(t2ID, "System notice for t2"); err != nil {
		t.Fatalf("AppendNotice to t2 failed: %v", err)
	}

	// 读取 t2 消息，断言 2 条历史加 1 条新消息均在且顺序正确
	page2, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: t2ID})
	if err != nil {
		t.Fatalf("ThreadHistory t2 failed: %v", err)
	}
	if len(page2.Messages) != 3 {
		t.Fatalf("expected 3 messages for t2 after append, got %d", len(page2.Messages))
	}
	if page2.Messages[0].Text != "T2 User 1" ||
		page2.Messages[1].Text != "T2 Assistant 1" ||
		page2.Messages[2].Text != "System notice for t2" {
		t.Errorf("t2 messages text or order mismatch: %+v", page2.Messages)
	}
}

// seedPersistStore 打开一个临时 store 并写入 1 个项目与 1 个线程，返回 store 与线程 ID。
func seedPersistStore(t *testing.T, ctx context.Context, name string) (*store.Store, string) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	now := time.Now().UTC()
	projID := "proj-" + name
	if err := s.UpsertProject(ctx, store.Project{
		ID:        projID,
		Name:      "Persist " + name,
		Path:      "/tmp/" + name,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		s.Close()
		t.Fatalf("upsert project failed: %v", err)
	}
	threadID := "thread-" + name
	if err := s.CreateThread(ctx, store.Thread{
		ID:        threadID,
		ProjectID: projID,
		Title:     "Persist " + name,
		Provider:  "memory",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		s.Close()
		t.Fatalf("create thread failed: %v", err)
	}
	return s, threadID
}

func TestOrphanTurnTimingUsesLastActivity(t *testing.T) {
	ctx := context.Background()
	s, threadID := seedPersistStore(t, ctx, "orphan-timing")
	defer s.Close()
	start := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Millisecond)
	toolEnd := start.Add(5 * time.Minute).Format(time.RFC3339Nano)
	invalid := "invalid"
	cases := []struct {
		id       string
		index    int
		messages []store.Message
		want     time.Time
	}{
		{"message", 0, []store.Message{{ID: "m1", Role: "user", CreatedAt: start.Add(time.Minute)}}, start.Add(time.Minute)},
		{"tool", 1, []store.Message{{ID: "m2", Role: "tool", CreatedAt: start.Add(time.Minute), ToolCompletedAt: &toolEnd}}, start.Add(5 * time.Minute)},
		{"empty", 2, nil, start},
		{"invalid", 3, []store.Message{{ID: "m3", Role: "tool", CreatedAt: start.Add(2 * time.Minute), ToolCompletedAt: &invalid}}, start.Add(2 * time.Minute)},
	}
	for _, tc := range cases {
		if err := s.CreateTurn(ctx, store.Turn{ID: tc.id, ThreadID: threadID, TurnIndex: tc.index, Status: "running", StartedAt: start}); err != nil {
			t.Fatal(err)
		}
		for _, msg := range tc.messages {
			msg.ThreadID, msg.TurnID, msg.TurnIndex = threadID, tc.id, tc.index
			if _, err := s.AppendMessage(ctx, msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.CreateTurn(ctx, store.Turn{ID: "future", ThreadID: threadID, TurnIndex: 4, Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(ctx, store.Message{ID: "m4", ThreadID: threadID, Role: "user", TurnID: "future", TurnIndex: 4, CreatedAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	before := time.Now().UTC()
	b.finalizeOrphanTurns(ctx, s, map[string]*memoryThread{})
	after := time.Now().UTC()
	for _, tc := range cases {
		turn, err := s.GetTurn(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if turn.Status != "interrupted" || turn.CompletedAt == nil || !turn.CompletedAt.Equal(tc.want) {
			t.Fatalf("%s: %+v, want %v", tc.id, turn, tc.want)
		}
	}
	future, err := s.GetTurn(ctx, "future")
	if err != nil {
		t.Fatal(err)
	}
	if future.CompletedAt == nil || future.CompletedAt.Before(before) || future.CompletedAt.After(after) {
		t.Fatalf("future completion: %v", future.CompletedAt)
	}
	b.finalizeOrphanTurns(ctx, s, map[string]*memoryThread{})
	again, err := s.GetTurn(ctx, "future")
	if err != nil {
		t.Fatal(err)
	}
	if again.CompletedAt == nil || !again.CompletedAt.Equal(*future.CompletedAt) {
		t.Fatalf("completion changed: %v", again.CompletedAt)
	}
}

func TestTurnTimingHistoryFromStore(t *testing.T) {
	ctx := context.Background()
	s, threadID := seedPersistStore(t, ctx, "turn-history")
	defer s.Close()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	end := start.Add(time.Minute)
	if err := s.CreateTurn(ctx, store.Turn{ID: "history-turn", ThreadID: threadID, TurnIndex: 0, Status: "completed", StartedAt: start, CompletedAt: &end}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.Message{
		{ID: "first", ThreadID: threadID, Role: "user", TurnID: "history-turn", CreatedAt: start},
		{ID: "middle", ThreadID: threadID, Role: "assistant", TurnID: "history-turn", CreatedAt: start.Add(time.Second)},
		{ID: "legacy", ThreadID: threadID, Role: "assistant", CreatedAt: end},
	} {
		if _, err := s.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	limit := 1
	page, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].TurnStatus != nil {
		t.Fatalf("legacy message: %+v", page.Messages)
	}
	page, err = b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID, Limit: &limit, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("middle page: %+v", page.Messages)
	}
	m := page.Messages[0]
	if m.ID != "middle" || m.TurnStatus == nil || *m.TurnStatus != "completed" || m.TurnStartedAt == nil || *m.TurnStartedAt != start.Format("2006-01-02T15:04:05.000Z07:00") || m.TurnCompletedAt == nil || *m.TurnCompletedAt != end.Format("2006-01-02T15:04:05.000Z07:00") {
		t.Fatalf("middle message: %+v", m)
	}
}

// TestPersist_AppendFallbackWhenRowMissing 覆盖 R3-001：内存里标成已持久化、库里却没有这行时，
// 下一轮写穿必须回退为追加，否则该消息在进程内永久不入库。
func TestPersist_AppendFallbackWhenRowMissing(t *testing.T) {
	ctx := context.Background()
	s, threadID := seedPersistStore(t, ctx, "fallback")
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	msg := wire.ThreadMessage{
		ID:        "msg-fallback",
		Role:      "user",
		Text:      "hello",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	backend.mu.Lock()
	td := backend.threads[threadID]
	if td == nil {
		backend.mu.Unlock()
		t.Fatalf("thread %s not restored", threadID)
	}
	td.messagesLoaded = true
	td.messages = []wire.ThreadMessage{msg}
	// 模拟旧实现留下的状态：内存认为已持久化，库里其实没有这行。
	td.persisted[msg.ID] = true
	backend.mu.Unlock()

	backend.persistMessages(threadID, []wire.ThreadMessage{msg})

	page, hasMore, _, err := s.ListMessagesTolerant(ctx, threadID, 0, 100)
	if err != nil {
		t.Fatalf("list messages failed: %v", err)
	}
	if hasMore || len(page) != 1 || page[0].ID != msg.ID {
		t.Fatalf("期望回退追加后库里有 1 条消息，实际 hasMore=%v, msgs=%+v", hasMore, page)
	}
}

// TestPersist_LoadFailureKeepsUnloaded 覆盖 R3-002：读库失败时不置 messagesLoaded，
// 否则一次 store 故障就会让本进程内该会话历史恒为空。
func TestPersist_LoadFailureKeepsUnloaded(t *testing.T) {
	ctx := context.Background()
	s, threadID := seedPersistStore(t, ctx, "loadfail")

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	// 关闭 store 模拟读库失败：b.store 仍指向它，查询会返回错误。
	if err := s.Close(); err != nil {
		t.Fatalf("close store failed: %v", err)
	}

	backend.mu.Lock()
	td := backend.threads[threadID]
	backend.ensureMessagesLoadedLocked(td)
	loaded, evicted := td.messagesLoaded, td.messagesEvicted
	backend.mu.Unlock()
	if loaded {
		t.Error("读库失败时不应置 messagesLoaded=true，否则本进程内历史恒为空")
	}
	if evicted {
		t.Error("读库失败时不应改变 messagesEvicted 状态")
	}
}
