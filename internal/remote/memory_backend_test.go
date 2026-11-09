package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestIsProviderCrashError(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"API 错误 500: out of memory", false},
		{"llm: API error (status 500): process exited", false},
		{"HTTP 500: signal: killed", false},
		{"out of memory", false},
		{"signal: killed", true},
		{"agy process exited with error: signal: killed", true},
	} {
		if got := isProviderCrashError(tc.text); got != tc.want {
			t.Errorf("isProviderCrashError(%q) = %v，期望 %v", tc.text, got, tc.want)
		}
	}
}

func TestTurnTimingProjectionAndHistory(t *testing.T) {
	started := time.Date(2026, 11, 6, 1, 2, 3, 4_000_000, time.UTC)
	completed := started.Add(time.Second)
	turnID := "turn-timing"
	turn := store.Turn{ID: turnID, Status: "starting", StartedAt: started, CompletedAt: &completed}
	msg := wire.ThreadMessage{TurnID: &turnID}
	applyTurnTiming(&msg, map[string]store.Turn{turnID: turn})
	if msg.TurnStatus == nil || *msg.TurnStatus != "running" {
		t.Fatalf("status = %v, want running", msg.TurnStatus)
	}
	if msg.TurnStartedAt == nil || *msg.TurnStartedAt != "2026-11-06T01:02:03.004Z" {
		t.Fatalf("startedAt = %v", msg.TurnStartedAt)
	}
	if msg.TurnCompletedAt == nil || *msg.TurnCompletedAt != "2026-11-06T01:02:04.004Z" {
		t.Fatalf("completedAt = %v", msg.TurnCompletedAt)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	current := started
	b.SetClock(func() time.Time { return current })
	threadID := "timing-thread"
	b.AddThread(wire.ThreadSummary{ID: threadID, Title: "timing"}, nil)
	conn := newMockStreamConn()
	hub.Subscribe(conn, threadID)
	b.mu.Lock()
	td := b.threads[threadID]
	st := b.startTurnLocked(td, "hello")
	if st.userMsg.TurnStartedAt == nil || *st.userMsg.TurnStartedAt != "2026-11-06T01:02:03.004Z" || st.userMsg.TurnStatus == nil || *st.userMsg.TurnStatus != "running" {
		t.Fatalf("start: %+v", st.userMsg)
	}
	td.messages = append(td.messages, wire.ThreadMessage{ID: "late-tool", Role: "tool", TurnID: &st.turnID, TurnIndex: st.turnIndex})
	b.mu.Unlock()
	current = completed
	b.finishTurnEarly(td, threadID, st.turnID, st.turnIndex, true, false)
	current = completed.Add(time.Hour)
	b.updateTurnFinish(threadID, st.turnID, st.turnIndex, "failed", "late")
	limit := 1
	page, err := b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: threadID, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "late-tool" || page.Messages[0].TurnStatus == nil || *page.Messages[0].TurnStatus != "completed" || page.Messages[0].TurnCompletedAt == nil || *page.Messages[0].TurnCompletedAt != "2026-11-06T01:02:04.004Z" {
		t.Fatalf("history: %+v", page.Messages)
	}
	var terminal bool
	for _, event := range conn.getEvents() {
		for _, m := range event.Payload.Messages {
			if m.TurnID != nil && *m.TurnID == st.turnID && m.TurnStatus != nil && *m.TurnStatus == "completed" && m.TurnStartedAt != nil && m.TurnCompletedAt != nil {
				terminal = true
			}
		}
	}
	if !terminal {
		t.Fatal("missing complete terminal projection")
	}
}

func TestTurnTimingQueueBoundary(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	start := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return start })
	threadID := "timing-queue"
	b.AddThread(wire.ThreadSummary{ID: threadID, Title: "queue"}, nil)
	conn := newMockStreamConn()
	hub.Subscribe(conn, threadID)
	b.mu.Lock()
	td := b.threads[threadID]
	first := b.startTurnLocked(td, "first")
	td.followUps = []wire.FollowUp{{ID: "queued", Text: "second"}}
	b.mu.Unlock()
	hub.BeginRun(threadID)
	hub.Publish(threadID, []wire.ThreadMessage{first.userMsg}, nil, first.currentSummary)
	hub.Flush(threadID)
	b.finishTurnEarly(td, threadID, first.turnID, first.turnIndex, true, false)
	hub.Flush(threadID)
	events := conn.getEvents()
	terminalIndex, nextIndex := -1, -1
	for i, ev := range events {
		for _, m := range ev.Payload.Messages {
			if m.TurnID == nil || m.TurnStatus == nil {
				continue
			}
			if *m.TurnID == first.turnID && *m.TurnStatus == "completed" && m.TurnCompletedAt != nil {
				terminalIndex = i
			}
			if *m.TurnID != first.turnID && *m.TurnStatus == "running" && m.TurnStartedAt != nil {
				nextIndex = i
			}
		}
	}
	if terminalIndex < 0 || nextIndex <= terminalIndex || events[terminalIndex].RunID == events[nextIndex].RunID {
		t.Fatalf("queue turn boundary: %+v", events)
	}
}

func TestMemoryBackend_NotFoundErrors(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	conn := newMockStreamConn()
	badThreadID := "00000000-0000-0000-0000-000000000404"

	// 1. GetThread 不存在的线程
	_, err := backend.GetThread(ctx, badThreadID)
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound for GetThread, got: %v", err)
	}

	// 2. ThreadHistory 不存在的线程
	_, err = backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: badThreadID})
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound for ThreadHistory, got: %v", err)
	}

	// 3. Subscribe 不存在的线程
	_, err = backend.Subscribe(ctx, conn, badThreadID)
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound for Subscribe, got: %v", err)
	}

	// 4. SendMessage 不存在的线程
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: badThreadID,
		Text:     "hello",
		ActionID: "00000000-0000-0000-0000-000000000001",
	})
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound for SendMessage, got: %v", err)
	}
}

