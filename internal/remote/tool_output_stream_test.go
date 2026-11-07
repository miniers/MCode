package remote

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// waitToolMessage 轮询等待指定工具名的条目出现，返回该条目当前的正文。
func waitToolMessage(t *testing.T, backend *MemoryBackend, ctx context.Context, threadID, toolName string) wire.ThreadMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
		if err == nil {
			for _, m := range page.Messages {
				if m.Kind != nil && *m.Kind == "tool" && m.ToolName != nil && *m.ToolName == toolName {
					return m
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tool message %q", toolName)
	return wire.ThreadMessage{}
}

// 运行中的增量输出要节流追加到对应工具条目；tool_end 的最终输出覆盖增量；对不上的 ToolID 静默忽略。
func TestMemoryBackend_ToolOutputStreaming(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	// 用可推进的假时钟控制节流窗口，避免测试依赖真实等待。
	var clockMu sync.Mutex
	clockNow := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	backend.SetClock(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clockNow
	})
	advance := func(d time.Duration) {
		clockMu.Lock()
		clockNow = clockNow.Add(d)
		clockMu.Unlock()
	}

	firstBatch := make(chan struct{})
	secondBatch := make(chan struct{})
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	var firstOnce, secondOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondOnce.Do(func() { close(secondRelease) }) }
	defer releaseFirst()
	defer releaseSecond()

	firstInput := []byte(`{"cmd":"make test"}`)
	secondInput := []byte(`{"cmd":"true"}`)
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-stream-1", ToolName: "Bash", Input: firstInput})
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "call-stream-1", Text: "第一行\n"})
			// 同一节流窗口内的片段先合并，不单独下发。
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "call-stream-1", Text: "第二行\n"})
			// 对不上任何条目的片段静默忽略。
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "call-unknown", Text: "孤儿输出\n"})
			close(firstBatch)
			<-firstRelease

			advance(toolOutputFlushInterval + 10*time.Millisecond)
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "call-stream-1", Text: "第三行\n"})
			// 落在新窗口里的残余片段，要等 tool_end 才刷出。
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "call-stream-1", Text: "残留片段\n"})
			close(secondBatch)
			<-secondRelease

			// 最终输出与增量内容一致：覆盖而不是追加，条目里不能出现两遍。
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "call-stream-1", Output: "第一行\n第二行\n第三行\n残留片段\n"})
			// 第二个工具只在运行中推增量、结束时没有 output：已流出的增量要保留。
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-stream-2", ToolName: "BashAlone", Input: secondInput})
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "call-stream-2", Text: "只有增量\n"})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "call-stream-2"})
			emit(harness.Event{Type: harness.EventText, Text: "命令执行完成"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000e1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Tool Output Stream"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "跑一下测试",
		ActionID: "00000000-0000-0000-0000-0000000000e2",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-firstBatch
	base1 := "Bash " + string(firstInput)
	running := waitToolMessage(t, backend, ctx, threadID, "Bash")
	if want := base1 + "\n第一行\n"; running.Text != want {
		t.Fatalf("运行中的条目正文 = %q, want %q", running.Text, want)
	}
	if running.ToolStatus == nil || *running.ToolStatus != "running" {
		t.Fatalf("运行中的条目状态应为 running, got %+v", running.ToolStatus)
	}

	releaseFirst()
	<-secondBatch
	streamed := waitToolMessage(t, backend, ctx, threadID, "Bash")
	if want := base1 + "\n第一行\n第二行\n第三行\n"; streamed.Text != want {
		t.Fatalf("第二个节流窗口之后的条目正文 = %q, want %q", streamed.Text, want)
	}

	releaseSecond()
	waitThreadIdle(t, backend, ctx, threadID)

	final := waitToolMessage(t, backend, ctx, threadID, "Bash")
	if want := base1 + "\n第一行\n第二行\n第三行\n残留片段\n"; final.Text != want {
		t.Fatalf("最终条目正文 = %q, want %q", final.Text, want)
	}
	if final.ToolStatus == nil || *final.ToolStatus != "completed" {
		t.Fatalf("最终条目状态应为 completed, got %+v", final.ToolStatus)
	}

	alone := waitToolMessage(t, backend, ctx, threadID, "BashAlone")
	if want := "BashAlone " + string(secondInput) + "\n只有增量\n"; alone.Text != want {
		t.Fatalf("只有增量的条目正文 = %q, want %q", alone.Text, want)
	}

	// 对不上的 ToolID 不能凭空造出条目。
	page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	toolCount := 0
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == "tool" {
			toolCount++
		}
	}
	if toolCount != 2 {
		t.Fatalf("工具条目数量 = %d, want 2", toolCount)
	}
}

