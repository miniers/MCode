package swarm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/kanban"
)

func newHumanApproveLandingManager(t *testing.T) (*Manager, *fakeDriver, string) {
	t.Helper()
	runner, root := initTestRepo(t)
	writePipelineFile(t, root, "lines.txt", landingBase)
	gitTestRun(t, root, "add", "lines.txt")
	gitTestRun(t, root, "commit", "-qm", "base lines")
	driver := newFakeDriver()
	m := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: filepath.Join(t.TempDir(), "worktrees"),
	})
	m.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
			AutoMerge:     true,
			ReviewMode:    "human",
		}
	})
	return m, driver, root
}

func TestManager_HumanApprove_ConflictWaitRetriesOnTick(t *testing.T) {
	t.Parallel()
	m, driver, root := newHumanApproveLandingManager(t)
	var touchedMu sync.Mutex
	active := true
	m.opts.LeadTouchedPaths = func(string) ([]string, bool) {
		touchedMu.Lock()
		defer touchedMu.Unlock()
		return []string{"lines.txt"}, active
	}
	ctx := context.Background()
	const leadID = "lead-human-approve-wait"

	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  root,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "修改文本", Prompt: "修改文本"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]

	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})

	m.Tick(ctx)
	got := m.State(leadID).Bees[0]
	if got.Status != StatusCompleted || !got.PendingMerge {
		t.Fatalf("未进入待审查状态: %+v", got)
	}

	before := gitTestRun(t, root, "rev-parse", "HEAD")
	userText := strings.Replace(landingBase, "one\n", "user\n", 1)
	writePipelineFile(t, root, "lines.txt", userText)

	approved, err := m.ApproveMerge(ctx, leadID, bee.ThreadID)
	if err == nil {
		t.Fatalf("有未提交冲突时 ApproveMerge 应返回等待错误")
	}
	if !approved.PendingMerge || approved.MergeWaitSince == nil || approved.Merged {
		t.Fatalf("批准遇冲突后应保留等待状态: %+v", approved)
	}
	if head := gitTestRun(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatalf("等待时移动了 HEAD: got=%s want=%s", head, before)
	}
	assertLandingFile(t, root, userText)

	writePipelineFile(t, root, "lines.txt", landingBase)
	touchedMu.Lock()
	active = false
	touchedMu.Unlock()
	m.Tick(ctx)

	got = m.State(leadID).Bees[0]
	if !got.Merged || got.PendingMerge || got.MergeWaitSince != nil {
		t.Fatalf("Tick 重试后应完成合入并清除等待: %+v", got)
	}
	assertLandingCommit(t, m, root, got)
	assertLandingFile(t, root, beeText)
}

func TestManager_DiscardMerge_ClearsMergeWait(t *testing.T) {
	t.Parallel()
	m, driver, root := newHumanApproveLandingManager(t)
	m.opts.LeadTouchedPaths = func(string) ([]string, bool) {
		return []string{"lines.txt"}, true
	}
	ctx := context.Background()
	const leadID = "lead-discard-wait"

	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  root,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "修改文本", Prompt: "修改文本"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]

	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})

	m.Tick(ctx)
	userText := strings.Replace(landingBase, "one\n", "user\n", 1)
	writePipelineFile(t, root, "lines.txt", userText)

	if _, err := m.ApproveMerge(ctx, leadID, bee.ThreadID); err == nil {
		t.Fatalf("有未提交冲突时 ApproveMerge 应返回等待错误")
	}
	if got := m.State(leadID).Bees[0]; !got.PendingMerge || got.MergeWaitSince == nil {
		t.Fatalf("前置等待状态未建立: %+v", got)
	}

	if err := m.DiscardMerge(ctx, leadID, bee.ThreadID); err != nil {
		t.Fatalf("DiscardMerge 失败: %v", err)
	}
	got := m.State(leadID).Bees[0]
	if got.MergeWaitSince != nil || got.PendingMerge || !got.MergeDiscarded || got.Merged {
		t.Fatalf("DiscardMerge 后状态异常: %+v", got)
	}

	writePipelineFile(t, root, "lines.txt", landingBase)
	m.Tick(ctx)

	got = m.State(leadID).Bees[0]
	if got.Merged || got.PendingMerge || got.MergeWaitSince != nil || !got.MergeDiscarded {
		t.Fatalf("DiscardMerge 后 Tick 不应重新合并: %+v", got)
	}
	assertLandingFile(t, root, landingBase)
}

