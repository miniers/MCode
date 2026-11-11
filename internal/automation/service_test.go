package automation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start.UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

type fakeDriver struct {
	mu           sync.Mutex
	starts       []kanban.SessionRequest
	states       map[string]kanban.SessionState
	defaultState kanban.SessionState
	startErr     error
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		states: make(map[string]kanban.SessionState),
		defaultState: kanban.SessionState{
			Running:           true,
			LastAssistantText: "working",
		},
	}
}

func (d *fakeDriver) StartSession(ctx context.Context, req kanban.SessionRequest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.startErr != nil {
		return "", d.startErr
	}
	d.starts = append(d.starts, req)
	threadID := uuid.NewString()
	d.states[threadID] = d.defaultState
	return threadID, nil
}

func (d *fakeDriver) SessionState(ctx context.Context, threadID string) (kanban.SessionState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.states[threadID]; ok {
		return st, nil
	}
	return d.defaultState, nil
}

func (d *fakeDriver) setThreadState(threadID string, st kanban.SessionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[threadID] = st
}

func (d *fakeDriver) startCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.starts)
}

type fakeProjects struct {
	root string
}

func (p *fakeProjects) ProjectRoot(ctx context.Context, projectID string) (string, error) {
	return p.root, nil
}

func setupTestService(t *testing.T, clock *fakeClock, driver *fakeDriver) (*Service, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}

	projects := &fakeProjects{root: t.TempDir()}
	svc := New(Options{
		Store:    s,
		Driver:   driver,
		Projects: projects,
		Now:      clock.Now,
		Interval: 10 * time.Millisecond,
		DataDir:  t.TempDir(),
		// 与调度锁无关的用例注入总是成功的假锁，避免测试之间争抢真实锁文件。
		AcquireLock: func(string) (LockHandle, error) { return &fakeLock{}, nil },
	})
	return svc, s
}

// callerThread 返回运行记录绑定的线程，供测试按所有权规则结算。
func callerThread(run store.AutomationRun) string {
	if run.ThreadID == nil {
		return ""
	}
	return *run.ThreadID
}

func TestServiceChangeNotifications(t *testing.T) {
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	svc, db := setupTestService(t, clock, driver)
	defer db.Close()
	ctx := context.Background()
	changes := 0
	svc.SetOnChange(func() { changes++ })
	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "p1", Name: "通知测试", Prompt: "检查",
		Spec: Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "更新通知测试"
	if _, err := svc.Update(ctx, def.ID, UpdateInput{Name: &name}, def.Revision); err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("create/update notifications: %d", changes)
	}
	run, err := svc.RunNow(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changes != 3 {
		t.Fatalf("run notification missing: %d", changes)
	}
	if err := svc.ReportRun(ctx, def.ID, run.ID, callerThread(run), RunSucceeded, "完成", "", false); err != nil {
		t.Fatal(err)
	}
	if changes != 4 {
		t.Fatalf("settlement notification missing: %d", changes)
	}
	if err := svc.Delete(ctx, def.ID); err != nil {
		t.Fatal(err)
	}
	if changes != 5 {
		t.Fatalf("delete notification missing: %d", changes)
	}
}

func TestOnTimeTrigger(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-on-time",
		Prompt:    "echo hello",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create automation: %v", err)
	}

	// +30s: 尚未到达触发时间，不触发任何会话
	clock.Advance(30 * time.Second)
	svc.Tick(ctx)
	if count := driver.startCount(); count != 0 {
		t.Fatalf("expected 0 starts at +30s, got %d", count)
	}

	// +60s: 到达触发时间，恰好触发一次
	clock.Advance(30 * time.Second)
	svc.Tick(ctx)
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected 1 start at +60s, got %d", count)
	}

	updated, err := svc.Get(ctx, def.ID)
	if err != nil {
		t.Fatalf("failed to get automation: %v", err)
	}
	expectedNext := t0.Add(120 * time.Second).UTC()
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(expectedNext) {
		t.Errorf("expected next_run_at %v, got %v", expectedNext, updated.NextRunAt)
	}
}

