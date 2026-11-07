package git

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultBranch 返回仓库的默认分支：优先 origin/HEAD 符号引用，
// 其次依次尝试本地 main、master，最后回退到当前分支。
func (r *Runner) DefaultBranch(ctx context.Context, root string) (string, error) {
	if out, err := r.run(ctx, root, nil, "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); err == nil {
		name := strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
		if name != "" {
			return name, nil
		}
	}
	for _, cand := range []string{"main", "master"} {
		if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+cand); err == nil {
			return cand, nil
		}
	}
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", errors.New("无法确定默认分支")
	}
	return branch, nil
}

// IsBranchMerged 判断 branch 是否已完全合入 base（branch 是 base 的祖先）。
func (r *Runner) IsBranchMerged(ctx context.Context, root, branch, base string) (bool, error) {
	if branch == "" || base == "" {
		return false, nil
	}
	_, err := r.run(ctx, root, nil, "merge-base", "--is-ancestor", branch, base)
	if err == nil {
		return true, nil
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// WorktreeScanItem 描述扫描到的一个附属 worktree。
type WorktreeScanItem struct {
	Path      string
	Branch    string
	RepoRoot  string
	Merged    bool
	Clean     bool
	Orphan    bool // 目录已不存在
	Age       time.Duration
	ModTime   time.Time
	SizeBytes int64
}

// mainRepoRoot 若 root 本身是 linked worktree，则经 --git-common-dir 回溯到主仓库根目录。
func (r *Runner) mainRepoRoot(ctx context.Context, root string) (string, error) {
	out, err := r.run(ctx, root, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", ErrNotRepo
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", ErrNotRepo
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	return root, nil
}

// ScanWorktrees 列出 root 所属仓库下除主 worktree 以外的全部 worktree。
func (r *Runner) ScanWorktrees(ctx context.Context, root string) ([]WorktreeScanItem, error) {
	mainRoot, err := r.mainRepoRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	list, err := r.WorktreeList(ctx, mainRoot)
	if err != nil {
		return nil, err
	}
	base, _ := r.DefaultBranch(ctx, mainRoot)
	now := time.Now()

	// 收集需要扫描的附属 worktree 下标，第一项为主 worktree。
	idxs := make([]int, 0, len(list))
	for i, wt := range list {
		if i == 0 || wt.Bare {
			continue
		}
		idxs = append(idxs, i)
	}

	// 每个附属 worktree 的目录体积统计与 git 查询互相独立，按有界并发处理。
	// Runner.run 内部有命令级信号量限流，并发调用是安全的；这里的并发主要让目录遍历
	// 与 git 子进程重叠，避免几十个 worktree 串行累加导致扫描超时。
	// 每个 worker 只写自己下标对应的槽位，无需加锁。
	slots := make([]*WorktreeScanItem, len(list))
	workers := 8
	if len(idxs) < workers {
		workers = len(idxs)
	}
	tasks := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range tasks {
				wt := list[i]
				item := WorktreeScanItem{Path: wt.Path, Branch: wt.Branch, RepoRoot: mainRoot}
				info, statErr := os.Stat(wt.Path)
				if statErr != nil {
					item.Orphan = true
					slots[i] = &item
					continue
				}
				item.ModTime = info.ModTime()
				item.Age = now.Sub(item.ModTime)
				item.SizeBytes = dirSize(ctx, wt.Path)
				if out, err := r.run(ctx, wt.Path, nil, "status", "--porcelain"); err == nil {
					item.Clean = len(strings.TrimSpace(string(out))) == 0
				}
				if wt.Branch != "" && base != "" {
					item.Merged, _ = r.IsBranchMerged(ctx, mainRoot, wt.Branch, base)
				}
				slots[i] = &item
			}
		}()
	}
	for _, i := range idxs {
		tasks <- i
	}
	close(tasks)
	wg.Wait()

	// 按原列表顺序压缩结果，保证输出顺序与串行实现一致。
	items := make([]WorktreeScanItem, 0, len(idxs))
	for _, it := range slots {
		if it != nil {
			items = append(items, *it)
		}
	}
	// 上下文已取消时结果不完整，交给调用方按超时处理。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// dirSize 累加目录下普通文件体积，跳过 .git；ctx 取消后立即结束遍历。
func dirSize(ctx context.Context, dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// CleanupGuard 为自动清理提供防竞态判定。
type CleanupGuard struct {
	InUse   func(path string) bool // 路径是否被活跃会话/Head 使用
	Now     func() time.Time       // 注入当前时间，默认 time.Now
	MinIdle time.Duration          // 最小空闲时长，默认 10 分钟
}

func (g CleanupGuard) inUse(p string) bool {
	return g.InUse != nil && g.InUse(p)
}

func (g CleanupGuard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g CleanupGuard) minIdle() time.Duration {
	if g.MinIdle > 0 {
		return g.MinIdle
	}
	return 10 * time.Minute
}

// resolvePath 解析符号链接；路径已不存在时，对最近一级存在的父目录求值后拼回剩余部分。
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// overlapsCwd 判断 path 是否等于、位于 cwd 之上（祖先）或之下。
func overlapsCwd(path, cwd string) bool {
	path, cwd = resolvePath(path), resolvePath(cwd)
	if path == cwd {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(cwd+sep, path+sep) || strings.HasPrefix(path+sep, cwd+sep)
}

// CleanSafeWorktrees 删除已合入、无改动、未被使用且空闲足够久的 worktree，返回已删除路径。
func (r *Runner) CleanSafeWorktrees(ctx context.Context, root string, g CleanupGuard) ([]string, error) {
	mainRoot, err := r.mainRepoRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	items, err := r.ScanWorktrees(ctx, mainRoot)
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()

	var removed []string
	var firstErr error
	for _, it := range items {
		// 只清理应用自建的 worktree；用户手建的（不在受管基目录下）一律不碰。
		if !IsManagedWorktreePath(it.Path) {
			continue
		}
		if it.Orphan || !it.Merged || !it.Clean {
			continue
		}
		if g.inUse(it.Path) {
			continue
		}
		if cwd != "" && overlapsCwd(it.Path, cwd) {
			continue
		}
		if g.now().Sub(it.ModTime) < g.minIdle() {
			continue
		}
		// 二次校验：删除前重新 stat 并再次确认未被占用。
		info, statErr := os.Stat(it.Path)
		if statErr != nil || g.now().Sub(info.ModTime()) < g.minIdle() || g.inUse(it.Path) {
			continue
		}
		if err := r.WorktreeRemove(ctx, mainRoot, it.Path, false); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, it.Path)
	}
	if len(removed) > 0 {
		if _, err := r.run(ctx, mainRoot, nil, "worktree", "prune"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return removed, firstErr
}
