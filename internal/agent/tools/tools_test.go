package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runTool(t *testing.T, env Env, name string, args any) Result {
	t.Helper()
	b, e := json.Marshal(args)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range CodingTools() {
		if tool.Name == name {
			r, e := tool.Run(context.Background(), env, b)
			if e != nil {
				t.Fatal(e)
			}
			return r
		}
	}
	t.Fatalf("missing tool %s", name)
	return Result{}
}
func put(t *testing.T, path, content string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(content), 0644); e != nil {
		t.Fatal(e)
	}
}
func TestPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	put(t, filepath.Join(outside, "secret"), "secret")
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Skip(e)
	}
	env := Env{WorkDir: root}
	for _, path := range []string{filepath.Join(outside, "secret"), "link/secret", "link/new"} {
		name := "read"
		args := map[string]any{"path": path}
		if strings.HasSuffix(path, "new") {
			name = "write"
			args["content"] = "x"
		}
		if r := runTool(t, env, name, args); !r.IsError {
			t.Fatalf("allowed %s", path)
		}
	}
	r := runTool(t, env, "grep", map[string]any{"pattern": "secret"})
	if strings.Contains(r.Text, "secret") {
		t.Fatal(r.Text)
	}
}
func TestReadAndList(t *testing.T) {
	root := t.TempDir()
	env := Env{WorkDir: root}
	put(t, filepath.Join(root, "a"), "one\ntwo\nthree\n")
	r := runTool(t, env, "read", map[string]any{"path": "a", "offset": 2, "limit": 1})
	if !strings.Contains(r.Text, "2: two") || strings.Contains(r.Text, "1: one") {
		t.Fatal(r.Text)
	}
	put(t, filepath.Join(root, "large"), strings.Repeat("abc\n", 2100))
	r = runTool(t, env, "read", map[string]any{"path": "large"})
	if !strings.Contains(r.Text, "truncated") || !strings.Contains(r.Text, "offset") {
		t.Fatal("missing truncation")
	}
	if e := os.Mkdir(filepath.Join(root, "dir"), 0755); e != nil {
		t.Fatal(e)
	}
	r = runTool(t, env, "ls", map[string]any{})
	if !strings.Contains(r.Text, "dir/") {
		t.Fatal(r.Text)
	}
}
func TestEdit(t *testing.T) {
	root := t.TempDir()
	env := Env{WorkDir: root}
	path := filepath.Join(root, "a")
	put(t, path, "hello\r\n“world”   \r\nbye\r\n")
	r := runTool(t, env, "edit", map[string]any{"path": "a", "edits": []textEdit{{"hello", "hi"}, {"\"world\"\nbye", "you\nbye"}}})
	if r.IsError {
		t.Fatal(r.Text)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "hi\r\nyou\r\nbye\r\n" {
		t.Fatalf("%q", b)
	}
	put(t, path, "abc abc")
	r = runTool(t, env, "edit", map[string]any{"path": "a", "edits": []textEdit{{"abc", "x"}}})
	if !r.IsError || !strings.Contains(r.Text, "2 times") {
		t.Fatal(r.Text)
	}
	put(t, path, "abcdef")
	r = runTool(t, env, "edit", map[string]any{"path": "a", "edits": []textEdit{{"abcd", "x"}, {"cdef", "y"}}})
	if !r.IsError || !strings.Contains(r.Text, "overlaps") {
		t.Fatal(r.Text)
	}
}
func TestWriteDiff(t *testing.T) {
	root := t.TempDir()
	env := Env{WorkDir: root}
	path := filepath.Join(root, "a")
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	put(t, path, old)
	r := runTool(t, env, "write", map[string]any{"path": "a", "content": strings.Replace(old, "f\n", "F\n", 1)})
	if r.IsError || r.Change == nil || !strings.Contains(r.Change.Patch, "-f\n+F\n") || strings.Contains(r.Change.Patch, "-a\n") {
		t.Fatalf("%+v", r)
	}
}
func TestGrep(t *testing.T) {
	root := t.TempDir()
	env := Env{WorkDir: root}
	put(t, filepath.Join(root, "a.txt"), "before\nA.B\naXb\nafter\n")
	put(t, filepath.Join(root, "b.go"), "a.b\n")
	r := runTool(t, env, "grep", map[string]any{"pattern": "a.b", "literal": true, "ignoreCase": true, "context": 1, "glob": "*.txt"})
	if r.IsError || !strings.Contains(r.Text, "a.txt:2:A.B") || !strings.Contains(r.Text, "a.txt:1:before") || strings.Contains(r.Text, "b.go") {
		t.Fatal(r.Text)
	}
}
func TestFindGitIgnore(t *testing.T) {
	if _, e := exec.LookPath("git"); e != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if e := cmd.Run(); e != nil {
		t.Skip(e)
	}
	put(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	put(t, filepath.Join(root, "ignored.txt"), "x")
	put(t, filepath.Join(root, "visible.txt"), "x")
	r := runTool(t, Env{WorkDir: root}, "find", map[string]any{"pattern": "*.txt"})
	if r.IsError || strings.Contains(r.Text, "ignored.txt") || !strings.Contains(r.Text, "visible.txt") {
		t.Fatal(r.Text)
	}
}
