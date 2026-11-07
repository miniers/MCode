package automation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// receiptDriver 在 fakeDriver 之上实现 kanban.SessionMessenger，并记录投递到会话的文本，
// 用于验证回执提示词、追问文案和追问节奏。
type receiptDriver struct {
	*fakeDriver
	mu   sync.Mutex
	sent []sentPrompt
}

type sentPrompt struct {
	threadID string
	text     string
}

var _ kanban.SessionMessenger = (*receiptDriver)(nil)

func (d *receiptDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, sentPrompt{threadID: threadID, text: text})
	return nil
}

func (d *receiptDriver) sentTexts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	texts := make([]string, len(d.sent))
	for i, m := range d.sent {
		texts[i] = m.text
	}
	return texts
}

func (d *receiptDriver) countContaining(substr string) int {
	n := 0
	for _, text := range d.sentTexts() {
		if strings.Contains(text, substr) {
			n++
		}
	}
	return n
}

// setupReceiptService 与 setupTestService 相同，但把支持追问的驱动整体注入 Service。
func setupReceiptService(t *testing.T, clock *fakeClock) (*Service, *store.Store, *receiptDriver) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	driver := &receiptDriver{fakeDriver: newFakeDriver()}
	svc := New(Options{
		Store:    s,
		Driver:   driver,
		Projects: &fakeProjects{root: t.TempDir()},
		Now:      clock.Now,
		Interval: 10 * time.Millisecond,
		DataDir:  t.TempDir(),
		AcquireLock: func(string) (LockHandle, error) {
			return &fakeLock{}, nil
		},
	})
	return svc, s, driver
}

// latestRun 返回自动化最新的一次运行，没有时直接失败。
func latestRun(t *testing.T, svc *Service, ctx context.Context, automationID string) store.AutomationRun {
	t.Helper()
	runs, err := svc.Runs(ctx, automationID, 1)
	if err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatalf("expected a run for automation %s", automationID)
	}
	return runs[0]
}

// TestRunPromptIncludesReceiptRequirement 三种运行模式发出的提示词都必须带 run_id 与回执工具说明。
func TestRunPromptIncludesReceiptRequirement(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	assertPrompt := func(t *testing.T, prompt, automationID, runID string) {
		t.Helper()
		for _, want := range []string{"automation_report_result", automationID, runID} {
			if !strings.Contains(prompt, want) {
				t.Errorf("expected prompt to contain %q, got %q", want, prompt)
			}
		}
	}

	t.Run("standalone", func(t *testing.T) {
		clock := newFakeClock(t0)
		svc, s, driver := setupReceiptService(t, clock)
		defer s.Close()
		def, err := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1", Name: "prompt-standalone", Prompt: "run",
			Spec:    Spec{Schedule: Schedule{Type: "interval", Seconds: 60}, RunMode: RunModeStandalone},
			Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(60 * time.Second)
		svc.Tick(ctx)
		run := latestRun(t, svc, ctx, def.ID)
		if len(driver.starts) != 1 {
			t.Fatalf("expected 1 started session, got %d", len(driver.starts))
		}
		assertPrompt(t, driver.starts[0].Prompt, def.ID, run.ID)
	})

	t.Run("dedicated reuses thread", func(t *testing.T) {
		clock := newFakeClock(t0)
		svc, s, driver := setupReceiptService(t, clock)
		defer s.Close()
		def, err := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1", Name: "prompt-dedicated", Prompt: "run",
			Spec: Spec{
				Schedule:          Schedule{Type: "interval", Seconds: 60},
				RunMode:           RunModeDedicated,
				DedicatedThreadID: "thread-dedicated",
			},
			Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(60 * time.Second)
		svc.Tick(ctx)
		run := latestRun(t, svc, ctx, def.ID)
		texts := driver.sentTexts()
		if len(texts) != 1 {
			t.Fatalf("expected 1 delivered prompt, got %d", len(texts))
		}
		if driver.sent[0].threadID != "thread-dedicated" {
			t.Errorf("expected prompt delivered to dedicated thread, got %s", driver.sent[0].threadID)
		}
		assertPrompt(t, texts[0], def.ID, run.ID)
	})

	t.Run("heartbeat delivers to origin thread", func(t *testing.T) {
		clock := newFakeClock(t0)
		svc, s, driver := setupReceiptService(t, clock)
		defer s.Close()
		driver.setThreadState("origin-1", kanban.SessionState{Running: true, Progress: "p0"})
		def, err := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1", Name: "prompt-heartbeat", Prompt: "run",
			Spec: Spec{
				Schedule: Schedule{Type: "interval", Seconds: 60},
				RunMode:  RunModeHeartbeat,
			},
			OriginThreadID: "origin-1",
			Enabled:        true,
		})
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(60 * time.Second)
		svc.Tick(ctx)
		run := latestRun(t, svc, ctx, def.ID)
		texts := driver.sentTexts()
		if len(texts) != 1 {
			t.Fatalf("expected 1 delivered prompt, got %d", len(texts))
		}
		assertPrompt(t, texts[0], def.ID, run.ID)
	})
}

