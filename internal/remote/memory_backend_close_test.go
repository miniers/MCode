package remote

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/harness"
)

// closableMockHarness 是实现了 io.Closer 的 mock harness，用于验证 MemoryBackend.Close 会回收 harness。
type closableMockHarness struct {
	kind      harness.Kind
	closeCall int32
}

func (m *closableMockHarness) Kind() harness.Kind {
	if m.kind != "" {
		return m.kind
	}
	return "closable"
}

func (m *closableMockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	return nil
}

func (m *closableMockHarness) Close() error {
	atomic.AddInt32(&m.closeCall, 1)
	return nil
}

func (m *closableMockHarness) closeCalls() int32 {
	return atomic.LoadInt32(&m.closeCall)
}

func TestMemoryBackend_CloseReclaimsHarnesses(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)

	closable := &closableMockHarness{kind: "codex"}
	// mockHarness 未实现 io.Closer，不应导致 Close 报错或 panic
	plain := &mockHarness{kind: "claude"}

	backend.RegisterHarness(closable)
	backend.RegisterHarness(plain)

	backend.Close()

	if got := closable.closeCalls(); got != 1 {
		t.Errorf("expected harness Close to be called once, got %d", got)
	}

	// Close 可重复调用：harness 的 Close 本身幂等，不要求 MemoryBackend 去重
	backend.Close()
	if got := closable.closeCalls(); got != 2 {
		t.Errorf("expected harness Close to be called again on second MemoryBackend.Close, got %d", got)
	}
}
