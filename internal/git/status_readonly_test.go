package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyStatus(t *testing.T) {
	t.Parallel()
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	// 修改已提交的 README.md
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# modified\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	// 新建未跟踪目录及两个文件，验证未跟踪目录折叠显示
	newDir := filepath.Join(dir, "newdir")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("mkdir newdir: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(newDir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	entries, err := runner.ReadOnlyStatus(ctx, dir)
	if err != nil {
		t.Fatalf("ReadOnlyStatus failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (README.md and collapsed newdir/), got %d: %+v", len(entries), entries)
	}

	var sawModified, sawNewDir bool
	for _, entry := range entries {
		switch entry.Path {
		case "README.md":
			if entry.Index == ' ' && entry.Worktree == 'M' {
				sawModified = true
			}
		case "newdir/":
			if entry.Index == '?' && entry.Worktree == '?' {
				sawNewDir = true
			}
		}
	}
	if !sawModified {
		t.Fatalf("expected modified README.md in entries, got %+v", entries)
	}
	if !sawNewDir {
		t.Fatalf("expected collapsed untracked newdir/ in entries, got %+v", entries)
	}

	lockPath := filepath.Join(dir, ".git", "index.lock")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s not to exist, got err=%v", lockPath, err)
	}
}