func TestMemoryBackend_SendMessageAndStreaming(t *testing.T) {
	// 聚合间隔设为 10ms，便于异步快速推送
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000010"
	summary := wire.ThreadSummary{
		ID:        threadID,
		ProjectID: "00000000-0000-0000-0000-000000000001",
		Title:     "Echo Thread",
	}
	backend.AddThread(summary, nil)

	conn := newMockStreamConn()
	subRes, err := backend.Subscribe(ctx, conn, threadID)
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}
	if !subRes.Subscribed || subRes.Snapshot == nil {
		t.Fatalf("expected subscribed snapshot, got: %+v", subRes)
	}

	userPrompt := "antigravity test"
	actionID := "00000000-0000-0000-0000-000000000099"

	sendRes, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     userPrompt,
		ActionID: actionID,
	})
	if err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	if sendRes.ActionID != actionID || sendRes.MessageID == nil {
		t.Errorf("unexpected SendMessageResult: %+v", sendRes)
	}

	// 等待最终事件到达（assistant 回复完成且 isRunning == false）
	deadline := time.Now().Add(3 * time.Second)
	var finalEvents []wire.ThreadEvent
	for time.Now().Before(deadline) {
		events := conn.getEvents()
		if len(events) > 0 {
			lastEv := events[len(events)-1]
			if !lastEv.Payload.Thread.IsRunning {
				finalEvents = events
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(finalEvents) == 0 {
		t.Fatalf("timed out waiting for assistant reply completion, current events: %d", len(conn.getEvents()))
	}

	// 验证序列号从 1 开始严格单调加 1 递增
	for i, ev := range finalEvents {
		expectedSeq := i + 1
		if ev.Sequence != expectedSeq {
			t.Errorf("event index %d sequence mismatch: expected %d, got %d", i, expectedSeq, ev.Sequence)
		}
		if ev.Kind != "projection" {
			t.Errorf("expected kind 'projection', got: %s", ev.Kind)
		}
	}

	// 验证在过程中收到了完整 "echo: antigravity test" 回复
	lastEvent := finalEvents[len(finalEvents)-1]
	if lastEvent.Payload.Thread.IsRunning {
		t.Errorf("thread should not be running in final event")
	}

	// 检查内存中最终保存的消息
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("fetch thread history failed: %v", err)
	}
	if len(hist.Messages) != 2 {
		t.Fatalf("expected 2 messages (1 user, 1 assistant), got %d", len(hist.Messages))
	}
	if hist.Messages[0].Role != "user" || hist.Messages[0].Text != userPrompt {
		t.Errorf("unexpected user message: %+v", hist.Messages[0])
	}
	if hist.Messages[1].Role != "assistant" || hist.Messages[1].Text != "echo: "+userPrompt {
		t.Errorf("unexpected assistant message: %+v", hist.Messages[1])
	}
}

func TestMemoryBackend_BasicOperations(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()

	// 1. Capabilities
	caps, err := backend.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capabilities error: %v", err)
	}
	if caps.SchemaVersion != 1 || len(caps.Methods) == 0 || len(caps.Features) == 0 {
		t.Errorf("invalid capabilities: %+v", caps)
	}

	// 2. KanbanBoard
	board, err := backend.KanbanBoard(ctx)
	if err != nil {
		t.Fatalf("kanbanBoard error: %v", err)
	}
	if board.Projects == nil || board.Tasks == nil || board.Goals == nil {
		t.Errorf("kanbanBoard slices should not be nil: %+v", board)
	}

	// 3. Projects
	p1 := wire.ProjectSummary{ID: "p1", Name: "Proj 1"}
	backend.AddProject(p1)
	pList, err := backend.ListProjects(ctx)
	if err != nil || len(pList.Projects) != 1 || pList.Projects[0].ID != "p1" {
		t.Errorf("unexpected ListProjects: %+v, err: %v", pList, err)
	}

	// 4. Threads list & history
	t1 := wire.ThreadSummary{ID: "t1", ProjectID: "p1", Title: "Thread 1", IsArchived: false}
	t2 := wire.ThreadSummary{ID: "t2", ProjectID: "p1", Title: "Thread 2", IsArchived: true}
	msgs := []wire.ThreadMessage{
		{ID: "m1", Text: "1"},
		{ID: "m2", Text: "2"},
		{ID: "m3", Text: "3"},
	}
	backend.AddThread(t1, msgs)
	backend.AddThread(t2, nil)

	// 不包含归档
	tList, err := backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &p1.ID})
	if err != nil {
		t.Fatalf("list threads error: %v", err)
	}
	if len(tList.Threads) != 1 || tList.Threads[0].ID != "t1" {
		t.Errorf("expected only t1, got: %+v", tList.Threads)
	}

	// 包含归档
	incArchived := true
	tListAll, err := backend.ListThreads(ctx, wire.ThreadListParams{
		ProjectID:       &p1.ID,
		IncludeArchived: &incArchived,
	})
	if err != nil || len(tListAll.Threads) != 2 {
		t.Errorf("expected 2 threads including archived, got: %+v", tListAll)
	}

	// 5. History 分页（limit 2）
	lim := 2
	hPage, err := backend.ThreadHistory(ctx, wire.HistoryParams{
		ThreadID: "t1",
		Limit:    &lim,
	})
	if err != nil {
		t.Fatalf("thread history error: %v", err)
	}
	if len(hPage.Messages) != 2 || hPage.Messages[0].ID != "m2" || hPage.Messages[1].ID != "m3" {
		t.Errorf("expected latest 2 messages [m2, m3], got: %+v", hPage.Messages)
	}
	if !hPage.HasMore || hPage.NextCursor == nil {
		t.Errorf("expected hasMore and nextCursor, got: %+v", hPage)
	}

	// 反向翻页拉取更早一页
	hPage2, err := backend.ThreadHistory(ctx, wire.HistoryParams{
		ThreadID: "t1",
		Cursor:   hPage.NextCursor,
		Limit:    &lim,
	})
	if err != nil {
		t.Fatalf("thread history page 2 error: %v", err)
	}
	if len(hPage2.Messages) != 1 || hPage2.Messages[0].ID != "m1" {
		t.Errorf("expected earlier message [m1], got: %+v", hPage2.Messages)
	}
	if hPage2.HasMore {
		t.Errorf("expected no more messages")
	}
}

type mockHarness struct {
	mu        sync.Mutex
	kind      harness.Kind
	runFunc   func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error
	turnCalls []harness.Turn
}

func (m *mockHarness) Kind() harness.Kind {
	if m.kind != "" {
		return m.kind
	}
	return "mock"
}

func (m *mockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	m.mu.Lock()
	m.turnCalls = append(m.turnCalls, turn)
	run := m.runFunc
	m.mu.Unlock()

	if run != nil {
		return run(ctx, turn, emit)
	}
	return nil
}

