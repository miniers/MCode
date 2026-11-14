package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_Checkout(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	if _, err := runner.run(ctx, dir, nil, "branch", "feature-checkout"); err != nil {
		t.Fatalf("git branch: %v", err)
	}
	if err := runner.Checkout(ctx, dir, "feature-checkout"); err != nil {
		t.Fatalf("Checkout failed: %v", err)
	}
	branch, err := runner.CurrentBranch(ctx, dir)
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}
	if branch != "feature-checkout" {
		t.Errorf("expected branch feature-checkout, got %q", branch)
	}

	if err := runner.Checkout(ctx, dir, ""); err == nil {
		t.Errorf("expected error for empty branch name, got nil")
	}
	if err := runner.Checkout(ctx, dir, "-f"); err == nil {
		t.Errorf("expected error for branch name starting with '-', got nil")
	}
}

func TestRunner_CheckoutDoesNotRestoreFile(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("未提交的内容\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runner.Checkout(context.Background(), dir, "README.md"); err == nil {
		t.Fatal("不存在的分支不应被解释为文件恢复")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "未提交的内容\n" {
		t.Fatalf("工作区内容被覆盖: %q, %v", content, err)
	}
}

func TestRunner_PushUsesConfiguredUpstream(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	ctx := context.Background()
	remoteDir := filepath.Join(t.TempDir(), "upstream.git")
	run := func(args ...string) {
		t.Helper()
		if _, err := runner.run(ctx, dir, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "--bare", remoteDir)
	run("remote", "add", "origin", remoteDir)
	// 本地分支和上游分支不同名，并且默认推送目标故意无效。
	run("checkout", "-b", "local-feature")
	run("push", "--set-upstream", "origin", "HEAD:refs/heads/upstream-feature")
	run("config", "push.default", "simple")
	run("config", "remote.pushDefault", "missing-remote")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runner.Commit(ctx, dir, "update"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Push(ctx, dir); err != nil {
		t.Fatal(err)
	}
	local, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := runner.RevParse(ctx, remoteDir, "refs/heads/upstream-feature")
	if err != nil || local != remote {
		t.Fatalf("未推送至配置的上游: local=%q remote=%q err=%v", local, remote, err)
	}
}

func TestRunner_Commit(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# changed\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	if err := runner.Commit(ctx, dir, "  add new file  "); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	entries, err := runner.Status(ctx, dir)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected clean status after commit, got %+v", entries)
	}

	out, err := runner.run(ctx, dir, nil, "log", "--format=%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(string(out), "add new file") {
		t.Errorf("expected commit message in log, got:\n%s", out)
	}

	if err := runner.Commit(ctx, dir, "   "); err == nil {
		t.Errorf("expected error for empty commit message, got nil")
	}
}

func TestRunner_Push(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	bareDir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := runner.run(ctx, t.TempDir(), nil, "init", "--bare", bareDir); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "remote", "add", "origin", bareDir); err != nil {
		t.Fatalf("git remote add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "checkout", "-b", "feature-push"); err != nil {
		t.Fatalf("git checkout -b: %v", err)
	}

	// 首次推送：无上游，走 --set-upstream origin <branch>
	if err := runner.Push(ctx, dir); err != nil {
		t.Fatalf("first Push failed: %v", err)
	}
	localHead, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	remoteHead, err := runner.RevParse(ctx, bareDir, "refs/heads/feature-push")
	if err != nil {
		t.Fatalf("expected feature-push in bare remote: %v", err)
	}
	if remoteHead != localHead {
		t.Errorf("expected remote head %s, got %s", localHead, remoteHead)
	}

	// 再次提交后已有上游，走无参 git push
	if err := os.WriteFile(filepath.Join(dir, "second.txt"), []byte("second\n"), 0644); err != nil {
		t.Fatalf("write second.txt: %v", err)
	}
	if err := runner.Commit(ctx, dir, "second commit"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := runner.Push(ctx, dir); err != nil {
		t.Fatalf("second Push failed: %v", err)
	}
	localHead, err = runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	remoteHead, err = runner.RevParse(ctx, bareDir, "refs/heads/feature-push")
	if err != nil {
		t.Fatalf("rev-parse remote feature-push: %v", err)
	}
	if remoteHead != localHead {
		t.Errorf("expected remote head %s after second push, got %s", localHead, remoteHead)
	}
}

func TestRunner_PushDetachedHead(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	if _, err := runner.run(ctx, dir, nil, "checkout", "--detach"); err != nil {
		t.Fatalf("git checkout --detach: %v", err)
	}
	if err := runner.Push(ctx, dir); err == nil {
		t.Errorf("expected error when pushing in detached HEAD, got nil")
	}
}

// IsRealRepo 必须绕过影子仓库判断：影子仓库已存在的普通目录不能算真实仓库。
func TestRunner_IsRealRepo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, repoDir := setupTestRepo(t)

	// 1. 真实仓库返回 true
	if !runner.IsRealRepo(ctx, repoDir) {
		t.Errorf("expected IsRealRepo == true for real repo %s", repoDir)
	}

	// 2. 普通目录返回 false
	plainDir := t.TempDir()
	if runner.IsRealRepo(ctx, plainDir) {
		t.Errorf("expected IsRealRepo == false for plain dir %s", plainDir)
	}

	// 3. 带 ShadowRoot 的 Runner：影子仓库建好后，普通目录仍为 false，真实仓库仍为 true
	shadowRunner := NewRunner("", 4)
	shadowRunner.ShadowRoot = t.TempDir()
	shadowProject := t.TempDir()
	if _, err := shadowRunner.EnsureShadow(ctx, shadowProject); err != nil {
		t.Fatalf("EnsureShadow failed: %v", err)
	}
	// 前提校验：带影子环境的 RepoRoot 会误判成功，这正是 IsRealRepo 要绕开的行为
	if _, err := shadowRunner.RepoRoot(ctx, shadowProject); err != nil {
		t.Fatalf("expected RepoRoot to succeed with shadow, got: %v", err)
	}
	if shadowRunner.IsRealRepo(ctx, shadowProject) {
		t.Errorf("expected IsRealRepo == false for shadowed plain dir %s", shadowProject)
	}
	if !shadowRunner.IsRealRepo(ctx, repoDir) {
		t.Errorf("expected IsRealRepo == true for real repo %s on shadow runner", repoDir)
	}
}