func TestScheduledCheck(t *testing.T) {
	for _, tc := range []struct {
		check string
		want  int
	}{{"true", 1}, {"false", 0}} {
		t.Run(tc.check, func(t *testing.T) {
			ctx := context.Background()
			clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			driver := newFakeDriver()
			svc, db := setupTestService(t, clock, driver)
			defer db.Close()
			def, err := svc.Create(ctx, CreateInput{
				ProjectID: "p1", Name: "检查", Prompt: "检查",
				Spec: Spec{Schedule: Schedule{Type: "interval", Seconds: 60}, Check: tc.check}, Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := svc.Get(ctx, def.ID)
			if err != nil || stored.Spec.Check != tc.check {
				t.Fatalf("检查命令未保存: %q, %v", stored.Spec.Check, err)
			}
			clock.Advance(time.Minute)
			now := clock.Now()
			svc.triggerDue(ctx, stored.Automation, now)
			runs, err := svc.Runs(ctx, def.ID, 10)
			if err != nil || len(runs) != tc.want || driver.startCount() != tc.want {
				t.Fatalf("运行记录 %d，会话 %d，错误 %v", len(runs), driver.startCount(), err)
			}
			updated, err := svc.Get(ctx, def.ID)
			if err != nil || updated.NextRunAt == nil || !updated.NextRunAt.After(now) {
				t.Fatalf("下次运行时间未推进: %v, %v", updated.NextRunAt, err)
			}
		})
	}
}

func TestCheckRejectsWriteCommands(t *testing.T) {
	ctx := context.Background()
	svc, db := setupTestService(t, newFakeClock(time.Now()), newFakeDriver())
	defer db.Close()
	in := CreateInput{ProjectID: "p1", Name: "检查", Prompt: "检查", Spec: Spec{
		Schedule: Schedule{Type: "interval", Seconds: 60}, Check: "rm -rf x",
	}}
	if _, err := svc.Create(ctx, in); err == nil || !strings.Contains(err.Error(), "检查命令必须是只读命令") {
		t.Fatalf("创建时应拒绝写命令: %v", err)
	}
	in.Spec.Check = "true"
	def, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	def.Spec.Check = "rm -rf x"
	if _, err := svc.Update(ctx, def.ID, UpdateInput{Spec: &def.Spec}, def.Revision); err == nil || !strings.Contains(err.Error(), "检查命令必须是只读命令") {
		t.Fatalf("更新时应拒绝写命令: %v", err)
	}
}

func TestMissedWindow(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-missed-window",
		Prompt:    "daily run",
		Spec: Spec{
			Schedule: Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: "UTC"},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create automation: %v", err)
	}

	// 时钟跃进 3 天到 2026-10-20 10:00:00 UTC，跨过 3 个触发窗口
	clock.Set(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	svc.Tick(ctx)

	// 只补偿执行 1 次，而非按错过的窗口重放 3 次
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected exactly 1 start after jump, got %d", count)
	}

	updated, err := svc.Get(ctx, def.ID)
	if err != nil {
		t.Fatalf("failed to get automation: %v", err)
	}
	// 下次执行时间应为当前时间之后的下一个 09:00 (2026-10-21 09:00:00 UTC)
	expectedNext := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(expectedNext) {
		t.Errorf("expected next_run_at %v, got %v", expectedNext, updated.NextRunAt)
	}
}

func TestMissedWindowTimezones(t *testing.T) {
	tests := []struct {
		name     string
		tz       string
		start    time.Time
		jumpTo   time.Time
		wantNext time.Time
	}{
		{
			name:     "America/New_York",
			tz:       "America/New_York",
			start:    time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
			jumpTo:   time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC), // 本地 10/20 16:00，已错过 10/18 10/19 10/20 三次
			wantNext: time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC), // 10/21 09:00 EDT
		},
		{
			name:     "Asia/Shanghai",
			tz:       "Asia/Shanghai",
			start:    time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
			jumpTo:   time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC), // 本地 10/20 16:00
			wantNext: time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC), // 10/21 09:00 CST
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newFakeClock(tt.start)
			driver := newFakeDriver()
			svc, s := setupTestService(t, clock, driver)
			defer s.Close()

			def, err := svc.Create(ctx, CreateInput{
				ProjectID: "proj-1",
				Name:      "tz-missed",
				Prompt:    "daily run",
				Spec:      Spec{Schedule: Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: tt.tz}},
				Enabled:   true,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			clock.Set(tt.jumpTo)
			svc.Tick(ctx)
			svc.Tick(ctx) // 第二次 tick 不得再次补跑
			if count := driver.startCount(); count != 1 {
				t.Fatalf("expected exactly 1 start, got %d", count)
			}
			updated, _ := svc.Get(ctx, def.ID)
			if updated.NextRunAt == nil || !updated.NextRunAt.Equal(tt.wantNext) {
				t.Errorf("expected next_run_at %v, got %v", tt.wantNext, updated.NextRunAt)
			}
		})
	}
}

