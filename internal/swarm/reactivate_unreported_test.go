package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// TestUnreportedCompletedBeeReactivatesAndReportsLatest 验证同批仍有其他 bee 运行、
// 先完成的 bee 尚未汇报时，若其会话重新运行，续跑探测能将其重新激活并在最终完成后
// 只合并增量改动、归档并投递最新一轮的报告且只投递一次。
func TestUnreportedCompletedBeeReactivatesAndReportsLatest(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	var reportMu sync.Mutex
	var reports []string
	archived := make(map[string]store.SwarmReport)

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		ArchiveReport: func(ctx context.Context, rec store.SwarmReport) error {
			reportMu.Lock()
			archived[rec.BeeID] = rec
			reportMu.Unlock()
			return nil
		},
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})

	const leadID = "lead-unreported-reactivate"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Aspen", Task: "task A", Prompt: "prompt A"},
			{Name: "Birch", Task: "task B", Prompt: "prompt B"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeA := bees[0]
	beeB := bees[1]

	// A 在第一轮写了一个文件并输出中途进度，此时 B 仍在运行。
	if err := os.WriteFile(filepath.Join(beeA.WorktreePath, "turn0.txt"), []byte("turn0\n"), 0644); err != nil {
		t.Fatalf("写入 turn0.txt 失败: %v", err)
	}
	driver.setSessionState(beeA.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "中途",
	})
	driver.setSessionState(beeB.ThreadID, kanban.SessionState{
		Running: true,
	})

	mgr.Tick(ctx)

	gotA := beeByThread(t, mgr, leadID, beeA.ThreadID)
	if gotA.Status != StatusCompleted || gotA.Reported || !gotA.Merged {
		t.Fatalf("第一轮结束后 A 应已完成、已合入且尚未汇报，实际: %+v", gotA)
	}
	if _, err := os.Stat(filepath.Join(dir, "turn0.txt")); err != nil {
		t.Fatalf("第一轮改动应已合入主工作区: %v", err)
	}
	reportMu.Lock()
	if len(reports) != 0 {
		reportMu.Unlock()
		t.Fatalf("B 仍在运行时不应投递整批汇报，实际: %v", reports)
	}
	reportMu.Unlock()

	// A 的会话重新进入 Running（例如后台任务结束后开启 turn 1），连续推进两次探测间隔触发重新激活。
	driver.setSessionState(beeA.ThreadID, kanban.SessionState{
		Running:           true,
		LastAssistantText: "中途",
	})
	clock.Advance(reactivateProbeInterval)
	mgr.Tick(ctx)
	if got := beeByThread(t, mgr, leadID, beeA.ThreadID); got.Status != StatusCompleted {
		t.Fatalf("首次续跑探测不应立即激活（需连续两次），实际状态: %s", got.Status)
	}

	clock.Advance(reactivateProbeInterval)
	mgr.Tick(ctx)
	gotA = beeByThread(t, mgr, leadID, beeA.ThreadID)
	if gotA.Status != StatusRunning || gotA.Reported {
		t.Fatalf("连续两次探测到运行后 A 应回到 Running 且未汇报，实际: %+v", gotA)
	}

	// 即便此时 B 先结束，同批汇报也应继续等待 A 完成 turn 1。
	driver.setSessionState(beeB.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "B 完成",
	})
	clock.Advance(2 * time.Second)
	mgr.Tick(ctx)
	reportMu.Lock()
	if len(reports) != 0 {
		reportMu.Unlock()
		t.Fatalf("A 重新激活运行中，不应提前投递汇报，实际: %v", reports)
	}
	reportMu.Unlock()

	// A 在 turn 1 产生增量改动并输出最终报告。
	if err := os.WriteFile(filepath.Join(beeA.WorktreePath, "turn1.txt"), []byte("turn1\n"), 0644); err != nil {
		t.Fatalf("写入 turn1.txt 失败: %v", err)
	}
	driver.setSessionState(beeA.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "最终报告",
	})
	clock.Advance(2 * time.Second)
	mgr.Tick(ctx)

	gotA = beeByThread(t, mgr, leadID, beeA.ThreadID)
	gotB := beeByThread(t, mgr, leadID, beeB.ThreadID)
	if gotA.Status != StatusCompleted || !gotA.Reported || !gotA.Merged {
		t.Fatalf("A 最终完成后应已合入并已汇报，实际: %+v", gotA)
	}
	if !gotB.Reported {
		t.Fatalf("B 应已随批汇报，实际: %+v", gotB)
	}
	if _, err := os.Stat(filepath.Join(dir, "turn0.txt")); err != nil {
		t.Fatalf("turn0.txt 应仍在主工作区: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "turn1.txt")); err != nil {
		t.Fatalf("turn1.txt 应已合入主工作区: %v", err)
	}
	if len(gotA.ChangedFiles) != 1 || gotA.ChangedFiles[0] != "turn1.txt" {
		t.Fatalf("续跑轮应只记录增量改动 turn1.txt，实际: %v", gotA.ChangedFiles)
	}

	reportMu.Lock()
	if len(reports) != 1 {
		reportMu.Unlock()
		t.Fatalf("汇报应只投递一次，实际 %d 次: %v", len(reports), reports)
	}
	delivered := reports[0]
	archA, hasArchA := archived[beeA.ThreadID]
	reportMu.Unlock()

	if !hasArchA || archA.Summary != "最终报告" {
		t.Fatalf("归档的 A 报告应为最新“最终报告”，实际: %+v (exists=%v)", archA, hasArchA)
	}
	if !strings.Contains(delivered, "最终报告") || strings.Contains(delivered, "中途") {
		t.Fatalf("投递的汇报应包含“最终报告”且不含旧“中途”，实际:\n%s", delivered)
	}

	// 再推进时钟并 Tick，确认不会重复投递。
	clock.Advance(reactivateProbeInterval)
	mgr.Tick(ctx)
	reportMu.Lock()
	countAfter := len(reports)
	reportMu.Unlock()
	if countAfter != 1 {
		t.Fatalf("后续 Tick 不应重复投递汇报，实际 %d 次", countAfter)
	}
}

