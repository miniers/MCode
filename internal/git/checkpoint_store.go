package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const checkpointRefRootPrefix = "refs/mcode/checkpoints/"

var (
	checkpointPathCacheMu sync.RWMutex
	checkpointPathCache   = map[string]string{} // normalizeDir(root) -> <commonDir>/mcode/checkpoints.git
	checkpointStoreLocks  sync.Map              // storeDir -> *sync.Mutex
)

// isCheckpointRef 判断 ref 是否属于回合检查点命名空间。
func isCheckpointRef(ref string) bool {
	return strings.HasPrefix(ref, checkpointRefRootPrefix)
}

// isCheckpointRefPrefix 判断前缀是否指向回合检查点命名空间。
func isCheckpointRefPrefix(prefix string) bool {
	return strings.HasPrefix(prefix, checkpointRefRootPrefix) || prefix == strings.TrimSuffix(checkpointRefRootPrefix, "/")
}

// checkpointEnv 返回在检查点独立仓库中执行 git 命令所需的环境变量。
func checkpointEnv(gitDir, root string) []string {
	return []string{
		"GIT_DIR=" + gitDir,
		"GIT_WORK_TREE=" + root,
	}
}

// checkpointObjectsEnv 返回在用户仓库执行只读命令时用于读取检查点仓库对象的环境变量。
func checkpointObjectsEnv(store string) []string {
	if store == "" {
		return nil
	}
	return []string{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(store, "objects"),
	}
}

func isBareCheckpointStore(storeDir string) bool {
	if storeDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(storeDir, "HEAD")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(storeDir, "objects", "info", "alternates")); err != nil {
		return false
	}
	return true
}

