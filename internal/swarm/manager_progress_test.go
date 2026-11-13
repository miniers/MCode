package swarm

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/testenv"
)

// countingPersister 记录 SaveLead 的调用次数，用于验证进度类变更是否被合并落盘。
type countingPersister struct {
	inner *fakePersister
	mu    sync.Mutex
	saves int
}

func newCountingPersister() *countingPersister {
	return &countingPersister{inner: newFakePersister()}
}

func (p *countingPersister) SaveLead(ctx context.Context, rec LeadRecord) error {
	p.mu.Lock()
	p.saves++
	p.mu.Unlock()
	return p.inner.SaveLead(ctx, rec)
}

func (p *countingPersister) LoadLeads(ctx context.Context) ([]LeadRecord, error) {
	return p.inner.LoadLeads(ctx)
}

func (p *countingPersister) saveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saves
}

func (p *countingPersister) record(leadThreadID string) (LeadRecord, bool) {
	p.inner.mu.Lock()
	defer p.inner.mu.Unlock()
	rec, ok := p.inner.leads[leadThreadID]
	return rec, ok
}

func newProgressTestManager(t *testing.T) (*Manager, *countingPersister) {
	t.Helper()
	persister := newCountingPersister()
	mgr := NewManager(Options{Persister: persister})
	// 定时器不自动触发：由测试显式兑现合并窗口，避免依赖真实时间。
	mgr.progressDelay = time.Hour
	t.Cleanup(mgr.Stop)

	ctx := context.Background()
	rec := LeadRecord{
		LeadThreadID: "lead-1",
		Enabled:      true,
		Bees:         []Bee{{ThreadID: "bee-1", Status: StatusCompleted}},
	}
	if err := persister.SaveLead(ctx, rec); err != nil {
		t.Fatalf("seed lead failed: %v", err)
	}
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	return mgr, persister
}

// TestManager_ProgressChangesCoalesced 验证连续多次进度类变更在合并窗口内只落盘并通知一次。
func TestManager_ProgressChangesCoalesced(t *testing.T) {
	mgr, persister := newProgressTestManager(t)
	base := persister.saveCount()

	var notified []string
	mgr.SetOnChange(func(leadThreadID string) { notified = append(notified, leadThreadID) })

	for i := 0; i < 20; i++ {
		mgr.notifyProgress("lead-1")
	}
	if got := persister.saveCount(); got != base {
		t.Fatalf("窗口内的进度变更不应逐次落盘，落盘次数 %d，期望 %d", got, base)
	}
	if len(notified) != 0 {
		t.Fatalf("窗口未关闭前不应通知，实际通知 %d 次", len(notified))
	}

	// 兑现合并窗口：20 次进度变更合并成 1 次落盘与 1 次通知。
	mgr.flushPendingChanges()
	if got := persister.saveCount(); got != base+1 {
		t.Fatalf("合并窗口只应落盘一次，落盘次数 %d，期望 %d", got, base+1)
	}
	if len(notified) != 1 || notified[0] != "lead-1" {
		t.Fatalf("合并窗口只应通知一次 lead-1，实际 %v", notified)
	}
}

// TestManager_StateChangePersistsImmediately 验证状态类变更立即落盘并顺带覆盖排队中的进度变更。
func TestManager_StateChangePersistsImmediately(t *testing.T) {
	mgr, persister := newProgressTestManager(t)

	var notified []string
	mgr.SetOnChange(func(leadThreadID string) { notified = append(notified, leadThreadID) })

	mgr.notifyProgress("lead-1")
	before := persister.saveCount()
	mgr.notifyChange("lead-1")
	if got := persister.saveCount(); got != before+1 {
		t.Fatalf("状态变更应立即落盘一次，落盘次数 %d，期望 %d", got, before+1)
	}
	if len(notified) != 1 {
		t.Fatalf("状态变更应只通知一次（覆盖排队中的进度变更），实际 %d 次", len(notified))
	}

	// 排队标记已被状态变更覆盖，再兑现窗口不应重复落盘或重复通知。
	mgr.flushPendingChanges()
	if got := persister.saveCount(); got != before+1 {
		t.Fatalf("进度变更已随状态变更落盘，不应重复落盘，落盘次数 %d，期望 %d", got, before+1)
	}
	if len(notified) != 1 {
		t.Fatalf("不应重复通知，实际 %d 次", len(notified))
	}
}

