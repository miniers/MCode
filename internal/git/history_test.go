package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogShowCommitAndCommitBase(t *testing.T) {
	r, dir := setupTestRepo(t)
	ctx := context.Background()
	first := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD"))
	runGitOutput(t, r, dir, "mv", "README.md", "renamed file")
	runGitOutput(t, r, dir, "-c", "user.name=Unique Author", "commit", "-qm", "Needle message")
	second := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD"))
	all, err := r.Log(ctx, dir, LogOptions{})
	if err != nil || len(all) != 2 {
		t.Fatalf("log: %+v %v", all, err)
	}
	if len(all[0].Refs) == 0 {
		t.Fatal("缺少引用")
	}
	for _, q := range []string{"needle", "unique author", second[:8]} {
		v, e := r.Log(ctx, dir, LogOptions{Query: q})
		if e != nil || len(v) != 1 || v[0].SHA != second {
			t.Fatalf("query %q: %+v %v", q, v, e)
		}
	}
	v, e := r.Log(ctx, dir, LogOptions{Skip: 1, Limit: 1})
	if e != nil || len(v) != 1 || v[0].SHA != first {
		t.Fatalf("分页: %+v %v", v, e)
	}
	v, e = r.Log(ctx, dir, LogOptions{Path: "renamed file"})
	if e != nil || len(v) != 1 {
		t.Fatalf("路径: %+v %v", v, e)
	}
	d, e := r.ShowCommit(ctx, dir, second)
	if e != nil || len(d.Files) != 1 || d.Files[0].Status != "R" || d.Files[0].OrigPath != "README.md" {
		t.Fatalf("重命名: %+v %v", d, e)
	}
	d, e = r.ShowCommit(ctx, dir, first)
	if e != nil || len(d.Files) == 0 || d.Files[0].Status != "A" {
		t.Fatalf("根提交: %+v %v", d, e)
	}
	b, e := r.CommitBase(ctx, dir, first)
	if e != nil || b != emptyTree {
		t.Fatalf("根基底: %q %v", b, e)
	}
}
func TestLogEmptyAndUnpushed(t *testing.T) {
	r := NewRunner("", 4)
	dir := t.TempDir()
	runGitOutput(t, r, dir, "init", "-q")
	v, e := r.Log(context.Background(), dir, LogOptions{})
	if e != nil || len(v) != 0 {
		t.Fatalf("空仓库: %+v %v", v, e)
	}
	r, dir, _ = initSyncRemote(t)
	if e := os.WriteFile(filepath.Join(dir, "new"), []byte("new\n"), 0644); e != nil {
		t.Fatal(e)
	}
	runGitOutput(t, r, dir, "add", "new")
	runGitOutput(t, r, dir, "commit", "-qm", "new")
	v, e = r.Log(context.Background(), dir, LogOptions{})
	if e != nil || !v[0].Unpushed || v[1].Unpushed {
		t.Fatalf("未推送: %+v %v", v, e)
	}
	runGitOutput(t, r, dir, "push", "-q", "origin", "HEAD")
	v, e = r.Log(context.Background(), dir, LogOptions{})
	if e != nil || v[0].Unpushed {
		t.Fatalf("推送后: %+v %v", v, e)
	}
}

func TestLogRefs(t *testing.T) {
	refs := parseRefs("HEAD -> refs/heads/main, refs/remotes/origin/main, tag: refs/tags/v1")
	want := []LogRef{{"HEAD", "head"}, {"main", "local"}, {"origin/main", "remote"}, {"v1", "tag"}}
	if len(refs) != len(want) {
		t.Fatalf("refs: %+v", refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("refs: %+v", refs)
		}
	}
}
