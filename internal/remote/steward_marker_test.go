package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// TestMemoryBackend_StewardMarkerSummary 验证只有前台会话带有总管标记。
func TestMemoryBackend_StewardMarkerSummary(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	stewardID := "thread-steward-a"
	otherID := "thread-other-b"
	beeID := "thread-bee-c"
	backend.AddThread(wire.ThreadSummary{ID: stewardID, ProjectID: "proj-steward", Title: "总管"}, nil)
	backend.AddThread(wire.ThreadSummary{ID: otherID, ProjectID: "proj-steward", Title: "普通会话"}, nil)
	backend.AddThread(wire.ThreadSummary{ID: beeID, ProjectID: "proj-steward", Title: "子会话"}, nil)
	backend.SetThreadRelation(beeID, stewardID, true)

	// 未注入钩子：所有会话都不是总管主会话。
	assertStewardMarker(t, backend, map[string]bool{stewardID: false, otherID: false, beeID: false})

	backend.stewardFeedID = stewardID
	assertStewardMarker(t, backend, map[string]bool{stewardID: true, otherID: false, beeID: false})

	backend.stewardFeedID = otherID
	assertStewardMarker(t, backend, map[string]bool{stewardID: false, otherID: true, beeID: false})
}

// assertStewardMarker 用 threads.get 与 threads.list 两个出口核对 IsSteward。
func assertStewardMarker(t *testing.T, backend *MemoryBackend, want map[string]bool) {
	t.Helper()
	ctx := context.Background()
	for threadID, expected := range want {
		got, err := backend.GetThread(ctx, threadID)
		if err != nil {
			t.Fatalf("GetThread %s failed: %v", threadID, err)
		}
		if got.IsSteward != expected {
			t.Errorf("GetThread %s: IsSteward = %v, want %v", threadID, got.IsSteward, expected)
		}
	}
	list, err := backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	seen := make(map[string]bool, len(list.Threads))
	for _, th := range list.Threads {
		expected, ok := want[th.ID]
		if !ok {
			continue
		}
		seen[th.ID] = true
		if th.IsSteward != expected {
			t.Errorf("ListThreads %s: IsSteward = %v, want %v", th.ID, th.IsSteward, expected)
		}
	}
	for threadID := range want {
		if !seen[threadID] {
			t.Errorf("ListThreads 缺少会话 %s", threadID)
		}
	}
}