// TestReceiptReminderFlow 覆盖缺回执时的追问节奏与判失败。
func TestReceiptReminderFlow(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	svc, s, driver := setupReceiptService(t, clock)
	defer s.Close()

	const origin = "origin-1"
	driver.setThreadState(origin, kanban.SessionState{Running: true, Progress: "p0"})
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "receipt-reminder", Prompt: "run",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			RunMode:  RunModeHeartbeat,
		},
		OriginThreadID: origin,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(60 * time.Second)
	svc.Tick(ctx) // 触发，投递带 run_id 的提示词
	run := latestRun(t, svc, ctx, def.ID)
	// 会话空闲但没有调用 automation_report_result：进度已变化，说明这轮确实跑过。
	driver.setThreadState(origin, kanban.SessionState{Running: false, Progress: "p1", LastAssistantText: "done"})

	svc.Tick(ctx)
	if got := driver.countContaining("还没有提交运行结果"); got != 1 {
		t.Fatalf("expected 1 receipt reminder after idle run, got %d", got)
	}
	if status := latestRun(t, svc, ctx, def.ID).Status; status != RunRunning {
		t.Fatalf("expected run to stay %s after first reminder, got %s", RunRunning, status)
	}

	// 宽限期内不重复追问。
	svc.Tick(ctx)
	if got := driver.countContaining("还没有提交运行结果"); got != 1 {
		t.Fatalf("expected no reminder within grace, got %d reminders", got)
	}

	// 过了宽限再追问一次，随后仍无回执则判失败。
	clock.Advance(31 * time.Second)
	svc.Tick(ctx)
	if got := driver.countContaining("还没有提交运行结果"); got != 2 {
		t.Fatalf("expected second reminder after grace, got %d", got)
	}
	if runID := run.ID; svc.receiptAskOf(runID).Count != 2 {
		t.Fatalf("expected 2 recorded reminders, got %d", svc.receiptAskOf(runID).Count)
	}

	clock.Advance(31 * time.Second)
	svc.Tick(ctx)
	settled := latestRun(t, svc, ctx, def.ID)
	if settled.Status != RunFailed {
		t.Fatalf("expected run status %s after two ignored reminders, got %s", RunFailed, settled.Status)
	}
	if settled.Error == nil || !strings.Contains(*settled.Error, "已追问 2 次") {
		t.Fatalf("expected error to mention two reminders, got %v", settled.Error)
	}
	if ask := svc.receiptAskOf(run.ID); ask.Count != 0 {
		t.Errorf("expected receipt ask state cleared after settlement, got %d", ask.Count)
	}
}

// TestIdleRunWithoutMessengerSucceeds 不支持追问的驱动保持旧的「空闲即完成」行为。
func TestIdleRunWithoutMessengerSucceeds(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "idle-success", Prompt: "run",
		Spec:    Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	run := latestRun(t, svc, ctx, def.ID)
	driver.setThreadState(*run.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "finished"})
	svc.Tick(ctx)

	settled := latestRun(t, svc, ctx, def.ID)
	if settled.Status != RunSucceeded {
		t.Fatalf("expected run status %s without messenger, got %s", RunSucceeded, settled.Status)
	}
	if settled.ResultSummary == nil || *settled.ResultSummary != "finished" {
		t.Fatalf("expected LastAssistantText as summary, got %v", settled.ResultSummary)
	}
}

