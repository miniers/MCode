package remote

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type usageMockHarness struct {
	mu        sync.Mutex
	callCount int
}

func (m *usageMockHarness) Kind() harness.Kind {
	return "mock-usage"
}

func (m *usageMockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if count == 1 {
		// 第 1 轮：emit 两次 usage，最终用量应为第二次的值
		emit(harness.Event{
			Type: harness.EventUsage,
			Usage: &harness.Usage{
				InputTokens:  100,
				OutputTokens: 50,
				CachedTokens: 20,
			},
		})
		emit(harness.Event{
			Type: harness.EventUsage,
			Usage: &harness.Usage{
				InputTokens:  120,
				OutputTokens: 60,
				CachedTokens: 30,
			},
		})
		emit(harness.Event{
			Type: harness.EventText,
			Text: "turn 1 completed",
		})
		emit(harness.Event{
			Type:       harness.EventDone,
			StopReason: "completed",
		})
	} else {
		// 第 2 轮：emit 1 次 usage
		emit(harness.Event{
			Type: harness.EventUsage,
			Usage: &harness.Usage{
				InputTokens:  200,
				OutputTokens: 80,
				CachedTokens: 50,
			},
		})
		emit(harness.Event{
			Type: harness.EventText,
			Text: "turn 2 completed",
		})
		emit(harness.Event{
			Type:       harness.EventDone,
			StopReason: "completed",
		})
	}
	return nil
}

// usageWithoutDuration 返回把 DurationMs 置 0 后的副本。
// 耗时取决于实际运行，本文件的断言只比较 token 字段。
func usageWithoutDuration(u harness.Usage) harness.Usage {
	u.DurationMs = 0
	return u
}

func TestBackend_ThreadUsage_TwiceInTurnAndAcrossTurnsAndReload(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode_usage.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store 1: %v", err)
	}

	mock := &usageMockHarness{}
	backend1.RegisterHarness(mock)

	projID := "proj-usage-1"
	backend1.AddProject(wire.ProjectSummary{
		ID:   projID,
		Name: "Usage Project",
		Path: "/test/usage/proj",
	})

	thID := "thread-usage-1"
	backend1.AddThread(wire.ThreadSummary{
		ID:        thID,
		ProjectID: projID,
		Title:     "Usage Thread",
		Provider:  "mock-usage",
	}, nil)

	// 1. 发送第 1 轮消息（一轮里 emit 两次 usage）
	_, err = backend1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "message 1",
		ActionID: "act-1",
	})
	if err != nil {
		t.Fatalf("send message 1: %v", err)
	}

	waitThreadIdle(t, backend1, ctx, thID)

	// 校验第 1 轮最终用量等于第二次的值
	turns1, total1, err := backend1.ThreadUsage(ctx, thID)
	if err != nil {
		t.Fatalf("ThreadUsage after turn 1: %v", err)
	}
	wantTurn1 := harness.Usage{
		InputTokens:  120,
		OutputTokens: 60,
		CachedTokens: 30,
	}
	if !reflect.DeepEqual(usageWithoutDuration(turns1[0]), wantTurn1) {
		t.Errorf("turn 1 usage mismatch: got %+v, want %+v", turns1[0], wantTurn1)
	}
	if !reflect.DeepEqual(usageWithoutDuration(total1), wantTurn1) {
		t.Errorf("total 1 usage mismatch: got %+v, want %+v", total1, wantTurn1)
	}

	// 2. 发送第 2 轮消息
	_, err = backend1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "message 2",
		ActionID: "act-2",
	})
	if err != nil {
		t.Fatalf("send message 2: %v", err)
	}

	waitThreadIdle(t, backend1, ctx, thID)

	// 校验两轮合计正确
	turns2, total2, err := backend1.ThreadUsage(ctx, thID)
	if err != nil {
		t.Fatalf("ThreadUsage after turn 2: %v", err)
	}
	wantTurn2 := harness.Usage{
		InputTokens:  200,
		OutputTokens: 80,
		CachedTokens: 50,
	}
	if !reflect.DeepEqual(usageWithoutDuration(turns2[1]), wantTurn2) {
		t.Errorf("turn 2 usage mismatch: got %+v, want %+v", turns2[1], wantTurn2)
	}
	wantTotal2 := harness.Usage{
		InputTokens:  120 + 200,
		OutputTokens: 60 + 80,
		CachedTokens: 30 + 50,
	}
	if !reflect.DeepEqual(usageWithoutDuration(total2), wantTotal2) {
		t.Errorf("total 2 usage mismatch: got %+v, want %+v", total2, wantTotal2)
	}

	// 3. 重启（重新打开 store 并恢复），验证用量还在
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	turnsReload, totalReload, err := backend2.ThreadUsage(ctx, thID)
	if err != nil {
		t.Fatalf("ThreadUsage after reload: %v", err)
	}
	if !reflect.DeepEqual(usageWithoutDuration(turnsReload[0]), wantTurn1) {
		t.Errorf("turn 1 usage after reload mismatch: got %+v, want %+v", turnsReload[0], wantTurn1)
	}
	if !reflect.DeepEqual(usageWithoutDuration(turnsReload[1]), wantTurn2) {
		t.Errorf("turn 2 usage after reload mismatch: got %+v, want %+v", turnsReload[1], wantTurn2)
	}
	if !reflect.DeepEqual(usageWithoutDuration(totalReload), wantTotal2) {
		t.Errorf("total usage after reload mismatch: got %+v, want %+v", totalReload, wantTotal2)
	}

	// 4. 不存在的会话返回错误
	_, _, err = backend2.ThreadUsage(ctx, "non-existent")
	if err == nil {
		t.Fatalf("expected error for non-existent thread, got nil")
	}
}

