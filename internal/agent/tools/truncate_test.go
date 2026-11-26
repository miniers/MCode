package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTruncateSpillWritesFullOutput(t *testing.T) {
	root := t.TempDir()
	previous := toolOutputTempDir
	toolOutputTempDir = func() string { return root }
	defer func() { toolOutputTempDir = previous }()

	input := strings.Repeat("a", maxOutputBytes+1024)
	notice := "Output truncated."
	got := truncate(input, notice)

	const marker = " Full output saved to "
	if !strings.Contains(got, notice) || !strings.Contains(got, marker) {
		t.Fatalf("unexpected truncated output: %q", got)
	}
	_, rest, ok := strings.Cut(got, marker)
	if !ok {
		t.Fatalf("missing saved path marker in %q", got)
	}
	path, _, ok := strings.Cut(rest, "; read it with offset/limit.")
	if !ok {
		t.Fatalf("missing saved path suffix in %q", rest)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved output %q: %v", path, err)
	}
	if string(data) != input {
		t.Fatalf("saved output length=%d, want %d", len(data), len(input))
	}
}

func TestTruncateSpillNoFileWhenShort(t *testing.T) {
	root := t.TempDir()
	previous := toolOutputTempDir
	toolOutputTempDir = func() string { return root }
	defer func() { toolOutputTempDir = previous }()

	input := "short output\nline 2\n"
	got := truncate(input, "Output truncated.")
	if got != input {
		t.Fatalf("got %q, want %q", got, input)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read temp root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files in %q, got %d", root, len(entries))
	}
}

func TestTruncateSpillFallbackOnWriteError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	previous := toolOutputTempDir
	toolOutputTempDir = func() string { return blocker }
	defer func() { toolOutputTempDir = previous }()

	input := strings.Repeat("b", maxOutputBytes+100)
	notice := "Output truncated."
	got := truncate(input, notice)
	want := input[:maxOutputBytes] + "\n" + notice
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "Full output saved to") {
		t.Fatalf("unexpected spill notice on write error: %q", got)
	}
}