// TestUnreportedNonCompletedOrPendingBeesNotReactivated 验证未汇报的失败、停止、
// 待审查/待合并/待验收及已驳回 bee 不会被续跑探测误激活。
func TestUnreportedNonCompletedOrPendingBeesNotReactivated(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	driver := newFakeDriver()
	mgr := NewManager(Options{
		Driver: driver,
		Now:    clock.Now,
	})

	wt := t.TempDir()
	const leadID = "lead-protect-unreported"
	cases := []Bee{
		{ThreadID: "bee-failed", Name: "Failed", Status: StatusFailed, Reported: false, WorktreePath: wt},
		{ThreadID: "bee-stopped", Name: "Stopped", Status: StatusStopped, Reported: false, WorktreePath: wt},
		{ThreadID: "bee-pending-merge", Name: "PendingMerge", Status: StatusCompleted, Reported: false, PendingMerge: true, ReviewState: ReviewRunning, WorktreePath: wt},
		{ThreadID: "bee-needs-human", Name: "NeedsHuman", Status: StatusCompleted, Reported: false, PendingMerge: true, ReviewState: ReviewNeedsHuman, WorktreePath: wt},
		{ThreadID: "bee-rejected", Name: "Rejected", Status: StatusCompleted, Reported: false, PendingMerge: false, ReviewState: ReviewRejected, WorktreePath: wt},
		{ThreadID: "bee-discarded", Name: "Discarded", Status: StatusCompleted, Reported: false, PendingMerge: false, MergeDiscarded: true, WorktreePath: wt},
	}
	for _, b := range cases {
		driver.setSessionState(b.ThreadID, kanban.SessionState{Running: true})
	}
	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		enabled:      true,
		bees:         cases,
	}
	mgr.mu.Unlock()

	for i := 0; i < 3; i++ {
		clock.Advance(reactivateProbeInterval)
		mgr.detectReactivatedBees(context.Background())
	}

	st := mgr.State(leadID)
	for _, b := range st.Bees {
		if b.Status == StatusRunning {
			t.Fatalf("未汇报且受保护的 bee %s (%s) 不应被重新激活", b.Name, b.ThreadID)
		}
	}
}

