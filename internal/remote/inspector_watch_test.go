package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

func watchTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func runWatchTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(out))
	}
}

func watchTestWatcherCount(b *MemoryBackend) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.watchers)
}

// 同一工作区的两个会话共用后端缓存的同一个 watcher。
func TestInspectorWatchSharedWorkspace(t *testing.T) {
	backend, threadID, repoDir, _ := setupGitOpsBackend(t)
	ctx, cancel := watchTestContext(t)
	defer cancel()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: watchThreadProjectID(t, backend, threadID),
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	if _, err := backend.WorkspaceGeneration(ctx, threadID); err != nil {
		t.Fatalf("WorkspaceGeneration failed: %v", err)
	}
	if _, err := backend.WorkspaceGeneration(ctx, res.ThreadID); err != nil {
		t.Fatalf("WorkspaceGeneration second thread failed: %v", err)
	}
	if got := watchTestWatcherCount(backend); got != 1 {
		t.Fatalf("同一工作区应只有一个 watcher，得到 %d", got)
	}
	resolved, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	backend.mu.Lock()
	w1 := backend.watchers[resolved]
	backend.mu.Unlock()
	if w1 == nil {
		t.Fatalf("缺少 %s 的 watcher", resolved)
	}
}

// watchThreadProjectID 从会话表里取项目 ID。
func watchThreadProjectID(t *testing.T, b *MemoryBackend, threadID string) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	td := b.threads[threadID]
	if td == nil {
		t.Fatalf("会话 %s 不存在", threadID)
	}
	return td.summary.ProjectID
}

// 切换 worktree 后使用新的工作区根与新的 watcher，旧根不再影响新根。
func TestInspectorWatchRootChange(t *testing.T) {
	backend, threadID, repoDir, _ := setupGitOpsBackend(t)
	ctx, cancel := watchTestContext(t)
	defer cancel()

	before, err := backend.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration before failed: %v", err)
	}
	if got := watchTestWatcherCount(backend); got != 1 {
		t.Fatalf("切换前应有一个 watcher，得到 %d", got)
	}

	worktreeDir := filepath.Join(t.TempDir(), "watch-worktree")
	runWatchTestGit(t, repoDir, "worktree", "add", "-b", "watch-wt", worktreeDir)
	if err := backend.GitSwitchWorktree(ctx, threadID, worktreeDir); err != nil {
		t.Fatalf("GitSwitchWorktree failed: %v", err)
	}
	after, err := backend.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration after failed: %v", err)
	}
	if after == before {
		t.Fatalf("切换 worktree 后代次没有变化")
	}
	if got := watchTestWatcherCount(backend); got != 2 {
		t.Fatalf("切换后应有两个 watcher，得到 %d", got)
	}

	if err := os.WriteFile(filepath.Join(worktreeDir, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	changed, err := backend.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration changed failed: %v", err)
	}
	if changed == after {
		t.Fatalf("工作区文件变化后代次没有变化")
	}
}

// 非 Git 普通目录也能取代次，文件写入后递增。
func TestInspectorWatchNonGitFiles(t *testing.T) {
	dir := t.TempDir()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	ctx, cancel := watchTestContext(t)
	defer cancel()

	projectID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "plain", Path: dir})
	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projectID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	before, err := backend.WorkspaceGeneration(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("plain\n"), 0644); err != nil {
		t.Fatalf("write plain.txt: %v", err)
	}
	after, err := backend.WorkspaceGeneration(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration after failed: %v", err)
	}
	if after == before {
		t.Fatalf("普通目录文件变化后代次没有变化")
	}
}

// 只提交空提交或只改 index、不动工作区文件时，代次也要变化。
func TestInspectorWatchIndexOnlyChange(t *testing.T) {
	backend, threadID, repoDir, _ := setupGitOpsBackend(t)
	ctx, cancel := watchTestContext(t)
	defer cancel()

	base, err := backend.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration base failed: %v", err)
	}
	runWatchTestGit(t, repoDir, "commit", "--allow-empty", "-m", "head only")
	headOnly, err := backend.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration after commit failed: %v", err)
	}
	if headOnly == base {
		t.Fatalf("只改 HEAD 时代次没有变化")
	}

	time.Sleep(20 * time.Millisecond)
	runWatchTestGit(t, repoDir, "rm", "--cached", "base.txt")
	indexOnly, err := backend.WorkspaceGeneration(ctx, threadID)
	if err != nil {
		t.Fatalf("WorkspaceGeneration after index change failed: %v", err)
	}
	if indexOnly == headOnly {
		t.Fatalf("只改 index 时代次没有变化")
	}
}

// 只移动分支引用（空提交）时，Git 元数据代次必须变化；直接调用代次计算，不依赖文件系统事件。
func TestGitMetadataGenerationTracksBranchRef(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}
	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		ctx, cancel := watchTestContext(t)
		defer cancel()
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, string(out))
		}
	}
	runGit("init")
	runGit("config", "user.name", "test-user")
	runGit("config", "user.email", "test@localhost")
	runGit("config", "core.autocrlf", "false")
	runGit("commit", "--allow-empty", "-m", "initial")

	ctx, cancel := watchTestContext(t)
	defer cancel()
	indexPath, err := git.NewRunner(gitBin, 1).IndexPath(ctx, repoDir)
	if err != nil {
		t.Fatalf("IndexPath failed: %v", err)
	}
	before := gitMetadataGeneration(indexPath)
	// 部分文件系统的 mtime 分辨率较粗，留出间隔再重写分支引用。
	time.Sleep(20 * time.Millisecond)
	runGit("commit", "--allow-empty", "-m", "second")
	after := gitMetadataGeneration(indexPath)
	if after == before {
		t.Fatal("空提交只改分支引用，元数据代次没有变化")
	}
}