func TestNoReentry(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver() // 默认 Running = true
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-reentry",
		Prompt:    "long running",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create automation: %v", err)
	}

	// 首次触发
	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected 1 start, got %d", count)
	}

	// 会话保持 running，时钟推进跳过下一个时间槽，再次 Tick 仍然不会重入
	clock.Advance(120 * time.Second)
	svc.Tick(ctx)
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected still 1 start when previous run in flight, got %d", count)
	}

	// 8 个并发协程同时调用 Tick，不会并发重入启动
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.Tick(ctx)
		}()
	}
	wg.Wait()
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected still 1 start after concurrent ticks, got %d", count)
	}

	// 在进行中调用 RunNow 应返回 ErrAlreadyRunning
	_, rerr := svc.RunNow(ctx, def.ID)
	if !errors.Is(rerr, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", rerr)
	}
}

func TestSettlement(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	t.Run("success schedules next run", func(t *testing.T) {
		clock := newFakeClock(t0)
		driver := newFakeDriver()
		svc, s := setupTestService(t, clock, driver)
		defer s.Close()

		def, _ := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1",
			Name:      "test-success",
			Prompt:    "run",
			Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
			Enabled:   true,
		})

		clock.Advance(60 * time.Second)
		svc.Tick(ctx) // 触发

		runs, _ := svc.Runs(ctx, def.ID, 1)
		threadID := *runs[0].ThreadID

		// 会话执行完成
		driver.setThreadState(threadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "job completed cleanly",
		})
		svc.Tick(ctx) // 结算

		settledRuns, _ := svc.Runs(ctx, def.ID, 1)
		if settledRuns[0].Status != RunSucceeded {
			t.Fatalf("expected run status %s, got %s", RunSucceeded, settledRuns[0].Status)
		}
		if settledRuns[0].ResultSummary == nil || *settledRuns[0].ResultSummary != "job completed cleanly" {
			t.Fatalf("expected summary 'job completed cleanly', got %v", settledRuns[0].ResultSummary)
		}

		updated, _ := svc.Get(ctx, def.ID)
		if updated.NextRunAt == nil {
			t.Fatalf("expected next run to be scheduled")
		}
	})

	t.Run("awaiting input does not settle as success", func(t *testing.T) {
		clock := newFakeClock(t0)
		driver := newFakeDriver()
		svc, s := setupTestService(t, clock, driver)
		defer s.Close()

		def, _ := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1",
			Name:      "test-awaiting-input",
			Prompt:    "run",
			Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
			Enabled:   true,
		})

		clock.Advance(60 * time.Second)
		svc.Tick(ctx) // 触发

		runs, _ := svc.Runs(ctx, def.ID, 1)
		runID := runs[0].ID
		threadID := *runs[0].ThreadID
		statusOf := func() string {
			all, _ := svc.Runs(ctx, def.ID, 10)
			for _, r := range all {
				if r.ID == runID {
					return r.Status
				}
			}
			return ""
		}

		// 智能体反问用户：会话没结束，既不能计成功、也不能清零失败计数
		driver.setThreadState(threadID, kanban.SessionState{
			Running:           false,
			AwaitingInput:     true,
			LastAssistantText: "which branch should I use?",
		})
		svc.Tick(ctx)
		if status := statusOf(); status != RunRunning {
			t.Fatalf("expected run to stay %s while awaiting input, got %s", RunRunning, status)
		}

		// 等待输入超时后按失败结算
		clock.Advance(awaitingInputTimeout + time.Second)
		svc.Tick(ctx)
		if status := statusOf(); status != RunFailed {
			t.Fatalf("expected run status %s after awaiting-input timeout, got %s", RunFailed, status)
		}
		updated, _ := svc.Get(ctx, def.ID)
		if updated.Spec.State.ConsecutiveFailures != 1 {
			t.Errorf("expected consecutive failures 1, got %d", updated.Spec.State.ConsecutiveFailures)
		}
	})

	t.Run("three failures disables automation", func(t *testing.T) {
		clock := newFakeClock(t0)
		driver := newFakeDriver()
		svc, s := setupTestService(t, clock, driver)
		defer s.Close()

		def, _ := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1",
			Name:      "test-fail-limit",
			Prompt:    "run",
			Spec: Spec{
				Schedule:               Schedule{Type: "interval", Seconds: 60},
				MaxConsecutiveFailures: 3,
			},
			Enabled: true,
		})

		for i := 1; i <= 3; i++ {
			clock.Advance(60 * time.Second)
			svc.Tick(ctx) // 触发
			runs, _ := svc.Runs(ctx, def.ID, 1)
			threadID := *runs[0].ThreadID
			driver.setThreadState(threadID, kanban.SessionState{
				Running:       false,
				Failed:        true,
				FailureReason: "failure reason",
			})
			svc.Tick(ctx) // 结算
		}

		updated, _ := svc.Get(ctx, def.ID)
		if updated.Enabled {
			t.Errorf("expected automation to be disabled after 3 failures")
		}
		if updated.Status != StatusFailed {
			t.Errorf("expected status %s, got %s", StatusFailed, updated.Status)
		}
		if !strings.Contains(updated.Spec.State.StopReason, "Reached maximum consecutive failure limit (3)") {
			t.Errorf("unexpected stopReason: %s", updated.Spec.State.StopReason)
		}
	})

	t.Run("stop condition satisfied completes automation", func(t *testing.T) {
		clock := newFakeClock(t0)
		driver := newFakeDriver()
		svc, s := setupTestService(t, clock, driver)
		defer s.Close()

		def, _ := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1",
			Name:      "test-stop-condition",
			Prompt:    "run tests",
			Spec: Spec{
				Schedule: Schedule{Type: "interval", Seconds: 60},
				StopWhen: "all tests pass",
			},
			Enabled: true,
		})

		clock.Advance(60 * time.Second)
		svc.Tick(ctx) // 触发

		runs, _ := svc.Runs(ctx, def.ID, 1)
		threadID := *runs[0].ThreadID
		driver.setThreadState(threadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "tests finished: all checks passed",
		})
		svc.Tick(ctx) // 结算

		updated, _ := svc.Get(ctx, def.ID)
		if updated.Enabled {
			t.Errorf("expected automation to be disabled upon stop condition")
		}
		if updated.Status != StatusCompleted {
			t.Errorf("expected status %s, got %s", StatusCompleted, updated.Status)
		}
		if !strings.Contains(updated.Spec.State.StopReason, "Stop condition satisfied") {
			t.Errorf("unexpected stopReason: %s", updated.Spec.State.StopReason)
		}
	})

	t.Run("max runs completes automation", func(t *testing.T) {
		clock := newFakeClock(t0)
		driver := newFakeDriver()
		svc, s := setupTestService(t, clock, driver)
		defer s.Close()

		def, _ := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1",
			Name:      "test-max-runs",
			Prompt:    "run once",
			Spec: Spec{
				Schedule: Schedule{Type: "interval", Seconds: 60},
				MaxRuns:  1,
			},
			Enabled: true,
		})

		clock.Advance(60 * time.Second)
		svc.Tick(ctx) // 触发

		runs, _ := svc.Runs(ctx, def.ID, 1)
		threadID := *runs[0].ThreadID
		driver.setThreadState(threadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "done 1 iteration",
		})
		svc.Tick(ctx) // 结算

		updated, _ := svc.Get(ctx, def.ID)
		if updated.Enabled {
			t.Errorf("expected automation to be disabled after max iterations")
		}
		if updated.Status != StatusCompleted {
			t.Errorf("expected status %s, got %s", StatusCompleted, updated.Status)
		}
		if !strings.Contains(updated.Spec.State.StopReason, "Reached maximum run count (1)") {
			t.Errorf("unexpected stopReason: %s", updated.Spec.State.StopReason)
		}
	})

	t.Run("start session error marks run failed", func(t *testing.T) {
		clock := newFakeClock(t0)
		driver := newFakeDriver()
		driver.startErr = errors.New("cannot start session")
		svc, s := setupTestService(t, clock, driver)
		defer s.Close()

		def, _ := svc.Create(ctx, CreateInput{
			ProjectID: "proj-1",
			Name:      "test-start-err",
			Prompt:    "run",
			Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
			Enabled:   true,
		})

		clock.Advance(60 * time.Second)
		svc.Tick(ctx) // 触发但 StartSession 报错

		runs, err := svc.Runs(ctx, def.ID, 1)
		if err != nil || len(runs) != 1 {
			t.Fatalf("expected 1 run, got %v", runs)
		}
		if runs[0].Status != RunFailed {
			t.Errorf("expected run status %s, got %s", RunFailed, runs[0].Status)
		}
		if runs[0].Error == nil || !strings.Contains(*runs[0].Error, "cannot start session") {
			t.Errorf("expected error to mention 'cannot start session', got %v", runs[0].Error)
		}
	})
}

