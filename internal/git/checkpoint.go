package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCheckpointRetention 为每个线程默认保留的最新检查点数量。
const DefaultCheckpointRetention = 50

// ErrCheckpointPruned 表示该轮检查点已被自动清理或删除，无法回退。
var ErrCheckpointPruned = errors.New("该轮检查点已被清理，无法回退")

// checkpointsRefPrefix 返回某个线程全部检查点引用的公共前缀。
// 命名约定：refs/mcode/checkpoints/<threadID>/
func checkpointsRefPrefix(threadID string) string {
	return fmt.Sprintf("refs/mcode/checkpoints/%s/", threadID)
}

// CheckpointRef 根据 threadID 与 turnID 构造规范的检查点引用名称。
// 命名约定：refs/mcode/checkpoints/<threadID>/<turnID>
func CheckpointRef(threadID, turnID string) string {
	return checkpointsRefPrefix(threadID) + turnID
}

// checkpointIndexConfig 是创建检查点时全部 git 命令统一附加的临时 index 配置（-c 前缀）：
//   - core.fsmonitor / core.untrackedCache：临时 index 不需要用户仓库的缓存加速，
//     关闭后避免临时 index 写入与用户仓库缓存（fsmonitor token、untracked 缓存）互相干扰；
//   - core.splitIndex / splitIndex.sharedIndexExpire：core.splitIndex 影响 git 写出的每个
//     index，不关闭时写临时 index 可能让用户 .git/index 依赖的 sharedindex.* 过期；
//   - core.longpaths：Windows 上即使仓库未开启长路径也允许处理超长路径（其他平台无影响）；
//   - sparse.expectFilesOutsideOfPatterns：稀疏检出仓库里允许工作区存在模式之外的文件。
//
// 参考 t3code apps/server/src/vcs/GitVcsDriver.ts 的 captureCheckpoint（indexConfig 与 longPaths）。
var checkpointIndexConfig = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.splitIndex=false",
	"-c", "splitIndex.sharedIndexExpire=never",
	"-c", "core.untrackedCache=false",
	"-c", "core.longpaths=true",
	"-c", "sparse.expectFilesOutsideOfPatterns=false",
}

// checkpointArgs 保留原样字节并拼接临时 index 配置与额外参数。
func checkpointArgs(extra ...string) []string {
	return checkpointArgsWithLineEndings(false, extra...)
}

func checkpointArgsWithLineEndings(normalized bool, extra ...string) []string {
	args := make([]string, 0, 4+len(checkpointIndexConfig)+len(extra))
	if !normalized {
		args = append(args, "-c", "core.autocrlf=false", "-c", "core.safecrlf=false")
	}
	args = append(args, checkpointIndexConfig...)
	return append(args, extra...)
}

// checkpointLockRetries 与 checkpointLockBackoff 定义了引用写入遇到锁文件占用时的重试策略。
const (
	checkpointLockRetries = 3
	checkpointLockBackoff = 50 * time.Millisecond
)

