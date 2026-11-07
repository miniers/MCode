package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initSyncRemote 创建带首个提交的本地仓库，并接入 t.TempDir 下的裸仓库 origin（默认分支 main）。
// 仅使用本地裸仓库，不涉及任何真实远程。
func initSyncRemote(t *testing.T) (runner *Runner, dir string, bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 可执行文件不在 PATH 中，跳过测试")
	}
	runner, dir = newRevertTestRepo(t)
	bare = filepath.Join(t.TempDir(), "origin.git")
	runTestGit(t, t.TempDir(), "init", "--bare", "-q", bare)
	runTestGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	// 不同 Git 版本/配置的初始分支名不同，统一改名为 main
	if cur := runTestGit(t, dir, "symbolic-ref", "--short", "HEAD"); cur != "main" {
		runTestGit(t, dir, "branch", "-m", "main")
	}
	runTestGit(t, dir, "remote", "add", "origin", bare)
	runTestGit(t, dir, "push", "-q", "-u", "origin", "main")
	return runner, dir, bare
}

// initSecondaryCheckout 在临时目录准备 origin 的第二个检出（detached HEAD），
// 用于制造远端前进或远端 squash 提交。
func initSecondaryCheckout(t *testing.T, bare string) string {
	t.Helper()
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q")
	runTestGit(t, dir, "config", "user.name", "mcode-test")
	runTestGit(t, dir, "config", "user.email", "mcode-test@localhost")
	runTestGit(t, dir, "remote", "add", "origin", bare)
	runTestGit(t, dir, "fetch", "-q", "origin")
	runTestGit(t, dir, "checkout", "-q", "--detach", "origin/main")
	return dir
}

func TestDefaultRemoteBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("回退到远端跟踪的 main", func(t *testing.T) {
		runner, dir, _ := initSyncRemote(t)
		got, err := runner.DefaultRemoteBranch(context.Background(), dir, "origin")
		if err != nil {
			t.Fatalf("DefaultRemoteBranch failed: %v", err)
		}
		if got != "main" {
			t.Errorf("got %q, want main", got)
		}
	})

	t.Run("优先使用 origin/HEAD 符号引用", func(t *testing.T) {
		runner, dir, _ := initSyncRemote(t)
		runTestGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/develop")
		runTestGit(t, dir, "remote", "set-head", "origin", "develop")
		got, err := runner.DefaultRemoteBranch(context.Background(), dir, "origin")
		if err != nil {
			t.Fatalf("DefaultRemoteBranch failed: %v", err)
		}
		if got != "develop" {
			t.Errorf("got %q, want develop", got)
		}
	})

	t.Run("无远端 main/master 时回退当前分支", func(t *testing.T) {
		runner, dir, _ := initSyncRemote(t)
		runTestGit(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
		runTestGit(t, dir, "branch", "-m", "trunk")
		got, err := runner.DefaultRemoteBranch(context.Background(), dir, "origin")
		if err != nil {
			t.Fatalf("DefaultRemoteBranch failed: %v", err)
		}
		if got != "trunk" {
			t.Errorf("got %q, want trunk", got)
		}
	})

	t.Run("仅有远端 master 时返回 master", func(t *testing.T) {
		runner, dir, _ := initSyncRemote(t)
		runTestGit(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
		runTestGit(t, dir, "update-ref", "refs/remotes/origin/master", "HEAD")
		got, err := runner.DefaultRemoteBranch(context.Background(), dir, "origin")
		if err != nil {
			t.Fatalf("DefaultRemoteBranch failed: %v", err)
		}
		if got != "master" {
			t.Errorf("got %q, want master", got)
		}
	})
}

func TestCommitPaths_OnlyCommitsGivenPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	// unstaged.txt 需先被跟踪：未跟踪文件的修改在 status --porcelain 中显示为 ??，不会出现 " M"。
	writeTestFile(t, dir, "unstaged.txt", "u\n")
	runTestGit(t, dir, "add", "unstaged.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "track unstaged")

	writeTestFile(t, dir, "a.txt", "a\n")
	writeTestFile(t, dir, "staged.txt", "s\n")
	runTestGit(t, dir, "add", "staged.txt")
	writeTestFile(t, dir, "unstaged.txt", "u2\n")

	headBefore := runTestGit(t, dir, "rev-parse", "HEAD")
	sha, err := runner.CommitPaths(ctx, dir, []string{"a.txt"}, "swarm: 提交 a")
	if err != nil {
		t.Fatalf("CommitPaths failed: %v", err)
	}
	if sha == "" || sha == headBefore {
		t.Fatalf("应产生新提交，sha=%q headBefore=%q", sha, headBefore)
	}

	committed := runTestGit(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	if committed != "a.txt" {
		t.Errorf("提交内容 = %q, want 仅 a.txt", committed)
	}
	if subject := runTestGit(t, dir, "log", "-1", "--format=%s", sha); subject != "swarm: 提交 a" {
		t.Errorf("提交说明 = %q", subject)
	}

	// 未列入 paths 的暂存改动不会被提交，且仍保留在暂存区
	status := runTestGit(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "A  staged.txt") {
		t.Errorf("staged.txt 应仍处于暂存状态，status:\n%s", status)
	}
	if !strings.Contains(status, " M unstaged.txt") {
		t.Errorf("unstaged.txt 的未暂存改动应保留，status:\n%s", status)
	}
	if strings.Contains(status, "a.txt") {
		t.Errorf("a.txt 提交后索引应干净，status:\n%s", status)
	}
	if readTestFile(t, dir, "unstaged.txt") != "u2\n" {
		t.Errorf("工作区修改不应被覆盖")
	}

	// 再次提交同一路径（无改动）：返回空字符串且不产生新提交
	sha2, err := runner.CommitPaths(ctx, dir, []string{"a.txt"}, "重复提交")
	if err != nil || sha2 != "" {
		t.Errorf("无改动时应返回空 sha 与 nil err，实际 sha=%q err=%v", sha2, err)
	}
	if headNow := runTestGit(t, dir, "rev-parse", "HEAD"); headNow != sha {
		t.Errorf("HEAD 不应变化，实际 %s", headNow)
	}
}

func TestCommitPaths_UnbornHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := NewRunner("git", 2)
	dir := t.TempDir()
	ctx := context.Background()

	runTestGit(t, dir, "init", "-q")
	runTestGit(t, dir, "config", "user.name", "mcode-test")
	runTestGit(t, dir, "config", "user.email", "mcode-test@localhost")
	writeTestFile(t, dir, "first.txt", "1\n")

	sha, err := runner.CommitPaths(ctx, dir, []string{"first.txt"}, "首个提交")
	if err != nil {
		t.Fatalf("CommitPaths failed: %v", err)
	}
	if sha == "" {
		t.Fatal("未出生 HEAD 上应创建根提交")
	}
	if subject := runTestGit(t, dir, "log", "-1", "--format=%s"); subject != "首个提交" {
		t.Errorf("提交说明 = %q", subject)
	}
	tree := runTestGit(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if tree != "first.txt" {
		t.Errorf("HEAD 树 = %q, want first.txt", tree)
	}

	sha2, err := runner.CommitPaths(ctx, dir, []string{"first.txt"}, "再次提交")
	if err != nil || sha2 != "" {
		t.Errorf("无改动时应返回空 sha 与 nil err，实际 sha=%q err=%v", sha2, err)
	}

	sha3, err := runner.CommitPaths(ctx, dir, nil, "空路径")
	if err != nil || sha3 != "" {
		t.Errorf("空 paths 应直接返回，实际 sha=%q err=%v", sha3, err)
	}
}

func TestPushRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir, bare := initSyncRemote(t)
	ctx := context.Background()

	writeTestFile(t, dir, "push.txt", "p\n")
	sha, err := runner.CommitPaths(ctx, dir, []string{"push.txt"}, "推送用提交")
	if err != nil || sha == "" {
		t.Fatalf("CommitPaths failed: sha=%q err=%v", sha, err)
	}

	if err := runner.PushRef(ctx, dir, "origin", "HEAD", "mcode/swarm-test"); err != nil {
		t.Fatalf("PushRef failed: %v", err)
	}
	remoteSHA := runTestGit(t, bare, "rev-parse", "refs/heads/mcode/swarm-test")
	if remoteSHA != sha {
		t.Errorf("裸仓库分支 = %s, want %s", remoteSHA, sha)
	}
	if localHead := runTestGit(t, dir, "rev-parse", "HEAD"); localHead != sha {
		t.Errorf("本地 HEAD 不应变化：%s", localHead)
	}

	if err := runner.PushRef(ctx, dir, "origin", "", "branch"); err == nil {
		t.Error("缺少 localRef 时应返回错误")
	}
}

func TestSyncDefaultBranch_FastForward(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir, bare := initSyncRemote(t)
	ctx := context.Background()

	w2 := initSecondaryCheckout(t, bare)
	writeTestFile(t, w2, "remote.txt", "r\n")
	runTestGit(t, w2, "add", "-A")
	runTestGit(t, w2, "commit", "-q", "-m", "remote advance")
	runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")
	remoteHead := runTestGit(t, bare, "rev-parse", "refs/heads/main")

	done, err := runner.SyncDefaultBranch(ctx, dir, "origin", "main")
	if err != nil {
		t.Fatalf("SyncDefaultBranch failed: %v", err)
	}
	if !done {
		t.Fatal("祖先关系成立时应快进并返回 true")
	}
	if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != remoteHead {
		t.Errorf("本地 HEAD = %s, want %s", head, remoteHead)
	}
	if readTestFile(t, dir, "remote.txt") != "r\n" {
		t.Errorf("快进后工作区应出现远端新文件")
	}
}

func TestSyncDefaultBranch_SquashSameTree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir, bare := initSyncRemote(t)
	ctx := context.Background()

	// 本地产生一个提交（树 T）
	writeTestFile(t, dir, "shared.txt", "s\n")
	runTestGit(t, dir, "add", "shared.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "local work")
	localHead := runTestGit(t, dir, "rev-parse", "HEAD")

	// 远端把同一份改动 squash 成另一个提交（父提交仍是 base，树相同）
	w2 := initSecondaryCheckout(t, bare)
	writeTestFile(t, w2, "shared.txt", "s\n")
	runTestGit(t, w2, "add", "shared.txt")
	runTestGit(t, w2, "commit", "-q", "-m", "squash merged local work")
	runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")
	remoteHead := runTestGit(t, bare, "rev-parse", "refs/heads/main")

	if remoteHead == localHead {
		t.Fatal("测试前置条件失败：远端 squash 提交应与本地提交不同")
	}
	localTree := runTestGit(t, dir, "rev-parse", "HEAD^{tree}")
	remoteTree := runTestGit(t, bare, "rev-parse", "refs/heads/main^{tree}")
	if localTree != remoteTree {
		t.Fatalf("测试前置条件失败：两棵树应相同，local=%s remote=%s", localTree, remoteTree)
	}

	done, err := runner.SyncDefaultBranch(ctx, dir, "origin", "main")
	if err != nil {
		t.Fatalf("SyncDefaultBranch failed: %v", err)
	}
	if !done {
		t.Fatal("树内容一致时应软重置对齐并返回 true")
	}
	if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != remoteHead {
		t.Errorf("本地 HEAD = %s, want %s", head, remoteHead)
	}
	if status := runTestGit(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("软重置后索引应与 HEAD 一致，status:\n%s", status)
	}
	if readTestFile(t, dir, "shared.txt") != "s\n" {
		t.Errorf("工作区内容不应变化")
	}
}

func TestSyncDefaultBranch_KeepsCheckoutWhenDiverged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir, bare := initSyncRemote(t)
	ctx := context.Background()

	writeTestFile(t, dir, "local.txt", "l\n")
	runTestGit(t, dir, "add", "local.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "local only")
	localHead := runTestGit(t, dir, "rev-parse", "HEAD")

	w2 := initSecondaryCheckout(t, bare)
	writeTestFile(t, w2, "remote.txt", "r\n")
	runTestGit(t, w2, "add", "-A")
	runTestGit(t, w2, "commit", "-q", "-m", "remote advance")
	runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")

	done, err := runner.SyncDefaultBranch(ctx, dir, "origin", "main")
	if err != nil {
		t.Fatalf("SyncDefaultBranch failed: %v", err)
	}
	if done {
		t.Fatal("分叉且树不同时应保持原样并返回 false")
	}
	if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != localHead {
		t.Errorf("HEAD 不应变化：%s", head)
	}
	if readTestFile(t, dir, "local.txt") != "l\n" {
		t.Errorf("工作区内容不应变化")
	}
}

func TestSyncDefaultBranch_SkipsWhenNotOnBaseBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir, _ := initSyncRemote(t)

	runTestGit(t, dir, "checkout", "-q", "-b", "feature")
	head := runTestGit(t, dir, "rev-parse", "HEAD")

	done, err := runner.SyncDefaultBranch(context.Background(), dir, "origin", "main")
	if err != nil {
		t.Fatalf("SyncDefaultBranch failed: %v", err)
	}
	if done {
		t.Fatal("不在 base 分支时应返回 false")
	}
	if now := runTestGit(t, dir, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD 不应变化：%s", now)
	}
}

