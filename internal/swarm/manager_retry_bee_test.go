package swarm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/git"
)

// newRetryBeeManager 造一个已启用、含指定 bee 记录的管理器，供「沿用改动续做」的用例共用。
func newRetryBeeManager(t *testing.T, leadID string, bees ...Bee) (*Manager, *fakeDriver, *fakePersister, *git.Runner, string) {
	t.Helper()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	persister := newFakePersister()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})
	mgr.SetEnabled(leadID, true)

	mgr.mu.Lock()
	lead := mgr.leads[leadID]
	for _, bee := range bees {
		if bee.TargetWorkDir == "" {
			bee.TargetWorkDir = dir
		}
		if bee.ProjectID == "" {
			bee.ProjectID = "proj-1"
		}
		lead.bees = append(lead.bees, bee)
	}
	mgr.mu.Unlock()
	return mgr, driver, persister, runner, dir
}

// 验证 RetryBee 成功复用失败 bee 的原 worktree 与半成品：新 bee 替换旧条目、沿用 Index、
// 提示里带上改动文件清单，并且只给一次重试机会。
func TestManager_RetryBee_ReusesWorktreeWithPartialWork(t *testing.T) {
	const leadID = "lead-retry-reuse"
	ctx := context.Background()

	// 先手工造一份「上一个子代理改了一半」的 worktree，记录落在 bee 上。
	mgr, driver, persister, runner, dir := newRetryBeeManager(t, leadID)
	base, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("RevParse failed: %v", err)
	}
	wtPath := filepath.Join(t.TempDir(), "wt-reuse")
	branch := "mcode/swarm/lead-ret/0"
	if err := runner.WorktreeAdd(ctx, dir, wtPath, branch, base); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "partial.txt"), []byte("half done\n"), 0644); err != nil {
		t.Fatalf("write partial.txt: %v", err)
	}

	mgr.mu.Lock()
	mgr.leads[leadID].bees = append(mgr.leads[leadID].bees, Bee{
		ThreadID:      "bee-failed-1",
		Index:         0,
		Name:          "Aspen",
		Task:          "补齐改动",
		Prompt:        "原始 brief",
		Status:        StatusFailed,
		Summary:       "工具调用失败",
		WorktreePath:  wtPath,
		Branch:        branch,
		BaseCommit:    base,
		ChangedFiles:  []string{"partial.txt"},
		ProjectID:     "proj-1",
		TargetWorkDir: dir,
		RuntimeMode:   "auto",
		Reported:      true,
	})
	mgr.mu.Unlock()

	newBee, err := mgr.RetryBee(ctx, leadID, "bee-failed-1")
	if err != nil {
		t.Fatalf("RetryBee failed: %v", err)
	}
	if newBee.ThreadID == "bee-failed-1" {
		t.Fatalf("重试应派出新会话，实际仍是旧线程 %s", newBee.ThreadID)
	}
	if newBee.Status != StatusRunning {
		t.Fatalf("重试出的 bee 应为 running，实际 %s", newBee.Status)
	}
	if newBee.WorktreePath != wtPath || newBee.Branch != branch || newBee.BaseCommit != base {
		t.Fatalf("重试出的 bee 应复用原 worktree 与基线，实际 %+v", newBee)
	}
	if newBee.Index != 0 {
		t.Fatalf("重试出的 bee 应沿用原 Index=0，实际 %d", newBee.Index)
	}
	if newBee.RuntimeMode != "auto" {
		t.Fatalf("重试出的 bee 应继承原权限模式 auto，实际 %q", newBee.RuntimeMode)
	}
	if !strings.Contains(newBee.Prompt, "partial.txt") || !strings.Contains(newBee.Prompt, "工具调用失败") {
		t.Fatalf("重试提示应说明现状与改动文件：%q", newBee.Prompt)
	}
	if got := mgr.getRetryCount(newBee.ThreadID); got != 1 {
		t.Fatalf("重试出的 bee 应标记为已用掉重试机会，实际 %d", got)
	}

	// 旧条目被新 bee 替换，数量不变；落盘记录同步更新。
	st := mgr.State(leadID)
	if len(st.Bees) != 1 || st.Bees[0].ThreadID != newBee.ThreadID {
		t.Fatalf("lead 里应只剩替换后的新 bee，实际 %+v", st.Bees)
	}
	if persisted := persister.leads[leadID]; len(persisted.Bees) != 1 || persisted.Bees[0].ThreadID != newBee.ThreadID {
		t.Fatalf("落盘记录应保存重试后的新 bee：%+v", persisted.Bees)
	}

	// 新会话确实走到了驱动层，且仍挂在原主会话下。
	driver.mu.Lock()
	relation := driver.relations[newBee.ThreadID]
	driver.mu.Unlock()
	if relation.parentThreadID != leadID || !relation.isHelper {
		t.Fatalf("新 bee 会话应挂在原主会话下，实际 %+v", relation)
	}
}

