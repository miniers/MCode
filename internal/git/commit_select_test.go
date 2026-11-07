package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runGitOutput 在测试仓库里执行一条 git 命令并返回标准输出。
func runGitOutput(t *testing.T, runner *Runner, dir string, args ...string) string {
	t.Helper()
	out, err := runner.run(context.Background(), dir, nil, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// headFiles 返回 HEAD 树里的全部文件路径。
func headFiles(t *testing.T, runner *Runner, dir string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	for _, line := range strings.Split(runGitOutput(t, runner, dir, "ls-tree", "-r", "--name-only", "HEAD"), "\n") {
		if line != "" {
			files[line] = true
		}
	}
	return files
}

// ChangedFiles 要给出单字符状态，重命名带来源路径，带空格的路径保持原样。
func TestRunner_ChangedFiles(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	spaced := filepath.Join(dir, "带 空格.txt")
	if err := os.WriteFile(spaced, []byte("新文件\n"), 0644); err != nil {
		t.Fatalf("write 带 空格.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Changed\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runGitOutput(t, runner, dir, "mv", "README.md", "RENAMED.md")

	files, err := runner.ChangedFiles(ctx, dir)
	if err != nil {
		t.Fatalf("ChangedFiles failed: %v", err)
	}

	byPath := map[string]ChangedFile{}
	for _, file := range files {
		byPath[file.Path] = file
	}
	if got := byPath["带 空格.txt"]; got.Status != "?" || got.OrigPath != "" {
		t.Errorf("带空格的新文件解析错误: %+v", got)
	}
	if got := byPath["RENAMED.md"]; got.Status != "R" || got.OrigPath != "README.md" {
		t.Errorf("重命名解析错误: %+v", got)
	}
}

// 只提交所选路径：没选中的改动留在工作区，且不会被带进这次提交。
func TestRunner_CommitSelectedOnlyCommitsChosenPaths(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# 未选中的改动\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chosen.txt"), []byte("选中的文件\n"), 0644); err != nil {
		t.Fatalf("write chosen.txt: %v", err)
	}
	// 先把 README.md 暂存，确认 CommitSelected 会复位索引、不会顺带提交它。
	runGitOutput(t, runner, dir, "add", "README.md")

	if err := runner.CommitSelected(ctx, dir, []string{"chosen.txt"}, "只提交 chosen.txt"); err != nil {
		t.Fatalf("CommitSelected failed: %v", err)
	}

	files := strings.Split(strings.TrimSpace(runGitOutput(t, runner, dir, "show", "--name-only", "--format=", "HEAD")), "\n")
	if len(files) != 1 || files[0] != "chosen.txt" {
		t.Errorf("这次提交只应有 chosen.txt，实际是 %v", files)
	}
	content, err := os.ReadFile(readmePath)
	if err != nil || string(content) != "# 未选中的改动\n" {
		t.Fatalf("工作区内容被改动: %q, %v", content, err)
	}
	status := runGitOutput(t, runner, dir, "status", "--porcelain=v1")
	if !strings.Contains(status, " M README.md") {
		t.Errorf("未选中的改动应留在工作区: %q", status)
	}
}

// 重命名要同时提交新旧路径，提交后工作区干净。
func TestRunner_CommitSelectedRename(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	runGitOutput(t, runner, dir, "mv", "README.md", "RENAMED.md")

	if err := runner.CommitSelected(ctx, dir, []string{"README.md", "RENAMED.md"}, "重命名 README"); err != nil {
		t.Fatalf("CommitSelected failed: %v", err)
	}

	files := headFiles(t, runner, dir)
	if files["README.md"] || !files["RENAMED.md"] {
		t.Fatalf("重命名结果不对: %v", files)
	}
	if status := strings.TrimSpace(runGitOutput(t, runner, dir, "status", "--porcelain=v1")); status != "" {
		t.Errorf("提交后工作区应干净: %q", status)
	}
}

// 空路径或空提交信息直接报错，不动仓库。
func TestRunner_CommitSelectedRejectsEmptyInput(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	if err := runner.CommitSelected(ctx, dir, nil, "空路径"); err == nil {
		t.Error("空 paths 应返回错误")
	}
	if err := runner.CommitSelected(ctx, dir, []string{" "}, "空路径"); err == nil {
		t.Error("只有空白的 paths 应返回错误")
	}
	if err := runner.CommitSelected(ctx, dir, []string{"README.md"}, "   "); err == nil {
		t.Error("空提交信息应返回错误")
	}
}

// 空仓库（还没有任何提交）也能用 CommitSelected 完成首次提交。
func TestRunner_CommitSelectedFirstCommitInEmptyRepo(t *testing.T) {
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	if runner.hasCommits(ctx, dir) {
		t.Fatal("测试仓库不应有提交")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("首个文件\n"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	// 另有一个已暂存但未选中的文件，它不应被首次提交带走。
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("已暂存\n"), 0644); err != nil {
		t.Fatalf("write staged.txt: %v", err)
	}
	runGitOutput(t, runner, dir, "add", "staged.txt")

	if err := runner.CommitSelected(ctx, dir, []string{"a.txt"}, "首次提交"); err != nil {
		t.Fatalf("CommitSelected failed: %v", err)
	}
	if !runner.hasCommits(ctx, dir) {
		t.Fatal("首次提交后 HEAD 应有效")
	}
	files := headFiles(t, runner, dir)
	if !files["a.txt"] || files["staged.txt"] {
		t.Errorf("首次提交应只包含 a.txt: %v", files)
	}
}