func (m *mockHarness) getTurnCalls() []harness.Turn {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]harness.Turn, len(m.turnCalls))
	copy(res, m.turnCalls)
	return res
}

func waitThreadIdle(t *testing.T, backend *MemoryBackend, ctx context.Context, threadID string) wire.ThreadSummary {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		th, err := backend.GetThread(ctx, threadID)
		if err == nil && !th.IsRunning {
			return th
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for thread %s to become idle", threadID)
	return wire.ThreadSummary{}
}

func TestMemoryBackend_HarnessEventFlow(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventSession, SessionID: "sess-flow-1"})
			emit(harness.Event{Type: harness.EventReasoning, Text: "深度思考中..."})
			emit(harness.Event{Type: harness.EventText, Text: "第一段正文"})
			emit(harness.Event{Type: harness.EventText, Text: "，继续补充第一段"})
			emit(harness.Event{
				Type:     harness.EventToolStart,
				ToolID:   "call-ls-1",
				ToolName: "list_files",
				Input:    []byte(`{"dir":"."}`),
			})
			emit(harness.Event{
				Type:   harness.EventToolEnd,
				ToolID: "call-ls-1",
				Output: "file1.go\nfile2.go",
			})
			emit(harness.Event{Type: harness.EventText, Text: "第二段正文总结"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace/project", harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000021"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Harness Flow Thread"}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "列出目录文件",
		ActionID: "00000000-0000-0000-0000-000000000201",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	finalSummary := waitThreadIdle(t, backend, ctx, threadID)
	if finalSummary.IsRunning {
		t.Errorf("expected thread IsRunning to be false, got true")
	}

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	if len(hist.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(hist.Messages))
	}

	// 1. 用户消息
	if hist.Messages[0].Role != "user" || hist.Messages[0].Text != "列出目录文件" {
		t.Errorf("msg 0 mismatch: %+v", hist.Messages[0])
	}

	// 2. 思考消息
	if hist.Messages[1].Role != "assistant" || hist.Messages[1].Kind == nil || *hist.Messages[1].Kind != "thinking" || hist.Messages[1].Text != "深度思考中..." {
		t.Errorf("msg 1 mismatch: %+v", hist.Messages[1])
	}

	// 3. 第一条正文消息
	if hist.Messages[2].Role != "assistant" || hist.Messages[2].Kind == nil || *hist.Messages[2].Kind != "assistant" || hist.Messages[2].Text != "第一段正文，继续补充第一段" {
		t.Errorf("msg 2 mismatch: %+v", hist.Messages[2])
	}
	if hist.Messages[2].IsFinalResponse != nil && *hist.Messages[2].IsFinalResponse {
		t.Errorf("msg 2 should not be final response")
	}

	// 4. 工具消息
	if hist.Messages[3].Role != "tool" || hist.Messages[3].Kind == nil || *hist.Messages[3].Kind != "tool" {
		t.Errorf("msg 3 mismatch: %+v", hist.Messages[3])
	}

	// 5. 第二条正文消息
	if hist.Messages[4].Role != "assistant" || hist.Messages[4].Kind == nil || *hist.Messages[4].Kind != "assistant" || hist.Messages[4].Text != "第二段正文总结" {
		t.Errorf("msg 4 mismatch: %+v", hist.Messages[4])
	}
	if hist.Messages[4].IsFinalResponse == nil || !*hist.Messages[4].IsFinalResponse {
		t.Errorf("msg 4 should be final response, got: %v", hist.Messages[4].IsFinalResponse)
	}
}

func TestMemoryBackend_HarnessSessionResume(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	callCount := 0
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			callCount++
			if callCount == 1 {
				emit(harness.Event{Type: harness.EventSession, SessionID: "session-persisted-123"})
				emit(harness.Event{Type: harness.EventText, Text: "第 1 轮回复"})
			} else {
				emit(harness.Event{Type: harness.EventText, Text: "第 2 轮回复"})
			}
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace/project", harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000022"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Harness Resume Thread"}, nil)

	// 第 1 轮
	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "第一轮提问",
		ActionID: "00000000-0000-0000-0000-000000000202",
	})
	if err != nil {
		t.Fatalf("SendMessage round 1 failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	// 第 2 轮
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "第二轮提问",
		ActionID: "00000000-0000-0000-0000-000000000203",
	})
	if err != nil {
		t.Fatalf("SendMessage round 2 failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}
	if calls[0].SessionID != "" {
		t.Errorf("expected empty SessionID on round 1, got %q", calls[0].SessionID)
	}
	if calls[1].SessionID != "session-persisted-123" {
		t.Errorf("expected round 2 SessionID to be 'session-persisted-123', got %q", calls[1].SessionID)
	}
}

func TestMemoryBackend_HarnessRunError(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "尝试执行..."})
			return errors.New("网络连接超时")
		},
	}
	backend.SetHarness(mock, "/workspace/project", harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000023"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Harness Error Thread"}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "开始任务",
		ActionID: "00000000-0000-0000-0000-000000000204",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}

	if len(hist.Messages) != 3 {
		t.Fatalf("expected 3 messages (user, text, error), got %d", len(hist.Messages))
	}

	lastMsg := hist.Messages[2]
	if lastMsg.Role != "assistant" {
		t.Errorf("expected error message role to be assistant, got %s", lastMsg.Role)
	}
	expectedErrText := "出错：网络连接超时"
	if lastMsg.Text != expectedErrText {
		t.Errorf("expected error text %q, got %q", expectedErrText, lastMsg.Text)
	}
}

func TestMemoryBackend_AlreadyRunningRPCError(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	startedCh := make(chan struct{})
	blockCh := make(chan struct{})

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			close(startedCh)
			<-blockCh
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace/project", harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000024"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Harness Busy Thread"}, nil)

	// 第 1 次发送消息，进入运行态并阻塞
	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "长时间运行的任务",
		ActionID: "00000000-0000-0000-0000-000000000205",
	})
	if err != nil {
		t.Fatalf("SendMessage 1 failed: %v", err)
	}

	// 等待 harness 开始执行
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for harness run to start")
	}

	// 在运行中再次发送消息，期望返回 RPCError
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "并发新消息",
		ActionID: "00000000-0000-0000-0000-000000000206",
	})
	if err == nil {
		t.Fatalf("expected error on second SendMessage while running, got nil")
	}

	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *wire.RPCError, got: %T (%v)", err, err)
	}
	if rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Errorf("expected ErrCodeOperationRejected (%d), got: %d", wire.ErrCodeOperationRejected, rpcErr.Code)
	}

	// 释放阻塞通道并等待结束
	close(blockCh)
	waitThreadIdle(t, backend, ctx, threadID)
}

