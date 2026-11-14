package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
func TestRevertHunkTouchesOnlySelectedBlock(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	p := filepath.Join(d, "a.txt")
	old := "one\ntwo\nthree\nfour\nfive\n"
	next := "one\nTWO\nthree\nFOUR\nfive\n"
	if err := os.WriteFile(p, []byte(next), 0644); err != nil {
		t.Fatal(err)
	}
	h, _ := RevertHunkHash(next, 2, 1)
	got, err := RevertHunk(d, "a.txt", old, next, 2, 1, h, testHash([]byte(next)))
	if err != nil {
		t.Fatal(err)
	}
	want := "one\ntwo\nthree\nFOUR\nfive\n"
	b, _ := os.ReadFile(p)
	if string(b) != want || got != testHash(b) {
		t.Fatalf("got %q %s", b, got)
	}
}
func TestRevertHunkPreservesIndexAndOtherEdits(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	testGit(t, d, "init")
	testGit(t, d, "config", "core.autocrlf", "false")
	testGit(t, d, "config", "user.name", "test-user")
	testGit(t, d, "config", "user.email", "test@localhost")
	p := filepath.Join(d, "a.txt")
	old := "a\nb\nc\n"
	next := "a\nB\nc\n"
	os.WriteFile(p, []byte(next), 0644)
	testGit(t, d, "add", "a.txt")
	testGit(t, d, "commit", "-m", "init")
	os.WriteFile(p, []byte(next), 0644)
	testGit(t, d, "add", "a.txt")
	working := "a\nB\nC\n"
	os.WriteFile(p, []byte(working), 0644) // snapshot does not match working, so the operation must not write.
	h, _ := RevertHunkHash(next, 2, 1)
	if _, err := RevertHunk(d, "a.txt", old, next, 2, 1, h, testHash([]byte(working))); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "a\nb\nC\n" {
		t.Fatalf("working tree: %q", b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "show", ":0:a.txt")
	cmd.Dir = d
	out, err := cmd.Output()
	if err != nil || string(out) != next {
		t.Fatalf("index changed: %q %v", out, err)
	}
}
func TestRevertHunkExpectedHashConflict(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	old, next := "a\nb\n", "a\nB\n"
	os.WriteFile(filepath.Join(d, "a"), []byte("changed\n"), 0644)
	h, _ := RevertHunkHash(next, 2, 1)
	if _, err := RevertHunk(d, "a", old, next, 2, 1, h, testHash([]byte(next))); err == nil {
		t.Fatal("expected conflict")
	}
}
func TestRevertHunkCRLFAndNoFinalNewline(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	old, next := "a\r\nb\r\nc", "a\r\nB\r\nc"
	os.WriteFile(filepath.Join(d, "a"), []byte(next), 0644)
	h, _ := RevertHunkHash(next, 2, 1)
	if _, err := RevertHunk(d, "a", old, next, 2, 1, h, testHash([]byte(next))); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "a"))
	if string(b) != "a\r\nb\r\nc" {
		t.Fatalf("%q", b)
	}
	if strings.HasSuffix(string(b), "\n") {
		t.Fatal("added final newline")
	}
}
