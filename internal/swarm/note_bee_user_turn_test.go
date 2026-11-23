package swarm

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"mcode/internal/kanban"
)

// TestNoteBeeUserTurn 验证用户直接向已结束 bee 发消息时立即将其置回运行态，
// 而对运行中 bee 与未知线程调用则保持不变且不触发变更通知。
func TestNoteBeeUserTurn(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		OnReport:    func(string, string) error { return nil },
	})

	const leadID = "lead-note-bee-user-turn"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Aspen", Task: "task A", Prompt: "prompt A"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]

	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "turn0.txt"), []byte("turn0\n"), 0644); err != nil {
		t.Fatalf("写入 turn0.txt 失败: %v", err)
	}
	driver.setSessionState(bee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "第一轮完成",
	})
	mgr.Tick(ctx)

	got := beeByThread(t, mgr, leadID, bee.ThreadID)
	if got.Status != StatusCompleted || !got.Reported {
		t.Fatalf("第一轮结束后 bee 应已完成并汇报，实际: %+v", got)
	}

	mgr.mu.Lock()
	mgr.reactivateProbedAt[bee.ThreadID] = clock.Now()
	mgr.reactivateSeen[bee.ThreadID] = 1
	mgr.mu.Unlock()

	var changeMu sync.Mutex
	var changes []string
	mgr.SetOnChange(func(changedLeadID string) {
		changeMu.Lock()
		changes = append(changes, changedLeadID)
		changeMu.Unlock()
	})

	// 1. 已汇报的 bee（假 Driver 仍返回未运行）：调用 NoteBeeUserTurn 后立即回到 Running。
	mgr.NoteBeeUserTurn(ctx, bee.ThreadID)

	got = beeByThread(t, mgr, leadID, bee.ThreadID)
	if got.Status != StatusRunning || got.Reported {
		t.Fatalf("NoteBeeUserTurn 后应置回 StatusRunning 且 Reported=false，实际: %+v", got)
	}

	changeMu.Lock()
	if len(changes) != 1 || changes[0] != leadID {
		changeMu.Unlock()
		t.Fatalf("onChange 应只收到一次 %q，实际: %v", leadID, changes)
	}
	changes = nil
	changeMu.Unlock()

	mgr.mu.Lock()
	_, hasProbe := mgr.reactivateProbedAt[bee.ThreadID]
	_, hasSeen := mgr.reactivateSeen[bee.ThreadID]
	mgr.mu.Unlock()
	if hasProbe || hasSeen {
		t.Fatalf("NoteBeeUserTurn 成功后应清理探测记录: hasProbe=%v hasSeen=%v", hasProbe, hasSeen)
	}

	// 2. 运行中的 bee：再次调用 NoteBeeUserTurn 不改变状态，也不触发 onChange。
	beforeRunning := beeByThread(t, mgr, leadID, bee.ThreadID)
	mgr.NoteBeeUserTurn(ctx, bee.ThreadID)
	afterRunning := beeByThread(t, mgr, leadID, bee.ThreadID)
	if afterRunning.Status != beforeRunning.Status || afterRunning.Reported != beforeRunning.Reported ||
		afterRunning.BaseCommit != beforeRunning.BaseCommit {
		t.Fatalf("运行中的 bee 调用 NoteBeeUserTurn 后状态不应变化: before=%+v after=%+v", beforeRunning, afterRunning)
	}

	changeMu.Lock()
	if len(changes) != 0 {
		changeMu.Unlock()
		t.Fatalf("运行中的 bee 调用 NoteBeeUserTurn 不应触发 onChange，实际: %v", changes)
	}
	changeMu.Unlock()

	// 3. 未知 threadID：不 panic、不触发 onChange。
	mgr.NoteBeeUserTurn(ctx, "unknown-bee-thread")

	changeMu.Lock()
	if len(changes) != 0 {
		changeMu.Unlock()
		t.Fatalf("未知 threadID 调用 NoteBeeUserTurn 不应触发 onChange，实际: %v", changes)
	}
	changeMu.Unlock()
}
