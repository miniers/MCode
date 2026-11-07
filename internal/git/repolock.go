package git

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// repoLocks 保存按仓库共用目录（common dir）划分的互斥锁：键为 .git 目录或 worktree 共享的
// commondir 绝对路径。同一仓库的主工作区与各 git worktree 共用 refs、packed-refs 与对象库，
// 并发写会争用 .git/index.lock、refs 下的锁文件，因此它们必须落在同一把锁上。
var repoLocks sync.Map // map[string]*sync.Mutex

// repoLockKeyCache 缓存目录到锁键的解析结果，避免每次写操作都读文件。
// 只缓存成功从 .git 解析出共用目录的目录，退回规范化路径的情况不缓存。
var repoLockKeyCache sync.Map // map[string]string

// canonicalRepoDir 返回用于标识仓库的规范化目录：绝对路径 + 清理后的形式。
// 解析失败（例如当前工作目录已不可用）时退回原值，保证仍能取到一把稳定的锁。
func canonicalRepoDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return filepath.Clean(abs)
}

// repoLockKey 返回用于加锁的仓库共用目录：从 dir 向上找 .git，
// .git 为目录时就是它本身；.git 为文件（worktree、子模块）时按其中的 gitdir 与 commondir 解析。
// 找不到 .git 或读取失败时退回 canonicalRepoDir(dir)，行为与未按仓库归类时一致。
func repoLockKey(dir string) string {
	base := canonicalRepoDir(dir)
	if cached, ok := repoLockKeyCache.Load(base); ok {
		if key, ok := cached.(string); ok {
			return key
		}
	}
	key, ok := findRepoCommonDir(base)
	if !ok || key == "" {
		return base
	}
	repoLockKeyCache.Store(base, key)
	return key
}

// findRepoCommonDir 从 dir 向上逐级寻找 .git，返回可用于串行化写操作的仓库共用目录。
// 只用 os 与 filepath 读文件，不启动 git 子进程。
func findRepoCommonDir(dir string) (string, bool) {
	for cur := dir; ; {
		gitEntry := filepath.Join(cur, ".git")
		info, err := os.Stat(gitEntry)
		if err == nil {
			if info.IsDir() {
				return gitEntry, true
			}
			return repoKeyFromGitFile(gitEntry)
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

// repoKeyFromGitFile 解析 .git 文件（worktree、子模块）指向的仓库共用目录。
// gitdir 与 commondir 中的相对路径分别相对 .git 文件所在目录与 gitdir 解析。
func repoKeyFromGitFile(gitFile string) (string, bool) {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "gitdir:"))
	if line == "" {
		return "", false
	}
	gitdir := resolveRepoPath(filepath.Dir(gitFile), line)

	commonData, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return gitdir, true
	}
	common := strings.TrimSpace(string(commonData))
	if common == "" {
		return gitdir, true
	}
	return resolveRepoPath(gitdir, common), true
}

// resolveRepoPath 把可能相对的路径按 base 解析为清理后的绝对路径。
func resolveRepoPath(base, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// lockRepo 获取指定目录所属仓库的写锁，返回释放函数（需由调用方 defer 调用）。
// 主工作区与其各 git worktree 共享同一把锁；不同仓库互不影响；
// 指向同一目录的不同写法（相对/绝对、含 ..）会落到同一把锁上。
func lockRepo(dir string) func() {
	key := repoLockKey(dir)
	value, _ := repoLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
