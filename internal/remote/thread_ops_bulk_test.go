package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

// 测试批量归档后在注入时钟的 9 秒时撤销成功，状态完全恢复（包括置顶）；
// 在 11 秒时撤销返回 ErrBulkUndoExpired。
func TestBulkArchive_UndoWithinWindowAndExpired(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	backend.SetBulkClock(func() time.Time { return currTime })

	projID := "proj-bulk-test-1"
	th1 := "th-archive-1"
	th2 := "th-archive-2"

	backend.AddThread(wire.ThreadSummary{
		ID:        th1,
		ProjectID: projID,
		Title:     "Thread 1",
		IsPinned:  true,
		UpdatedAt: baseTime.Format(time.RFC3339Nano),
	}, nil)
	backend.AddThread(wire.ThreadSummary{
		ID:        th2,
		ProjectID: projID,
		Title:     "Thread 2",
		IsPinned:  false,
		UpdatedAt: baseTime.Format(time.RFC3339Nano),
	}, nil)

	token, skipped, err := backend.BulkArchive(ctx, projID, []string{th1, th2})
	if err != nil {
		t.Fatalf("BulkArchive failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", skipped)
	}

	// 验证已被归档
	for _, id := range []string{th1, th2} {
		th, err := backend.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s) failed: %v", id, err)
		}
		if !th.IsArchived {
			t.Fatalf("expected thread %s to be archived", id)
		}
	}

	// 9 秒时撤销，应成功且完全恢复（包括置顶状态）
	currTime = baseTime.Add(9 * time.Second)
	if err := backend.UndoBulkOperation(ctx, projID, token); err != nil {
		t.Fatalf("UndoBulkOperation at 9s failed: %v", err)
	}

	th1Restored, err := backend.GetThread(ctx, th1)
	if err != nil {
		t.Fatalf("GetThread(%s) failed: %v", th1, err)
	}
	if th1Restored.IsArchived {
		t.Fatalf("expected thread %s to not be archived after undo", th1)
	}
	if !th1Restored.IsPinned {
		t.Fatalf("expected thread %s to have restored pinned=true", th1)
	}

	th2Restored, err := backend.GetThread(ctx, th2)
	if err != nil {
		t.Fatalf("GetThread(%s) failed: %v", th2, err)
	}
	if th2Restored.IsArchived {
		t.Fatalf("expected thread %s to not be archived after undo", th2)
	}
	if th2Restored.IsPinned {
		t.Fatalf("expected thread %s to have pinned=false", th2)
	}

	// 再次归档并测试 11 秒过期
	currTime = baseTime.Add(100 * time.Second)
	token2, _, err := backend.BulkArchive(ctx, projID, []string{th1})
	if err != nil {
		t.Fatalf("BulkArchive round 2 failed: %v", err)
	}
	currTime = currTime.Add(11 * time.Second)
	err = backend.UndoBulkOperation(ctx, projID, token2)
	if !errors.Is(err, ErrBulkUndoExpired) {
		t.Fatalf("expected ErrBulkUndoExpired at 11s, got: %v", err)
	}
}

// 测试运行中的线程被跳过并计入 skippedRunning。
func TestBulkOperations_SkipRunningThreads(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-bulk-running"
	thRunning := "th-running"
	thIdle := "th-idle"

	backend.AddThread(wire.ThreadSummary{
		ID:        thRunning,
		ProjectID: projID,
		Title:     "Running Thread",
		IsRunning: true,
	}, nil)
	backend.AddThread(wire.ThreadSummary{
		ID:        thIdle,
		ProjectID: projID,
		Title:     "Idle Thread",
		IsRunning: false,
	}, nil)

	// 运行中的会话必须伴随真实的运行实例（runTurnID）：对外摘要只以真实运行实例为准，
	// 空 runTurnID 会被当成粘滞标记纠正为空闲。
	backend.mu.Lock()
	backend.threads[thRunning].runTurnID = "turn-bulk-running"
	backend.mu.Unlock()

	// 1. BulkPreview 跳过运行中的线程
	previewIDs, skipped, err := backend.BulkPreview(ctx, projID, nil, true, true)
	if err != nil {
		t.Fatalf("BulkPreview failed: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped running thread in preview, got %d", skipped)
	}
	if len(previewIDs) != 1 || previewIDs[0] != thIdle {
		t.Fatalf("expected only idle thread in preview, got: %v", previewIDs)
	}

	// 2. BulkArchive 跳过运行中的线程
	token, skippedArchive, err := backend.BulkArchive(ctx, projID, []string{thRunning, thIdle})
	if err != nil {
		t.Fatalf("BulkArchive failed: %v", err)
	}
	if skippedArchive != 1 {
		t.Fatalf("expected 1 skipped running thread in archive, got %d", skippedArchive)
	}
	if token == "" {
		t.Fatal("expected non-empty token for idle thread archive")
	}

	thR, _ := backend.GetThread(ctx, thRunning)
	if thR.IsArchived {
		t.Fatalf("running thread should not be archived")
	}
	thI, _ := backend.GetThread(ctx, thIdle)
	if !thI.IsArchived {
		t.Fatalf("idle thread should be archived")
	}
}

