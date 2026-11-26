package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func countLooseObjects(t *testing.T, runner *Runner, dir string) int {
	t.Helper()
	out, err := runner.run(context.Background(), dir, nil, "count-objects", "-v")
	if err != nil {
		t.Fatalf("count-objects: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "count:") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "count:")))
			if err != nil {
				t.Fatalf("parse count-objects %q: %v", line, err)
			}
			return n
		}
	}
	t.Fatalf("missing count line in count-objects output: %q", string(out))
	return 0
}

// TestCheckpointStore_IsolatesRefsAndObjects 验证场景 (a)：
// 写入 refs/mcode/checkpoints/t/x/start 后，用户仓库中 refs/mcode 为空、松散对象数不增加，store 中可 RevParse。
func TestCheckpointStore_IsolatesRefsAndObjects(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "work.txt", "initial content\n")
	looseBefore := countLooseObjects(t, runner, dir)

	ref := "refs/mcode/checkpoints/t/x/start"
	commit, err := runner.Checkpoint(ctx, dir, ref, "start")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if commit == "" {
		t.Fatal("expected non-empty checkpoint commit")
	}

	userRefs := runTestGit(t, dir, "for-each-ref", "refs/mcode")
	if userRefs != "" {
		t.Fatalf("expected user repo refs/mcode to be empty, got %q", userRefs)
	}

	looseAfter := countLooseObjects(t, runner, dir)
	if looseAfter > looseBefore {
		t.Fatalf("expected user repo loose object count not to increase: before=%d after=%d", looseBefore, looseAfter)
	}

	storeDir, ok := runner.existingCheckpointStore(ctx, dir)
	if !ok {
		t.Fatal("expected checkpoint store to exist")
	}
	altBytes, err := os.ReadFile(filepath.Join(storeDir, "objects", "info", "alternates"))
	if err != nil {
		t.Fatalf("read alternates: %v", err)
	}
	if strings.TrimSpace(string(altBytes)) != "../../../objects" {
		t.Fatalf("unexpected alternates content: %q", string(altBytes))
	}

	got, err := runner.RevParse(ctx, dir, ref)
	if err != nil {
		t.Fatalf("RevParse in store: %v", err)
	}
	if got != commit {
		t.Fatalf("RevParse = %s, want %s", got, commit)
	}
}

// TestCheckpointStore_DiffAndRevertWorkAcrossStore 验证场景 (b)：
// 修改文件后拍 end 快照，Diff / DiffText / NumStat 和 ExecuteRevert 使用 start/end SHA 均可正常工作。
func TestCheckpointStore_DiffAndRevertWorkAcrossStore(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "app.txt", "v1\n")
	startSHA, err := runner.Checkpoint(ctx, dir, CheckpointRef("th-b", "turn1")+"/start", "start")
	if err != nil {
		t.Fatalf("Checkpoint start: %v", err)
	}

	writeTestFile(t, dir, "app.txt", "v2\n")
	writeTestFile(t, dir, "extra.txt", "extra\n")
	endSHA, err := runner.Checkpoint(ctx, dir, CheckpointRef("th-b", "turn1")+"/end", "end")
	if err != nil {
		t.Fatalf("Checkpoint end: %v", err)
	}

	diffs, err := runner.Diff(ctx, dir, startSHA, endSHA, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := strings.Join(TouchedPaths(diffs), ","); got != "app.txt,extra.txt" {
		t.Fatalf("Diff TouchedPaths = %q, want app.txt,extra.txt", got)
	}

	text, _, err := runner.DiffText(ctx, dir, startSHA, endSHA, nil, 0)
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	if !strings.Contains(text, "+v2") || !strings.Contains(text, "+extra") {
		t.Fatalf("DiffText missing expected lines:\n%s", text)
	}

	stats, err := runner.NumStat(ctx, dir, startSHA, endSHA, nil)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("NumStat count = %d, want 2: %+v", len(stats), stats)
	}

	if _, err := runner.ExecuteRevert(ctx, dir, startSHA, endSHA); err != nil {
		t.Fatalf("ExecuteRevert: %v", err)
	}
	if got := readTestFile(t, dir, "app.txt"); got != "v1\n" {
		t.Fatalf("app.txt after revert = %q, want v1\\n", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt should be removed after revert, stat err=%v", err)
	}
}