// 验证状态不是 failed/stopped 的 bee（这里用已完成的）不允许沿用改动续做。
func TestManager_RetryBee_RejectsFinishedStatus(t *testing.T) {
	const leadID = "lead-retry-status"
	ctx := context.Background()

	mgr, _, _, _, _ := newRetryBeeManager(t, leadID, Bee{
		ThreadID: "bee-done-1",
		Index:    0,
		Name:     "Birch",
		Task:     "已完成",
		Status:   StatusCompleted,
	})
	if _, err := mgr.RetryBee(ctx, leadID, "bee-done-1"); err == nil || !strings.Contains(err.Error(), "只有失败或已停止") {
		t.Fatalf("已完成状态应被拒绝，实际 %v", err)
	}

	// 已合并的失败 bee 同样不能沿用改动续做。
	mgr.mu.Lock()
	mgr.leads[leadID].bees = append(mgr.leads[leadID].bees, Bee{
		ThreadID: "bee-merged-2",
		Index:    1,
		Name:     "Cedar",
		Status:   StatusFailed,
		Merged:   true,
	})
	mgr.mu.Unlock()
	if _, err := mgr.RetryBee(ctx, leadID, "bee-merged-2"); err == nil || !strings.Contains(err.Error(), "已合并") {
		t.Fatalf("已合并的 bee 应被拒绝，实际 %v", err)
	}

	// 被拒绝后旧记录保持原样，不会残留派发中的占位状态。
	if st := mgr.State(leadID); len(st.Bees) != 2 || st.Bees[0].ThreadID != "bee-done-1" {
		t.Fatalf("拒绝后不应改动 bee 列表：%+v", st.Bees)
	}
}

// 验证 bee 不属于该主会话时不重试：既接不住别的 lead 的 bee，也接不住不存在的线程。
func TestManager_RetryBee_RejectsBeeFromOtherLead(t *testing.T) {
	const leadID = "lead-retry-owner"
	const otherLeadID = "lead-retry-other"
	ctx := context.Background()

	mgr, _, _, _, _ := newRetryBeeManager(t, leadID, Bee{
		ThreadID: "bee-mine-1",
		Index:    0,
		Name:     "Aspen",
		Task:     "我的任务",
		Status:   StatusFailed,
	})
	mgr.SetEnabled(otherLeadID, true)
	mgr.mu.Lock()
	mgr.leads[otherLeadID].bees = append(mgr.leads[otherLeadID].bees, Bee{
		ThreadID: "bee-other-1",
		Index:    0,
		Name:     "Elm",
		Task:     "别人的任务",
		Status:   StatusFailed,
	})
	mgr.mu.Unlock()

	if _, err := mgr.RetryBee(ctx, leadID, "bee-other-1"); !errors.Is(err, ErrBeeNotFound) {
		t.Fatalf("不属于该 lead 的 bee 应返回 ErrBeeNotFound，实际 %v", err)
	}
	if _, err := mgr.RetryBee(ctx, leadID, "bee-missing"); !errors.Is(err, ErrBeeNotFound) {
		t.Fatalf("不存在的 bee 应返回 ErrBeeNotFound，实际 %v", err)
	}
	if _, err := mgr.RetryBee(ctx, "lead-missing", "bee-mine-1"); !errors.Is(err, ErrLeadNotFound) {
		t.Fatalf("主会话不存在时应返回 ErrLeadNotFound，实际 %v", err)
	}

	// 别的 lead 的 bee 必须原封不动。
	st := mgr.State(otherLeadID)
	if len(st.Bees) != 1 || st.Bees[0].ThreadID != "bee-other-1" || st.Bees[0].Status != StatusFailed {
		t.Fatalf("跨会话的重试请求不应改动另一个主会话的记录：%+v", st.Bees)
	}
}

// 验证原 worktree 已被删除时退回新建 worktree，并在提示里说明半成品已丢失。
func TestManager_RetryBee_FallsBackWhenWorktreeGone(t *testing.T) {
	const leadID = "lead-retry-gone"
	ctx := context.Background()

	goneWorktree := filepath.Join(t.TempDir(), "gone")
	mgr, _, _, _, dir := newRetryBeeManager(t, leadID, Bee{
		ThreadID:     "bee-stopped-1",
		Index:        0,
		Name:         "Fir",
		Task:         "补测试",
		Prompt:       "原始 brief",
		Status:       StatusStopped,
		Summary:      "已停止",
		WorktreePath: goneWorktree,
		Branch:       "mcode/swarm/lead-ret/0",
		BaseCommit:   "abc123",
	})

	newBee, err := mgr.RetryBee(ctx, leadID, "bee-stopped-1")
	if err != nil {
		t.Fatalf("RetryBee failed: %v", err)
	}
	if newBee.WorktreePath == "" || newBee.WorktreePath == goneWorktree {
		t.Fatalf("worktree 已不存在时应新建，实际 %q", newBee.WorktreePath)
	}
	if _, statErr := os.Stat(newBee.WorktreePath); statErr != nil {
		t.Fatalf("新建的 worktree 应该真实存在：%v", statErr)
	}
	if newBee.BaseCommit == "abc123" {
		t.Fatalf("新建 worktree 应重新取基线，实际沿用了旧基线 %q", newBee.BaseCommit)
	}
	if newBee.TargetWorkDir != dir {
		t.Fatalf("新 bee 仍应以原工作区为目标目录，实际 %q", newBee.TargetWorkDir)
	}
	if !strings.Contains(newBee.Prompt, "工作副本已经不存在") || !strings.Contains(newBee.Prompt, "原始 brief") {
		t.Fatalf("半成品丢失时提示应说明并从原 brief 重做：%q", newBee.Prompt)
	}
	if st := mgr.State(leadID); len(st.Bees) != 1 || st.Bees[0].ThreadID != newBee.ThreadID {
		t.Fatalf("lead 里应只剩替换后的新 bee，实际 %+v", st.Bees)
	}
}
