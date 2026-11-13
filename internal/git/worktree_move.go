package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// WorktreeMovePreview 描述把当前工作区迁入 worktree 时将被转移的内容。
type WorktreeMovePreview struct {
	RepoRoot   string
	HeadCommit string
	Files      []string // 相对 HEAD 有改动的 tracked 文件（含已暂存/未暂存/已删除）
	Untracked  []string // 未跟踪且未被忽略的文件
}

func splitNUL(b []byte) []string {
	var out []string
	for _, s := range strings.Split(string(b), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// PreviewWorktreeMove 预览迁移内容；仓库没有任何 commit 时报错。
func (r *Runner) PreviewWorktreeMove(ctx context.Context, repoDir string) (WorktreeMovePreview, error) {
	root, err := r.RepoRoot(ctx, repoDir)
	if err != nil {
		return WorktreeMovePreview{}, err
	}
	head, err := r.RevParse(ctx, root, "HEAD")
	if err != nil {
		return WorktreeMovePreview{}, errors.New("仓库还没有任何 commit，无法迁入 Worktree")
	}
	files, err := r.run(ctx, root, nil, "diff", "--name-only", "--no-renames", "-z", "HEAD")
	if err != nil {
		return WorktreeMovePreview{}, err
	}
	untracked, err := r.run(ctx, root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return WorktreeMovePreview{}, err
	}
	return WorktreeMovePreview{
		RepoRoot:   root,
		HeadCommit: head,
		Files:      splitNUL(files),
		Untracked:  splitNUL(untracked),
	}, nil
}

// runStdin 与 run 类似，但允许向 git 传入标准输入。
func (r *Runner) runStdin(ctx context.Context, dir string, stdin []byte, args ...string) error {
	cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(ctx, r.Bin, args...)))
	cmd.Dir = dir
	// 继承登录 Shell 环境变量，保证 git hooks 能用到用户 PATH 下的工具
	cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// MoveIntoWorktree 在 wtPath 基于当前 HEAD 新建分支 worktree，并把未提交改动搬过去，
// 成功后原工作区恢复干净。任一步失败都不改动原仓库，并回滚新建的 worktree 与分支。
// stillIdle 为 nil 时视为恒空闲。
func (r *Runner) MoveIntoWorktree(ctx context.Context, repoDir, wtPath, branch string, stillIdle func() bool) (WorktreeMovePreview, error) {
	pv, err := r.PreviewWorktreeMove(ctx, repoDir)
	if err != nil {
		return pv, err
	}
	if !filepath.IsAbs(wtPath) {
		return pv, fmt.Errorf("worktree path must be absolute: %s", wtPath)
	}
	if branch == "" {
		return pv, errors.New("分支名不能为空")
	}

	// 目标路径已存在：已登记的 worktree 不覆盖，否则视为残留目录清掉。
	if _, statErr := os.Lstat(wtPath); statErr == nil {
		list, lerr := r.WorktreeList(ctx, pv.RepoRoot)
		if lerr != nil {
			return pv, lerr
		}
		for _, wt := range list {
			if resolvePath(wt.Path) == resolvePath(wtPath) {
				return pv, fmt.Errorf("目标路径已是 Worktree: %s", wtPath)
			}
		}
		if err := os.RemoveAll(wtPath); err != nil {
			return pv, err
		}
	}
	_, _ = r.run(ctx, pv.RepoRoot, nil, "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return pv, err
	}
	if err := r.WorktreeAdd(ctx, pv.RepoRoot, wtPath, branch, "HEAD"); err != nil {
		return pv, err
	}

	rollback := func(cause error) (WorktreeMovePreview, error) {
		_ = r.WorktreeRemove(ctx, pv.RepoRoot, wtPath, true)
		_ = os.RemoveAll(wtPath)
		_, _ = r.run(ctx, pv.RepoRoot, nil, "worktree", "prune")
		_ = r.BranchDelete(ctx, pv.RepoRoot, branch, true)
		return pv, cause
	}

	if stillIdle != nil && !stillIdle() {
		return rollback(errors.New("会话状态已变化，取消迁入 Worktree"))
	}

	for _, rel := range append(append([]string{}, pv.Files...), pv.Untracked...) {
		if err := copyOrDelete(pv.RepoRoot, wtPath, rel); err != nil {
			return rollback(fmt.Errorf("复制 %s 失败: %w", rel, err))
		}
	}

	// 复制全部成功后才清理原工作区。
	var inHead, notInHead []string
	for _, rel := range pv.Files {
		if _, err := r.run(ctx, pv.RepoRoot, nil, "cat-file", "-e", "HEAD:"+rel); err == nil {
			inHead = append(inHead, rel)
		} else {
			notInHead = append(notInHead, rel)
		}
	}
	if len(inHead) > 0 {
		in := strings.Join(inHead, "\x00") + "\x00"
		if err := r.runStdin(ctx, pv.RepoRoot, []byte(in), "restore", "--staged", "--worktree", "--source=HEAD", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return pv, fmt.Errorf("还原原工作区失败（新 Worktree 已保留）: %w", err)
		}
	}
	for _, rel := range notInHead { // 新增且已暂存的文件
		_, _ = r.run(ctx, pv.RepoRoot, nil, "rm", "-f", "--cached", "--ignore-unmatch", "--", rel)
		_ = os.Remove(filepath.Join(pv.RepoRoot, rel))
	}
	for _, rel := range pv.Untracked {
		_ = os.Remove(filepath.Join(pv.RepoRoot, rel))
	}
	return pv, nil
}

// copyOrDelete 把 src 下的 rel 复制到 dst；源文件不存在则删除目标侧文件。保留权限与符号链接。
func copyOrDelete(srcRoot, dstRoot, rel string) error {
	src := filepath.Join(srcRoot, rel)
	dst := filepath.Join(dstRoot, rel)
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("不支持的文件类型: %s", info.Mode())
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_ = os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode().Perm())
}
