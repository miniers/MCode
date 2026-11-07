package kanban

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/store"
)

// pokeCall 记录一次催促投递。
type pokeCall struct {
	threadID string
	text     string
}

// pokeDriver 同时实现 SessionDriver、SessionMessenger 与 SessionInterrupter，并记录调用。
type pokeDriver struct {
	mu          sync.Mutex
	states      map[string]SessionState
	stateErr    error
	followUps   []pokeCall
	interrupted []string
}

func newPokeDriver() *pokeDriver {
	return &pokeDriver{states: make(map[string]SessionState)}
}

func (d *pokeDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	threadID := uuid.NewString()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[threadID] = SessionState{Running: true, LastAssistantText: "任务已开始执行"}
	return threadID, nil
}

func (d *pokeDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stateErr != nil {
		return SessionState{}, d.stateErr
	}
	if st, ok := d.states[threadID]; ok {
		return st, nil
	}
	return SessionState{}, store.ErrNotFound
}

func (d *pokeDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.followUps = append(d.followUps, pokeCall{threadID: threadID, text: text})
	return nil
}

func (d *pokeDriver) InterruptSession(ctx context.Context, threadID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.interrupted = append(d.interrupted, threadID)
	return nil
}

func (d *pokeDriver) sentFollowUps() []pokeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]pokeCall(nil), d.followUps...)
}

func (d *pokeDriver) interruptedThreads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.interrupted...)
}

func (d *pokeDriver) setStateErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stateErr = err
}

// setupPokeWatchdog 创建注入 Now 的看板服务与巡视器，驱动由调用方指定。
func setupPokeWatchdog(t *testing.T, driver SessionDriver, autoRescue bool, nudgeTimeout, interruptTimeout time.Duration, nowFn func() time.Time) (*Service, *store.Store, *Watchdog) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_poke.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	svc := NewService(Options{
		Store:                 s,
		Driver:                driver,
		Projects:              &mockProjects{dir: t.TempDir()},
		AutoReview:            func() bool { return true },
		AutoStartOnCreate:     func() bool { return true },
		AutoRescueStuck:       func() bool { return autoRescue },
		StuckNudgeTimeout:     func() time.Duration { return nudgeTimeout },
		StuckInterruptTimeout: func() time.Duration { return interruptTimeout },
		Now:                   nowFn,
	})
	t.Cleanup(func() { svc.Close() })

	return svc, s, NewWatchdog(svc, 0)
}

// TestWatchdogPokeSendsFollowUpOnce 验证超过 nudge 超时后只发送一次催促，且文案为 StuckPokeMessage。
func TestWatchdogPokeSendsFollowUpOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	currTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, wd := setupPokeWatchdog(t, driver, false, 600*time.Second, 1800*time.Second, func() time.Time { return currTime })

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "催促任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil || *task.ThreadID == "" {
		t.Fatalf("期望任务已绑定 threadID")
	}

	// 首次巡视建立活动时间戳，此时不应催促
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("首次 check 失败: %v", err)
	}
	if calls := driver.sentFollowUps(); len(calls) != 0 {
		t.Fatalf("未超时不应发送催促，实际: %+v", calls)
	}

	// 超过 nudge 阈值后发送一次催促
	currTime = currTime.Add(650 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	calls := driver.sentFollowUps()
	if len(calls) != 1 {
		t.Fatalf("期望发送一次催促，实际 %d 次", len(calls))
	}
	if calls[0].threadID != *task.ThreadID {
		t.Fatalf("催促会话不符，期望 %s，实际 %s", *task.ThreadID, calls[0].threadID)
	}
	if calls[0].text != StuckPokeMessage {
		t.Fatalf("催促文案不符，期望 %q，实际 %q", StuckPokeMessage, calls[0].text)
	}
	latest, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.LastActivityText == nil || !strings.Contains(*latest.LastActivityText, "已发送催促") {
		t.Fatalf("期望记录催促发送结果，实际: %v", latest.LastActivityText)
	}

	// 再跑一轮不应重复发送
	currTime = currTime.Add(30 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	if calls = driver.sentFollowUps(); len(calls) != 1 {
		t.Fatalf("同一卡死周期不应重复发送催促，实际 %d 次", len(calls))
	}
}

// TestWatchdogInterruptPutsTaskBackToTodo 验证超过 interrupt 超时且开启 AutoRescueStuck 时中断会话并把任务置回待办。
func TestWatchdogInterruptPutsTaskBackToTodo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	currTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, wd := setupPokeWatchdog(t, driver, true, 600*time.Second, 1800*time.Second, func() time.Time { return currTime })

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "中断任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil || *task.ThreadID == "" {
		t.Fatalf("期望任务已绑定 threadID")
	}
	threadID := *task.ThreadID

	// 首次巡视建立活动时间戳，随后依次触发催促与中断
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("首次 check 失败: %v", err)
	}
	currTime = currTime.Add(650 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	currTime = currTime.Add(1250 * time.Second) // 累计 1900 秒，超过 interrupt 阈值
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}

	interrupted := driver.interruptedThreads()
	if len(interrupted) != 1 || interrupted[0] != threadID {
		t.Fatalf("期望中断会话 %s，实际: %v", threadID, interrupted)
	}
	latest, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusTodo) {
		t.Fatalf("期望中断后状态为 todo，实际为 %s", latest.Status)
	}
	if latest.ThreadID != nil {
		t.Fatalf("期望中断后清空 ThreadID，实际为 %v", *latest.ThreadID)
	}
	if latest.LastFailureReason == nil || !strings.Contains(*latest.LastFailureReason, "卡死救援已自动中断") {
		t.Fatalf("期望标记卡死中断原因，实际: %v", latest.LastFailureReason)
	}
	if latest.LastActivityText == nil || *latest.LastActivityText != *latest.LastFailureReason {
		t.Fatalf("期望 LastActivityText 与失败原因为同一文案，实际: %v", latest.LastActivityText)
	}
}