func lockCheckpointStoreInit(storeDir string) func() {
	v, _ := checkpointStoreLocks.LoadOrStore(storeDir, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// resolveCheckpointStorePath 解析 root 对应的检查点仓库路径，并缓存解析结果。
// 影子仓库不使用独立检查点仓库，返回 ("", true, nil)。
func (r *Runner) resolveCheckpointStorePath(ctx context.Context, root string) (storeDir string, isShadow bool, err error) {
	if r == nil {
		return "", false, errors.New("git runner is nil")
	}
	if len(r.shadowEnv(root)) > 0 {
		return "", true, nil
	}
	normRoot := normalizeDir(root)
	checkpointPathCacheMu.RLock()
	cached, ok := checkpointPathCache[normRoot]
	checkpointPathCacheMu.RUnlock()
	if ok && cached != "" {
		return cached, false, nil
	}
	commonDir, err := r.GitCommonDir(ctx, root)
	if err != nil {
		return "", false, err
	}
	storeDir = filepath.Join(normalizeDir(commonDir), "mcode", "checkpoints.git")
	checkpointPathCacheMu.Lock()
	checkpointPathCache[normRoot] = storeDir
	checkpointPathCacheMu.Unlock()
	return storeDir, false, nil
}

// existingCheckpointStore 仅在检查点仓库已存在时返回其路径，不触发创建（供只读路径使用）。
func (r *Runner) existingCheckpointStore(ctx context.Context, root string) (string, bool) {
	storeDir, isShadow, err := r.resolveCheckpointStorePath(ctx, root)
	if err != nil || isShadow || !isBareCheckpointStore(storeDir) {
		return "", false
	}
	return storeDir, true
}

// checkpointReadEnv 在检查点仓库已存在时返回只读对象环境变量，否则返回 nil。
func (r *Runner) checkpointReadEnv(ctx context.Context, root string) []string {
	if storeDir, ok := r.existingCheckpointStore(ctx, root); ok {
		return checkpointObjectsEnv(storeDir)
	}
	return nil
}

// checkpointStore 确保真实仓库拥有可用的独立检查点仓库并返回其路径。
// 影子仓库返回 ("", nil)，保持原有行为。首次创建时会批量清理用户仓库中遗留的 refs/mcode/checkpoints/*。
func (r *Runner) checkpointStore(ctx context.Context, root string) (gitDir string, err error) {
	storeDir, isShadow, err := r.resolveCheckpointStorePath(ctx, root)
	if err != nil {
		return "", err
	}
	if isShadow {
		return "", nil
	}
	if isBareCheckpointStore(storeDir) {
		return storeDir, nil
	}

	unlock := lockCheckpointStoreInit(storeDir)
	defer unlock()

	if isBareCheckpointStore(storeDir) {
		return storeDir, nil
	}

	mcodeDir := filepath.Dir(storeDir)
	if err := os.MkdirAll(mcodeDir, 0o755); err != nil {
		return "", fmt.Errorf("create checkpoint store parent dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(mcodeDir, ".checkpoints.git.tmp-*")
	if err != nil {
		return "", fmt.Errorf("create checkpoint store temp dir: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	if _, err := r.run(ctx, root, nil, "init", "--bare", tmpDir); err != nil {
		return "", fmt.Errorf("init checkpoint store: %w", err)
	}
	infoDir := filepath.Join(tmpDir, "objects", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return "", fmt.Errorf("create checkpoint store alternates dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "alternates"), []byte("../../../objects\n"), 0o644); err != nil {
		return "", fmt.Errorf("write checkpoint store alternates: %w", err)
	}

	if err := os.Rename(tmpDir, storeDir); err != nil {
		if isBareCheckpointStore(storeDir) {
			return storeDir, nil
		}
		return "", fmt.Errorf("install checkpoint store: %w", err)
	}
	renamed = true

	if err := r.migrateUserRepoCheckpoints(ctx, root); err != nil {
		return "", err
	}
	return storeDir, nil
}

// migrateUserRepoCheckpoints 使用一次 update-ref --stdin 批量删除用户仓库中遗留的 refs/mcode/checkpoints/*。
func (r *Runner) migrateUserRepoCheckpoints(ctx context.Context, root string) error {
	out, err := r.run(ctx, root, nil, "for-each-ref", "--format=%(refname)", checkpointRefRootPrefix)
	if err != nil {
		return fmt.Errorf("list legacy checkpoints: %w", err)
	}
	var stdin bytes.Buffer
	for _, line := range strings.Split(string(out), "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" {
			continue
		}
		fmt.Fprintf(&stdin, "delete %s\n", ref)
	}
	if stdin.Len() == 0 {
		return nil
	}
	if _, err := r.runWithStdinLockRetry(ctx, root, nil, stdin.Bytes(), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("delete legacy checkpoints: %w", err)
	}
	return nil
}

// runWithStdinLockRetry 在遇到锁文件冲突时带退避重试 runWithStdin。
func (r *Runner) runWithStdinLockRetry(ctx context.Context, root string, env []string, stdin []byte, args ...string) ([]byte, error) {
	var (
		out []byte
		err error
	)
	for attempt := 0; attempt < checkpointLockRetries; attempt++ {
		out, err = r.runWithStdin(ctx, root, env, stdin, args...)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "lock") {
			return out, err
		}
		if attempt == checkpointLockRetries-1 {
			break
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(checkpointLockBackoff):
		}
	}
	return out, err
}

// resolveCheckpointRev 将 refs/mcode/checkpoints/* 引用解析为对应提交哈希，其余修订名原样返回。
func (r *Runner) resolveCheckpointRev(ctx context.Context, root, rev string) (string, error) {
	if rev == "" || !isCheckpointRef(rev) {
		return rev, nil
	}
	return r.RevParse(ctx, root, rev)
}

// syncInfoFile 将用户仓库的 info 文件同步到检查点仓库；内容相同时跳过写入，源文件不存在时移除目标文件。
func syncInfoFile(src, dst string) error {
	srcData, srcErr := os.ReadFile(src)
	if os.IsNotExist(srcErr) {
		if _, err := os.Stat(dst); err == nil {
			_ = os.Remove(dst)
		}
		return nil
	}
	if srcErr != nil {
		return srcErr
	}
	if dstData, dstErr := os.ReadFile(dst); dstErr == nil && bytes.Equal(srcData, dstData) {
		return nil
	}
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dstDir, ".sync-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(srcData); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return err
	}
	success = true
	return nil
}

// gitPath 解析用户仓库指定 git 内部路径（如 info/sparse-checkout）的绝对路径。
func (r *Runner) gitPath(ctx context.Context, root, rel string) (string, error) {
	out, err := r.run(ctx, root, nil, "rev-parse", "--git-path", rel)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("empty git-path for %s", rel)
	}
	if !filepath.IsAbs(p) {
		base := root
		if absRoot, absErr := filepath.Abs(root); absErr == nil {
			base = absRoot
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), nil
}