func TestPullFastForward(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	t.Run("快进成功", func(t *testing.T) {
		runner, dir, bare := initSyncRemote(t)
		w2 := initSecondaryCheckout(t, bare)
		writeTestFile(t, w2, "remote.txt", "r\n")
		runTestGit(t, w2, "add", "-A")
		runTestGit(t, w2, "commit", "-q", "-m", "remote advance")
		runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")
		remoteHead := runTestGit(t, bare, "rev-parse", "refs/heads/main")

		if err := runner.PullFastForward(ctx, dir, "origin", "main"); err != nil {
			t.Fatalf("PullFastForward failed: %v", err)
		}
		if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != remoteHead {
			t.Errorf("本地 HEAD = %s, want %s", head, remoteHead)
		}
	})

	t.Run("分叉时报错且不改动本地", func(t *testing.T) {
		runner, dir, bare := initSyncRemote(t)
		writeTestFile(t, dir, "local.txt", "l\n")
		runTestGit(t, dir, "add", "local.txt")
		runTestGit(t, dir, "commit", "-q", "-m", "local only")
		localHead := runTestGit(t, dir, "rev-parse", "HEAD")

		w2 := initSecondaryCheckout(t, bare)
		writeTestFile(t, w2, "remote.txt", "r\n")
		runTestGit(t, w2, "add", "-A")
		runTestGit(t, w2, "commit", "-q", "-m", "remote advance")
		runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")

		if err := runner.PullFastForward(ctx, dir, "origin", "main"); err == nil {
			t.Fatal("分叉时应返回错误")
		}
		if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != localHead {
			t.Errorf("HEAD 不应变化：%s", head)
		}
	})
}

