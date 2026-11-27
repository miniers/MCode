package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteTxWaitsForOtherProcessLock(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "txlock.db")

	storeA, err := OpenWithOptions(dbPath, OpenOptions{SkipQuickCheck: true})
	if err != nil {
		t.Fatalf("open storeA: %v", err)
	}
	defer storeA.Close()

	now := time.Now().UTC()
	if err := storeA.UpsertProject(ctx, Project{
		ID:        "proj-txlock",
		Name:      "txlock",
		Path:      t.TempDir(),
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if err := storeA.CreateThread(ctx, Thread{
		ID:        "thread-txlock",
		ProjectID: "proj-txlock",
		Title:     "txlock",
		Provider:  "memory",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	storeB, err := OpenWithOptions(dbPath, OpenOptions{SkipQuickCheck: true})
	if err != nil {
		t.Fatalf("open storeB: %v", err)
	}
	defer storeB.Close()

	locked := make(chan struct{})
	commitErrCh := make(chan error, 1)
	go func() {
		tx, err := storeA.db.BeginTx(ctx, nil)
		if err != nil {
			close(locked)
			commitErrCh <- err
			return
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET name = name WHERE id = ?", "proj-txlock"); err != nil {
			_ = tx.Rollback()
			close(locked)
			commitErrCh <- err
			return
		}
		close(locked)
		time.Sleep(200 * time.Millisecond)
		commitErrCh <- tx.Commit()
	}()

	<-locked
	msg, err := storeB.AppendMessage(ctx, Message{
		ID:       "msg-txlock-1",
		ThreadID: "thread-txlock",
		Role:     "user",
		Kind:     "text",
		Text:     "hello",
	})
	if err != nil {
		t.Fatalf("storeB.AppendMessage failed while storeA held write lock: %v", err)
	}
	if msg.Seq != 1 {
		t.Fatalf("expected Seq 1, got %d", msg.Seq)
	}
	if err := <-commitErrCh; err != nil {
		t.Fatalf("storeA commit failed: %v", err)
	}
}

func TestQuickCheckMarkerPerm(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "perm.db")
	writeQuickCheckMarker(dbPath)

	info, err := os.Stat(quickCheckMarkerPath(dbPath))
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("marker perm = %#o, want 0600", perm)
	}
}
