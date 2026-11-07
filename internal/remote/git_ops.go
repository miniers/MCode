package remote

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"mcode/internal/git"
	"mcode/internal/store"
)

// GitThreadWorktree 表示会话所在仓库中的单个 worktree。
type GitThreadWorktree struct {
	Path    string
	Branch  string
	Current bool // 该 worktree 根目录即会话所在仓库根目录
}

// GitThreadState 汇总会话工作目录所在仓库的分支、worktree 与改动状态。
type GitThreadState struct {
	WorkDir   string
	Branch    string // 当前分支，detached 时为空
	Detached  bool
	Head      string // HEAD 的 7 位短哈希，空仓库时为空
	Branches  []string
	Worktrees []GitThreadWorktree
	Changes   int
	// ChangedFiles 统计改动文件数（与 Changes 相同，面板标题使用）。
	ChangedFiles int
	Ahead        int  // 当前分支相对上游领先的提交数
	Behind       int  // 当前分支相对上游落后的提交数
	HasUpstream  bool // 当前分支是否配置了上游
	HasStash     bool // 仓库是否有贮藏记录
	// CanFastForward 当前分支是否落后于上游（拉取可快进）。
	CanFastForward bool
}

// gitThreadContext 返回 git 执行器与会话的工作目录。
func (b *MemoryBackend) gitThreadContext(threadID string) (*git.Runner, string, error) {
	b.mu.Lock()
	runner := b.git
	_, exists := b.threads[threadID]
	b.mu.Unlock()

	if runner == nil {
		return nil, "", errors.New("未启用 git")
	}
	if !exists {
		return nil, "", store.ErrNotFound
	}
	workDir, ok := b.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return nil, "", fmt.Errorf("会话 %s 没有工作目录", threadID)
	}
	return runner, workDir, nil
}

// gitThreadRoot 返回 git 执行器、会话工作目录与其所在仓库根目录。
func (b *MemoryBackend) gitThreadRoot(ctx context.Context, threadID string) (*git.Runner, string, string, error) {
	runner, workDir, err := b.gitThreadContext(threadID)
	if err != nil {
		return nil, "", "", err
	}
	root, err := runner.RepoRoot(ctx, workDir)
	if err != nil {
		return nil, "", "", err
	}
	return runner, workDir, root, nil
}

// GitState 读取会话工作目录所在仓库的当前分支、本地分支、worktree 列表与改动条目数。
func (b *MemoryBackend) GitState(ctx context.Context, threadID string) (GitThreadState, error) {
	runner, workDir, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return GitThreadState{}, err
	}

	branch, err := runner.CurrentBranch(ctx, root)
	if err != nil {
		return GitThreadState{}, err
	}
	state := GitThreadState{
		WorkDir:  workDir,
		Branch:   branch,
		Detached: branch == "",
	}

	// 空仓库没有 HEAD 提交，此时 Head 留空而不报错
	if head, err := runner.RevParse(ctx, root, "HEAD"); err == nil {
		if len(head) > 7 {
			head = head[:7]
		}
		state.Head = head
	}

	refs, err := runner.ListRefs(ctx, root, "refs/heads/")
	if err != nil {
		return GitThreadState{}, err
	}
	state.Branches = make([]string, 0, len(refs))
	for _, ref := range refs {
		state.Branches = append(state.Branches, strings.TrimPrefix(ref.Name, "refs/heads/"))
	}

	worktrees, err := runner.WorktreeList(ctx, root)
	if err != nil {
		return GitThreadState{}, err
	}
	state.Worktrees = make([]GitThreadWorktree, 0, len(worktrees))
	for _, wt := range worktrees {
		state.Worktrees = append(state.Worktrees, GitThreadWorktree{
			Path:    wt.Path,
			Branch:  strings.TrimPrefix(wt.Branch, "refs/heads/"),
			Current: samePath(wt.Path, root),
		})
	}

	entries, err := runner.Status(ctx, root)
	if err != nil {
		return GitThreadState{}, err
	}
	state.Changes = len(entries)
	state.ChangedFiles = state.Changes

	// 上游、领先/落后与贮藏状态均为附加信息，探测失败时保持零值，不影响状态读取
	if state.HasUpstream, err = runner.HasUpstream(ctx, root); err != nil {
		state.HasUpstream = false
	}
	if state.HasUpstream {
		if ahead, behind, err := runner.AheadBehind(ctx, root); err == nil {
			state.Ahead = ahead
			state.Behind = behind
			state.CanFastForward = behind > 0
		}
	}
	if hasStash, err := runner.HasStash(ctx, root); err == nil {
		state.HasStash = hasStash
	}
	return state, nil
}

// GitFetch 抓取会话所在仓库 origin 的全部远程分支。
func (b *MemoryBackend) GitFetch(ctx context.Context, threadID string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.Fetch(ctx, root)
}

// GitPull 快进拉取会话所在仓库当前分支的上游。
func (b *MemoryBackend) GitPull(ctx context.Context, threadID string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.PullCurrentFastForward(ctx, root)
}

