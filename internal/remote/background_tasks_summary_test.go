package remote

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// waitBackgroundTasks 轮询等待会话摘要里的 BackgroundTasks 计数变为 want。
// 事件由运行中的 mock harness 异步写入，摘要计数也跟着事件走，所以这里按截止时间轮询。
func waitBackgroundTasks(t *testing.T, backend *MemoryBackend, ctx context.Context, threadID string, want int) wire.ThreadSummary {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last wire.ThreadSummary
	for time.Now().Before(deadline) {
		th, err := backend.GetThread(ctx, threadID)
		if err == nil {
			last = th
			if th.BackgroundTasks == want {
				return th
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for thread %s backgroundTasks=%d, last=%d", threadID, want, last.BackgroundTasks)
	return wire.ThreadSummary{}
}

// 会话摘要的 BackgroundTasks：provider 托管的后台任务进入 background 后计为 1，
// task_notification 落定后回到 0。
func TestMemoryBackend_BackgroundTasksSummary(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000fb"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_count_1", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60","run_in_background":true}`)})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "toolu_bg_count_1", Output: "Command running in background"})
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_bg_count_1", TaskID: "task-count-1"})
			close(started)
			<-release
			emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "toolu_bg_count_1", TaskID: "task-count-1", TaskStatus: "completed"})
			emit(harness.Event{Type: harness.EventText, Text: "后台任务完成后的续跑"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "后台跑一下",
		ActionID: "00000000-0000-0000-0000-0000000000fc",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-started
	waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	if th := waitBackgroundTasks(t, backend, ctx, threadID, 1); th.BackgroundTasks != 1 {
		t.Fatalf("background 工具的会话摘要计数 = %d, want 1", th.BackgroundTasks)
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)
	waitToolStatus(t, backend, ctx, threadID, "Bash", "completed")
	if th := waitBackgroundTasks(t, backend, ctx, threadID, 0); th.BackgroundTasks != 0 {
		t.Fatalf("后台任务落定后的会话摘要计数 = %d, want 0", th.BackgroundTasks)
	}
}

// 会话摘要的 BackgroundTasks：回合内的 running 工具不算后台，回合结束后仍在 running 的工具才计入。
func TestMemoryBackend_BackgroundTasksCountsRunningToolOnlyWhenIdle(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000fd"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_count_2", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60"}`)})
			close(started)
			<-release
			// 不补 tool_end：回合结束时命令仍在跑，应作为后台任务继续计数。
			emit(harness.Event{Type: harness.EventText, Text: "命令还在跑"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "跑一个长命令",
		ActionID: "00000000-0000-0000-0000-0000000000fe",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-started
	waitToolStatus(t, backend, ctx, threadID, "Bash", "running")
	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !th.IsRunning {
		t.Fatalf("expected thread to still be running before release")
	}
	if th.BackgroundTasks != 0 {
		t.Fatalf("回合内的 running 工具不应计入后台任务, got %d", th.BackgroundTasks)
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)
	if th := waitBackgroundTasks(t, backend, ctx, threadID, 1); th.BackgroundTasks != 1 {
		t.Fatalf("回合结束后仍在 running 的工具应计入后台任务, got %d", th.BackgroundTasks)
	}
}

// 启动恢复：background 工具被收敛为 failed 后，摘要的后台任务计数不能停留在旧值。
// 计数缓存的匹配键只看消息集合，工具状态在原消息上改写，必须由改写处显式失效。
func TestMemoryBackend_BackgroundRecoveryInvalidatesCount(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "background_recovery.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetDataDir(t.TempDir())
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	projectID := "00000000-0000-0000-0000-0000000000a1"
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "项目", Path: t.TempDir()})
	threadID := "00000000-0000-0000-0000-0000000000ff"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "后台任务", ProjectID: projectID}, nil)

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_recovery_1", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60","run_in_background":true}`)})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "toolu_bg_recovery_1", Output: "Command running in background"})
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_bg_recovery_1", TaskID: "task-recovery-1"})
			close(started)
			<-release
			// 模拟宿主在后台任务结束前退出：不发 task_notification，状态停在 background。
			emit(harness.Event{Type: harness.EventText, Text: "后台任务还在跑"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "后台跑一下",
		ActionID: "00000000-0000-0000-0000-000000000100",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-started
	waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	close(release)
	waitThreadIdle(t, backend, ctx, threadID)

	// 先读一次摘要，让计数缓存按 background 状态生效。
	if th := waitBackgroundTasks(t, backend, ctx, threadID, 1); th.BackgroundTasks != 1 {
		t.Fatalf("恢复前的后台任务计数 = %d, want 1", th.BackgroundTasks)
	}

	backend.mu.Lock()
	threads := make(map[string]*memoryThread, len(backend.threads))
	for id, td := range backend.threads {
		threads[id] = td
	}
	backend.mu.Unlock()
	// 直接走启动恢复里收敛 background 工具的那段逻辑。
	backend.finalizeBackgroundToolMessages(ctx, s, threads, time.Now().UTC(), backgroundToolNotice)

	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.BackgroundTasks != 0 {
		t.Fatalf("启动恢复后后台任务计数 = %d, want 0", th.BackgroundTasks)
	}
}

// 后台任务计数在 0 与非 0 之间切换时，StreamHub 要立即通知观察者，侧边栏的「后台」标记才能及时出现与消失。
func TestStreamHub_PublishNotifiesOnBackgroundTasksToggle(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 50 * time.Millisecond})
	defer hub.Close()

	var mu sync.Mutex
	var events []wire.ThreadSummary
	stop := hub.AddObserver(func(threadID, method string, params any) {
		if method != "thread.summary" {
			return
		}
		summary, ok := params.(wire.ThreadSummary)
		if !ok {
			return
		}
		mu.Lock()
		events = append(events, summary)
		mu.Unlock()
	})
	defer stop()

	idle := wire.ThreadSummary{ID: "bg-1", Title: "后台任务"}
	hub.Publish("bg-1", nil, nil, idle)

	waiting := idle
	waiting.BackgroundTasks = 1
	hub.Publish("bg-1", nil, nil, waiting)

	hub.Publish("bg-1", nil, nil, idle)

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 3 {
		t.Fatalf("expected at least 3 summary events, got %d: %+v", len(events), events)
	}
	if events[0].BackgroundTasks != 0 || events[1].BackgroundTasks != 1 || events[2].BackgroundTasks != 0 {
		t.Fatalf("unexpected backgroundTasks sequence: %+v", events[:3])
	}
}

// TestFinalizeThreadBackgroundTasksClearsCount 验证常驻进程退出收尾：
// 调用 finalizeThreadBackgroundTasks 后，会话后台任务计数归零，消息状态收敛为 failed，
// 正文包含 residentExitToolNotice，其他会话不受影响。
func TestFinalizeThreadBackgroundTasksClearsCount(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test_bg_exit.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetDataDir(t.TempDir())
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	projectID := "00000000-0000-0000-0000-0000000000a1"
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "项目", Path: t.TempDir()})

	threadIDA := "00000000-0000-0000-0000-0000000000a2"
	threadIDB := "00000000-0000-0000-0000-0000000000b2"
	backend.AddThread(wire.ThreadSummary{ID: threadIDA, Title: "会话A", ProjectID: projectID}, nil)
	backend.AddThread(wire.ThreadSummary{ID: threadIDB, Title: "会话B", ProjectID: projectID}, nil)

	now := time.Now().UTC()
	if err := s.CreateTurn(ctx, store.Turn{ID: "turn-a", ThreadID: threadIDA, TurnIndex: 0, Status: "completed", StartedAt: now}); err != nil {
		t.Fatalf("CreateTurn A failed: %v", err)
	}
	if err := s.CreateTurn(ctx, store.Turn{ID: "turn-b", ThreadID: threadIDB, TurnIndex: 0, Status: "completed", StartedAt: now}); err != nil {
		t.Fatalf("CreateTurn B failed: %v", err)
	}

	bgStatusA := "background"
	if _, err := s.AppendMessage(ctx, store.Message{
		ID: "msg-tool-a", ThreadID: threadIDA, TurnID: "turn-a", TurnIndex: 0,
		Role: "tool", Kind: "tool", Text: "命令A", ToolName: strPtr("Bash"),
		ToolStatus: &bgStatusA, CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendMessage A failed: %v", err)
	}

	bgStatusB := "background"
	if _, err := s.AppendMessage(ctx, store.Message{
		ID: "msg-tool-b", ThreadID: threadIDB, TurnID: "turn-b", TurnIndex: 0,
		Role: "tool", Kind: "tool", Text: "命令B", ToolName: strPtr("Bash"),
		ToolStatus: &bgStatusB, CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendMessage B failed: %v", err)
	}

	msgA := wire.ThreadMessage{
		ID: "msg-tool-a", Role: "tool", Kind: strPtr("tool"), Text: "命令A",
		ToolName: strPtr("Bash"), ToolStatus: &bgStatusA, CreatedAt: now.Format(time.RFC3339Nano),
	}
	msgB := wire.ThreadMessage{
		ID: "msg-tool-b", Role: "tool", Kind: strPtr("tool"), Text: "命令B",
		ToolName: strPtr("Bash"), ToolStatus: &bgStatusB, CreatedAt: now.Format(time.RFC3339Nano),
	}

	backend.mu.Lock()
	tdA := backend.threads[threadIDA]
	tdA.messagesLoaded = true
	tdA.messages = []wire.ThreadMessage{msgA}
	tdA.backgroundTools = map[string]string{"toolu_a": "msg-tool-a"}
	tdA.persisted = map[string]bool{"msg-tool-a": true}
	tdA.bgCount.invalidate()

	tdB := backend.threads[threadIDB]
	tdB.messagesLoaded = true
	tdB.messages = []wire.ThreadMessage{msgB}
	tdB.backgroundTools = map[string]string{"toolu_b": "msg-tool-b"}
	tdB.persisted = map[string]bool{"msg-tool-b": true}
	tdB.bgCount.invalidate()
	backend.mu.Unlock()

	// 确认收敛前计数为 1
	if th := waitBackgroundTasks(t, backend, ctx, threadIDA, 1); th.BackgroundTasks != 1 {
		t.Fatalf("收敛前会话A后台任务计数 = %d, want 1", th.BackgroundTasks)
	}
	if th := waitBackgroundTasks(t, backend, ctx, threadIDB, 1); th.BackgroundTasks != 1 {
		t.Fatalf("收敛前会话B后台任务计数 = %d, want 1", th.BackgroundTasks)
	}

	// 执行会话A的常驻退出收尾
	backend.finalizeThreadBackgroundTasks(threadIDA, time.Time{})

	// 会话A计数归零
	if th := waitBackgroundTasks(t, backend, ctx, threadIDA, 0); th.BackgroundTasks != 0 {
		t.Fatalf("收敛后会话A后台任务计数 = %d, want 0", th.BackgroundTasks)
	}

	// 会话A内存消息已收敛
	backend.mu.Lock()
	memMsgA := backend.threads[threadIDA].messages[0]
	bgToolsA := len(backend.threads[threadIDA].backgroundTools)
	backend.mu.Unlock()
	if memMsgA.ToolStatus == nil || *memMsgA.ToolStatus != "failed" || memMsgA.ToolCompletedAt == nil {
		t.Fatalf("内存消息A未收敛为 failed: %+v", memMsgA)
	}
	if !strings.Contains(memMsgA.Text, residentExitToolNotice) {
		t.Fatalf("内存消息A正文未包含 residentExitToolNotice: %q", memMsgA.Text)
	}
	if bgToolsA != 0 {
		t.Fatalf("会话A backgroundTools 期望被清空，剩余 %d", bgToolsA)
	}

	// 会话A SQLite 消息已收敛
	msgsA, _, err := s.ListMessages(ctx, threadIDA, 0, 10)
	if err != nil || len(msgsA) == 0 {
		t.Fatalf("读取 SQLite 消息A失败: %v", err)
	}
	dbMsgA := msgsA[0]
	if dbMsgA.ToolStatus == nil || *dbMsgA.ToolStatus != "failed" || dbMsgA.ToolCompletedAt == nil {
		t.Fatalf("SQLite 消息A未收敛为 failed: %+v", dbMsgA)
	}
	if !strings.Contains(dbMsgA.Text, residentExitToolNotice) {
		t.Fatalf("SQLite 消息A正文未包含 residentExitToolNotice: %q", dbMsgA.Text)
	}

	// 会话B不受影响
	if th := waitBackgroundTasks(t, backend, ctx, threadIDB, 1); th.BackgroundTasks != 1 {
		t.Fatalf("会话B后台任务计数应保持 1, got %d", th.BackgroundTasks)
	}
	backend.mu.Lock()
	memMsgB := backend.threads[threadIDB].messages[0]
	backend.mu.Unlock()
	if memMsgB.ToolStatus == nil || *memMsgB.ToolStatus != "background" {
		t.Fatalf("会话B内存消息不应被改动: %+v", memMsgB)
	}
	if strings.Contains(memMsgB.Text, residentExitToolNotice) {
		t.Fatalf("会话B内存消息正文不应包含 residentExitToolNotice: %q", memMsgB.Text)
	}
	msgsB, _, err := s.ListMessages(ctx, threadIDB, 0, 10)
	if err != nil || len(msgsB) == 0 {
		t.Fatalf("读取 SQLite 消息B失败: %v", err)
	}
	dbMsgB := msgsB[0]
	if dbMsgB.ToolStatus == nil || *dbMsgB.ToolStatus != "background" {
		t.Fatalf("会话B SQLite 消息不应被改动: %+v", dbMsgB)
	}
}

// TestFinalizeThreadBackgroundTasksKeepsReplacementTasks 验证有替代进程时常驻退出收尾：
// 只收敛早于 cutoff 出生的旧任务，晚于 cutoff 的替代进程任务与 backgroundTools 保持不变。
func TestFinalizeThreadBackgroundTasksKeepsReplacementTasks(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetDataDir(t.TempDir())

	projectID := "00000000-0000-0000-0000-0000000000a1"
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "项目", Path: t.TempDir()})

	threadIDA := "00000000-0000-0000-0000-0000000000a2"
	backend.AddThread(wire.ThreadSummary{ID: threadIDA, Title: "会话A", ProjectID: projectID}, nil)

	cutoff := time.Now().UTC()
	oldTime := cutoff.Add(-10 * time.Second)
	newTime := cutoff.Add(10 * time.Second)

	bgStatusOld := "background"
	bgStatusNew := "background"

	msgOld := wire.ThreadMessage{
		ID:         "msg-tool-old",
		Role:       "tool",
		Kind:       strPtr("tool"),
		Text:       "旧后台命令",
		ToolName:   strPtr("Bash"),
		ToolStatus: &bgStatusOld,
		CreatedAt:  oldTime.Format(time.RFC3339Nano),
	}
	msgNew := wire.ThreadMessage{
		ID:         "msg-tool-new",
		Role:       "tool",
		Kind:       strPtr("tool"),
		Text:       "新后台命令",
		ToolName:   strPtr("Bash"),
		ToolStatus: &bgStatusNew,
		CreatedAt:  newTime.Format(time.RFC3339Nano),
	}

	backend.mu.Lock()
	tdA := backend.threads[threadIDA]
	tdA.messagesLoaded = true
	tdA.messages = []wire.ThreadMessage{msgOld, msgNew}
	tdA.backgroundTools = map[string]string{
		"toolu_old": "msg-tool-old",
		"toolu_new": "msg-tool-new",
	}
	tdA.bgCount.invalidate()
	backend.mu.Unlock()

	// 确认收敛前计数为 2
	if th := waitBackgroundTasks(t, backend, ctx, threadIDA, 2); th.BackgroundTasks != 2 {
		t.Fatalf("收敛前后台任务计数 = %d, want 2", th.BackgroundTasks)
	}

	// 传入 cutoff 执行常驻退出收尾
	backend.finalizeThreadBackgroundTasks(threadIDA, cutoff)

	// 断言后台任务计数为 1
	if th := waitBackgroundTasks(t, backend, ctx, threadIDA, 1); th.BackgroundTasks != 1 {
		t.Fatalf("收敛后后台任务计数 = %d, want 1", th.BackgroundTasks)
	}

	backend.mu.Lock()
	memOld := backend.threads[threadIDA].messages[0]
	memNew := backend.threads[threadIDA].messages[1]
	bgTools := backend.threads[threadIDA].backgroundTools
	backend.mu.Unlock()

	// 旧的那条变为 failed，带 residentExitToolNotice
	if memOld.ToolStatus == nil || *memOld.ToolStatus != "failed" || memOld.ToolCompletedAt == nil {
		t.Fatalf("旧后台任务未收敛为 failed: %+v", memOld)
	}
	if !strings.Contains(memOld.Text, residentExitToolNotice) {
		t.Fatalf("旧后台任务未包含 residentExitToolNotice: %q", memOld.Text)
	}

	// 新的那条仍是 background
	if memNew.ToolStatus == nil || *memNew.ToolStatus != "background" || memNew.ToolCompletedAt != nil {
		t.Fatalf("替代进程后台任务不应被改动: %+v", memNew)
	}
	if strings.Contains(memNew.Text, residentExitToolNotice) {
		t.Fatalf("替代进程后台任务不应追加 residentExitToolNotice: %q", memNew.Text)
	}

	// backgroundTools 只剩新那条的条目
	if len(bgTools) != 1 {
		t.Fatalf("backgroundTools 条目数期望 1，实际 %d: %+v", len(bgTools), bgTools)
	}
	if bgTools["toolu_new"] != "msg-tool-new" {
		t.Fatalf("backgroundTools 应保留 toolu_new -> msg-tool-new，实际 %+v", bgTools)
	}
	if _, ok := bgTools["toolu_old"]; ok {
		t.Fatalf("backgroundTools 不应再包含 toolu_old")
	}
}
