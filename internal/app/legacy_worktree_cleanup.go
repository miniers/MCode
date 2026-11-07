package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/git"
	"mcode/internal/store"
)

// legacyHydraCleanupSettingKey 是一次性清理标记：写入后不再扫描旧副本目录。
const legacyHydraCleanupSettingKey = "legacy_hydra_worktrees_cleaned"

// legacyHydraCleanupTimeout 限制首次清理的总时长，避免扫上百个旧副本时长时间占用 git 子进程。
const legacyHydraCleanupTimeout = 10 * time.Minute

// legacyResolvedPath 解析符号链接；路径已不存在时，对最近一级存在的父目录求值后拼回剩余部分。
// macOS 上 /var 指向 /private/var，两侧都解析后才能正确比较包含关系。
func legacyResolvedPath(p string) string {
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

// legacyPathUnderBase 报告 path 是否严格位于 base 之下（base 本身不算），两侧都先解析符号链接。
func legacyPathUnderBase(path, base string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(base) == "" {
		return false
	}
	path, base = legacyResolvedPath(path), legacyResolvedPath(base)
	if path == base {
		return false
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path+sep, base+sep)
}

// legacyHydraItemDeletable 判定一个旧副本是否满足全部删除条件。
// 这里会真的删除用户磁盘上的数据，任何一项不确定都必须保留。
func legacyHydraItemDeletable(item git.WorktreeScanItem, hydraDir string, inUse func(string) bool) bool {
	if item.Orphan || !item.Merged || !item.Clean {
		return false
	}
	if strings.TrimSpace(item.Branch) == "" {
		return false
	}
	if !legacyPathUnderBase(item.Path, hydraDir) {
		return false
	}
	if inUse != nil && inUse(item.Path) {
		return false
	}
	return true
}

// legacyThreadPathInUse 报告 path 是否被任一会话的 worktree_path 引用：路径完全相同，
// 或会话的工作目录位于该副本之下（子目录也算在用），两侧都先解析符号链接。
func legacyThreadPathInUse(path string, threadPaths []string) bool {
	target := legacyResolvedPath(path)
	for _, p := range threadPaths {
		if legacyResolvedPath(p) == target || legacyPathUnderBase(p, path) {
			return true
		}
	}
	return false
}

// legacyCollectThreadWorktreePaths 收集全部会话（含已归档）绑定的 worktree 路径。
// 有会话读不出来时无法确认保护范围，直接返回错误，让调用方放弃本次清理。
func legacyCollectThreadWorktreePaths(ctx context.Context, st *store.Store) ([]string, error) {
	threads, badIDs, err := st.ListThreadsTolerant(ctx)
	if err != nil {
		return nil, fmt.Errorf("列出会话失败: %w", err)
	}
	if len(badIDs) > 0 {
		return nil, fmt.Errorf("有 %d 个会话读不出来（%s），无法确认副本保护范围", len(badIDs), strings.Join(badIDs, ", "))
	}
	paths := make([]string, 0, len(threads))
	for _, th := range threads {
		if p := strings.TrimSpace(th.WorktreePath); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// cleanupLegacyHydraWorktrees 清理旧版 hydra-worktrees 目录下已合并且干净的 worktree 副本。
//
// 只删除同时满足「位于 hydraDir 之下、已合入默认分支、无未提交改动、目录仍在、分支名非空、
// 未在使用」的项；其余项一律保留且不做任何改动（未合并的留给用户在设置页手动处理）。
// hydraDir 之外的项目 worktree 既不删除也不计入 kept。单项删除失败只记日志并计入 kept；
// 某个仓库扫描失败会继续处理下一个仓库，但最终返回非 nil 错误，让调用方不写完成标记、
// 下次启动重试。ctx 取消时立即返回 ctx.Err()。
func cleanupLegacyHydraWorktrees(ctx context.Context, runner *git.Runner, hydraDir string, roots []string, inUse func(string) bool) (removed, kept int, err error) {
	if runner == nil {
		return 0, 0, errors.New("git runner 不可用")
	}
	hydraDir = strings.TrimSpace(hydraDir)
	if hydraDir == "" {
		return 0, 0, errors.New("旧副本目录为空")
	}
	var firstErr error
	for _, root := range roots {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return removed, kept, ctxErr
		}
		items, scanErr := runner.ScanWorktrees(ctx, root)
		if scanErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return removed, kept, ctxErr
			}
			log.Printf("旧副本清理: 扫描仓库 %s 失败，本次跳过: %v", root, scanErr)
			if firstErr == nil {
				firstErr = scanErr
			}
			continue
		}
		for _, item := range items {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return removed, kept, ctxErr
			}
			// hydraDir 之外的项目 worktree 与本次清理无关，既不删除也不计数。
			if !legacyPathUnderBase(item.Path, hydraDir) {
				continue
			}
			if !legacyHydraItemDeletable(item, hydraDir, inUse) {
				kept++
				continue
			}
			// allowDirty 必须为 false：扫描后工作区可能又有改动，PurgeWorktree 会再确认一次。
			if purgeErr := runner.PurgeWorktree(ctx, item.RepoRoot, item, false); purgeErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return removed, kept, ctxErr
				}
				log.Printf("旧副本清理: 删除 %s 失败，已保留: %v", item.Path, purgeErr)
				kept++
				continue
			}
			removed++
		}
	}
	return removed, kept, firstErr
}

