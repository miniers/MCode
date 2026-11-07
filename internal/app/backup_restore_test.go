package app

import (
	"context"
	"os"
	"testing"

	"mcode/internal/backup"
)

func TestBackupRestoreEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if result, err := backup.ApplyPending(dir); err != nil || result.Applied || result.RollbackDir != "" || len(result.SecretFailures) != 0 || len(result.InvalidProjectIDs) != 0 {
		t.Fatalf("ApplyPending: %+v, %v", result, err)
	}
	if result, err := backup.CompletePending(context.Background(), dir, nil); err != nil || result.Applied || result.RollbackDir != "" || len(result.SecretFailures) != 0 || len(result.InvalidProjectIDs) != 0 {
		t.Fatalf("CompletePending: %+v, %v", result, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory entries: %v, %v", entries, err)
	}
}