// GitFastForwardInto 把会话所在仓库的当前分支快进到 branch。
func (b *MemoryBackend) GitFastForwardInto(ctx context.Context, threadID, branch string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.FastForwardInto(ctx, root, branch)
}

// GitMergeFrom 把 branch 合并进会话所在仓库的当前分支。
func (b *MemoryBackend) GitMergeFrom(ctx context.Context, threadID, branch string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.MergeFrom(ctx, root, branch)
}

// GitStash 把会话所在仓库的工作区改动贮藏起来。
func (b *MemoryBackend) GitStash(ctx context.Context, threadID string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashPush(ctx, root, "")
}

// GitStashPop 恢复会话所在仓库最近一条贮藏。
func (b *MemoryBackend) GitStashPop(ctx context.Context, threadID string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashPop(ctx, root)
}

// GitCheckout 在会话所在仓库切换到指定分支。
func (b *MemoryBackend) GitCheckout(ctx context.Context, threadID, branch string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return store.ErrNotFound
	}
	if td.summary.IsRunning || td.runTurnID != "" || td.runCancel != nil {
		return errors.New("会话运行中，无法切换分支")
	}
	return runner.Checkout(ctx, root, branch)
}

// GitCommit 暂存会话所在仓库的全部改动并提交。
func (b *MemoryBackend) GitCommit(ctx context.Context, threadID, message string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.Commit(ctx, root, message)
}

// GitChangedFiles 列出会话所在仓库工作区里的改动文件，供提交时逐个勾选。
func (b *MemoryBackend) GitChangedFiles(ctx context.Context, threadID string) ([]git.ChangedFile, error) {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.ChangedFiles(ctx, root)
}

// GitCommitSelected 只提交会话所在仓库里 paths 指定的改动。
func (b *MemoryBackend) GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.CommitSelected(ctx, root, paths, message)
}

// GitPush 推送会话所在仓库的当前分支，无上游时推到 origin 并设置上游。
func (b *MemoryBackend) GitPush(ctx context.Context, threadID string) error {
	runner, _, root, err := b.gitThreadRoot(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.Push(ctx, root)
}

// GitSwitchWorktree 校验 path 属于会话所在仓库的 worktree 后，把会话工作目录切换过去。
func (b *MemoryBackend) GitSwitchWorktree(ctx context.Context, threadID, path string) error {
	return b.setThreadWorkDir(ctx, threadID, path)
}

// SetThreadWorkDir 把会话工作目录设为 dir；dir 必须出现在当前工作目录所在仓库的 worktree 列表中。
// 切换结果会持久化到 store（worktree_path 与 worktree_requested 一并落库并清空旧 session），重启后仍在新目录；
// 切回项目根目录时清空 worktree_path，重启后回到项目根。
func (b *MemoryBackend) SetThreadWorkDir(threadID, dir string) error {
	return b.setThreadWorkDir(context.Background(), threadID, dir)
}

func (b *MemoryBackend) setThreadWorkDir(ctx context.Context, threadID, dir string) error {
	runner, workDir, err := b.gitThreadContext(threadID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return errors.New("worktree 路径为空")
	}
	dir = filepath.Clean(dir)

	worktrees, err := runner.WorktreeList(ctx, workDir)
	if err != nil {
		return err
	}
	found := false
	for _, wt := range worktrees {
		if samePath(wt.Path, dir) {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s 不是当前仓库的 worktree", dir)
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if td.summary.IsRunning || td.runTurnID != "" || td.runCancel != nil {
		b.mu.Unlock()
		return errors.New("会话运行中，无法切换 worktree")
	}
	// 路径没变时不动目录、不清 session，否则切到同一 worktree 会把 provider 会话无谓丢掉。
	// 用会话的当前实际工作目录比较：workDir 为空时等于项目根目录。
	curDir, _ := b.threadWorkDirLocked(threadID)
	if curDir != "" && samePath(curDir, dir) {
		b.mu.Unlock()
		return nil
	}

	// 切回项目根目录时把 worktree_path 存成空串，与 threadWorkDirLocked 的回退口径一致。
	storePath, requested := dir, true
	if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" && samePath(proj.Path, dir) {
		storePath, requested = "", false
	}

	td.workDir = dir
	td.summary.WorktreeRequested = requested
	// store 会清空 session_id，内存必须同步丢掉旧 session，否则旧 provider 会话会在新目录下被续接。
	td.sessionID = ""
	td.sessionEpoch++
	td.continuationSent = false
	td.needsHandoff = true
	b.mu.Unlock()

	// persistThreadWorkDir 自己会加 b.mu，必须在释放锁之后调用。
	b.persistThreadWorkDir(threadID, storePath, requested)
	return nil
}

// samePath 比较两个路径是否指向同一位置：先比较 filepath.Clean 结果，
// 不同时再比较解析符号链接后的结果（例如 macOS 下 /var 与 /private/var）。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
