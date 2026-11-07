package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManager_ProjectIDForWorkDir_RestoredAndStartingBees(t *testing.T) {
	// Restore 会清空磁盘上不存在的 WorktreePath，所以这里用真实临时目录。
	root := t.TempDir()
	cross := filepath.Join(root, "cross")
	legacy := filepath.Join(root, "legacy")
	starting := filepath.Join(root, "starting")
	for _, dir := range []string{cross, legacy} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	persister := newFakePersister()
	if err := persister.SaveLead(context.Background(), LeadRecord{
		LeadThreadID: "lead", ProjectID: "lead-project",
		Bees: []Bee{
			{ThreadID: "cross-project", ProjectID: "bee-project", WorktreePath: cross, Status: StatusCompleted},
			{ThreadID: "legacy", WorktreePath: legacy, Status: StatusCompleted},
		},
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Persister: persister})
	if err := manager.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.startingWorkDirs[starting] = workDirOwner{leadThreadID: "lead", projectID: "starting-project"}
	for _, test := range []struct {
		path, project string
	}{
		{cross, "bee-project"},
		{filepath.Join(cross, "subdir"), "bee-project"},
		{legacy, "lead-project"},
		{starting, "starting-project"},
	} {
		if id, ok := manager.ProjectIDForWorkDir(test.path); !ok || id != test.project {
			t.Fatalf("wrong project for %s: %s, %v", test.path, id, ok)
		}
		if id, ok := manager.LeadThreadForWorkDir(test.path); !ok || id != "lead" {
			t.Fatalf("wrong lead for %s: %s, %v", test.path, id, ok)
		}
	}
	if _, ok := manager.ProjectIDForWorkDir(filepath.Join(root, "cross-other")); ok {
		t.Fatal("similar prefix must not match")
	}
}

// 验证恢复时工作副本已丢失的 bee 被清理：路径清空、取消待审合并并记录错误。
func TestManager_RestoreClearsMissingWorktree(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	persister := newFakePersister()
	if err := persister.SaveLead(context.Background(), LeadRecord{
		LeadThreadID: "lead", ProjectID: "lead-project",
		Bees: []Bee{
			{ThreadID: "lost", WorktreePath: missing, Status: StatusCompleted, PendingMerge: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Persister: persister})
	if err := manager.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	bees := manager.State("lead").Bees
	if len(bees) != 1 {
		t.Fatalf("want 1 bee, got %d", len(bees))
	}
	if bees[0].WorktreePath != "" {
		t.Fatalf("worktree path should be cleared, got %q", bees[0].WorktreePath)
	}
	if bees[0].PendingMerge {
		t.Fatal("pending merge should be cancelled")
	}
	if bees[0].MergeError == "" {
		t.Fatal("merge error should be recorded")
	}
}
