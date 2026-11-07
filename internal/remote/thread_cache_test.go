package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// C-ST-07：超过 warmCacheLimit 的已归档冷会话在 ListThreads 后被淘汰（messages 置空），
// 再次读取时按需从 store 完整回载。
func TestThreadCache_EvictColdArchivedThreads(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "thread-cache.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "LRU 项目", Path: "/tmp/lru-project"})

	const total = 40
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("thread-%02d", i)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		msgs := []wire.ThreadMessage{
			{ID: id + "-user", Role: "user", Text: fmt.Sprintf("问题-%02d", i), CreatedAt: now},
			{ID: id + "-reply", Role: "assistant", Text: fmt.Sprintf("回答-%02d", i), CreatedAt: now},
		}
		backend.AddThread(wire.ThreadSummary{
			ID:         id,
			ProjectID:  "p1",
			Title:      "归档会话",
			Provider:   "memory",
			IsArchived: true,
		}, msgs)
	}

	if _, err := backend.ListThreads(ctx, wire.ThreadListParams{}); err != nil {
		t.Fatalf("list threads: %v", err)
	}

	backend.mu.Lock()
	evicted := 0
	var evictedID string
	for id, td := range backend.threads {
		if td.messagesEvicted {
			evicted++
			evictedID = id
			if td.messages != nil {
				t.Errorf("evicted thread %s still holds messages", id)
			}
		}
	}
	backend.mu.Unlock()

	if evicted != total-warmCacheLimit {
		t.Fatalf("expected %d evicted threads, got %d", total-warmCacheLimit, evicted)
	}

	// 被淘汰会话读取消息时按需回载，内容与顺序完整，且淘汰标记被清除。
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: evictedID})
	if err != nil {
		t.Fatalf("thread history: %v", err)
	}
	idx := strings.TrimPrefix(evictedID, "thread-")
	if len(hist.Messages) != 2 {
		t.Fatalf("expected 2 messages after reload, got %d: %+v", len(hist.Messages), hist.Messages)
	}
	if hist.Messages[0].Role != "user" || hist.Messages[0].Text != "问题-"+idx {
		t.Errorf("message 0 mismatch: %+v", hist.Messages[0])
	}
	if hist.Messages[1].Role != "assistant" || hist.Messages[1].Text != "回答-"+idx {
		t.Errorf("message 1 mismatch: %+v", hist.Messages[1])
	}

	backend.mu.Lock()
	stillEvicted := backend.threads[evictedID].messagesEvicted
	backend.mu.Unlock()
	if stillEvicted {
		t.Fatalf("messagesEvicted should be cleared after reload")
	}
}
