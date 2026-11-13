package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PurgeWorktree 单独删除一个附属 worktree 或已失效的孤立目录，并回收磁盘空间。
//
// 行为按 item 的状态区分：
//   - 目录存在的已注册 worktree：执行 git worktree remove；存在未提交改动时，
//     仅在 allowDirty 为 true 时附加 --force，否则返回明确错误；
//   - 孤立项（item.Orphan，或删除时目录已不存在）：仅当路径位于受管 worktree
//     基目录（见 SetWorktreePurgeRoots）之下时才 os.RemoveAll，否则拒绝；
//   - 删除完成后执行 git worktree prune 清理过期注册信息；若关联分支已合入默认
//     分支，尽力执行 git branch -d 回收分支（不使用 -D，未合入分支不会被删除）。
func (r *Runner) PurgeWorktree(ctx context.Context, root string, item WorktreeScanItem, allowDirty bool) error {
	if r == nil {
		return fmt.Errorf("git runner 不可用")
	}
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("仓库根目录为空")
	}
	path := filepath.Clean(strings.TrimSpace(item.Path))
	if path == "" || path == "." || !filepath.IsAbs(path) {
		return fmt.Errorf("worktree 路径无效: %q", item.Path)
	}

	rootResolved, pathResolved := resolvePath(root), resolvePath(path)
	sep := string(filepath.Separator)
	if pathResolved == rootResolved {
		return fmt.Errorf("拒绝删除主 worktree: %s", path)
	}
	if strings.HasPrefix(rootResolved+sep, pathResolved+sep) {
		return fmt.Errorf("拒绝删除包含仓库根目录的路径: %s", path)
	}

	orphan := item.Orphan
	if !orphan {
		if _, err := os.Stat(path); err != nil {
			orphan = true // 扫描后目录消失，按孤立项处理
		}
	}

	if orphan {
		if !underWorktreePurgeRoots(path) {
			return fmt.Errorf("孤立目录 %s 不在受管 worktree 基目录内，拒绝删除", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("删除孤立目录失败: %w", err)
		}
	} else {
		// 扫描与删除之间工作区可能又有改动，删除前用 git status 再确认一次。
		dirty := !item.Clean
		if out, err := r.run(ctx, path, nil, "status", "--porcelain"); err == nil {
			dirty = len(strings.TrimSpace(string(out))) > 0
		}
		if dirty && !allowDirty {
			return fmt.Errorf("worktree %s 存在未提交改动，需要确认丢弃后才能删除", path)
		}
		if err := r.WorktreeRemove(ctx, root, path, dirty); err != nil {
			return err
		}
	}

	if item.Branch != "" && item.Merged {
		// 已合入分支用 -d 删除；-d 会拒绝未合入的分支，不会误删。
		_ = r.BranchDelete(ctx, root, item.Branch, false)
	}
	if _, err := r.run(ctx, root, nil, "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune failed: %w", err)
	}
	return nil
}

// 受管 worktree 基目录：集中存储、项目内 .mcode/worktrees 与编排目录。
// 清理孤立目录前用它兜底校验，
// 避免过期注册信息把删除操作引向基目录之外的任意路径。
var (
	purgeRootsMu       sync.RWMutex
	purgeRootsOverride []string
)

// SetWorktreePurgeRoots 覆盖受管 worktree 基目录，应用层可传入真实数据目录；
// 传入空列表时恢复默认推导（MCODE_DATA 或 ~/.mcode）。
func SetWorktreePurgeRoots(dirs ...string) {
	clean := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if strings.TrimSpace(d) != "" {
			clean = append(clean, filepath.Clean(d))
		}
	}
	purgeRootsMu.Lock()
	purgeRootsOverride = clean
	purgeRootsMu.Unlock()
}

// worktreePurgeRoots 返回受管 worktree 基目录：优先使用显式覆盖值，否则按数据目录推导。
func worktreePurgeRoots() []string {
	purgeRootsMu.RLock()
	override := purgeRootsOverride
	purgeRootsMu.RUnlock()
	if len(override) > 0 {
		out := make([]string, len(override))
		copy(out, override)
		return out
	}
	dataDir := strings.TrimSpace(os.Getenv("MCODE_DATA"))
	if dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dataDir = filepath.Join(home, ".mcode")
		}
	}
	if dataDir == "" {
		return nil
	}
	return []string{
		filepath.Join(dataDir, "worktrees"),
		filepath.Join(dataDir, "swarm-worktrees"),
		filepath.Join(dataDir, "hydra-worktrees"),
	}
}

// pathUnderBase 判断 path 是否位于 base 之下（base 本身不算；两侧都先解析符号链接）。
func pathUnderBase(path, base string) bool {
	if path == "" || base == "" {
		return false
	}
	path, base = resolvePath(path), resolvePath(base)
	if path == base {
		return false
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path+sep, base+sep)
}

// underWorktreePurgeRoots 判断路径是否位于任一受管 worktree 基目录之下。
func underWorktreePurgeRoots(path string) bool {
	if IsProjectWorktreePath(resolvePath(path)) {
		return true
	}
	for _, base := range worktreePurgeRoots() {
		if pathUnderBase(path, base) {
			return true
		}
	}
	return false
}
