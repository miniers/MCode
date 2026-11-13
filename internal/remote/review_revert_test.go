package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

const reviewRevertOld = "one\ntwo\nthree\nfour\nfive\n"
const reviewRevertNew = "one\nTWO\nthree\nFOUR\nfive\n"

func newReviewRevertFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := newReviewFixture(t, true)
	f.b.dataDir = t.TempDir()
	f.write("a.go", reviewRevertOld)
	f.git("add", "--", "a.go")
	f.git("commit", "-m", "hunk base")
	f.write("a.go", reviewRevertNew)
	return f
}

func reviewRevertRequest(t *testing.T, f *reviewFixture, scope wire.ReviewScope, turn, path string) wire.ThreadsReviewRevertHunkParams {
	t.Helper()
	r := f.get(scope, turn, path)
	if r.File == nil || r.Unavailable != "" {
		t.Fatalf("审阅快照不可用: %+v", r)
	}
	start, count := 2, 1
	if len(strings.Split(r.File.NewText, "\n")) < 2 {
		start, count = 1, 0
	}
	h, err := git.RevertHunkHash(r.File.NewText, start, count)
	if err != nil {
		t.Fatal(err)
	}
	return wire.ThreadsReviewRevertHunkParams{ThreadID: f.id, Scope: scope, TurnID: turn, Path: path, BaseRevision: r.BaseRevision, HeadRevision: r.HeadRevision, StartLine: start, LineCount: count, HunkHash: h, ExpectedFileHash: reviewHash([]byte(r.File.NewText))}
}