// TestManager_ProgressFlushPersistsLatestState 验证合并落盘写的是窗口关闭时的最新状态。
func TestManager_ProgressFlushPersistsLatestState(t *testing.T) {
	mgr, persister := newProgressTestManager(t)

	mgr.mu.Lock()
	mgr.leads["lead-1"].bees[0].Tokens = 4242
	mgr.mu.Unlock()

	mgr.notifyProgress("lead-1")
	mgr.flushPendingChanges()

	rec, ok := persister.record("lead-1")
	if !ok || len(rec.Bees) != 1 {
		t.Fatalf("落盘记录缺失或 bee 数量不符：%+v", rec)
	}
	if rec.Bees[0].Tokens != 4242 {
		t.Fatalf("落盘应为最新状态，Tokens = %d，期望 4242", rec.Bees[0].Tokens)
	}
}

// TestManager_StopFlushesPendingProgress 验证退出时兑现合并窗口内的进度变更，保证最后状态已落盘。
func TestManager_StopFlushesPendingProgress(t *testing.T) {
	mgr, persister := newProgressTestManager(t)
	base := persister.saveCount()

	mgr.notifyProgress("lead-1")
	mgr.Stop()

	if got := persister.saveCount(); got != base+1 {
		t.Fatalf("Stop 应兑现排队中的进度变更，落盘次数 %d，期望 %d", got, base+1)
	}
}

// usageDriver 在 fakeDriver 之上提供会话累计用量，模拟真实驱动实现 kanban.SessionUsageReader。
type usageDriver struct {
	*fakeDriver
	mu    sync.Mutex
	usage map[string][2]int
}

func newUsageDriver() *usageDriver {
	return &usageDriver{fakeDriver: newFakeDriver(), usage: make(map[string][2]int)}
}

func (d *usageDriver) setUsage(threadID string, tokens, toolCalls int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.usage[threadID] = [2]int{tokens, toolCalls}
}

// SessionUsage 返回测试预设的 tokens 与工具调用数。
func (d *usageDriver) SessionUsage(ctx context.Context, threadID string) (int, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	u, ok := d.usage[threadID]
	if !ok {
		return 0, 0, fmt.Errorf("session not found: %s", threadID)
	}
	return u[0], u[1], nil
}

// TestManager_TickRecordsBeeUsage 验证运行中的 bee 把会话累计用量写回记录并经合并窗口落盘。
func TestManager_TickRecordsBeeUsage(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newUsageDriver()
	persister := newCountingPersister()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})
	// 定时器不自动触发：由测试显式兑现合并窗口，避免依赖真实时间。
	mgr.progressDelay = time.Hour
	t.Cleanup(mgr.Stop)

	leadID := "lead-usage"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "UsageHank", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID
	mgr.Tick(ctx)

	if h := beeByThread(t, mgr, leadID, beeThreadID); h.Tokens != 0 || h.ToolCalls != 0 {
		t.Fatalf("尚无输出时用量应为 0，实际 tokens=%d toolCalls=%d", h.Tokens, h.ToolCalls)
	}

	driver.setUsage(beeThreadID, 1500, 3)
	mgr.Tick(ctx)

	h := beeByThread(t, mgr, leadID, beeThreadID)
	if h.Tokens != 1500 || h.ToolCalls != 3 {
		t.Fatalf("bee 记录应带上会话用量，实际 tokens=%d toolCalls=%d", h.Tokens, h.ToolCalls)
	}

	mgr.flushPendingChanges()
	rec, ok := persister.record(leadID)
	if !ok || len(rec.Bees) != 1 {
		t.Fatalf("落盘记录缺失或 bee 数量不符：%+v", rec)
	}
	if rec.Bees[0].Tokens != 1500 || rec.Bees[0].ToolCalls != 3 {
		t.Fatalf("heads 记录应持久化用量，实际 tokens=%d toolCalls=%d", rec.Bees[0].Tokens, rec.Bees[0].ToolCalls)
	}
}

