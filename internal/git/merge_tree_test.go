package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeTreeOnto_CleanMerge 覆盖场景 (a)：
// base 上 a.txt 有三行；onto 改第 1 行（模拟主工作区之后的未提交改动，用 Checkpoint 生成）；change 改第 3 行；
// MergeTreeOnto 无冲突，DiffTreesBinary(onto, tree) 用 ApplyPatch3Way 应用到工作内容等于 onto 的工作区后，文件同时含两处改动。
func TestMergeTreeOnto_CleanMerge(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	// 1. base 提交：a.txt 包含三行
	aPath := filepath.Join(dir, "a.txt")
	baseContent := "line 1\nline 2\nline 3\n"
	if err := os.WriteFile(aPath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "a.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "base commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	baseCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	// 2. 子代理独立 worktree：change 改第 3 行
	wtDir := filepath.Join(t.TempDir(), "wt-change")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "change-branch", baseCommit); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	wtAPath := filepath.Join(wtDir, "a.txt")
	changeContent := "line 1\nline 2\nline 3 modified by subagent\n"
	if err := os.WriteFile(wtAPath, []byte(changeContent), 0644); err != nil {
		t.Fatalf("write wt a.txt: %v", err)
	}
	if _, err := runner.run(ctx, wtDir, nil, "add", "a.txt"); err != nil {
		t.Fatalf("wt git add: %v", err)
	}
	if _, err := runner.run(ctx, wtDir, nil, "commit", "-m", "subagent change"); err != nil {
		t.Fatalf("wt git commit: %v", err)
	}
	changeCommit, err := runner.RevParse(ctx, wtDir, "HEAD")
	if err != nil {
		t.Fatalf("wt rev-parse HEAD: %v", err)
	}

	// 3. 主工作区：onto 改第 1 行（模拟主工作区之后的未提交改动，用 Checkpoint 生成）
	ontoContent := "line 1 modified by main\nline 2\nline 3\n"
	if err := os.WriteFile(aPath, []byte(ontoContent), 0644); err != nil {
		t.Fatalf("write main a.txt: %v", err)
	}
	ontoRef := CheckpointRef("thread-test", "onto-clean")
	ontoCommit, err := runner.Checkpoint(ctx, dir, ontoRef, "onto checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// 4. MergeTreeOnto 无冲突
	res, err := runner.MergeTreeOnto(ctx, dir, baseCommit, ontoCommit, changeCommit)
	if err != nil {
		t.Fatalf("MergeTreeOnto failed: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %v", res.Conflicts)
	}
	if res.Tree == "" {
		t.Fatalf("expected non-empty tree")
	}

	// 5. DiffTreesBinary(onto, tree)
	patch, err := runner.DiffTreesBinary(ctx, dir, ontoCommit, res.Tree)
	if err != nil {
		t.Fatalf("DiffTreesBinary failed: %v", err)
	}
	if len(patch) == 0 {
		t.Fatalf("expected non-empty patch")
	}

	// 6. 用 ApplyPatch3Way 应用到工作内容等于 onto 的工作区后，文件同时含两处改动
	applyRes, err := runner.ApplyPatch3Way(ctx, dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch3Way failed: %v, output: %s", err, applyRes.Output)
	}
	if applyRes.Conflicted {
		t.Fatalf("ApplyPatch3Way conflicted unexpectedly: %s", applyRes.Output)
	}

	data, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	expected := "line 1 modified by main\nline 2\nline 3 modified by subagent\n"
	if string(data) != expected {
		t.Fatalf("expected merged content:\n%q\ngot:\n%q", expected, string(data))
	}
}

// TestMergeTreeOnto_Conflict 覆盖场景 (b)：
// onto 和 change 改同一行，Conflicts 包含 a.txt，且主工作区文件不变。
func TestMergeTreeOnto_Conflict(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	// 1. base 提交：a.txt 包含三行
	aPath := filepath.Join(dir, "a.txt")
	baseContent := "line 1\nline 2\nline 3\n"
	if err := os.WriteFile(aPath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "a.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "base commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	baseCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	// 2. 子代理独立 worktree：change 改第 1 行
	wtDir := filepath.Join(t.TempDir(), "wt-conflict")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "conflict-branch", baseCommit); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	wtAPath := filepath.Join(wtDir, "a.txt")
	changeContent := "line 1 subagent edit\nline 2\nline 3\n"
	if err := os.WriteFile(wtAPath, []byte(changeContent), 0644); err != nil {
		t.Fatalf("write wt a.txt: %v", err)
	}
	if _, err := runner.run(ctx, wtDir, nil, "add", "a.txt"); err != nil {
		t.Fatalf("wt git add: %v", err)
	}
	if _, err := runner.run(ctx, wtDir, nil, "commit", "-m", "subagent conflict change"); err != nil {
		t.Fatalf("wt git commit: %v", err)
	}
	changeCommit, err := runner.RevParse(ctx, wtDir, "HEAD")
	if err != nil {
		t.Fatalf("wt rev-parse HEAD: %v", err)
	}

	// 3. 主工作区：onto 也改第 1 行
	ontoContent := "line 1 main edit\nline 2\nline 3\n"
	if err := os.WriteFile(aPath, []byte(ontoContent), 0644); err != nil {
		t.Fatalf("write main a.txt: %v", err)
	}
	ontoRef := CheckpointRef("thread-test", "onto-conflict")
	ontoCommit, err := runner.Checkpoint(ctx, dir, ontoRef, "onto conflict checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// 记录合并前主工作区内容
	contentBefore, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("read a.txt before merge: %v", err)
	}

	// 4. MergeTreeOnto 发生冲突
	res, err := runner.MergeTreeOnto(ctx, dir, baseCommit, ontoCommit, changeCommit)
	if err != nil {
		t.Fatalf("MergeTreeOnto returned unexpected error: %v", err)
	}

	// Conflicts 包含 a.txt
	var found bool
	for _, c := range res.Conflicts {
		if c == "a.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Conflicts to contain a.txt, got %v", res.Conflicts)
	}

	// 主工作区文件不变
	contentAfter, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("read a.txt after merge: %v", err)
	}
	if string(contentAfter) != string(contentBefore) {
		t.Fatalf("main worktree file changed, before:\n%s\nafter:\n%s", contentBefore, contentAfter)
	}
}

