package automation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"mcode/internal/filelock"
	"mcode/internal/store"
)

// fakeLock 是测试用的锁句柄，只记录 Close 与 WriteInfo 的调用。
type fakeLock struct {
	mu     sync.Mutex
	closed int
	info   any
}

func (l *fakeLock) Close() error {
	l.mu.Lock()
	l.closed++
	l.mu.Unlock()
	return nil
}

func (l *fakeLock) WriteInfo(v any) error {
	l.mu.Lock()
	l.info = v
	l.mu.Unlock()
	return nil
}

// fakeLockResult 是一次假加锁调用的结果。
type fakeLockResult struct {
	lock LockHandle
	err  error
}

// fakeLockAcquirer 按序列返回预设结果；序列用完后重复返回固定的 lock/err。
type fakeLockAcquirer struct {
	mu    sync.Mutex
	seq   []fakeLockResult
	lock  LockHandle
	err   error
	calls int
}

func (a *fakeLockAcquirer) acquire(string) (LockHandle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if len(a.seq) > 0 {
		res := a.seq[0]
		a.seq = a.seq[1:]
		return res.lock, res.err
	}
	return a.lock, a.err
}

// set 固定后续所有加锁调用的结果。
func (a *fakeLockAcquirer) set(lock LockHandle, err error) {
	a.mu.Lock()
	a.lock, a.err = lock, err
	a.mu.Unlock()
}

func (a *fakeLockAcquirer) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// newLockTestService 构造使用独立测试库的服务；ticker 间隔设得足够长，
// 锁逻辑只由测试显式 Tick 驱动，避免后台协程干扰断言。
func newLockTestService(t *testing.T, clock *fakeClock, driver *fakeDriver, dataDir string, acquire LockAcquirer, logf func(string, ...any)) (*Service, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return newLockTestServiceOnStore(t, s, clock, driver, dataDir, acquire, logf), s
}

// newLockTestServiceOnStore 在共享库上构造服务，供双实例用例使用。
func newLockTestServiceOnStore(t *testing.T, s *store.Store, clock *fakeClock, driver *fakeDriver, dataDir string, acquire LockAcquirer, logf func(string, ...any)) *Service {
	t.Helper()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return New(Options{
		Store:       s,
		Driver:      driver,
		Projects:    &fakeProjects{root: t.TempDir()},
		Now:         clock.Now,
		Interval:    time.Hour,
		DataDir:     dataDir,
		AcquireLock: acquire,
		Logf:        logf,
	})
}

