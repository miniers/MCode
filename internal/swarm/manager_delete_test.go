package swarm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"mcode/internal/testenv"
)

type fakePersisterWithDelete struct {
	*fakePersister
}

func (p *fakePersisterWithDelete) DeleteLead(ctx context.Context, leadThreadID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.leads, leadThreadID)
	return nil
}

// 验证删除 running bee 会被标记为 StatusStopped，FinishedAt 不为空，摘要为空，且 Lead 能收到结算汇报；非 bee 的 threadID 调用时不报错、状态不变。
func TestManager_BeeThreadDeleted_RunningBee(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	var reports []string
	var reportMu sync.Mutex
	persister := newFakePersister()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		Persister:   persister,
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})

	leadID := "lead-delete-bee"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "DelBee", Task: "Delete task", Prompt: "Delete prompt"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(bees) != 1 {
		t.Fatalf("expected 1 bee dispatched, got %d", len(bees))
	}
	beeThreadID := bees[0].ThreadID

	// 确认分发后为 running 状态
	stInit := mgr.State(leadID)
	if len(stInit.Bees) != 1 || stInit.Bees[0].Status != StatusRunning {
		t.Fatalf("expected bee to be running, got %+v", stInit.Bees)
	}

	// 调用 BeeThreadDeleted
	mgr.BeeThreadDeleted(ctx, beeThreadID)

	// 断言 bee 变为 StatusStopped，FinishedAt 不为空，Summary 为空
	stAfter := mgr.State(leadID)
	if len(stAfter.Bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(stAfter.Bees))
	}
	h := stAfter.Bees[0]
	if h.Status != StatusStopped {
		t.Errorf("expected bee status %s, got %s", StatusStopped, h.Status)
	}
	if h.FinishedAt == nil {
		t.Errorf("expected FinishedAt to be non-nil")
	}
	if h.Summary != "" {
		t.Errorf("expected Summary to be empty, got %q", h.Summary)
	}

	// 断言 Lead 收到结算汇报
	reportMu.Lock()
	count := len(reports)
	var reportMsg string
	if count > 0 {
		reportMsg = reports[0]
	}
	reportMu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 report, got %d", count)
	}
	if !strings.HasPrefix(reportMsg, ReportPrefix) {
		t.Errorf("expected report message to start with %q, got: %s", ReportPrefix, reportMsg)
	}
	if !strings.Contains(reportMsg, "DelBee") {
		t.Errorf("expected report message to contain bee name 'DelBee', got: %s", reportMsg)
	}
	if !strings.Contains(reportMsg, "已停止") {
		t.Errorf("expected report message to contain '已停止', got: %s", reportMsg)
	}

	// 再次调用对非 bee 的 threadID 调用：不报错、状态不变
	mgr.BeeThreadDeleted(ctx, "nonexistent-thread-id")
	stUnchanged := mgr.State(leadID)
	if len(stUnchanged.Bees) != 1 || stUnchanged.Bees[0].Status != StatusStopped {
		t.Errorf("expected state to remain unchanged after nonexistent thread deleted")
	}
}

// 验证当 threadID 为 Lead 时，停止其所有 running bee 并移除 lead 状态与持久化记录。
func TestManager_BeeThreadDeleted_LeadThread(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	var stoppedSessions []string
	var stopMu sync.Mutex

	basePersister := newFakePersister()
	persister := &fakePersisterWithDelete{fakePersister: basePersister}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		Persister:   persister,
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stoppedSessions = append(stoppedSessions, threadID)
			stopMu.Unlock()
			return nil
		},
	})

	leadID := "lead-to-delete"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "T1", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	beeID := bees[0].ThreadID

	// 调用 BeeThreadDeleted 删除 Lead 本身
	mgr.BeeThreadDeleted(ctx, leadID)

	// 验证 running bee 被 StopSession 停止
	stopMu.Lock()
	found := false
	for _, id := range stoppedSessions {
		if id == beeID {
			found = true
			break
		}
	}
	stopMu.Unlock()
	if !found {
		t.Errorf("expected StopSession to be called for running bee %s", beeID)
	}

	// 验证 lead 状态已从 Manager 中移除
	st := mgr.State(leadID)
	if len(st.Bees) != 0 || st.Enabled {
		t.Errorf("expected lead state to be removed, got %+v", st)
	}

	// 验证持久化中已删除该 lead
	persister.mu.Lock()
	_, existsInStore := persister.leads[leadID]
	persister.mu.Unlock()
	if existsInStore {
		t.Errorf("expected lead to be deleted from persister")
	}

	// 对未知 thread 调用时不报错、状态不变
	mgr.BeeThreadDeleted(ctx, "unknown-thread")
}
