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
	if err := os.MkdirAll(dir, 0700); err != nil { t.Fatal(err) }
	path := filepath.Join(dir, "abc.output")
	var lines []string
	for i := 0; i < 100; i++ { lines = append(lines, fmt.Sprintf("line %03d", i)) }
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil { t.Fatal(err) }
	text := "Command running in background with ID: abc. Output is being written to: " + path + "."
	backend, ctx, threadID, messageID := backgroundControlFixture(t, text, "background")
	got, available, err := backend.BackgroundTaskLog(ctx, threadID, messageID)
	if err != nil || !available || got != strings.Join(lines[80:], "\n")+"\n" {
		t.Fatalf("末尾日志 = %q, available=%v, err=%v", got, available, err)
	}

	// 窗口从多字节字符和长行中间开始时，仍只返回完整行。
	large := strings.Repeat("界", backgroundTaskLogMaxBytes) + "\n完整行一\n完整行二\n"
	if err := os.WriteFile(path, []byte(large), 0600); err != nil { t.Fatal(err) }
	got, available, err = backend.BackgroundTaskLog(ctx, threadID, messageID)
	if err != nil || !available || got != "完整行一\n完整行二\n" {
		t.Fatalf("大文件末尾 = %q, available=%v, err=%v", got, available, err)
	}

	link := filepath.Join(dir, "link.output")
	if err := os.Symlink(path, link); err != nil { t.Fatal(err) }
	for _, bad := range []string{filepath.Join(filepath.Dir(dir), "abc.output"), filepath.Join(dir, "bad.name.output"), link} {
		backend.mu.Lock()
		backend.threads[threadID].messages[0].Text = "Output is being written to: " + bad
		backend.mu.Unlock()
		_, available, err := backend.BackgroundTaskLog(ctx, threadID, messageID)
		if err != nil || available { t.Errorf("路径 %q: available=%v, err=%v", bad, available, err) }
	}
	backend.mu.Lock()
	done := "completed"
	backend.threads[threadID].messages[0].ToolStatus = &done
	backend.threads[threadID].messages[0].Text = text
	backend.mu.Unlock()
	_, available, err = backend.BackgroundTaskLog(ctx, threadID, messageID)
	if err != nil || available { t.Fatalf("终态日志: available=%v, err=%v", available, err) }
}

type backgroundControlHarness struct {
	*mockHarness
	threadID string
	taskID string
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
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "启动", ActionID: "00000000-0000-0000-0000-0000000000f3"}); err != nil { t.Fatal(err) }
	<-started
	msg := waitToolStatus(t, backend, ctx, threadID, "Bash", "background")
	waitBackgroundTasks(t, backend, ctx, threadID, 1)
	stopped, err := backend.StopBackgroundTask(ctx, threadID, msg.ID)
	if err != nil || !stopped || h.threadID != threadID || h.taskID != "task-1" { t.Fatalf("停止结果: %v, %v, 参数 %s %s", stopped, err, h.threadID, h.taskID) }
	waitToolStatus(t, backend, ctx, threadID, "Bash", "stopped")
	waitBackgroundTasks(t, backend, ctx, threadID, 0)
	deadline := time.After(3 * time.Second)
	published := false
	for !published {
		select {
		case event := <-conn.notifyCh:
			for _, m := range event.Payload.Messages {
				if m.ID == msg.ID && m.ToolStatus != nil && *m.ToolStatus == "stopped" { published = true }
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

func TestStopBackgroundTaskUnsupported(t *testing.T) {
	backend, ctx, threadID, messageID := backgroundControlFixture(t, "Command running in background with ID: task-1.", "background")
	backend.SetHarness(&mockHarness{}, t.TempDir(), harness.ModeAgent)
	stopped, err := backend.StopBackgroundTask(ctx, threadID, messageID)
	if stopped || err == nil { t.Fatalf("不支持时: stopped=%v, err=%v", stopped, err) }
}