// TestIdleRunWithoutHostToolsSucceeds 会话的 provider 没有接入宿主工具时不追问：
// 即使驱动支持 SendFollowUp 也直接按最后一条助手文本记成功。
func TestIdleRunWithoutHostToolsSucceeds(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	svc, s, driver := setupReceiptService(t, clock)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "idle-without-host-tools", Prompt: "run",
		Spec:    Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	run := latestRun(t, svc, ctx, def.ID)

	driver.setThreadState(*run.ThreadID, kanban.SessionState{
		Running:              false,
		LastAssistantText:    "没有回执也算完成",
		HostToolsUnavailable: true,
	})
	svc.Tick(ctx)

	settled := latestRun(t, svc, ctx, def.ID)
	if settled.Status != RunSucceeded {
		t.Fatalf("宿主工具不可用时应直接记成功 %s，实际 %s", RunSucceeded, settled.Status)
	}
	if settled.ResultSummary == nil || *settled.ResultSummary != "没有回执也算完成" {
		t.Fatalf("期望用 LastAssistantText 作摘要，实际 %v", settled.ResultSummary)
	}
	if got := len(driver.sentTexts()); got != 0 {
		t.Fatalf("宿主工具不可用时不应追问，实际发送 %d 条", got)
	}
}

// TestReportRunStopsReminders Agent 主动提交回执后，Tick 不再追问。
func TestReportRunStopsReminders(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	svc, s, driver := setupReceiptService(t, clock)
	defer s.Close()

	const origin = "origin-1"
	driver.setThreadState(origin, kanban.SessionState{Running: true, Progress: "p0"})
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "report-stops-reminders", Prompt: "run",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			RunMode:  RunModeHeartbeat,
		},
		OriginThreadID: origin,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	run := latestRun(t, svc, ctx, def.ID)

	if err := svc.ReportRun(ctx, def.ID, run.ID, origin, RunSucceeded, "all done", "", false); err != nil {
		t.Fatalf("failed to report run: %v", err)
	}
	sentBefore := len(driver.sentTexts())

	driver.setThreadState(origin, kanban.SessionState{Running: false, Progress: "p1"})
	clock.Advance(31 * time.Second)
	svc.Tick(ctx)

	if got := len(driver.sentTexts()); got != sentBefore {
		t.Errorf("expected no reminder after report, got %d new messages", got-sentBefore)
	}
	if status := latestRun(t, svc, ctx, def.ID).Status; status != RunSucceeded {
		t.Errorf("expected run status %s after report, got %s", RunSucceeded, status)
	}
}

// TestHeartbeatProgressBaseline 投递后进度未变化视为排队未开跑，不结算；变化后进入正常流程。
func TestHeartbeatProgressBaseline(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	svc, s, driver := setupReceiptService(t, clock)
	defer s.Close()

	const origin = "origin-1"
	driver.setThreadState(origin, kanban.SessionState{Running: true, Progress: "p0"})
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "heartbeat-baseline", Prompt: "run",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			RunMode:  RunModeHeartbeat,
		},
		OriginThreadID: origin,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	run := latestRun(t, svc, ctx, def.ID)

	// 投递只是排队：会话空闲但进度与基线相同，这轮还没开跑。
	driver.setThreadState(origin, kanban.SessionState{Running: false, Progress: "p0"})
	svc.Tick(ctx)
	if status := latestRun(t, svc, ctx, def.ID).Status; status != RunRunning {
		t.Fatalf("expected run to stay %s while prompt has not started, got %s", RunRunning, status)
	}
	if got := driver.countContaining("还没有提交运行结果"); got != 0 {
		t.Fatalf("expected no receipt reminder before the run starts, got %d", got)
	}

	// 进度变化后不再被基线拦住，进入回执流程。
	driver.setThreadState(origin, kanban.SessionState{Running: false, Progress: "p1"})
	svc.Tick(ctx)
	if got := driver.countContaining("还没有提交运行结果"); got != 1 {
		t.Fatalf("expected receipt reminder after progress changed, got %d", got)
	}
	if _, ok := svc.runBaselineOf(run.ID); ok {
		t.Errorf("expected baseline cleared after progress changed")
	}

	// Agent 补交回执后正常结算。
	if err := svc.ReportRun(ctx, def.ID, run.ID, origin, RunSucceeded, "done", "", false); err != nil {
		t.Fatalf("failed to report run: %v", err)
	}
	if status := latestRun(t, svc, ctx, def.ID).Status; status != RunSucceeded {
		t.Fatalf("expected run status %s after report, got %s", RunSucceeded, status)
	}
}

