package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/git"
)

// 被 bee 引用的 worktree 保留，未被引用的孤儿副本经 git worktree remove 清理。
func TestManager_SweepOrphanCopies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	runner, repoDir := initTestRepo(t)
	wtRoot := t.TempDir()
	mgr := NewManager(Options{Git: runner, WorktreeDir: wtRoot})

	ctx := context.Background()
	referenced := filepath.Join(wtRoot, "lead-1-0")
	orphan := filepath.Join(wtRoot, "lead-1-1")
	if err := runner.WorktreeAdd(ctx, repoDir, referenced, "mcode/test-referenced", "HEAD"); err != nil {
		t.Fatalf("worktree add referenced: %v", err)
	}
	if err := runner.WorktreeAdd(ctx, repoDir, orphan, "mcode/test-orphan", "HEAD"); err != nil {
		t.Fatalf("worktree add orphan: %v", err)
	}

	mgr.mu.Lock()
	mgr.leads["lead-1"] = &leadState{
		leadThreadID: "lead-1",
		bees:         []Bee{{ThreadID: "bee-1", WorktreePath: referenced}},
	}
	mgr.mu.Unlock()

	removed, err := mgr.SweepOrphanCopies(ctx)
	if err != nil {
		t.Fatalf("SweepOrphanCopies returned error: %v", err)
	}
	if len(removed) != 1 || removed[0] != orphan {
		t.Fatalf("expected only the orphan dir removed, got %v", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("expected orphan dir removed, stat err: %v", err)
	}
	if _, err := os.Stat(referenced); err != nil {
		t.Errorf("expected referenced worktree kept, stat err: %v", err)
	}
}

// 非 git worktree 的孤儿目录直接删除；startingWorkDirs 中登记的目录保留。
func TestManager_SweepOrphanCopies_PlainDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	wtRoot := t.TempDir()
	mgr := NewManager(Options{Git: git.NewRunner("git", 4), WorktreeDir: wtRoot})

	keep := filepath.Join(wtRoot, "keep-0")
	orphan := filepath.Join(wtRoot, "orphan-1")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatalf("mkdir keep: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orphan, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	mgr.mu.Lock()
	mgr.startingWorkDirs[filepath.Clean(keep)] = workDirOwner{leadThreadID: "lead-1"}
	mgr.mu.Unlock()

	removed, err := mgr.SweepOrphanCopies(context.Background())
	if err != nil {
		t.Fatalf("SweepOrphanCopies returned error: %v", err)
	}
	if len(removed) != 1 || removed[0] != orphan {
		t.Fatalf("expected only the orphan dir removed, got %v", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("expected plain orphan dir removed, stat err: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("expected starting work dir kept, stat err: %v", err)
	}
}

// WorktreeDir 为空或不存在时返回 nil, nil。
func TestManager_SweepOrphanCopies_MissingWorktreeDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mgr := NewManager(Options{WorktreeDir: filepath.Join(t.TempDir(), "missing")})
	removed, err := mgr.SweepOrphanCopies(context.Background())
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got %v", err)
	}
	if removed != nil {
		t.Fatalf("expected nil removed list, got %v", removed)
	}

	empty := NewManager(Options{})
	removed, err = empty.SweepOrphanCopies(context.Background())
	if err != nil || removed != nil {
		t.Fatalf("expected nil, nil for empty WorktreeDir, got %v, %v", removed, err)
	}
}
