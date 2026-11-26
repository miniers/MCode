package git

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var prBranchPattern = regexp.MustCompile(`^mcode/pr-(\d+)$`)

// PRThreadState 描述 PR 审阅会话所在 worktree 的本地分支状态。
type PRThreadState struct {
	Number       int
	CrossRepo    bool
	Dirty        bool
	LocalCommits int
}

// PRThreadState 检查 root 当前所在分支是否为 PR 审阅分支 mcode/pr-<N>，
// 并返回是否跨仓库（无上游）、工作区是否有未提交改动以及本地新增提交数。
func (r *Runner) PRThreadState(ctx context.Context, root string) (PRThreadState, bool, error) {
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return PRThreadState{}, false, err
	}
	matches := prBranchPattern.FindStringSubmatch(branch)
	if len(matches) != 2 {
		return PRThreadState{}, false, nil
	}
	number, err := strconv.Atoi(matches[1])
	if err != nil || number <= 0 {
		return PRThreadState{}, false, nil
	}

	_, _, _, hasUpstream, err := r.gitMenuUpstream(ctx, root, branch)
	if err != nil {
		return PRThreadState{}, false, err
	}
	crossRepo := !hasUpstream

	statusOut, err := r.run(ctx, root, nil, "status", "--porcelain")
	if err != nil {
		return PRThreadState{}, false, err
	}
	dirty := len(strings.TrimSpace(string(statusOut))) > 0

	revRange := "@{u}..HEAD"
	if crossRepo {
		revRange = PRHeadRef(number) + "..HEAD"
	}
	countOut, err := r.run(ctx, root, nil, "rev-list", "--count", revRange)
	if err != nil {
		return PRThreadState{}, false, err
	}
	localCommits, err := strconv.Atoi(strings.TrimSpace(string(countOut)))
	if err != nil {
		return PRThreadState{}, false, fmt.Errorf("解析本地提交数 %q: %w", strings.TrimSpace(string(countOut)), err)
	}

	return PRThreadState{
		Number:       number,
		CrossRepo:    crossRepo,
		Dirty:        dirty,
		LocalCommits: localCommits,
	}, true, nil
}

// PRRebaseOntoBase 在 fork PR 合并后将本地新增提交变基到 origin/<base> 并推回远端 base。
// rebase 失败时自动执行 rebase --abort 恢复工作区并返回含“冲突”字样的错误。
func (r *Runner) PRRebaseOntoBase(ctx context.Context, root string, number int, base string) error {
	if number <= 0 {
		return fmt.Errorf("无效的 PR 编号: %d", number)
	}
	base = strings.TrimSpace(base)
	if err := r.validatePRBranch(ctx, root, base); err != nil {
		return err
	}
	if _, err := r.run(ctx, root, nil, "fetch", "origin", "+refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
		return fmt.Errorf("fetch %s: %w", base, err)
	}
	// 为了 sync 重试幂等：origin/<base> 已是 HEAD 祖先时跳过 rebase。
	alreadyRebased, err := r.IsBranchMerged(ctx, root, "origin/"+base, "HEAD")
	if err != nil {
		return err
	}
	if !alreadyRebased {
		if _, err := r.run(ctx, root, opEnv, "rebase", "--onto", "origin/"+base, PRHeadRef(number)); err != nil {
			abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_, _ = r.run(abortCtx, root, opEnv, "rebase", "--abort")
			return fmt.Errorf("rebase 到 origin/%s 冲突: %w", base, err)
		}
	}
	return r.PushRef(ctx, root, "origin", "HEAD", base)
}

// FastForwardLocalBranch 将主仓库的本地 base 分支同步到 origin/<base>。
// base 检出在主仓库时要求工作区干净：HEAD 是 origin/<base> 祖先时直接快进，
// 否则将本地提交变基到 origin/<base>（冲突时中止变基并返回错误）；
// 检出在其他 worktree 时返回含路径的错误；
// 未被检出时：若本地已包含 origin/<base> 则直接返回，若本地是 origin/<base> 祖先则通过 fetch 快进引用，分叉时返回错误。
func (r *Runner) FastForwardLocalBranch(ctx context.Context, mainRoot, base string) error {
	base = strings.TrimSpace(base)
	if err := r.validatePRBranch(ctx, mainRoot, base); err != nil {
		return err
	}
	if _, err := r.run(ctx, mainRoot, nil, "fetch", "origin", "+refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
		return fmt.Errorf("fetch %s: %w", base, err)
	}

	worktrees, err := r.WorktreeList(ctx, mainRoot)
	if err != nil {
		return err
	}
	resolvedMain := resolvePath(mainRoot)
	checkedOutInMain := false
	for _, wt := range worktrees {
		if wt.Prunable || wt.Branch != base {
			continue
		}
		if resolvePath(wt.Path) == resolvedMain {
			checkedOutInMain = true
			continue
		}
		return fmt.Errorf("分支 %s 正检出在 worktree %s，无法快进", base, wt.Path)
	}

	remoteRef := "origin/" + base
	if checkedOutInMain {
		out, err := r.run(ctx, mainRoot, nil, "status", "--porcelain")
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(out))) > 0 {
			return fmt.Errorf("主仓库工作区有未提交改动，无法快进分支 %s", base)
		}
		isAncestor, err := r.IsBranchMerged(ctx, mainRoot, "HEAD", remoteRef)
		if err != nil {
			return err
		}
		if isAncestor {
			return r.FastForwardInto(ctx, mainRoot, remoteRef)
		}
		if _, err := r.run(ctx, mainRoot, opEnv, "rebase", remoteRef); err != nil {
			abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_, _ = r.run(abortCtx, mainRoot, opEnv, "rebase", "--abort")
			return fmt.Errorf("本地 %s 有未推送的提交，变基到 origin/%s 冲突: %w", base, base, err)
		}
		return nil
	}

	localRef := "refs/heads/" + base
	remoteInLocal, err := r.IsBranchMerged(ctx, mainRoot, remoteRef, localRef)
	if err != nil {
		return err
	}
	if remoteInLocal {
		return nil
	}
	localInRemote, err := r.IsBranchMerged(ctx, mainRoot, localRef, remoteRef)
	if err != nil {
		return err
	}
	if localInRemote {
		_, err = r.run(ctx, mainRoot, nil, "fetch", "origin", localRef+":"+localRef)
		return err
	}
	return fmt.Errorf("本地分支 %s 有未推送的提交且与 origin/%s 分叉，请检出后变基", base, base)
}
