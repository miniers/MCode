package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPRThreadState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("同仓有上游且有本地提交", func(t *testing.T) {
		t.Parallel()
		runner, dir, _ := initSyncRemote(t)
		runTestGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/feat-1")

		wtPath := filepath.Join(t.TempDir(), "pr-1")
		if err := runner.PRCheckoutWorktree(ctx, dir, wtPath, ForgeGitHub, 1, "feat-1", false); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, wtPath, "local.txt", "local\n")
		runTestGit(t, wtPath, "add", "local.txt")
		runTestGit(t, wtPath, "commit", "-qm", "local commit")

		state, ok, err := runner.PRThreadState(ctx, wtPath)
		if err != nil {
			t.Fatalf("PRThreadState 失败: %v", err)
		}
		if !ok {
			t.Fatal("期望 ok=true")
		}
		if state.Number != 1 || state.CrossRepo || state.Dirty || state.LocalCommits != 1 {
			t.Fatalf("state = %+v，期望 Number=1 CrossRepo=false Dirty=false LocalCommits=1", state)
		}
	})

	t.Run("fork 无上游", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)
		headSHA := runTestGit(t, dir, "rev-parse", "HEAD")
		runTestGit(t, bare, "update-ref", "refs/pull/2/head", headSHA)

		wtPath := filepath.Join(t.TempDir(), "pr-2")
		if err := runner.PRCheckoutWorktree(ctx, dir, wtPath, ForgeGitHub, 2, "", true); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, wtPath, "fork.txt", "fork\n")
		runTestGit(t, wtPath, "add", "fork.txt")
		runTestGit(t, wtPath, "commit", "-qm", "fork local")

		state, ok, err := runner.PRThreadState(ctx, wtPath)
		if err != nil {
			t.Fatalf("PRThreadState 失败: %v", err)
		}
		if !ok {
			t.Fatal("期望 ok=true")
		}
		if state.Number != 2 || !state.CrossRepo || state.Dirty || state.LocalCommits != 1 {
			t.Fatalf("state = %+v，期望 Number=2 CrossRepo=true Dirty=false LocalCommits=1", state)
		}
	})

	t.Run("有未提交改动", func(t *testing.T) {
		t.Parallel()
		runner, dir, _ := initSyncRemote(t)
		runTestGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/feat-3")

		wtPath := filepath.Join(t.TempDir(), "pr-3")
		if err := runner.PRCheckoutWorktree(ctx, dir, wtPath, ForgeGitHub, 3, "feat-3", false); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, wtPath, "dirty.txt", "dirty\n")

		state, ok, err := runner.PRThreadState(ctx, wtPath)
		if err != nil {
			t.Fatalf("PRThreadState 失败: %v", err)
		}
		if !ok || !state.Dirty || state.Number != 3 {
			t.Fatalf("state = %+v, ok = %v，期望 ok=true Number=3 Dirty=true", state, ok)
		}
	})

	t.Run("非 PR 分支", func(t *testing.T) {
		t.Parallel()
		runner, dir, _ := initSyncRemote(t)
		state, ok, err := runner.PRThreadState(ctx, dir)
		if err != nil {
			t.Fatalf("PRThreadState 失败: %v", err)
		}
		if ok {
			t.Fatalf("期望 ok=false，得到 state = %+v", state)
		}
	})
}

