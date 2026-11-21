package app

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/remote/wire"
)

// TestScanWorktreesDedup 验证同一仓库的主目录与附属 worktree 同时注册为项目时，
// worktreeRepoRoots 按共享 git 目录去重且保留首个根，ScanWorktrees 返回的每个路径只出现一次。
func TestScanWorktreesDedup(t *testing.T) {
	ctx := context.Background()
	client, _, runner, repoDir := newClientRevertEnv(t)
	client.SetGit(runner)

	writeRevertRepoFile(t, repoDir, "README.md", "init\n")
	branchGit(t, repoDir, "add", "README.md")
	branchGit(t, repoDir, "commit", "-m", "init")

	wt1 := filepath.Join(t.TempDir(), "wt-one")
	if err := runner.WorktreeAdd(ctx, repoDir, wt1, "feat/wt-one", ""); err != nil {
		t.Fatalf("WorktreeAdd wt1: %v", err)
	}
	wt2 := filepath.Join(t.TempDir(), "wt-two")
	if err := runner.WorktreeAdd(ctx, repoDir, wt2, "feat/wt-two", ""); err != nil {
		t.Fatalf("WorktreeAdd wt2: %v", err)
	}

	// 把附属 worktree 也注册为项目，模拟主仓库与 worktree 同时出现在项目列表的场景。
	client.backend.AddProject(wire.ProjectSummary{ID: "p2", Name: "Project 1 WT", Path: wt1})

	roots, err := client.worktreeRepoRoots(ctx)
	if err != nil {
		t.Fatalf("worktreeRepoRoots: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("worktreeRepoRoots 应去重为 1 个仓库根，实际 %d: %v", len(roots), roots)
	}
	wantRoot := repoDir
	if real, err := filepath.EvalSymlinks(repoDir); err == nil {
		wantRoot = filepath.Clean(real)
	}
	if filepath.Clean(roots[0]) != wantRoot {
		t.Fatalf("worktreeRepoRoots 应保留首个根 %q，实际 %q", wantRoot, roots[0])
	}

	items, err := client.ScanWorktrees(ctx)
	if err != nil {
		t.Fatalf("ScanWorktrees: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ScanWorktrees 应返回 2 个不重复 worktree，实际 %d: %+v", len(items), items)
	}

	seen := make(map[string]int, len(items))
	for _, it := range items {
		seen[filepath.Clean(it.Path)]++
	}
	for p, count := range seen {
		if count != 1 {
			t.Fatalf("路径 %q 重复出现 %d 次: %+v", p, count, items)
		}
	}

	for _, raw := range []string{wt1, wt2} {
		wantPath := filepath.Clean(raw)
		if real, err := filepath.EvalSymlinks(raw); err == nil {
			wantPath = filepath.Clean(real)
		}
		if seen[wantPath] != 1 {
			t.Fatalf("缺少预期的 worktree 路径 %q，实际结果: %+v", wantPath, items)
		}
	}
}