// TestManager_BeeUsageSurvivesRestart 验证 bee 结束时落盘的最终用量在重新加载后仍然保留。
func TestManager_BeeUsageSurvivesRestart(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newUsageDriver()
	persister := newCountingPersister()
	newMgr := func() *Manager {
		m := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			Persister:   persister,
		})
		m.progressDelay = time.Hour
		t.Cleanup(m.Stop)
		return m
	}
	mgr := newMgr()

	leadID := "lead-usage-done"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "UsageHank", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID
	mgr.Tick(ctx)

	driver.setUsage(beeThreadID, 2400, 4)
	driver.setSessionState(beeThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	h := beeByThread(t, mgr, leadID, beeThreadID)
	if h.Status != StatusCompleted {
		t.Fatalf("bee 应完成，实际状态: %s（摘要 %s）", h.Status, h.Summary)
	}
	if h.Tokens != 2400 || h.ToolCalls != 4 {
		t.Fatalf("结束时应写回最终用量，实际 tokens=%d toolCalls=%d", h.Tokens, h.ToolCalls)
	}

	// 重启：新 Manager 从同一份持久化记录恢复，最终用量不丢。
	restored := newMgr()
	if err := restored.Restore(ctx); err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	bee := beeByThread(t, restored, leadID, beeThreadID)
	if bee.Tokens != 2400 || bee.ToolCalls != 4 {
		t.Fatalf("恢复后用量应保留，实际 tokens=%d toolCalls=%d", bee.Tokens, bee.ToolCalls)
	}
}

// TestManager_RunningBeesReturnsOnlyRunning 验证 RunningBees 只返回 running 状态的 bee 副本，按 lead 分组。
func TestManager_RunningBeesReturnsOnlyRunning(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Persister:   newCountingPersister(),
	})
	mgr.progressDelay = time.Hour
	t.Cleanup(mgr.Stop)

	leadID := "lead-running-bees"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "BeeA", Task: "task-a", Prompt: "prompt-a"},
			{Name: "BeeB", Task: "task-b", Prompt: "prompt-b"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	if len(bees) != 2 {
		t.Fatalf("应派发两个 bee，实际 %d 个", len(bees))
	}

	// 让第二个 bee 结束，只剩第一个在 running。
	driver.setSessionState(bees[1].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	running := mgr.RunningBees()
	got := running[leadID]
	if len(got) != 1 || got[0].ThreadID != bees[0].ThreadID {
		t.Fatalf("RunningBees 应只返回仍在运行的 bee %s，实际 %+v", bees[0].ThreadID, got)
	}

	// 返回的是副本：改动结果不影响内部状态。
	got[0].Summary = "tampered"
	if h := beeByThread(t, mgr, leadID, bees[0].ThreadID); h.Summary == "tampered" {
		t.Fatalf("RunningBees 应返回 bee 副本，改动不得写回内部状态")
	}

	// 全部结束后返回空 map（非 nil）。
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)
	empty := mgr.RunningBees()
	if empty == nil || len(empty) != 0 {
		t.Fatalf("没有运行中的 bee 时应返回空 map，实际 %+v", empty)
	}
}

// TestManager_PollCopiesLastFileChangeAt 验证轮询把会话的文件改动时刻复制到 bee 记录并落盘。
func TestManager_PollCopiesLastFileChangeAt(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()
	persister := newCountingPersister()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})
	mgr.progressDelay = time.Hour
	t.Cleanup(mgr.Stop)

	leadID := "lead-file-change"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "FileChange", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID

	// 建立进度基线：此时会话还没有文件改动时刻。
	mgr.Tick(ctx)
	if h := beeByThread(t, mgr, leadID, beeThreadID); !h.LastFileChangeAt.IsZero() {
		t.Fatalf("尚无文件改动时 LastFileChangeAt 应为零值，实际 %v", h.LastFileChangeAt)
	}

	changedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Millisecond)
	driver.setSessionState(beeThreadID, kanban.SessionState{
		Running:          true,
		Progress:         "progress-after-edit",
		LastFileChangeAt: changedAt,
	})
	mgr.Tick(ctx)

	h := beeByThread(t, mgr, leadID, beeThreadID)
	if !h.LastFileChangeAt.Equal(changedAt) {
		t.Fatalf("轮询应把会话的文件改动时刻复制到 bee，期望 %v，实际 %v", changedAt, h.LastFileChangeAt)
	}

	mgr.flushPendingChanges()
	rec, ok := persister.record(leadID)
	if !ok || len(rec.Bees) != 1 {
		t.Fatalf("落盘记录缺失或 bee 数量不符：%+v", rec)
	}
	if !rec.Bees[0].LastFileChangeAt.Equal(changedAt) {
		t.Fatalf("落盘记录应带上文件改动时刻，期望 %v，实际 %v", changedAt, rec.Bees[0].LastFileChangeAt)
	}
}
