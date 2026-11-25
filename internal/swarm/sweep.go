package swarm

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcode/internal/config"
	"mcode/internal/git"
)

// SweepOrphanCopies 清理集中目录与已知项目内的孤儿副本目录：既没有被任何 Bee 的
// WorktreePath 引用（按 filepath.Clean 与 filepath.EvalSymlinks 比较，兼容 macOS
// 的 /var 到 /private/var 映射），也不在 startingWorkDirs 中登记。
// 返回已移除的目录列表；单个目录失败时继续处理其余目录，并返回首个错误。
func (m *Manager) SweepOrphanCopies(ctx context.Context) ([]string, error) {
	// 持锁收集已知项目，仓库探测与目录读取放在锁外。
	m.mu.Lock()
	leadDirs := make(map[string]struct{})
	for _, lead := range m.leads {
		if lead.leadWorkDir != "" {
			leadDirs[lead.leadWorkDir] = struct{}{}
		}
		for _, h := range lead.bees {
			if h.TargetWorkDir != "" && !h.ShadowRepo && !h.SharedNoGit {
				leadDirs[h.TargetWorkDir] = struct{}{}
			}
		}
	}
	for _, q := range m.queue {
		if q.leadWorkDir != "" {
			leadDirs[q.leadWorkDir] = struct{}{}
		}
	}
	m.mu.Unlock()

	var roots []string
	if m.opts.WorktreeDir != "" {
		roots = append(roots, m.opts.WorktreeDir)
	}
	seen := sweepPathSet(roots)
	repoRoots := make(map[string]struct{})
	if m.opts.Git != nil {
		for dir := range leadDirs {
			repoRoot, err := m.opts.Git.RepoRoot(ctx, dir)
			if err != nil {
				continue
			}
			repoRoots[repoRoot] = struct{}{}
			root := filepath.Join(git.WorktreeBase("", repoRoot, config.WorktreeLocationProject), "swarm")
			if !sweepPathReferenced(seen, root) {
				roots = append(roots, root)
				seen = sweepPathSet(roots)
			}
		}
	}

	var candidates []string
	var firstErr error
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidates = append(candidates, filepath.Join(root, entry.Name()))
		}
	}

	// 扫描之后再收集引用，保护扫描期间完成启动登记的副本。
	m.mu.Lock()
	referenced := make([]string, 0, len(m.startingWorkDirs))
	referencedBranches := make(map[string]struct{})
	activePrefixes := make(map[string]struct{})
	for leadID, lead := range m.leads {
		if len(lead.bees) > 0 || len(lead.heldQueue) > 0 {
			if prefix := leadPrefixID(leadID); prefix != "" {
				activePrefixes[prefix] = struct{}{}
			}
		}
		for _, h := range lead.bees {
			if h.Branch != "" {
				referencedBranches[h.Branch] = struct{}{}
			}
			if h.WorktreePath != "" {
				referenced = append(referenced, h.WorktreePath)
			}
		}
	}
	for _, q := range m.queue {
		if prefix := leadPrefixID(q.leadThreadID); prefix != "" {
			activePrefixes[prefix] = struct{}{}
		}
	}
	for path, owner := range m.startingWorkDirs {
		referenced = append(referenced, path)
		if prefix := leadPrefixID(owner.leadThreadID); prefix != "" {
			activePrefixes[prefix] = struct{}{}
		}
	}
	m.mu.Unlock()
	referencedSet := sweepPathSet(referenced)
	var removed []string
	for _, dir := range candidates {
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
	for repoRoot := range repoRoots {
		if _, err := m.runGitCmd(ctx, repoRoot, "worktree", "prune"); err != nil && firstErr == nil {
			firstErr = err
		}
		branches, err := m.opts.Git.Branches(ctx, repoRoot)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		worktrees, err := m.opts.Git.WorktreeList(ctx, repoRoot)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		checkedOut := make(map[string]struct{}, len(worktrees))
		for _, worktree := range worktrees {
			checkedOut[worktree.Branch] = struct{}{}
		}
		for _, branch := range branches {
			if branch.Remote || !strings.HasPrefix(branch.Name, "mcode/swarm/") {
				continue
			}
			if _, ok := referencedBranches[branch.Name]; ok {
				continue
			}
			if _, ok := checkedOut[branch.Name]; ok {
				continue
			}
			if err := m.opts.Git.BranchDelete(ctx, repoRoot, branch.Name, true); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			log.Printf("swarm: 已删除无引用分支 %s（%s）", branch.Name, repoRoot)
		}
		if err := m.sweepOrphanSwarmRefs(ctx, repoRoot, activePrefixes); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return removed, firstErr
}

// sweepOrphanSwarmRefs 清理真实 Git 仓库中不再属于任何活跃 lead 的 swarm-base、swarm-land、swarm-fold 引用。
func (m *Manager) sweepOrphanSwarmRefs(ctx context.Context, repoRoot string, activePrefixes map[string]struct{}) error {
	if m.opts.Git == nil || !m.opts.Git.IsRealRepo(ctx, repoRoot) {
		return nil
	}
	orphanSet := make(map[string]struct{})
	var firstErr error
	for _, basePrefix := range []string{
		"refs/mcode/swarm-base/",
		"refs/mcode/swarm-land/",
		"refs/mcode/swarm-fold/",
	} {
		refs, err := m.opts.Git.ListRefs(ctx, repoRoot, basePrefix)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, ref := range refs {
			rest := strings.TrimPrefix(ref.Name, basePrefix)
			leadPrefix, _, _ := strings.Cut(rest, "/")
			if leadPrefix == "" {
				continue
			}
			if _, active := activePrefixes[leadPrefix]; active {
				continue
			}
			orphanSet[leadPrefix] = struct{}{}
		}
	}
	if len(orphanSet) == 0 {
		return firstErr
	}

	orphans := make([]string, 0, len(orphanSet))
	for prefix := range orphanSet {
		orphans = append(orphans, prefix)
	}
	sort.Strings(orphans)

	const maxPrefixesPerCall = 90
	batch := make([]string, 0, maxPrefixesPerCall)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := m.opts.Git.DeleteRefsWithPrefix(ctx, repoRoot, batch...); err != nil && firstErr == nil {
			firstErr = err
		}
		batch = batch[:0]
	}
	for _, prefix := range orphans {
		batch = append(batch,
			"refs/mcode/swarm-base/"+prefix,
			"refs/mcode/swarm-land/"+prefix+"/",
			"refs/mcode/swarm-fold/"+prefix+"/",
		)
		if len(batch) >= maxPrefixesPerCall {
			flush()
		}
	}
	flush()
	return firstErr
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
