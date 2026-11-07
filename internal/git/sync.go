package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// emptyTreeSHA 是 Git 空树对象的固定哈希。
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// DefaultRemoteBranch 探测远端 <remote> 的默认分支：
// 优先 refs/remotes/<remote>/HEAD 符号引用，其次远端跟踪的 main、master，
// 最后回退到当前分支（detached HEAD 时兜底 main）。
// 注意：worktree_cleanup.go 已有 DefaultBranch(ctx, root)，此处以不同命名避免方法重名。
func (r *Runner) DefaultRemoteBranch(ctx context.Context, root, remote string) (string, error) {
	if remote == "" {
		remote = "origin"
	}
	if out, err := r.run(ctx, root, nil, "symbolic-ref", "--short", "-q", "refs/remotes/"+remote+"/HEAD"); err == nil {
		name := strings.TrimPrefix(strings.TrimSpace(string(out)), remote+"/")
		if name != "" {
			return name, nil
		}
	}
	for _, cand := range []string{"main", "master"} {
		if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+cand); err == nil {
			return cand, nil
		}
	}
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "main", nil
	}
	return branch, nil
}

// CommitPaths 通过临时 GIT_INDEX_FILE 为指定路径构建一次提交，不污染用户索引。
// 流程：read-tree HEAD（无 HEAD 时从空索引开始）→ add -A -- paths → write-tree →
// commit-tree（HEAD 为父提交）→ update-ref HEAD（旧值作为并发保护）→
// reset -- paths 让真实索引对齐新 HEAD。
// 当目标树与 HEAD 树一致（没有可提交内容）时返回空字符串与 nil。
func (r *Runner) CommitPaths(ctx context.Context, root string, paths []string, message string) (string, error) {
	paths = normalizePaths(paths)
	if len(paths) == 0 {
		return "", nil
	}

	tmpDir, err := os.MkdirTemp("", "mcode-commit-index-*")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}

	hasHead := r.hasCommits(ctx, root)
	headSHA := ""
	if hasHead {
		out, err := r.run(ctx, root, nil, "rev-parse", "HEAD")
		if err != nil {
			return "", fmt.Errorf("resolve HEAD: %w", err)
		}
		headSHA = strings.TrimSpace(string(out))
		if _, err := r.run(ctx, root, env, "read-tree", "HEAD"); err != nil {
			return "", fmt.Errorf("populate temp index: %w", err)
		}
	}

	addArgs := append([]string{"add", "-A", "--"}, paths...)
	if _, err := r.run(ctx, root, env, addArgs...); err != nil {
		return "", fmt.Errorf("stage paths: %w", err)
	}

	treeOut, err := r.run(ctx, root, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	// 树没有变化：没有可提交的内容
	if hasHead {
		headTreeOut, err := r.run(ctx, root, nil, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return "", fmt.Errorf("resolve HEAD tree: %w", err)
		}
		if tree == strings.TrimSpace(string(headTreeOut)) {
			return "", nil
		}
	} else if tree == emptyTreeSHA {
		return "", nil
	}

	commitArgs := []string{"commit-tree", tree}
	if headSHA != "" {
		commitArgs = append(commitArgs, "-p", headSHA)
	}
	commitArgs = append(commitArgs, "-m", message)
	shaOut, err := r.run(ctx, root, nil, commitArgs...)
	if err != nil {
		return "", fmt.Errorf("create commit: %w", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	if headSHA != "" {
		// 旧值作为保护：HEAD 在构建期间被移动时 update-ref 失败，避免覆盖别人的提交
		if _, err := r.run(ctx, root, nil, "update-ref", "HEAD", sha, headSHA); err != nil {
			return "", fmt.Errorf("update HEAD: %w", err)
		}
	} else {
		if _, err := r.run(ctx, root, nil, "update-ref", "HEAD", sha); err != nil {
			return "", fmt.Errorf("update HEAD: %w", err)
		}
	}

	// 真实索引跟随新 HEAD，保持这些路径在 status 中干净
	resetArgs := append([]string{"reset", "-q", "--"}, paths...)
	if _, err := r.run(ctx, root, nil, resetArgs...); err != nil {
		return "", fmt.Errorf("sync index for committed paths: %w", err)
	}
	return sha, nil
}

// PushRef 将本地引用 localRef 推送到 remote 的 remoteBranch 分支。
// 使用完整 refspec，避免受 push.default 与上游配置影响。
func (r *Runner) PushRef(ctx context.Context, root, remote, localRef, remoteBranch string) error {
	if remote == "" {
		remote = "origin"
	}
	if localRef == "" || remoteBranch == "" {
		return errors.New("push refspec is incomplete")
	}
	_, err := r.run(ctx, root, nil, "push", "--", remote, localRef+":refs/heads/"+remoteBranch)
	return err
}

// PullFastForward 拉取远端 branch 的最新提交，并在当前分支上执行 --ff-only 合并。
// 远端分支会被更新到 refs/remotes/<remote>/<branch> 再合入。
func (r *Runner) PullFastForward(ctx context.Context, root, remote, branch string) error {
	if remote == "" {
		remote = "origin"
	}
	if branch == "" {
		return errors.New("branch is empty")
	}
	ref := "refs/remotes/" + remote + "/" + branch
	if _, err := r.run(ctx, root, nil, "fetch", "--", remote, "refs/heads/"+branch+":"+ref); err != nil {
		return fmt.Errorf("fetch %s/%s: %w", remote, branch, err)
	}
	if _, err := r.run(ctx, root, nil, "merge", "--ff-only", ref); err != nil {
		return fmt.Errorf("fast-forward merge %s: %w", ref, err)
	}
	return nil
}

// SyncDefaultBranch 将本地检出对齐到远端 base 分支，不切换分支：
//  1. fetch remote base；
//  2. 当前分支不是 base 时不做任何事，返回 false；
//  3. HEAD 是远端 base 的祖先时执行 merge --ff-only，返回 true；
//  4. HEAD 与远端 base 树内容一致（例如远端 squash 合入了我们推送的提交）时，
//     执行 reset --soft 让分支指针跟上，返回 true；
//  5. 其他情况保持原样，返回 false。
func (r *Runner) SyncDefaultBranch(ctx context.Context, root, remote, base string) (bool, error) {
	if remote == "" {
		remote = "origin"
	}
	if base == "" {
		return false, errors.New("base branch is empty")
	}
	ref := "refs/remotes/" + remote + "/" + base
	if _, err := r.run(ctx, root, nil, "fetch", "--", remote, "refs/heads/"+base+":"+ref); err != nil {
		return false, fmt.Errorf("fetch %s/%s: %w", remote, base, err)
	}

	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return false, err
	}
	if branch != base {
		return false, nil
	}

	if _, err := r.run(ctx, root, nil, "merge-base", "--is-ancestor", "HEAD", ref); err == nil {
		if _, err := r.run(ctx, root, nil, "merge", "--ff-only", ref); err != nil {
			return false, fmt.Errorf("fast-forward merge %s: %w", ref, err)
		}
		return true, nil
	} else if !isExitCode(err, 1) {
		return false, fmt.Errorf("check ancestor of %s: %w", ref, err)
	}

	if _, err := r.run(ctx, root, nil, "diff", "--quiet", "HEAD", ref); err == nil {
		// 树内容一致：软重置分支指针，索引与工作区保持不变
		if _, err := r.run(ctx, root, nil, "reset", "--soft", ref); err != nil {
			return false, fmt.Errorf("soft reset to %s: %w", ref, err)
		}
		return true, nil
	} else if !isExitCode(err, 1) {
		return false, fmt.Errorf("diff with %s: %w", ref, err)
	}
	return false, nil
}

// DiffPaths 返回 HEAD 与指定路径当前工作区内容之间的统一 diff 文本，
// 供提交说明生成器使用。实现沿用临时索引模式（read-tree HEAD → add -A -- paths →
// write-tree → diff HEAD <tree>），因此未跟踪的新文件同样包含在 diff 中，
// 且全程不接触用户索引。
func (r *Runner) DiffPaths(ctx context.Context, root string, paths []string) (string, error) {
	paths = normalizePaths(paths)
	if len(paths) == 0 {
		return "", nil
	}
	if !r.hasCommits(ctx, root) {
		return "", nil
	}

	tmpDir, err := os.MkdirTemp("", "mcode-diff-paths-*")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}

	if _, err := r.run(ctx, root, env, "read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("populate temp index: %w", err)
	}
	addArgs := append([]string{"add", "-A", "--"}, paths...)
	if _, err := r.run(ctx, root, env, addArgs...); err != nil {
		return "", fmt.Errorf("stage paths for diff: %w", err)
	}
	treeOut, err := r.run(ctx, root, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write tree for diff: %w", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	out, err := r.run(ctx, root, nil, "diff", "--no-color", "--no-ext-diff", "HEAD", tree)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// isExitCode 判断错误链中的 git 退出码是否等于 code。
func isExitCode(err error, code int) bool {
	var exitErr interface{ ExitCode() int }
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

// normalizePaths 去除空路径与重复路径，保持原有顺序。
func normalizePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		normalized = append(normalized, p)
	}
	return normalized
}
