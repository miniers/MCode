package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreviewWorktreeMove_NoCommit(t *testing.T) {
	setGitTestEnv(t)
	r, dir := initGitRepo(t)
	if _, err := r.PreviewWorktreeMove(context.Background(), dir); err == nil {
		t.Fatal("没有 commit 的仓库应报错")
	}
}

func TestMoveIntoWorktree(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()

	// tracked：一个已暂存修改、一个未暂存修改；untracked：带可执行位的脚本。
	for name, body := range map[string]string{"staged.txt": "v1\n", "unstaged.txt": "v1\n", "gone.txt": "v1\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.run(ctx, root, nil, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, root, nil, "commit", "-m", "base files"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged-change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(ctx, root, nil, "add", "staged.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unstaged.txt"), []byte("unstaged-change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "new.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pv, err := r.PreviewWorktreeMove(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Files) != 3 || len(pv.Untracked) != 1 || pv.HeadCommit == "" {
		t.Fatalf("预览异常: %+v", pv)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := r.MoveIntoWorktree(ctx, root, wt, "worktree-move-branch", func() bool { return true }); err != nil {
		t.Fatalf("MoveIntoWorktree: %v", err)
	}

	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read(filepath.Join(wt, "staged.txt")); got != "staged-change\n" {
		t.Fatalf("staged.txt = %q", got)
	}
	if got := read(filepath.Join(wt, "unstaged.txt")); got != "unstaged-change\n" {
		t.Fatalf("unstaged.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(wt, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("gone.txt 应在目标侧被删除: %v", err)
	}
	info, err := os.Stat(filepath.Join(wt, "sub", "new.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("untracked 文件应保留可执行位: %v", info.Mode())
	}

	// 原工作区应恢复干净。
	out, err := r.run(ctx, root, nil, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("原工作区应干净，实际:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "new.sh")); !os.IsNotExist(err) {
		t.Fatalf("原仓库 untracked 文件应被移除: %v", err)
	}
	if got := read(filepath.Join(root, "gone.txt")); got != "v1\n" {
		t.Fatalf("原仓库 gone.txt 应恢复: %q", got)
	}
}

func TestMoveIntoWorktree_RollbackWhenNotIdle(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := r.MoveIntoWorktree(ctx, root, wt, "rollback-branch", func() bool { return false }); err == nil {
		t.Fatal("stillIdle=false 应返回错误")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree 目录应被回滚: %v", err)
	}
	if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/heads/rollback-branch"); err == nil {
		t.Fatal("分支应被回滚删除")
	}
	b, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(b) != "changed\n" {
		t.Fatalf("原仓库改动不应被动: %q", b)
	}
}

func TestMoveIntoWorktree_StaleDirectoryRemoved(t *testing.T) {
	setGitTestEnv(t)
	r, root := setupTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MoveIntoWorktree(context.Background(), root, wt, "stale-branch", nil); err != nil {
		t.Fatalf("残留目录应被清理后成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("残留文件应被清掉: %v", err)
	}
}
