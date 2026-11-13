// 此文件包含 Client 的Git 与工作树方法。
package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
	"mcode/internal/textgen"
	mcodeui "mcode/internal/ui"
)

// GitState 调用 backend.GitState 读取会话所在仓库的分支、worktree 与改动状态。
func (c *Client) GitState(ctx context.Context, threadID string) (mcodeui.GitState, error) {
	if c.backend == nil {
		return mcodeui.GitState{}, errors.New("backend 不可用")
	}
	st, err := c.backend.GitState(ctx, threadID)
	if err != nil {
		return mcodeui.GitState{}, err
	}
	workDir := filepath.Clean(st.WorkDir)
	worktrees := make([]mcodeui.GitWorktree, len(st.Worktrees))
	for i, wt := range st.Worktrees {
		worktrees[i] = mcodeui.GitWorktree{
			Path:    wt.Path,
			Branch:  strings.TrimPrefix(wt.Branch, "refs/heads/"),
			Current: wt.Current || filepath.Clean(wt.Path) == workDir,
		}
	}
	var remoteURL string
	if rURL, err := c.gitRunner().RemoteURL(ctx, st.WorkDir); err == nil {
		remoteURL = rURL
	}
	return mcodeui.GitState{
		WorkDir:   st.WorkDir,
		Branch:    st.Branch,
		Detached:  st.Detached,
		Head:      st.Head,
		Branches:  st.Branches,
		Worktrees: worktrees,
		Changes:   st.Changes,
		RemoteURL: remoteURL,

		ChangedFiles:   st.ChangedFiles,
		Ahead:          st.Ahead,
		Behind:         st.Behind,
		HasUpstream:    st.HasUpstream,
		HasStash:       st.HasStash,
		CanFastForward: st.CanFastForward,
	}, nil
}

// GitCheckout 调用 backend.GitCheckout 切换会话所在仓库的分支。
func (c *Client) GitCheckout(ctx context.Context, threadID, branch string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitCheckout(ctx, threadID, branch)
}

// GitSwitchWorktree 调用 backend.GitSwitchWorktree 把会话工作目录切换到指定 worktree。
func (c *Client) GitSwitchWorktree(ctx context.Context, threadID, path string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitSwitchWorktree(ctx, threadID, path)
}

// GitCommit 调用 backend.GitCommit 暂存全部改动并提交。
func (c *Client) GitCommit(ctx context.Context, threadID, message string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitCommit(ctx, threadID, message)
}

// GitChangedFiles 调用 backend.GitChangedFiles 列出会话工作目录的改动文件。
func (c *Client) GitChangedFiles(ctx context.Context, threadID string) ([]mcodeui.GitChangedFile, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	files, err := c.backend.GitChangedFiles(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]mcodeui.GitChangedFile, len(files))
	for i, file := range files {
		out[i] = mcodeui.GitChangedFile{Path: file.Path, OrigPath: file.OrigPath, Status: file.Status}
	}
	return out, nil
}

// GitCommitSelected 调用 backend.GitCommitSelected 只提交勾选的路径。
func (c *Client) GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitCommitSelected(ctx, threadID, paths, message)
}

// GitPush 调用 backend.GitPush 推送当前分支。
func (c *Client) GitPush(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitPush(ctx, threadID)
}

// GitCreateBranch 在会话工作目录创建并切换到新分支。
func (c *Client) GitCreateBranch(ctx context.Context, threadID, name string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return fmt.Errorf("会话 %s 没有工作目录", threadID)
	}
	return c.gitRunner().CreateBranch(ctx, workDir, name)
}

// GitFastForwardInto 把会话所在仓库的当前分支快进到 branch。
func (c *Client) GitFastForwardInto(ctx context.Context, threadID, branch string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitFastForwardInto(ctx, threadID, branch)
}

// GitMergeFrom 把 branch 合并进会话所在仓库的当前分支。
func (c *Client) GitMergeFrom(ctx context.Context, threadID, branch string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitMergeFrom(ctx, threadID, branch)
}

// GitFetch 调用 backend.GitFetch 抓取远程分支。
func (c *Client) GitFetch(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitFetch(ctx, threadID)
}

// GitPull 调用 backend.GitPull 快进拉取当前分支。
func (c *Client) GitPull(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitPull(ctx, threadID)
}

// GitStash 调用 backend.GitStash 贮藏当前工作区改动。
func (c *Client) GitStash(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitStash(ctx, threadID)
}

// GitStashPop 调用 backend.GitStashPop 恢复最近一条贮藏。
func (c *Client) GitStashPop(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.GitStashPop(ctx, threadID)
}

// GenerateCommitMessage 为指定会话的工作区未提交改动生成提交信息。
func (c *Client) GenerateCommitMessage(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}

	cfg, ok := c.loadConfig()
	if !ok {
		cfg = config.Defaults()
	}
	if !cfg.SourceControl.GenerateCommitMessageEnabled() {
		return "", errors.New("未启用")
	}

	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return "", fmt.Errorf("会话 %s 没有工作目录", threadID)
	}

	diffStat := runGitDiffForCommit(ctx, workDir, "--stat")
	diffPatch := runGitDiffForCommit(ctx, workDir)

	if strings.TrimSpace(diffStat) == "" && strings.TrimSpace(diffPatch) == "" {
		return "", errors.New("没有检测到未提交的代码改动")
	}

	// 与会话标题自动生成共用同一套模型选择。
	provider, model, effort := textgenModelSelection(cfg)

	prompt := textgen.CommitMessagePrompt(diffStat, diffPatch, cfg.SourceControl.CommitInstructions)
	ctx = withThreadTextgenNotice(ctx, c.backend, threadID)
	ctx = withTextgenGlobalSelection(ctx, cfg)
	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)
	if err != nil {
		return "", err
	}

	cleaned := textgen.CleanCommitMessage(out)
	if cleaned == "" {
		return "", errors.New("生成提交信息为空")
	}
	return cleaned, nil
}