// createDueAutomation 创建每分钟执行一次的自动化，并把时钟推进到已到期。
func createDueAutomation(t *testing.T, svc *Service, clock *fakeClock) Definition {
	t.Helper()
	def, err := svc.Create(context.Background(), CreateInput{
		ProjectID: "proj-lock",
		Name:      "调度锁测试",
		Prompt:    "检查",
		Enabled:   true,
		Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}
	clock.Advance(61 * time.Second)
	return def
}

// TestSchedulerLockENOLCKStaysPassive 验证 ENOLCK 只让本进程保持 passive，不触发任务。
func TestSchedulerLockENOLCKStaysPassive(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	acq := &fakeLockAcquirer{}
	acq.set(nil, syscall.ENOLCK)

	svc, _ := newLockTestService(t, clock, driver, t.TempDir(), acq.acquire, nil)
	createDueAutomation(t, svc, clock)
	svc.Start()
	defer svc.Stop()

	svc.Tick(ctx)
	if got := driver.startCount(); got != 0 {
		t.Fatalf("ENOLCK 时不应触发任务，实际触发 %d 次", got)
	}
	status := svc.SchedulerLockState()
	if status.State != SchedulerLockPassiveError {
		t.Fatalf("state = %q, want %q", status.State, SchedulerLockPassiveError)
	}
	if status.Error != syscall.ENOLCK.Error() {
		t.Fatalf("error = %q, want %q", status.Error, syscall.ENOLCK.Error())
	}
}

// TestSchedulerLockErrUnsupportedStaysPassive 验证不支持跨进程锁的平台保持 passive_error。
func TestSchedulerLockErrUnsupportedStaysPassive(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	acq := &fakeLockAcquirer{}
	acq.set(nil, filelock.ErrUnsupported)

	svc, _ := newLockTestService(t, clock, driver, t.TempDir(), acq.acquire, nil)
	createDueAutomation(t, svc, clock)
	svc.Start()
	defer svc.Stop()

	svc.Tick(ctx)
	if got := driver.startCount(); got != 0 {
		t.Fatalf("ErrUnsupported 时不应触发任务，实际触发 %d 次", got)
	}
	status := svc.SchedulerLockState()
	if status.State != SchedulerLockPassiveError {
		t.Fatalf("state = %q, want %q", status.State, SchedulerLockPassiveError)
	}
	if !strings.Contains(status.Error, filelock.ErrUnsupported.Error()) {
		t.Fatalf("error = %q, want contains %q", status.Error, filelock.ErrUnsupported.Error())
	}
}

// TestSchedulerLockOpenFailureStaysPassive 验证锁文件打不开时保持 passive_error 并带上错误详情。
func TestSchedulerLockOpenFailureStaysPassive(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, "automation", "scheduler.lock")
	acq := &fakeLockAcquirer{}
	acq.set(nil, &os.PathError{Op: "open", Path: lockPath, Err: syscall.EACCES})

	svc, _ := newLockTestService(t, clock, driver, dataDir, acq.acquire, nil)
	createDueAutomation(t, svc, clock)
	svc.Start()
	defer svc.Stop()

	svc.Tick(ctx)
	if got := driver.startCount(); got != 0 {
		t.Fatalf("打开锁失败时不应触发任务，实际触发 %d 次", got)
	}
	status := svc.SchedulerLockState()
	if status.State != SchedulerLockPassiveError {
		t.Fatalf("state = %q, want %q", status.State, SchedulerLockPassiveError)
	}
	if !strings.Contains(status.Error, lockPath) || !strings.Contains(status.Error, "permission denied") {
		t.Fatalf("error = %q, want 包含锁路径与 permission denied", status.Error)
	}
}

// TestSchedulerLockLogsThrottled 验证同一错误文本 10 分钟内只记一条，文本变化立即记录。
func TestSchedulerLockLogsThrottled(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	acq := &fakeLockAcquirer{}
	acq.set(nil, syscall.ENOLCK)
	logs := 0
	logf := func(string, ...any) { logs++ }

	svc, _ := newLockTestService(t, clock, driver, t.TempDir(), acq.acquire, logf)
	createDueAutomation(t, svc, clock)

	for i := 0; i < 5; i++ {
		svc.Tick(ctx)
		clock.Advance(10 * time.Second)
	}
	if logs != 1 {
		t.Fatalf("同一错误文本在 10 分钟内应只记 1 条，实际 %d 条", logs)
	}

	clock.Advance(11 * time.Minute)
	svc.Tick(ctx)
	if logs != 2 {
		t.Fatalf("超过限流窗口应再记 1 条，实际 %d 条", logs)
	}

	acq.set(nil, syscall.EINTR)
	svc.Tick(ctx)
	if logs != 3 {
		t.Fatalf("错误文本变化应立即记录，实际 %d 条", logs)
	}
	if got := driver.startCount(); got != 0 {
		t.Fatalf("加锁失败期间不应触发任务，实际触发 %d 次", got)
	}
}

