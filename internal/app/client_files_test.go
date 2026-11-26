package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func clientFilesTestSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestClientFilesWrite_Success(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()
	target := filepath.Join(workDir, "notes.txt")
	if err := os.WriteFile(target, []byte("old content"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	ctx := context.Background()
	application, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer application.Close()

	client := application.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	hash, err := client.FilesWrite(ctx, threadID, "notes.txt", "new content\n", clientFilesTestSHA256("old content"))
	if err != nil {
		t.Fatalf("FilesWrite failed: %v", err)
	}
	if want := clientFilesTestSHA256("new content\n"); hash != want {
		t.Fatalf("hash = %q, want %q", hash, want)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "new content\n" {
		t.Fatalf("file content = %q, want %q", got, "new content\n")
	}
}

func TestClientFilesWrite_ExpectedHashConflict(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()
	target := filepath.Join(workDir, "conflict.txt")
	if err := os.WriteFile(target, []byte("current"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	ctx := context.Background()
	application, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer application.Close()

	client := application.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	_, err = client.FilesWrite(ctx, threadID, "conflict.txt", "mine", clientFilesTestSHA256("stale"))
	if err == nil {
		t.Fatal("expected error on expectedHash mismatch, got nil")
	}
	if !errors.Is(err, mcodeui.ErrFileVersionConflict) {
		t.Fatalf("expected ErrFileVersionConflict, got %v", err)
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeVersionConflict {
		t.Fatalf("expected RPCError -32009, got %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile failed: %v", readErr)
	}
	if string(got) != "current" {
		t.Fatalf("file must remain unchanged, got %q", got)
	}
}