// PullCurrentFastForward 先 fetch 当前分支的上游再 merge --ff-only：本地从未抓取、
// 远程跟踪分支还停在旧提交时（AheadBehind 看到 0/0）也能快进到远端最新提交。
func TestPullCurrentFastForward(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	t.Run("本地未抓取时先 fetch 再快进", func(t *testing.T) {
		runner, dir, bare := initSyncRemote(t)
		w2 := initSecondaryCheckout(t, bare)
		writeTestFile(t, w2, "remote.txt", "r\n")
		runTestGit(t, w2, "add", "-A")
		runTestGit(t, w2, "commit", "-q", "-m", "remote advance")
		runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")
		remoteHead := runTestGit(t, bare, "rev-parse", "refs/heads/main")

		// 本地没 fetch 过，origin/main 仍是旧提交，正是拉取按钮以前被禁用的场景
		if ahead, behind, err := runner.AheadBehind(ctx, dir); err != nil || ahead != 0 || behind != 0 {
			t.Fatalf("未抓取时应为 0/0，got ahead=%d behind=%d err=%v", ahead, behind, err)
		}
		if err := runner.PullCurrentFastForward(ctx, dir); err != nil {
			t.Fatalf("PullCurrentFastForward failed: %v", err)
		}
		if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != remoteHead {
			t.Errorf("本地 HEAD = %s, want %s", head, remoteHead)
		}
	})

	t.Run("已是最新时成功且 HEAD 不变", func(t *testing.T) {
		runner, dir, _ := initSyncRemote(t)
		headBefore := runTestGit(t, dir, "rev-parse", "HEAD")
		if err := runner.PullCurrentFastForward(ctx, dir); err != nil {
			t.Fatalf("PullCurrentFastForward failed: %v", err)
		}
		if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != headBefore {
			t.Errorf("HEAD 不应变化：%s", head)
		}
	})

	t.Run("分叉时报错且不改动本地", func(t *testing.T) {
		runner, dir, bare := initSyncRemote(t)
		writeTestFile(t, dir, "local.txt", "l\n")
		runTestGit(t, dir, "add", "local.txt")
		runTestGit(t, dir, "commit", "-q", "-m", "local only")
		localHead := runTestGit(t, dir, "rev-parse", "HEAD")

		w2 := initSecondaryCheckout(t, bare)
		writeTestFile(t, w2, "remote.txt", "r\n")
		runTestGit(t, w2, "add", "-A")
		runTestGit(t, w2, "commit", "-q", "-m", "remote advance")
		runTestGit(t, w2, "push", "-q", "origin", "HEAD:refs/heads/main")

		if err := runner.PullCurrentFastForward(ctx, dir); err == nil {
			t.Fatal("分叉时应返回错误")
		}
		if head := runTestGit(t, dir, "rev-parse", "HEAD"); head != localHead {
			t.Errorf("HEAD 不应变化：%s", head)
		}
	})
}

func TestDiffPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "a.txt", "aaa\n")
	writeTestFile(t, dir, "b.txt", "bbb\n")

	diff, err := runner.DiffPaths(ctx, dir, []string{"a.txt"})
	if err != nil {
		t.Fatalf("DiffPaths failed: %v", err)
	}
	if !strings.Contains(diff, "a.txt") || !strings.Contains(diff, "+aaa") {
		t.Errorf("diff 应包含 a.txt 的内容：\n%s", diff)
	}
	if strings.Contains(diff, "b.txt") {
		t.Errorf("diff 不应包含未指定路径：\n%s", diff)
	}

	all, err := runner.DiffPaths(ctx, dir, []string{"b.txt", "a.txt", "b.txt"})
	if err != nil {
		t.Fatalf("DiffPaths failed: %v", err)
	}
	if !strings.Contains(all, "a.txt") || !strings.Contains(all, "b.txt") {
		t.Errorf("多路径 diff 不完整：\n%s", all)
	}
}