// GeneratePRDescription 根据分支提交或未提交的改动生成 PR 描述。
func (c *Client) GeneratePRDescription(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	cfg, ok := c.loadConfig()
	if !ok {
		cfg = config.Defaults()
	}
	if !cfg.SourceControl.GenerateCommitMessageEnabled() {
		return "", errors.New("未启用")
	}
	workDir, ok := c.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return "", fmt.Errorf("会话 %s 没有工作目录", threadID)
	}
	commits, stat, patch, err := gitPRChanges(ctx, workDir)
	if err != nil {
		return "", err
	}
	provider, model, effort := textgenModelSelection(cfg)
	prompt := textgen.PRDescriptionPrompt(commits, stat, patch, cfg.SourceControl.CommitInstructions)
	ctx = withThreadTextgenNotice(ctx, c.backend, threadID)
	ctx = withTextgenGlobalSelection(ctx, cfg)
	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)
	if err != nil {
		return "", err
	}
	cleaned := textgen.CleanCommitMessage(out)
	if cleaned == "" {
		return "", errors.New("生成 PR 描述为空")
	}
	return cleaned, nil
}

// MoveToWorktree 将指定会话迁移至独立的 Git worktree 分支。
func (c *Client) MoveToWorktree(ctx context.Context, threadID, branch string, transferFiles bool) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.WorktreeMoveExecute(ctx, wire.ThreadsWorktreeExecuteParams{
		ThreadID:      threadID,
		Branch:        branch,
		TransferFiles: transferFiles,
	})
	return err
}

// ScanWorktrees 扫描并返回所有 Git worktree 列表。
func (c *Client) ScanWorktrees(ctx context.Context) ([]mcodeui.WorktreeInfo, error) {
	roots, err := c.worktreeRepoRoots(ctx)
	if err != nil {
		return nil, err
	}
	inUse := c.worktreeInUseFunc()
	var out []mcodeui.WorktreeInfo
	for _, root := range roots {
		// ctx 已取消或超时：不再对剩余仓库发起扫描，直接返回已收集的结果。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		items, err := c.gitRunner().ScanWorktrees(ctx, root)
		if err != nil {
			continue
		}
		for _, it := range items {
			out = append(out, mcodeui.WorktreeInfo{
				Path:        it.Path,
				Branch:      it.Branch,
				ProjectPath: it.RepoRoot,
				Merged:      it.Merged,
				Active:      inUse(it.Path),
				AgeDays:     int(it.Age / (24 * time.Hour)),
				SizeBytes:   it.SizeBytes,
				Clean:       it.Clean,
				Orphan:      it.Orphan,
			})
		}
	}
	return out, nil
}

// worktreeRepoRoots 返回全部项目对应的去重 git 仓库根目录。
func (c *Client) worktreeRepoRoots(ctx context.Context) ([]string, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	res, err := c.backend.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var roots []string
	for _, p := range res.Projects {
		root, err := c.gitRunner().RepoRoot(ctx, p.Path)
		if err != nil || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots, nil
}

// worktreeInUseFunc 合并会话与 Swarm Bee 正在使用的 worktree 路径，返回判定函数。
func (c *Client) worktreeInUseFunc() func(string) bool {
	return func(path string) bool {
		paths := c.backend.WorktreePathsInUse()
		if c.swarm != nil {
			paths = append(paths, c.swarm.ActiveWorktreePaths()...)
		}
		clean := filepath.Clean(path)
		for _, p := range paths {
			if filepath.Clean(p) == clean {
				return true
			}
			if real, err := filepath.EvalSymlinks(p); err == nil && real == clean {
				return true
			}
		}
		return false
	}
}

// CleanSafeWorktrees 清理已合并且安全的空闲 Git worktree。
func (c *Client) CleanSafeWorktrees(ctx context.Context) (int, error) {
	if c.backend == nil {
		return 0, errors.New("backend 不可用")
	}
	roots, err := c.worktreeRepoRoots(ctx)
	if err != nil {
		return 0, err
	}
	guard := git.CleanupGuard{InUse: c.worktreeInUseFunc()}
	total := 0
	var firstErr error
	for _, root := range roots {
		removed, err := c.gitRunner().CleanSafeWorktrees(ctx, root, guard)
		total += len(removed)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return total, firstErr
}

// TriggerAutoMerge 手动触发对指定 lead 会话下所有已合入 bee 的自动合并。
// 手动路径按设计始终推送，不受 AutoMerge/AutoPush 开关限制。
func (c *Client) TriggerAutoMerge(ctx context.Context, leadThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	p := c.swarmPipeline
	c.kanbanMu.Unlock()
	if m == nil || p == nil {
		return errors.New("编排自动合并流水线不可用")
	}

	dir, _ := c.ThreadWorkDir(leadThreadID)
	projectID := ""
	if c.backend != nil {
		if thread, err := c.backend.GetThread(ctx, leadThreadID); err == nil {
			projectID = thread.ProjectID
		}
	}
	batches := swarm.BatchesFromState(m.State(leadThreadID), dir, projectID)
	if len(batches) == 0 {
		return errors.New("没有可合并的子代理改动")
	}
	return p.Trigger(ctx, batches)
}
