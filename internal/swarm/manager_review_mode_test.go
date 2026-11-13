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
	"mcode/internal/testenv"
)

// collectReports 让管理器把汇报收进切片，返回读取函数；主会话显式登记为未运行，
// 因为汇报只在主会话空闲的 tick 投递。
func collectReports(m *Manager, leadThreadID string) func() []string {
	driver := m.opts.Driver.(*fakeDriver)
	driver.setSessionState(leadThreadID, kanban.SessionState{Running: false})
	var reports []string
	m.opts.OnReport = func(_, message string) error {
		reports = append(reports, message)
		return nil
	}
	return func() []string { return reports }
}

// 人工审查档：待审查期间不汇报，批准后的下一个 Tick 汇报一次并写明审查通过。
func TestReviewMode_HumanApproveReportsAfterDecision(t *testing.T) {
	m, _, bee := pendingReviewBee(t)
	ctx := context.Background()
	reports := collectReports(m, "lead-review")

	m.Tick(ctx)
	if got := reports(); len(got) != 0 {
		t.Fatalf("待审查期间不应汇报: %v", got)
	}
	if _, err := m.ApproveMerge(ctx, "lead-review", bee.ThreadID); err != nil {
		t.Fatalf("ApproveMerge: %v", err)
	}
	m.Tick(ctx)
	got := reports()
	if len(got) != 1 {
		t.Fatalf("批准后应汇报一次，实际 %d 条", len(got))
	}
	if !strings.Contains(got[0], "已合入（审查通过）") {
		t.Fatalf("汇报应写明审查通过: %s", got[0])
	}
}

// 人工审查档：放弃合并后下一个 Tick 汇报一次，正文写明已放弃。
func TestReviewMode_HumanDiscardReportsAfterDecision(t *testing.T) {
	m, _, bee := pendingReviewBee(t)
	ctx := context.Background()
	reports := collectReports(m, "lead-review")

	m.Tick(ctx)
	if got := reports(); len(got) != 0 {
		t.Fatalf("待审查期间不应汇报: %v", got)
	}
	if err := m.DiscardMerge(ctx, "lead-review", bee.ThreadID); err != nil {
		t.Fatalf("DiscardMerge: %v", err)
	}
	m.Tick(ctx)
	got := reports()
	if len(got) != 1 {
		t.Fatalf("放弃合并后应汇报一次，实际 %d 条", len(got))
	}
	if !strings.Contains(got[0], "已放弃合并") {
		t.Fatalf("汇报应写明已放弃: %s", got[0])
	}
}

// 一个 bee 仍在审查、另一个已经结束时，整批都不汇报；审查通过后一次汇报里两个 bee 都有结果。
func TestReviewMode_ReviewRunningHoldsBatchReport(t *testing.T) {
	m, dir, first := pendingReviewBee(t)
	ctx := context.Background()
	reports := collectReports(m, "lead-review")

	// 另一个 bee 结束时没有任何改动：补丁为空，直接算已合入。
	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: "lead-review", LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Birch", Task: "只做检查，不改文件"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver := m.opts.Driver.(*fakeDriver)
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "无需改动"})
	// 把第一个 bee 标成 AI 审查进行中，模拟审查模型尚未给出结论。
	m.mu.Lock()
	m.leads["lead-review"].bees[0].ReviewState = ReviewRunning
	m.mu.Unlock()

	m.Tick(ctx)
	if got := reports(); len(got) != 0 {
		t.Fatalf("审查进行中不应汇报: %v", got)
	}
	if second := m.State("lead-review").Bees[1]; !second.Merged || second.PendingMerge {
		t.Fatalf("无改动的 bee 应直接算已合入: %+v", second)
	}

	m.applyReviewApprove(ctx, "lead-review", first.ThreadID, "")
	m.Tick(ctx)
	got := reports()
	if len(got) != 1 {
		t.Fatalf("审查结束后应一次性汇报，实际 %d 条", len(got))
	}
	for _, name := range []string{first.Name, "Birch"} {
		if !strings.Contains(got[0], name) {
			t.Fatalf("汇报缺少 bee 名字 %s: %s", name, got[0])
		}
	}
}

