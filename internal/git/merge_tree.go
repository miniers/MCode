package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// MergeTreeResult 描述 git merge-tree 三方合并树的操作结果。
type MergeTreeResult struct {
	Tree      string
	Conflicts []string
}

// CommitIndexTree 在 root 下基于当前暂存区（index）生成 tree，并创建一个父提交为 parent 的 commit 对象，返回 commit sha。
// 调用方需事先将变更添加至暂存区；该方法仅读取当前 index，不更新任何引用、HEAD 或工作区。
// 身份环境变量沿用 mcode 统一配置。
func (r *Runner) CommitIndexTree(ctx context.Context, root, parent, message string) (string, error) {
	env := []string{
		"GIT_AUTHOR_NAME=mcode",
		"GIT_AUTHOR_EMAIL=mcode@localhost",
		"GIT_COMMITTER_NAME=mcode",
		"GIT_COMMITTER_EMAIL=mcode@localhost",
	}

	treeOut, err := r.run(ctx, root, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("commit index tree: write-tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOut))
	if tree == "" {
		return "", fmt.Errorf("commit index tree: write-tree returned empty tree sha")
	}

	if message == "" {
		message = "mcode commit"
	}

	commitArgs := []string{"commit-tree", tree}
	if parent != "" {
		commitArgs = append(commitArgs, "-p", parent)
	}
	commitArgs = append(commitArgs, "-m", message)

	commitOut, err := r.run(ctx, root, env, commitArgs...)
	if err != nil {
		return "", fmt.Errorf("commit index tree: commit-tree: %w", err)
	}
	commitSha := strings.TrimSpace(string(commitOut))
	if commitSha == "" {
		return "", fmt.Errorf("commit index tree: commit-tree returned empty commit sha")
	}
	return commitSha, nil
}

// MergeTreeOnto 在对象层执行 git merge-tree 三方合并，将变更 change 合并至基准 onto（共同祖先为 base）。
// 该方法完全在 git 对象库中进行计算，不触碰工作区、暂存区及任何引用。
// - 退出码 0：无冲突合并成功，Tree 为合并后的树 OID，Conflicts 为空切片；
// - 退出码 1：存在冲突，Tree 为包含冲突阶段的树 OID，Conflicts 为去重后的冲突文件路径列表；
// - 其他非零退出码或执行异常：原样包装错误并返回。
func (r *Runner) MergeTreeOnto(ctx context.Context, root, base, onto, change string) (MergeTreeResult, error) {
	args := []string{
		"merge-tree",
		"--write-tree",
		"--name-only",
		"--no-messages",
		"--merge-base=" + base,
		onto,
		change,
	}

	out, runErr := r.run(ctx, root, nil, args...)
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
			return MergeTreeResult{}, runErr
		}
	}

	outStr := strings.ReplaceAll(string(out), "\r\n", "\n")
	lines := strings.Split(outStr, "\n")
	if len(lines) == 0 {
		if runErr != nil {
			return MergeTreeResult{}, runErr
		}
		return MergeTreeResult{}, fmt.Errorf("git merge-tree returned empty output")
	}

	tree := strings.TrimSpace(lines[0])
	if tree == "" {
		if runErr != nil {
			return MergeTreeResult{}, runErr
		}
		return MergeTreeResult{}, fmt.Errorf("git merge-tree returned empty tree oid")
	}

	conflicts := make([]string, 0)
	if runErr != nil {
		seen := make(map[string]struct{})
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; !ok {
				seen[trimmed] = struct{}{}
				conflicts = append(conflicts, trimmed)
			}
		}
	}

	return MergeTreeResult{
		Tree:      tree,
		Conflicts: conflicts,
	}, nil
}

// DiffTreesBinary 生成两个树或提交（from 与 to）之间的完整二进制差异补丁。
// from 和 to 可以是 commit 或 tree 对象哈希。
// 生成的补丁包含完整索引信息（--full-index），可直接供 ApplyPatch3Way 应用至工作区。
func (r *Runner) DiffTreesBinary(ctx context.Context, root, from, to string) ([]byte, error) {
	return r.run(ctx, root, nil, "diff", "--binary", "--full-index", from, to)
}
