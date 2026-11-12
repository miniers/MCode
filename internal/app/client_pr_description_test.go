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

func TestPRChangesBaseAndFallback(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	write("base")
	run("add", "a")
	run("commit", "-m", "base")
	if _, _, _, err := gitPRChanges(ctx, dir); err == nil || err.Error() != "当前分支没有可描述的改动" {
		t.Fatalf("空分支错误: %v", err)
	}
	run("checkout", "-b", "feature")
	write("feature")
	run("commit", "-am", "feature change")
	commits, stat, patch, err := gitPRChanges(ctx, dir)
	if err != nil || !strings.Contains(commits, "feature change") || !strings.Contains(stat, "a") || !strings.Contains(patch, "+feature") {
		t.Fatalf("分支改动: %q %q %q %v", commits, stat, patch, err)
	}
	run("checkout", "main")
	write("uncommitted")
	commits, _, patch, err = gitPRChanges(ctx, dir)
	if err != nil || commits != "" || !strings.Contains(patch, "+uncommitted") {
		t.Fatalf("未提交改动: %q %q %v", commits, patch, err)
	}
}