func TestRunNowConcurrentWithTick(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-runnow-concurrent-tick",
		Prompt:    "echo test",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create automation: %v", err)
	}

	// 推进时钟使其超过下一次触发时间
	clock.Advance(61 * time.Second)

	// 保持 fake 会话为 Running 状态（newFakeDriver 默认即为 Running）
	driver.defaultState.Running = true

	// 启动 8 个并发 goroutine 交替执行 svc.Tick(ctx) 与 svc.RunNow(ctx, id)
	var wg sync.WaitGroup
	const workers = 8
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				svc.Tick(ctx)
			} else {
				_, _ = svc.RunNow(ctx, def.ID)
			}
		}(i)
	}
	wg.Wait()

	// 校验 Driver 的 StartSession 仅触发了 1 次，且 Runs 返回恰好 1 次记录
	if count := driver.startCount(); count != 1 {
		t.Fatalf("expected exactly 1 StartSession call, got %d", count)
	}
	runs, err := svc.Runs(ctx, def.ID, 10)
	if err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 run, got %d", len(runs))
	}
}

func TestReportRun(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-report-run",
		Prompt:    "check",
		Spec: Spec{
			Schedule:               Schedule{Type: "interval", Seconds: 60},
			StopWhen:               "all checks passed",
			MaxConsecutiveFailures: 2,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("failed to create automation: %v", err)
	}

	// 1. 运行不存在 / 状态非法
	if err := svc.ReportRun(ctx, def.ID, "missing-run", "", RunSucceeded, "ok", "", false); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}
	run, err := svc.RunNow(ctx, def.ID)
	if err != nil {
		t.Fatalf("RunNow failed: %v", err)
	}
	if err := svc.ReportRun(ctx, def.ID, run.ID, callerThread(run), "unknown", "ok", "", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if err := svc.ReportRun(ctx, "other-automation", run.ID, callerThread(run), RunSucceeded, "ok", "", false); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound for foreign automation, got %v", err)
	}

	// 2. 报告失败：运行结算、失败计数递增、自动化保持启用
	if err := svc.ReportRun(ctx, def.ID, run.ID, callerThread(run), RunFailed, "boom", "", false); err != nil {
		t.Fatalf("ReportRun failed: %v", err)
	}
	storedRun, err := s.GetAutomationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("failed to load run: %v", err)
	}
	if storedRun.Status != RunFailed {
		t.Fatalf("expected run status %s, got %s", RunFailed, storedRun.Status)
	}
	if storedRun.Error == nil || *storedRun.Error != "boom" {
		t.Fatalf("expected run error 'boom', got %v", storedRun.Error)
	}
	if storedRun.FinishedAt == nil {
		t.Fatalf("expected FinishedAt to be set")
	}
	updated, err := svc.Get(ctx, def.ID)
	if err != nil {
		t.Fatalf("failed to reload automation: %v", err)
	}
	if updated.Spec.State.ConsecutiveFailures != 1 {
		t.Fatalf("expected 1 consecutive failure, got %d", updated.Spec.State.ConsecutiveFailures)
	}
	if updated.Spec.State.LastRunStatus != RunFailed {
		t.Fatalf("expected last run status %s, got %s", RunFailed, updated.Spec.State.LastRunStatus)
	}
	if !updated.Enabled {
		t.Fatalf("expected automation to stay enabled below the failure limit")
	}

	// 3. 重复结算被拒绝
	if err := svc.ReportRun(ctx, def.ID, run.ID, callerThread(run), RunSucceeded, "again", "", false); !errors.Is(err, ErrRunAlreadySettled) {
		t.Fatalf("expected ErrRunAlreadySettled, got %v", err)
	}

	// 4. 再次失败达到上限：暂停并记录原因
	run2, err := svc.RunNow(ctx, def.ID)
	if err != nil {
		t.Fatalf("second RunNow failed: %v", err)
	}
	if err := svc.ReportRun(ctx, def.ID, run2.ID, callerThread(run2), RunFailed, "boom again", "", false); err != nil {
		t.Fatalf("ReportRun failed: %v", err)
	}
	paused, err := svc.Get(ctx, def.ID)
	if err != nil {
		t.Fatalf("failed to reload automation: %v", err)
	}
	if paused.Enabled {
		t.Fatalf("expected automation to pause after reaching the failure limit")
	}
	if paused.Status != StatusFailed {
		t.Fatalf("expected status %s, got %s", StatusFailed, paused.Status)
	}
	if !strings.Contains(paused.Spec.State.StopReason, "maximum consecutive failure limit (2)") {
		t.Fatalf("unexpected stop reason: %s", paused.Spec.State.StopReason)
	}

	// 5. 报告成功且满足停止条件：完成并记录原因
	enabled := true
	if _, err := svc.Update(ctx, def.ID, UpdateInput{Enabled: &enabled}, paused.Revision); err != nil {
		t.Fatalf("failed to re-enable automation: %v", err)
	}
	run3, err := svc.RunNow(ctx, def.ID)
	if err != nil {
		t.Fatalf("third RunNow failed: %v", err)
	}
	if err := svc.ReportRun(ctx, def.ID, run3.ID, callerThread(run3), RunSucceeded, "all checks passed", "no failing checks", true); err != nil {
		t.Fatalf("ReportRun failed: %v", err)
	}
	completed, err := svc.Get(ctx, def.ID)
	if err != nil {
		t.Fatalf("failed to reload automation: %v", err)
	}
	if completed.Enabled {
		t.Fatalf("expected automation to complete and stop")
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("expected status %s, got %s", StatusCompleted, completed.Status)
	}
	if completed.Spec.State.StopReason != "Completed: Stop condition satisfied (no failing checks)." {
		t.Fatalf("unexpected stop reason: %s", completed.Spec.State.StopReason)
	}
	if completed.Spec.State.ConsecutiveFailures != 0 {
		t.Fatalf("expected consecutive failures reset, got %d", completed.Spec.State.ConsecutiveFailures)
	}
}