// usageRequestsMockHarness 先上报带两个逐请求样本的用量，再上报一条不带样本的整轮用量，
// 模拟 provider 最后一条 usage 只带累计计费量的情形。
type usageRequestsMockHarness struct{}

func (m *usageRequestsMockHarness) Kind() harness.Kind {
	return "mock-usage-requests"
}

func (m *usageRequestsMockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	emit(harness.Event{
		Type: harness.EventUsage,
		Usage: &harness.Usage{
			InputTokens:  100,
			OutputTokens: 60,
			Requests: []harness.RequestSpeed{
				{OutputTokens: 30, DurationMs: 2000},
				{OutputTokens: 30, DurationMs: 3000},
			},
		},
	})
	emit(harness.Event{
		Type: harness.EventUsage,
		Usage: &harness.Usage{
			InputTokens:  120,
			OutputTokens: 60,
		},
	})
	emit(harness.Event{Type: harness.EventText, Text: "done"})
	emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
	return nil
}

// TestBackend_TurnUsageKeepsRequestsWhenFinalUsageHasNone 验证不带样本的后续 usage 不会抹掉已收集的
// 逐请求速度样本，且样本经落库后重启仍能恢复。
func TestBackend_TurnUsageKeepsRequestsWhenFinalUsageHasNone(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode_usage_requests.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store: %v", err)
	}
	backend1.RegisterHarness(&usageRequestsMockHarness{})
	backend1.AddProject(wire.ProjectSummary{ID: "proj-requests", Name: "Requests", Path: "/test/requests"})
	backend1.AddThread(wire.ThreadSummary{
		ID:        "thread-requests",
		ProjectID: "proj-requests",
		Title:     "Requests Thread",
		Provider:  "mock-usage-requests",
	}, nil)

	if _, err := backend1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: "thread-requests",
		Text:     "message",
		ActionID: "act-requests",
	}); err != nil {
		t.Fatalf("send message: %v", err)
	}
	waitThreadIdle(t, backend1, ctx, "thread-requests")

	wantRequests := []harness.RequestSpeed{
		{OutputTokens: 30, DurationMs: 2000},
		{OutputTokens: 30, DurationMs: 3000},
	}
	turns, _, err := backend1.ThreadUsage(ctx, "thread-requests")
	if err != nil {
		t.Fatalf("ThreadUsage: %v", err)
	}
	if !reflect.DeepEqual(turns[0].Requests, wantRequests) {
		t.Fatalf("逐请求样本被抹掉或改写: got %+v, want %+v", turns[0].Requests, wantRequests)
	}

	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()
	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()
	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	turnsReload, _, err := backend2.ThreadUsage(ctx, "thread-requests")
	if err != nil {
		t.Fatalf("ThreadUsage after reload: %v", err)
	}
	if !reflect.DeepEqual(turnsReload[0].Requests, wantRequests) {
		t.Fatalf("重启后逐请求样本不一致: got %+v, want %+v", turnsReload[0].Requests, wantRequests)
	}
}
