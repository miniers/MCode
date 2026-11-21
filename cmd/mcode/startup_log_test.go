package main

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
)

func TestRedirectCrashOutput(t *testing.T) {
	t.Cleanup(func() {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	})

	crashPath := filepath.Join(t.TempDir(), crashLogFileName)
	if err := redirectCrashOutput(crashPath); err != nil {
		t.Fatalf("redirectCrashOutput(%q) returned unexpected error: %v", crashPath, err)
	}

	info, err := os.Stat(crashPath)
	if err != nil {
		t.Fatalf("os.Stat(%q) failed: %v", crashPath, err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("expected file mode 0600, got %o", perm)
		}
	}

	missingPath := filepath.Join(t.TempDir(), "nonexistent", crashLogFileName)
	if err := redirectCrashOutput(missingPath); err == nil {
		t.Fatalf("expected error for missing directory %q, got nil", missingPath)
	}
}
