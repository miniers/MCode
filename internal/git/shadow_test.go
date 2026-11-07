package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShadow_EnsureShadowAndRepoRoot 测试场景 (a)：
// 对非 git 目录调用 EnsureShadow 后，项目目录里没有 .git；
// RepoRoot(项目目录) 返回项目目录；rev-parse HEAD 成功。
func TestShadow_EnsureShadowAndRepoRoot(t *testing.T) {
	shadowRoot := t.TempDir()
	projectDir := t.TempDir()

	// 在非 git 项目中写入测试文件
	if err := os.WriteFile(filepath.Join(projectDir, "app.txt"), []byte("shadow project content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	runner := NewRunner("", 4)
	runner.ShadowRoot = shadowRoot
	ctx := context.Background()

	gitDir, err := runner.EnsureShadow(ctx, projectDir)
	if err != nil {
		t.Fatalf("EnsureShadow failed: %v", err)
	}
	if gitDir == "" {
		t.Fatalf("expected non-empty gitDir")
	}

	// 验证项目目录内绝不出现 .git
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected no .git in projectDir, got stat err: %v", err)
	}

	// 验证 IsShadow 状态
	if !runner.IsShadow(projectDir) {
		t.Fatalf("expected IsShadow(projectDir) to be true")
	}

	// 验证 RepoRoot(项目目录) 返回项目目录绝对路径
	cleanProjectDir := filepath.Clean(projectDir)
	if resolved, err := filepath.EvalSymlinks(cleanProjectDir); err == nil {
		cleanProjectDir = filepath.Clean(resolved)
	}
	root, err := runner.RepoRoot(ctx, projectDir)
	if err != nil {
		t.Fatalf("RepoRoot failed: %v", err)
	}
	if root != cleanProjectDir {
		t.Fatalf("expected RepoRoot %s, got %s", cleanProjectDir, root)
	}

	// 验证在项目目录中执行 rev-parse HEAD 成功
	out, err := runner.run(ctx, projectDir, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD failed: %v", err)
	}
	headCommit := strings.TrimSpace(string(out))
	if len(headCommit) != 40 {
		t.Fatalf("expected 40-char commit hash, got %q", headCommit)
	}

	// 验证再次调用 EnsureShadow 幂等返回已有 gitDir
	secondGitDir, err := runner.EnsureShadow(ctx, projectDir)
	if err != nil {
		t.Fatalf("second EnsureShadow failed: %v", err)
	}
	if secondGitDir != gitDir {
		t.Fatalf("expected same gitDir %s, got %s", gitDir, secondGitDir)
	}
}

// TestShadow_CheckpointAndWorktree 测试场景 (b)：
// 在项目目录上调用 Checkpoint 成功；WorktreeAdd 从该 commit 建 worktree 成功，
// worktree 内的文件内容正确；在 worktree 目录里执行 git 命令走 worktree 自身的 .git，不注入影子环境。
func TestShadow_CheckpointAndWorktree(t *testing.T) {
	shadowRoot := t.TempDir()
	projectDir := t.TempDir()

	file1 := filepath.Join(projectDir, "file1.txt")
	if err := os.WriteFile(file1, []byte("content v1"), 0644); err != nil {
		t.Fatalf("write file1: %v", err)
	}

	runner := NewRunner("", 4)
	runner.ShadowRoot = shadowRoot
	ctx := context.Background()

	if _, err := runner.EnsureShadow(ctx, projectDir); err != nil {
		t.Fatalf("EnsureShadow failed: %v", err)
	}

	// 修改文件并创建 checkpoint
	if err := os.WriteFile(file1, []byte("content v2 checkpoint"), 0644); err != nil {
		t.Fatalf("update file1: %v", err)
	}
	file2 := filepath.Join(projectDir, "file2.txt")
	if err := os.WriteFile(file2, []byte("file 2 content"), 0644); err != nil {
		t.Fatalf("write file2: %v", err)
	}

	commit, err := runner.Checkpoint(ctx, projectDir, "refs/mcode/checkpoints/thread-1/turn-1", "turn 1 snapshot")
	if err != nil {
		t.Fatalf("Checkpoint failed: %v", err)
	}
	if commit == "" {
		t.Fatalf("expected non-empty commit hash")
	}

	// 从该 commit 创建 worktree
	wtDir := filepath.Join(t.TempDir(), "swarm-wt-1")
	if err := runner.WorktreeAdd(ctx, projectDir, wtDir, "", commit); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}

	// 检查 worktree 内文件内容正确
	wtFile1, err := os.ReadFile(filepath.Join(wtDir, "file1.txt"))
	if err != nil {
		t.Fatalf("read wt file1: %v", err)
	}
	if string(wtFile1) != "content v2 checkpoint" {
		t.Fatalf("unexpected wt file1 content: %s", string(wtFile1))
	}

	wtFile2, err := os.ReadFile(filepath.Join(wtDir, "file2.txt"))
	if err != nil {
		t.Fatalf("read wt file2: %v", err)
	}
	if string(wtFile2) != "file 2 content" {
		t.Fatalf("unexpected wt file2 content: %s", string(wtFile2))
	}

	// worktree 目录包含 .git 文件（非目录）
	wtGitInfo, err := os.Stat(filepath.Join(wtDir, ".git"))
	if err != nil {
		t.Fatalf("stat wt .git: %v", err)
	}
	if wtGitInfo.IsDir() {
		t.Fatalf("expected worktree .git to be a file, not a directory")
	}

	// 在 worktree 目录里，shadowEnv 应返回 nil，不注入影子环境
	if env := runner.shadowEnv(wtDir); env != nil {
		t.Fatalf("expected shadowEnv(wtDir) to be nil, got: %v", env)
	}
	if runner.IsShadow(wtDir) {
		t.Fatalf("expected IsShadow(wtDir) to be false")
	}

	// 在 worktree 目录里执行 RepoRoot 走 worktree 自身的 .git 正常解析
	wtRoot, err := runner.RepoRoot(ctx, wtDir)
	if err != nil {
		t.Fatalf("wt RepoRoot failed: %v", err)
	}
	cleanWtDir := filepath.Clean(wtDir)
	if resolved, err := filepath.EvalSymlinks(cleanWtDir); err == nil {
		cleanWtDir = filepath.Clean(resolved)
	}
	if wtRoot != cleanWtDir {
		t.Fatalf("expected wt RepoRoot %s, got %s", cleanWtDir, wtRoot)
	}
}