func TestMaxRunsLegacyMaxIterationsFallback(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	// 旧数据只有 MaxIterations：达到上限后仍应自动停用。
	def, _ := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-legacy-max-iterations",
		Prompt:    "run once",
		Spec: Spec{
			Schedule:      Schedule{Type: "interval", Seconds: 60},
			MaxIterations: 1,
		},
		Enabled: true,
	})

	clock.Advance(60 * time.Second)
	svc.Tick(ctx)

	runs, _ := svc.Runs(ctx, def.ID, 1)
	driver.setThreadState(*runs[0].ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "done",
	})
	svc.Tick(ctx)

	updated, _ := svc.Get(ctx, def.ID)
	if updated.Enabled {
		t.Fatalf("expected automation to be disabled by legacy maxIterations")
	}
	if updated.Spec.EffectiveMaxRuns() != 1 {
		t.Fatalf("expected effective max runs 1, got %d", updated.Spec.EffectiveMaxRuns())
	}
	if !strings.Contains(updated.Spec.State.StopReason, "Reached maximum run count (1)") {
		t.Errorf("unexpected stopReason: %s", updated.Spec.State.StopReason)
	}
}

func TestSettleDisablesAtMaxRuns(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()

	def, _ := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "test-settle-max-runs",
		Prompt:    "run once",
		Spec: Spec{
			Schedule: Schedule{Type: "interval", Seconds: 60},
			MaxRuns:  1,
		},
		Enabled: true,
	})

	clock.Advance(60 * time.Second)
	svc.Tick(ctx)

	runs, _ := svc.Runs(ctx, def.ID, 1)
	if runs[0].Trigger != RunTriggerScheduled || runs[0].MissedCount != 0 {
		t.Fatalf("expected scheduled run without missed points, got trigger=%q missed=%d", runs[0].Trigger, runs[0].MissedCount)
	}
	driver.setThreadState(*runs[0].ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "done",
	})
	svc.Tick(ctx)

	updated, _ := svc.Get(ctx, def.ID)
	if updated.Enabled {
		t.Fatalf("expected automation to be disabled at max runs")
	}
	if updated.Status != StatusCompleted {
		t.Fatalf("expected status %s, got %s", StatusCompleted, updated.Status)
	}
	if !strings.Contains(updated.Spec.State.StopReason, "Reached maximum run count (1)") {
		t.Errorf("unexpected stopReason: %s", updated.Spec.State.StopReason)
	}
}