func setupPendingPatchWaitBee(t *testing.T, leadID string) (*Manager, *fakeDriver, *fakePersister, string, Bee) {
	t.Helper()
	runner, root := initTestRepo(t)
	writePipelineFile(t, root, "lines.txt", landingBase)
	gitTestRun(t, root, "add", "lines.txt")
	gitTestRun(t, root, "commit", "-qm", "base lines")
	driver := newFakeDriver()
	persister := newFakePersister()
	m := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		Persister:   persister,
		WorktreeDir: filepath.Join(t.TempDir(), "worktrees"),
	})
	m.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
			AutoMerge:     true,
			ReviewMode:    "human",
		}
	})
	ctx := context.Background()
	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  root,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "修改文本", Prompt: "修改文本"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]
	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})
	m.Tick(ctx)

	got := m.State(leadID).Bees[0]
	if got.Status != StatusCompleted || !got.PendingMerge || len(got.MergePatch) == 0 {
		t.Fatalf("未进入待审查状态: %+v", got)
	}
	return m, driver, persister, root, got
}

func TestApproveMergePatchChangedRequeuesReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("非human档重进L0审查", func(t *testing.T) {
		t.Parallel()
		const leadID = "lead-patch-changed-ai"
		m, _, _, _, bee := setupPendingPatchWaitBee(t, leadID)

		m.SetSettingsFunc(func() Settings {
			return Settings{
				Enabled:       true,
				AutoDelegate:  true,
				WorkspaceMode: "worktree",
				AutoMerge:     true,
				ReviewMode:    "ai",
			}
		})

		waitSince := time.Now().Add(-time.Minute)
		startedAt := time.Now().Add(-30 * time.Second)
		m.mu.Lock()
		b := &m.leads[leadID].bees[0]
		b.MergeWaitSince = &waitSince
		b.MergeError = "旧等待错误"
		b.ReviewState = ReviewNeedsHuman
		b.ReviewLayer = "l2"
		b.ReviewReason = "旧审查理由"
		b.ReviewConcerns = []string{"旧疑点"}
		b.ReviewApplyPending = true
		b.ReviewStartedAt = &startedAt
		b.ReviewAttempts = 2
		origPatch := append([]byte(nil), b.MergePatch...)
		m.mu.Unlock()

		updatedText := strings.Replace(landingBase, "one\n", "bee-updated\n", 1)
		writePipelineFile(t, bee.WorktreePath, "lines.txt", updatedText)
		gitTestRun(t, bee.WorktreePath, "add", "lines.txt")
		wantPatch, err := m.opts.Git.DiffCachedBinary(ctx, bee.WorktreePath, bee.BaseCommit)
		if err != nil {
			t.Fatalf("DiffCachedBinary 失败: %v", err)
		}
		if bytes.Equal(wantPatch, origPatch) {
			t.Fatal("新暂存补丁应与原补丁不同")
		}

		got, err := m.approveMerge(ctx, leadID, bee.ThreadID, false)
		if err == nil || !strings.Contains(err.Error(), "审查补丁已变化") {
			t.Fatalf("补丁变化时应返回重审错误，实际 err=%v", err)
		}
		if !bytes.Equal(got.MergePatch, wantPatch) {
			t.Fatalf("MergePatch 未更新为当前暂存补丁")
		}
		if got.MergeWaitSince != nil || got.MergeError != "" {
			t.Fatalf("MergeWaitSince 与 MergeError 应清零: %+v", got)
		}
		if got.ReviewState != ReviewRunning || got.ReviewStage != ReviewStageL0 {
			t.Fatalf("应重新进入 L0 审查: state=%s stage=%s", got.ReviewState, got.ReviewStage)
		}
		if got.ReviewQueuedAt == nil || got.ReviewStartedAt != nil || got.ReviewApplyPending || got.ReviewAttempts != 0 {
			t.Fatalf("审查调度字段未正确复位: %+v", got)
		}
		stored := m.State(leadID).Bees[0]
		if !bytes.Equal(stored.MergePatch, wantPatch) || stored.ReviewState != ReviewRunning || stored.ReviewStage != ReviewStageL0 || stored.MergeWaitSince != nil {
			t.Fatalf("持久化状态未更新: %+v", stored)
		}
	})

	t.Run("human档无验收命令转人工重审", func(t *testing.T) {
		t.Parallel()
		const leadID = "lead-patch-changed-human"
		m, _, _, _, bee := setupPendingPatchWaitBee(t, leadID)

		waitSince := time.Now().Add(-time.Minute)
		m.mu.Lock()
		b := &m.leads[leadID].bees[0]
		b.MergeWaitSince = &waitSince
		b.MergeError = "旧等待错误"
		b.ReviewConcerns = []string{"旧疑点"}
		b.ReviewApplyPending = true
		origPatch := append([]byte(nil), b.MergePatch...)
		m.mu.Unlock()

		updatedText := strings.Replace(landingBase, "one\n", "bee-human-updated\n", 1)
		writePipelineFile(t, bee.WorktreePath, "lines.txt", updatedText)
		gitTestRun(t, bee.WorktreePath, "add", "lines.txt")
		wantPatch, err := m.opts.Git.DiffCachedBinary(ctx, bee.WorktreePath, bee.BaseCommit)
		if err != nil {
			t.Fatalf("DiffCachedBinary 失败: %v", err)
		}
		if bytes.Equal(wantPatch, origPatch) {
			t.Fatal("新暂存补丁应与原补丁不同")
		}

		got, err := m.approveMerge(ctx, leadID, bee.ThreadID, false)
		if err == nil || !strings.Contains(err.Error(), "审查补丁已变化") {
			t.Fatalf("补丁变化时应返回重审错误，实际 err=%v", err)
		}
		if !bytes.Equal(got.MergePatch, wantPatch) {
			t.Fatalf("MergePatch 未更新为当前暂存补丁")
		}
		if got.MergeWaitSince != nil || got.MergeError != "" {
			t.Fatalf("MergeWaitSince 与 MergeError 应清零: %+v", got)
		}
		if got.ReviewState != ReviewNeedsHuman || got.ReviewLayer != "human" || got.ReviewReason != "暂存补丁与获批补丁不一致，需重新审查" {
			t.Fatalf("human 档应转为待人工重审: state=%s layer=%s reason=%q", got.ReviewState, got.ReviewLayer, got.ReviewReason)
		}
	})
}

