//go:build darwin

package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeAdd_CloneIgnored 验证被忽略且匹配模式的条目会以写时复制带入新 worktree。
func TestWorktreeAdd_CloneIgnored(t *testing.T) {
	if ok, reason := cloneVolumeOK(t.TempDir(), t.TempDir()); !ok {
		t.Skipf("当前环境不支持 clonefile：%s", reason)
	}

	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	ignoreContent := "node_modules/\ndist/\n.env.local\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(ignoreContent), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", ".gitignore"); err != nil {
		t.Fatalf("git add .gitignore: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "add gitignore"); err != nil {
		t.Fatalf("git commit .gitignore: %v", err)
	}

	// pkg/b 下放一个已跟踪文件，使 pkg/b/node_modules 也出现在忽略列表中
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "b"), 0o755); err != nil {
		t.Fatalf("mkdir pkg/b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "b", "main.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write pkg/b/main.go: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "pkg/b/main.go"); err != nil {
		t.Fatalf("git add pkg/b/main.go: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "add pkg/b"); err != nil {
		t.Fatalf("git commit pkg/b: %v", err)
	}

	files := map[string]string{
		"node_modules/a/index.js": "module.exports = 1;\n",
		"pkg/b/node_modules/x.js": "export default 2;\n",
		".env.local":              "SECRET=abc\n",
		"dist/out.js":             "built\n",
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	SetCloneIgnored(true, []string{"node_modules", ".env.*"})
	t.Cleanup(func() { SetCloneIgnored(false, nil) })

	wtDir := filepath.Join(t.TempDir(), "wt-clone")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "feat-clone", ""); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}

	for rel, want := range map[string]string{
		"node_modules/a/index.js": files["node_modules/a/index.js"],
		"pkg/b/node_modules/x.js": files["pkg/b/node_modules/x.js"],
		".env.local":              files[".env.local"],
	} {
		got, err := os.ReadFile(filepath.Join(wtDir, rel))
		if err != nil {
			t.Fatalf("expected %s cloned into worktree: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s content mismatch: got %q, want %q", rel, string(got), want)
		}
	}

	if _, err := os.Stat(filepath.Join(wtDir, "dist")); !os.IsNotExist(err) {
		t.Errorf("expected dist not cloned, stat err = %v", err)
	}

	out, err := runner.run(ctx, wtDir, nil, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status in worktree failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected clean worktree status, got %q", string(out))
	}
}

// TestWorktreeAdd_CloneIgnoredDisabled 验证关闭开关时不会克隆任何忽略条目。
func TestWorktreeAdd_CloneIgnoredDisabled(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", ".gitignore"); err != nil {
		t.Fatalf("git add .gitignore: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "add gitignore"); err != nil {
		t.Fatalf("git commit .gitignore: %v", err)
	}

	nmDir := filepath.Join(dir, "node_modules", "a")
	if err := os.MkdirAll(nmDir, 0o755); err != nil {
		t.Fatalf("mkdir node_modules/a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "index.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write node_modules/a/index.js: %v", err)
	}

	SetCloneIgnored(false, []string{"node_modules"})
	t.Cleanup(func() { SetCloneIgnored(false, nil) })

	wtDir := filepath.Join(t.TempDir(), "wt-noclone")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "feat-noclone", ""); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtDir, "node_modules")); !os.IsNotExist(err) {
		t.Errorf("expected node_modules absent in worktree, stat err = %v", err)
	}
}
