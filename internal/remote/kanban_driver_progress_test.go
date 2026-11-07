package remote

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// progressTestClock 是并发安全的可推进时钟，供进度相关测试控制 SessionState.LastFileChangeAt。
type progressTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func newProgressTestClock(t time.Time) *progressTestClock {
	return &progressTestClock{t: t}
}

func (c *progressTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *progressTestClock) Set(v time.Time) {
	c.mu.Lock()
	c.t = v
	c.mu.Unlock()
}

// waitLastFileChangeAt 轮询 SessionState，直到 LastFileChangeAt 等于期望值。
func waitLastFileChangeAt(t *testing.T, backend *MemoryBackend, threadID string, want time.Time) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	var last time.Time
	for time.Now().Before(deadline) {
		st, err := backend.SessionState(ctx, threadID)
		if err == nil {
			last = st.LastFileChangeAt
			if last.Equal(want) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("LastFileChangeAt 未前进到 %v，实际 %v", want, last)
}

// TestSessionStateTracksLastFileChangeAt 验证工具写文件后 SessionState 的 LastFileChangeAt 前进到写文件时刻。
func TestSessionStateTracksLastFileChangeAt(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	clock := newProgressTestClock(base)
	backend.SetClock(clock.Now)

	proceed := make(chan struct{})
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			select {
			case <-proceed:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(harness.Event{
				Type:     harness.EventToolStart,
				ToolID:   "call-1",
				ToolName: "Edit",
				Input:    json.RawMessage(`{"file_path": "internal/remote/a.go"}`),
			})
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000ca"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "FileChange"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "改文件",
		ActionID: "00000000-0000-0000-0000-0000000000cb",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// 轮次开始即记录一次落地时刻，此时还没有工具写文件。
	waitLastFileChangeAt(t, backend, threadID, base)

	changedAt := base.Add(7 * time.Minute)
	clock.Set(changedAt)
	close(proceed)

	waitLastFileChangeAt(t, backend, threadID, changedAt)

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)
	if st, err := backend.SessionState(ctx, threadID); err != nil {
		t.Fatalf("SessionState failed: %v", err)
	} else if !st.LastFileChangeAt.Equal(changedAt) {
		t.Fatalf("轮次结束后 LastFileChangeAt 应保持写文件时刻 %v，实际 %v", changedAt, st.LastFileChangeAt)
	}
}

// TestSessionStateResetsFileChangeAtOnNewTurn 验证新一轮开始时 LastFileChangeAt 更新为当轮开始时刻。
func TestSessionStateResetsFileChangeAtOnNewTurn(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	clock := newProgressTestClock(base)
	backend.SetClock(clock.Now)

	write1 := make(chan struct{})
	hold2 := make(chan struct{})
	var mu sync.Mutex
	runs := 0
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			mu.Lock()
			runs++
			run := runs
			mu.Unlock()
			if run == 1 {
				select {
				case <-write1:
				case <-ctx.Done():
					return ctx.Err()
				}
				emit(harness.Event{
					Type:     harness.EventToolStart,
					ToolID:   "call-1",
					ToolName: "Write",
					Input:    json.RawMessage(`{"file_path": "internal/remote/b.go"}`),
				})
				return nil
			}
			// 第二轮保持运行中，便于观察新一轮开始时刻。
			select {
			case <-hold2:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000cc"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "NewTurn"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "第一轮",
		ActionID: "00000000-0000-0000-0000-0000000000cd",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	firstChange := base.Add(10 * time.Minute)
	clock.Set(firstChange)
	close(write1)
	waitThreadIdle(t, backend, ctx, threadID)
	if st, err := backend.SessionState(ctx, threadID); err != nil {
		t.Fatalf("SessionState failed: %v", err)
	} else if !st.LastFileChangeAt.Equal(firstChange) {
		t.Fatalf("第一轮写文件后 LastFileChangeAt 应为 %v，实际 %v", firstChange, st.LastFileChangeAt)
	}

	secondTurnStart := base.Add(30 * time.Minute)
	clock.Set(secondTurnStart)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "第二轮",
		ActionID: "00000000-0000-0000-0000-0000000000ce",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	waitLastFileChangeAt(t, backend, threadID, secondTurnStart)

	close(hold2)
	waitThreadIdle(t, backend, ctx, threadID)
}
