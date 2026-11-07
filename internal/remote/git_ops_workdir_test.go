package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// setupGitWorkDirBackend 建好带一次提交的 git 仓库、附带 store 的 backend、项目与会话，供工作目录持久化测试使用。
func setupGitWorkDirBackend(t *testing.T) (*MemoryBackend, *store.Store, string, string, func(args ...string) string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init")
	runGit("config", "user.name", "test-user")
	runGit("config", "user.email", "test@localhost")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("baseline\n"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	runGit("add", "base.txt")
	runGit("commit", "-m", "initial commit")
	runGit("checkout", "-b", "main-test")

	s, err := store.Open(filepath.Join(t.TempDir(), "workdir.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(context.Background(), s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{
		ID:   projID,
		Name: "workdir-project",
		Path: repoDir,
	})
	res, err := backend.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	return backend, s, res.ThreadID, repoDir, runGit
}

// setThreadProviderSession 把 provider 会话同时写进内存与 store，模拟切换前已有会话的状态。
func setThreadProviderSession(t *testing.T, b *MemoryBackend, s *store.Store, threadID, sessionID string) {
	t.Helper()
	if err := s.UpdateThreadSession(context.Background(), threadID, sessionID); err != nil {
		t.Fatalf("UpdateThreadSession: %v", err)
	}
	b.mu.Lock()
	b.threads[threadID].sessionID = sessionID
	b.mu.Unlock()
}

func mustStoreThread(t *testing.T, s *store.Store, threadID string) store.Thread {
	t.Helper()
	row, err := s.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("store.GetThread: %v", err)
	}
	return row
}

// 切到另一个 worktree 后 worktree_path 落库、session 清空（内存与 store 一致），重建 backend 后仍在新目录。
func TestSetThreadWorkDir_PersistsWorktreeAcrossReload(t *testing.T) {
	backend, st, threadID, _, runGit := setupGitWorkDirBackend(t)
	ctx := context.Background()
	setThreadProviderSession(t, backend, st, threadID, "sess-old")

	wtDir := filepath.Join(t.TempDir(), "wt-persist")
	runGit("worktree", "add", "-b", "persist-branch", wtDir)

	if err := backend.SetThreadWorkDir(threadID, wtDir); err != nil {
		t.Fatalf("SetThreadWorkDir failed: %v", err)
	}

	row := mustStoreThread(t, st, threadID)
	if !samePath(row.WorktreePath, wtDir) {
		t.Errorf("store worktree_path 应为 %q，实得 %q", wtDir, row.WorktreePath)
	}
	if !row.WorktreeRequested {
		t.Errorf("store worktree_requested 应为 true")
	}
	if row.SessionID != "" {
		t.Errorf("store session_id 应被清空，实得 %q", row.SessionID)
	}

	// 内存与 store 保持一致：workDir 更新、旧 session 不再持有。
	if dir, ok := backend.ThreadWorkDir(threadID); !ok || !samePath(dir, wtDir) {
		t.Errorf("内存 workDir 应为 %q，实得 %q (ok=%v)", wtDir, dir, ok)
	}
	backend.mu.Lock()
	memSession := backend.threads[threadID].sessionID
	memRequested := backend.threads[threadID].summary.WorktreeRequested
	backend.mu.Unlock()
	if memSession != "" {
		t.Errorf("内存 session 应被清空，实得 %q", memSession)
	}
	if !memRequested {
		t.Errorf("内存 worktreeRequested 应为 true")
	}

	// 重启：用同一个 store 重建 backend，会话仍在 worktree。
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	reloaded := NewMemoryBackend(hub)
	t.Cleanup(reloaded.Close)
	if err := reloaded.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore reload: %v", err)
	}
	if dir, ok := reloaded.ThreadWorkDir(threadID); !ok || !samePath(dir, wtDir) {
		t.Errorf("重载后 workDir 应为 %q，实得 %q (ok=%v)", wtDir, dir, ok)
	}
}