func TestMemoryBackend_ReasoningAfterToolStartsNewMessage(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventReasoning, Text: "想一想"})
			emit(harness.Event{
				Type:     harness.EventToolStart,
				ToolID:   "t1",
				ToolName: "WebSearch",
			})
			emit(harness.Event{
				Type:   harness.EventToolEnd,
				ToolID: "t1",
			})
			emit(harness.Event{Type: harness.EventReasoning, Text: "再想想"})
			emit(harness.Event{Type: harness.EventText, Text: "答案"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace/project", harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000025"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Reasoning After Tool Thread"}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "请帮我搜索并解答",
		ActionID: "00000000-0000-0000-0000-000000000207",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}

	var thinkingMsgs []wire.ThreadMessage
	var toolIdx, secondThinkingIdx, textIdx int = -1, -1, -1

	for idx, msg := range hist.Messages {
		if msg.Kind != nil && *msg.Kind == "thinking" {
			thinkingMsgs = append(thinkingMsgs, msg)
			if len(thinkingMsgs) == 2 {
				secondThinkingIdx = idx
			}
		}
		if msg.Role == "tool" || (msg.Kind != nil && *msg.Kind == "tool") {
			toolIdx = idx
		}
		if msg.Role == "assistant" && (msg.Kind == nil || *msg.Kind == "assistant") {
			textIdx = idx
		}
	}

	if len(thinkingMsgs) != 2 {
		t.Fatalf("expected 2 thinking messages, got %d", len(thinkingMsgs))
	}
	if thinkingMsgs[0].Text != "想一想" {
		t.Errorf("expected first thinking message text to be '想一想', got %q", thinkingMsgs[0].Text)
	}
	if thinkingMsgs[1].Text != "再想想" {
		t.Errorf("expected second thinking message text to be '再想想', got %q", thinkingMsgs[1].Text)
	}
	if toolIdx == -1 || secondThinkingIdx <= toolIdx {
		t.Errorf("expected second thinking message (idx %d) to be after tool message (idx %d)", secondThinkingIdx, toolIdx)
	}
	if textIdx != len(hist.Messages)-1 {
		t.Errorf("expected text message to be at the end, got idx %d (len %d)", textIdx, len(hist.Messages))
	}
}

func TestMemoryBackend_MultiProjectWorkDir(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	defaultWorkDir := t.TempDir()
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{
				Type: harness.EventText,
				Text: "done: " + turn.Prompt,
			})
			return nil
		},
	}
	backend.SetHarness(mock, defaultWorkDir, harness.ModeAgent)

	ctx := context.Background()

	// 添加两个项目
	p1, err := backend.AddProjectPath(ctx, dir1)
	if err != nil {
		t.Fatalf("AddProjectPath dir1: %v", err)
	}
	p2, err := backend.AddProjectPath(ctx, dir2)
	if err != nil {
		t.Fatalf("AddProjectPath dir2: %v", err)
	}

	// 各自创建一个会话
	th1Res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p1.ID})
	if err != nil {
		t.Fatalf("CreateThread p1: %v", err)
	}
	th2Res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p2.ID})
	if err != nil {
		t.Fatalf("CreateThread p2: %v", err)
	}

	// 分别在两个会话中发送消息
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: th1Res.ThreadID,
		Text:     "run in p1",
	})
	if err != nil {
		t.Fatalf("SendMessage th1: %v", err)
	}
	waitThreadIdle(t, backend, ctx, th1Res.ThreadID)

	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: th2Res.ThreadID,
		Text:     "run in p2",
	})
	if err != nil {
		t.Fatalf("SendMessage th2: %v", err)
	}
	waitThreadIdle(t, backend, ctx, th2Res.ThreadID)

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}

	cleanDir1 := filepath.Clean(dir1)
	cleanDir2 := filepath.Clean(dir2)

	if calls[0].WorkDir != cleanDir1 {
		t.Errorf("turn 1 workDir mismatch: got %q, want %q", calls[0].WorkDir, cleanDir1)
	}
	if calls[1].WorkDir != cleanDir2 {
		t.Errorf("turn 2 workDir mismatch: got %q, want %q", calls[1].WorkDir, cleanDir2)
	}

	// 测试：若线程所属项目 Path 为空，回退到 b.harnessWorkDir
	emptyProj := wire.ProjectSummary{
		ID:   "p-empty-path",
		Name: "Empty Path Proj",
		Path: "",
	}
	backend.AddProject(emptyProj)
	thEmptyRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: emptyProj.ID})
	if err != nil {
		t.Fatalf("CreateThread emptyProj: %v", err)
	}

	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thEmptyRes.ThreadID,
		Text:     "run in fallback",
	})
	if err != nil {
		t.Fatalf("SendMessage fallback: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thEmptyRes.ThreadID)

	calls = mock.getTurnCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 harness calls, got %d", len(calls))
	}
	if calls[2].WorkDir != defaultWorkDir {
		t.Errorf("turn 3 workDir mismatch: got %q, want default %q", calls[2].WorkDir, defaultWorkDir)
	}
}

func TestMemoryBackend_AddProjectPath(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	tempDir := t.TempDir()

	// 1. 成功添加已存在目录
	p1, err := backend.AddProjectPath(ctx, tempDir)
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}
	if p1.ID == "" {
		t.Errorf("expected non-empty ID")
	}
	if p1.Name != filepath.Base(tempDir) {
		t.Errorf("expected Name %q, got %q", filepath.Base(tempDir), p1.Name)
	}
	if p1.Path != filepath.Clean(tempDir) {
		t.Errorf("expected Path %q, got %q", filepath.Clean(tempDir), p1.Path)
	}

	// 2. 去重：添加相同路径直接返回已有项目
	p2, err := backend.AddProjectPath(ctx, tempDir)
	if err != nil {
		t.Fatalf("AddProjectPath second time failed: %v", err)
	}
	if p2.ID != p1.ID {
		t.Errorf("expected deduplicated project ID %q, got %q", p1.ID, p2.ID)
	}

	// 3. 拒绝不存在的路径
	nonExistent := filepath.Join(tempDir, "does-not-exist-dir")
	_, err = backend.AddProjectPath(ctx, nonExistent)
	if err == nil {
		t.Errorf("expected error for non-existent path, got nil")
	}

	// 4. 拒绝非目录文件
	filePath := filepath.Join(tempDir, "test-file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err = backend.AddProjectPath(ctx, filePath)
	if err == nil {
		t.Errorf("expected error for regular file, got nil")
	}
}

