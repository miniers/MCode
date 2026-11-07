package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// gitMenuUpstream 返回分支的上游 remote、远程分支名与 status 用的 ref（<remote>/<branch>）；
// 分支没有配置上游时 ok 为 false。
func (r *Runner) gitMenuUpstream(ctx context.Context, root, branch string) (remote, remoteBranch, ref string, ok bool, err error) {
	if branch == "" {
		return "", "", "", false, nil
	}
	out, err := r.run(ctx, root, nil, "for-each-ref", "--format=%(upstream:remotename)%00%(upstream:remoteref)", "refs/heads/"+branch)
	if err != nil {
		return "", "", "", false, err
	}
	remote, remoteRef, found := strings.Cut(strings.TrimSpace(string(out)), "\x00")
	remoteBranch = strings.TrimPrefix(strings.TrimSpace(remoteRef), "refs/heads/")
	if !found || remote == "" || remoteBranch == "" {
		return "", "", "", false, nil
	}
	return remote, remoteBranch, remote + "/" + remoteBranch, true, nil
}

// HasUpstream 报告当前分支是否配置了上游；
// detached HEAD 或分支没有配置上游时返回 false。
func (r *Runner) HasUpstream(ctx context.Context, root string) (bool, error) {
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return false, err
	}
	_, _, _, ok, err := r.gitMenuUpstream(ctx, root, branch)
	return ok, err
}

// AheadBehind 返回当前分支相对上游的领先（Ahead）与落后（Behind）提交数；
// detached HEAD、没有上游或上游引用尚不存在时返回 (0, 0, nil)。
func (r *Runner) AheadBehind(ctx context.Context, root string) (int, int, error) {
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return 0, 0, err
	}
	_, _, ref, ok, err := r.gitMenuUpstream(ctx, root, branch)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, nil
	}
	// 上游已被删除或尚未 fetch 过时 ref 不存在，按 0/0 处理
	if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref); err != nil {
		return 0, 0, nil
	}
	out, err := r.run(ctx, root, nil, "rev-list", "--left-right", "--count", "refs/remotes/"+ref+"...HEAD")
	if err != nil {
		return 0, 0, err
	}
	var behind, ahead int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &behind, &ahead); err != nil {
		return 0, 0, fmt.Errorf("parse rev-list count %q: %w", strings.TrimSpace(string(out)), err)
	}
	return ahead, behind, nil
}

// Fetch 抓取 origin 的全部远程分支与标签，并清理已删除的远程跟踪分支。
func (r *Runner) Fetch(ctx context.Context, root string) error {
	_, err := r.run(ctx, root, nil, "fetch", "--prune", "--tags", "origin")
	return err
}

// PullCurrentFastForward 先 fetch origin 的当前分支，再执行 git merge --ff-only <remote>/<branch>；
// detached HEAD、没有上游或不能快进时返回错误，工作区不做任何改动。
func (r *Runner) PullCurrentFastForward(ctx context.Context, root string) error {
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return err
	}
	if branch == "" {
		return errors.New("cannot pull in detached HEAD state")
	}
	remote, remoteBranch, ref, ok, err := r.gitMenuUpstream(ctx, root, branch)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("分支 %s 没有配置上游，无法拉取", branch)
	}
	if _, err := r.run(ctx, root, nil, "fetch", "--", remote, "refs/heads/"+remoteBranch+":refs/remotes/"+ref); err != nil {
		return fmt.Errorf("fetch %s: %w", ref, err)
	}
	if _, err := r.run(ctx, root, nil, "merge", "--ff-only", ref); err != nil {
		return fmt.Errorf("fast-forward merge %s: %w", ref, err)
	}
	return nil
}

// FastForwardInto 把当前分支快进到 target（target 即被合并进来的分支，可以是远程跟踪分支）；
// 不是当前分支的严格后继时返回错误。
func (r *Runner) FastForwardInto(ctx context.Context, root, target string) error {
	target = strings.TrimSpace(target)
	if err := validBranchArg(target); err != nil {
		return err
	}
	cur, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return err
	}
	if cur == "" {
		return errors.New("cannot merge in detached HEAD state")
	}
	if target == cur {
		return nil
	}
	if _, err := r.run(ctx, root, nil, "merge", "--ff-only", target); err != nil {
		return fmt.Errorf("fast-forward %s into %s: %w", target, cur, err)
	}
	return nil
}

// MergeFrom 把 branch 合并进当前分支（非快进时生成合并提交）。
// 合并前要求工作区没有未跟踪文件覆盖、也没有未解决的冲突，避免遗留冲突标记：
// 校验失败时返回 ErrDirtyWorktree，工作区不做任何改动。
func (r *Runner) MergeFrom(ctx context.Context, root, branch string) error {
	branch = strings.TrimSpace(branch)
	if err := validBranchArg(branch); err != nil {
		return err
	}
	cur, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return err
	}
	if cur == "" {
		return errors.New("cannot merge in detached HEAD state")
	}
	if branch == cur {
		return nil
	}
	entries, err := r.Status(ctx, root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Index == 'U' || e.Worktree == 'U' || (e.Index == 'A' && e.Worktree == 'A') || (e.Index == 'D' && e.Worktree == 'D') {
			return errors.New("工作区存在未解决的冲突，请先处理")
		}
	}
	if _, err := r.run(ctx, root, nil, "merge", "--no-ff", "--no-edit", "--", branch); err != nil {
		return fmt.Errorf("merge %s: %w", branch, err)
	}
	return nil
}

// HasStash 报告仓库是否至少有一条贮藏记录。
func (r *Runner) HasStash(ctx context.Context, root string) (bool, error) {
	out, err := r.run(ctx, root, nil, "stash", "list", "--format=%gd")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// StashPush 把工作区改动（含未跟踪文件）贮藏起来，message 为空时使用 git 默认说明。
func (r *Runner) StashPush(ctx context.Context, root, message string) error {
	args := []string{"stash", "push", "--include-untracked"}
	if message = strings.TrimSpace(message); message != "" {
		args = append(args, "-m", message)
	}
	_, err := r.run(ctx, root, nil, args...)
	return err
}

// StashPop 恢复最近一条贮藏；没有贮藏记录时返回错误。
func (r *Runner) StashPop(ctx context.Context, root string) error {
	if ok, err := r.HasStash(ctx, root); err != nil {
		return err
	} else if !ok {
		return errors.New("没有可恢复的贮藏")
	}
	_, err := r.run(ctx, root, nil, "stash", "pop")
	return err
}

// validBranchArg 拒绝空的分支名与以 '-' 开头的分支名（防止被当作命令行选项）。
func validBranchArg(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name: %q", branch)
	}
	return nil
}
