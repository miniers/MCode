package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func initGitRepo(t *testing.T) (*Runner, string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	dir := t.TempDir()
	runner := NewRunner(gitBin, 4)
	ctx := context.Background()

	// 初始化 git 仓库
	_, err = runner.run(ctx, dir, nil, "init")
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// 统一配置测试仓库的隔离用户信息，避免依赖全局 git config
	testEnv := []string{
		"GIT_AUTHOR_NAME=test-user",
		"GIT_AUTHOR_EMAIL=test@localhost",
		"GIT_COMMITTER_NAME=test-user",
		"GIT_COMMITTER_EMAIL=test@localhost",
	}

	// 防止初始主分支由于旧版本 git 默认分支名不一致问题
	_, _ = runner.run(ctx, dir, testEnv, "config", "user.name", "test-user")
	_, _ = runner.run(ctx, dir, testEnv, "config", "user.email", "test@localhost")
	if _, err := runner.run(ctx, dir, nil, "config", "core.autocrlf", "false"); err != nil {
		t.Fatalf("git config core.autocrlf: %v", err)
	}

	return runner, dir
}

func TestCheckpointNormalizedLineEndings(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()
	runTestGit(t, dir, "config", "core.autocrlf", "true")
	writeTestFile(t, dir, "lines.txt", "first\nsecond\n")
	runTestGit(t, dir, "add", "lines.txt")
	runTestGit(t, dir, "commit", "-m", "initial")
	if err := os.Remove(filepath.Join(dir, "lines.txt")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "checkout", "--", "lines.txt")
	if readTestFile(t, dir, "lines.txt") != "first\r\nsecond\r\n" {
		t.Skip("git 检出未生成 CRLF")
	}

	normalized, err := runner.CheckpointNormalized(ctx, dir, "refs/mcode/test/normalized", "normalized")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runner.Checkpoint(ctx, dir, "refs/mcode/test/raw", "raw")
	if err != nil {
		t.Fatal(err)
	}
	headTree := runTestGit(t, dir, "rev-parse", "HEAD^{tree}")
	if tree := runTestGit(t, dir, "rev-parse", normalized+"^{tree}"); tree != headTree {
		t.Fatalf("规范化快照树 %s 不等于 HEAD 树 %s", tree, headTree)
	}
	if tree := runTestGit(t, dir, "rev-parse", raw+"^{tree}"); tree == headTree {
		t.Fatalf("原样快照树不应等于 HEAD 树 %s", headTree)
	}
}