// 切回项目根目录后 worktree_path 置空，重建 backend 后工作目录回到项目根。
func TestSetThreadWorkDir_BackToProjectRootClearsStore(t *testing.T) {
	backend, st, threadID, repoDir, runGit := setupGitWorkDirBackend(t)
	ctx := context.Background()

	wtDir := filepath.Join(t.TempDir(), "wt-back")
	runGit("worktree", "add", "-b", "back-branch", wtDir)
	if err := backend.SetThreadWorkDir(threadID, wtDir); err != nil {
		t.Fatalf("SetThreadWorkDir(wt) failed: %v", err)
	}
	setThreadProviderSession(t, backend, st, threadID, "sess-in-wt")

	if err := backend.SetThreadWorkDir(threadID, repoDir); err != nil {
		t.Fatalf("SetThreadWorkDir(root) failed: %v", err)
	}

	row := mustStoreThread(t, st, threadID)
	if row.WorktreePath != "" {
		t.Errorf("切回项目根后 worktree_path 应为空，实得 %q", row.WorktreePath)
	}
	if row.WorktreeRequested {
		t.Errorf("切回项目根后 worktree_requested 应为 false")
	}
	if row.SessionID != "" {
		t.Errorf("切回项目根后 session_id 应被清空，实得 %q", row.SessionID)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	reloaded := NewMemoryBackend(hub)
	t.Cleanup(reloaded.Close)
	if err := reloaded.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore reload: %v", err)
	}
	if dir, ok := reloaded.ThreadWorkDir(threadID); !ok || !samePath(dir, repoDir) {
		t.Errorf("重载后 workDir 应为项目根 %q，实得 %q (ok=%v)", repoDir, dir, ok)
	}
}

// 切到与当前相同的路径不清 session、不改库。
func TestSetThreadWorkDir_SamePathKeepsSession(t *testing.T) {
	backend, st, threadID, repoDir, runGit := setupGitWorkDirBackend(t)

	// 当前工作目录就是项目根（workDir 为空时回落到项目路径）。
	setThreadProviderSession(t, backend, st, threadID, "sess-keep")
	if err := backend.SetThreadWorkDir(threadID, repoDir); err != nil {
		t.Fatalf("SetThreadWorkDir(root) failed: %v", err)
	}
	row := mustStoreThread(t, st, threadID)
	if row.SessionID != "sess-keep" {
		t.Errorf("同路径切换不应清 session，实得 %q", row.SessionID)
	}
	if row.WorktreePath != "" || row.WorktreeRequested {
		t.Errorf("同路径切换不应改库: path=%q requested=%v", row.WorktreePath, row.WorktreeRequested)
	}

	// 已在 worktree 中再切同一路径同样不清 session。
	wtDir := filepath.Join(t.TempDir(), "wt-same")
	runGit("worktree", "add", "-b", "same-branch", wtDir)
	if err := backend.SetThreadWorkDir(threadID, wtDir); err != nil {
		t.Fatalf("SetThreadWorkDir(wt) failed: %v", err)
	}
	setThreadProviderSession(t, backend, st, threadID, "sess-wt")
	if err := backend.SetThreadWorkDir(threadID, wtDir); err != nil {
		t.Fatalf("SetThreadWorkDir(wt again) failed: %v", err)
	}
	row = mustStoreThread(t, st, threadID)
	if row.SessionID != "sess-wt" {
		t.Errorf("同路径切换不应清 session，实得 %q", row.SessionID)
	}
	if !samePath(row.WorktreePath, wtDir) || !row.WorktreeRequested {
		t.Errorf("同路径切换不应改库: path=%q requested=%v", row.WorktreePath, row.WorktreeRequested)
	}
}

// 运行中的会话不允许切换，store 与内存都不变。
func TestSetThreadWorkDir_RunningThreadLeavesStoreUntouched(t *testing.T) {
	backend, st, threadID, repoDir, runGit := setupGitWorkDirBackend(t)

	wtDir := filepath.Join(t.TempDir(), "wt-running")
	runGit("worktree", "add", "-b", "running-branch", wtDir)
	setThreadProviderSession(t, backend, st, threadID, "sess-running")

	backend.mu.Lock()
	backend.threads[threadID].summary.IsRunning = true
	backend.mu.Unlock()

	if err := backend.SetThreadWorkDir(threadID, wtDir); err == nil {
		t.Fatal("运行中不应允许切换 worktree")
	}

	row := mustStoreThread(t, st, threadID)
	if row.WorktreePath != "" || row.WorktreeRequested {
		t.Errorf("运行中切换失败不应改库: path=%q requested=%v", row.WorktreePath, row.WorktreeRequested)
	}
	if row.SessionID != "sess-running" {
		t.Errorf("运行中切换失败不应清 session，实得 %q", row.SessionID)
	}
	if dir, _ := backend.ThreadWorkDir(threadID); !samePath(dir, repoDir) {
		t.Errorf("运行中切换失败后 workDir 应保持项目根，实得 %q", dir)
	}
}