// TestInterruptedRunSettlesStopped 用户中断记 stopped，不影响连续失败计数。
func TestInterruptedRunSettlesStopped(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "interrupted", Prompt: "run",
		Spec:    Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	run := latestRun(t, svc, ctx, def.ID)
	driver.setThreadState(*run.ThreadID, kanban.SessionState{Running: false, Interrupted: true})
	svc.Tick(ctx)

	settled := latestRun(t, svc, ctx, def.ID)
	if settled.Status != RunStopped {
		t.Fatalf("expected run status %s, got %s", RunStopped, settled.Status)
	}
	if settled.Error == nil || !strings.Contains(*settled.Error, "本轮被用户中断") {
		t.Fatalf("expected error to mention user interruption, got %v", settled.Error)
	}
	updated, err := svc.Get(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Spec.State.ConsecutiveFailures != 0 {
		t.Errorf("expected consecutive failures unchanged at 0, got %d", updated.Spec.State.ConsecutiveFailures)
	}
	if updated.NextRunAt == nil {
		t.Errorf("expected next run to be scheduled after stopped run")
	}
}

// TestHeartbeatInterruptedWaitsForProgress heartbeat 来源会话残留 Interrupted 时，
// 进度与基线一致说明本轮提示词还在排队，不能记 stopped；进度变化后才按中断结算。
func TestHeartbeatInterruptedWaitsForProgress(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	svc, s, driver := setupReceiptService(t, clock)
	defer s.Close()

	const origin = "origin-interrupted"
	driver.setThreadState(origin, kanban.SessionState{Running: true, Interrupted: true, Progress: "p0"})
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "heartbeat-interrupted", Prompt: "run",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			RunMode:  RunModeHeartbeat,
		},
		OriginThreadID: origin,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	run := latestRun(t, svc, ctx, def.ID)

	// 上一轮被用户中断后 Interrupted 一直为 true，本轮提示词还在排队。
	driver.setThreadState(origin, kanban.SessionState{Running: false, Interrupted: true, Progress: "p0"})
	svc.Tick(ctx)
	if status := latestRun(t, svc, ctx, def.ID).Status; status != RunRunning {
		t.Fatalf("expected run to stay %s while prompt has not started, got %s", RunRunning, status)
	}
	if _, ok := svc.runBaselineOf(run.ID); !ok {
		t.Errorf("expected baseline kept while prompt has not started")
	}

	// 进度变化后越过基线判断，此时 Interrupted 仍为 true 才记 stopped。
	driver.setThreadState(origin, kanban.SessionState{Running: false, Interrupted: true, Progress: "p1"})
	svc.Tick(ctx)
	settled := latestRun(t, svc, ctx, def.ID)
	if settled.Status != RunStopped {
		t.Fatalf("expected run status %s after progress changed, got %s", RunStopped, settled.Status)
	}
	if settled.Error == nil || !strings.Contains(*settled.Error, "本轮被用户中断") {
		t.Errorf("expected error to mention user interruption, got %v", settled.Error)
	}
}

