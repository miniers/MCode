package ui

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

type stopSwarmClient struct {
	*FakeClient
	mu    sync.Mutex
	calls []string
}

func (f *stopSwarmClient) StopAllSwarmBees(ctx context.Context, threadID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "stop:"+threadID)
	f.mu.Unlock()
	return f.FakeClient.StopAllSwarmBees(ctx, threadID)
}

func (f *stopSwarmClient) Interrupt(ctx context.Context, threadID string) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "interrupt:"+threadID)
	f.mu.Unlock()
	return f.FakeClient.Interrupt(ctx, threadID)
}

func TestStopCurrentSwarm(t *testing.T) {
	for _, tc := range []struct {
		name  string
		busy  bool
		calls []string
	}{
		{name: "busy", busy: true, calls: []string{"stop:t1", "interrupt:t1"}},
		{name: "idle", calls: []string{"interrupt:t1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fake := &stopSwarmClient{FakeClient: NewFakeClient()}
			s := newChatState(fake)
			t.Cleanup(s.close)
			s.mu.Lock()
			s.selectedThreadID = "t1"
			s.threads = []Thread{{ID: "other", SwarmBusy: !tc.busy}, {ID: "t1", SwarmBusy: tc.busy}}
			s.mu.Unlock()

			s.stopCurrent(false)

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				s.mu.Lock()
				interrupted := s.interrupted["t1"]
				s.mu.Unlock()
				if interrupted {
					fake.mu.Lock()
					calls := append([]string(nil), fake.calls...)
					fake.mu.Unlock()
					if !reflect.DeepEqual(calls, tc.calls) {
						t.Fatalf("calls = %v, want %v", calls, tc.calls)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("timed out waiting for interrupt")
		})
	}
}