func TestPRRebaseOntoBase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("成功", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)

		// 在另一个检出构造 fork PR 的原始提交并推到 refs/pull/10/head。
		other := initSecondaryCheckout(t, bare)
		runTestGit(t, other, "checkout", "-qb", "fork-pr")
		writeTestFile(t, other, "pr.txt", "from pr\n")
		runTestGit(t, other, "add", "pr.txt")
		runTestGit(t, other, "commit", "-qm", "fork pr commit")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/pull/10/head")

		// 在 worktree 检出 fork PR 并追加一个本地提交。
		wtPath := filepath.Join(t.TempDir(), "pr-10")
		if err := runner.PRCheckoutWorktree(ctx, dir, wtPath, ForgeGitHub, 10, "", true); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, wtPath, "followup.txt", "followup\n")
		runTestGit(t, wtPath, "add", "followup.txt")
		runTestGit(t, wtPath, "commit", "-qm", "agent followup")

		// 模拟远端 main 已合并该 PR（含 pr.txt）。
		runTestGit(t, other, "checkout", "-q", "--detach", "origin/main")
		writeTestFile(t, other, "pr.txt", "from pr\n")
		runTestGit(t, other, "add", "pr.txt")
		runTestGit(t, other, "commit", "-qm", "merged pr 10")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")

		if err := runner.PRRebaseOntoBase(ctx, wtPath, 10, "main"); err != nil {
			t.Fatalf("PRRebaseOntoBase 失败: %v", err)
		}

		remoteLog := runTestGit(t, bare, "log", "-n", "1", "--format=%s", "refs/heads/main")
		if remoteLog != "agent followup" {
			t.Fatalf("远端 main 最新提交 = %q，期望 %q", remoteLog, "agent followup")
		}
	})

	t.Run("冲突时回滚", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)

		other := initSecondaryCheckout(t, bare)
		runTestGit(t, other, "checkout", "-qb", "fork-pr")
		writeTestFile(t, other, "conflict.txt", "pr line\n")
		runTestGit(t, other, "add", "conflict.txt")
		runTestGit(t, other, "commit", "-qm", "fork pr commit")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/pull/11/head")

		wtPath := filepath.Join(t.TempDir(), "pr-11")
		if err := runner.PRCheckoutWorktree(ctx, dir, wtPath, ForgeGitHub, 11, "", true); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, wtPath, "conflict.txt", "agent line\n")
		runTestGit(t, wtPath, "commit", "-qam", "agent change")
		origHead := runTestGit(t, wtPath, "rev-parse", "HEAD")

		// 远端 main 对同一文件同一行写入不同内容，制造 rebase 冲突。
		runTestGit(t, other, "checkout", "-q", "--detach", "origin/main")
		writeTestFile(t, other, "conflict.txt", "base line\n")
		runTestGit(t, other, "add", "conflict.txt")
		runTestGit(t, other, "commit", "-qm", "advance main")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")

		err := runner.PRRebaseOntoBase(ctx, wtPath, 11, "main")
		if err == nil || !strings.Contains(err.Error(), "冲突") {
			t.Fatalf("err = %v，期望包含“冲突”", err)
		}
		if curHead := runTestGit(t, wtPath, "rev-parse", "HEAD"); curHead != origHead {
			t.Fatalf("HEAD = %s，期望回滚到 %s", curHead, origHead)
		}
		if status := runTestGit(t, wtPath, "status", "--porcelain"); status != "" {
			t.Fatalf("冲突中止后工作区不干净: %q", status)
		}
	})
}

func TestPRRebaseOntoBaseRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	runner, dir, bare := initSyncRemote(t)

	other := initSecondaryCheckout(t, bare)
	runTestGit(t, other, "checkout", "-qb", "fork-pr")
	writeTestFile(t, other, "pr.txt", "from pr\n")
	runTestGit(t, other, "add", "pr.txt")
	runTestGit(t, other, "commit", "-qm", "fork pr commit")
	runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/pull/12/head")

	wtPath := filepath.Join(t.TempDir(), "pr-12")
	if err := runner.PRCheckoutWorktree(ctx, dir, wtPath, ForgeGitHub, 12, "", true); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, wtPath, "followup.txt", "followup\n")
	runTestGit(t, wtPath, "add", "followup.txt")
	runTestGit(t, wtPath, "commit", "-qm", "agent followup")

	runTestGit(t, other, "checkout", "-q", "--detach", "origin/main")
	writeTestFile(t, other, "pr.txt", "from pr\n")
	runTestGit(t, other, "add", "pr.txt")
	runTestGit(t, other, "commit", "-qm", "merged pr 12")
	runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")
	baseCommits := runTestGit(t, bare, "log", "--format=%s", "refs/heads/main")

	// 仅让推送失败而保留 fetch 可用，模拟 rebase 成功但推回失败的场景。
	runTestGit(t, wtPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if err := runner.PRRebaseOntoBase(ctx, wtPath, 12, "main"); err == nil {
		t.Fatal("第一次 PRRebaseOntoBase 期望因推送失败返回错误")
	}
	rebasedHead := runTestGit(t, wtPath, "rev-parse", "HEAD")

	// 恢复远端推送地址并重试，应跳过重复 rebase 并直接推回。
	runTestGit(t, wtPath, "config", "--unset", "remote.origin.pushurl")
	if err := runner.PRRebaseOntoBase(ctx, wtPath, 12, "main"); err != nil {
		t.Fatalf("第二次 PRRebaseOntoBase 失败: %v", err)
	}
	if curHead := runTestGit(t, wtPath, "rev-parse", "HEAD"); curHead != rebasedHead {
		t.Fatalf("重试后 HEAD = %s，期望保持 %s 不变", curHead, rebasedHead)
	}

	gotCommits := runTestGit(t, bare, "log", "--format=%s", "refs/heads/main")
	wantCommits := "agent followup\n" + baseCommits
	if gotCommits != wantCommits {
		t.Fatalf("远端 main 提交列表 =\n%s\n期望 =\n%s", gotCommits, wantCommits)
	}
}

func TestFastForwardLocalBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("base 已检出且干净", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)
		other := initSecondaryCheckout(t, bare)
		writeTestFile(t, other, "next.txt", "next\n")
		runTestGit(t, other, "add", "next.txt")
		runTestGit(t, other, "commit", "-qm", "advance main")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")
		wantSHA := runTestGit(t, other, "rev-parse", "HEAD")

		if err := runner.FastForwardLocalBranch(ctx, dir, "main"); err != nil {
			t.Fatalf("FastForwardLocalBranch 失败: %v", err)
		}
		if gotSHA := runTestGit(t, dir, "rev-parse", "HEAD"); gotSHA != wantSHA {
			t.Fatalf("main HEAD = %s，期望 %s", gotSHA, wantSHA)
		}
	})

	t.Run("base 未检出", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)
		runTestGit(t, dir, "switch", "-q", "-c", "other")

		other := initSecondaryCheckout(t, bare)
		writeTestFile(t, other, "next.txt", "next\n")
		runTestGit(t, other, "add", "next.txt")
		runTestGit(t, other, "commit", "-qm", "advance main")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")
		wantSHA := runTestGit(t, other, "rev-parse", "HEAD")

		if err := runner.FastForwardLocalBranch(ctx, dir, "main"); err != nil {
			t.Fatalf("FastForwardLocalBranch 失败: %v", err)
		}
		if gotSHA := runTestGit(t, dir, "rev-parse", "refs/heads/main"); gotSHA != wantSHA {
			t.Fatalf("refs/heads/main = %s，期望 %s", gotSHA, wantSHA)
		}
	})

	t.Run("base 已检出且有未推送提交", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)
		other := initSecondaryCheckout(t, bare)
		writeTestFile(t, other, "next.txt", "next\n")
		runTestGit(t, other, "add", "next.txt")
		runTestGit(t, other, "commit", "-qm", "advance main")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")

		writeTestFile(t, dir, "local.txt", "local\n")
		runTestGit(t, dir, "add", "local.txt")
		runTestGit(t, dir, "commit", "-qm", "local commit")

		if err := runner.FastForwardLocalBranch(ctx, dir, "main"); err != nil {
			t.Fatalf("FastForwardLocalBranch 失败: %v", err)
		}
		merged, err := runner.IsBranchMerged(ctx, dir, "origin/main", "HEAD")
		if err != nil || !merged {
			t.Fatalf("期望 origin/main 是 HEAD 祖先，merged=%v, err=%v", merged, err)
		}
		if gotMsg := runTestGit(t, dir, "log", "-n", "1", "--format=%s", "HEAD"); gotMsg != "local commit" {
			t.Fatalf("HEAD 提交说明 = %q，期望 %q", gotMsg, "local commit")
		}
		if status := runTestGit(t, dir, "status", "--porcelain"); status != "" {
			t.Fatalf("工作区不干净: %q", status)
		}
	})

	t.Run("base 已检出且变基冲突", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)
		other := initSecondaryCheckout(t, bare)
		writeTestFile(t, other, "conflict.txt", "remote line\n")
		runTestGit(t, other, "add", "conflict.txt")
		runTestGit(t, other, "commit", "-qm", "remote conflict")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")

		writeTestFile(t, dir, "conflict.txt", "local line\n")
		runTestGit(t, dir, "add", "conflict.txt")
		runTestGit(t, dir, "commit", "-qm", "local conflict")
		origHead := runTestGit(t, dir, "rev-parse", "HEAD")

		err := runner.FastForwardLocalBranch(ctx, dir, "main")
		if err == nil || !strings.Contains(err.Error(), "冲突") {
			t.Fatalf("err = %v，期望包含“冲突”", err)
		}
		for _, stateDir := range []string{"rebase-merge", "rebase-apply"} {
			if _, statErr := os.Stat(filepath.Join(dir, ".git", stateDir)); !os.IsNotExist(statErr) {
				t.Fatalf("期望 %s 不存在，statErr = %v", stateDir, statErr)
			}
		}
		if curHead := runTestGit(t, dir, "rev-parse", "HEAD"); curHead != origHead {
			t.Fatalf("HEAD = %s，期望回滚到 %s", curHead, origHead)
		}
	})

	t.Run("base 未检出且本地已包含远端", func(t *testing.T) {
		t.Parallel()
		runner, dir, _ := initSyncRemote(t)
		writeTestFile(t, dir, "local.txt", "local\n")
		runTestGit(t, dir, "add", "local.txt")
		runTestGit(t, dir, "commit", "-qm", "local ahead")
		wantSHA := runTestGit(t, dir, "rev-parse", "refs/heads/main")
		runTestGit(t, dir, "switch", "-q", "-c", "other")

		if err := runner.FastForwardLocalBranch(ctx, dir, "main"); err != nil {
			t.Fatalf("FastForwardLocalBranch 失败: %v", err)
		}
		if gotSHA := runTestGit(t, dir, "rev-parse", "refs/heads/main"); gotSHA != wantSHA {
			t.Fatalf("refs/heads/main = %s，期望 %s", gotSHA, wantSHA)
		}
	})

	t.Run("base 未检出且分叉", func(t *testing.T) {
		t.Parallel()
		runner, dir, bare := initSyncRemote(t)
		writeTestFile(t, dir, "local.txt", "local\n")
		runTestGit(t, dir, "add", "local.txt")
		runTestGit(t, dir, "commit", "-qm", "local commit")
		wantSHA := runTestGit(t, dir, "rev-parse", "refs/heads/main")
		runTestGit(t, dir, "switch", "-q", "-c", "other")

		other := initSecondaryCheckout(t, bare)
		writeTestFile(t, other, "next.txt", "next\n")
		runTestGit(t, other, "add", "next.txt")
		runTestGit(t, other, "commit", "-qm", "advance main")
		runTestGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")

		err := runner.FastForwardLocalBranch(ctx, dir, "main")
		if err == nil || !strings.Contains(err.Error(), "分叉") {
			t.Fatalf("err = %v，期望包含“分叉”", err)
		}
		if gotSHA := runTestGit(t, dir, "rev-parse", "refs/heads/main"); gotSHA != wantSHA {
			t.Fatalf("refs/heads/main = %s，期望 %s", gotSHA, wantSHA)
		}
	})
}
