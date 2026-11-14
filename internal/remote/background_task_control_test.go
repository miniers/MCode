package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func backgroundControlFixture(t *testing.T, text, status string) (*MemoryBackend, context.Context, string, string) {
	t.Helper()
	threadID := "00000000-0000-0000-0000-0000000000f1"
	messageID := "00000000-0000-0000-0000-0000000000f2"
	backend, ctx := newBackgroundTestBackend(t, threadID)
	backend.mu.Lock()
	td := backend.threads[threadID]
	td.messages = append(td.messages, wire.ThreadMessage{ID: messageID, Text: text, ToolStatus: &status})
	td.bgCount.invalidate()
	backend.mu.Unlock()
	return backend, ctx, threadID, messageID
}

func TestBackgroundTaskLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claude-test", "tasks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "abc.output")
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text := "Command running in background with ID: abc. Output is being written to: " + path + "."
	backend, ctx, threadID, messageID := backgroundControlFixture(t, text, "background")
	got, available, err := backend.BackgroundTaskLog(ctx, threadID, messageID)
	if err != nil || !available || got != strings.Join(lines[80:], "\n")+"\n" {
		t.Fatalf("末尾日志 = %q, available=%v, err=%v", got, available, err)
	}

	// 窗口从多字节字符和长行中间开始时，仍只返回完整行。
	large := strings.Repeat("界", backgroundTaskLogMaxBytes) + "\n完整行一\n完整行二\n"
	if err := os.WriteFile(path, []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	got, available, err = backend.BackgroundTaskLog(ctx, threadID, messageID)
	if err != nil || !available || got != "完整行一\n完整行二\n" {
		t.Fatalf("大文件末尾 = %q, available=%v, err=%v", got, available, err)
	}

	link := filepath.Join(dir, "link.output")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{filepath.Join(filepath.Dir(dir), "abc.output"), filepath.Join(dir, "bad.name.output"), link} {
		backend.mu.Lock()
		backend.threads[threadID].messages[0].Text = "Output is being written to: " + bad
		backend.mu.Unlock()
		_, available, err := backend.BackgroundTaskLog(ctx, threadID, messageID)
		if err != nil || available {
			t.Errorf("路径 %q: available=%v, err=%v", bad, available, err)
		}
	}
	backend.mu.Lock()
	done := "completed"
	backend.threads[threadID].messages[0].ToolStatus = &done
	backend.threads[threadID].messages[0].Text = text
	backend.mu.Unlock()
	_, available, err = backend.BackgroundTaskLog(ctx, threadID, messageID)
	if err != nil || available {
		t.Fatalf("终态日志: available=%v, err=%v", available, err)
	}
}

func TestBackgroundTaskEndIncludesOutput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		taskStatus string
		fileState  string
	}{
		{name: "完成", taskStatus: "completed", fileState: "output"},
		{name: "失败", taskStatus: "failed", fileState: "output"},
		{name: "空文件", taskStatus: "completed", fileState: "empty"},
		{name: "文件不存在", taskStatus: "completed", fileState: "missing"},
		{name: "路径不合法", taskStatus: "completed", fileState: "invalid"},
		{name: "读取出错", taskStatus: "failed", fileState: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "claude-x", "会话", "tasks")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "abc.output")
			var lines []string
			for i := 0; i < 25; i++ {
				lines = append(lines, fmt.Sprintf("输出 %02d", i))
			}
			switch tc.fileState {
			case "output", "empty":
				var data []byte
				if tc.fileState == "output" {
					data = []byte(strings.Join(lines, "\n") + "\n")
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				path = filepath.Join(filepath.Dir(dir), "abc.output")
			case "error":
				parent := filepath.Join(dir, "not-directory")
				if err := os.WriteFile(parent, nil, 0600); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(parent, "tasks", "abc.output")
			}
			output := "Command running in background with ID: abc.\nOutput is being written to: " + path
			const input = `{"command":"printf output"}`
			wantText := "Bash " + input + "\n" + output
			if tc.fileState == "output" {
				wantText = "Bash " + input + "\n" + strings.Join(lines[5:], "\n") + "\n"
			}
			wantStatus := "completed"
			if tc.taskStatus != "completed" {
				wantStatus = "failed"
				wantText += "\n[后台任务结束：" + tc.taskStatus + "]"
			}
			threadID := "00000000-0000-0000-0000-0000000000f1"
			backend, ctx := newBackgroundTestBackend(t, threadID)
			backend.SetHarness(&mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
				emit(harness.Event{Type: harness.EventToolStart, ToolID: "tool-1", ToolName: "Bash", Input: []byte(input)})
				emit(harness.Event{Type: harness.EventToolEnd, ToolID: "tool-1", Output: output})
				emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "tool-1", TaskID: "abc"})
				emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "tool-1", TaskID: "abc", TaskStatus: tc.taskStatus})
				// 重复通知不得改写终态或重复追加失败标记。
				emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "tool-1", TaskID: "abc", TaskStatus: "failed"})
				emit(harness.Event{Type: harness.EventDone})
				return nil
			}}, t.TempDir(), harness.ModeAgent)
			if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "启动", ActionID: "00000000-0000-0000-0000-0000000000f3"}); err != nil {
				t.Fatal(err)
			}
			waitThreadIdle(t, backend, ctx, threadID)
			msg := waitToolStatus(t, backend, ctx, threadID, "Bash", wantStatus)
			if msg.Text != wantText {
				t.Fatalf("任务结束后的正文 = %q，期望 %q", msg.Text, wantText)
			}
		})
	}
}

