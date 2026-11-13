package swarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// 总时长预算：超时后 DispatchSingleEntry 拒绝派发并返回 ErrRequestTimeout，不启动 bee、不增加轮次。
func TestDispatchSingleEntry_RequestTimeout(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()

	mgr := NewManager(Options{
		Git:            runner,
		Driver:         newFakeDriver(),
		WorktreeDir:    t.TempDir(),
		Now:            clock.Now,
		RequestTimeout: 2 * time.Hour,
	})
	// 配置里的分钟数优先于 Options.RequestTimeout（90 分钟 < 2 小时）。
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, MaxRounds: 0, RequestTimeoutMinutes: 90}
	})

	leadID := "lead-request-timeout"
	mgr.BeginLeadRequest(leadID)

	clock.Advance(89 * time.Minute)
	bee, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "first", Prompt: "first"},
	})
	if err != nil || bee.ThreadID == "" {
		t.Fatalf("预算内应派出 bee: bee=%+v err=%v", bee, err)
	}

	clock.Advance(2 * time.Minute)
	bee, _, err = mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "second", Prompt: "second"},
	})
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("超时后应返回 ErrRequestTimeout，实际 err=%v", err)
	}
	if bee.ThreadID != "" {
		t.Fatalf("超时后不应派发 bee，实际 %s", bee.ThreadID)
	}
	if got := mgr.DelegationRounds(leadID); got != 0 {
		t.Fatalf("被拒绝的派发不应消耗轮次，实际 %d 轮", got)
	}
	if got := len(mgr.State(leadID).Bees); got != 1 {
		t.Fatalf("超时后不应新增 bee，实际 %d 个", got)
	}
}

// 总时长预算：Settings.RequestTimeoutMinutes 生效于 Tick 看门狗。
func TestTick_RequestTimeoutFromSettings(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()

	var stopMu sync.Mutex
	var stopped []string
	mgr := NewManager(Options{
		Git:            runner,
		Driver:         newFakeDriver(),
		WorktreeDir:    t.TempDir(),
		Now:            clock.Now,
		RequestTimeout: 3 * time.Hour,
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stopped = append(stopped, threadID)
			stopMu.Unlock()
			return nil
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, RequestTimeoutMinutes: 30}
	})

	leadID := "lead-timeout-from-settings"
	mgr.BeginLeadRequest(leadID)
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task", Prompt: "prompt"}},
	})
	if err != nil || len(bees) != 1 {
		t.Fatalf("Dispatch 失败: bees=%d err=%v", len(bees), err)
	}

	// 配置的 30 分钟就触发看门狗，不等到 Options 的 3 小时。
	clock.Advance(31 * time.Minute)
	mgr.Tick(ctx)

	h := beeByThread(t, mgr, leadID, bees[0].ThreadID)
	if h.Status != StatusFailed {
		t.Fatalf("超时后 bee 应被标记失败，实际 %s", h.Status)
	}
	if !strings.Contains(h.Summary, "30 分钟") || !strings.Contains(h.Summary, "用户发送新消息后重新计时") {
		t.Fatalf("看门狗摘要应包含上限与新消息重新计时的说明，实际: %s", h.Summary)
	}
	stopMu.Lock()
	stoppedCount := len(stopped)
	stopMu.Unlock()
	if stoppedCount != 1 {
		t.Fatalf("应停止 1 个会话，实际 %d 个", stoppedCount)
	}
}

// RequestLimits 汇总本次请求的轮次、并发、bee 预算与时长上限。
func TestRequestLimitsSnapshot(t *testing.T) {
	testenv.TempHome(t)
	clock := newTestClock()
	mgr := NewManager(Options{
		Now:            clock.Now,
		RequestTimeout: 3 * time.Hour,
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, MaxBees: 6, RequestTimeoutMinutes: 120}
	})

	leadID := "lead-limits"
	mgr.BeginLeadRequest(leadID)
	mgr.NoteDelegationRound(leadID)
	clock.Advance(45 * time.Minute)

	limits := mgr.RequestLimits(leadID, 2)
	if limits.MaxRounds != 2 || limits.RoundsUsed != 1 {
		t.Fatalf("轮次上限不符: %+v", limits)
	}
	if limits.MaxConcurrent != 6 {
		t.Fatalf("并发上限应取 Settings.MaxBees，实际 %d", limits.MaxConcurrent)
	}
	if limits.BeeBudget != DefaultBeeBudget || limits.BeesStarted != 0 {
		t.Fatalf("bee 预算不符: %+v", limits)
	}
	if limits.Timeout != 2*time.Hour || limits.Elapsed != 45*time.Minute {
		t.Fatalf("时长预算不符: timeout=%s elapsed=%s", limits.Timeout, limits.Elapsed)
	}
}

// formatBudgetDuration 输出中文时长文本。
func TestFormatBudgetDuration(t *testing.T) {
	cases := map[time.Duration]string{
		3 * time.Hour:                "3 小时",
		2*time.Hour + 30*time.Minute: "2 小时 30 分钟",
		45 * time.Minute:             "45 分钟",
		90 * time.Minute:             "1 小时 30 分钟",
		30 * time.Second:             "1 分钟",
	}
	for d, want := range cases {
		if got := formatBudgetDuration(d); got != want {
			t.Fatalf("formatBudgetDuration(%s)=%q，期望 %q", d, got, want)
		}
	}
}
