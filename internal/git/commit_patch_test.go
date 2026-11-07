package git

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCommitPatchesOnHead_PreservesUnstagedWorktreeChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	defaultBranch := runTestGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	// 准备基准提交：a.txt 包含三行
	initContent := "line 1: original\nline 2: original\nline 3: original\n"
	writeTestFile(t, dir, "a.txt", initContent)
	runTestGit(t, dir, "add", "a.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "commit with 3 lines")

	// 模拟快照分支，生成基于用户第 1 行改动的第 3 行修改补丁
	runTestGit(t, dir, "branch", "snapshot-branch")
	runTestGit(t, dir, "checkout", "-q", "-b", "bee-work", "snapshot-branch")
	writeTestFile(t, dir, "a.txt", "line 1: user-dirty\nline 2: original\nline 3: original\n")
	runTestGit(t, dir, "commit", "-q", "-am", "checkpoint snapshot")
	snapCommit := runTestGit(t, dir, "rev-parse", "HEAD")

	writeTestFile(t, dir, "a.txt", "line 1: user-dirty\nline 2: original\nline 3: bee-modified\n")
	runTestGit(t, dir, "commit", "-q", "-am", "bee modification")
	patchBytes := []byte(runTestGit(t, dir, "diff", "-U3", snapCommit, "HEAD") + "\n")

	// 切回默认分支，工作区落地 bee 的改动同时保留用户第 1 行未提交改动
	runTestGit(t, dir, "checkout", "-q", defaultBranch)
	worktreeContent := "line 1: user-dirty\nline 2: original\nline 3: bee-modified\n"
	writeTestFile(t, dir, "a.txt", worktreeContent)

	// 调用 CommitPatchesOnHead
	res, err := runner.CommitPatchesOnHead(ctx, dir, [][]byte{patchBytes}, "swarm: commit patch")
	if err != nil {
		t.Fatalf("CommitPatchesOnHead 失败: %v", err)
	}
	if res.SHA == "" {
		t.Fatal("期望生成新提交 SHA，实际为空")
	}
	if len(res.Conflicts) > 0 {
		t.Fatalf("不期望产生冲突: %v", res.Conflicts)
	}

	// 1. 新 commit 里的 a.txt 只有第 3 行的改动
	commitContent := runTestGit(t, dir, "cat-file", "-p", res.SHA+":a.txt")
	expectedCommitContent := "line 1: original\nline 2: original\nline 3: bee-modified"
	if commitContent != expectedCommitContent {
		t.Errorf("新 commit 内容不符合预期，期望只有第 3 行改动:\ngot:\n%s\nwant:\n%s", commitContent, expectedCommitContent)
	}

	// 2. 工作区文件内容不变
	actualWorktree := readTestFile(t, dir, "a.txt")
	if actualWorktree != worktreeContent {
		t.Errorf("工作区内容被修改: got %q, want %q", actualWorktree, worktreeContent)
	}

	// 3. git status 中 a.txt 仍是未暂存修改
	status := runTestGit(t, dir, "status", "--porcelain")
	foundModified := false
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "M a.txt" {
			foundModified = true
			break
		}
	}
	if !foundModified {
		t.Errorf("git status 应显示 a.txt 为未暂存修改，实际状态:\n%s", status)
	}
	cachedDiff := runTestGit(t, dir, "diff", "--cached", "--name-only")
	if strings.Contains(cachedDiff, "a.txt") {
		t.Errorf("a.txt 不应有已暂存修改，实际 cached diff:\n%s", cachedDiff)
	}

	// 4. git diff 只显示第 1 行
	diff := runTestGit(t, dir, "diff", "a.txt")
	if !strings.Contains(diff, "+line 1: user-dirty") || !strings.Contains(diff, "-line 1: original") {
		t.Errorf("git diff 应包含第 1 行改动:\n%s", diff)
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+line 3") || strings.HasPrefix(line, "-line 3") {
			t.Errorf("git diff 不应显示第 3 行改动:\n%s", diff)
			break
		}
	}
}

func TestCommitPatchesOnHead_AddNewFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	// 构造新增文件 b.txt 的补丁
	patch := []byte("diff --git a/b.txt b/b.txt\nnew file mode 100644\n--- /dev/null\n+++ b/b.txt\n@@ -0,0 +1 @@\n+hello from b\n")

	// 工作区也落地了 b.txt
	writeTestFile(t, dir, "b.txt", "hello from b\n")

	res, err := runner.CommitPatchesOnHead(ctx, dir, [][]byte{patch}, "add b.txt")
	if err != nil {
		t.Fatalf("CommitPatchesOnHead 失败: %v", err)
	}
	if res.SHA == "" {
		t.Fatal("期望生成新提交 SHA，实际为空")
	}

	// b.txt 在 HEAD 里
	bContent := runTestGit(t, dir, "cat-file", "-p", "HEAD:b.txt")
	if bContent != "hello from b" {
		t.Errorf("HEAD:b.txt 内容不符合预期: %q", bContent)
	}

	// 用户 index 已同步，b.txt 不显示为已暂存删除或修改
	status := runTestGit(t, dir, "status", "--porcelain")
	for _, line := range strings.Split(status, "\n") {
		if strings.Contains(line, "b.txt") {
			if len(line) >= 2 && (line[0] == 'D' || line[0] == 'M') {
				t.Errorf("b.txt 不应处于已暂存删除或修改状态: %q", line)
			}
		}
	}
}

