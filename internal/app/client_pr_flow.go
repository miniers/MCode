package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

const mergedThreadTitlePrefix = "已合并 · "

var (
	gitPRDetailFunc = (*Client).GitPRDetail
	gitPRMergeFunc  = (*Client).GitPRMerge
)

var prMergeFlowSteps = []string{"push", "merge", "sync", "cleanup"}

// GitPRThreadState 返回指定会话所在 worktree 的 PR 分支状态；
// 不处于 PR 审阅分支 mcode/pr-<N> 时返回 ok=false。
func (c *Client) GitPRThreadState(ctx context.Context, threadID string) (git.PRThreadState, bool, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return git.PRThreadState{}, false, err
	}
	return runner.PRThreadState(ctx, root)
}

// GitPRMergeFlow 执行 PR 审阅会话的一键合并四步流程：
// "push" -> "merge" -> "sync" -> "cleanup"。
// from 非空时跳过 from 之前的步骤以便从失败步骤重试；
// 每步开始前回调 onStep（可为 nil），任一步失败立即返回 (该步名, err)。
func (c *Client) GitPRMergeFlow(ctx context.Context, threadID string, number int, method, from string, onStep func(step string)) (failedStep string, err error) {
	if number <= 0 {
		return "", fmt.Errorf("无效的 PR 编号: %d", number)
	}
	startIdx, ok := prMergeStepIndex(from)
	if !ok {
		return "", fmt.Errorf("无效的起始步骤: %q", from)
	}

	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	state, isPR, err := runner.PRThreadState(ctx, root)
	if err != nil {
		return "", err
	}
	if !isPR || state.Number != number {
		return "", fmt.Errorf("当前会话不是 PR #%d 的审阅会话", number)
	}
	if state.Dirty {
		return "push", errors.New("有未提交的改动，先让 agent 提交")
	}

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	detail, err := gitPRDetailFunc(c, callCtx, threadID, number)
	if err != nil {
		return "", err
	}
	baseBranch := strings.TrimSpace(detail.BaseBranch)
	if baseBranch == "" {
		return "", errors.New("PR base 分支为空")
	}

	mainRoot, err := runner.MainRepoRoot(callCtx, root)
	if err != nil {
		return "", err
	}

	notify := func(step string) {
		if onStep != nil {
			onStep(step)
		}
	}

	if startIdx <= 0 {
		notify("push")
		if !state.CrossRepo && state.LocalCommits > 0 {
			if err := runner.Push(callCtx, root); err != nil {
				return "push", err
			}
		}
	}

	if startIdx <= 1 {
		notify("merge")
		if err := gitPRMergeFunc(c, callCtx, threadID, number, method); err != nil {
			return "merge", err
		}
	}

	if startIdx <= 2 {
		notify("sync")
		if state.CrossRepo && state.LocalCommits > 0 {
			if err := runner.PRRebaseOntoBase(callCtx, root, number, baseBranch); err != nil {
				return "sync", err
			}
		}
		if err := runner.FastForwardLocalBranch(callCtx, mainRoot, baseBranch); err != nil {
			return "sync", err
		}
	}

	if startIdx <= 3 {
		notify("cleanup")
		curState, curOK, err := runner.PRThreadState(callCtx, root)
		if err != nil {
			return "cleanup", err
		}
		if !curOK || curState.Number != number {
			return "cleanup", fmt.Errorf("当前会话不是 PR #%d 的审阅会话", number)
		}
		if curState.Dirty {
			return "cleanup", errors.New("有未提交的改动，无法清理 worktree")
		}

		wtPath := root
		if samePRPath(root, mainRoot) {
			dataDir := c.effectiveDataDir()
			if dataDir == "" {
				return "cleanup", errors.New("数据目录未初始化，无法清理 Worktree")
			}
			wtPath = prWorktreePath(dataDir, mainRoot, number)
		}
		if _, err := c.cleanupPRWorktreeThreads(callCtx, runner, mainRoot, wtPath, number, true); err != nil {
			return "cleanup", err
		}
		if err := c.archiveMergedPRThread(callCtx, threadID); err != nil {
			return "cleanup", err
		}
	}

	return "", nil
}

func prMergeStepIndex(from string) (int, bool) {
	if from == "" {
		return 0, true
	}
	for i, step := range prMergeFlowSteps {
		if step == from {
			return i, true
		}
	}
	return 0, false
}

func samePRPath(a, b string) bool {
	clean := func(p string) string {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return filepath.Clean(p)
	}
	return clean(a) == clean(b)
}

func (c *Client) archiveMergedPRThread(ctx context.Context, threadID string) error {
	if err := c.SetThreadArchived(threadID, true); err != nil {
		return err
	}
	includeArchived := true
	threads, err := c.backend.ListThreads(ctx, wire.ThreadListParams{IncludeArchived: &includeArchived})
	if err != nil {
		return err
	}
	for _, th := range threads.Threads {
		if th.ID != threadID {
			continue
		}
		if !strings.HasPrefix(th.Title, mergedThreadTitlePrefix) {
			return c.RenameThread(threadID, mergedThreadTitlePrefix+th.Title)
		}
		return nil
	}
	return nil
}
