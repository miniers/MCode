package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runApplyPatch(t *testing.T, root, input string) Result {
	t.Helper()
	args, err := json.Marshal(map[string]string{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyPatchTool().Run(context.Background(), Env{WorkDir: root}, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func writePatchFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readPatchFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplyPatchToolFiles(t *testing.T) {
	root := t.TempDir()
	writePatchFile(t, filepath.Join(root, "a.txt"), "one\ntwo\n")
	writePatchFile(t, filepath.Join(root, "b.txt"), "old\n")
	writePatchFile(t, filepath.Join(root, "del.txt"), "gone\n")

	input := `*** Begin Patch
*** Add File: sub/new.txt
+hello
+world
*** Update File: a.txt
@@
-one
+ONE
*** Update File: b.txt
*** Move to: moved.txt
@@
-old
+new
*** Delete File: del.txt
*** End Patch`
	result := runApplyPatch(t, root, input)
	if result.IsError {
		t.Fatal(result.Text)
	}
	if got := readPatchFile(t, filepath.Join(root, "sub/new.txt")); got != "hello\nworld\n" {
		t.Fatalf("new.txt = %q", got)
	}
	if got := readPatchFile(t, filepath.Join(root, "a.txt")); got != "ONE\ntwo\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt still exists: %v", err)
	}
	if got := readPatchFile(t, filepath.Join(root, "moved.txt")); got != "new\n" {
		t.Fatalf("moved.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "del.txt")); !os.IsNotExist(err) {
		t.Fatalf("del.txt still exists: %v", err)
	}
	for _, want := range []string{"Success.", "Added: sub/new.txt", "Updated: a.txt", "b.txt -> moved.txt", "Deleted: del.txt"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("output %q missing %q", result.Text, want)
		}
	}
	if result.Change != nil {
		t.Fatalf("change = %+v, want per-file changes", result.Change)
	}
	wantChanges := map[string]bool{
		filepath.Join(root, "sub/new.txt"): false,
		filepath.Join(root, "a.txt"):       false,
		filepath.Join(root, "moved.txt"):   false,
		filepath.Join(root, "del.txt"):     false,
	}
	if len(result.Changes) != len(wantChanges) {
		t.Fatalf("changes = %+v", result.Changes)
	}
	for _, change := range result.Changes {
		seen, ok := wantChanges[change.Path]
		if !ok || seen || !strings.Contains(change.Patch, "--- a/") {
			t.Fatalf("change = %+v", change)
		}
		wantChanges[change.Path] = true
	}
	if lines := strings.Count(ApplyPatchTool().Description, "\n") + 1; lines > 10 {
		t.Fatalf("description has %d lines", lines)
	}
}

func TestApplyPatchToolAtomic(t *testing.T) {
	root := t.TempDir()
	writePatchFile(t, filepath.Join(root, "a.txt"), "old\n")
	input := `*** Begin Patch
*** Update File: a.txt
@@
-old
+new
*** Update File: missing.txt
@@
-x
+y
*** End Patch`
	result := runApplyPatch(t, root, input)
	if !result.IsError {
		t.Fatal("expected error")
	}
	if got := readPatchFile(t, filepath.Join(root, "a.txt")); got != "old\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "missing.txt")); !os.IsNotExist(err) {
		t.Fatalf("missing.txt was written: %v", err)
	}
}

func TestApplyPatchToolOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "added.txt")
	result := runApplyPatch(t, root, "*** Begin Patch\n*** Add File: "+target+"\n+x\n*** End Patch")
	if !result.IsError || !strings.Contains(result.Text, "outside allowed directories") {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("outside file was written: %v", err)
	}

	writePatchFile(t, filepath.Join(root, "a.txt"), "old\n")
	moved := filepath.Join(outside, "moved.txt")
	input := "*** Begin Patch\n*** Update File: a.txt\n*** Move to: " + moved + "\n@@\n-old\n+new\n*** End Patch"
	result = runApplyPatch(t, root, input)
	if !result.IsError {
		t.Fatal("expected error")
	}
	if got := readPatchFile(t, filepath.Join(root, "a.txt")); got != "old\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if _, err := os.Stat(moved); !os.IsNotExist(err) {
		t.Fatalf("outside move target was written: %v", err)
	}
}

func TestApplyPatchToolMultipleChanges(t *testing.T) {
	root := t.TempDir()
	writePatchFile(t, filepath.Join(root, "a.txt"), "old\n")
	writePatchFile(t, filepath.Join(root, "b.txt"), "old\n")
	input := `*** Begin Patch
*** Update File: a.txt
@@
-old
+new
*** Update File: b.txt
@@
-old
+new
*** End Patch`
	result := runApplyPatch(t, root, input)
	if result.IsError {
		t.Fatal(result.Text)
	}
	if len(result.Changes) != 2 || result.Change != nil {
		t.Fatalf("changes = %+v change = %+v", result.Changes, result.Change)
	}
	if result.Changes[0].Path != filepath.Join(root, "a.txt") || result.Changes[1].Path != filepath.Join(root, "b.txt") {
		t.Fatalf("changes = %+v", result.Changes)
	}
	for _, change := range result.Changes {
		if !strings.Contains(change.Patch, "-old") || !strings.Contains(change.Patch, "+new") {
			t.Fatalf("patch = %q", change.Patch)
		}
	}
}