// createThreadAutomation 以指定会话身份创建一条启用中的定时任务。
func createThreadAutomation(t *testing.T, svc *Service, projectID, threadID string) Definition {
	t.Helper()
	def, err := svc.Create(context.Background(), CreateInput{
		ProjectID:       projectID,
		Name:            fmt.Sprintf("会话任务-%s", threadID),
		Prompt:          "检查",
		Enabled:         true,
		CreatorThreadID: threadID,
		Spec:            Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}},
	})
	if err != nil {
		t.Fatalf("创建定时任务失败: %v", err)
	}
	return def
}

// TestCreateEnforcesPerThreadLimit 验证同一会话建满 50 条后拒绝新建，停用一条可释放额度。
func TestCreateEnforcesPerThreadLimit(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	svc, db := setupTestService(t, clock, newFakeDriver())
	defer db.Close()

	const threadID = "thread-limit"
	var first Definition
	for i := 0; i < MaxActiveAutomationsPerThread; i++ {
		def := createThreadAutomation(t, svc, "p1", threadID)
		if def.Spec.CreatorThreadID != threadID {
			t.Fatalf("CreatorThreadID 未写入 spec: %q", def.Spec.CreatorThreadID)
		}
		if i == 0 {
			first = def
		}
	}

	_, err := svc.Create(ctx, CreateInput{
		ProjectID:       "p1",
		Name:            "超限任务",
		Prompt:          "检查",
		Enabled:         true,
		CreatorThreadID: threadID,
		Spec:            Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("第 %d 条应返回 ErrInvalidInput, 实际为 %v", MaxActiveAutomationsPerThread+1, err)
	}
	if !strings.Contains(err.Error(), "50") {
		t.Fatalf("错误文案应包含上限 50: %v", err)
	}

	// 停用一条后额度释放。
	disabled := false
	if _, err := svc.Update(ctx, first.ID, UpdateInput{Enabled: &disabled}, first.Revision); err != nil {
		t.Fatalf("停用任务失败: %v", err)
	}
	if _, err := svc.Create(ctx, CreateInput{
		ProjectID:       "p1",
		Name:            "停用后新建",
		Prompt:          "检查",
		Enabled:         true,
		CreatorThreadID: threadID,
		Spec:            Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}},
	}); err != nil {
		t.Fatalf("停用一条后应能继续新建: %v", err)
	}
}