// TestCheckpointStore_MigratesLegacyRefsPreservesSwarm 验证场景 (c)：
// 用户仓库预先存在旧 checkpoints ref 和 swarm-base ref 时，首次建 store 会把旧 checkpoints 迁移至新 store、从用户仓库删除并保留 swarm-base。
func TestCheckpointStore_MigratesLegacyRefsPreservesSwarm(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	head := runTestGit(t, dir, "rev-parse", "HEAD")
	oldStart := "refs/mcode/checkpoints/old-thread/t1/start"
	oldEnd := "refs/mcode/checkpoints/old-thread/t1/end"
	runTestGit(t, dir, "update-ref", oldStart, head)
	runTestGit(t, dir, "update-ref", oldEnd, head)
	runTestGit(t, dir, "update-ref", "refs/mcode/swarm-base/x", head)

	if _, err := runner.Checkpoint(ctx, dir, CheckpointRef("new-thread", "t1")+"/start", "new"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if oldRefs := runTestGit(t, dir, "for-each-ref", "refs/mcode/checkpoints/"); oldRefs != "" {
		t.Fatalf("expected legacy checkpoint refs to be deleted from user repo, got %q", oldRefs)
	}
	if swarmRefs := runTestGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/mcode/swarm-base/"); swarmRefs != "refs/mcode/swarm-base/x" {
		t.Fatalf("expected swarm-base ref to be preserved, got %q", swarmRefs)
	}
	for _, ref := range []string{oldStart, oldEnd} {
		got, err := runner.RevParse(ctx, dir, ref)
		if err != nil {
			t.Fatalf("RevParse(%s) in store: %v", ref, err)
		}
		if got != head {
			t.Fatalf("RevParse(%s) = %s, want %s", ref, got, head)
		}
	}
}

// TestCheckpointStore_MigratesLegacyRefsKeepsObjects 验证旧检查点引用迁移后对象已独立打包进 store，
// 即使在用户仓库执行 reflog expire 与 gc --prune=now 清理不可达对象，store 中仍可解析且对象可读。
func TestCheckpointStore_MigratesLegacyRefsKeepsObjects(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "legacy-only.txt", "legacy orphan content\n")
	blobSHA := runTestGit(t, dir, "hash-object", "-w", "legacy-only.txt")
	if err := os.Remove(filepath.Join(dir, "legacy-only.txt")); err != nil {
		t.Fatalf("remove legacy-only.txt: %v", err)
	}
	treeOut, err := runner.runWithStdin(ctx, dir, nil, []byte(fmt.Sprintf("100644 blob %s\tlegacy-only.txt\n", blobSHA)), "mktree")
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	treeSHA := strings.TrimSpace(string(treeOut))
	legacyCommit := runTestGit(t, dir, "commit-tree", treeSHA, "-m", "legacy checkpoint")
	legacyRef := "refs/mcode/checkpoints/legacy-thread/turn-1/start"
	runTestGit(t, dir, "update-ref", legacyRef, legacyCommit)

	storeDir, err := runner.checkpointStore(ctx, dir)
	if err != nil {
		t.Fatalf("checkpointStore: %v", err)
	}
	if userRefs := runTestGit(t, dir, "for-each-ref", "refs/mcode/checkpoints/"); userRefs != "" {
		t.Fatalf("expected legacy checkpoint refs to be deleted from user repo, got %q", userRefs)
	}

	runTestGit(t, dir, "reflog", "expire", "--expire=now", "--all")
	runTestGit(t, dir, "gc", "--prune=now")

	got, err := runner.RevParse(ctx, dir, legacyRef)
	if err != nil {
		t.Fatalf("RevParse(%s) after user gc: %v", legacyRef, err)
	}
	if got != legacyCommit {
		t.Fatalf("RevParse(%s) = %s, want %s", legacyRef, got, legacyCommit)
	}
	storeEnv := checkpointEnv(storeDir, dir)
	if _, err := runner.run(ctx, dir, storeEnv, "cat-file", "-e", legacyCommit); err != nil {
		t.Fatalf("cat-file -e %s in store after user gc: %v", legacyCommit, err)
	}
	if _, err := runner.run(ctx, dir, storeEnv, "cat-file", "-e", blobSHA); err != nil {
		t.Fatalf("cat-file -e %s in store after user gc: %v", blobSHA, err)
	}
}

