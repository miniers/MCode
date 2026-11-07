package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// 测试归档带 helper 子会话的会话后，用 token 撤销：父子会话都恢复为未归档，
// 置顶、休眠、未读状态与操作前一致。
func TestUpdateThreadWithUndo_ArchiveUndoRestoresFamily(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	backend.SetBulkClock(func() time.Time { return currTime })

	projID := "proj-thread-undo-1"
	parentID := "th-undo-parent"
	childID := "th-undo-child"

	backend.AddThread(wire.ThreadSummary{
		ID:        parentID,
		ProjectID: projID,
		Title:     "Parent",
		IsPinned:  true,
		HasUnread: true,
		UpdatedAt: baseTime.Format(time.RFC3339Nano),
	}, nil)
	backend.AddThread(wire.ThreadSummary{
		ID:        childID,
		ProjectID: projID,
		Title:     "Helper Child",
		IsDormant: true,
		UpdatedAt: baseTime.Format(time.RFC3339Nano),
	}, nil)
	backend.SetThreadRelation(childID, parentID, true)

	childDormantAtBefore, ok := backend.ThreadDormantAt(childID)
	if !ok {
		t.Fatal("expected helper child to have dormantAt before archive")
	}

	archived := true
	res, token, err := backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: parentID,
		Archived: &archived,
	})
	if err != nil {
		t.Fatalf("UpdateThreadWithUndo failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty undo token")
	}
	if res.Thread.ID != parentID || !res.Thread.IsArchived {
		t.Fatalf("expected parent archived in result, got %+v", res.Thread)
	}

	// 归档级联到 helper 子会话，同时取消置顶、清除休眠
	for _, id := range []string{parentID, childID} {
		th, err := backend.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s) failed: %v", id, err)
		}
		if !th.IsArchived {
			t.Fatalf("expected thread %s archived", id)
		}
		if th.IsPinned {
			t.Fatalf("expected thread %s unpinned after archive", id)
		}
		if th.IsDormant {
			t.Fatalf("expected thread %s undormant after archive", id)
		}
	}

	// 10 秒窗口内撤销，父子会话都恢复
	if err := backend.UndoBulkOperation(ctx, projID, token); err != nil {
		t.Fatalf("UndoBulkOperation failed: %v", err)
	}

	parent, err := backend.GetThread(ctx, parentID)
	if err != nil {
		t.Fatalf("GetThread(%s) failed: %v", parentID, err)
	}
	if parent.IsArchived {
		t.Fatalf("expected parent not archived after undo")
	}
	if !parent.IsPinned {
		t.Fatalf("expected parent pinned=true restored")
	}
	if parent.IsDormant {
		t.Fatalf("expected parent dormant=false kept")
	}
	if !parent.HasUnread {
		t.Fatalf("expected parent hasUnread=true restored")
	}

	child, err := backend.GetThread(ctx, childID)
	if err != nil {
		t.Fatalf("GetThread(%s) failed: %v", childID, err)
	}
	if child.IsArchived {
		t.Fatalf("expected helper child not archived after undo")
	}
	if !child.IsDormant {
		t.Fatalf("expected helper child dormant=true restored")
	}
	if child.IsPinned {
		t.Fatalf("expected helper child pinned=false kept")
	}
	if child.HasUnread {
		t.Fatalf("expected helper child hasUnread=false kept")
	}
	childDormantAtAfter, ok := backend.ThreadDormantAt(childID)
	if !ok || !childDormantAtAfter.Equal(childDormantAtBefore) {
		t.Fatalf("expected helper child dormantAt restored: before=%v after=%v ok=%v", childDormantAtBefore, childDormantAtAfter, ok)
	}
}

// 测试休眠后时钟前移 11 秒再撤销，返回 ErrBulkUndoExpired。
func TestUpdateThreadWithUndo_DormantUndoExpired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	backend.SetBulkClock(func() time.Time { return currTime })

	projID := "proj-thread-undo-2"
	thID := "th-undo-settle"

	backend.AddThread(wire.ThreadSummary{
		ID:        thID,
		ProjectID: projID,
		Title:     "Settle Me",
		UpdatedAt: baseTime.Format(time.RFC3339Nano),
	}, nil)

	dormant := true
	_, token, err := backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: thID,
		Dormant:  &dormant,
	})
	if err != nil {
		t.Fatalf("UpdateThreadWithUndo failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty undo token")
	}

	th, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("GetThread(%s) failed: %v", thID, err)
	}
	if !th.IsDormant {
		t.Fatalf("expected thread dormant")
	}

	// 11 秒时撤销应已过期
	currTime = baseTime.Add(11 * time.Second)
	err = backend.UndoBulkOperation(ctx, projID, token)
	if !errors.Is(err, ErrBulkUndoExpired) {
		t.Fatalf("expected ErrBulkUndoExpired at 11s, got: %v", err)
	}
}

// 测试 Archived=false 透传 UpdateThread 时不产生撤销 token。
func TestUpdateThreadWithUndo_NoTokenWhenNotArchiving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-thread-undo-3"
	thID := "th-undo-unarchive"

	backend.AddThread(wire.ThreadSummary{
		ID:         thID,
		ProjectID:  projID,
		Title:      "Archived Thread",
		IsArchived: true,
	}, nil)

	archived := false
	res, token, err := backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: thID,
		Archived: &archived,
	})
	if err != nil {
		t.Fatalf("UpdateThreadWithUndo failed: %v", err)
	}
	if token != "" {
		t.Fatalf("expected empty undo token, got %q", token)
	}
	if res.Thread.IsArchived {
		t.Fatalf("expected thread unarchived in result")
	}

	th, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("GetThread(%s) failed: %v", thID, err)
	}
	if th.IsArchived {
		t.Fatalf("expected thread unarchived")
	}
}
