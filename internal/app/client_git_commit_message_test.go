package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initCommitMessageDiffRepo(t *testing.T, ctx context.Context) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt", "b.txt")
	run("commit", "-m", "init")
	return dir
}

func TestCommitMessageDiffStagedOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := initCommitMessageDiffRepo(t, ctx)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "git", "add", "a.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add a.txt: %v\n%s", err, out)
	}

	stat := runGitDiffForCommit(ctx, dir, "--stat")
	patch := runGitDiffForCommit(ctx, dir)
	if !strings.Contains(stat, "a.txt") || strings.Contains(stat, "b.txt") {
		t.Fatalf("stat 应只包含 a.txt，实际: %q", stat)
	}
	if !strings.Contains(patch, "a.txt") || !strings.Contains(patch, "+a1") || strings.Contains(patch, "b.txt") || strings.Contains(patch, "+b1") {
		t.Fatalf("patch 应只包含 a.txt 的已暂存改动，实际: %q", patch)
	}
}

func TestCommitMessageDiffUnstagedAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := initCommitMessageDiffRepo(t, ctx)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	stat := runGitDiffForCommit(ctx, dir, "--stat")
	patch := runGitDiffForCommit(ctx, dir)
	if !strings.Contains(stat, "a.txt") || !strings.Contains(stat, "b.txt") {
		t.Fatalf("stat 应同时包含 a.txt 与 b.txt，实际: %q", stat)
	}
	if !strings.Contains(patch, "a.txt") || !strings.Contains(patch, "b.txt") {
		t.Fatalf("patch 应同时包含 a.txt 与 b.txt，实际: %q", patch)
	}
}
