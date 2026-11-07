package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const diffContextSample = `package sample

func Untouched() {
	untouchedLine()
}

func Target() {
	firstLine()
	secondLine()
	thirdLine()
}

func Other() {
	otherLine()
}
`

func TestDiffContextIncludesEnclosingFunction(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	filePath := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(filePath, []byte(diffContextSample), 0644); err != nil {
		t.Fatalf("write sample.go: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "sample.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	base, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	modified := strings.Replace(diffContextSample, "secondLine()", "changedLine()", 1)
	if err := os.WriteFile(filePath, []byte(modified), 0644); err != nil {
		t.Fatalf("modify sample.go: %v", err)
	}

	out, truncated, err := runner.DiffContext(ctx, dir, base, nil, 0)
	if err != nil {
		t.Fatalf("DiffContext: %v", err)
	}
	if truncated {
		t.Fatalf("DiffContext unexpectedly truncated")
	}

	for _, want := range []string{
		"func Target() {",
		"\n \tfirstLine()",
		"\n+\tchangedLine()",
		"\n \tthirdLine()",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("DiffContext output missing %q:\n%s", want, out)
		}
	}
	// -W 只扩展改动块所在函数，不应把相邻函数也整段带进来。
	for _, unwanted := range []string{"untouchedLine()", "otherLine()"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("DiffContext output unexpectedly contains %q:\n%s", unwanted, out)
		}
	}
}

func TestDiffContextTruncates(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	filePath := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(filePath, []byte(diffContextSample), 0644); err != nil {
		t.Fatalf("write sample.go: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "sample.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	base, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	modified := strings.Replace(diffContextSample, "secondLine()", "changedLine()", 1)
	if err := os.WriteFile(filePath, []byte(modified), 0644); err != nil {
		t.Fatalf("modify sample.go: %v", err)
	}

	out, truncated, err := runner.DiffContext(ctx, dir, base, nil, 120)
	if err != nil {
		t.Fatalf("DiffContext: %v", err)
	}
	if !truncated {
		t.Fatalf("DiffContext should report truncation, output:\n%s", out)
	}
	if len(out) > 120 {
		t.Fatalf("truncated output should be <= 120 bytes, got %d", len(out))
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("truncated output should end on a whole line, got %q", out)
	}

	full, truncated, err := runner.DiffContext(ctx, dir, base, nil, 0)
	if err != nil {
		t.Fatalf("DiffContext without limit: %v", err)
	}
	if truncated {
		t.Fatalf("DiffContext without limit should not truncate")
	}
	if !strings.HasPrefix(full, out) {
		t.Fatalf("truncated output is not a prefix of full output")
	}
}

func TestDiffContextLeavesIndexUntouched(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	filePath := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(filePath, []byte(diffContextSample), 0644); err != nil {
		t.Fatalf("write sample.go: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "sample.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	base, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	modified := strings.Replace(diffContextSample, "secondLine()", "changedLine()", 1)
	if err := os.WriteFile(filePath, []byte(modified), 0644); err != nil {
		t.Fatalf("modify sample.go: %v", err)
	}
	// 未跟踪文件也要验证：暂存动作只能落在临时索引上。
	if err := os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("package sample\n"), 0644); err != nil {
		t.Fatalf("write untracked.go: %v", err)
	}

	beforeStatus, err := runner.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status before: %v", err)
	}
	beforeIndex, err := runner.run(ctx, dir, nil, "ls-files", "--stage")
	if err != nil {
		t.Fatalf("git ls-files before: %v", err)
	}

	out, _, err := runner.DiffContext(ctx, dir, base, []string{"sample.go"}, 0)
	if err != nil {
		t.Fatalf("DiffContext: %v", err)
	}
	if !strings.Contains(out, "changedLine()") {
		t.Fatalf("DiffContext output missing change:\n%s", out)
	}

	afterStatus, err := runner.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status after: %v", err)
	}
	afterIndex, err := runner.run(ctx, dir, nil, "ls-files", "--stage")
	if err != nil {
		t.Fatalf("git ls-files after: %v", err)
	}
	if !bytes.Equal(beforeStatus, afterStatus) {
		t.Fatalf("git status changed:\nbefore:\n%s\nafter:\n%s", beforeStatus, afterStatus)
	}
	if !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatalf("index changed:\nbefore:\n%s\nafter:\n%s", beforeIndex, afterIndex)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read sample.go: %v", err)
	}
	if string(data) != modified {
		t.Fatalf("worktree file was modified by DiffContext")
	}
}

func TestDiffBetweenWorktreesAddedAndDeleted(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}
	runner := NewRunner(gitBin, 2)
	ctx := context.Background()

	root := t.TempDir()
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	for _, dir := range []string{filepath.Join(oldRoot, "pkg"), filepath.Join(newRoot, "pkg")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "pkg", "keep.go"), []byte("package pkg\n\nfunc Keep() { old() }\n"), 0644); err != nil {
		t.Fatalf("write old keep.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "pkg", "keep.go"), []byte("package pkg\n\nfunc Keep() { new() }\n"), 0644); err != nil {
		t.Fatalf("write new keep.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "pkg", "deleted.go"), []byte("package pkg\n\nfunc Deleted() {}\n"), 0644); err != nil {
		t.Fatalf("write deleted.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "pkg", "added.go"), []byte("package pkg\n\nfunc Added() {}\n"), 0644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	paths := []string{"pkg/keep.go", "pkg/added.go", "pkg/deleted.go"}
	out, err := runner.DiffBetween(ctx, oldRoot, newRoot, paths, 0)
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}

	for _, want := range []string{
		"diff --git a/pkg/keep.go b/pkg/keep.go",
		"-func Keep() { old() }",
		"+func Keep() { new() }",
		"diff --git a/pkg/added.go b/pkg/added.go",
		"new file mode",
		"+++ b/pkg/added.go",
		"diff --git a/pkg/deleted.go b/pkg/deleted.go",
		"deleted file mode",
		"+++ /dev/null",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("DiffBetween output missing %q:\n%s", want, out)
		}
	}
	for _, leaked := range []string{oldRoot, newRoot, filepath.ToSlash(oldRoot), filepath.ToSlash(newRoot)} {
		if strings.Contains(out, leaked) {
			t.Fatalf("DiffBetween output leaked absolute root %q:\n%s", leaked, out)
		}
	}
}