// TestStallStopKeepsLastAssistantText 验证看门狗因无进展终止 bee 时，
// 若 SessionState 已有非空 LastAssistantText，Summary 保留该文本并前置停止文案，MergeError 仍为停止文案。
func TestStallStopKeepsLastAssistantText(t *testing.T) {
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
		StopSession: func(ctx context.Context, threadID string) error { return nil },
	})

	const leadID = "lead-stall-keep-text"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Birch", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID

	// 工作副本有改动，避免走无改动快速熔断，直接覆盖无进展超时分支。
	if err := os.WriteFile(filepath.Join(bees[0].WorktreePath, "edited.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatalf("写入工作副本失败: %v", err)
	}
	driver.setSessionState(beeThreadID, kanban.SessionState{
		Running:           true,
		LastAssistantText: "已完成的报告",
	})

	// 首次 Tick 建立初始进度快照。
	mgr.Tick(ctx)

	// 推进超过默认无进展超时（15 分钟）。
	clock.Advance(16 * time.Minute)
	mgr.Tick(ctx)

	got := beeByThread(t, mgr, leadID, beeThreadID)
	if got.Status != StatusFailed {
		t.Fatalf("超时后 bee 应为 failed，实际: %s", got.Status)
	}
	if got.MergeError == "" {
		t.Fatalf("MergeError 应保留停止文案，实际为空")
	}
	wantSummary := got.MergeError + "\n\n已完成的报告"
	if got.Summary != wantSummary {
		t.Fatalf("Summary 应保留已有报告并前置停止文案:\n got: %q\nwant: %q", got.Summary, wantSummary)
	}
	if !strings.HasPrefix(got.Summary, got.MergeError) || !strings.Contains(got.Summary, "已完成的报告") {
		t.Fatalf("Summary 格式不符合预期: %q", got.Summary)
	}
}

// TestWatchdogStopEmptyLastAssistantTextUnchanged 验证 SessionState 没有文本时，
// 看门狗停止分支的 Summary 与 MergeError 仍与原行为一致。
func TestWatchdogStopEmptyLastAssistantTextUnchanged(t *testing.T) {
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
		StopSession: func(ctx context.Context, threadID string) error { return nil },
	})

	const leadID = "lead-stall-empty-text"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Cedar", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID
	driver.setSessionState(beeThreadID, kanban.SessionState{
		Running:           true,
		LastAssistantText: "   \n\t ",
	})

	mgr.Tick(ctx)
	clock.Advance(16 * time.Minute)
	mgr.Tick(ctx)

	got := beeByThread(t, mgr, leadID, beeThreadID)
	if got.Status != StatusFailed {
		t.Fatalf("超时后 bee 应为 failed，实际: %s", got.Status)
	}
	if got.Summary != got.MergeError || got.Summary == "" {
		t.Fatalf("无有效文本时 Summary 应等于 MergeError，实际 summary=%q mergeError=%q", got.Summary, got.MergeError)
	}
}

