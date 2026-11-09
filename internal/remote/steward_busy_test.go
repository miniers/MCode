package remote

import (
	"context"
	"reflect"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

func TestSetStewardBusy(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.AddThread(wire.ThreadSummary{ID: "feed", Provider: "steward"}, nil)
	b.mu.Lock()
	b.stewardFeedID = "feed"
	b.mu.Unlock()
	var states []bool
	remove := hub.AddObserver(func(id, method string, value any) {
		if id == "feed" && method == "thread.summary" {
			states = append(states, value.(wire.ThreadSummary).IsRunning)
		}
	})
	defer remove()
	for _, busy := range []bool{true, false} {
		b.SetStewardBusy("feed", busy)
		summary, err := b.GetThread(context.Background(), "feed")
		if err != nil || summary.IsRunning != busy {
			t.Fatalf("状态 %v，摘要 %+v，错误 %v", busy, summary, err)
		}
	}
	if !reflect.DeepEqual(states, []bool{true, false}) {
		t.Fatalf("发布状态 %v", states)
	}
}