func TestApproveWaitRetryStopsOnPermanentError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("errMergeNotSettled保持MergeWaitSince不变", func(t *testing.T) {
		t.Parallel()
		const leadID = "lead-wait-not-settled"
		m, _, _, root, bee := setupPendingPatchWaitBee(t, leadID)
		m.opts.LeadTouchedPaths = func(string) ([]string, bool) {
			return []string{"lines.txt"}, true
		}

		userText := strings.Replace(landingBase, "one\n", "user\n", 1)
		writePipelineFile(t, root, "lines.txt", userText)

		approved, err := m.ApproveMerge(ctx, leadID, bee.ThreadID)
		if err == nil {
			t.Fatal("有未提交冲突时 ApproveMerge 应返回等待错误")
		}
		if approved.MergeWaitSince == nil {
			t.Fatal("首次等待应记录 MergeWaitSince")
		}
		wantSince := *approved.MergeWaitSince

		m.Tick(ctx)

		got := m.State(leadID).Bees[0]
		if !got.PendingMerge || got.MergeWaitSince == nil || !got.MergeWaitSince.Equal(wantSince) {
			t.Fatalf("errMergeNotSettled 后 MergeWaitSince 应保持不变: got=%+v wantSince=%v", got, wantSince)
		}
	})

	t.Run("缺少审查补丁时一次Tick后停止重试", func(t *testing.T) {
		t.Parallel()
		const leadID = "lead-wait-permanent-empty-patch"
		m, _, persister, _, _ := setupPendingPatchWaitBee(t, leadID)

		waitSince := time.Now().Add(-time.Minute)
		m.mu.Lock()
		b := &m.leads[leadID].bees[0]
		b.MergeWaitSince = &waitSince
		b.MergePatch = nil
		b.ReviewState = ReviewApproved
		m.mu.Unlock()

		changeCalls := 0
		m.SetOnChange(func(id string) {
			if id == leadID {
				changeCalls++
			}
		})
		baseSaves := persister.saveCount(leadID)

		m.Tick(ctx)

		got := m.State(leadID).Bees[0]
		if got.MergeWaitSince != nil {
			t.Fatalf("永久错误后 MergeWaitSince 应清零: %+v", got)
		}
		if got.ReviewState != ReviewNeedsHuman {
			t.Fatalf("永久错误后应转为 ReviewNeedsHuman，实际 %s", got.ReviewState)
		}
		if got.MergeError == "" || got.ReviewReason != got.MergeError {
			t.Fatalf("永久错误后应记录 MergeError 与 ReviewReason: err=%q reason=%q", got.MergeError, got.ReviewReason)
		}
		if changeCalls != 1 || persister.saveCount(leadID) != baseSaves+1 {
			t.Fatalf("首次永久错误应落盘并通知一次: changes=%d saves=%d base=%d", changeCalls, persister.saveCount(leadID), baseSaves)
		}

		m.Tick(ctx)

		after := m.State(leadID).Bees[0]
		if changeCalls != 1 || persister.saveCount(leadID) != baseSaves+1 {
			t.Fatalf("第二次 Tick 不应再次触发通知或落盘: changes=%d saves=%d base=%d", changeCalls, persister.saveCount(leadID), baseSaves)
		}
		if after.MergeWaitSince != nil || after.ReviewState != ReviewNeedsHuman || after.MergeError != got.MergeError {
			t.Fatalf("第二次 Tick 不应改变 bee 状态: %+v", after)
		}
	})

	t.Run("DiffCachedBinary失败时一次Tick后停止重试", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell git wrapper test runs on POSIX")
		}
		t.Parallel()
		const leadID = "lead-wait-diff-error"
		m, _, _, _, _ := setupPendingPatchWaitBee(t, leadID)

		realGit := m.opts.Git.Bin
		scriptDir := t.TempDir()
		countFile := filepath.Join(scriptDir, "diff_calls")
		quote := func(s string) string {
			return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
		}
		body := "#!/bin/sh\nset -eu\nreal_git=" + quote(realGit) + "\ncount_file=" + quote(countFile) + "\nif [ \"$#\" -ge 3 ] && [ \"$1\" = \"diff\" ] && [ \"$2\" = \"--cached\" ] && [ \"$3\" = \"--binary\" ]; then\n  printf '1\\n' >> \"$count_file\"\n  printf 'simulated diff failure\\n' >&2\n  exit 1\nfi\nexec \"$real_git\" \"$@\"\n"
		wrapper := filepath.Join(scriptDir, "git")
		if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
			t.Fatalf("创建 git 替身失败: %v", err)
		}
		m.opts.Git = git.NewRunner(wrapper, 4)

		waitSince := time.Now().Add(-time.Minute)
		m.mu.Lock()
		b := &m.leads[leadID].bees[0]
		b.MergeWaitSince = &waitSince
		b.ReviewState = ReviewApproved
		m.mu.Unlock()

		m.Tick(ctx)

		raw, err := os.ReadFile(countFile)
		if err != nil {
			t.Fatalf("读取调用计数失败: %v", err)
		}
		if lines := strings.Count(string(raw), "\n"); lines != 1 {
			t.Fatalf("首次 Tick 应只调用一次 DiffCachedBinary，实际 %d", lines)
		}
		got := m.State(leadID).Bees[0]
		if got.MergeWaitSince != nil || got.ReviewState != ReviewNeedsHuman || !strings.Contains(got.MergeError, "读取暂存补丁失败") {
			t.Fatalf("DiffCachedBinary 失败后状态异常: %+v", got)
		}

		m.Tick(ctx)

		raw2, err := os.ReadFile(countFile)
		if err != nil {
			t.Fatalf("读取调用计数失败: %v", err)
		}
		if lines := strings.Count(string(raw2), "\n"); lines != 1 {
			t.Fatalf("第二次 Tick 不应再调用 DiffCachedBinary，实际 %d", lines)
		}
	})
}