func TestMemoryBackend_ListProjectsThreadCount(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	d1 := t.TempDir()
	d2 := t.TempDir()

	p1, err := backend.AddProjectPath(ctx, d1)
	if err != nil {
		t.Fatalf("AddProjectPath d1: %v", err)
	}
	p2, err := backend.AddProjectPath(ctx, d2)
	if err != nil {
		t.Fatalf("AddProjectPath d2: %v", err)
	}

	// 初始 ThreadCount 均为 0
	list, err := backend.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range list.Projects {
		if p.ThreadCount != 0 {
			t.Errorf("expected initial ThreadCount 0 for project %s, got %d", p.ID, p.ThreadCount)
		}
	}

	// 在 p1 下创建 2 个会话，在 p2 下创建 1 个会话
	_, _ = backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p1.ID})
	_, _ = backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p1.ID})
	_, _ = backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: p2.ID})

	list, err = backend.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after create: %v", err)
	}
	for _, p := range list.Projects {
		if p.ID == p1.ID && p.ThreadCount != 2 {
			t.Errorf("expected p1 ThreadCount 2, got %d", p.ThreadCount)
		}
		if p.ID == p2.ID && p.ThreadCount != 1 {
			t.Errorf("expected p2 ThreadCount 1, got %d", p.ThreadCount)
		}
	}
}

func TestMemoryBackend_TurnIndexSemanticConsecutiveTurns(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "reply to " + turn.Prompt})
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	ctx := context.Background()
	thID := "thread-turn-index-test"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID,
		Title:    "Turn Index Test",
		Provider: "mock",
	}, nil)

	// 第 1 轮
	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "prompt round 1",
		ActionID: "act-turn-1",
	})
	if err != nil {
		t.Fatalf("send message round 1: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thID)

	// 第 2 轮
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "prompt round 2",
		ActionID: "act-turn-2",
	})
	if err != nil {
		t.Fatalf("send message round 2: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thID)

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: thID})
	if err != nil {
		t.Fatalf("ThreadHistory: %v", err)
	}

	if len(hist.Messages) != 4 {
		t.Fatalf("expected 4 messages across 2 turns, got %d", len(hist.Messages))
	}

	startTurnIndex := 0

	// 连续两轮之后：
	// 第一轮的用户消息和助手消息 TurnIndex 都等于起始值（0）
	m0, m1 := hist.Messages[0], hist.Messages[1]
	if m0.Role != "user" || m0.TurnIndex != startTurnIndex {
		t.Errorf("turn 1 user message TurnIndex mismatch: got %d, want %d", m0.TurnIndex, startTurnIndex)
	}
	if m1.Role != "assistant" || m1.TurnIndex != startTurnIndex {
		t.Errorf("turn 1 assistant message TurnIndex mismatch: got %d, want %d", m1.TurnIndex, startTurnIndex)
	}
	if m0.TurnID == nil || m1.TurnID == nil || *m0.TurnID != *m1.TurnID {
		t.Errorf("turn 1 user and assistant TurnID mismatch: %v vs %v", m0.TurnID, m1.TurnID)
	}

	// 第二轮的用户消息和助手消息 TurnIndex 都等于起始值加 1（1）
	m2, m3 := hist.Messages[2], hist.Messages[3]
	if m2.Role != "user" || m2.TurnIndex != startTurnIndex+1 {
		t.Errorf("turn 2 user message TurnIndex mismatch: got %d, want %d", m2.TurnIndex, startTurnIndex+1)
	}
	if m3.Role != "assistant" || m3.TurnIndex != startTurnIndex+1 {
		t.Errorf("turn 2 assistant message TurnIndex mismatch: got %d, want %d", m3.TurnIndex, startTurnIndex+1)
	}
	if m2.TurnID == nil || m3.TurnID == nil || *m2.TurnID != *m3.TurnID {
		t.Errorf("turn 2 user and assistant TurnID mismatch: %v vs %v", m2.TurnID, m3.TurnID)
	}

	if *m0.TurnID == *m2.TurnID {
		t.Errorf("turn 1 and turn 2 TurnID should be distinct")
	}
}

func TestMemoryBackend_UpdateThreadRunningOptions(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		effort   string
		invalid  bool
	}{
		{name: "effort", effort: "high"},
		{name: "invalid effort", effort: "invalid", invalid: true},
		{name: "model", model: DefaultModel("codex")},
		{name: "provider", provider: "claude", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			backend := NewMemoryBackend(hub)
			defer backend.Close()
			st, err := store.Open(filepath.Join(t.TempDir(), "thread.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := backend.AttachStore(ctx, st); err != nil {
				t.Fatal(err)
			}
			backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "Project", Path: t.TempDir()})
			created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
			if err != nil {
				t.Fatal(err)
			}
			backend.mu.Lock()
			backend.threads[created.ThreadID].summary.IsRunning = true
			backend.threads[created.ThreadID].summary.Provider = "codex"
			backend.threads[created.ThreadID].summary.Model = nil
			backend.mu.Unlock()
			params := wire.ThreadUpdateParams{ThreadID: created.ThreadID}
			if tc.provider != "" {
				params.Provider = &tc.provider
			}
			if tc.model != "" {
				params.Model = &tc.model
			}
			if tc.effort != "" {
				params.Effort = &tc.effort
			}
			var changed wire.ThreadsChanged
			backend.SetThreadsChangedHook(func(event wire.ThreadsChanged) { changed = event })
			result, err := backend.UpdateThread(ctx, params)
			if tc.invalid {
				var rpcErr *wire.RPCError
				if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
					t.Fatalf("expected InvalidParams, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.model != "" && derefString(result.Thread.Model) != tc.model {
				t.Fatalf("model = %v", result.Thread.Model)
			}
			if tc.effort != "" {
				if derefString(result.Thread.Effort) != tc.effort {
					t.Fatalf("effort = %v", result.Thread.Effort)
				}
				stored, err := st.GetThread(ctx, created.ThreadID)
				if err != nil || stored.Effort != tc.effort {
					t.Fatalf("stored effort = %q, error = %v", stored.Effort, err)
				}
			}
			if len(changed.Threads) != 1 || changed.Threads[0].ID != created.ThreadID {
				t.Fatalf("threads.changed = %+v", changed)
			}
		})
	}
}

