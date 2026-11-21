package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCompletePendingPrunesOldRollbackDirs(t *testing.T) {
	bkSetup(t)
	ctx := context.Background()
	source, dest := t.TempDir(), t.TempDir()
	s := bkOpen(t, source)
	bkThread(t, s, "source", source)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d := bkOpen(t, dest)
	bkThread(t, d, "target", dest)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	bkWrite(t, source, "config.json", `{"version":9,"providers":{"claude":{"bin":"source"}}}`)
	bkWrite(t, dest, "config.json", `{"version":9,"providers":{"claude":{"bin":"target"}}}`)
	archive := bkCreate(t, source, false)

	oldDir1 := filepath.Join(dest, "pre-restore-20200101-000000.000000001")
	oldDir2 := filepath.Join(dest, "pre-restore-20200101-000000.000000002")
	bkWrite(t, oldDir1, "mcode.db", "old1")
	bkWrite(t, oldDir2, "mcode.db", "old2")
	keepFile := filepath.Join(dest, "pre-restore-note")
	if err := os.WriteFile(keepFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	bkStage(t, dest, archive)
	if err := ConfirmPending(dest); err != nil {
		t.Fatal(err)
	}
	if result, err := ApplyPending(dest); err != nil || !result.Applied {
		t.Fatalf("apply: %+v %v", result, err)
	}
	d = bkOpen(t, dest)
	defer d.Close()
	result, err := CompletePending(ctx, dest, d)
	if err != nil || !result.Applied {
		t.Fatalf("complete: %+v %v", result, err)
	}
	if result.RollbackDir == "" {
		t.Fatal("expected non-empty RollbackDir")
	}

	for _, oldDir := range []string{oldDir1, oldDir2} {
		if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected old rollback dir %s to be pruned, got err=%v", oldDir, err)
		}
	}
	if info, err := os.Stat(result.RollbackDir); err != nil || !info.IsDir() {
		t.Fatalf("expected current rollback dir %s to remain: info=%v err=%v", result.RollbackDir, info, err)
	}
	if data, err := os.ReadFile(keepFile); err != nil || string(data) != "keep" {
		t.Fatalf("expected non-dir file %s to remain: data=%q err=%v", keepFile, data, err)
	}
}