func TestCheckpointAndDiffWorkflow(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	// 1. 空仓库场景测试 Checkpoint
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Demo Repo\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	cp0Ref := CheckpointRef("thread-1", "0")
	cp0Commit, err := runner.Checkpoint(ctx, dir, cp0Ref, "initial empty repo checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint on empty repo failed: %v", err)
	}
	if cp0Commit == "" {
		t.Fatalf("expected non-empty commit hash for cp0")
	}

	// 此时用户真正的 HEAD 应该仍然未提交
	if runner.hasCommits(ctx, dir) {
		t.Fatalf("user repository HEAD should still be unborn")
	}

	// 用户执行首次正常 commit
	if _, err := runner.run(ctx, dir, nil, "add", "README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "init commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	headBeforeBytes, err := runner.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	headBefore := strings.TrimSpace(string(headBeforeBytes))

	// 2. 在已有提交的仓库中模拟用户的暂存与未暂存变更
	stagedFile := filepath.Join(dir, "staged.txt")
	if err := os.WriteFile(stagedFile, []byte("staged line 1\n"), 0644); err != nil {
		t.Fatalf("write staged.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "staged.txt"); err != nil {
		t.Fatalf("git add staged.txt: %v", err)
	}

	untrackedFile := filepath.Join(dir, "untracked.txt")
	if err := os.WriteFile(untrackedFile, []byte("untracked content\n"), 0644); err != nil {
		t.Fatalf("write untracked.txt: %v", err)
	}

	if err := os.WriteFile(readmePath, []byte("# Demo Repo Updated\n"), 0644); err != nil {
		t.Fatalf("update README.md: %v", err)
	}

	// 记录用户当前状态
	cachedBefore, err := runner.run(ctx, dir, nil, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("diff --cached: %v", err)
	}
	statusBefore, err := runner.Status(ctx, dir)
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}

	// 执行第一个正常轮次检查点
	cp1Ref := CheckpointRef("thread-1", "1")
	cp1Commit, err := runner.Checkpoint(ctx, dir, cp1Ref, "checkpoint turn 1")
	if err != nil {
		t.Fatalf("Checkpoint turn 1 failed: %v", err)
	}

	// 3. 验证对用户 HEAD、暂存区 index 和状态零副作用
	headAfterBytes, err := runner.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD after checkpoint: %v", err)
	}
	headAfter := strings.TrimSpace(string(headAfterBytes))
	if headBefore != headAfter {
		t.Errorf("HEAD changed: before=%s, after=%s", headBefore, headAfter)
	}

	cachedAfter, err := runner.run(ctx, dir, nil, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("diff --cached after checkpoint: %v", err)
	}
	if string(cachedBefore) != string(cachedAfter) {
		t.Errorf("cached index changed: before=%s, after=%s", string(cachedBefore), string(cachedAfter))
	}

	statusAfter, err := runner.Status(ctx, dir)
	if err != nil {
		t.Fatalf("Status after checkpoint: %v", err)
	}
	if len(statusBefore) != len(statusAfter) {
		t.Errorf("status entries count mismatch: before=%d, after=%d", len(statusBefore), len(statusAfter))
	}

	// 4. 修改、新增、删除文件后建立第 2 个检查点，并比对两次检查点 Diff
	// 修改 staged.txt
	if err := os.WriteFile(stagedFile, []byte("staged line 1\nstaged line 2 modified\n"), 0644); err != nil {
		t.Fatalf("modify staged.txt: %v", err)
	}
	// 删除 untracked.txt
	if err := os.Remove(untrackedFile); err != nil {
		t.Fatalf("remove untracked.txt: %v", err)
	}
	// 新增 new_created.txt
	newCreatedFile := filepath.Join(dir, "new_created.txt")
	if err := os.WriteFile(newCreatedFile, []byte("brand new file\n"), 0644); err != nil {
		t.Fatalf("create new_created.txt: %v", err)
	}

	cp2Ref := CheckpointRef("thread-1", "2")
	cp2Commit, err := runner.Checkpoint(ctx, dir, cp2Ref, "checkpoint turn 2")
	if err != nil {
		t.Fatalf("Checkpoint turn 2 failed: %v", err)
	}

	diffs, err := runner.Diff(ctx, dir, cp1Commit, cp2Commit, nil)
	if err != nil {
		t.Fatalf("Diff(cp1, cp2) failed: %v", err)
	}

	touched := TouchedPaths(diffs)
	expectedTouched := []string{"new_created.txt", "staged.txt", "untracked.txt"}
	if strings.Join(touched, ",") != strings.Join(expectedTouched, ",") {
		t.Errorf("TouchedPaths mismatch: got %v, want %v", touched, expectedTouched)
	}

	// 5. 测试 Diff 到当前工作区（to 为空）包含未跟踪新文件
	freshUntracked := filepath.Join(dir, "fresh_untracked.txt")
	if err := os.WriteFile(freshUntracked, []byte("freshly created untracked\n"), 0644); err != nil {
		t.Fatalf("write fresh_untracked: %v", err)
	}

	diffsToWorktree, err := runner.Diff(ctx, dir, cp2Commit, "", nil)
	if err != nil {
		t.Fatalf("Diff to working tree failed: %v", err)
	}

	foundFresh := false
	for _, d := range diffsToWorktree {
		if d.NewPath == "fresh_untracked.txt" && d.Status == "added" {
			foundFresh = true
			break
		}
	}
	if !foundFresh {
		t.Errorf("expected fresh_untracked.txt to be present in diff to worktree")
	}

	// 6. 测试 RestoreFiles：还原被修改的文件，并删除检查点后新增的文件
	// 将 staged.txt 恶意修改，并让 fresh_untracked.txt 留在工作区
	if err := os.WriteFile(stagedFile, []byte("broken content\n"), 0644); err != nil {
		t.Fatalf("corrupt staged.txt: %v", err)
	}

	err = runner.RestoreFiles(ctx, dir, cp2Commit, []string{"staged.txt", "fresh_untracked.txt"})
	if err != nil {
		t.Fatalf("RestoreFiles failed: %v", err)
	}

	// 验证 fresh_untracked.txt 已经被删除（因为在 cp2 中不存在）
	if _, err := os.Stat(freshUntracked); !os.IsNotExist(err) {
		t.Errorf("fresh_untracked.txt should have been deleted by RestoreFiles")
	}

	// 验证 staged.txt 已恢复为 cp2 时的内容
	stagedData, err := os.ReadFile(stagedFile)
	if err != nil {
		t.Fatalf("read restored staged.txt: %v", err)
	}
	if string(stagedData) != "staged line 1\nstaged line 2 modified\n" {
		t.Errorf("staged.txt content mismatch: got %q", string(stagedData))
	}

	// 7. 测试 RestoreFiles 路径越界校验
	escapePaths := []string{
		"../outside.txt",
		"../../etc/passwd",
		filepath.Join(t.TempDir(), "outside_repo.txt"),
	}
	for _, ep := range escapePaths {
		if err := runner.RestoreFiles(ctx, dir, cp2Commit, []string{ep}); err == nil {
			t.Errorf("expected error for escaping path %q, got nil", ep)
		}
	}
}