// TestCommitIndexTree 覆盖场景 (c)：
// CommitIndexTree 生成的 commit 的父提交等于 parent，且 HEAD 没变。
func TestCommitIndexTree(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	// 1. 基础提交
	file1 := filepath.Join(dir, "init.txt")
	if err := os.WriteFile(file1, []byte("init\n"), 0644); err != nil {
		t.Fatalf("write init.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "init.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "init commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	parentCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	// 2. 修改/新增文件并 add 到暂存区（调用方事先已 add 好）
	file2 := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(file2, []byte("second\n"), 0644); err != nil {
		t.Fatalf("write second.txt: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "second.txt"); err != nil {
		t.Fatalf("git add second.txt: %v", err)
	}

	// 3. 执行 CommitIndexTree
	commitSha, err := runner.CommitIndexTree(ctx, dir, parentCommit, "commit from index")
	if err != nil {
		t.Fatalf("CommitIndexTree failed: %v", err)
	}
	if commitSha == "" {
		t.Fatalf("expected non-empty commitSha")
	}

	// 4. 验证生成的 commit 的父提交等于 parent
	parentOut, err := runner.run(ctx, dir, nil, "rev-parse", commitSha+"^")
	if err != nil {
		t.Fatalf("rev-parse commit parent failed: %v", err)
	}
	actualParent := strings.TrimSpace(string(parentOut))
	if actualParent != parentCommit {
		t.Fatalf("expected parent %s, got %s", parentCommit, actualParent)
	}

	// 5. 验证 HEAD 没变
	headAfter, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD after: %v", err)
	}
	if headAfter != parentCommit {
		t.Fatalf("HEAD changed! expected %s, got %s", parentCommit, headAfter)
	}
}