func TestCommitPatchesOnHead_Conflict(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "conflict.txt", "line 1: base\n")
	runTestGit(t, dir, "add", "conflict.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "base commit")

	// HEAD 上提交版本 B
	writeTestFile(t, dir, "conflict.txt", "line 1: head-change\n")
	runTestGit(t, dir, "commit", "-q", "-am", "head change")
	currentHead := runTestGit(t, dir, "rev-parse", "HEAD")

	// 制作基于 base 修改同一行的补丁
	patch := []byte("diff --git a/conflict.txt b/conflict.txt\n--- a/conflict.txt\n+++ b/conflict.txt\n@@ -1 +1 @@\n-line 1: base\n+line 1: incoming-change\n")

	res, err := runner.CommitPatchesOnHead(ctx, dir, [][]byte{patch}, "conflict commit")
	if err != nil {
		t.Fatalf("冲突时不应返回致命 error，应返回 Conflicts: %v", err)
	}
	if res.SHA != "" {
		t.Errorf("冲突时不应生成新 commit，实际 SHA=%s", res.SHA)
	}
	if len(res.Conflicts) == 0 {
		t.Fatal("期望返回冲突路径列表，实际为空")
	}
	found := false
	for _, p := range res.Conflicts {
		if p == "conflict.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Conflicts 应包含 conflict.txt，实际为: %v", res.Conflicts)
	}

	// HEAD 不变
	afterHead := runTestGit(t, dir, "rev-parse", "HEAD")
	if afterHead != currentHead {
		t.Errorf("HEAD 发生变化: got %s, want %s", afterHead, currentHead)
	}
}

func TestCommitPatchesOnHead_CASFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	// 记录旧 HEAD
	oldHead := runTestGit(t, dir, "rev-parse", "HEAD")

	// 另起一个提交移动 HEAD
	writeTestFile(t, dir, "other.txt", "other content\n")
	runTestGit(t, dir, "add", "other.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "advance HEAD")
	newHead := runTestGit(t, dir, "rev-parse", "HEAD")

	// 准备一份补丁
	patch := []byte("diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new content\n")

	// 传入 expectedHead=oldHead 模拟并发下 HEAD 被移动导致的 CAS 失败
	res, err := runner.commitPatchesOnHead(ctx, dir, [][]byte{patch}, "cas test", oldHead)
	if err == nil {
		t.Fatal("CAS 失败时应返回错误，实际未报错")
	}
	if !strings.Contains(err.Error(), "HEAD") || !strings.Contains(err.Error(), "未改动") {
		t.Errorf("错误信息应说明 HEAD 被移动且分支未改动，实际为: %v", err)
	}
	if res.SHA != "" {
		t.Errorf("CAS 失败时不应返回 SHA: %s", res.SHA)
	}

	// 确认 HEAD 未被改动
	headNow := runTestGit(t, dir, "rev-parse", "HEAD")
	if headNow != newHead {
		t.Errorf("HEAD 被意外改动: got %s, want %s", headNow, newHead)
	}
}

func TestCommitPatchesOnHead_StagedKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "a.txt", "line 1: original\n")
	runTestGit(t, dir, "add", "a.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "init a.txt")

	// 用户在 index 里暂存了 a.txt 的另一版本
	writeTestFile(t, dir, "a.txt", "line 1: user-staged\n")
	runTestGit(t, dir, "add", "a.txt")

	// 补丁修改 a.txt 为 patch-version
	patch := []byte("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-line 1: original\n+line 1: patch-version\n")

	res, err := runner.CommitPatchesOnHead(ctx, dir, [][]byte{patch}, "patch commit")
	if err != nil {
		t.Fatalf("CommitPatchesOnHead 失败: %v", err)
	}
	if res.SHA == "" {
		t.Fatal("期望生成新提交 SHA，实际为空")
	}

	// a.txt 进入 StagedKept
	found := false
	for _, p := range res.StagedKept {
		if p == "a.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("期望 a.txt 进入 StagedKept，实际 StagedKept=%v", res.StagedKept)
	}

	// 用户的暂存区应保持原样（user-staged）
	cachedDiff := runTestGit(t, dir, "diff", "--cached", "a.txt")
	if !strings.Contains(cachedDiff, "user-staged") {
		t.Errorf("暂存区内容被冲掉: %s", cachedDiff)
	}
}

func TestCommitPatchesOnHead_NoHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q")
	runner := NewRunner("git", 2)
	ctx := context.Background()

	patch := []byte("diff --git a/a.txt b/a.txt\nnew file mode 100644\n--- /dev/null\n+++ b/a.txt\n@@ -0,0 +1 @@\n+a\n")
	_, err := runner.CommitPatchesOnHead(ctx, dir, [][]byte{patch}, "no head")
	if !errors.Is(err, ErrNoHead) {
		t.Errorf("期望返回 ErrNoHead，实际返回: %v", err)
	}
}

func TestCommitPatchesOnHead_EmptyPatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	headBefore := runTestGit(t, dir, "rev-parse", "HEAD")
	res, err := runner.CommitPatchesOnHead(ctx, dir, nil, "empty")
	if err != nil {
		t.Fatalf("空补丁不应报错: %v", err)
	}
	if res.SHA != "" {
		t.Errorf("空补丁不应生成提交: %s", res.SHA)
	}
	headAfter := runTestGit(t, dir, "rev-parse", "HEAD")
	if headBefore != headAfter {
		t.Errorf("HEAD 不应发生变化")
	}
}
