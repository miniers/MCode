package automation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
)

// followUpDriver 在 fakeDriver 基础上实现 SessionMessenger，记录 heartbeat 投递目标。
type followUpDriver struct {
	*fakeDriver
	mu   sync.Mutex
	sent []string
}

func (d *followUpDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, threadID)
	return nil
}

func (d *followUpDriver) sentTargets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sent...)
}

// TestTickCatchUpRunsOnceAfterThreeMissedPeriods 验证休眠跨过 3 个周期后只补跑 1 次，
// 运行记录标记 catch_up 与 missed_count=3，而不是按窗口重放。
func TestTickCatchUpRunsOnceAfterThreeMissedPeriods(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "catch-up-once",
		Prompt:    "run",
		Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// due = t0+60s；推进到 due+180s，共 4 个到期触发点（含 due），需要补跑 3 个。
	clock.Set(t0.Add(240 * time.Second))
	svc.Tick(ctx)

	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected exactly 1 start, got %d", count)
	}
	runs, err := svc.Runs(ctx, def.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 run, got %d", len(runs))
	}
	if runs[0].Trigger != RunTriggerCatchUp {
		t.Errorf("expected trigger %s, got %s", RunTriggerCatchUp, runs[0].Trigger)
	}
	if runs[0].MissedCount != 3 {
		t.Errorf("expected missed_count 3, got %d", runs[0].MissedCount)
	}

	updated, _ := svc.Get(ctx, def.ID)
	if updated.NextRunAt == nil || !updated.NextRunAt.After(clock.Now()) {
		t.Errorf("expected next_run_at after now, got %v", updated.NextRunAt)
	}
	if updated.Spec.State.TotalRunCount != 1 {
		t.Errorf("expected total run count 1, got %d", updated.Spec.State.TotalRunCount)
	}

	// 运行还在进行中，再次 Tick 不得重复补跑。
	svc.Tick(ctx)
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected still 1 start, got %d", count)
	}
}

