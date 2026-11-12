package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

func branchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func branchClient(t *testing.T) (*Client, string, string) {
	t.Helper()
	client, _, _, root := newClientRevertEnv(t)
	branchGit(t, root, "config", "user.name", "测试")
	branchGit(t, root, "config", "user.email", "test@example.com")
	writeRevertRepoFile(t, root, "file.txt", "base\n")
	branchGit(t, root, "add", "file.txt")
	branchGit(t, root, "commit", "-m", "base")
	thread, err := client.backend.CreateThread(context.Background(), wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	return client, root, thread.ThreadID
}

func TestGitBranchNilBackend(t *testing.T) {
	c := NewClient(nil)
	if _, err := c.GitBranches(context.Background(), "thread"); err == nil {
		t.Fatal("应拒绝不可用的 backend")
	}
	if _, err := c.GitOpState(context.Background(), "thread"); err == nil {
		t.Fatal("应拒绝不可用的 backend")
	}
}

func TestGitBranchListAndRename(t *testing.T) {
	c, root, thread := branchClient(t)
	branchGit(t, root, "branch", "before")
	ctx := context.Background()
	branches, err := c.GitBranches(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range branches {
		if b.Name == "before" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少分支: %+v", branches)
	}
	if err := c.GitBranchRename(ctx, thread, "before", "after"); err != nil {
		t.Fatal(err)
	}
	branches, err = c.GitBranches(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, b := range branches {
		if b.Name == "before" {
			t.Fatal("旧分支仍存在")
		}
		if b.Name == "after" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少新分支: %+v", branches)
	}
}

func TestGitPullWithInvalidStrategy(t *testing.T) {
	c, _, thread := branchClient(t)
	if err := c.GitPullWith(context.Background(), thread, "invalid"); err == nil || !strings.Contains(err.Error(), "无效的拉取策略") {
		t.Fatalf("应拒绝无效策略: %v", err)
	}
}

func TestGitOpStateAndGitAbortOp(t *testing.T) {
	c, root, thread := branchClient(t)
	branchGit(t, root, "branch", "other")
	writeRevertRepoFile(t, root, "file.txt", "ours\n")
	branchGit(t, root, "commit", "-am", "ours")
	branchGit(t, root, "checkout", "other")
	writeRevertRepoFile(t, root, "file.txt", "theirs\n")
	branchGit(t, root, "commit", "-am", "theirs")
	sha := branchGit(t, root, "rev-parse", "HEAD")
	branchGit(t, root, "checkout", "-")
	ctx := context.Background()
	if err := c.GitCherryPick(ctx, thread, sha); err == nil {
		t.Fatal("应产生冲突")
	}
	state, err := c.GitOpState(ctx, thread)
	if err != nil || state.Kind != git.OpCherryPick {
		t.Fatalf("操作状态: %+v %v", state, err)
	}
	if err := c.GitAbortOp(ctx, thread); err != nil {
		t.Fatal(err)
	}
	state, err = c.GitOpState(ctx, thread)
	if err != nil || state.Kind != "" {
		t.Fatalf("中止后状态: %+v %v", state, err)
	}
}

func TestGitPushTagWithProgress(t *testing.T) {
	c, root, thread := branchClient(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	branchGit(t, root, "init", "--bare", bare)
	branchGit(t, root, "remote", "add", "origin", bare)
	branchGit(t, root, "tag", "v1")
	var progress []git.Progress
	ctx := git.WithProgress(context.Background(), func(p git.Progress) { progress = append(progress, p) })
	if err := c.GitPushTag(ctx, thread, "origin", "v1"); err != nil {
		t.Fatal(err)
	}
	if got := branchGit(t, bare, "rev-parse", "refs/tags/v1"); got != branchGit(t, root, "rev-parse", "refs/tags/v1") {
		t.Fatalf("远程标签不匹配: %s", got)
	}
	if len(progress) == 0 {
		t.Fatal("未收到推送进度")
	}
}
