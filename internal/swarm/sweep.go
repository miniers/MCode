package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// SweepOrphanCopies 清理 WorktreeDir 下的孤儿副本目录：既没有被任何 Bee 的
// WorktreePath 引用（按 filepath.Clean 与 filepath.EvalSymlinks 比较，兼容 macOS
// 的 /var 到 /private/var 映射），也不在 startingWorkDirs 中登记。
// 返回已移除的目录列表；单个目录失败时继续处理其余目录，并返回首个错误。
func (m *Manager) SweepOrphanCopies(ctx context.Context) ([]string, error) {
	root := m.opts.WorktreeDir
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// 持锁收集被引用的路径，随后释放锁再做文件系统操作。
	m.mu.Lock()
	referenced := make([]string, 0, len(m.startingWorkDirs))
	for _, lead := range m.leads {
		for _, h := range lead.bees {
			if h.WorktreePath != "" {
				referenced = append(referenced, h.WorktreePath)
			}
		}
	}
	for path := range m.startingWorkDirs {
		referenced = append(referenced, path)
	}
	m.mu.Unlock()

	referencedSet := sweepPathSet(referenced)

	var removed []string
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if sweepPathReferenced(referencedSet, dir) {
			continue
		}
		if err := m.removeOrphanCopy(ctx, dir); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, dir)
	}
	return removed, firstErr
}

// sweepPathSet 将路径集合规范化为可比较集合：同时记录 Clean 结果与解析符号链接后的结果。
func sweepPathSet(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths)*2)
	for _, p := range paths {
		if p == "" {
			continue
		}
		cleaned := filepath.Clean(p)
		set[cleaned] = struct{}{}
		if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
			set[filepath.Clean(resolved)] = struct{}{}
		}
	}
	return set
}

// sweepPathReferenced 判断路径是否命中引用集合（Clean 或解析符号链接后命中都算）。
func sweepPathReferenced(set map[string]struct{}, p string) bool {
	if _, ok := set[filepath.Clean(p)]; ok {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		if _, ok := set[filepath.Clean(resolved)]; ok {
			return true
		}
	}
	return false
}

// removeOrphanCopy 移除单个孤儿副本目录：目录是 git worktree 时从主仓库执行
// worktree remove --force，否则直接删除目录。
func (m *Manager) removeOrphanCopy(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil && m.opts.Git != nil {
		if common, err := m.opts.Git.RevParse(ctx, dir, "--git-common-dir"); err == nil {
			if mainRepo, ok := mainRepoFromCommonDir(dir, common); ok {
				return m.opts.Git.WorktreeRemove(ctx, mainRepo, dir, true)
			}
		}
	}
	return os.RemoveAll(dir)
}

// mainRepoFromCommonDir 将 git rev-parse --git-common-dir 的输出换算为主仓库根目录；
// 与 internal/git 的 mainRepoRoot 一致：common 目录名不是 ".git" 时退回 worktree 目录本身。
func mainRepoFromCommonDir(dir, common string) (string, bool) {
	common = strings.TrimSpace(common)
	if common == "" {
		return "", false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) != ".git" {
		return dir, true
	}
	return filepath.Dir(common), true
}