func TestMemoryBackend_UpdateThreadPinnedAndArchived(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	backend := NewMemoryBackend(hub)

	// 打开真实 store 验证持久化写穿
	dbPath := filepath.Join(t.TempDir(), "remote_pin_archive.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	p := wire.ProjectSummary{ID: "p1", Name: "Project 1"}
	backend.AddProject(p)

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	// 1. 设置置顶
	pinned := true
	upRes, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: thID,
		Pinned:   &pinned,
	})
	if err != nil {
		t.Fatalf("UpdateThread pinned=true: %v", err)
	}
	if !upRes.Thread.IsPinned {
		t.Fatalf("expected IsPinned=true, got false")
	}

	// 验证 store 中也写穿了
	storeTh, err := st.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("st.GetThread: %v", err)
	}
	if !storeTh.Pinned {
		t.Fatalf("expected storeTh.Pinned=true, got false")
	}

	// 2. 取消置顶
	notPinned := false
	upRes, err = backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: thID,
		Pinned:   &notPinned,
	})
	if err != nil {
		t.Fatalf("UpdateThread pinned=false: %v", err)
	}
	if upRes.Thread.IsPinned {
		t.Fatalf("expected IsPinned=false, got true")
	}

	// 3. 设置归档
	archived := true
	upRes, err = backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: thID,
		Archived: &archived,
	})
	if err != nil {
		t.Fatalf("UpdateThread archived=true: %v", err)
	}
	if !upRes.Thread.IsArchived {
		t.Fatalf("expected IsArchived=true, got false")
	}

	// ListThreads 默认不包含归档
	listRes, err := backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &p.ID})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(listRes.Threads) != 0 {
		t.Fatalf("expected 0 threads without includeArchived, got %d", len(listRes.Threads))
	}

	// 验证重新 AttachStore 读回 Pinned 与 Archived
	backend2 := NewMemoryBackend(hub)
	if err := backend2.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 2: %v", err)
	}
	th2, err := backend2.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("backend2.GetThread: %v", err)
	}
	if !th2.IsArchived || th2.IsPinned {
		t.Fatalf("backend2 summary mismatch: %+v", th2)
	}
}

