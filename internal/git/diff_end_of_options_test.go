package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDiffArgsEndOfOptions(t *testing.T) {
	t.Parallel()

	argsNoPaths := diffArgs("a", "b", nil, false)
	idxEnd := slices.Index(argsNoPaths, "--end-of-options")
	if idxEnd < 0 || idxEnd+2 >= len(argsNoPaths) || argsNoPaths[idxEnd+1] != "a" || argsNoPaths[idxEnd+2] != "b" {
		t.Fatalf("unexpected args without paths: %v", argsNoPaths)
	}

	argsWithPaths := diffArgs("a", "b", []string{"p"}, true)
	idxEnd = slices.Index(argsWithPaths, "--end-of-options")
	if idxEnd < 0 || idxEnd+4 >= len(argsWithPaths) ||
		argsWithPaths[idxEnd+1] != "a" ||
		argsWithPaths[idxEnd+2] != "b" ||
		argsWithPaths[idxEnd+3] != "--" ||
		argsWithPaths[idxEnd+4] != "p" {
		t.Fatalf("unexpected args with paths: %v", argsWithPaths)
	}
}

func TestDiffRejectsOptionLikeRevision(t *testing.T) {
	t.Parallel()

	runner, dir := initGitRepo(t)
	writeTestFile(t, dir, "file.txt", "hello\n")
	runTestGit(t, dir, "add", "file.txt")
	runTestGit(t, dir, "commit", "-m", "initial")

	leakPath := filepath.Join(t.TempDir(), "leak")
	from := "--output=" + leakPath

	_, err := runner.Diff(context.Background(), dir, from, "HEAD", nil)
	if err == nil {
		t.Fatalf("expected error for option-like revision %q, got nil", from)
	}
	if _, statErr := os.Stat(leakPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected leak file %q not to exist, stat err=%v", leakPath, statErr)
	}
}
