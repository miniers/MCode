package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

func setupPRFlowEnv(t *testing.T, number int, headBranch string) (*Client, string, string, string, string) {
	t.Helper()
	c, mainRoot, threadID := branchClient(t)
	c.dataDir = t.TempDir()
	ctx := context.Background()

	bare := filepath.Join(t.TempDir(), "origin.git")
	branchGit(t, mainRoot, "init", "--bare", bare)
	branchGit(t, mainRoot, "remote", "add", "origin", bare)
	base := branchGit(t, mainRoot, "branch", "--show-current")
	branchGit(t, mainRoot, "push", "origin", "HEAD:refs/heads/"+base, "HEAD:refs/heads/"+headBranch)

	wtPath := prWorktreePath(c.dataDir, mainRoot, number)
	if err := c.gitRunner().PRCheckoutWorktree(ctx, mainRoot, wtPath, git.ForgeGitHub, number, headBranch, false); err != nil {
		t.Fatal(err)
	}
	if err := c.GitSwitchWorktree(ctx, threadID, wtPath); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameThread(threadID, "PR 审查"); err != nil {
		t.Fatal(err)
	}
	return c, mainRoot, bare, wtPath, threadID
}

func TestGitPRMergeFlow(t *testing.T) {
	ctx := context.Background()

	t.Run("Dirty 时在 push 前拦截", func(t *testing.T) {
		c, _, _, wtPath, threadID := setupPRFlowEnv(t, 1, "feat-1")
		writeRevertRepoFile(t, wtPath, "uncommitted.txt", "dirty\n")

		origDetail := gitPRDetailFunc
		origMerge := gitPRMergeFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRMergeFunc = origMerge
		})

		detailCalled := false
		mergeCalled := false
		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, _ int) (git.PRDetail, error) {
			detailCalled = true
			return git.PRDetail{PRSummary: git.PRSummary{Number: 1, BaseBranch: "master"}}, nil
		}
		gitPRMergeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string) error {
			mergeCalled = true
			return nil
		}

		var steps []string
		failedStep, err := c.GitPRMergeFlow(ctx, threadID, 1, "squash", "", func(step string) {
			steps = append(steps, step)
		})
		if failedStep != "push" {
			t.Fatalf("failedStep = %q，期望 %q", failedStep, "push")
		}
		if err == nil || !strings.Contains(err.Error(), "有未提交的改动") {
			t.Fatalf("err = %v，期望提示未提交改动", err)
		}
		if len(steps) != 0 {
			t.Fatalf("onStep 被意外调用: %v", steps)
		}
		if detailCalled || mergeCalled {
			t.Fatalf("不应调用 detail(%v) 或 merge(%v)", detailCalled, mergeCalled)
		}
	})

	t.Run("from=sync 时不调用 merge", func(t *testing.T) {
		c, mainRoot, bare, wtPath, threadID := setupPRFlowEnv(t, 2, "feat-2")
		base := branchGit(t, mainRoot, "branch", "--show-current")

		// 模拟远端 base 已合并提交并前进。
		other := t.TempDir()
		branchGit(t, other, "clone", bare, ".")
		branchGit(t, other, "config", "user.name", "测试")
		branchGit(t, other, "config", "user.email", "test@example.com")
		branchGit(t, other, "checkout", base)
		writeRevertRepoFile(t, other, "merged.txt", "merged\n")
		branchGit(t, other, "add", "merged.txt")
		branchGit(t, other, "commit", "-m", "squash merged pr 2")
		branchGit(t, other, "push", "origin", base)
		wantBaseSHA := branchGit(t, other, "rev-parse", "HEAD")

		origDetail := gitPRDetailFunc
		origMerge := gitPRMergeFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRMergeFunc = origMerge
		})

		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, number int) (git.PRDetail, error) {
			return git.PRDetail{PRSummary: git.PRSummary{Number: number, BaseBranch: base}}, nil
		}
		mergeCalled := false
		gitPRMergeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string) error {
			mergeCalled = true
			return nil
		}

		var steps []string
		failedStep, err := c.GitPRMergeFlow(ctx, threadID, 2, "squash", "sync", func(step string) {
			steps = append(steps, step)
		})
		if failedStep != "" || err != nil {
			t.Fatalf("GitPRMergeFlow 失败: step=%q err=%v", failedStep, err)
		}
		if mergeCalled {
			t.Fatal("from=sync 时不应调用 merge")
		}
		if !reflect.DeepEqual(steps, []string{"sync", "cleanup"}) {
			t.Fatalf("steps = %v，期望 [sync cleanup]", steps)
		}
		if gotSHA := branchGit(t, mainRoot, "rev-parse", "HEAD"); gotSHA != wantBaseSHA {
			t.Fatalf("mainRoot HEAD = %s，期望快进到 %s", gotSHA, wantBaseSHA)
		}
		if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
			t.Fatalf("worktree 目录未被清理: %v", err)
		}

		includeArchived := true
		list, err := c.backend.ListThreads(ctx, wire.ThreadListParams{IncludeArchived: &includeArchived})
		if err != nil {
			t.Fatal(err)
		}
		var found *wire.ThreadSummary
		for i := range list.Threads {
			if list.Threads[i].ID == threadID {
				found = &list.Threads[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("未找到会话 %s", threadID)
		}
		if !found.IsArchived {
			t.Fatal("会话应已归档")
		}
		if found.Title != "已合并 · PR 审查" {
			t.Fatalf("会话标题 = %q，期望 %q", found.Title, "已合并 · PR 审查")
		}
	})

	t.Run("merge 失败时返回 merge 且不进入 sync", func(t *testing.T) {
		c, mainRoot, _, wtPath, threadID := setupPRFlowEnv(t, 3, "feat-3")
		base := branchGit(t, mainRoot, "branch", "--show-current")

		origDetail := gitPRDetailFunc
		origMerge := gitPRMergeFunc
		t.Cleanup(func() {
			gitPRDetailFunc = origDetail
			gitPRMergeFunc = origMerge
		})

		gitPRDetailFunc = func(_ *Client, _ context.Context, _ string, number int) (git.PRDetail, error) {
			return git.PRDetail{PRSummary: git.PRSummary{Number: number, BaseBranch: base}}, nil
		}
		mergeErr := errors.New("gh merge failed")
		gitPRMergeFunc = func(_ *Client, _ context.Context, _ string, _ int, _ string) error {
			return mergeErr
		}

		var steps []string
		failedStep, err := c.GitPRMergeFlow(ctx, threadID, 3, "squash", "", func(step string) {
			steps = append(steps, step)
		})
		if failedStep != "merge" {
			t.Fatalf("failedStep = %q，期望 %q", failedStep, "merge")
		}
		if !errors.Is(err, mergeErr) {
			t.Fatalf("err = %v，期望 %v", err, mergeErr)
		}
		if !reflect.DeepEqual(steps, []string{"push", "merge"}) {
			t.Fatalf("steps = %v，期望 [push merge]", steps)
		}
		if _, err := os.Stat(wtPath); err != nil {
			t.Fatalf("merge 失败后应保留 worktree 现场: %v", err)
		}
	})
}
