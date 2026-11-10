package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestQueuePersistenceRetriesEvents(t *testing.T) {
	b, s, _, threadID, _ := newQueuedMediaBackend(t)
	oldDelay := queueRetryDelay
	queueRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { queueRetryDelay = oldDelay })

	var calls atomic.Int32
	done := make(chan error, 1)
	b.replaceFollowUpsHook = func(ctx context.Context, id string, revision int64, items []store.FollowUp, events []store.FollowUpEvent) error {
		if calls.Add(1) == 1 {
			return errors.New("injected failure")
		}
		err := s.ReplaceFollowUpsWithAudit(ctx, id, revision, items, events)
		done <- err
		return err
	}
	var notifications atomic.Int32
	b.SetQueueChangedHook(func(wire.ThreadsQueueSnapshot) { notifications.Add(1) })
	fu := wire.FollowUp{ID: newUUID(), Text: "retry queue", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	b.mu.Lock()
	td := b.threads[threadID]
	td.followUps = append(td.followUps, fu)
	td.recordFollowUpEvents(newFollowUpEvent(store.FollowUpEventDispatching, "retry-test", fu, "turn"))
	b.mu.Unlock()
	b.commitQueueChange(threadID)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queue retry did not complete")
	}
	items, err := s.ListFollowUps(context.Background(), threadID)
	if err != nil || len(items) != 1 || items[0].ID != fu.ID {
		t.Fatalf("queue not persisted: %+v, %v", items, err)
	}
	if !hasAuditEvent(auditEvents(t, s, threadID), store.FollowUpEventDispatching, "retry-test", fu.ID) {
		t.Fatal("audit event not persisted")
	}
	if calls.Load() != 2 || notifications.Load() != 1 {
		t.Fatalf("calls=%d notifications=%d", calls.Load(), notifications.Load())
	}
}

func TestBranchMediaCopyAndStoreFailureRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "rollback"}[fail], func(t *testing.T) {
			b, _, dir, threadID, _ := newQueuedMediaBackend(t)
			mediaDir := filepath.Join(dir, "media", threadID)
			if err := os.MkdirAll(mediaDir, 0700); err != nil {
				t.Fatal(err)
			}
			content := []byte("branched media")
			if err := os.WriteFile(filepath.Join(mediaDir, "image.png"), content, 0600); err != nil {
				t.Fatal(err)
			}
			isFinal := true
			b.mu.Lock()
			b.threads[threadID].messages = []wire.ThreadMessage{
				{ID: "user", Role: "user", Media: []wire.MediaRef{{ID: "image", Name: "image.png", MimeType: "image/png"}}},
				{ID: "answer", Role: "assistant", IsFinalResponse: &isFinal},
			}
			b.threads[threadID].messagesLoaded = true
			count := b.projects["project"].ThreadCount
			b.mu.Unlock()
			var failedID string
			if fail {
				b.createBranchThreadHook = func(_ context.Context, thread store.Thread) error {
					failedID = thread.ID
					return errors.New("injected failure")
				}
			}
			actionID := newUUID()
			result, err := b.BranchThread(context.Background(), wire.ThreadsBranchParams{ThreadID: threadID, MessageID: "answer", ActionID: actionID})
			if fail {
				if err == nil || failedID == "" {
					t.Fatalf("expected injected failure, got %v", err)
				}
				list, listErr := b.ListThreads(context.Background(), wire.ThreadListParams{})
				if listErr != nil {
					t.Fatal(listErr)
				}
				for _, thread := range list.Threads {
					if thread.ID == failedID {
						t.Fatal("failed branch remains in thread list")
					}
				}
				if _, statErr := os.Stat(filepath.Join(dir, "media", failedID)); !os.IsNotExist(statErr) {
					t.Fatalf("branch media directory remains: %v", statErr)
				}
				b.mu.Lock()
				_, actionExists := b.actionThreads[actionID]
				gotCount := b.projects["project"].ThreadCount
				b.mu.Unlock()
				if actionExists || gotCount != count {
					t.Fatalf("rollback failed: action=%v count=%d want=%d", actionExists, gotCount, count)
				}
				rec, ok := b.actions.get(actionID)
				if !ok || rec.State != wire.ActionStateFailed {
					t.Fatalf("action not marked failed: %+v", rec)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copied, err := os.ReadFile(filepath.Join(dir, "media", result.ThreadID, "image.png"))
			if err != nil || string(copied) != string(content) {
				t.Fatalf("media not copied: %q, %v", copied, err)
			}
		})
	}
}