type backgroundControlHarness struct {
	*mockHarness
	threadID string
	taskID   string
}

func (h *backgroundControlHarness) StopBackgroundTask(ctx context.Context, threadID, taskID string) error {
	h.threadID, h.taskID = threadID, taskID
	return nil
}

func TestStopBackgroundTask(t *testing.T) {
	threadID := "00000000-0000-0000-0000-0000000000f1"
	backend, ctx := newBackgroundTestBackend(t, threadID)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseTask := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTask()
	h := &backgroundControlHarness{mockHarness: &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventToolStart, ToolID: "tool-1", ToolName: "Bash"})
		emit(harness.Event{Type: harness.EventToolEnd, ToolID: "tool-1", Output: "后台运行"})
		emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "tool-1", TaskID: "task-1"})
		close(started)
		<-release
		emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "tool-1", TaskID: "task-1", TaskStatus: "failed"})
		emit(harness.Event{Type: harness.EventDone})
		return nil
	}}}
	backend.SetHarness(h, t.TempDir(), harness.ModeAgent)
	conn := newMockStreamConn()
	backend.hub.Subscribe(conn, threadID)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "启动", ActionID: "00000000-0000-0000-0000-0000000000f3"}); err != nil {
		t.Fatal(err)
	}
	<-started
	msg := waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	waitBackgroundTasks(t, backend, ctx, threadID, 1)
	stopped, err := backend.StopBackgroundTask(ctx, threadID, msg.ID)
	if err != nil || !stopped || h.threadID != threadID || h.taskID != "task-1" {
		t.Fatalf("停止结果: %v, %v, 参数 %s %s", stopped, err, h.threadID, h.taskID)
	}
	waitToolStatus(t, backend, ctx, threadID, "Bash", "stopped")
	waitBackgroundTasks(t, backend, ctx, threadID, 0)
	deadline := time.After(3 * time.Second)
	published := false
	for !published {
		select {
		case event := <-conn.notifyCh:
			for _, m := range event.Payload.Messages {
				if m.ID == msg.ID && m.ToolStatus != nil && *m.ToolStatus == "stopped" {
					published = true
				}
			}
		case <-deadline:
			t.Fatal("未发布停止状态")
		}
	}
	releaseTask()
	waitThreadIdle(t, backend, ctx, threadID)
	waitToolStatus(t, backend, ctx, threadID, "Bash", "stopped")
	waitBackgroundTasks(t, backend, ctx, threadID, 0)
}

func TestBackgroundTaskEndConcurrentStop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claude-x", "会话", "tasks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "abc.output")
	if err := os.WriteFile(path, []byte("最终输出\n"), 0600); err != nil {
		t.Fatal(err)
	}
	threadID := "00000000-0000-0000-0000-0000000000f1"
	backend, ctx := newBackgroundTestBackend(t, threadID)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseTask := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTask()
	output := "Command running in background with ID: abc.\nOutput is being written to: " + path
	h := &backgroundControlHarness{mockHarness: &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventToolStart, ToolID: "tool-1", ToolName: "Bash"})
		emit(harness.Event{Type: harness.EventToolEnd, ToolID: "tool-1", Output: output})
		emit(harness.Event{Type: harness.EventBackgroundTaskStart, ToolID: "tool-1", TaskID: "abc"})
		close(started)
		<-release
		emit(harness.Event{Type: harness.EventBackgroundTaskEnd, ToolID: "tool-1", TaskID: "abc", TaskStatus: "completed"})
		return nil
	}}}
	backend.SetHarness(h, t.TempDir(), harness.ModeAgent)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "启动", ActionID: "00000000-0000-0000-0000-0000000000f3"}); err != nil {
		t.Fatal(err)
	}
	<-started
	msg := waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		<-release
		// 两个操作都可以先完成；后完成的一方不得覆盖已经落定的状态或正文。
		_, _ = backend.StopBackgroundTask(ctx, threadID, msg.ID)
	}()
	releaseTask()
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("停止后台任务超时")
	}
	waitThreadIdle(t, backend, ctx, threadID)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, final := range backend.threads[threadID].messages {
		if final.ID != msg.ID {
			continue
		}
		if final.ToolStatus == nil {
			t.Fatal("缺少任务终态")
		}
		wantText := msg.Text
		switch *final.ToolStatus {
		case "completed":
			wantText = "Bash\n最终输出\n"
		case "stopped":
		default:
			t.Fatalf("任务终态 = %q", *final.ToolStatus)
		}
		if final.Text != wantText {
			t.Fatalf("任务终态 %q 的正文 = %q，期望 %q", *final.ToolStatus, final.Text, wantText)
		}
		return
	}
	t.Fatal("后台任务消息丢失")
}

func TestStopBackgroundTaskUnsupported(t *testing.T) {
	backend, ctx, threadID, messageID := backgroundControlFixture(t, "Command running in background with ID: task-1.", "background")
	backend.SetHarness(&mockHarness{}, t.TempDir(), harness.ModeAgent)
	stopped, err := backend.StopBackgroundTask(ctx, threadID, messageID)
	if stopped || err == nil {
		t.Fatalf("不支持时: stopped=%v, err=%v", stopped, err)
	}
}