// TestSharedBeeReactivate 验证已汇报且 WorktreePath 为空的共享 bee 可被续跑探测唤醒，
// 续跑完成后不走合并、仅重新汇报给主会话；同样条件的非共享 bee 不会被纳入续跑候选。
func TestSharedBeeReactivate(t *testing.T) {
	t.Parallel()

	sharedCandidate := Bee{
		ThreadID:        "bee-shared-reported",
		Status:          StatusCompleted,
		Reported:        true,
		Merged:          true,
		SharedWorkspace: true,
		WorktreePath:    "",
	}
	if !isReactivateCandidate(sharedCandidate) {
		t.Fatal("已汇报、WorktreePath 为空的共享 bee 应为续跑候选")
	}

	nonSharedCandidate := Bee{
		ThreadID:        "bee-nonshared-empty-wt",
		Status:          StatusCompleted,
		Reported:        true,
		Merged:          true,
		SharedWorkspace: false,
		WorktreePath:    "",
	}
	if isReactivateCandidate(nonSharedCandidate) {
		t.Fatal("WorktreePath 为空的非共享 bee 不应成为续跑候选")
	}

	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	var reportMu sync.Mutex
	var reports []string

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
			ReviewMode:    "human",
			Tiers: []Tier{
				{ID: "design", Name: "设计", SharedWorkspace: true},
			},
		}
	})

	const leadID = "lead-shared-reactivate"
	bee, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "画设计稿", Prompt: "第一轮设计", Tier: "design"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if !bee.SharedWorkspace || bee.WorktreePath != "" {
		t.Fatalf("期望共享 bee 且 WorktreePath 为空，实际: %+v", bee)
	}

	// 插入一条同为已汇报且 WorktreePath 为空的非共享 bee，确认探测不会误激活它。
	const nonSharedID = "bee-reclaimed-nonshared"
	driver.setSessionState(nonSharedID, kanban.SessionState{Running: true})
	mgr.mu.Lock()
	mgr.leads[leadID].bees = append(mgr.leads[leadID].bees, Bee{
		ThreadID:     nonSharedID,
		Index:        99,
		Name:         "Reclaimed",
		Status:       StatusCompleted,
		Reported:     true,
		Merged:       true,
		WorktreePath: "",
	})
	mgr.mu.Unlock()

	// 第一轮完成并汇报。
	driver.setSessionState(bee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "第一轮设计完成",
	})
	mgr.Tick(ctx)

	got := beeByThread(t, mgr, leadID, bee.ThreadID)
	if got.Status != StatusCompleted || !got.Reported || !got.Merged || got.PendingMerge {
		t.Fatalf("第一轮结束后共享 bee 应已完成、已汇报且不进待合并，实际: %+v", got)
	}
	reportMu.Lock()
	if len(reports) != 1 || !strings.Contains(reports[0], "第一轮设计完成") {
		reportMu.Unlock()
		t.Fatalf("第一轮应投递 1 次汇报，实际: %v", reports)
	}
	reportMu.Unlock()

	// 用户在共享 bee 会话里发消息续聊，连续两次探测到 Running 后唤醒。
	driver.setSessionState(bee.ThreadID, kanban.SessionState{
		Running:           true,
		LastAssistantText: "第一轮设计完成",
	})
	clock.Advance(reactivateProbeInterval)
	mgr.Tick(ctx)
	if mid := beeByThread(t, mgr, leadID, bee.ThreadID); mid.Status != StatusCompleted {
		t.Fatalf("首次探测不应立即激活，实际状态: %s", mid.Status)
	}

	clock.Advance(reactivateProbeInterval)
	mgr.Tick(ctx)
	got = beeByThread(t, mgr, leadID, bee.ThreadID)
	if got.Status != StatusRunning || got.Reported || !got.SharedWorkspace {
		t.Fatalf("二次探测后共享 bee 应回到 Running 且保留 SharedWorkspace，实际: %+v", got)
	}
	if nonShared := beeByThread(t, mgr, leadID, nonSharedID); nonShared.Status != StatusCompleted {
		t.Fatalf("WorktreePath 为空的非共享 bee 不应被唤醒，实际状态: %s", nonShared.Status)
	}

	// 续跑轮完成，应跳过合并/审查并重新汇报给主会话。
	driver.setSessionState(bee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "第二轮修改完成",
	})
	clock.Advance(2 * time.Second)
	mgr.Tick(ctx)

	got = beeByThread(t, mgr, leadID, bee.ThreadID)
	if got.Status != StatusCompleted || !got.Reported || !got.Merged || got.PendingMerge || got.MergeError != "" {
		t.Fatalf("续跑完成后应直接完成并汇报、不走合并且无错误，实际: %+v", got)
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	if len(reports) != 2 {
		t.Fatalf("续跑完成后应重新汇报给主会话，期望 2 次汇报，实际 %d 次: %v", len(reports), reports)
	}
	if !strings.Contains(reports[1], "第二轮修改完成") {
		t.Fatalf("第二次汇报应包含最新输出，实际:\n%s", reports[1])
	}
}
