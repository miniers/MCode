package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

// TestPollWatcherGeneration 验证轮询监听器：未改动时代次不变，改文件后递增，.git 内的变化不计入。
func TestPollWatcherGeneration(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	root := t.TempDir()

	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("a\n"), 0644); err != nil {
		t.Fatalf("写入 a.txt 失败: %v", err)
	}

	w := newPollWatcher(root)
	defer func() {
		if err := w.Close(); err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	}()

	gen1, err := w.Generation(ctx)
	if err != nil {
		t.Fatalf("首次 Generation 失败: %v", err)
	}
	gen2, err := w.Generation(ctx)
	if err != nil {
		t.Fatalf("再次 Generation 失败: %v", err)
	}
	if gen1 != gen2 {
		t.Fatalf("未改动时 generation 应保持不变: %d -> %d", gen1, gen2)
	}

	if err := os.WriteFile(target, []byte("a\nb\n"), 0644); err != nil {
		t.Fatalf("修改 a.txt 失败: %v", err)
	}
	gen3, err := w.Generation(ctx)
	if err != nil {
		t.Fatalf("修改后 Generation 失败: %v", err)
	}
	if gen3 <= gen2 {
		t.Fatalf("改文件后 generation 应递增: %d -> %d", gen2, gen3)
	}
	gen4, err := w.Generation(ctx)
	if err != nil {
		t.Fatalf("再次 Generation 失败: %v", err)
	}
	if gen4 != gen3 {
		t.Fatalf("改动后无新变化，generation 应保持: %d -> %d", gen3, gen4)
	}

	// .git 目录被跳过，其中的写入不影响代次
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("创建 .git 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("x\n"), 0644); err != nil {
		t.Fatalf("写入 .git/index 失败: %v", err)
	}
	gen5, err := w.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation 失败: %v", err)
	}
	if gen5 != gen4 {
		t.Fatalf(".git 内的写入不应改变 generation: %d -> %d", gen4, gen5)
	}
	wtDir := filepath.Join(root, ".mcode", "worktrees", "task")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "change.txt"), []byte("worktree"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen6, err := w.Generation(ctx)
	if err != nil || gen6 != gen5 {
		t.Fatalf("项目内 worktree 不应改变 generation: %d -> %d，错误 %v", gen5, gen6, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcode", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen7, err := w.Generation(ctx)
	if err != nil || gen7 <= gen6 {
		t.Fatalf(".mcode 其他文件仍应被监听: %d -> %d，错误 %v", gen6, gen7, err)
	}
}

// TestCheckpointWatchedReusesTree 验证监听命中时复用 tree、未命中时重新快照。
func TestCheckpointWatchedReusesTree(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	// 先做一次正常提交，让 HEAD 存在
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("a\n"), 0644); err != nil {
		t.Fatalf("写入 a.txt 失败: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "-A"); err != nil {
		t.Fatalf("git add 失败: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "init"); err != nil {
		t.Fatalf("git commit 失败: %v", err)
	}

	w := newPollWatcher(dir)
	defer func() {
		if err := w.Close(); err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	}()

	ref1 := CheckpointRef("thread-watch", "1")
	commit1, err := runner.CheckpointWatched(ctx, w, dir, ref1, "turn 1")
	if err != nil {
		t.Fatalf("首次 CheckpointWatched 失败: %v", err)
	}
	tree1 := treeOfCommit(t, runner, dir, commit1)

	// 工作区未改动：第二次快照应复用同一 tree
	ref2 := CheckpointRef("thread-watch", "2")
	commit2, err := runner.CheckpointWatched(ctx, w, dir, ref2, "turn 2")
	if err != nil {
		t.Fatalf("第二次 CheckpointWatched 失败: %v", err)
	}
	tree2 := treeOfCommit(t, runner, dir, commit2)
	if tree1 != tree2 {
		t.Fatalf("未改动时两次快照的 tree 应相同: %s != %s", tree1, tree2)
	}
	if commit1 == commit2 {
		t.Fatalf("每次检查点应是新的提交对象: %s", commit1)
	}

	// 工作区变化：应重新快照，tree 不同
	if err := os.WriteFile(target, []byte("a\nb\n"), 0644); err != nil {
		t.Fatalf("修改 a.txt 失败: %v", err)
	}
	ref3 := CheckpointRef("thread-watch", "3")
	commit3, err := runner.CheckpointWatched(ctx, w, dir, ref3, "turn 3")
	if err != nil {
		t.Fatalf("第三次 CheckpointWatched 失败: %v", err)
	}
	tree3 := treeOfCommit(t, runner, dir, commit3)
	if tree3 == tree1 {
		t.Fatalf("改文件后 tree 应变化: %s", tree1)
	}

	// 引用已更新到最新提交
	out, err := runner.run(ctx, dir, nil, "rev-parse", ref3)
	if err != nil {
		t.Fatalf("读取引用失败: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != commit3 {
		t.Fatalf("引用未指向最新提交: %s != %s", got, commit3)
	}
}

// errWatcher 模拟平台监听不可用：CheckpointWatched 应退回完整快照。
type errWatcher struct{}

func (errWatcher) Generation(context.Context) (uint64, error) {
	return 0, errors.New("监听不可用")
}
func (errWatcher) Close() error { return nil }

// TestCheckpointWatchedFallsBackWithoutWatcher 验证监听缺失或失败时仍能完成快照。
func TestCheckpointWatchedFallsBackWithoutWatcher(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatalf("写入 a.txt 失败: %v", err)
	}

	for name, w := range map[string]Watcher{"nil": nil, "err": errWatcher{}} {
		ref := CheckpointRef("thread-fallback", name)
		commit, err := runner.CheckpointWatched(ctx, w, dir, ref, "fallback "+name)
		if err != nil {
			t.Fatalf("%s watcher: CheckpointWatched 失败: %v", name, err)
		}
		if commit == "" {
			t.Fatalf("%s watcher: 提交为空", name)
		}
		out, err := runner.run(ctx, dir, nil, "rev-parse", ref)
		if err != nil {
			t.Fatalf("%s watcher: 读取引用失败: %v", name, err)
		}
		if got := strings.TrimSpace(string(out)); got != commit {
			t.Fatalf("%s watcher: 引用未指向提交: %s != %s", name, got, commit)
		}
	}
}

// TestNewWatcherMissingRootFallsBack 验证不存在的路径会退回轮询实现且不 panic。
func TestNewWatcherMissingRootFallsBack(t *testing.T) {
	testenv.TempHome(t)
	root := filepath.Join(t.TempDir(), "missing")

	w := NewWatcher(root)
	if w == nil {
		t.Fatal("NewWatcher 不应返回 nil")
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	}()
	if _, err := w.Generation(context.Background()); err == nil {
		t.Fatal("根目录不存在时 Generation 应返回错误而不是 panic")
	}
}

// treeOfCommit 读取提交对应的 tree 哈希。
func treeOfCommit(t *testing.T, runner *Runner, dir, commit string) string {
	t.Helper()
	out, err := runner.run(context.Background(), dir, nil, "rev-parse", commit+"^{tree}")
	if err != nil {
		t.Fatalf("读取 %s 的 tree 失败: %v", commit, err)
	}
	return strings.TrimSpace(string(out))
}