func TestMemoryBackend_DeleteThread(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	backend := NewMemoryBackend(hub)

	dbPath := filepath.Join(t.TempDir(), "remote_delete_thread.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	p := wire.ProjectSummary{ID: "p1", Name: "Project 1"}
	backend.AddProject(p)

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	var changedPayload wire.ThreadsChanged
	var changedCalled bool
	backend.SetThreadsChangedHook(func(tc wire.ThreadsChanged) {
		changedCalled = true
		changedPayload = tc
	})

	// 模拟线程处于运行中状态
	backend.mu.Lock()
	td := backend.threads[thID]
	td.summary.IsRunning = true
	td.runTurnID = "turn-active-1"
	backend.mu.Unlock()

	// 调用 DeleteThread
	if err := backend.DeleteThread(ctx, thID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}

	// 验证通知已发出
	if !changedCalled {
		t.Fatalf("expected threadsChangedHook to be called")
	}
	if len(changedPayload.RemovedThreadIDs) != 1 || changedPayload.RemovedThreadIDs[0] != thID {
		t.Fatalf("expected RemovedThreadIDs to contain %s, got %+v", thID, changedPayload.RemovedThreadIDs)
	}

	// 验证内存已被删除
	if _, err := backend.GetThread(ctx, thID); err == nil {
		t.Fatalf("expected GetThread to fail after delete")
	}

	// 验证 store 中也已被删除
	if _, err := st.GetThread(ctx, thID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected store.ErrNotFound, got %v", err)
	}

	// 再次删除不存在的线程返回 ErrNotFound
	if err := backend.DeleteThread(ctx, thID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existing thread, got %v", err)
	}
}

func TestMemoryBackend_RemoveProject(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	backend := NewMemoryBackend(hub)

	dbPath := filepath.Join(t.TempDir(), "remote_remove_proj.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	p := wire.ProjectSummary{ID: "p1", Name: "Project 1"}
	backend.AddProject(p)

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	var changedPayload wire.ThreadsChanged
	var changedCalled bool
	backend.SetThreadsChangedHook(func(tc wire.ThreadsChanged) {
		changedCalled = true
		changedPayload = tc
	})

	// 调用 RemoveProject
	if err := backend.RemoveProject(ctx, "p1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}

	// 项目下的会话被级联删除后也必须广播 threads.changed，否则远程客户端列表会残留。
	if !changedCalled {
		t.Fatalf("expected threadsChangedHook to be called")
	}
	if len(changedPayload.RemovedThreadIDs) != 1 || changedPayload.RemovedThreadIDs[0] != thID {
		t.Fatalf("expected RemovedThreadIDs to contain %s, got %+v", thID, changedPayload.RemovedThreadIDs)
	}

	// 验证项目在内存中已移除
	pList, err := backend.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, proj := range pList.Projects {
		if proj.ID == "p1" {
			t.Fatalf("expected project p1 to be removed from projects list")
		}
	}

	// 项目删除时 store 会在同一事务里级联删除项目下的会话；内存中的会话也要同步消失，
	// 否则它们会以已删除项目的分组留在侧边栏里。
	if _, err := st.GetThread(ctx, thID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected thread to be deleted with its project, got %v", err)
	}
	if _, err := backend.GetThread(ctx, thID); err == nil {
		t.Fatalf("expected thread %s to be removed from memory", thID)
	}

	listRes, err := backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, th := range listRes.Threads {
		if th.ID == thID {
			t.Fatalf("expected thread %s to be absent from thread list", thID)
		}
	}

	// 再次移除返回 ErrNotFound
	if err := backend.RemoveProject(ctx, "p1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on repeat remove, got %v", err)
	}
}

func TestMemoryBackend_SetThreadRelation_ListThreads(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	parentID := "thread-parent-1"
	childID := "thread-child-1"

	backend.AddThread(wire.ThreadSummary{
		ID:        parentID,
		ProjectID: "proj-rel",
		Title:     "Parent Thread",
	}, nil)

	backend.AddThread(wire.ThreadSummary{
		ID:        childID,
		ProjectID: "proj-rel",
		Title:     "Child Helper Thread",
	}, nil)

	// 设置父子及 helper 关系
	backend.SetThreadRelation(childID, parentID, true)

	// 验证 ListThreads 返回的摘要包含 parentThreadID 与 isHelper
	listRes, err := backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}

	var foundChild bool
	for _, th := range listRes.Threads {
		if th.ID == childID {
			foundChild = true
			if th.ParentThreadID == nil || *th.ParentThreadID != parentID {
				t.Errorf("expected ParentThreadID to be %q, got %v", parentID, th.ParentThreadID)
			}
			if !th.IsHelper {
				t.Errorf("expected IsHelper to be true, got %v", th.IsHelper)
			}
		}
	}
	if !foundChild {
		t.Fatalf("child thread %s not found in ListThreads response", childID)
	}

	// 验证 GetThread 也包含这两个字段
	childTh, err := backend.GetThread(ctx, childID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if childTh.ParentThreadID == nil || *childTh.ParentThreadID != parentID {
		t.Errorf("GetThread: expected ParentThreadID %q, got %v", parentID, childTh.ParentThreadID)
	}
	if !childTh.IsHelper {
		t.Errorf("GetThread: expected IsHelper true, got %v", childTh.IsHelper)
	}
}

// 子会话运行中建立父子关系时，父会话收到的摘要必须是父会话自己的（IsRunning 保持 false）。
func TestMemoryBackend_SetThreadRelationPublishesParentOwnSummary(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	parentID := "thread-parent-rel-1"
	childID := "thread-child-rel-1"
	backend.AddThread(wire.ThreadSummary{ID: parentID, ProjectID: "proj-rel", Title: "Parent"}, nil)
	backend.AddThread(wire.ThreadSummary{ID: childID, ProjectID: "proj-rel", Title: "Child"}, nil)

	// 子会话处于运行中，父会话空闲。
	backend.mu.Lock()
	backend.threads[childID].summary.IsRunning = true
	// 运行中必须伴随真实的运行实例（runTurnID）：摘要只以真实运行实例为准。
	backend.threads[childID].runTurnID = "turn-child-running"
	backend.mu.Unlock()

	var mu sync.Mutex
	published := map[string][]wire.ThreadSummary{}
	backend.hub.AddObserver(func(threadID, method string, params any) {
		if method != "thread.summary" {
			return
		}
		sum, ok := params.(wire.ThreadSummary)
		if !ok {
			return
		}
		mu.Lock()
		published[threadID] = append(published[threadID], sum)
		mu.Unlock()
	})

	backend.SetThreadRelation(childID, parentID, false)

	mu.Lock()
	parentSummaries := append([]wire.ThreadSummary(nil), published[parentID]...)
	childSummaries := append([]wire.ThreadSummary(nil), published[childID]...)
	mu.Unlock()

	if len(parentSummaries) == 0 {
		t.Fatalf("expected a summary publish for parent %s", parentID)
	}
	for _, sum := range parentSummaries {
		if sum.ID != parentID {
			t.Fatalf("parent publish carried summary of %s, want %s", sum.ID, parentID)
		}
		if sum.IsRunning {
			t.Fatalf("expected parent summary IsRunning=false, got true")
		}
	}
	if len(childSummaries) == 0 {
		t.Fatalf("expected a summary publish for child %s", childID)
	}

	parentTh, err := backend.GetThread(context.Background(), parentID)
	if err != nil {
		t.Fatalf("GetThread parent failed: %v", err)
	}
	if parentTh.IsRunning {
		t.Fatalf("expected parent thread to stay idle, got IsRunning=true")
	}
	childTh, err := backend.GetThread(context.Background(), childID)
	if err != nil {
		t.Fatalf("GetThread child failed: %v", err)
	}
	if !childTh.IsRunning {
		t.Fatalf("expected child thread to stay running, got IsRunning=false")
	}
}

// 运行中 sessionEpoch 递增（等价于切换 Provider）导致轮末早退后，线程最终必须回到空闲。
func TestMemoryBackend_TurnEndEpochChangeRestoresIdle(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventSession, SessionID: "sess-epoch-1"})
			emit(harness.Event{Type: harness.EventText, Text: "处理中"})
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000e1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Epoch Thread"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "运行中切换 Provider",
		ActionID: "00000000-0000-0000-0000-000000000e01",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for harness run to start")
	}

	// 模拟运行中切换 Provider：UpdateThread 在 providerChanged 时递增 sessionEpoch，本轮随之失效。
	backend.mu.Lock()
	backend.threads[threadID].sessionEpoch++
	backend.mu.Unlock()

	close(release)

	finalSummary := waitThreadIdle(t, backend, ctx, threadID)
	if finalSummary.IsRunning {
		t.Fatalf("expected thread IsRunning=false after epoch change, got true")
	}
}

func TestMemoryBackend_RunHarnessCancelledTurnRecoversIdle(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000b1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Cancelled Turn Thread"}, nil)

	turnID := "00000000-0000-0000-0000-0000000000b2"
	// 模拟 startTurnLocked 之后、harness 尚未启动时本轮被取消：runTurnID 仍指向本轮。
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.summary.IsRunning = true
	td.phase = phaseRunning
	td.runTurnID = turnID
	backend.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	cancel()
	backend.runHarness(runCtx, threadID, turnID, "被打断的轮次", 0, nil, nil, "", "")

	finalSummary := waitThreadIdle(t, backend, ctx, threadID)
	if finalSummary.IsRunning {
		t.Fatalf("expected thread IsRunning=false after cancelled run, got true")
	}
	if finalSummary.Attention != "ready" {
		t.Fatalf("expected attention ready for interrupted turn, got %q", finalSummary.Attention)
	}

	backend.mu.Lock()
	runTurnID := td.runTurnID
	phase := td.phase
	backend.mu.Unlock()
	if runTurnID != "" {
		t.Fatalf("expected runTurnID to be cleared, got %q", runTurnID)
	}
	if phase != phaseIdle {
		t.Fatalf("expected phase to be phaseIdle, got %v", phase)
	}
}

