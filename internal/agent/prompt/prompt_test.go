package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestContextCandidateOrderAndGitRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	work := filepath.Join(repo, "one", "two")
	put(t, filepath.Join(repo, ".git", "marker"), "")
	put(t, filepath.Join(base, "AGENTS.md"), "outside")
	put(t, filepath.Join(repo, "AGENTS.override.md"), "override")
	put(t, filepath.Join(repo, "AGENTS.md"), "agents")
	put(t, filepath.Join(repo, "CLAUDE.md"), "claude")
	put(t, filepath.Join(repo, "one", "AGENTS.override.md"), "nested")
	put(t, filepath.Join(work, "CLAUDE.md"), "work")
	files := ContextFiles(Options{WorkDir: work})
	if len(files) != 3 || !strings.Contains(files[0].Content, "override") || !strings.Contains(files[1].Content, "nested") || !strings.Contains(files[2].Content, "work") {
		t.Fatalf("unexpected context files: %#v", files)
	}
	for _, file := range files {
		if strings.Contains(file.Path, filepath.Join(base, "AGENTS.md")) {
			t.Fatalf("walked above git root: %s", file.Path)
		}
	}
}

func TestContextGlobalFirstAndHomeBoundary(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	global := filepath.Join(base, "global")
	work := filepath.Join(home, "project", "child")
	put(t, filepath.Join(global, "CLAUDE.md"), "global")
	put(t, filepath.Join(home, "AGENTS.md"), "home")
	put(t, filepath.Join(filepath.Dir(home), "AGENTS.md"), "above")
	put(t, filepath.Join(work, "AGENTS.md"), "work")
	files := ContextFiles(Options{WorkDir: work, HomeDir: home, GlobalDir: global})
	if len(files) != 3 || !strings.Contains(files[0].Content, "global") || !strings.Contains(files[1].Content, "home") || !strings.Contains(files[2].Content, "work") {
		t.Fatalf("unexpected ordered context: %#v", files)
	}
}

func TestContextSizeLimits(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	put(t, filepath.Join(root, ".git", "marker"), "")
	put(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("a", maxContextFileBytes+10))
	files := ContextFiles(Options{WorkDir: root})
	if len(files) != 1 || !strings.Contains(files[0].Content, "Truncated") || len(files[0].Content) <= maxContextFileBytes {
		t.Fatalf("single-file truncation missing: %#v", files)
	}
	put(t, filepath.Join(root, "child", "AGENTS.md"), strings.Repeat("b", maxContextFileBytes))
	put(t, filepath.Join(root, "child", "leaf", "AGENTS.md"), strings.Repeat("c", maxContextFileBytes))
	files = ContextFiles(Options{WorkDir: filepath.Join(root, "child", "leaf")})
	var total int
	for _, file := range files {
		total += len(file.Content)
	}
	if total > maxContextTotalBytes || len(files) != 2 {
		t.Fatalf("total size limit should discard farthest ancestor; got %d bytes in %d files", total, len(files))
	}
}

func TestBuildPromptContents(t *testing.T) {
	work := t.TempDir()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	prompt := Build(Options{WorkDir: work, ToolNames: []string{"read", "bash", "edit", "write", "grep", "find", "ls", "ask_user_question", "todo_write", "unknown"}, Now: now, OS: "test-os"})
	for _, expected := range []string{work, "2026-11-08", "test-os", "- read:", "- bash:", "- edit:", "- write:", "- grep:", "- find:", "- ls:", "- ask_user_question:", "- todo_write:"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q", expected)
		}
	}
	if strings.Contains(prompt, "unknown") || strings.Contains(prompt, "Available skills:") {
		t.Fatalf("prompt included unavailable tool or empty skill section: %s", prompt)
	}
	partial := Build(Options{ToolNames: []string{"read", "unknown"}, Now: now})
	if strings.Contains(partial, "- unknown:") || !strings.Contains(partial, "- read:") {
		t.Fatalf("tool list did not filter to available tools: %s", partial)
	}
}