func TestPruneCheckpoints(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	threadID := "thread-prune-test"

	// 依次创建 3 个检查点，通过间隔让 creatordate 严格递增
	cp1Ref := CheckpointRef(threadID, "turn-1")
	f1 := filepath.Join(dir, "f1.txt")
	if err := os.WriteFile(f1, []byte("content 1\n"), 0644); err != nil {
		t.Fatalf("write f1: %v", err)
	}
	if _, err := runner.Checkpoint(ctx, dir, cp1Ref, "checkpoint 1"); err != nil {
		t.Fatalf("Checkpoint 1 failed: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	cp2Ref := CheckpointRef(threadID, "turn-2")
	f2 := filepath.Join(dir, "f2.txt")
	if err := os.WriteFile(f2, []byte("content 2\n"), 0644); err != nil {
		t.Fatalf("write f2: %v", err)
	}
	if _, err := runner.Checkpoint(ctx, dir, cp2Ref, "checkpoint 2"); err != nil {
		t.Fatalf("Checkpoint 2 failed: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	cp3Ref := CheckpointRef(threadID, "turn-3")
	f3 := filepath.Join(dir, "f3.txt")
	if err := os.WriteFile(f3, []byte("content 3\n"), 0644); err != nil {
		t.Fatalf("write f3: %v", err)
	}
	if _, err := runner.Checkpoint(ctx, dir, cp3Ref, "checkpoint 3"); err != nil {
		t.Fatalf("Checkpoint 3 failed: %v", err)
	}

	// 此时存在 3 个检查点，调用 PruneCheckpoints 保留最新的 2 个
	if err := runner.PruneCheckpoints(ctx, dir, threadID, 2); err != nil {
		t.Fatalf("PruneCheckpoints failed: %v", err)
	}

	prefix := checkpointsRefPrefix(threadID)
	out, err := runner.run(ctx, dir, nil, "for-each-ref", "--sort=-creatordate", "--format=%(refname)", prefix)
	if err != nil {
		t.Fatalf("for-each-ref failed: %v", err)
	}

	var remaining []string
	for _, ref := range strings.Split(string(out), "\n") {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			remaining = append(remaining, ref)
		}
	}

	// 断言只剩最新的 2 个检查点（cp3 和 cp2）
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining checkpoints, got %d: %v", len(remaining), remaining)
	}
	if remaining[0] != cp3Ref {
		t.Errorf("expected newest checkpoint %q, got %q", cp3Ref, remaining[0])
	}
	if remaining[1] != cp2Ref {
		t.Errorf("expected second newest checkpoint %q, got %q", cp2Ref, remaining[1])
	}

	// 最早的 cp1Ref 应已被清理
	if _, err := runner.run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", cp1Ref); err == nil {
		t.Errorf("expected cp1Ref %q to be pruned", cp1Ref)
	}
}

// treePaths 返回某个提交（检查点 commit）里全部文件路径，按字典序排列。
func treePaths(t *testing.T, runner *Runner, dir, commit string) []string {
	t.Helper()
	out, err := runner.run(context.Background(), dir, nil, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		t.Fatalf("ls-tree %s: %v", commit, err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths
}

// 仓库开启了 core.fsmonitor / core.splitIndex / core.untrackedCache 时，
// 临时 index 仍应写出内容正确的检查点，且不影响用户索引。
func TestCheckpointWithFsmonitorAndSplitIndex(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write readme.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested\n"), 0644); err != nil {
		t.Fatalf("write sub/nested.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "-A"); err != nil {
		t.Fatalf("git add -A: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "base commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// 打开三个会让临时 index 出问题的用户配置：fsmonitor hook 不存在、
	// 启用 split index、启用 untracked cache。
	configs := [][2]string{
		{"core.fsmonitor", filepath.Join(dir, ".git", "missing-fsmonitor-hook")},
		{"core.splitIndex", "true"},
		{"core.untrackedCache", "true"},
	}
	for _, kv := range configs {
		if _, err := runner.run(ctx, dir, nil, "config", kv[0], kv[1]); err != nil {
			t.Fatalf("git config %s: %v", kv[0], err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested changed\n"), 0644); err != nil {
		t.Fatalf("modify sub/nested.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "added.txt"), []byte("added\n"), 0644); err != nil {
		t.Fatalf("write added.txt: %v", err)
	}

	commit, err := runner.Checkpoint(ctx, dir, CheckpointRef("thread-cfg", "1"), "checkpoint with user configs")
	if err != nil {
		t.Fatalf("Checkpoint failed with fsmonitor/splitIndex enabled: %v", err)
	}

	want := []string{"added.txt", "readme.txt", "sub/nested.txt"}
	if got := treePaths(t, runner, dir, commit); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("checkpoint tree mismatch: got %v, want %v", got, want)
	}

	// 检查点内容应是工作区最新状态（而不是 HEAD 里的旧内容）
	content, err := runner.run(ctx, dir, nil, "show", commit+":sub/nested.txt")
	if err != nil {
		t.Fatalf("show checkpoint sub/nested.txt: %v", err)
	}
	if !strings.Contains(string(content), "nested changed") {
		t.Errorf("检查点未记录工作区修改: %q", string(content))
	}

	// 用户 HEAD 不应因为检查点而移动（fsmonitor hook 缺失时部分命令会往 stderr 输出fatal，
	// 但 rev-parse 不刷新索引，不受影响）
	head, err := runner.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if strings.TrimSpace(string(head)) == "" {
		t.Error("HEAD 不应为空")
	}
}

// 空仓库（HEAD 未出生）的首次检查点应能写出包含子目录文件的树，且不给提交加父提交。
func TestCheckpointEmptyRepoSnapshot(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}

	commit, err := runner.Checkpoint(ctx, dir, CheckpointRef("thread-empty", "0"), "")
	if err != nil {
		t.Fatalf("Checkpoint on empty repo failed: %v", err)
	}
	if commit == "" {
		t.Fatal("expected non-empty checkpoint commit")
	}
	if runner.hasCommits(ctx, dir) {
		t.Error("用户仓库 HEAD 不应因为检查点而出生")
	}

	want := []string{"a.txt", "sub/b.txt"}
	if got := treePaths(t, runner, dir, commit); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("empty repo checkpoint tree mismatch: got %v, want %v", got, want)
	}

	parents, err := runner.run(ctx, dir, nil, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		t.Fatalf("rev-list %s: %v", commit, err)
	}
	if fields := strings.Fields(string(parents)); len(fields) != 1 {
		t.Errorf("空仓库检查点不应有父提交，got %v", fields)
	}
}

// 同一仓库并发执行 add 时，写锁应把它们串行化，不出现 index.lock 冲突。
func TestConcurrentAddSerializedByRepoLock(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "base.txt"); err != nil {
		t.Fatalf("git add base.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "base"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	const workers = 20
	names := make([]string, 0, workers)
	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("concurrent-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		names = append(names, name)
	}

	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			if _, err := runner.run(ctx, dir, nil, "add", path); err != nil {
				errs <- err
			}
		}(name)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("并发 add 失败: %v", err)
	}

	out, err := runner.run(ctx, dir, nil, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("diff --cached: %v", err)
	}
	staged := strings.Fields(string(out))
	if len(staged) != workers {
		t.Errorf("暂存文件数量不符: got %d %v, want %d", len(staged), staged, workers)
	}
}

// 稀疏检出仓库里，模式外的已跟踪文件不能被写成假删除。
func TestCheckpointWithSparseCheckoutKeepsOutsideFiles(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "f1.txt"), []byte("f1\n"), 0644); err != nil {
		t.Fatalf("write a/f1.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "f2.txt"), []byte("f2\n"), 0644); err != nil {
		t.Fatalf("write b/f2.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "-A"); err != nil {
		t.Fatalf("git add -A: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "base"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// 只检出 a 目录，b 目录从工作区消失
	if _, err := runner.run(ctx, dir, nil, "sparse-checkout", "set", "a"); err != nil {
		t.Skipf("git sparse-checkout 不可用，跳过: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a", "f1.txt"), []byte("f1 changed\n"), 0644); err != nil {
		t.Fatalf("modify a/f1.txt: %v", err)
	}

	commit, err := runner.Checkpoint(ctx, dir, CheckpointRef("thread-sparse", "1"), "sparse checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint on sparse repo failed: %v", err)
	}

	got := treePaths(t, runner, dir, commit)
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "a/f1.txt") {
		t.Errorf("模式内文件缺失: %v", got)
	}
	if !strings.Contains(joined, "b/f2.txt") {
		t.Errorf("模式外的已跟踪文件被写成了删除: %v", got)
	}

	content, err := runner.run(ctx, dir, nil, "show", commit+":a/f1.txt")
	if err != nil {
		t.Fatalf("show checkpoint a/f1.txt: %v", err)
	}
	if !strings.Contains(string(content), "f1 changed") {
		t.Errorf("检查点未记录模式内的修改: %q", string(content))
	}
}
