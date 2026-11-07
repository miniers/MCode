package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// 辅助会话（Swarm bee）的 Turn 要带 Transient，普通会话不带；驱动据此决定要不要保留常驻进程。
func TestSendMessageMarksHelperThreadTurnTransient(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	fakeH := &fakeMediaHarness{}
	b.RegisterHarness(fakeH)

	projID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projID, Name: "p", Path: t.TempDir()})
	parent := "lead-1"
	b.AddThread(wire.ThreadSummary{ID: parent, ProjectID: projID, Provider: string(fakeH.Kind())}, nil)
	b.AddThread(wire.ThreadSummary{ID: "bee-1", ProjectID: projID, Provider: string(fakeH.Kind()), IsHelper: true, ParentThreadID: &parent}, nil)

	for _, tc := range []struct {
		threadID  string
		transient bool
	}{{"bee-1", true}, {parent, false}} {
		fakeH.mu.Lock()
		fakeH.called = false
		fakeH.mu.Unlock()
		if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: tc.threadID, Text: "go", ActionID: newUUID()}); err != nil {
			t.Fatalf("SendMessage(%s) failed: %v", tc.threadID, err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			fakeH.mu.Lock()
			called := fakeH.called
			fakeH.mu.Unlock()
			if called {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		turn := fakeH.getLastTurn()
		if turn.ThreadID != tc.threadID || turn.Transient != tc.transient {
			t.Fatalf("线程 %s 的 Turn 异常: ThreadID=%q Transient=%v，期望 Transient=%v", tc.threadID, turn.ThreadID, turn.Transient, tc.transient)
		}
		// 等本轮收尾，下一次发送才不会撞上「已在运行」。
		for time.Now().Before(deadline) {
			if s, err := b.GetThread(context.Background(), tc.threadID); err == nil && !s.IsRunning {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
