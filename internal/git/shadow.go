package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotShadowCandidate 表示目录位于真实 Git 仓库或其工作树内，不应作为影子仓库候选。
var ErrNotShadowCandidate = errors.New("directory is already inside a git repository")

// ErrShadowTooLarge 表示将纳入影子仓库的文件数量或总大小超过上限。
var ErrShadowTooLarge = errors.New("shadow repository size limit exceeded")

// ShadowMaxFiles 与 ShadowMaxBytes 分别表示影子仓库允许纳入的最大文件数与最大字节数。
var (
	ShadowMaxFiles       = 20000
	ShadowMaxBytes int64 = 512 * 1024 * 1024 // 512 MiB
)

// normalizeDir 将路径规范化为绝对路径并解析符号链接（如 macOS 下 /var -> /private/var）。
// 若 EvalSymlinks 失败，则回退至 filepath.Abs 的结果。
func normalizeDir(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(abs)
	}
	if realPath, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(realPath)
	}
	return abs
}

// shadowGitDir 返回 ShadowRoot 下以项目绝对路径 sha256 的前 16 位十六进制命名、后缀为 .git 的目录。
func (r *Runner) shadowGitDir(projectRoot string) string {
	if r == nil || r.ShadowRoot == "" || projectRoot == "" {
		return ""
	}
	abs := normalizeDir(projectRoot)
	h := sha256.Sum256([]byte(abs))
	name := hex.EncodeToString(h[:])[:16] + ".git"
	return filepath.Join(r.ShadowRoot, name)
}

