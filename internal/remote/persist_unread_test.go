package remote

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestPersistThreadUnreadUsesCurrentMemoryState(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "unread.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Project 1"})
	created, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	threadID := created.ThreadID

	b.mu.Lock()
	b.threads[threadID].summary.HasUnread = true
	b.mu.Unlock()
	b.persistThreadUnread(threadID, false)
	stored, err := s.GetThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HasUnread {
		t.Fatal("旧的已读状态覆盖了内存中的未读状态")
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(unread bool) {
			defer wg.Done()
			b.mu.Lock()
			b.threads[threadID].summary.HasUnread = unread
			b.mu.Unlock()
			b.persistThreadUnread(threadID, !unread)
		}(i%2 == 0)
	}
	wg.Wait()
	b.mu.Lock()
	want := b.threads[threadID].summary.HasUnread
	b.mu.Unlock()
	stored, err = s.GetThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.HasUnread != want {
		t.Fatalf("库中未读状态为 %v，内存中为 %v", stored.HasUnread, want)
	}
}