func readReviewRevertFile(t *testing.T, root, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReviewRevertHunkRevertsOnlySelected(t *testing.T) {
	f := newReviewRevertFixture(t)
	f.git("add", "--", "a.go")
	f.write("other.txt", "other edit\n")
	indexBefore := readReviewRevertFile(t, f.root, ".git/index")
	p := reviewRevertRequest(t, f, wire.ReviewScopeUncommitted, "", "a.go")
	result, err := f.b.ReviewRevertHunk(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	data := readReviewRevertFile(t, f.root, "a.go")
	if string(data) != "one\ntwo\nthree\nFOUR\nfive\n" || result.FileHash != reviewHash(data) {
		t.Fatalf("选中块或结果哈希错误: %q %+v", data, result)
	}
	if !reflect.DeepEqual(indexBefore, readReviewRevertFile(t, f.root, ".git/index")) || string(readReviewRevertFile(t, f.root, "other.txt")) != "other edit\n" {
		t.Fatal("真实 index 或其他文件被修改")
	}
}

func TestReviewRevertRejectsBusyStaleAndTraversal(t *testing.T) {
	f := newReviewRevertFixture(t)
	turn := newUUID()
	f.order(turn)
	f.write("a.go", reviewRevertOld)
	f.checkpoint(turn, "start")
	f.write("a.go", reviewRevertNew)
	f.checkpoint(turn, "end")
	p := reviewRevertRequest(t, f, wire.ReviewScopeTurn, turn, "a.go")
	for _, tc := range []struct {
		name string
		edit func(*wire.ThreadsReviewRevertHunkParams)
	}{
		{"expectedFileHash", func(p *wire.ThreadsReviewRevertHunkParams) { p.ExpectedFileHash = "stale" }},
		{"baseRevision", func(p *wire.ThreadsReviewRevertHunkParams) { p.BaseRevision = "stale" }},
		{"headRevision", func(p *wire.ThreadsReviewRevertHunkParams) { p.HeadRevision = "stale" }},
		{"hunkHash", func(p *wire.ThreadsReviewRevertHunkParams) { p.HunkHash = "stale" }},
		{"traversal", func(p *wire.ThreadsReviewRevertHunkParams) { p.Path = "../a.go" }},
		{"absolute", func(p *wire.ThreadsReviewRevertHunkParams) { p.Path = filepath.Join(f.root, "a.go") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := p
			tc.edit(&bad)
			if _, err := f.b.ReviewRevertHunk(f.ctx, bad); err == nil {
				t.Fatal("危险或过期请求未被拒绝")
			}
			if string(readReviewRevertFile(t, f.root, "a.go")) != reviewRevertNew {
				t.Fatal("拒绝请求仍修改了磁盘")
			}
		})
	}
	for _, kind := range []string{"running", "shared", "external"} {
		t.Run(kind, func(t *testing.T) {
			f.b.mu.Lock()
			switch kind {
			case "running":
				f.b.threads[f.id].summary.IsRunning = true
			case "shared":
				other := *f.b.threads[f.id]
				other.summary.IsRunning = true
				f.b.threads["other"] = &other
			case "external":
				f.b.revertBusyCheck = func(string, string) string { return "工作区繁忙" }
			}
			f.b.mu.Unlock()
			_, err := f.b.ReviewRevertHunk(f.ctx, p)
			f.b.mu.Lock()
			f.b.threads[f.id].summary.IsRunning = false
			delete(f.b.threads, "other")
			f.b.revertBusyCheck = nil
			f.b.mu.Unlock()
			if err == nil || string(readReviewRevertFile(t, f.root, "a.go")) != reviewRevertNew {
				t.Fatal("繁忙工作区未被保护")
			}
		})
	}
	// 使用固定 checkpoint，让当前文件变化只触发 expectedFileHash 冲突。
	f.write("a.go", reviewRevertNew+"later\n")
	if _, err := f.b.ReviewRevertHunk(f.ctx, p); err == nil || !strings.Contains(err.Error(), "工作区文件已改变") {
		t.Fatalf("未拒绝磁盘变化: %v", err)
	}
	f.write("a.go", reviewRevertNew)
	for _, kind := range []string{"added", "deleted", "renamed"} {
		t.Run(kind, func(t *testing.T) {
			g := newReviewRevertFixture(t)
			path := "a.go"
			switch kind {
			case "added":
				path = "new.go"
				g.write(path, reviewRevertNew)
				g.git("add", "--", path)
			case "deleted":
				if err := os.Remove(filepath.Join(g.root, path)); err != nil {
					t.Fatal(err)
				}
			case "renamed":
				g.write(path, reviewRevertOld)
				g.git("mv", "--", path, "renamed.go")
				path = "renamed.go"
			}
			request := reviewRevertRequest(t, g, wire.ReviewScopeUncommitted, "", path)
			before := g.git("status", "--porcelain")
			if _, err := g.b.ReviewRevertHunk(g.ctx, request); err == nil {
				t.Fatal("不支持的整文件操作未被拒绝")
			}
			if g.git("status", "--porcelain") != before {
				t.Fatal("拒绝请求改变了文件状态")
			}
		})
	}
}

func TestReviewRevertDoesNotRewriteHistory(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := newReviewRevertFixture(t)
	if err := f.b.AttachStore(f.ctx, st); err != nil {
		t.Fatal(err)
	}
	pid := newUUID()
	f.b.AddProject(wire.ProjectSummary{ID: pid, Name: "history", Path: f.root})
	thread, err := f.b.CreateThread(f.ctx, wire.ThreadCreateParams{ProjectID: pid, ActionID: newUUID()})
	if err != nil {
		t.Fatal(err)
	}
	f.id = thread.ThreadID
	turn := newUUID()
	f.order(turn)
	f.write("a.go", reviewRevertOld)
	f.checkpoint(turn, "start")
	f.write("a.go", reviewRevertNew)
	f.checkpoint(turn, "end")
	p := reviewRevertRequest(t, f, wire.ReviewScopeTurn, turn, "a.go")
	if err := st.CreateTurn(f.ctx, store.Turn{ID: turn, ThreadID: f.id, TurnIndex: 0, Status: "completed", StartedAt: time.Now().UTC(), BaseCheckpoint: p.BaseRevision, FinishCheckpoint: p.HeadRevision, UserPrompt: "保留历史"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(f.ctx, store.Message{ID: newUUID(), ThreadID: f.id, TurnID: turn, Role: "user", Text: "保留此消息", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	turnsBefore, err := st.ListTurns(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	messagesBefore, _, err := st.ListMessages(f.ctx, f.id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	f.b.mu.Lock()
	before, err := json.Marshal(f.b.threads[f.id].messages)
	runTurn := f.b.threads[f.id].runTurnID
	f.b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	refs := f.git("show-ref")
	if _, err := f.b.ReviewRevertHunk(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	f.b.mu.Lock()
	after, err := json.Marshal(f.b.threads[f.id].messages)
	gotTurn := f.b.threads[f.id].runTurnID
	f.b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || runTurn != gotTurn || f.git("show-ref") != refs {
		t.Fatal("撤销块改写了消息、轮次或 checkpoint ref")
	}
	turnsAfter, err := st.ListTurns(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	messagesAfter, _, err := st.ListMessages(f.ctx, f.id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(turnsBefore) != 1 || len(messagesBefore) != 1 || !reflect.DeepEqual(turnsBefore, turnsAfter) || !reflect.DeepEqual(messagesBefore, messagesAfter) {
		t.Fatal("撤销块改写了持久化消息或轮次")
	}
	if r := f.get(wire.ReviewScopeTurn, turn, "a.go"); r.TurnID != turn || r.BaseRevision != p.BaseRevision || r.HeadRevision != p.HeadRevision || r.File.NewText != reviewRevertNew {
		t.Fatal("历史审阅快照被改写")
	}
}

func TestReviewRevertSnapshotSaved(t *testing.T) {
	f := newReviewRevertFixture(t)
	p := reviewRevertRequest(t, f, wire.ReviewScopeUncommitted, "", "a.go")
	if _, err := f.b.ReviewRevertHunk(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.b.dataDir, "review-revert", f.id)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("未保存快照: %v %v", entries, err)
	}
	oldest := entries[0].Name()
	if string(readReviewRevertFile(t, dir, oldest)) != reviewRevertNew {
		t.Fatal("快照不是写入前的原始内容")
	}
	info, err := os.Stat(filepath.Join(dir, oldest))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("快照权限不符: %v %v", info, err)
	}
	for i := range 20 {
		// Windows clock resolution can reuse/non-monotonically order adjacent UnixNano values.
		time.Sleep(time.Millisecond)
		if err := saveReviewRevertSnapshot(f.b, f.id, "a.go", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 20 {
		t.Fatalf("快照数量未受限: %d %v", len(entries), err)
	}
	if _, err := os.Stat(filepath.Join(dir, oldest)); !os.IsNotExist(err) {
		t.Fatalf("最旧快照仍存在: %v", err)
	}
	if string(readReviewRevertFile(t, dir, entries[len(entries)-1].Name())) != string([]byte{19}) {
		t.Fatal("最新快照被删除")
	}
}

func TestReviewRevertCrossThreadIsolation(t *testing.T) {
	f := newReviewRevertFixture(t)
	otherRoot := filepath.Join(t.TempDir(), "worktree")
	f.git("worktree", "add", "--detach", otherRoot)
	pid := newUUID()
	f.b.AddProject(wire.ProjectSummary{ID: pid, Name: "other", Path: otherRoot})
	other, err := f.b.CreateThread(f.ctx, wire.ThreadCreateParams{ProjectID: pid, ActionID: newUUID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherRoot, "a.go"), []byte(reviewRevertNew), 0600); err != nil {
		t.Fatal(err)
	}
	p := reviewRevertRequest(t, f, wire.ReviewScopeUncommitted, "", "a.go")
	if _, err := f.b.ReviewRevertHunk(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	if string(readReviewRevertFile(t, otherRoot, "a.go")) != reviewRevertNew {
		t.Fatal("A 的请求修改了 B 的工作区")
	}
	if other.ThreadID == f.id {
		t.Fatal("测试会话未隔离")
	}
}

func TestReviewRevertConcurrentRequests(t *testing.T) {
	f := newReviewRevertFixture(t)
	p := reviewRevertRequest(t, f, wire.ReviewScopeUncommitted, "", "a.go")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.b.ReviewRevertHunk(f.ctx, p); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || string(readReviewRevertFile(t, f.root, "a.go")) != "one\ntwo\nthree\nFOUR\nfive\n" {
		t.Fatal("并发请求未串行校验同一工作区")
	}
}