// TestCheckpointStore_SharedAcrossWorktrees 验证场景 (d)：
// linked worktree 与主工作区共用同一个 store。
func TestCheckpointStore_SharedAcrossWorktrees(t *testing.T) {
	t.Parallel()
	runner, mainDir := newRevertTestRepo(t)
	ctx := context.Background()

	wtDir := filepath.Join(t.TempDir(), "linked-wt")
	runTestGit(t, mainDir, "worktree", "add", "-b", "wt-branch", wtDir)

	writeTestFile(t, mainDir, "main.txt", "main\n")
	mainRef := CheckpointRef("th-wt", "main1") + "/start"
	mainCommit, err := runner.Checkpoint(ctx, mainDir, mainRef, "main")
	if err != nil {
		t.Fatalf("Checkpoint mainDir: %v", err)
	}

	writeTestFile(t, wtDir, "wt.txt", "wt\n")
	wtRef := CheckpointRef("th-wt", "wt1") + "/start"
	wtCommit, err := runner.Checkpoint(ctx, wtDir, wtRef, "wt")
	if err != nil {
		t.Fatalf("Checkpoint wtDir: %v", err)
	}

	mainStore, ok := runner.existingCheckpointStore(ctx, mainDir)
	if !ok {
		t.Fatal("expected main store to exist")
	}
	wtStore, ok := runner.existingCheckpointStore(ctx, wtDir)
	if !ok {
		t.Fatal("expected worktree store to exist")
	}
	if mainStore != wtStore {
		t.Fatalf("expected shared store, got main=%q wt=%q", mainStore, wtStore)
	}

	refs, err := runner.ListRefs(ctx, mainDir, "refs/mcode/checkpoints/th-wt/")
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 shared refs, got %d: %+v", len(refs), refs)
	}
	if got, err := runner.RevParse(ctx, mainDir, wtRef); err != nil || got != wtCommit {
		t.Fatalf("mainDir RevParse(wtRef) = %q, %v; want %s", got, err, wtCommit)
	}
	if got, err := runner.RevParse(ctx, wtDir, mainRef); err != nil || got != mainCommit {
		t.Fatalf("wtDir RevParse(mainRef) = %q, %v; want %s", got, err, mainCommit)
	}
}