// shadowEnv 从 dir 逐级向上查找祖先目录。
// 遇到存在 .git（文件或目录）的目录返回 nil（真实仓库或 worktree 已指向自有环境）；
// 遇到某个祖先在 ShadowRoot 下已有影子仓库目录，返回 GIT_DIR 与 GIT_WORK_TREE。
func (r *Runner) shadowEnv(dir string) []string {
	if r == nil || r.ShadowRoot == "" || dir == "" {
		return nil
	}
	cur := normalizeDir(dir)
	cleanShadowRoot := normalizeDir(r.ShadowRoot)
	for {
		if cur == cleanShadowRoot {
			return nil
		}
		gitPath := filepath.Join(cur, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			// 遇到存在 .git（文件或目录）的目录就返回 nil
			return nil
		}

		sDir := r.shadowGitDir(cur)
		if sDir != "" {
			if fi, err := os.Stat(sDir); err == nil && fi.IsDir() {
				return []string{
					"GIT_DIR=" + sDir,
					"GIT_WORK_TREE=" + cur,
				}
			}
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil
}

// EnsureShadow 确保 projectDir 拥有一个可用的影子 Git 仓库。
// 若 ShadowRoot 未配置则报错；若 projectDir 位于真实 Git 仓库内返回 ErrNotShadowCandidate；
// 影子仓库已存在时直接返回；否则执行 bare 初始化、配置工作树、写入默认排除规则、检查文件规模，
// 并以临时 index 生成初始提交并将 HEAD 设为 refs/heads/main。
func (r *Runner) EnsureShadow(ctx context.Context, projectDir string) (gitDir string, err error) {
	if r == nil || r.ShadowRoot == "" {
		return "", errors.New("shadow root is not configured")
	}

	absProjectDir := normalizeDir(projectDir)

	fi, err := os.Stat(absProjectDir)
	if err != nil {
		return "", fmt.Errorf("stat project dir: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", absProjectDir)
	}

	// 检查 projectDir 是否位于真实 Git 仓库内（自身或其任何祖先包含 .git）
	for cur := absProjectDir; ; {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return "", ErrNotShadowCandidate
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}

	// 若影子仓库已存在则直接返回
	gitDir = r.shadowGitDir(absProjectDir)
	if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
		return gitDir, nil
	}

	if err := os.MkdirAll(r.ShadowRoot, 0755); err != nil {
		return "", fmt.Errorf("create shadow root: %w", err)
	}

	// 1. 初始化 bare 仓库
	if _, err := r.run(ctx, absProjectDir, nil, "init", "--bare", gitDir); err != nil {
		return "", fmt.Errorf("shadow git init: %w", err)
	}

	// 保证后续发生任何错误时，清理刚创建的影子目录
	dirToClean := gitDir
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dirToClean)
		}
	}()

	baseEnv := []string{
		"GIT_DIR=" + gitDir,
		"GIT_WORK_TREE=" + absProjectDir,
	}

	// 2. 设置 core.bare=false 与 core.worktree=<projectDir>
	if _, err := r.run(ctx, absProjectDir, baseEnv, "config", "core.bare", "false"); err != nil {
		return "", fmt.Errorf("shadow config core.bare: %w", err)
	}
	if _, err := r.run(ctx, absProjectDir, baseEnv, "config", "core.worktree", absProjectDir); err != nil {
		return "", fmt.Errorf("shadow config core.worktree: %w", err)
	}

	// 3. 在 <gitDir>/info/exclude 写入默认忽略规则
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		return "", fmt.Errorf("shadow mkdir info: %w", err)
	}
	defaultExcludes := []string{
		"node_modules/",
		".venv/",
		"venv/",
		"__pycache__/",
		"dist/",
		"build/",
		"target/",
		".next/",
		".cache/",
		"*.log",
		".DS_Store",
		"",
	}
	if err := os.WriteFile(filepath.Join(infoDir, "exclude"), []byte(strings.Join(defaultExcludes, "\n")), 0644); err != nil {
		return "", fmt.Errorf("write shadow exclude: %w", err)
	}

	// 4. 规模检查：用 ls-files -o --exclude-standard 统计将纳入的文件数和总大小
	out, err := r.run(ctx, absProjectDir, baseEnv, "ls-files", "-o", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("shadow ls-files check: %w", err)
	}

	var fileCount int64
	var totalBytes int64
	if len(out) > 0 {
		items := bytes.Split(out, []byte{0})
		for _, item := range items {
			if len(item) == 0 {
				continue
			}
			fileCount++
			filePath := filepath.Join(absProjectDir, string(item))
			if lfi, statErr := os.Lstat(filePath); statErr == nil {
				totalBytes += lfi.Size()
			}
		}
	}

	if (ShadowMaxFiles > 0 && fileCount > int64(ShadowMaxFiles)) || (ShadowMaxBytes > 0 && totalBytes > ShadowMaxBytes) {
		return "", fmt.Errorf("%w: files=%d (limit %d), bytes=%d (limit %d)", ErrShadowTooLarge, fileCount, ShadowMaxFiles, totalBytes, ShadowMaxBytes)
	}

	// 5. 使用临时 index 执行 add -A、write-tree、commit-tree 生成初始 commit 并更新引用
	tmpDir, err := os.MkdirTemp("", "mcode-git-shadow-*")
	if err != nil {
		return "", fmt.Errorf("create shadow temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpIndex := filepath.Join(tmpDir, "index")
	shadowCommitEnv := []string{
		"GIT_DIR=" + gitDir,
		"GIT_WORK_TREE=" + absProjectDir,
		"GIT_INDEX_FILE=" + tmpIndex,
		"GIT_AUTHOR_NAME=mcode",
		"GIT_AUTHOR_EMAIL=mcode@localhost",
		"GIT_COMMITTER_NAME=mcode",
		"GIT_COMMITTER_EMAIL=mcode@localhost",
	}

	if _, err := r.run(ctx, absProjectDir, shadowCommitEnv, checkpointArgs("add", "-A", "--", ".")...); err != nil {
		return "", fmt.Errorf("shadow checkpoint add: %w", err)
	}

	treeOut, err := r.run(ctx, absProjectDir, shadowCommitEnv, checkpointArgs("write-tree")...)
	if err != nil {
		return "", fmt.Errorf("shadow write-tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	commitArgs := checkpointArgs("commit-tree", tree, "-m", "Initial shadow commit")
	commitOut, err := r.run(ctx, absProjectDir, shadowCommitEnv, commitArgs...)
	if err != nil {
		return "", fmt.Errorf("shadow commit-tree: %w", err)
	}
	commit := strings.TrimSpace(string(commitOut))

	updateRefArgs := checkpointArgs("update-ref", "refs/heads/main", commit)
	if _, err := r.run(ctx, absProjectDir, shadowCommitEnv, updateRefArgs...); err != nil {
		return "", fmt.Errorf("shadow update-ref: %w", err)
	}

	if _, err := r.run(ctx, absProjectDir, baseEnv, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return "", fmt.Errorf("shadow symbolic-ref HEAD: %w", err)
	}

	success = true
	return gitDir, nil
}

// IsShadow 判断 dir 是否由影子仓库管理（即 shadowEnv 非空时返回 true）。
func (r *Runner) IsShadow(dir string) bool {
	return len(r.shadowEnv(dir)) > 0
}

// RemoveShadow 清理 projectDir 对应的影子仓库目录。
func (r *Runner) RemoveShadow(projectDir string) error {
	if r == nil || r.ShadowRoot == "" || projectDir == "" {
		return nil
	}
	abs := normalizeDir(projectDir)
	gitDir := r.shadowGitDir(abs)
	if gitDir == "" {
		return nil
	}
	return os.RemoveAll(gitDir)
}
