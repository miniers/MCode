package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ErrHeadMoved 表示 HEAD 的比较交换失败，引用与索引均未被本次操作修改。
var ErrHeadMoved = errors.New("HEAD 比较交换失败，分支未改动")

// ErrIndexLocked 表示索引已被锁定，引用与索引均未被本次操作修改。
var ErrIndexLocked = errors.New("暂存区被 index.lock 锁定，分支未改动")

// CommitTree 使用用户自己的身份创建提交对象，不移动引用或修改工作区。
func (r *Runner) CommitTree(ctx context.Context, root, tree, parent, message string) (string, error) {
	out, err := r.run(ctx, root, nil, "commit-tree", tree, "-p", parent, "-m", message)
	if err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DiffTreeNames 返回两个提交或树之间变更的路径，保留原始文件名。
func (r *Runner) DiffTreeNames(ctx context.Context, root, from, to string) ([]string, error) {
	out, err := r.run(ctx, root, nil, "diff-tree", "-r", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, fmt.Errorf("diff-tree: %w", err)
	}
	paths := []string{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// AdvanceHead 比较交换 HEAD，随后同步未被用户暂存的路径；不写工作区文件。
// 已存在索引锁时不前进 HEAD；之后索引同步出错时 HEAD 已前进。
func (r *Runner) AdvanceHead(ctx context.Context, root, oldHead, newSHA string) (stagedKept []string, err error) {
	paths, err := r.DiffTreeNames(ctx, root, oldHead, newSHA)
	if err != nil {
		return nil, err
	}
	indexPath, err := r.IndexPath(ctx, root)
	if err != nil {
		return nil, err
	}
	lockPath := indexPath + ".lock"
	if _, err := os.Stat(lockPath); err == nil {
		cleared, clearErr := r.ClearStaleIndexLock(ctx, root)
		if clearErr != nil || !cleared {
			return nil, fmt.Errorf("%w: %s", ErrIndexLocked, lockPath)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := r.run(ctx, root, nil, "update-ref", "-m", "mcode-swarm", "HEAD", newSHA, oldHead); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHeadMoved, err)
	}
	stagedKept, err = r.syncRealIndex(ctx, root, oldHead, newSHA, paths)
	if err != nil {
		return stagedKept, fmt.Errorf("sync real index: %w", err)
	}
	return stagedKept, nil
}