// TestTickReconcilesRunningRuns 首次持有调度锁的 Tick 对账 DB 中残留的 running 运行；
// 被动实例拿不到锁，不得对账。
func TestTickReconcilesRunningRuns(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	// 残留的运行不一定属于启用中的自动化，这里用暂停的自动化也应被对账。
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1", Name: "reconcile", Prompt: "run",
		Spec: Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
	})
	if err != nil {
		t.Fatal(err)
	}

	noThread := store.AutomationRun{
		ID: uuid.NewString(), AutomationID: def.ID, Status: RunRunning, StartedAt: t0,
	}
	if err := s.CreateAutomationRun(ctx, noThread); err != nil {
		t.Fatal(err)
	}
	threadID := "thread-a"
	withThread := store.AutomationRun{
		ID: uuid.NewString(), AutomationID: def.ID, ThreadID: &threadID, Status: RunRunning, StartedAt: t0.Add(time.Second),
	}
	if err := s.CreateAutomationRun(ctx, withThread); err != nil {
		t.Fatal(err)
	}
	driver.setThreadState(threadID, kanban.SessionState{Running: true})

	// 被动实例（拿不到跨进程调度锁）不能改动别人名下的在途运行。
	passive := New(Options{
		Store:    s,
		Driver:   driver,
		Projects: &fakeProjects{root: t.TempDir()},
		Now:      clock.Now,
		Interval: 10 * time.Millisecond,
		DataDir:  t.TempDir(),
		AcquireLock: func(string) (LockHandle, error) {
			return nil, errors.New("lock held by another process")
		},
	})
	passive.Tick(ctx)
	if status := latestRun(t, svc, ctx, def.ID).Status; status != RunRunning {
		t.Errorf("passive instance must not settle runs, got %s", status)
	}
	passive.inFlightMu.Lock()
	passiveInFlight := len(passive.inFlight)
	passive.inFlightMu.Unlock()
	if passiveInFlight != 0 {
		t.Errorf("passive instance must not register in-flight runs, got %d", passiveInFlight)
	}

	// 对账结算的回调在 Tick 内触发，此时 app 层接线已经就绪。
	var settledByCallback []string
	svc.opts.OnRunSettled = func(def Definition, run store.AutomationRun) {
		settledByCallback = append(settledByCallback, run.ID)
	}
	svc.Tick(ctx)

	failedRun, err := s.GetAutomationRun(ctx, noThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedRun.Status != RunFailed {
		t.Errorf("expected run without thread to be %s, got %s", RunFailed, failedRun.Status)
	}
	if failedRun.Error == nil || !strings.Contains(*failedRun.Error, "mcode 重启时本轮尚未开始") {
		t.Errorf("unexpected reconcile error: %v", failedRun.Error)
	}
	if len(settledByCallback) != 1 || settledByCallback[0] != noThread.ID {
		t.Errorf("expected reconcile to notify OnRunSettled for %s, got %v", noThread.ID, settledByCallback)
	}

	runningRun, err := s.GetAutomationRun(ctx, withThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runningRun.Status != RunRunning {
		t.Errorf("expected run with thread to stay %s, got %s", RunRunning, runningRun.Status)
	}
	svc.inFlightMu.Lock()
	inFlightID := svc.inFlight[def.ID]
	svc.inFlightMu.Unlock()
	if inFlightID != withThread.ID {
		t.Errorf("expected run %s registered in flight, got %q", withThread.ID, inFlightID)
	}
}

// TestHeuristicEmptyDiffNotClean 启发式评估不再把空 diff 当成工作区干净的证据。
func TestHeuristicEmptyDiffNotClean(t *testing.T) {
	empty := Heuristic("working tree is clean", "Ran checks", "")
	if empty.Satisfied {
		t.Errorf("expected empty diff not to satisfy clean condition, got %+v", empty)
	}
	marker := Heuristic("working tree is clean", "Ran checks", NoCodeChangesMarker)
	if !marker.Satisfied {
		t.Errorf("expected explicit no-code-changes marker to satisfy clean condition, got %+v", marker)
	}
	phrase := Heuristic("working tree is clean", "All good, working tree clean", "")
	if !phrase.Satisfied {
		t.Errorf("expected clean phrase in summary to satisfy clean condition, got %+v", phrase)
	}
}