// 拒绝审查只标记结论，不丢补丁与工作副本，汇报里给出审查意见和补丁路径供返工。
func TestReviewMode_RejectKeepsWorktreeAndReportsReason(t *testing.T) {
	m, _, bee := pendingReviewBee(t)
	ctx := context.Background()
	reports := collectReports(m, "lead-review")

	const reason = "缺少针对新文件的测试"
	patchPath := filepath.Join(t.TempDir(), "lead-review-0.patch")
	m.applyReviewReject("lead-review", bee.ThreadID, reason)
	m.mu.Lock()
	m.leads["lead-review"].bees[0].ReviewPatchPath = patchPath
	m.mu.Unlock()
	h := beeByThread(t, m, "lead-review", bee.ThreadID)
	if h.ReviewState != ReviewRejected {
		t.Fatalf("拒绝后应为 rejected，实际 %s", h.ReviewState)
	}
	if h.MergeDiscarded {
		t.Fatal("拒绝不能让副本进入已放弃状态，返工仍以补丁为准")
	}
	if h.PendingMerge || h.Merged {
		t.Fatalf("拒绝后不应保持待审查或已合入: %+v", h)
	}
	if len(h.MergePatch) == 0 {
		t.Fatal("拒绝后应保留补丁")
	}
	if _, err := os.Stat(h.WorktreePath); err != nil {
		t.Fatalf("工作副本应保留: %v", err)
	}

	m.Tick(ctx)
	got := reports()
	if len(got) != 1 {
		t.Fatalf("拒绝后应汇报一次，实际 %d 条", len(got))
	}
	for _, want := range []string{"审查意见：" + reason, "补丁：未合入", patchPath, "审查未通过"} {
		if !strings.Contains(got[0], want) {
			t.Fatalf("汇报应包含 %q: %s", want, got[0])
		}
	}
	if h.WorktreePath != "" && strings.Contains(got[0], h.WorktreePath) {
		t.Fatalf("汇报不应再给出工作副本路径 %q: %s", h.WorktreePath, got[0])
	}
}

// 交回人工后仍保持待审查，且不汇报。
func TestReviewMode_NeedsHumanStaysPendingAndSilent(t *testing.T) {
	m, _, bee := pendingReviewBee(t)
	ctx := context.Background()
	reports := collectReports(m, "lead-review")

	const reason = "与主工作区冲突，需人工裁决"
	m.markReviewNeedsHuman("lead-review", bee.ThreadID, reason)
	h := beeByThread(t, m, "lead-review", bee.ThreadID)
	if h.ReviewState != ReviewNeedsHuman || !h.PendingMerge {
		t.Fatalf("应交回人工并保持待审查: %+v", h)
	}
	if h.ReviewReason != reason {
		t.Fatalf("原因未写入，实际 %q", h.ReviewReason)
	}

	m.Tick(ctx)
	if got := reports(); len(got) != 0 {
		t.Fatalf("等人工审查期间不应汇报: %v", got)
	}
}

// 审查进行中算忙并占用并发名额；只剩等人工决定的 bee 时不算忙。
func TestReviewMode_BusyDuringAIReviewOnly(t *testing.T) {
	m, _, _ := pendingReviewBee(t)
	if m.Busy("lead-review") {
		t.Fatal("只剩等人工审查的 bee 时不应算忙")
	}
	m.mu.Lock()
	m.leads["lead-review"].bees[0].ReviewState = ReviewRunning
	m.mu.Unlock()
	if !m.Busy("lead-review") {
		t.Fatal("审查进行中应算忙，占住并发名额")
	}
}

