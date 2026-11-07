package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeWorktree_RemovesCleanMergedWorktree(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	wt := addTestWorktree(t, r, root, "purge-clean")

	items, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findScanItem(items, wt)
	if !ok || it.Orphan || !it.Clean {
		t.Fatalf("扫描项异常: %+v ok=%v", it, ok)
	}

	if err := r.PurgeWorktree(ctx, root, it, false); err != nil {
		t.Fatalf("PurgeWorktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("目录应已删除: %v", err)
	}

	after, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findScanItem(after, wt); ok {
		t.Fatalf("worktree 注册应已 prune: %+v", after)
	}

	// 已合入分支随清理删除（git branch -d）
	if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/heads/purge-clean"); err == nil {
		t.Fatal("已合入分支应随清理删除")
	}
}

func TestPurgeWorktree_DirtyRejectedWithoutAllowDirty(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	wt := addTestWorktree(t, r, root, "purge-dirty")
	if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findScanItem(items, wt)
	if !ok || it.Clean {
		t.Fatalf("应为有未提交改动的项: %+v ok=%v", it, ok)
	}

	if err := r.PurgeWorktree(ctx, root, it, false); err == nil {
		t.Fatal("存在未提交改动且未确认时应拒绝删除")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("拒绝时不应删除目录: %v", err)
	}

	if err := r.PurgeWorktree(ctx, root, it, true); err != nil {
		t.Fatalf("确认丢弃改动后应删除成功: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("目录应已删除: %v", err)
	}
}

func TestPurgeWorktree_UnmergedBranchKept(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	wt := addTestWorktree(t, r, root, "purge-unmerged")
	if err := os.WriteFile(filepath.Join(wt, "u.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, wt, nil, "add", "u.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, wt, nil, "commit", "-m", "u"); err != nil {
		t.Fatal(err)
	}

	items, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findScanItem(items, wt)
	if !ok || it.Merged || !it.Clean {
		t.Fatalf("扫描项应为干净且未合入: %+v ok=%v", it, ok)
	}

	if err := r.PurgeWorktree(ctx, root, it, false); err != nil {
		t.Fatalf("干净但未合入的 worktree 应可删除: %v", err)
	}
	if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/heads/purge-unmerged"); err != nil {
		t.Fatalf("未合入分支不应被删除: %v", err)
	}
}

func TestPurgeWorktree_OrphanOutsideBaseRejected(t *testing.T) {
	setGitTestEnv(t)
	SetWorktreePurgeRoots(filepath.Join(t.TempDir(), "managed-worktrees"))
	t.Cleanup(func() { SetWorktreePurgeRoots() })

	r, root := setupTestRepo(t)
	ctx := context.Background()

	wt := addTestWorktree(t, r, root, "purge-outside")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	items, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findScanItem(items, wt)
	if !ok || !it.Orphan {
		t.Fatalf("应为孤立项: %+v ok=%v", it, ok)
	}

	if err := r.PurgeWorktree(ctx, root, it, false); err == nil {
		t.Fatal("受管基目录之外的孤立路径应被拒绝")
	}
}

func TestPurgeWorktree_OrphanUnderBasePruned(t *testing.T) {
	setGitTestEnv(t)
	base := filepath.Join(t.TempDir(), "managed-worktrees")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	SetWorktreePurgeRoots(base)
	t.Cleanup(func() { SetWorktreePurgeRoots() })

	r, root := setupTestRepo(t)
	ctx := context.Background()

	wt := filepath.Join(base, "orphan-wt")
	if err := r.WorktreeAdd(ctx, root, wt, "orphan-wt", ""); err != nil {
		t.Fatal(err)
	}
	// 目录被外部删除，只剩过期的注册信息。
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	items, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findScanItem(items, wt)
	if !ok || !it.Orphan {
		t.Fatalf("应为孤立项: %+v ok=%v", it, ok)
	}

	if err := r.PurgeWorktree(ctx, root, it, false); err != nil {
		t.Fatalf("受管基目录内的孤立项应删除成功: %v", err)
	}
	after, err := r.ScanWorktrees(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findScanItem(after, wt); ok {
		t.Fatalf("孤立项注册应已 prune: %+v", after)
	}
}
