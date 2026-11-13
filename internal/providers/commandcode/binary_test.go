package commandcode

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveBinaryWindows(t *testing.T) {
	root := t.TempDir()
	system := filepath.Join(root, "Windows")
	sysdir := filepath.Join(system, "System32")
	npm := filepath.Join(root, "npm")
	for _, dir := range []string{sysdir, npm} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(sysdir, "cmd.exe"), filepath.Join(npm, "cmd.cmd")} {
		if err := os.WriteFile(p, []byte("test"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveBinaryIn("windows", []string{sysdir, npm}, system)
	if err != nil || got != filepath.Join(npm, "cmd.cmd") {
		t.Fatalf("got %q: %v", got, err)
	}
	_, err = resolveBinaryIn("windows", []string{sysdir}, system)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatal(err)
	}
}
func TestCanceledStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isStopped(ctx, nil) {
		t.Fatal("cancel should stop")
	}
	if isStopped(context.Background(), nil) {
		t.Fatal("normal completion should not stop")
	}
}
