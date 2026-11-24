package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerInit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	evalDir := func(t *testing.T, p string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", p, err)
		}
		return filepath.Clean(resolved)
	}

	t.Run("plain dir", func(t *testing.T) {
		t.Parallel()
		runner := NewRunner("", 4)
		dir := t.TempDir()

		if err := runner.Init(ctx, dir); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		root, err := runner.RepoRoot(ctx, dir)
		if err != nil {
			t.Fatalf("RepoRoot failed: %v", err)
		}
		if got, want := evalDir(t, root), evalDir(t, dir); got != want {
			t.Fatalf("RepoRoot = %q, want %q", got, want)
		}
		if !runner.IsRealRepo(ctx, dir) {
			t.Fatalf("expected IsRealRepo to be true after Init")
		}
	})

	t.Run("shadowed dir", func(t *testing.T) {
		t.Parallel()
		runner := NewRunner("", 4)
		runner.ShadowRoot = t.TempDir()
		dir := t.TempDir()

		if err := os.WriteFile(filepath.Join(dir, "app.txt"), []byte("hello\n"), 0644); err != nil {
			t.Fatalf("write app.txt: %v", err)
		}
		shadowDir, err := runner.EnsureShadow(ctx, dir)
		if err != nil {
			t.Fatalf("EnsureShadow failed: %v", err)
		}
		if runner.IsRealRepo(ctx, dir) {
			t.Fatalf("expected IsRealRepo to be false before Init")
		}

		if err := runner.Init(ctx, dir); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		if !runner.IsRealRepo(ctx, dir) {
			t.Fatalf("expected IsRealRepo to be true after Init")
		}
		if runner.IsShadow(dir) {
			t.Fatalf("expected IsShadow to be false after Init")
		}

		wantDir := evalDir(t, dir)
		root, err := runner.RepoRoot(ctx, dir)
		if err != nil {
			t.Fatalf("RepoRoot failed: %v", err)
		}
		if got := evalDir(t, root); got != wantDir {
			t.Fatalf("RepoRoot = %q, want %q", got, wantDir)
		}

		out, err := runner.run(ctx, dir, nil, "rev-parse", "--absolute-git-dir")
		if err != nil {
			t.Fatalf("rev-parse --absolute-git-dir failed: %v", err)
		}
		if got, want := evalDir(t, strings.TrimSpace(string(out))), filepath.Join(wantDir, ".git"); got != want {
			t.Fatalf("git-dir = %q, want %q", got, want)
		}

		if fi, err := os.Stat(shadowDir); err != nil || !fi.IsDir() {
			t.Fatalf("expected shadow dir %q to remain intact, err=%v", shadowDir, err)
		}
	})
}