// TestCheckpointStore_SyncsInfoExclude 验证场景 (e)：
// .git/info/exclude 中忽略的文件不会进入快照，且 info/exclude 更新后能同步到 store。
func TestCheckpointStore_SyncsInfoExclude(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	commonDir, err := runner.GitCommonDir(ctx, dir)
	if err != nil {
		t.Fatalf("GitCommonDir: %v", err)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		t.Fatalf("mkdir info: %v", err)
	}
	if err := os.WriteFile(excludePath, []byte("secret.local\n"), 0o644); err != nil {
		t.Fatalf("write exclude: %v", err)
	}

	writeTestFile(t, dir, "secret.local", "do not snapshot\n")
	writeTestFile(t, dir, "keep.txt", "keep\n")

	c1, err := runner.Checkpoint(ctx, dir, CheckpointRef("th-ex", "1")+"/start", "c1")
	if err != nil {
		t.Fatalf("Checkpoint c1: %v", err)
	}
	paths1 := treePaths(t, runner, dir, c1)
	if strings.Contains(strings.Join(paths1, ","), "secret.local") {
		t.Fatalf("excluded file secret.local should not be in snapshot: %v", paths1)
	}
	if !strings.Contains(strings.Join(paths1, ","), "keep.txt") {
		t.Fatalf("keep.txt missing from snapshot: %v", paths1)
	}

	// store 创建后追加新的 exclude 规则，下一次快照也应生效
	if err := os.WriteFile(excludePath, []byte("secret.local\n*.tmp\n"), 0o644); err != nil {
		t.Fatalf("update exclude: %v", err)
	}
	writeTestFile(t, dir, "scratch.tmp", "tmp\n")
	c2, err := runner.Checkpoint(ctx, dir, CheckpointRef("th-ex", "2")+"/start", "c2")
	if err != nil {
		t.Fatalf("Checkpoint c2: %v", err)
	}
	paths2 := treePaths(t, runner, dir, c2)
	if strings.Contains(strings.Join(paths2, ","), "scratch.tmp") {
		t.Fatalf("updated exclude *.tmp should not be in snapshot: %v", paths2)
	}
}

// TestCheckpointStore_SwarmNormalizedStaysInUserRepo 验证场景 (f)：
// CheckpointNormalized 写 refs/mcode/swarm-land/a/1 时，ref 与对象仍保存在用户仓库。
func TestCheckpointStore_SwarmNormalizedStaysInUserRepo(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "swarm.txt", "swarm work\n")
	ref := "refs/mcode/swarm-land/a/1"
	commit, err := runner.CheckpointNormalized(ctx, dir, ref, "swarm land")
	if err != nil {
		t.Fatalf("CheckpointNormalized: %v", err)
	}

	gotRef := runTestGit(t, dir, "rev-parse", ref)
	if gotRef != commit {
		t.Fatalf("user repo rev-parse %s = %s, want %s", ref, gotRef, commit)
	}
	runTestGit(t, dir, "cat-file", "-e", commit)
	if _, ok := runner.existingCheckpointStore(ctx, dir); ok {
		t.Fatal("swarm ref write should not create checkpoint store")
	}
}

// TestCheckpointStore_ConcurrentInitAndReadOnlyNoCreate 验证并发初始化幂等且只读路径不会创建 store。
func TestCheckpointStore_ConcurrentInitAndReadOnlyNoCreate(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	head := runTestGit(t, dir, "rev-parse", "HEAD")

	// 只读路径不创建 store
	if _, err := runner.ListRefs(ctx, dir, "refs/mcode/checkpoints/none/"); err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if _, err := runner.RevParse(ctx, dir, "HEAD"); err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	if _, err := runner.Diff(ctx, dir, head, "", nil); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if _, _, err := runner.DiffText(ctx, dir, head, "", nil, 0); err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	if _, err := runner.NumStat(ctx, dir, head, "", nil); err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if _, err := runner.ReviewRevision(ctx, dir, "HEAD"); err != nil {
		t.Fatalf("ReviewRevision: %v", err)
	}
	if _, _, err := runner.ReviewWorkingTree(ctx, dir); err != nil {
		t.Fatalf("ReviewWorkingTree: %v", err)
	}
	if err := runner.DeleteCheckpoints(ctx, dir, "none"); err != nil {
		t.Fatalf("DeleteCheckpoints: %v", err)
	}
	if _, ok := runner.existingCheckpointStore(ctx, dir); ok {
		t.Fatal("read-only operations must not create checkpoint store")
	}

	// 并发首次创建 store
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			ref := CheckpointRef("concurrent", fmt.Sprintf("t-%d", idx)) + "/start"
			if _, err := runner.Checkpoint(ctx, dir, ref, "concurrent"); err != nil {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Checkpoint failed: %v", err)
	}

	refs, err := runner.ListRefs(ctx, dir, "refs/mcode/checkpoints/concurrent/")
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	if len(refs) != workers {
		t.Fatalf("expected %d refs, got %d", workers, len(refs))
	}
}