// 重启后审查会话已消失：审查中的 bee 改交人工，并清掉旧审查线程。
func TestReviewMode_RestoreHandsInterruptedReviewToHuman(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	persister := newFakePersister()
	leadID := "lead-restore-review"
	rec := LeadRecord{
		LeadThreadID: leadID,
		Enabled:      true,
		LeadWorkDir:  dir,
		Bees: []Bee{{
			ThreadID:       "bee-review",
			Name:           "Aspen",
			Task:           "改一处",
			Status:         StatusCompleted,
			PendingMerge:   true,
			MergePatch:     []byte("diff --git a/a.txt b/a.txt\n+x\n"),
			WorktreePath:   t.TempDir(),
			ReviewState:    ReviewRunning,
			ReviewThreadID: "review-thread",
		}},
	}
	if err := persister.SaveLead(ctx, rec); err != nil {
		t.Fatal(err)
	}

	m := NewManager(Options{
		Git: runner, Driver: newFakeDriver(), WorktreeDir: t.TempDir(), Persister: persister,
	})
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	h := beeByThread(t, m, leadID, "bee-review")
	if h.ReviewState != ReviewNeedsHuman {
		t.Fatalf("重启后审查应改交人工，实际 %s", h.ReviewState)
	}
	if h.ReviewReason != "MCode 重启，审查中断" {
		t.Fatalf("审查中断原因不正确: %q", h.ReviewReason)
	}
	if h.ReviewThreadID != "" {
		t.Fatalf("应清掉旧审查线程，实际 %q", h.ReviewThreadID)
	}
	if !h.PendingMerge {
		t.Fatal("工作副本仍在，应继续保持待审查")
	}
}

// AI 审查通过时主工作区尚未落定：保持审查中并保留待审查，落定后重试即合入，不转人工。
func TestManager_ReviewAI_ApproveRetriesWhenCheckoutUnsettled(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()
	clock := newTestClock()

	var touchedMu sync.Mutex
	touched := []string{"./review.txt"}
	active := true
	m := humanReviewSettings(NewManager(Options{
		Git: runner, Driver: driver, WorktreeDir: t.TempDir(),
		Persister:     newFakePersister(),
		Now:           clock.Now,
		SettleTimeout: 30 * time.Minute,
		LeadTouchedPaths: func(string) ([]string, bool) {
			touchedMu.Lock()
			defer touchedMu.Unlock()
			return append([]string(nil), touched...), active
		},
	}))
	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: "lead-review", LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Aspen", Task: "新增文件"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bees[0].WorktreePath, "review.txt"), []byte("reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{LastAssistantText: "已新增 review.txt"})
	m.Tick(ctx)
	bee := beeByThread(t, m, "lead-review", bees[0].ThreadID)
	if !bee.PendingMerge || len(bee.MergePatch) == 0 {
		t.Fatalf("bee 未进入待审查状态: %+v", bee)
	}
	// 模拟 AI 审查通过：审查中、补丁待合入。
	m.mu.Lock()
	m.leads["lead-review"].bees[0].ReviewState = ReviewRunning
	m.mu.Unlock()

	m.applyReviewApprove(ctx, "lead-review", bee.ThreadID, "")
	h := beeByThread(t, m, "lead-review", bee.ThreadID)
	if h.ReviewState != ReviewRunning {
		t.Fatalf("主工作区未落定时不应转人工，实际 %s", h.ReviewState)
	}
	if !h.PendingMerge || h.Merged {
		t.Fatalf("主工作区未落定时应保留待审查: %+v", h)
	}
	if _, err := os.Stat(filepath.Join(dir, "review.txt")); !os.IsNotExist(err) {
		t.Fatalf("主工作区未落定时不应写入: %v", err)
	}

	// 主工作区落定后重试批准即合入，审查结论记为通过。
	touchedMu.Lock()
	active = false
	touchedMu.Unlock()
	m.applyReviewApprove(ctx, "lead-review", bee.ThreadID, "")
	h = beeByThread(t, m, "lead-review", bee.ThreadID)
	if !h.Merged || h.ReviewState != ReviewApproved {
		t.Fatalf("落定后重试应合入并记为审查通过: %+v", h)
	}
	if h.MergeWaitSince != nil {
		t.Fatalf("合入后应清除等待起点")
	}
	if content, err := os.ReadFile(filepath.Join(dir, "review.txt")); err != nil || string(content) != "reviewed\n" {
		t.Fatalf("主工作区未合入审查内容: %q, %v", content, err)
	}
}
