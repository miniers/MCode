package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_ApplyPatch3Way_Clean(t *testing.T) {
	t.Parallel()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("line 1\nline 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "file.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	baseCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse failed: %v", err)
	}

	wtDir := filepath.Join(t.TempDir(), "wt-clean")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "wt-branch-clean", baseCommit); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}

	wtFile := filepath.Join(wtDir, "file.txt")
	if err := os.WriteFile(wtFile, []byte("line 1\nline 2 modified\nline 3\n"), 0644); err != nil {
		t.Fatalf("write wtFile: %v", err)
	}

	if err := runner.AddAll(ctx, wtDir); err != nil {
		t.Fatalf("AddAll failed: %v", err)
	}

	names, err := runner.DiffCachedNames(ctx, wtDir, baseCommit)
	if err != nil {
		t.Fatalf("DiffCachedNames failed: %v", err)
	}
	if len(names) != 1 || names[0] != "file.txt" {
		t.Fatalf("expected [file.txt], got %v", names)
	}

	patch, err := runner.DiffCachedBinary(ctx, wtDir, baseCommit)
	if err != nil {
		t.Fatalf("DiffCachedBinary failed: %v", err)
	}
	if len(patch) == 0 {
		t.Fatalf("expected non-empty patch")
	}

	res, err := runner.ApplyPatch3Way(ctx, dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch3Way failed: %v", err)
	}
	if res.Conflicted {
		t.Fatalf("expected non-conflicted apply result")
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "line 1\nline 2 modified\nline 3\n" {
		t.Fatalf("unexpected content after apply: %s", string(content))
	}

	conflicts, err := ScanConflictMarkers(dir, names)
	if err != nil {
		t.Fatalf("ScanConflictMarkers failed: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %v", conflicts)
	}
}

func TestRunner_ApplyPatch3Way_Conflict(t *testing.T) {
	t.Parallel()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	filePath := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(filePath, []byte("line 1\nbase line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "shared.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	baseCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse failed: %v", err)
	}

	wtDir := filepath.Join(t.TempDir(), "wt-conflict")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "wt-branch-conflict", baseCommit); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}

	// 在 worktree 中修改第二行
	wtFile := filepath.Join(wtDir, "shared.txt")
	if err := os.WriteFile(wtFile, []byte("line 1\nhead change line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write wtFile: %v", err)
	}
	if err := runner.AddAll(ctx, wtDir); err != nil {
		t.Fatalf("AddAll failed: %v", err)
	}
	patch, err := runner.DiffCachedBinary(ctx, wtDir, baseCommit)
	if err != nil {
		t.Fatalf("DiffCachedBinary failed: %v", err)
	}

	// 在主工作区做出冲突修改并提交
	if err := os.WriteFile(filePath, []byte("line 1\nmain change line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write main filePath: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-am", "main concurrent change"); err != nil {
		t.Fatalf("main commit: %v", err)
	}

	// 此时应用补丁，应触发冲突标记且 err 为 nil
	res, err := runner.ApplyPatch3Way(ctx, dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch3Way returned unexpected error: %v", err)
	}
	if !res.Conflicted {
		t.Fatalf("expected Conflicted=true, got %+v", res)
	}

	conflicts, err := ScanConflictMarkers(dir, []string{"shared.txt"})
	if err != nil {
		t.Fatalf("ScanConflictMarkers failed: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != "shared.txt" {
		t.Fatalf("expected [shared.txt] conflicted, got %v", conflicts)
	}
}

// 冲突合并只写工作区：真实索引不留下未合并条目，也不暂存任何内容。
func TestRunner_ApplyPatch3Way_KeepsRealIndexClean(t *testing.T) {
	t.Parallel()
	runner, dir, patch := setupConflictingApply(t)

	res, err := runner.ApplyPatch3Way(context.Background(), dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch3Way returned unexpected error: %v", err)
	}
	if !res.Conflicted {
		t.Fatalf("expected Conflicted=true, got %+v", res)
	}

	out, err := runner.run(context.Background(), dir, nil, "ls-files", "-u")
	if err != nil {
		t.Fatalf("git ls-files -u failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected no unmerged index entries, got: %s", string(out))
	}

	out, err = runner.run(context.Background(), dir, nil, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("git diff --cached failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected nothing staged in the real index, got: %s", string(out))
	}
}

// 首个补丁留下冲突标记后，后续针对同一文件的补丁仍能走三方合并，而不是因索引缺条目硬失败。
func TestRunner_ApplyPatch3Way_SecondPatchAfterConflict(t *testing.T) {
	t.Parallel()
	runner, dir, patch := setupConflictingApply(t)
	ctx := context.Background()

	if _, err := runner.ApplyPatch3Way(ctx, dir, patch); err != nil {
		t.Fatalf("first ApplyPatch3Way failed: %v", err)
	}

	// 第二个 head 基于同一 base 修改同一文件
	baseCommit, err := runner.RevParse(ctx, dir, "HEAD~1")
	if err != nil {
		t.Fatalf("rev-parse failed: %v", err)
	}
	wtDir := filepath.Join(t.TempDir(), "wt-second")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "wt-branch-second", baseCommit); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "shared.txt"), []byte("line 1\nsecond head line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write second worktree file: %v", err)
	}
	if err := runner.AddAll(ctx, wtDir); err != nil {
		t.Fatalf("AddAll failed: %v", err)
	}
	secondPatch, err := runner.DiffCachedBinary(ctx, wtDir, baseCommit)
	if err != nil {
		t.Fatalf("DiffCachedBinary failed: %v", err)
	}

	if _, err := runner.ApplyPatch3Way(ctx, dir, secondPatch); err != nil {
		t.Fatalf("second ApplyPatch3Way should not hard-fail, got: %v", err)
	}
}