// TestCatchUpDisabledSkipsAndReschedules 验证关闭补跑后周期任务只跳过错过点并推进下一次触发。
func TestCatchUpDisabledSkipsAndReschedules(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	disabled := false
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "catch-up-disabled",
		Prompt:    "run",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			CatchUp:  &disabled,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Set(t0.Add(180 * time.Second)) // due=t0+60，错过 2 个点
	svc.Tick(ctx)

	if count := driver.startCount(); count != 0 {
		t.Fatalf("expected no start when catch-up disabled, got %d", count)
	}
	runs, _ := svc.Runs(ctx, def.ID, 10)
	if len(runs) != 0 {
		t.Fatalf("expected no runs for periodic skip, got %d", len(runs))
	}
	updated, _ := svc.Get(ctx, def.ID)
	if updated.Spec.State.TotalRunCount != 0 {
		t.Errorf("expected total run count 0, got %d", updated.Spec.State.TotalRunCount)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(clock.Now()) {
		t.Errorf("expected next_run_at after now, got %v", updated.NextRunAt)
	}

	// 第二次 Tick 不得重复补跑或重复推进。
	svc.Tick(ctx)
	if count := driver.startCount(); count != 0 {
		t.Fatalf("expected still no start, got %d", count)
	}
}

// TestHeartbeatCatchUpDefault 验证 heartbeat 未显式配置时默认补跑，仍投递回来源线程。
func TestHeartbeatCatchUpDefault(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	fd := &followUpDriver{fakeDriver: newFakeDriver()}
	svc, s := setupTestService(t, clock, fd.fakeDriver)
	defer s.Close()
	svc.opts.Driver = fd

	def, err := svc.Create(ctx, CreateInput{
		ProjectID:      "proj-1",
		Name:           "heartbeat-catch-up",
		Prompt:         "check",
		OriginThreadID: "origin-thread",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			RunMode:  RunModeHeartbeat,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Set(t0.Add(240 * time.Second))
	svc.Tick(ctx)

	if targets := fd.sentTargets(); len(targets) != 1 || targets[0] != "origin-thread" {
		t.Fatalf("expected one follow-up to origin-thread, got %v", targets)
	}
	runs, _ := svc.Runs(ctx, def.ID, 1)
	if len(runs) != 1 || runs[0].Trigger != RunTriggerCatchUp || runs[0].MissedCount != 3 {
		t.Fatalf("expected catch_up run with missed_count 3, got %+v", runs)
	}
}

// TestOnceMissedWithoutCatchUpRecordsSkippedAndDisables 验证错过 once 且关闭补跑时
// 记一条 skipped 运行并停用任务，不增加运行次数。
func TestOnceMissedWithoutCatchUpRecordsSkippedAndDisables(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	disabled := false
	date := t0.Add(time.Minute)
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "once-missed-disabled",
		Prompt:    "run",
		Spec: Spec{
			Schedule: Schedule{Type: "once", Date: &date},
			CatchUp:  &disabled,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Set(date.Add(5 * time.Minute))
	svc.Tick(ctx)

	if count := driver.startCount(); count != 0 {
		t.Fatalf("expected no start, got %d", count)
	}
	runs, err := svc.Runs(ctx, def.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 skipped run, got %d", len(runs))
	}
	skipped := runs[0]
	if skipped.Status != RunSkipped {
		t.Errorf("expected status %s, got %s", RunSkipped, skipped.Status)
	}
	if skipped.Trigger != RunTriggerCatchUp || skipped.MissedCount != 1 {
		t.Errorf("expected trigger catch_up with missed_count 1, got %q/%d", skipped.Trigger, skipped.MissedCount)
	}
	if skipped.ThreadID != nil {
		t.Errorf("expected skipped run without thread, got %v", skipped.ThreadID)
	}
	if skipped.FinishedAt == nil || !skipped.StartedAt.Equal(clock.Now()) || !skipped.FinishedAt.Equal(clock.Now()) {
		t.Errorf("expected skipped run finished immediately, got started=%v finished=%v", skipped.StartedAt, skipped.FinishedAt)
	}
	if skipped.ResultSummary == nil || !strings.Contains(*skipped.ResultSummary, "Missed one-time schedule") {
		t.Errorf("unexpected skipped summary: %v", skipped.ResultSummary)
	}

	updated, _ := svc.Get(ctx, def.ID)
	if updated.Enabled {
		t.Errorf("expected automation disabled after missed once")
	}
	if updated.Status != StatusCompleted {
		t.Errorf("expected status %s, got %s", StatusCompleted, updated.Status)
	}
	if updated.NextRunAt != nil {
		t.Errorf("expected next_run_at nil, got %v", updated.NextRunAt)
	}
	if updated.Spec.State.TotalRunCount != 0 {
		t.Errorf("expected total run count 0, got %d", updated.Spec.State.TotalRunCount)
	}
	if !strings.Contains(updated.Spec.State.StopReason, "Missed one-time schedule") {
		t.Errorf("unexpected stopReason: %s", updated.Spec.State.StopReason)
	}

	// 再次 Tick 不得重复记录 skipped。
	svc.Tick(ctx)
	runs, _ = svc.Runs(ctx, def.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("expected still 1 run after second tick, got %d", len(runs))
	}
}

// TestOnceMissedWithCatchUpRuns 验证 once 错过但开启补跑（默认）时正常补跑一次。
func TestOnceMissedWithCatchUpRuns(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	date := t0.Add(time.Minute)
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "once-missed-catch-up",
		Prompt:    "run",
		Spec:      Spec{Schedule: Schedule{Type: "once", Date: &date}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Set(date.Add(5 * time.Minute))
	svc.Tick(ctx)

	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected 1 start, got %d", count)
	}
	runs, _ := svc.Runs(ctx, def.ID, 1)
	if len(runs) != 1 || runs[0].Trigger != RunTriggerCatchUp || runs[0].MissedCount != 1 {
		t.Fatalf("expected catch_up run with missed_count 1, got %+v", runs)
	}
	updated, _ := svc.Get(ctx, def.ID)
	if updated.NextRunAt != nil {
		t.Fatalf("expected next_run_at nil for once, got %v", updated.NextRunAt)
	}

	// 会话结束后按一次性任务完成。
	driver.setThreadState(*runs[0].ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "done",
	})
	svc.Tick(ctx)

	completed, _ := svc.Get(ctx, def.ID)
	if completed.Enabled {
		t.Errorf("expected once automation completed")
	}
	if completed.Status != StatusCompleted {
		t.Errorf("expected status %s, got %s", StatusCompleted, completed.Status)
	}
	if !strings.Contains(completed.Spec.State.StopReason, "one-time schedule finished") {
		t.Errorf("unexpected stopReason: %s", completed.Spec.State.StopReason)
	}
}

// TestOnceWithinGraceIsScheduled 验证 once 到期后宽限期内仍按正常触发处理。
func TestOnceWithinGraceIsScheduled(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	date := t0.Add(30 * time.Second)
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "once-within-grace",
		Prompt:    "run",
		Spec:      Spec{Schedule: Schedule{Type: "once", Date: &date}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Set(date.Add(10 * time.Second))
	svc.Tick(ctx)

	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected 1 start, got %d", count)
	}
	runs, _ := svc.Runs(ctx, def.ID, 1)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Trigger != RunTriggerScheduled || runs[0].MissedCount != 0 {
		t.Errorf("expected scheduled run without missed points, got %q/%d", runs[0].Trigger, runs[0].MissedCount)
	}
}

// TestTriggerRejectsAtMaxRuns 验证达到次数上限的任务即使被重新启用也不会再启动运行。
func TestTriggerRejectsAtMaxRuns(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "max-runs-reject",
		Prompt:    "run once",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			MaxRuns:  1,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	runs, _ := svc.Runs(ctx, def.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if err := svc.ReportRun(ctx, def.ID, runs[0].ID, callerThread(runs[0]), RunSucceeded, "done", "", false); err != nil {
		t.Fatalf("report run: %v", err)
	}

	paused, _ := svc.Get(ctx, def.ID)
	if paused.Enabled {
		t.Fatalf("expected automation disabled at max runs")
	}

	// 重新启用已到上限的任务，RunNow 应前置拒绝并再次停用。
	enabled := true
	if _, err := svc.Update(ctx, def.ID, UpdateInput{Enabled: &enabled}, paused.Revision); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if _, err := svc.RunNow(ctx, def.ID); !errors.Is(err, ErrMaxRunsReached) {
		t.Fatalf("expected ErrMaxRunsReached, got %v", err)
	}
	runs, _ = svc.Runs(ctx, def.ID, 10)
	if len(runs) != 1 {
		t.Fatalf("expected no new run after rejection, got %d", len(runs))
	}
	final, _ := svc.Get(ctx, def.ID)
	if final.Enabled {
		t.Errorf("expected automation disabled after RunNow rejection")
	}
	if !strings.Contains(final.Spec.State.StopReason, "Reached maximum run count (1)") {
		t.Errorf("unexpected stopReason: %s", final.Spec.State.StopReason)
	}
}

// TestRunNowRecordsManualTrigger 验证手动运行单独标记来源，不占用补跑计数。
func TestRunNowRecordsManualTrigger(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "manual-trigger",
		Prompt:    "run now",
		Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	run, err := svc.RunNow(ctx, def.ID)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	if run.Trigger != RunTriggerManual {
		t.Errorf("expected trigger %s, got %s", RunTriggerManual, run.Trigger)
	}
	if run.MissedCount != 0 {
		t.Errorf("expected missed_count 0, got %d", run.MissedCount)
	}
}