// TestSchedulerLockRecoversAndTriggers 验证后续 Tick 能接管并触发任务。
func TestSchedulerLockRecoversAndTriggers(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	acq := &fakeLockAcquirer{seq: []fakeLockResult{
		{err: syscall.ENOLCK},
		{lock: &fakeLock{}},
	}}

	svc, _ := newLockTestService(t, clock, driver, t.TempDir(), acq.acquire, nil)
	defer svc.Stop()
	createDueAutomation(t, svc, clock)

	svc.Tick(ctx)
	if got := driver.startCount(); got != 0 {
		t.Fatalf("第一次 Tick 加锁失败，不应触发任务，实际 %d 次", got)
	}
	if status := svc.SchedulerLockState(); status.State != SchedulerLockPassiveError {
		t.Fatalf("第一次 Tick 后 state = %q, want %q", status.State, SchedulerLockPassiveError)
	}

	svc.Tick(ctx)
	if got := driver.startCount(); got != 1 {
		t.Fatalf("接管后应触发 1 次，实际 %d 次", got)
	}
	status := svc.SchedulerLockState()
	if status.State != SchedulerLockActive || status.Error != "" {
		t.Fatalf("接管后状态 = %+v, want active 且无错误", status)
	}
}

// TestSchedulerLockEmptyDataDirStaysPassive 验证没有 DataDir 时不尝试加锁且无法触发。
func TestSchedulerLockEmptyDataDirStaysPassive(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	acq := &fakeLockAcquirer{}
	acq.set(&fakeLock{}, nil)

	svc, _ := newLockTestService(t, clock, driver, "", acq.acquire, nil)
	def := createDueAutomation(t, svc, clock)
	svc.Start()
	defer svc.Stop()

	svc.Tick(ctx)
	if got := driver.startCount(); got != 0 {
		t.Fatalf("无 DataDir 时不应触发任务，实际触发 %d 次", got)
	}
	status := svc.SchedulerLockState()
	if status.State != SchedulerLockPassiveError || status.Error != ErrSchedulerNoDataDir.Error() {
		t.Fatalf("status = %+v, want passive_error %q", status, ErrSchedulerNoDataDir.Error())
	}
	if _, err := svc.RunNow(ctx, def.ID); !errors.Is(err, ErrSchedulerPassive) {
		t.Fatalf("无 DataDir 时 RunNow error = %v, want ErrSchedulerPassive", err)
	}
	if acq.count() != 0 {
		t.Fatalf("无 DataDir 时不应尝试加锁，实际调用 %d 次", acq.count())
	}
}

