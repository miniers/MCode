package git

import (
	"context"
	"fmt"
	"strings"
)

// IsManagedWorktreePath 报告 path 是否位于应用自管的 worktree 基目录
// （<DataDir>/worktrees、<DataDir>/swarm-worktrees、旧版 <DataDir>/hydra-worktrees）之下。
// 这些 worktree 由 mcode 为会话、看板任务或编排子代理（head）创建，不算「AI 自建」。
func IsManagedWorktreePath(path string) bool {
	return underWorktreePurgeRoots(strings.TrimSpace(path))
}

// WorkingTreeDiffText 返回 root 工作区相对 HEAD 的未提交改动的 unified diff 文本，含未跟踪的新文件。
// 会话在 AI 自建的 worktree 里干活时，Diff 面板用它取那个 worktree 的改动。
// 内部用临时 index 快照工作区，不修改用户的 index 与 HEAD。
func (r *Runner) WorkingTreeDiffText(ctx context.Context, root string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("git runner 不可用")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("worktree 路径为空")
	}
	if !r.hasCommits(ctx, root) {
		return "", nil
	}
	tree, err := r.captureWorkingTree(ctx, root)
	if err != nil {
		return "", err
	}
	out, err := r.run(ctx, root, nil, diffArgs("HEAD", tree, nil, false)...)
	if err != nil {
		return "", fmt.Errorf("git diff 工作区改动失败: %w", err)
	}
	return string(out), nil
}