func TestMemoryBackend_RunHarnessMissingHarnessRecoversIdle(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000a1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Missing Harness Thread"}, nil)

	turnID := "00000000-0000-0000-0000-0000000000a2"
	// 模拟 startTurnLocked 之后的状态：本轮已登记且线程处于运行中，但没有任何 harness 可用。
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.summary.IsRunning = true
	td.phase = phaseRunning
	td.runTurnID = turnID
	backend.mu.Unlock()

	// 未设置任何 harness 时 threadHarnessLocked 返回 nil，runHarness 走 h == nil 分支。
	backend.runHarness(ctx, threadID, turnID, "无人接管的轮次", 0, nil, nil, "", "")

	finalSummary := waitThreadIdle(t, backend, ctx, threadID)
	if finalSummary.IsRunning {
		t.Fatalf("expected thread IsRunning=false after missing harness, got true")
	}

	backend.mu.Lock()
	runTurnID := td.runTurnID
	phase := td.phase
	backend.mu.Unlock()
	if runTurnID != "" {
		t.Fatalf("expected runTurnID to be cleared, got %q", runTurnID)
	}
	if phase != phaseIdle {
		t.Fatalf("expected phase to be phaseIdle, got %v", phase)
	}
}

// TestThreadHarnessLocked_UnknownProviderUnavailable 覆盖 R3-006：未注册的 provider
// （供应商被关闭、自定义端点被删）不得回落到默认厂商，否则该会话的历史与本轮内容会发给另一家；
// 空 provider 与内存演示会话（memory）仍走默认。
func TestThreadHarnessLocked_UnknownProviderUnavailable(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mock := &mockHarness{}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	backend.mu.Lock()
	defer backend.mu.Unlock()

	if h := backend.threadHarnessLocked("mock"); h != mock {
		t.Errorf("已注册 provider 应返回对应 harness，得到 %#v", h)
	}
	for _, provider := range []string{"", "memory"} {
		if h := backend.threadHarnessLocked(provider); h != mock {
			t.Errorf("provider %q 应回落到默认 harness，得到 %#v", provider, h)
		}
	}
	unknown := backend.threadHarnessLocked("claude")
	unavailable, ok := unknown.(unavailableCustomHarness)
	if !ok {
		t.Fatalf("未注册 provider 应返回 unavailableCustomHarness，得到 %#v", unknown)
	}
	if unavailable.Kind() != harness.Kind("claude") {
		t.Errorf("不可用 harness 的 Kind 应为原 provider，得到 %q", unavailable.Kind())
	}
	if err := unknown.Run(context.Background(), harness.Turn{}, func(harness.Event) {}); err == nil {
		t.Error("不可用 harness 的 Run 应返回错误，而不是静默降级")
	}
}

// 摘要必须带上最近一轮的上下文占用与成本：threads.list / threads.get / threads.changed
// 都用 summaryForPublishLocked 的结果，所以这里直接断言该出口的字段计算与填充。
// 窗口上限优先用 CLI 上报值，缺失时回退静态模型表；没有用量时三个字段保持 0（omitempty 不输出）。
func TestMemoryBackend_SummaryContextAndCost(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetDataDir(t.TempDir())

	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "proj", Path: t.TempDir()})
	created, err := backend.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("create thread failed: %v", err)
	}
	threadID := created.ThreadID

	backend.mu.Lock()
	td := backend.threads[threadID]
	backend.mu.Unlock()
	if td == nil {
		t.Fatalf("thread %s not registered", threadID)
	}

	backend.mu.Lock()
	empty := backend.summaryForPublishLocked(td)
	backend.mu.Unlock()
	if empty.ContextUsedTokens != 0 || empty.ContextWindow != 0 || empty.LastTurnCostUSD != 0 {
		t.Fatalf("无用量时摘要不应带用量字段，得到 used=%d window=%d cost=%v",
			empty.ContextUsedTokens, empty.ContextWindow, empty.LastTurnCostUSD)
	}

	// 两轮用量：上下文占用取最近一轮（turnIndex 最大）的 ContextTokens；费用也只取最近一轮，
	// 因为 CLI 的 total_cost_usd 可能已是整次会话的累计值，逐轮相加会重复计费。
	backend.mu.Lock()
	backend.usage = map[string]map[int]harness.Usage{
		threadID: {
			0: {InputTokens: 1200, OutputTokens: 300, CostUSD: 0.02},
			1: {InputTokens: 5000, OutputTokens: 800, ContextTokens: 4100, ContextWindow: 200000, CostUSD: 0.05},
		},
	}
	summary := backend.summaryForPublishLocked(td)
	backend.mu.Unlock()
	if summary.ContextUsedTokens != 4100 {
		t.Errorf("expected contextUsedTokens 4100 from latest turn, got %d", summary.ContextUsedTokens)
	}
	if summary.ContextWindow != 200000 {
		t.Errorf("expected contextWindow 200000 from CLI usage, got %d", summary.ContextWindow)
	}
	if summary.LastTurnCostUSD < 0.049 || summary.LastTurnCostUSD > 0.051 {
		t.Errorf("expected lastTurnCostUSD 0.05 from latest turn, got %v", summary.LastTurnCostUSD)
	}

	// 读路径（threads.get）同样带上这三个字段。
	got, err := backend.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("get thread failed: %v", err)
	}
	if got.ContextUsedTokens != 4100 || got.ContextWindow != 200000 {
		t.Errorf("threads.get summary 缺少用量字段，得到 used=%d window=%d", got.ContextUsedTokens, got.ContextWindow)
	}
	if got.LastTurnCostUSD < 0.049 || got.LastTurnCostUSD > 0.051 {
		t.Errorf("threads.get summary 缺少本轮费用，得到 %v", got.LastTurnCostUSD)
	}

	// CLI 既没上报单次请求大小也没上报窗口上限：回退「输入 + 输出」与静态模型表。
	backend.mu.Lock()
	td.summary.Provider = "claude"
	td.summary.Model = strPtr("claude-sonnet-4-5")
	backend.usage[threadID] = map[int]harness.Usage{2: {InputTokens: 1000, OutputTokens: 250}}
	fallback := backend.summaryForPublishLocked(td)
	backend.mu.Unlock()
	if fallback.ContextUsedTokens != 1250 {
		t.Errorf("expected fallback contextUsedTokens 1250 (input+output), got %d", fallback.ContextUsedTokens)
	}
	if want := int64(fallbackContextLimit("claude", "claude-sonnet-4-5")); fallback.ContextWindow != want {
		t.Errorf("expected fallback contextWindow %d, got %d", want, fallback.ContextWindow)
	}
}