// 测试 BulkMarkRead 清除未读，撤销后恢复未读。
func TestBulkMarkRead_AndUndo(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-bulk-read"
	th1 := "th-unread-1"
	th2 := "th-unread-2"

	backend.AddThread(wire.ThreadSummary{
		ID:        th1,
		ProjectID: projID,
		Title:     "Unread Thread",
		HasUnread: true,
	}, nil)
	backend.AddThread(wire.ThreadSummary{
		ID:        th2,
		ProjectID: projID,
		Title:     "Read Thread",
		HasUnread: false,
	}, nil)

	token, err := backend.BulkMarkRead(ctx, projID, []string{th1, th2})
	if err != nil {
		t.Fatalf("BulkMarkRead failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	th1After, _ := backend.GetThread(ctx, th1)
	if th1After.HasUnread {
		t.Fatalf("expected thread %s to have unread cleared", th1)
	}

	// 撤销后恢复未读状态
	if err := backend.UndoBulkOperation(ctx, projID, token); err != nil {
		t.Fatalf("UndoBulkOperation failed: %v", err)
	}

	th1Restored, _ := backend.GetThread(ctx, th1)
	if !th1Restored.HasUnread {
		t.Fatalf("expected thread %s to have restored hasUnread=true", th1)
	}
	th2Restored, _ := backend.GetThread(ctx, th2)
	if th2Restored.HasUnread {
		t.Fatalf("expected thread %s to remain hasUnread=false", th2)
	}
}

// 测试 BulkMarkDormant 的 dormantAt 按顺序递减。
func TestBulkMarkDormant_DormantAtDecreasing(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	fixedNow := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	backend.SetBulkClock(func() time.Time { return fixedNow })

	projID := "proj-bulk-settle"
	t1 := "th-dormant-1"
	t2 := "th-dormant-2"
	t3 := "th-dormant-3"

	for _, tid := range []string{t1, t2, t3} {
		backend.AddThread(wire.ThreadSummary{
			ID:        tid,
			ProjectID: projID,
			Title:     "Thread " + tid,
		}, nil)
	}

	token, skipped, err := backend.BulkMarkDormant(ctx, projID, []string{t1, t2, t3})
	if err != nil {
		t.Fatalf("BulkMarkDormant failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", skipped)
	}

	s1, ok1 := backend.ThreadDormantAt(t1)
	s2, ok2 := backend.ThreadDormantAt(t2)
	s3, ok3 := backend.ThreadDormantAt(t3)

	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("expected all dormantAt to be set: ok1=%v, ok2=%v, ok3=%v", ok1, ok2, ok3)
	}

	// dormantAt 应按顺序递减：s1 > s2 > s3
	if !s1.After(s2) {
		t.Fatalf("expected s1 (%v) to be after s2 (%v)", s1, s2)
	}
	if !s2.After(s3) {
		t.Fatalf("expected s2 (%v) to be after s3 (%v)", s2, s3)
	}
}

// 测试错误的 token 返回 NotFound。
func TestBulkUndo_NotFound(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	err := backend.UndoBulkOperation(ctx, "proj-any", "non-existent-token")
	if !errors.Is(err, ErrBulkUndoNotFound) {
		t.Fatalf("expected ErrBulkUndoNotFound, got: %v", err)
	}
}