// runWithLockRetry 执行一条 git 命令，仅在错误文本包含 "lock"（index.lock/ref lock 被占用）时
// 最多重试 checkpointLockRetries 次；其他错误立即返回，不做掩盖。
func (r *Runner) runWithLockRetry(ctx context.Context, root string, env []string, args ...string) ([]byte, error) {
	var (
		out []byte
		err error
	)
	for attempt := 0; attempt < checkpointLockRetries; attempt++ {
		out, err = r.run(ctx, root, env, args...)
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

// sparseCheckoutEnabled 判断仓库是否启用了稀疏检出。
func (r *Runner) sparseCheckoutEnabled(ctx context.Context, root string) bool {
	out, err := r.run(ctx, root, nil, "config", "--bool", "core.sparseCheckout")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// sparseCheckoutConeEnabled 判断仓库是否启用了 cone 模式稀疏检出。
func (r *Runner) sparseCheckoutConeEnabled(ctx context.Context, root string) bool {
	out, err := r.run(ctx, root, nil, "config", "--bool", "core.sparseCheckoutCone")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// headCommit 解析用户仓库当前 HEAD 的提交哈希；空仓库时返回 ("", false)。
func (r *Runner) headCommit(ctx context.Context, root string) (string, bool) {
	out, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(string(out))
	return sha, sha != ""
}

// hasCommits 检查指定仓库根目录下是否已存在至少一个提交（即 HEAD 是否有效）。
func (r *Runner) hasCommits(ctx context.Context, root string) bool {
	_, ok := r.headCommit(ctx, root)
	return ok
}

// Checkpoint 在不修改用户的暂存区（index）、HEAD 和工作区的前提下，
// 将当前工作区的全部文件状态（含未跟踪但未被 .gitignore 忽略的新文件）快照为一个新的 commit，
// 并将其更新到指定的 ref（通常由 CheckpointRef 生成）。
func (r *Runner) Checkpoint(ctx context.Context, root, ref, message string) (string, error) {
	return r.checkpoint(ctx, root, ref, message, false)
}

// CheckpointNormalized 按用户的换行配置规范化，结果与 git add 一致，用于与提交对象做 merge-tree 的场景。
func (r *Runner) CheckpointNormalized(ctx context.Context, root, ref, message string) (string, error) {
	return r.checkpoint(ctx, root, ref, message, true)
}

func (r *Runner) checkpoint(ctx context.Context, root, ref, message string, normalized bool) (string, error) {
	var storeDir string
	if isCheckpointRef(ref) {
		var storeErr error
		storeDir, storeErr = r.checkpointStore(ctx, root)
		if storeErr != nil {
			return "", storeErr
		}
	}
	useStore := storeDir != ""

	// HEAD 的 SHA 与稀疏检出配置先在用户仓库里解析出来。
	headSHA, hasHead := r.headCommit(ctx, root)
	sparse := hasHead && r.sparseCheckoutEnabled(ctx, root)
	var extraStoreCfg []string
	if useStore {
		commonDir := filepath.Dir(filepath.Dir(storeDir))
		if err := syncInfoFile(filepath.Join(commonDir, "info", "exclude"), filepath.Join(storeDir, "info", "exclude")); err != nil {
			return "", fmt.Errorf("sync checkpoint exclude: %w", err)
		}
		if sparse {
			sparseCone := r.sparseCheckoutConeEnabled(ctx, root)
			if srcSparse, gpErr := r.gitPath(ctx, root, "info/sparse-checkout"); gpErr == nil {
				if err := syncInfoFile(srcSparse, filepath.Join(storeDir, "info", "sparse-checkout")); err != nil {
					return "", fmt.Errorf("sync checkpoint sparse-checkout: %w", err)
				}
			}
			coneVal := "false"
			if sparseCone {
				coneVal = "true"
			}
			extraStoreCfg = []string{"-c", "core.sparseCheckout=true", "-c", "core.sparseCheckoutCone=" + coneVal}
		}
	}

	args := func(extra ...string) []string {
		if len(extraStoreCfg) > 0 {
			combined := make([]string, 0, len(extraStoreCfg)+len(extra))
			combined = append(combined, extraStoreCfg...)
			combined = append(combined, extra...)
			return checkpointArgsWithLineEndings(normalized, combined...)
		}
		return checkpointArgsWithLineEndings(normalized, extra...)
	}
	// 创建隔离的临时目录存放专用的 GIT_INDEX_FILE，用完后立即清理
	tmpDir, err := os.MkdirTemp("", "mcode-git-checkpoint-*")
	if err != nil {
		return "", fmt.Errorf("create checkpoint temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpIndex := filepath.Join(tmpDir, "index")
	cpEnv := []string{
		"GIT_INDEX_FILE=" + tmpIndex,
		"GIT_AUTHOR_NAME=mcode",
		"GIT_AUTHOR_EMAIL=mcode@localhost",
		"GIT_COMMITTER_NAME=mcode",
		"GIT_COMMITTER_EMAIL=mcode@localhost",
	}
	if useStore {
		cpEnv = append(checkpointEnv(storeDir, root), cpEnv...)
	}

	// 1. 若仓库已有提交，先将 HEAD 的树载入临时 index；若为空仓库则跳过
	if hasHead {
		var readTreeArgs []string
		if sparse {
			readTreeArgs = args("-c", "index.sparse=true", "read-tree", "--reset", headSHA)
		} else {
			readTreeArgs = args("read-tree", headSHA)
		}
		if _, err := r.run(ctx, root, cpEnv, readTreeArgs...); err != nil {
			return "", fmt.Errorf("checkpoint read-tree: %w", err)
		}
	}

	// 2. 将工作区变更同步进临时 index（遵守 .gitignore）
	addBase := args("add")
	addTail := []string{"-A", "--", "."}
	stageArgs := func(withSparse bool) []string {
		args := append([]string{}, addBase...)
		if withSparse {
			args = append(args, "--sparse")
		}
		return append(args, addTail...)
	}
	_, addErr := r.run(ctx, root, cpEnv, stageArgs(sparse)...)
	if addErr != nil && sparse && strings.Contains(addErr.Error(), "--sparse") {
		// git < 2.35 的 add 不认识 --sparse：退回普通 add。模式外的已跟踪文件仍有
		// skip-worktree 保护，只是模式外新建的未跟踪文件不会进入快照。
		_, addErr = r.run(ctx, root, cpEnv, stageArgs(false)...)
	}
	if addErr != nil {
		return "", fmt.Errorf("checkpoint add: %w", addErr)
	}

	// 3. 基于临时 index 生成 tree 对象
	writeTreeArgs := args("write-tree")
	treeOut, err := r.run(ctx, root, cpEnv, writeTreeArgs...)
	if err != nil {
		return "", fmt.Errorf("checkpoint write-tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	// 4. 将 tree 提交为 commit 对象，若存在 HEAD 则以 HEAD 为父提交
	if message == "" {
		message = "mcode checkpoint"
	}
	commitArgs := args("commit-tree", tree, "-m", message)
	if hasHead {
		commitArgs = append(commitArgs, "-p", headSHA)
	}

	commitOut, err := r.run(ctx, root, cpEnv, commitArgs...)
	if err != nil {
		return "", fmt.Errorf("checkpoint commit-tree: %w", err)
	}
	commit := strings.TrimSpace(string(commitOut))

	// 5. 更新目标引用指向该 commit（refs 下的锁文件可能被并发 git 进程短暂占用，做一次短重试）
	updateRefArgs := args("update-ref", ref, commit)
	if _, err := r.runWithLockRetry(ctx, root, cpEnv, updateRefArgs...); err != nil {
		return "", fmt.Errorf("checkpoint update-ref %s: %w", ref, err)
	}

	// 检查点成功写入后，尽力按保留策略清理历史检查点，失败只忽略
	if isCheckpointRef(ref) {
		trimmed := strings.TrimPrefix(ref, checkpointRefRootPrefix)
		parts := strings.Split(trimmed, "/")
		if len(parts) > 0 && parts[0] != "" {
			_ = r.PruneCheckpoints(ctx, root, parts[0], DefaultCheckpointRetention)
		}
	}

	return commit, nil
}

// PruneCheckpoints 列出指定线程名下的全部检查点引用，按创建时间倒序保留最新的 keep 个，其余引用予以删除。
// keep <= 0 时不做任何操作；git 调用方式沿用本文件 Runner 写法。
func (r *Runner) PruneCheckpoints(ctx context.Context, root, threadID string, keep int) error {
	if keep <= 0 {
		return nil
	}
	var env []string
	useStore := false
	if storeDir, ok := r.existingCheckpointStore(ctx, root); ok {
		env = checkpointEnv(storeDir, root)
		useStore = true
	}
	prefix := checkpointsRefPrefix(threadID)
	out, err := r.run(ctx, root, env, "for-each-ref", "--sort=-creatordate", "--format=%(refname)", prefix)
	if err != nil {
		return fmt.Errorf("list checkpoints %s: %w", prefix, err)
	}

	var refs []string
	for _, ref := range strings.Split(string(out), "\n") {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			refs = append(refs, ref)
		}
	}

	if len(refs) <= keep {
		return nil
	}

	var stdin bytes.Buffer
	for _, ref := range refs[keep:] {
		fmt.Fprintf(&stdin, "delete %s\n", ref)
	}
	if _, err := r.runWithStdinLockRetry(ctx, root, env, stdin.Bytes(), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("delete pruned checkpoint refs %s: %w", prefix, err)
	}
	if useStore {
		_, _ = r.run(ctx, root, env, "gc", "--auto", "--quiet")
	}
	return nil
}

// DeleteCheckpoints 删除指定线程名下的全部检查点引用（refs/mcode/checkpoints/<threadID>/...）。
// 在会话被永久删除时调用，避免游离 ref 随长期使用不断累积；该线程没有任何检查点时返回 nil。
func (r *Runner) DeleteCheckpoints(ctx context.Context, root, threadID string) error {
	var env []string
	if storeDir, ok := r.existingCheckpointStore(ctx, root); ok {
		env = checkpointEnv(storeDir, root)
	}
	prefix := checkpointsRefPrefix(threadID)
	out, err := r.run(ctx, root, env, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return fmt.Errorf("list checkpoints %s: %w", prefix, err)
	}
	var stdin bytes.Buffer
	for _, ref := range strings.Split(string(out), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		fmt.Fprintf(&stdin, "delete %s\n", ref)
	}
	if stdin.Len() == 0 {
		return nil
	}
	if _, err := r.runWithStdinLockRetry(ctx, root, env, stdin.Bytes(), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("delete checkpoint refs %s: %w", prefix, err)
	}
	return nil
}
