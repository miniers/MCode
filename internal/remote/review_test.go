package remote

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

type reviewFixture struct {
	t        *testing.T
	root, id string
	b        *MemoryBackend
	ctx      context.Context
}

func newReviewFixture(t *testing.T, born bool) *reviewFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &reviewFixture{t: t, root: t.TempDir(), ctx: ctx}
	f.git("init")
	f.git("config", "user.name", "review")
	f.git("config", "user.email", "review@localhost")
	if born {
		f.write("a.go", "package old\n")
		f.git("add", "--", ".")
		f.git("commit", "-m", "base")
	}
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)
	f.b = NewMemoryBackend(hub)
	t.Cleanup(f.b.Close)
	pid := newUUID()
	f.b.AddProject(wire.ProjectSummary{ID: pid, Name: "review", Path: f.root})
	thread, err := f.b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: pid, ActionID: newUUID()})
	if err != nil {
		t.Fatal(err)
	}
	f.id = thread.ThreadID
	return f
}
func (f *reviewFixture) git(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = f.root
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	o, e := c.CombinedOutput()
	if e != nil {
		f.t.Fatalf("git %v: %v %s", args, e, o)
	}
	return strings.TrimSpace(string(o))
}
func (f *reviewFixture) write(path, text string) {
	f.t.Helper()
	if e := os.WriteFile(filepath.Join(f.root, path), []byte(text), 0600); e != nil {
		f.t.Fatal(e)
	}
}
func (f *reviewFixture) checkpoint(turn, kind string) {
	f.t.Helper()
	_, e := f.b.git.Checkpoint(f.ctx, f.root, git.CheckpointRef(f.id, turn)+"/"+kind, kind)
	if e != nil {
		f.t.Fatal(e)
	}
}
func (f *reviewFixture) order(turns ...string) {
	f.b.mu.Lock()
	defer f.b.mu.Unlock()
	td := f.b.threads[f.id]
	td.messages = nil
	for i, id := range turns {
		id := id
		td.messages = append(td.messages, wire.ThreadMessage{TurnID: &id, TurnIndex: i})
	}
}
func (f *reviewFixture) get(scope wire.ReviewScope, turn, path string) wire.ThreadsReviewGetResult {
	f.t.Helper()
	r, e := f.b.ReviewGet(f.ctx, wire.ThreadsReviewGetParams{ThreadID: f.id, Scope: scope, TurnID: turn, Path: path})
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func reviewPaths(r wire.ThreadsReviewGetResult) []string {
	p := []string{}
	for _, f := range r.Files {
		p = append(p, f.Path)
	}
	return p
}

func TestReviewScopeTurnSessionUncommitted(t *testing.T) {
	f := newReviewFixture(t, true)
	t1, t2 := newUUID(), newUUID()
	f.order(t1, t2)
	f.checkpoint(t1, "start")
	f.write("a.go", "package first\n")
	f.checkpoint(t1, "end")
	f.checkpoint(t2, "start")
	f.write("b.txt", "second\n")
	f.checkpoint(t2, "end")
	f.git("add", "--", ".")
	f.git("commit", "-m", "turns")
	f.write("c.txt", "staged\n")
	f.git("add", "--", "c.txt")
	f.write("a.go", "package unstaged\n")
	f.write("u.txt", "untracked\n")
	for _, tc := range []struct {
		s     wire.ReviewScope
		turn  string
		paths []string
	}{
		{wire.ReviewScopeTurn, t1, []string{"a.go"}}, {wire.ReviewScopeTurn, "", []string{"b.txt"}}, {wire.ReviewScopeSession, "", []string{"a.go", "b.txt"}}, {wire.ReviewScopeUncommitted, "", []string{"a.go", "c.txt", "u.txt"}},
	} {
		r := f.get(tc.s, tc.turn, "")
		if r.Unavailable != "" || !reflect.DeepEqual(reviewPaths(r), tc.paths) || r.BaseRevision == "" || r.HeadRevision == "" {
			t.Fatalf("%s: %+v", tc.s, r)
		}
	}
	// 运行中只允许本轮使用当前树；整个会话仍停在最后有效结束点。
	f.b.mu.Lock()
	td := f.b.threads[f.id]
	td.summary.IsRunning = true
	td.runTurnID = t2
	f.b.mu.Unlock()
	r := f.get(wire.ReviewScopeTurn, "", "a.go")
	if !r.Running || r.TurnID != t2 || r.File.NewText != "package unstaged\n" {
		t.Fatalf("运行快照: %+v", r)
	}
	if r := f.get(wire.ReviewScopeSession, "", ""); r.Running || !reflect.DeepEqual(reviewPaths(r), []string{"a.go", "b.txt"}) {
		t.Fatalf("会话范围被覆盖: %+v", r)
	}
}
func TestReviewWorktreeScopeNotOverridden(t *testing.T) {
	f := newReviewFixture(t, true)
	main := f.root
	wt := filepath.Join(t.TempDir(), "worktree")
	f.git("worktree", "add", "--detach", wt)
	f.b.mu.Lock()
	f.b.threads[f.id].workDir = wt
	f.b.mu.Unlock()
	f.root = wt
	turn := newUUID()
	f.order(turn)
	f.checkpoint(turn, "start")
	f.write("a.go", "package worktree\n")
	f.checkpoint(turn, "end")
	f.write("later.txt", "later\n")
	r := f.get(wire.ReviewScopeSession, "", "a.go")
	if r.File.NewText != "package worktree\n" || !reflect.DeepEqual(reviewPaths(r), []string{"a.go"}) {
		t.Fatalf("worktree 会话: %+v", r)
	}
	if _, e := os.Stat(filepath.Join(main, "later.txt")); !os.IsNotExist(e) {
		t.Fatal("主仓库被修改")
	}
}
func TestReviewUntrackedAndUnbornBranch(t *testing.T) {
	f := newReviewFixture(t, false)
	f.write("staged.txt", "staged\n")
	f.git("add", "--", "staged.txt")
	f.write("new.txt", "new\n")
	before := f.git("status", "--porcelain")
	r := f.get(wire.ReviewScopeUncommitted, "", "")
	if len(r.Files) != 2 || r.Files[0].Status != "?" || r.Files[1].Status != "A" {
		t.Fatalf("未出生分支: %+v", r)
	}
	if after := f.git("status", "--porcelain"); after != before {
		t.Fatalf("索引被修改: %q -> %q", before, after)
	}
}
func TestReviewSnapshotFullTextAndLimits(t *testing.T) {
	f := newReviewFixture(t, true)
	f.write("a.go", "package new\n")
	f.write("binary.dat", "\x00\x01\x02")
	f.write("large.txt", strings.Repeat("x", git.ReviewMaxTextBytes+1))
	r := f.get(wire.ReviewScopeUncommitted, "", "")
	raw, _ := json.Marshal(r)
	if r.File != nil || strings.Contains(string(raw), "oldText") || strings.Contains(string(raw), "newText") {
		t.Fatal("列表含全文")
	}
	r = f.get(wire.ReviewScopeUncommitted, "", "a.go")
	if r.File.OldText != "package old\n" || r.File.NewText != "package new\n" || r.File.OldHash == r.File.NewHash || r.File.NewHash == "" {
		t.Fatalf("全文: %+v", r.File)
	}
	for _, path := range []string{"binary.dat", "large.txt"} {
		r = f.get(wire.ReviewScopeUncommitted, "", path)
		if r.File.OldText != "" || r.File.NewText != "" || (!r.File.Binary && !r.File.TooLarge) {
			t.Fatalf("受限全文: %+v", r.File)
		}
	}
	// 后续写入得到新版本；之前返回的快照不变。
	old := r.HeadRevision
	f.write("a.go", "package newer\n")
	if r = f.get(wire.ReviewScopeUncommitted, "", "a.go"); r.HeadRevision == old || r.File.NewText != "package newer\n" {
		t.Fatal("快照版本未更新")
	}
}
func TestReviewMissingCheckpointNotWorkingTree(t *testing.T) {
	f := newReviewFixture(t, true)
	f.write("a.go", "package dirty\n")
	for _, scope := range []wire.ReviewScope{wire.ReviewScopeTurn, wire.ReviewScopeSession} {
		r := f.get(scope, "", "")
		if r.Unavailable == "" || len(r.Files) != 0 {
			t.Fatalf("缺 checkpoint: %+v", r)
		}
	}
	turn := newUUID()
	f.order(turn)
	f.checkpoint(turn, "start")
	if r := f.get(wire.ReviewScopeTurn, turn, ""); r.Unavailable == "" {
		t.Fatalf("已结束轮缺 end: %+v", r)
	}
}
func TestReviewPathMustBeInScope(t *testing.T) {
	f := newReviewFixture(t, true)
	f.write("new.txt", "new\n")
	for _, path := range []string{"a.go", "../secret", ".git/config", ":(glob)*", "/etc/passwd"} {
		_, e := f.b.ReviewGet(f.ctx, wire.ThreadsReviewGetParams{ThreadID: f.id, Scope: wire.ReviewScopeUncommitted, Path: path})
		ext, ok := e.(*ExtError)
		if !ok || ext.Code != wire.InvalidParams {
			t.Fatalf("%q: %v", path, e)
		}
	}
}

func TestReviewRenameDeleteSymlinkAndNetChanges(t *testing.T) {
	f := newReviewFixture(t, true)
	f.write("delete.txt", "remove\n")
	f.write("rename.txt", "rename contents\n")
	f.git("add", "--", ".")
	f.git("commit", "-m", "files")
	f.write("a.go", "package staged\n")
	f.git("add", "--", "a.go")
	f.write("a.go", "package old\n")
	if err := os.Rename(filepath.Join(f.root, "rename.txt"), filepath.Join(f.root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	f.git("add", "--", "rename.txt", "renamed.txt")
	if err := os.Remove(filepath.Join(f.root, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(f.root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	r := f.get(wire.ReviewScopeUncommitted, "", "renamed.txt")
	if len(r.Files) != 3 || r.File.OldText != "rename contents\n" || r.File.NewText != "rename contents\n" {
		t.Fatalf("净变化或重命名正文不符: %+v", r)
	}
	for _, file := range r.Files {
		if file.Path == "renamed.txt" && (file.Status != "R" || file.OrigPath != "rename.txt") {
			t.Fatalf("重命名元数据: %+v", file)
		}
		if file.Path == "a.go" {
			t.Fatal("staged 与 unstaged 抵消后仍有改动")
		}
	}
	r = f.get(wire.ReviewScopeUncommitted, "", "delete.txt")
	if r.File.OldText != "remove\n" || r.File.NewText != "" || r.File.NewHash != "" {
		t.Fatalf("删除文件正文: %+v", r.File)
	}
	r = f.get(wire.ReviewScopeUncommitted, "", "link.txt")
	if r.File.NewText != "/etc/passwd" {
		t.Fatal("符号链接被跟随读取")
	}
}

func TestReviewConcurrentSnapshots(t *testing.T) {
	f := newReviewFixture(t, true)
	turn := newUUID()
	f.order(turn)
	f.checkpoint(turn, "start")
	f.write("a.go", "package finished\n")
	f.checkpoint(turn, "end")
	f.write("a.go", "package running\n")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				r, err := f.b.ReviewGet(f.ctx, wire.ThreadsReviewGetParams{ThreadID: f.id, Scope: wire.ReviewScopeTurn, TurnID: turn, Path: "a.go"})
				if err != nil || r.Unavailable != "" || r.File == nil {
					t.Errorf("并发快照失败: %+v %v", r, err)
					return
				}
				want := "package finished\n"
				if r.Running {
					want = "package running\n"
				}
				if r.File.NewText != want || r.File.NewHash != r.Files[0].NewHash {
					t.Errorf("运行状态与快照内容不一致: %+v", r)
				}
			}
		}()
	}
	for i := range 20 {
		f.b.mu.Lock()
		td := f.b.threads[f.id]
		td.summary.IsRunning = i%2 == 0
		td.runTurnID = turn
		f.b.mu.Unlock()
	}
	wg.Wait()
}
