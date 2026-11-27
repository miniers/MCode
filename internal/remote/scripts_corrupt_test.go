package remote

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/remote/wire"
)

func TestSetFavoritesKeepsCorruptFile(t *testing.T) {
	dataDir := t.TempDir()
	remoteDir := filepath.Join(dataDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote dir: %v", err)
	}
	favPath := filepath.Join(remoteDir, "script-favorites.json")
	corrupt := []byte("{corrupt json\n")
	if err := os.WriteFile(favPath, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt favorites: %v", err)
	}

	b := newProjectScriptsTestBackend(t, dataDir)
	addProjectScriptsTestThread(t, b, "p1", "t1")

	_, err := b.ScriptsSetFavorite(context.Background(), wire.ScriptsSetFavoriteParams{
		ThreadID:   "t1",
		Name:       "build",
		IsFavorite: true,
	})
	if err == nil {
		t.Fatal("expected error when script-favorites.json is corrupt, got nil")
	}

	got, readErr := os.ReadFile(favPath)
	if readErr != nil {
		t.Fatalf("read favorites file: %v", readErr)
	}
	if !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt favorites file was modified: got %q, want %q", got, corrupt)
	}
}

func TestWriteProjectScriptsKeepsCorruptFile(t *testing.T) {
	dataDir := t.TempDir()
	remoteDir := filepath.Join(dataDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote dir: %v", err)
	}
	scriptsPath := filepath.Join(remoteDir, projectScriptsFile)
	corrupt := []byte("{corrupt json\n")
	if err := os.WriteFile(scriptsPath, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt project-scripts: %v", err)
	}

	b := newProjectScriptsTestBackend(t, dataDir)
	addProjectScriptsTestThread(t, b, "p1", "t1")

	err := b.SetProjectScripts("t1", []ProjectScript{{Name: "lint", Command: "make lint"}})
	if err == nil {
		t.Fatal("expected error when project-scripts.json is corrupt, got nil")
	}

	got, readErr := os.ReadFile(scriptsPath)
	if readErr != nil {
		t.Fatalf("read project-scripts file: %v", readErr)
	}
	if !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt project-scripts file was modified: got %q, want %q", got, corrupt)
	}
}
