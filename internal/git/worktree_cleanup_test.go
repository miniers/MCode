package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/testenv"
)

func setGitTestEnv(t *testing.T) {
	t.Helper()
	testenv.TempHome(t)
	t.Setenv("GIT_AUTHOR_NAME", "test-user")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@localhost")
	t.Setenv("GIT_COMMITTER_NAME", "test-user")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@localhost")
}

// addTestWorktree 在主仓库下新建 worktree，并返回其路径。
func addTestWorktree(t *testing.T, r *Runner, root, name string) string {
	t.Helper()
	return addTestWorktreeAt(t, r, root, t.TempDir(), name)
}

// addTestWorktreeAt 在指定父目录下新建 worktree，并返回其路径。
func addTestWorktreeAt(t *testing.T, r *Runner, root, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := r.WorktreeAdd(context.Background(), root, path, name, ""); err != nil {
		t.Fatalf("WorktreeAdd %s: %v", name, err)
	}
	return path
}

// useTestPurgeRoots 把测试用的 worktree 父目录注册为受管基目录，测试结束恢复默认推导。
func useTestPurgeRoots(t *testing.T, dirs ...string) {
	t.Helper()
	SetWorktreePurgeRoots(dirs...)
	t.Cleanup(func() { SetWorktreePurgeRoots() })
}

func findScanItem(items []WorktreeScanItem, path string) (WorktreeScanItem, bool) {
	for _, it := range items {
		if resolvePath(it.Path) == resolvePath(path) {
			return it, true
		}
	}
	return WorktreeScanItem{}, false
}

func TestDefaultBranchAndIsBranchMerged(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	base, err := r.DefaultBranch(ctx, root)
	if err != nil || base == "" {
		t.Fatalf("DefaultBranch = %q, %v", base, err)
	}
	cur, _ := r.CurrentBranch(ctx, root)
	if base != cur {
		t.Fatalf("DefaultBranch = %q, want current %q", base, cur)
	}

	wt := addTestWorktree(t, r, root, "feat")
	if merged, err := r.IsBranchMerged(ctx, root, "feat", base); err != nil || !merged {
		t.Fatalf("同点分支应视为已合入: merged=%v err=%v", merged, err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, wt, nil, "add", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, wt, nil, "commit", "-m", "feat commit"); err != nil {
		t.Fatal(err)
	}
	if merged, err := r.IsBranchMerged(ctx, root, "feat", base); err != nil || merged {
		t.Fatalf("有新提交的分支应未合入: merged=%v err=%v", merged, err)
	}
}

func TestScanWorktrees(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	clean := addTestWorktree(t, r, root, "clean")
	payload := make([]byte, 1234)
	if err := os.WriteFile(filepath.Join(clean, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	// 已提交的内容不影响体积统计的预期，这里提交以保持 Clean。
	if _, err := r.run(ctx, clean, nil, "add", "big.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, clean, nil, "commit", "-m", "add big"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(clean, old, old); err != nil {
		t.Fatal(err)
	}

	dirty := addTestWorktree(t, r, root, "dirty")
	if err := os.WriteFile(filepath.Join(dirty, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("应只返回 2 个附属 worktree，实际 %d: %+v", len(items), items)
	}
	c, ok := findScanItem(items, clean)
	if !ok {
		t.Fatalf("未扫描到 %s", clean)
	}
	if c.Branch != "clean" || !c.Clean || c.Orphan {
		t.Fatalf("clean 项异常: %+v", c)
	}
	if c.Merged {
		t.Fatalf("含新提交的分支不应为 Merged: %+v", c)
	}
	if want := int64(len("# Worktree Test Repo\n") + 1234); c.SizeBytes != want {
		t.Fatalf("SizeBytes = %d, want %d", c.SizeBytes, want)
	}
	if c.Age < 47*time.Hour {
		t.Fatalf("Age = %v, 期望约 48h", c.Age)
	}
	d, ok := findScanItem(items, dirty)
	if !ok || d.Clean || !d.Merged {
		t.Fatalf("dirty 项异常: %+v ok=%v", d, ok)
	}
}

func TestScanWorktrees_Orphan(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	wt := addTestWorktree(t, r, root, "gone")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	items, err := r.ScanWorktrees(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findScanItem(items, wt)
	if !ok || !it.Orphan {
		t.Fatalf("应识别为孤儿: %+v ok=%v", it, ok)
	}
}

func TestCleanSafeWorktrees(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	// 批量清理只动受管基目录下的 worktree，测试统一放到同一个临时基目录里。
	base := t.TempDir()
	useTestPurgeRoots(t, base)

	safe := addTestWorktreeAt(t, r, root, base, "safe")
	inUse := addTestWorktreeAt(t, r, root, base, "inuse")
	dirty := addTestWorktreeAt(t, r, root, base, "dirty")
	if err := os.WriteFile(filepath.Join(dirty, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	unmerged := addTestWorktreeAt(t, r, root, base, "unmerged")
	if err := os.WriteFile(filepath.Join(unmerged, "u.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, unmerged, nil, "add", "u.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, unmerged, nil, "commit", "-m", "u"); err != nil {
		t.Fatal(err)
	}

	guard := func(now time.Time) CleanupGuard {
		return CleanupGuard{
			InUse: func(p string) bool { return resolvePath(p) == resolvePath(inUse) },
			Now:   func() time.Time { return now },
		}
	}

	// 刚动过（10 分钟内）：全部保留。
	removed, err := r.CleanSafeWorktrees(ctx, root, guard(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("10 分钟内不应清理任何 worktree: %v", removed)
	}

	// 空闲足够久：只清理 safe。
	removed, err = r.CleanSafeWorktrees(ctx, root, guard(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || resolvePath(removed[0]) != resolvePath(safe) {
		t.Fatalf("应只清理 safe，实际 %v", removed)
	}
	if _, err := os.Stat(safe); !os.IsNotExist(err) {
		t.Fatalf("safe 目录应已删除: %v", err)
	}
	for _, p := range []string{inUse, dirty, unmerged} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s 不应被清理: %v", p, err)
		}
	}
}

func TestCleanSafeWorktrees_FromLinkedWorktreeRoot(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()
	base := t.TempDir()
	useTestPurgeRoots(t, base)
	a := addTestWorktreeAt(t, r, root, base, "a")
	b := addTestWorktreeAt(t, r, root, base, "b")

	// 以 linked worktree 作为 root，应回溯到主仓库后清理 b，且不能清理自身 a 之外的主仓库。
	removed, err := r.CleanSafeWorktrees(ctx, a, CleanupGuard{
		Now: func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("应回溯主仓库并清理 2 个 worktree，实际 %v", removed)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s 应已删除", p)
		}
	}
}

func TestCleanSafeWorktrees_SkipsWorktreeOutsideBase(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	base := t.TempDir()
	useTestPurgeRoots(t, base)
	managed := addTestWorktreeAt(t, r, root, base, "managed")
	// 用户手建、已合入、干净且空闲的 worktree，不在受管基目录下。
	userMade := addTestWorktreeAt(t, r, root, t.TempDir(), "user-made")

	removed, err := r.CleanSafeWorktrees(ctx, root, CleanupGuard{
		Now: func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || resolvePath(removed[0]) != resolvePath(managed) {
		t.Fatalf("应只清理受管基目录内的 worktree，实际 %v", removed)
	}
	if _, err := os.Stat(userMade); err != nil {
		t.Fatalf("受管基目录之外的 worktree 不应被清理: %v", err)
	}
}
