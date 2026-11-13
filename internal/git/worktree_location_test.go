package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcode/internal/config"
)

func TestWorktreeBase(t *testing.T) {
	data, repo := t.TempDir(), t.TempDir()
	for _, tc := range []struct{ location, want string }{
		{config.WorktreeLocationCentral, filepath.Join(data, "worktrees")},
		{config.WorktreeLocationProject, filepath.Join(repo, ".mcode", "worktrees")},
		{"unknown", filepath.Join(data, "worktrees")},
	} {
		if got := WorktreeBase(data, repo, tc.location); got != tc.want {
			t.Errorf("位置 %q：得到 %q，期望 %q", tc.location, got, tc.want)
		}
	}
}

func TestIsManagedWorktreePathProject(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/repo/.mcode/worktrees/task", true},
		{"/repo/.mcode/worktrees/task/src", true},
		{`C:\repo\.mcode\worktrees\task`, true},
		{"/repo/.mcode/worktrees", false},
		{"/repo/.mcode/worktrees/", false},
		{"/repo/.mcode/worktrees-other/task", false},
		{"/repo/mcode/worktrees/task", false},
		{"/repo/.mcode/worktrees/../outside", false},
		{`C:\repo\.mcode\worktrees\..\outside`, false},
		{"", false},
	} {
		if got := IsManagedWorktreePath(tc.path); got != tc.want {
			t.Errorf("路径 %q：得到 %v，期望 %v", tc.path, got, tc.want)
		}
	}
}

func TestProjectWorktreeExcludedAndPurgeable(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()
	wt := filepath.Join(root, ".mcode", "worktrees", "task")
	if err := r.WorktreeAdd(ctx, root, wt, "project-task", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if out, err := r.run(ctx, root, nil, "status", "--porcelain"); err != nil || len(out) != 0 {
		t.Fatalf("项目内 worktree 应已被排除：%s，错误 %v", out, err)
	}
	if err := r.WorktreeRemove(ctx, root, wt, true); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.PurgeWorktree(ctx, root, WorktreeScanItem{Path: wt, Orphan: true}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("孤立目录未删除：%v", err)
	}
}

func TestEnsureGitExcludeConcurrent(t *testing.T) {
	root := t.TempDir()
	info := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(info, "exclude")
	if err := os.WriteFile(file, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { EnsureGitExclude(root, "/.mcode/worktrees/") })
		wg.Go(func() { EnsureGitExclude(root, "/.mcode/context/") })
	}
	wg.Wait()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"existing", "/.mcode/worktrees/", "/.mcode/context/"} {
		if strings.Count(string(data), entry+"\n") != 1 {
			t.Fatalf("排除行应保留且不重复：%s", data)
		}
	}
}