// TestCheckpointStore_MissingObjectErrors 验证对象缺失（已被 GC）时 diff、撤销和审阅均返回明确错误。
func TestCheckpointStore_MissingObjectErrors(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "a.txt", "v1\n")
	if _, err := runner.Checkpoint(ctx, dir, CheckpointRef("th-gc", "1")+"/start", "start"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	head := runTestGit(t, dir, "rev-parse", "HEAD")
	missingSHA := strings.Repeat("d", 40)

	if _, err := runner.Diff(ctx, dir, missingSHA, head, nil); err == nil {
		t.Fatal("Diff expected error for missing object")
	}
	if _, _, err := runner.DiffText(ctx, dir, missingSHA, head, nil, 0); err == nil {
		t.Fatal("DiffText expected error for missing object")
	}
	if _, err := runner.NumStat(ctx, dir, missingSHA, head, nil); err == nil {
		t.Fatal("NumStat expected error for missing object")
	}
	if _, err := runner.PreviewRestore(ctx, dir, missingSHA, head); err == nil {
		t.Fatal("PreviewRestore expected error for missing object")
	}
	if _, err := runner.ExecuteRevert(ctx, dir, missingSHA, head); err == nil {
		t.Fatal("ExecuteRevert expected error for missing object")
	}
	if err := runner.RestoreFiles(ctx, dir, missingSHA, []string{"a.txt"}); err == nil {
		t.Fatal("RestoreFiles expected error for missing object")
	}
	if _, err := runner.ReviewRevision(ctx, dir, missingSHA); err == nil {
		t.Fatal("ReviewRevision expected error for missing object")
	}
	if _, err := runner.ReviewFiles(ctx, dir, missingSHA, head, nil); err == nil {
		t.Fatal("ReviewFiles expected error for missing object")
	}
}

// TestCheckpointStore_WatchedCacheIsolatedFromUserRepo 验证 watch.go 缓存不跨 store 与用户仓库混用。
func TestCheckpointStore_WatchedCacheIsolatedFromUserRepo(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "w.txt", "watched\n")
	w := newPollWatcher(dir)
	defer func() { _ = w.Close() }()

	cpRef := CheckpointRef("th-watch-iso", "1") + "/start"
	cpCommit, err := runner.CheckpointWatched(ctx, w, dir, cpRef, "cp")
	if err != nil {
		t.Fatalf("CheckpointWatched cp: %v", err)
	}

	swarmRef := "refs/mcode/swarm-base/iso"
	swarmCommit, err := runner.CheckpointWatched(ctx, w, dir, swarmRef, "swarm")
	if err != nil {
		t.Fatalf("CheckpointWatched swarm: %v", err)
	}

	// 用户仓库中必须存在 swarmCommit 及其 tree（不能因复用 store 中的 tree 而丢失对象）
	runTestGit(t, dir, "cat-file", "-e", swarmCommit+"^{tree}")
	if got := runTestGit(t, dir, "rev-parse", swarmRef); got != swarmCommit {
		t.Fatalf("swarm ref = %s, want %s", got, swarmCommit)
	}

	// store 中必须存在 cpCommit，用户仓库中不应存在 cpRef
	if got, err := runner.RevParse(ctx, dir, cpRef); err != nil || got != cpCommit {
		t.Fatalf("RevParse(cpRef) = %s, %v; want %s", got, err, cpCommit)
	}
	if userRefs := runTestGit(t, dir, "for-each-ref", "refs/mcode/checkpoints/"); userRefs != "" {
		t.Fatalf("expected user repo checkpoints empty, got %q", userRefs)
	}
}