// waitToolStatus 轮询等待指定工具名的条目状态变成 want，返回该条目。
func waitToolStatus(t *testing.T, backend *MemoryBackend, ctx context.Context, threadID, toolName, want string) wire.ThreadMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
		if err == nil {
			for _, m := range page.Messages {
				if m.Kind != nil && *m.Kind == "tool" && m.ToolName != nil && *m.ToolName == toolName &&
					m.ToolStatus != nil && *m.ToolStatus == want {
					return m
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tool message %q status %q", toolName, want)
	return wire.ThreadMessage{}
}

// newBackgroundTestBackend 建一个干净的后端，用于后台任务状态用例。
func newBackgroundTestBackend(t *testing.T, threadID string) (*MemoryBackend, context.Context) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Background Task"}, nil)
	return backend, context.Background()
}

// 后台任务：tool_result 先到（工具消息先落成 completed）、task_started 后到时，要改回 background
// 并清空完成时间；task_notification 到达后回写 completed 并补上完成时间。
func TestMemoryBackend_BackgroundTaskCompletesAfterToolEnd(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000f1"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	started := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_1", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60","run_in_background":true}`)})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "toolu_bg_1", Output: "Command running in background"})
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_bg_1", TaskID: "task-1", Text: "sleep 60"})
			close(started)
			<-release
			emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "toolu_bg_1", TaskID: "task-1", TaskStatus: "completed"})
			emit(harness.Event{Type: harness.EventText, Text: "后台任务完成后的续跑"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "后台跑一下",
		ActionID: "00000000-0000-0000-0000-0000000000f2",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	<-started
	running := waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	if running.ToolCompletedAt != nil {
		t.Fatalf("background 状态应清空完成时间, got %q", *running.ToolCompletedAt)
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)

	final := waitToolStatus(t, backend, ctx, threadID, "Bash", "completed")
	if final.ToolCompletedAt == nil || *final.ToolCompletedAt == "" {
		t.Fatalf("后台任务结束回写 completed 时应带完成时间, got %+v", final.ToolCompletedAt)
	}
}

// 后台任务：task_started 早于 tool_result 到达时，tool_end 直接落成 background 且不设完成时间。
func TestMemoryBackend_BackgroundTaskStartBeforeToolEnd(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000f3"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_2", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60","run_in_background":true}`)})
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_bg_2", TaskID: "task-2"})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "toolu_bg_2", Output: "Command running in background"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "后台跑一下",
		ActionID: "00000000-0000-0000-0000-0000000000f4",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	msg := waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	if msg.ToolCompletedAt != nil {
		t.Fatalf("background 状态不应有完成时间, got %q", *msg.ToolCompletedAt)
	}
}

// 后台任务：task_started 早于 tool_result 到达时，tool_end 报错说明工具本身失败，
// 应落成 failed 并带完成时间，而不是 background。
func TestMemoryBackend_BackgroundTaskStartBeforeToolEndError(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000f9"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_4", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60","run_in_background":true}`)})
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_bg_4", TaskID: "task-4"})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "toolu_bg_4", Output: "command failed", IsError: true})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "后台跑一下",
		ActionID: "00000000-0000-0000-0000-0000000000fa",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	msg := waitToolStatus(t, backend, ctx, threadID, "Bash", "failed")
	if msg.ToolCompletedAt == nil || *msg.ToolCompletedAt == "" {
		t.Fatalf("工具自身出错时应落成 failed 并带完成时间, got %+v", msg.ToolCompletedAt)
	}
}

// 后台任务：结束状态不是 completed 时落成 failed，并在正文末尾记下原始状态。
func TestMemoryBackend_BackgroundTaskFailedStatus(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000f5"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "toolu_bg_3", ToolName: "Bash", Input: []byte(`{"cmd":"sleep 60","run_in_background":true}`)})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "toolu_bg_3", Output: "Command running in background"})
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_bg_3", TaskID: "task-3"})
			emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "toolu_bg_3", TaskID: "task-3", TaskStatus: "failed"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "后台跑一下",
		ActionID: "00000000-0000-0000-0000-0000000000f6",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	msg := waitToolStatus(t, backend, ctx, threadID, "Bash", "failed")
	if !strings.Contains(msg.Text, "后台任务结束：failed") {
		t.Fatalf("failed 条目正文应带上原始结束状态, got %q", msg.Text)
	}
	if msg.ToolCompletedAt == nil {
		t.Fatalf("failed 条目应带完成时间")
	}
}

// 后台任务事件带未知 tool id 时静默忽略，不 panic、不凭空造条目。
func TestMemoryBackend_BackgroundTaskIgnoresUnknownToolID(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000f7"
	backend, ctx := newBackgroundTestBackend(t, threadID)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "toolu_unknown_1", TaskID: "task-x"})
			emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "toolu_unknown_2", TaskID: "task-y", TaskStatus: "completed"})
			emit(harness.Event{Type: harness.EventText, Text: "正常继续"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "无关事件",
		ActionID: "00000000-0000-0000-0000-0000000000f8",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == "tool" {
			t.Fatalf("未知 tool id 不应造出工具条目, got %+v", m)
		}
	}
}