// runLegacyHydraCleanupOnce 在应用升级后的首次启动执行一次清理：
// 标记已存在时直接返回；旧副本目录不存在时直接写标记；仓库根目录列表为空或清理出错时
// 不写标记，下次启动重试。除 inUse 之外，任何会话（含已归档）绑定的 worktree 路径也算在用；
// 会话列表读不完整时无法确认保护范围，同样不删除、不写标记。
func runLegacyHydraCleanupOnce(ctx context.Context, st *store.Store, runner *git.Runner, hydraDir string, repoRoots func(context.Context) ([]string, error), inUse func(string) bool) error {
	if st == nil {
		return errors.New("store 不可用")
	}
	if _, ok, err := st.GetSetting(ctx, legacyHydraCleanupSettingKey); err != nil {
		return fmt.Errorf("读取 %s 标记失败: %w", legacyHydraCleanupSettingKey, err)
	} else if ok {
		return nil
	}
	hydraDir = strings.TrimSpace(hydraDir)
	if hydraDir == "" {
		return errors.New("旧副本目录为空")
	}
	if info, err := os.Stat(hydraDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 新装用户没有旧目录，直接写标记，之后不再检查。
			return markLegacyHydraCleaned(ctx, st)
		}
		// 其他错误（权限、IO 等）无法判定目录状态，不写标记，下次启动重试。
		return fmt.Errorf("检查旧副本目录失败: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("旧副本路径不是目录: %s", hydraDir)
	}
	var roots []string
	if repoRoots != nil {
		r, err := repoRoots(ctx)
		if err != nil {
			return fmt.Errorf("列出仓库根目录失败: %w", err)
		}
		roots = r
	}
	if len(roots) == 0 {
		// 项目列表可能尚未加载，这一次不扫描也不写标记，下次启动重试。
		return nil
	}
	threadPaths, err := legacyCollectThreadWorktreePaths(ctx, st)
	if err != nil {
		return err
	}
	guardedInUse := func(path string) bool {
		if inUse != nil && inUse(path) {
			return true
		}
		return legacyThreadPathInUse(path, threadPaths)
	}
	removed, kept, err := cleanupLegacyHydraWorktrees(ctx, runner, hydraDir, roots, guardedInUse)
	if err != nil {
		// 出错不写标记，下次启动重试。
		return err
	}
	log.Printf("旧副本清理: 已删除 %d 个已合并副本，保留 %d 个（未合并或有改动，可在设置页手动处理）", removed, kept)
	return markLegacyHydraCleaned(ctx, st)
}

// markLegacyHydraCleaned 写入一次性完成标记。
func markLegacyHydraCleaned(ctx context.Context, st *store.Store) error {
	if err := st.SetSetting(ctx, legacyHydraCleanupSettingKey, "1"); err != nil {
		return fmt.Errorf("写入 %s 标记失败: %w", legacyHydraCleanupSettingKey, err)
	}
	return nil
}
