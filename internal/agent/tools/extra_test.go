package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEditBOMAndPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	put(t, path, "\xef\xbb\xbfhello\n")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	result := runTool(t, Env{WorkDir: root}, "edit", map[string]any{"path": "file", "edits": []textEdit{{"hello", "world"}}})
	if result.IsError {
		t.Fatal(result.Text)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "\xef\xbb\xbfworld\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestWriteDirectoriesAndPermissions(t *testing.T) {
	root := t.TempDir()
	env := Env{WorkDir: root}
	result := runTool(t, env, "write", map[string]any{"path": "nested/file", "content": "first"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	path := filepath.Join(root, "nested", "file")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	result = runTool(t, env, "write", map[string]any{"path": "nested/file", "content": "second"})
	info, err := os.Stat(path)
	if result.IsError || err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("result=%+v info=%v err=%v", result, info, err)
	}
}

func TestGrepRejectsOutsideFileSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	put(t, outside, "private-marker")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	result := runTool(t, Env{WorkDir: root}, "grep", map[string]any{"pattern": "private-marker"})
	if result.IsError || strings.Contains(result.Text, "private-marker") {
		t.Fatal(result.Text)
	}
}

func TestGrepAndListLimits(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "a"), "match\nmatch\n")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	env := Env{WorkDir: root}
	result := runTool(t, env, "grep", map[string]any{"pattern": "match", "limit": 1})
	if !strings.Contains(result.Text, "a:1:match") || strings.Contains(result.Text, "a:2:match") || !strings.Contains(result.Text, "truncated") {
		t.Fatal(result.Text)
	}
	result = runTool(t, env, "ls", map[string]any{"limit": 1})
	if !strings.Contains(result.Text, "truncated") || strings.Contains(result.Text, "dir/") {
		t.Fatal(result.Text)
	}
}

func TestBashTool(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("需要 Unix shell")
	}
	root := t.TempDir()
	var streamed strings.Builder
	env := Env{WorkDir: root, ExtraEnv: []string{"MCODE_BASH_TEST=value"}, OnOutput: func(chunk string) { streamed.WriteString(chunk) }}
	result := runTool(t, env, "bash", map[string]any{"command": "printf '%s' \"$MCODE_BASH_TEST\""})
	if result.IsError || result.Text != "value" || streamed.String() != "value" {
		t.Fatalf("result=%+v streamed=%q", result, streamed.String())
	}
	result = runTool(t, env, "bash", map[string]any{"command": "printf failed; exit 7"})
	if !result.IsError || !strings.Contains(result.Text, "failed") || !strings.Contains(result.Text, "7") {
		t.Fatal(result.Text)
	}
	result = runTool(t, env, "bash", map[string]any{"command": "sleep 30", "timeout": 0.05})
	if !result.IsError || !strings.Contains(result.Text, "timed out") {
		t.Fatal(result.Text)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := json.RawMessage(`{"command":"echo test"}`)
	result, err := BashTool().Run(ctx, env, raw)
	if err != nil || !result.IsError || !strings.Contains(result.Text, "cancelled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