// TestShadow_RealGitRepo 测试场景 (c)：
// 对位于真实 git 仓库内的目录返回 ErrNotShadowCandidate，并且 shadowEnv 返回 nil。
func TestShadow_RealGitRepo(t *testing.T) {
	shadowRoot := t.TempDir()
	realRepoDir := t.TempDir()

	runner := NewRunner("", 4)
	runner.ShadowRoot = shadowRoot
	ctx := context.Background()

	// 初始化一个真实 git 仓库
	if _, err := runner.run(ctx, realRepoDir, nil, "init"); err != nil {
		t.Fatalf("git init real repo failed: %v", err)
	}
	subDir := filepath.Join(realRepoDir, "sub", "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}

	// 对真实仓库根目录调用 EnsureShadow 应拒绝
	_, err := runner.EnsureShadow(ctx, realRepoDir)
	if !errors.Is(err, ErrNotShadowCandidate) {
		t.Fatalf("expected ErrNotShadowCandidate for real repo root, got: %v", err)
	}
	if env := runner.shadowEnv(realRepoDir); env != nil {
		t.Fatalf("expected shadowEnv(realRepoDir) to be nil, got: %v", env)
	}

	// 对真实仓库的子目录调用 EnsureShadow 应拒绝
	_, err = runner.EnsureShadow(ctx, subDir)
	if !errors.Is(err, ErrNotShadowCandidate) {
		t.Fatalf("expected ErrNotShadowCandidate for real repo subdir, got: %v", err)
	}
	if env := runner.shadowEnv(subDir); env != nil {
		t.Fatalf("expected shadowEnv(subDir) to be nil, got: %v", env)
	}
}

// TestShadow_TooLarge 测试场景 (d)：
// 临时把 ShadowMaxFiles 调成很小的值时返回 ErrShadowTooLarge，并且影子目录已被删除。
func TestShadow_TooLarge(t *testing.T) {
	shadowRoot := t.TempDir()
	projectDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(projectDir, "a.txt"), []byte("file a"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "b.txt"), []byte("file b"), 0644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	runner := NewRunner("", 4)
	runner.ShadowRoot = shadowRoot
	ctx := context.Background()

	// 1. 测试文件数超过上限
	origMaxFiles := ShadowMaxFiles
	ShadowMaxFiles = 1
	defer func() {
		ShadowMaxFiles = origMaxFiles
	}()

	_, err := runner.EnsureShadow(ctx, projectDir)
	if !errors.Is(err, ErrShadowTooLarge) {
		t.Fatalf("expected ErrShadowTooLarge, got: %v", err)
	}

	// 确认错误信息中包含了实际数量
	if !strings.Contains(err.Error(), "files=2") {
		t.Fatalf("expected error message to contain actual file count (files=2), got: %v", err)
	}

	// 确认影子目录已被自动删除
	expectedGitDir := runner.shadowGitDir(projectDir)
	if _, err := os.Stat(expectedGitDir); !os.IsNotExist(err) {
		t.Fatalf("expected shadow gitDir to be deleted, got stat err: %v", err)
	}

	// 2. 恢复文件数上限，测试字节数超过上限
	ShadowMaxFiles = origMaxFiles
	origMaxBytes := ShadowMaxBytes
	ShadowMaxBytes = 5 // 限制为 5 字节，文件大小超过该上限
	defer func() {
		ShadowMaxBytes = origMaxBytes
	}()

	_, byteErr := runner.EnsureShadow(ctx, projectDir)
	if !errors.Is(byteErr, ErrShadowTooLarge) {
		t.Fatalf("expected ErrShadowTooLarge for bytes limit, got: %v", byteErr)
	}
	if _, err := os.Stat(expectedGitDir); !os.IsNotExist(err) {
		t.Fatalf("expected shadow gitDir to be deleted on bytes limit, got stat err: %v", err)
	}
}

// TestShadow_DefaultRunnerNoShadow 测试场景 (e)：
// ShadowRoot 为空的 Runner 对非 git 目录执行 RepoRoot 仍返回 ErrNotRepo。
func TestShadow_DefaultRunnerNoShadow(t *testing.T) {
	nonGitDir := t.TempDir()
	runner := NewRunner("", 4) // ShadowRoot 默认为空
	ctx := context.Background()

	// 默认 Runner 对非 git 目录返回 ErrNotRepo
	_, err := runner.RepoRoot(ctx, nonGitDir)
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("expected ErrNotRepo from default runner on non-git dir, got: %v", err)
	}

	// 未配置 ShadowRoot 时 EnsureShadow 应报错
	_, err = runner.EnsureShadow(ctx, nonGitDir)
	if err == nil {
		t.Fatalf("expected EnsureShadow to fail when ShadowRoot is empty")
	}

	// 验证 RemoveShadow 的安全清理
	shadowRunner := NewRunner("", 4)
	shadowRunner.ShadowRoot = t.TempDir()
	if _, err := shadowRunner.EnsureShadow(ctx, nonGitDir); err != nil {
		t.Fatalf("EnsureShadow failed: %v", err)
	}
	if !shadowRunner.IsShadow(nonGitDir) {
		t.Fatalf("expected IsShadow to be true")
	}
	if err := shadowRunner.RemoveShadow(nonGitDir); err != nil {
		t.Fatalf("RemoveShadow failed: %v", err)
	}
	if shadowRunner.IsShadow(nonGitDir) {
		t.Fatalf("expected IsShadow to be false after RemoveShadow")
	}
}