// TestCreatePerThreadLimitIsolatedByThread 验证不同会话的上限互不影响。
func TestCreatePerThreadLimitIsolatedByThread(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	svc, db := setupTestService(t, clock, newFakeDriver())
	defer db.Close()

	const fullThread = "thread-full"
	for i := 0; i < MaxActiveAutomationsPerThread; i++ {
		createThreadAutomation(t, svc, "p1", fullThread)
	}

	createThreadAutomation(t, svc, "p1", "thread-other")

	if _, err := svc.Create(ctx, CreateInput{
		ProjectID:       "p1",
		Name:            "仍然超限",
		Prompt:          "检查",
		Enabled:         true,
		CreatorThreadID: fullThread,
		Spec:            Spec{Schedule: Schedule{Type: "interval", Seconds: 3600}},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("已满会话仍应被拦截, 实际为 %v", err)
	}
}

// TestCreateWithoutCreatorThreadHasNoLimit 验证 UI 手动创建（不带来源会话）不设上限。
func TestCreateWithoutCreatorThreadHasNoLimit(t *testing.T) {
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	svc, db := setupTestService(t, clock, newFakeDriver())
	defer db.Close()

	for i := 0; i < MaxActiveAutomationsPerThread+1; i++ {
		def := createThreadAutomation(t, svc, "p1", "")
		if def.Spec.CreatorThreadID != "" {
			t.Fatalf("无来源会话时不应写入 CreatorThreadID: %q", def.Spec.CreatorThreadID)
		}
	}
}
