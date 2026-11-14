package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBlame(t *testing.T) {
	t.Parallel()
	r, dir := setupTestRepo(t)
	p := filepath.Join(dir, "README.md")
	if e := os.WriteFile(p, []byte("first\nsecond\n"), 0644); e != nil {
		t.Fatal(e)
	}
	runGitOutput(t, r, dir, "add", "README.md")
	runGitOutput(t, r, dir, "commit", "-qm", "first")
	if e := os.WriteFile(p, []byte("first\nchanged\n"), 0644); e != nil {
		t.Fatal(e)
	}
	runGitOutput(t, r, dir, "commit", "-qam", "second")
	if e := os.WriteFile(p, []byte("first\nchanged\nnew\n"), 0644); e != nil {
		t.Fatal(e)
	}
	lines, e := r.Blame(context.Background(), dir, "README.md", "")
	if e != nil || len(lines) != 3 {
		t.Fatalf("blame: %+v %v", lines, e)
	}
	if lines[0].SHA == lines[1].SHA || lines[2].Author != "未提交" {
		t.Fatalf("归属: %+v", lines)
	}
	blocks := BlameBlocks([]BlameLine{{Line: 1, SHA: "a"}, {Line: 2, SHA: "a"}, {Line: 3, SHA: "b"}})
	if len(blocks) != 2 || blocks[0].End != 2 {
		t.Fatalf("块: %+v", blocks)
	}
}
