package swarm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 同一 lead 两个 bee 在窗口内完成时只调用一次模型。
func TestReviewFunnelBatchOneCall(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		return approveJSON(0, 1), nil
	}
	first := dispatchFunnelBee(t, m, fake, "lead-batch", "Aspen", dir, "修改 internal/a.go")
	second := dispatchFunnelBee(t, m, fake, "lead-batch", "Birch", dir, "修改 internal/b.go")
	writeBeeFile(t, first, "internal/a.go", strings.Repeat("a\n", 50))
	writeBeeFile(t, second, "internal/b.go", strings.Repeat("b\n", 50))
	completeBee(t, m, first, "完成")
	completeBee(t, m, second, "完成")

	waitFunnelIndex(t, m, "lead-batch", first.Index, isApproved)
	waitFunnelIndex(t, m, "lead-batch", second.Index, isApproved)
	if _, generate := fake.calls(); generate != 1 {
		t.Fatalf("两个 bee 应合并为一次模型调用，实际 %d 次", generate)
	}
}

// 批量结果缺一个 bee 时单独重试一次，第二次仍缺失则升级 L3。
func TestReviewFunnelBatchMissingRetry(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	var calls int
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		calls++
		if calls == 1 {
			return approveJSON(0), nil
		}
		return `{"reviews":[]}`, nil
	}
	first := dispatchFunnelBee(t, m, fake, "lead-missing", "Aspen", dir, "修改 internal/a.go")
	second := dispatchFunnelBee(t, m, fake, "lead-missing", "Birch", dir, "修改 internal/b.go")
	writeBeeFile(t, first, "internal/a.go", strings.Repeat("a\n", 50))
	writeBeeFile(t, second, "internal/b.go", strings.Repeat("b\n", 50))
	completeBee(t, m, first, "完成")
	completeBee(t, m, second, "完成")

	waitFunnelIndex(t, m, "lead-missing", first.Index, isApproved)
	h := waitFunnelIndex(t, m, "lead-missing", second.Index, isL3)
	if h.ReviewAttempts < 2 {
		t.Fatalf("缺失的 bee 应重试到第二次再升级，实际 attempts=%d", h.ReviewAttempts)
	}
	if _, generate := fake.calls(); generate < 2 {
		t.Fatalf("缺失重试应至少调用两次模型，实际 %d", generate)
	}
}

// 攒批在 lead 空闲、最老排队满窗口或队列满 6 个时发车。
func TestCollectL2BatchConditions(t *testing.T) {
	m := NewManager(Options{Persister: newFakePersister()})
	m.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree", ReviewMode: "ai"}
	})
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	m.mu.Lock()
	m.leads["lead"] = &leadState{leadThreadID: "lead", bees: []Bee{{
		Index: 0, ThreadID: "b1", ReviewState: ReviewRunning, ReviewStage: ReviewStageL2Queued,
	}}}
	m.leads["lead"].bees[0].ReviewQueuedAt = &now
	m.mu.Unlock()
	// 刚入队且仍有运行中的 bee：窗口未满不发车。
	m.mu.Lock()
	m.leads["lead"].bees = append(m.leads["lead"].bees, Bee{
		Index: 1, ThreadID: "running", Status: StatusRunning,
	})
	m.mu.Unlock()
	if got := m.collectL2Batch("lead", now.Add(5*time.Second)); len(got) != 0 {
		t.Fatalf("窗口未满不应发车: %+v", got)
	}
	// 满 20 秒后发车。
	if got := m.collectL2Batch("lead", now.Add(reviewBatchWindow)); len(got) != 1 {
		t.Fatalf("排队满窗口应发车: %+v", got)
	}
}
