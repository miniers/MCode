package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// setupGitOpsBackend 在临时目录建好带一次提交的 git 仓库，并创建指向它的项目与会话。
func setupGitOpsBackend(t *testing.T) (*MemoryBackend, string, string, func(args ...string) string) {
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

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{
		ID:   projID,
		Name: "git-ops-project",
		Path: repoDir,
	})
	res, err := backend.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: projID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	return backend, res.ThreadID, repoDir, runGit
}

func TestMemoryBackend_GitState(t *testing.T) {
	backend, threadID, repoDir, runGit := setupGitOpsBackend(t)
	ctx := context.Background()

	runGit("branch", "feature-a")
	if err := os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}

	st, err := backend.GitState(ctx, threadID)
	if err != nil {
		t.Fatalf("GitState failed: %v", err)
	}
	if st.Branch != "main-test" || st.Detached {
		t.Errorf("expected branch main-test (not detached), got %q detached=%v", st.Branch, st.Detached)
	}
	if len(st.Head) != 7 {
		t.Errorf("expected 7-char head, got %q", st.Head)
	}
	if st.Changes != 2 {
		t.Errorf("expected 2 changes, got %d", st.Changes)
	}
	foundFeature := false
	for _, b := range st.Branches {
		if strings.HasPrefix(b, "refs/") {
			t.Errorf("expected short branch name, got %q", b)
		}
		if b == "feature-a" {
			foundFeature = true
		}
	}
	if !foundFeature {
		t.Errorf("expected feature-a in branches, got %v", st.Branches)
	}
	if len(st.Worktrees) != 1 || !st.Worktrees[0].Current || st.Worktrees[0].Branch != "main-test" {
		t.Errorf("expected single current worktree on main-test, got %+v", st.Worktrees)
	}

	if _, err := backend.GitState(ctx, "missing-thread"); err == nil {
		t.Errorf("expected error for missing thread, got nil")
	}
}

func TestMemoryBackend_GitSwitchWorktree(t *testing.T) {
	backend, threadID, _, runGit := setupGitOpsBackend(t)
	ctx := context.Background()

	if err := backend.GitSwitchWorktree(ctx, threadID, t.TempDir()); err == nil {
		t.Fatalf("expected error for path outside worktree list, got nil")
	}

	wtDir := filepath.Join(t.TempDir(), "wt-feature")
	runGit("worktree", "add", "-b", "feature-wt", wtDir)

	if err := backend.GitSwitchWorktree(ctx, threadID, wtDir); err != nil {
		t.Fatalf("GitSwitchWorktree failed: %v", err)
	}
	got, ok := backend.ThreadWorkDir(threadID)
	if !ok || got != filepath.Clean(wtDir) {
		t.Errorf("expected thread workDir %s, got %q (ok=%v)", wtDir, got, ok)
	}

	st, err := backend.GitState(ctx, threadID)
	if err != nil {
		t.Fatalf("GitState failed: %v", err)
	}
	if st.Branch != "feature-wt" {
		t.Errorf("expected branch feature-wt after switch, got %q", st.Branch)
	}
	if len(st.Worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %+v", st.Worktrees)
	}
	for _, wt := range st.Worktrees {
		if wt.Current != (wt.Branch == "feature-wt") {
			t.Errorf("unexpected Current flag on worktree %+v", wt)
		}
	}
}

func TestMemoryBackend_GitWorktreeUsedByHarness(t *testing.T) {
	backend, threadID, repoDir, runGit := setupGitOpsBackend(t)
	ctx := context.Background()
	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit("worktree", "add", "-b", "agent-worktree", wtDir)
	if err := backend.GitSwitchWorktree(ctx, threadID, wtDir); err != nil {
		t.Fatal(err)
	}
	mock := &mockHarness{}
	backend.SetHarness(mock, repoDir, harness.ModeAgent)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "test"}); err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, backend, ctx, threadID)
	calls := mock.getTurnCalls()
	if len(calls) != 1 || !samePath(calls[0].WorkDir, wtDir) {
		t.Fatalf("Agent 未使用选中的 worktree: %+v", calls)
	}
}

func TestMemoryBackend_GitSwitchRejectsRunningThread(t *testing.T) {
	backend, threadID, repoDir, runGit := setupGitOpsBackend(t)
	ctx := context.Background()
	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit("worktree", "add", "-b", "other-worktree", wtDir)
	runGit("branch", "other-branch")
	backend.mu.Lock()
	backend.threads[threadID].summary.IsRunning = true
	backend.mu.Unlock()
	if err := backend.GitSwitchWorktree(ctx, threadID, wtDir); err == nil {
		t.Fatal("运行中不应允许切换 worktree")
	}
	if err := backend.GitCheckout(ctx, threadID, "other-branch"); err == nil {
		t.Fatal("运行中不应允许切换分支")
	}
	if dir, _ := backend.ThreadWorkDir(threadID); !samePath(dir, repoDir) {
		t.Fatalf("运行中目录发生变化: %q", dir)
	}
	if branch := runGit("branch", "--show-current"); branch != "main-test" {
		t.Fatalf("运行中分支发生变化: %q", branch)
	}
}