// TestWatchdogResetsTaskWhenSessionNotFound 验证会话不存在（store.ErrNotFound）时任务被重置为待办。
func TestWatchdogResetsTaskWhenSessionNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	currTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, wd := setupPokeWatchdog(t, driver, true, 600*time.Second, 1800*time.Second, func() time.Time { return currTime })

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "会话丢失任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil || *task.ThreadID == "" {
		t.Fatalf("期望任务已绑定 threadID")
	}
	driver.setStateErr(store.ErrNotFound)

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}

	latest, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusTodo) {
		t.Fatalf("期望会话丢失后状态为 todo，实际为 %s", latest.Status)
	}
	if latest.ThreadID != nil {
		t.Fatalf("期望会话丢失后清空 ThreadID，实际为 %v", *latest.ThreadID)
	}
	if latest.LastActivityText == nil || !strings.Contains(*latest.LastActivityText, "会话已不存在") {
		t.Fatalf("期望记录会话丢失说明，实际: %v", latest.LastActivityText)
	}
}

// TestWatchdogNudgeWithoutMessengerDriver 验证驱动仅实现 SessionDriver 时催促不 panic，文案为发送失败。
func TestWatchdogNudgeWithoutMessengerDriver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	currTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	driver := newMockDriver() // 仅实现 SessionDriver，不支持催促与中断
	svc, s, wd := setupPokeWatchdog(t, driver, false, 600*time.Second, 1800*time.Second, func() time.Time { return currTime })

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "无催促驱动任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil || *task.ThreadID == "" {
		t.Fatalf("期望任务已绑定 threadID")
	}
	driver.setThreadState(*task.ThreadID, SessionState{Running: true, LastAssistantText: "处理中"})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("首次 check 失败: %v", err)
	}
	currTime = currTime.Add(650 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("驱动不支持催促时不应报错: %v", err)
	}

	latest, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.LastActivityText == nil || !strings.Contains(*latest.LastActivityText, "催促发送失败") {
		t.Fatalf("期望文案为催促发送失败，实际: %v", latest.LastActivityText)
	}
	if latest.Status != string(TaskStatusInProgress) {
		t.Fatalf("催促失败不应改变任务状态，实际为 %s", latest.Status)
	}
}

// TestWatchdogInterruptFailureKeepsTaskAndRetries 验证驱动不支持中断时保留任务与 ThreadID，
// 下一轮巡视继续重试，且同一失败文案不重复写库。
func TestWatchdogInterruptFailureKeepsTaskAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	currTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	driver := newMockDriver() // 仅实现 SessionDriver，不支持中断
	svc, s, wd := setupPokeWatchdog(t, driver, true, 600*time.Second, 1800*time.Second, func() time.Time { return currTime })

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "中断失败任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil || *task.ThreadID == "" {
		t.Fatalf("期望任务已绑定 threadID")
	}
	threadID := *task.ThreadID
	driver.setThreadState(threadID, SessionState{Running: true, LastAssistantText: "处理中"})

	// 首次巡视建立活动时间戳，随后依次触发催促与中断尝试
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("首次 check 失败: %v", err)
	}
	currTime = currTime.Add(650 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	currTime = currTime.Add(1250 * time.Second) // 累计 1900 秒，超过 interrupt 阈值
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}

	first, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if first.Status != string(TaskStatusInProgress) {
		t.Fatalf("中断失败时期望保留 in_progress，实际为 %s", first.Status)
	}
	if first.ThreadID == nil || *first.ThreadID != threadID {
		t.Fatalf("中断失败时期望保留 ThreadID，实际为 %v", first.ThreadID)
	}
	if first.LastFailureReason == nil || !strings.Contains(*first.LastFailureReason, "卡死救援中断失败") {
		t.Fatalf("期望标记中断失败原因，实际为 %v", first.LastFailureReason)
	}
	if first.LastActivityText == nil || *first.LastActivityText != *first.LastFailureReason {
		t.Fatalf("期望 LastActivityText 与失败原因为同一文案，实际为 %v", first.LastActivityText)
	}

	// 下一轮巡视仍会重试中断，但同一失败文案不重复写库
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	second, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if second.Revision != first.Revision {
		t.Fatalf("同一失败文案不应重复写库，版本号 %d -> %d", first.Revision, second.Revision)
	}
	if second.Status != string(TaskStatusInProgress) || second.ThreadID == nil || *second.ThreadID != threadID {
		t.Fatalf("重试期间任务不应被改变，实际 status=%s threadID=%v", second.Status, second.ThreadID)
	}
}
