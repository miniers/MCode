package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

func TestDeleteThreadWorktreeCleanup(t *testing.T) {
	c, root, thread := branchClient(t)
	ctx := context.Background()

	// 先验证主仓库检出会话删除时绝不删除主仓库目录与当前分支。
	mainThread, err := c.backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	mainBranch := branchGit(t, root, "branch", "--show-current")
	if err := c.DeleteThread(mainThread.ThreadID); err != nil {
		t.Fatalf("删除主仓库会话失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); err != nil {
		t.Fatalf("主仓库文件被误删: %v", err)
	}
	if got := branchGit(t, root, "branch", "--list", mainBranch); got == "" {
		t.Fatalf("主分支 %s 不应被删除", mainBranch)
	}

	// 建立 linked worktree 并把会话切到该 worktree，删除会话应同时清理目录与分支。
	wtDir := filepath.Join(t.TempDir(), "wt-cleanup")
	const wtBranch = "feat-worktree-cleanup"
	branchGit(t, root, "worktree", "add", "-b", wtBranch, wtDir)
	if err := c.GitSwitchWorktree(ctx, thread, wtDir); err != nil {
		t.Fatalf("切换会话 worktree 失败: %v", err)
	}

	if err := c.DeleteThread(thread); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}

	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("worktree 目录未被删除: %v", err)
	}
	if got := branchGit(t, root, "branch", "--list", wtBranch); got != "" {
		t.Fatalf("worktree 分支未被删除: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); err != nil {
		t.Fatalf("主仓库应保留: %v", err)
	}
	if got := branchGit(t, root, "branch", "--list", mainBranch); got == "" {
		t.Fatalf("主分支 %s 应保留", mainBranch)
	}
}

func TestResolveDeleteThreadWorktreeMainRootUnknown(t *testing.T) {
	c, root, thread := branchClient(t)
	ctx := context.Background()

	st, err := c.backend.GitState(ctx, thread)
	if err != nil {
		t.Fatalf("GitState 失败: %v", err)
	}
	foundMain := false
	for _, wt := range st.Worktrees {
		if legacyResolvedPath(wt.Path) == legacyResolvedPath(root) {
			foundMain = true
			break
		}
	}
	if !foundMain {
		t.Fatalf("GitState 应包含主检出条目 %q: %+v", root, st.Worktrees)
	}

	orig := resolveDeleteThreadMainRepoRoot
	resolveDeleteThreadMainRepoRoot = func(*Client, context.Context, string) (string, error) {
		return "", errors.New("main repo root unavailable")
	}
	t.Cleanup(func() {
		resolveDeleteThreadMainRepoRoot = orig
	})

	target := c.resolveDeleteThreadWorktree(ctx, thread)
	if target.isMcodeWT {
		t.Fatalf("MainRepoRoot 不可用时不应把主检出识别为 worktree: %+v", target)
	}
	if target.wtPath != "" {
		t.Fatalf("MainRepoRoot 不可用时 wtPath 应为空，实际为 %q", target.wtPath)
	}

	if err := c.DeleteThread(thread); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); err != nil {
		t.Fatalf("主仓库文件被误删: %v", err)
	}
}

func seedThreadsForListTest(t *testing.T, total, archived int) (*Client, map[string]bool, map[string]bool) {
	t.Helper()
	backend := remote.NewMemoryBackend(nil)
	t.Cleanup(func() { backend.Close() })

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	archivedIDs := make(map[string]bool, archived)
	activeIDs := make(map[string]bool, total-archived)
	for i := 0; i < total; i++ {
		isArchived := i < archived
		id := fmt.Sprintf("active-%03d", i-archived)
		if isArchived {
			id = fmt.Sprintf("arch-%03d", i)
			archivedIDs[id] = true
		} else {
			activeIDs[id] = true
		}
		backend.AddThread(wire.ThreadSummary{
			ID:         id,
			ProjectID:  "p1",
			Provider:   "memory",
			UpdatedAt:  base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			IsArchived: isArchived,
		}, nil)
	}
	return NewClient(backend), archivedIDs, activeIDs
}

func TestArchivedThreadsReturnsAllBeyondOnePage(t *testing.T) {
	c, archivedIDs, _ := seedThreadsForListTest(t, 250, 130)
	got, err := c.ArchivedThreads(context.Background())
	if err != nil {
		t.Fatalf("ArchivedThreads 失败: %v", err)
	}
	if len(got) != 130 {
		t.Fatalf("ArchivedThreads 返回数量 = %d，期望 130", len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, th := range got {
		if !archivedIDs[th.ID] {
			t.Fatalf("ArchivedThreads 返回了非归档会话 %q", th.ID)
		}
		if seen[th.ID] {
			t.Fatalf("ArchivedThreads 返回了重复会话 %q", th.ID)
		}
		seen[th.ID] = true
	}
}

func TestThreadsReturnsAllBeyondOnePage(t *testing.T) {
	c, _, activeIDs := seedThreadsForListTest(t, 250, 130)
	got, err := c.Threads(context.Background(), "")
	if err != nil {
		t.Fatalf("Threads 失败: %v", err)
	}
	if len(got) != 120 {
		t.Fatalf("Threads 返回数量 = %d，期望 120", len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, th := range got {
		if !activeIDs[th.ID] {
			t.Fatalf("Threads 返回了意外或归档会话 %q", th.ID)
		}
		if seen[th.ID] {
			t.Fatalf("Threads 返回了重复会话 %q", th.ID)
		}
		seen[th.ID] = true
	}
}
