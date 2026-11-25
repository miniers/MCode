package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogShowCommitAndCommitBase(t *testing.T) {
	t.Parallel()
	r, dir := setupTestRepo(t)
	ctx := context.Background()
	first := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD"))
	runGitOutput(t, r, dir, "mv", "README.md", "renamed file")
	if _, err := r.run(ctx, dir, []string{"GIT_AUTHOR_NAME=Unique Author", "GIT_COMMITTER_NAME=Unique Author"}, "-c", "user.name=Unique Author", "commit", "-qm", "Needle message"); err != nil {
		t.Fatal(err)
	}
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
	t.Parallel()
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
	t.Parallel()
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

func TestLogAllExcludesMcodeRefs(t *testing.T) {
	t.Parallel()
	r, dir := setupTestRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOutput(t, r, dir, "add", "main.txt")
	runGitOutput(t, r, dir, "commit", "-qm", "main commit")

	mcodeSHA := strings.TrimSpace(runGitOutput(t, r, dir, "commit-tree", emptyTree, "-m", "turn x start"))
	runGitOutput(t, r, dir, "update-ref", "refs/mcode/checkpoints/t/x/start", mcodeSHA)

	runGitOutput(t, r, dir, "checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOutput(t, r, dir, "add", "feature.txt")
	runGitOutput(t, r, dir, "commit", "-qm", "feature commit")
	featureSHA := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD"))
	runGitOutput(t, r, dir, "checkout", "-q", "-")

	commits, err := r.Log(ctx, dir, LogOptions{All: true})
	if err != nil {
		t.Fatalf("Log All: %v", err)
	}
	var gotFeature, gotMcode bool
	for _, c := range commits {
		if c.SHA == featureSHA {
			gotFeature = true
		}
		if c.SHA == mcodeSHA || c.Subject == "turn x start" {
			gotMcode = true
		}
	}
	if !gotFeature {
		t.Fatalf("Log All 缺少 feature 提交 %s: %+v", featureSHA, commits)
	}
	if gotMcode {
		t.Fatalf("Log All 不应包含 refs/mcode 提交 %s: %+v", mcodeSHA, commits)
	}
}