// TestServiceSecondInstancePassive 验证第二个实例保持 passive_held，且不触发任务。
func TestServiceSecondInstancePassive(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dataDir := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatalf("open shared store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	driver1 := newFakeDriver()
	driver2 := newFakeDriver()
	svc1 := newLockTestServiceOnStore(t, s, clock, driver1, dataDir, nil, nil)
	svc2 := newLockTestServiceOnStore(t, s, clock, driver2, dataDir, nil, nil)
	createDueAutomation(t, svc1, clock)

	svc1.Start()
	defer svc1.Stop()
	svc2.Start()
	defer svc2.Stop()

	if got := svc1.SchedulerLockState().State; got != SchedulerLockActive {
		t.Fatalf("第一个实例 state = %q, want active", got)
	}
	status := svc2.SchedulerLockState()
	if status.State != SchedulerLockPassiveHeld || status.Error != "" {
		t.Fatalf("第二个实例 status = %+v, want passive_held 且无错误", status)
	}

	svc2.Tick(ctx)
	if driver2.startCount() != 0 {
		t.Fatal("passive 实例不应触发任务")
	}
	if driver1.startCount() != 0 {
		t.Fatal("passive 实例的 Tick 不应驱动持锁实例")
	}
	svc1.Tick(ctx)
	if driver1.startCount() != 1 {
		t.Fatalf("active 实例应触发 1 次，实际 %d 次", driver1.startCount())
	}
}

// TestServicePassiveTakesOverAfterStop 验证持锁实例 Stop 后，另一个实例在下一次 Tick 接管。
func TestServicePassiveTakesOverAfterStop(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dataDir := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatalf("open shared store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	driver1 := newFakeDriver()
	driver2 := newFakeDriver()
	svc1 := newLockTestServiceOnStore(t, s, clock, driver1, dataDir, nil, nil)
	svc2 := newLockTestServiceOnStore(t, s, clock, driver2, dataDir, nil, nil)
	createDueAutomation(t, svc1, clock)

	svc1.Start()
	defer svc1.Stop()
	svc2.Start()
	defer svc2.Stop()

	svc1.Stop()
	svc2.Tick(ctx)
	if got := svc2.SchedulerLockState().State; got != SchedulerLockActive {
		t.Fatalf("持锁实例停止后 state = %q, want active", got)
	}
	if got := driver2.startCount(); got != 1 {
		t.Fatalf("接管实例应触发 1 次，实际 %d 次", got)
	}
}

// TestServicePassiveTakesOverAfterCrash 验证持锁进程崩溃（只关闭描述符、不更新状态）后也能接管。
func TestServicePassiveTakesOverAfterCrash(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dataDir := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatalf("open shared store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	driver1 := newFakeDriver()
	driver2 := newFakeDriver()
	svc1 := newLockTestServiceOnStore(t, s, clock, driver1, dataDir, nil, nil)
	svc2 := newLockTestServiceOnStore(t, s, clock, driver2, dataDir, nil, nil)
	createDueAutomation(t, svc1, clock)

	svc1.Start()
	defer svc1.Stop()
	svc2.Start()
	defer svc2.Stop()

	// 模拟崩溃：只关闭描述符，不调用 releaseSchedulerLock。
	svc1.lockMu.Lock()
	crashedLock := svc1.lock
	svc1.lock = nil
	svc1.lockMu.Unlock()
	if crashedLock == nil {
		t.Fatal("持锁实例没有锁句柄")
	}
	if err := crashedLock.Close(); err != nil {
		t.Fatalf("close crashed lock: %v", err)
	}

	svc2.Tick(ctx)
	if got := svc2.SchedulerLockState().State; got != SchedulerLockActive {
		t.Fatalf("崩溃后 state = %q, want active", got)
	}
	if got := driver2.startCount(); got != 1 {
		t.Fatalf("崩溃后接管实例应触发 1 次，实际 %d 次", got)
	}
	lockPath := filepath.Join(dataDir, "automation", "scheduler.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("崩溃后锁文件应存在: %v", err)
	}
}

// TestServicePassiveDoesNotRunNow 验证 passive 实例的手动触发被拒绝且不产生运行记录。
func TestServicePassiveDoesNotRunNow(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dataDir := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatalf("open shared store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	driver1 := newFakeDriver()
	driver2 := newFakeDriver()
	svc1 := newLockTestServiceOnStore(t, s, clock, driver1, dataDir, nil, nil)
	svc2 := newLockTestServiceOnStore(t, s, clock, driver2, dataDir, nil, nil)
	def := createDueAutomation(t, svc1, clock)

	svc1.Start()
	defer svc1.Stop()
	svc2.Start()
	defer svc2.Stop()

	if _, err := svc2.RunNow(ctx, def.ID); !errors.Is(err, ErrSchedulerPassive) {
		t.Fatalf("passive 实例 RunNow error = %v, want ErrSchedulerPassive", err)
	}
	if got := driver2.startCount(); got != 0 {
		t.Fatalf("passive 实例不应启动会话，实际 %d 次", got)
	}
	runs, err := s.ListAutomationRuns(ctx, def.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("passive 实例不应产生运行记录，实际 %d 条", len(runs))
	}
}

// TestSchedulerLockPath 验证锁文件位于 <DataDir>/automation/scheduler.lock，且停止后仍然存在。
func TestSchedulerLockPath(t *testing.T) {
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dataDir := t.TempDir()
	svc, _ := newLockTestService(t, clock, newFakeDriver(), dataDir, nil, nil)

	svc.Start()
	defer svc.Stop()

	want := filepath.Join(dataDir, "automation", "scheduler.lock")
	svc.lockMu.Lock()
	got := svc.lockPath
	svc.lockMu.Unlock()
	if got != want {
		t.Fatalf("lock path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("start 后锁文件应存在: %v", err)
	}
	if state := svc.SchedulerLockState().State; state != SchedulerLockActive {
		t.Fatalf("state = %q, want active", state)
	}

	svc.Stop()
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stop 后锁文件应保留: %v", err)
	}
}