// 非 ASCII 文件名经 -z 输出原始路径，冲突扫描能命中对应文件。
func TestRunner_DiffCachedNames_NonASCIIPath(t *testing.T) {
	t.Parallel()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	name := "中文文件.txt"
	fullPath := filepath.Join(dir, name)
	if err := os.WriteFile(fullPath, []byte("line 1\nline 2\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "--", name); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "add unicode file"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	baseCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse failed: %v", err)
	}

	if err := os.WriteFile(fullPath, []byte("line 1\n<<<<<<< HEAD\nlocal\n=======\nremote\n>>>>>>> branch\n"), 0644); err != nil {
		t.Fatalf("write conflict markers: %v", err)
	}
	if err := runner.AddAll(ctx, dir); err != nil {
		t.Fatalf("AddAll failed: %v", err)
	}

	names, err := runner.DiffCachedNames(ctx, dir, baseCommit)
	if err != nil {
		t.Fatalf("DiffCachedNames failed: %v", err)
	}
	if len(names) != 1 || names[0] != name {
		t.Fatalf("expected [%s], got %v", name, names)
	}

	conflicts, err := ScanConflictMarkers(dir, names)
	if err != nil {
		t.Fatalf("ScanConflictMarkers failed: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != name {
		t.Fatalf("expected [%s] conflicted, got %v", name, conflicts)
	}
}

// setupConflictingApply 构造“主工作区已前进、补丁基于旧 base”的冲突场景，
// 返回 runner、主工作区目录与待应用的补丁。
func setupConflictingApply(t *testing.T) (*Runner, string, []byte) {
	t.Helper()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	filePath := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(filePath, []byte("line 1\nbase line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "shared.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	baseCommit, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse failed: %v", err)
	}

	wtDir := filepath.Join(t.TempDir(), "wt-conflict")
	if err := runner.WorktreeAdd(ctx, dir, wtDir, "wt-branch-conflict", baseCommit); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "shared.txt"), []byte("line 1\nhead change line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write wtFile: %v", err)
	}
	if err := runner.AddAll(ctx, wtDir); err != nil {
		t.Fatalf("AddAll failed: %v", err)
	}
	patch, err := runner.DiffCachedBinary(ctx, wtDir, baseCommit)
	if err != nil {
		t.Fatalf("DiffCachedBinary failed: %v", err)
	}

	if err := os.WriteFile(filePath, []byte("line 1\nmain change line 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write main filePath: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-am", "main concurrent change"); err != nil {
		t.Fatalf("main commit: %v", err)
	}

	return runner, dir, patch
}

func TestScanConflictMarkers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fileA := filepath.Join(dir, "z_conflict.txt")
	contentA := "top\n<<<<<<< HEAD\nlocal\n=======\nremote\n>>>>>>> branch\nbottom\n"
	if err := os.WriteFile(fileA, []byte(contentA), 0644); err != nil {
		t.Fatalf("write fileA: %v", err)
	}

	fileB := filepath.Join(dir, "a_conflict.txt")
	contentB := "<<<<<<< HEAD\none\n=======\ntwo\n>>>>>>> 123456\n"
	if err := os.WriteFile(fileB, []byte(contentB), 0644); err != nil {
		t.Fatalf("write fileB: %v", err)
	}

	fileClean := filepath.Join(dir, "clean.txt")
	contentClean := "just ======= equals line\n"
	if err := os.WriteFile(fileClean, []byte(contentClean), 0644); err != nil {
		t.Fatalf("write fileClean: %v", err)
	}

	// 排序、去重、跳过不存在文件
	paths := []string{
		"nonexistent.txt",
		"z_conflict.txt",
		"clean.txt",
		"a_conflict.txt",
		"z_conflict.txt",
	}

	conflicts, err := ScanConflictMarkers(dir, paths)
	if err != nil {
		t.Fatalf("ScanConflictMarkers error: %v", err)
	}

	expected := []string{"a_conflict.txt", "z_conflict.txt"}
	if len(conflicts) != len(expected) {
		t.Fatalf("expected %d conflicts, got %d: %v", len(expected), len(conflicts), conflicts)
	}
	for i := range expected {
		if conflicts[i] != expected[i] {
			t.Errorf("conflict[%d] mismatch: got %s, want %s", i, conflicts[i], expected[i])
		}
	}
}
