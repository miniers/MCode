package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// normalizePath 解析符号链接（例如 macOS 下 /var -> /private/var），方便路径比对
func normalizePath(t *testing.T, path string) string {
	t.Helper()
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return realPath
}

func setupTestRepo(t *testing.T) (*Runner, string) {
	t.Helper()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Worktree Test Repo\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "add", "README.md"); err != nil {
		t.Fatalf("git add README.md: %v", err)
	}
	if _, err := runner.run(ctx, dir, nil, "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	return runner, dir
}

func TestWorktree_AddAndRemove(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	// 1. 相对路径报错
	relPath := "relative-wt"
	if err := runner.WorktreeAdd(ctx, dir, relPath, "feat-rel", ""); err == nil {
		t.Fatalf("expected error for relative path %q, got nil", relPath)
	}

	// 2. WorktreeAdd 新分支
	wtDir := filepath.Join(t.TempDir(), "wt-feat")
	branchName := "feature-test"
	if err := runner.WorktreeAdd(ctx, dir, wtDir, branchName, ""); err != nil {
		t.Fatalf("WorktreeAdd failed: %v", err)
	}

	// 验证目录存在
	stat, err := os.Stat(wtDir)
	if err != nil || !stat.IsDir() {
		t.Fatalf("expected worktree directory %s to exist as dir, err: %v", wtDir, err)
	}

	// 验证 WorktreeList 含该路径与分支
	list, err := runner.WorktreeList(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeList failed: %v", err)
	}

	normWtDir := normalizePath(t, wtDir)
	var found *Worktree
	for i := range list {
		if normalizePath(t, list[i].Path) == normWtDir {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected worktree %s in list, got %+v", wtDir, list)
	}
	if found.Branch != branchName {
		t.Errorf("expected branch %s, got %s", branchName, found.Branch)
	}

	// 3. 在 worktree 内写未提交文件后非 force 删除应失败、force 删除成功，删除后 List 不再包含
	dirtyFile := filepath.Join(wtDir, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted change\n"), 0644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	// 非 force 删除应失败
	if err := runner.WorktreeRemove(ctx, dir, wtDir, false); err == nil {
		t.Fatalf("expected error when removing dirty worktree without force, got nil")
	}

	// force 删除成功
	if err := runner.WorktreeRemove(ctx, dir, wtDir, true); err != nil {
		t.Fatalf("WorktreeRemove with force failed: %v", err)
	}

	// 删除后 List 不再包含
	listAfter, err := runner.WorktreeList(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeList after remove failed: %v", err)
	}
	for _, wt := range listAfter {
		if normalizePath(t, wt.Path) == normWtDir {
			t.Errorf("worktree %s still present in list after removal", wtDir)
		}
	}
}

func TestWorktree_Detached(t *testing.T) {
	runner, dir := setupTestRepo(t)
	ctx := context.Background()

	wtDetached := filepath.Join(t.TempDir(), "wt-detached")
	if err := runner.WorktreeAdd(ctx, dir, wtDetached, "", ""); err != nil {
		t.Fatalf("WorktreeAdd detached failed: %v", err)
	}

	list, err := runner.WorktreeList(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeList failed: %v", err)
	}

	normWt := normalizePath(t, wtDetached)
	var found *Worktree
	for i := range list {
		if normalizePath(t, list[i].Path) == normWt {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected detached worktree %s in list", wtDetached)
	}
	if !found.Detached {
		t.Errorf("expected Detached=true, got %+v", found)
	}
	if found.Branch != "" {
		t.Errorf("expected empty branch for detached worktree, got %q", found.Branch)
	}

	// 清理
	if err := runner.WorktreeRemove(ctx, dir, wtDetached, true); err != nil {
		t.Fatalf("cleanup detached worktree failed: %v", err)
	}
}

func TestParseWorktreeList_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []Worktree
	}{
		{
			name:     "empty input",
			input:    "",
			expected: []Worktree{},
		},
		{
			name:     "quoted path",
			input:    "worktree \"/path/to/\\344\\270\\255\\346\\226\\207\\tline\\nnext\"\nHEAD abc\ndetached\n",
			expected: []Worktree{{Path: "/path/to/中文\tline\nnext", Head: "abc", Detached: true}},
		},
		{
			name:     "nul separated path preserves control characters",
			input:    "worktree /path/to/中文\tline\nnext\r\x00HEAD abc\x00detached\x00\x00",
			expected: []Worktree{{Path: "/path/to/中文\tline\nnext\r", Head: "abc", Detached: true}},
		},
		{
			name: "standard branch worktree",
			input: `worktree /path/to/main
HEAD 0123456789abcdef0123456789abcdef01234567
branch refs/heads/main

`,
			expected: []Worktree{
				{
					Path:   "/path/to/main",
					Head:   "0123456789abcdef0123456789abcdef01234567",
					Branch: "main",
				},
			},
		},
		{
			name: "bare repository worktree",
			input: `worktree /path/to/bare
bare

`,
			expected: []Worktree{
				{
					Path: "/path/to/bare",
					Bare: true,
				},
			},
		},
		{
			name: "detached worktree",
			input: `worktree /path/to/detached
HEAD fedcba9876543210fedcba9876543210fedcba98
detached

`,
			expected: []Worktree{
				{
					Path:     "/path/to/detached",
					Head:     "fedcba9876543210fedcba9876543210fedcba98",
					Detached: true,
				},
			},
		},
		{
			name: "locked with reason and prunable with reason",
			input: `worktree /path/to/locked
HEAD 1111222233334444555566667777888899990000
branch refs/heads/feature-x
locked working on sensitive refactor

worktree /path/to/prunable
HEAD 2222333344445555666677778888999900001111
detached
prunable gitdir file points to non-existent location
`,
			expected: []Worktree{
				{
					Path:   "/path/to/locked",
					Head:   "1111222233334444555566667777888899990000",
					Branch: "feature-x",
					Locked: true,
				},
				{
					Path:     "/path/to/prunable",
					Head:     "2222333344445555666677778888999900001111",
					Detached: true,
					Prunable: true,
				},
			},
		},
		{
			name: "locked and prunable without reason text",
			input: `worktree /path/to/locked-no-reason
HEAD 3333444455556666777788889999000011112222
branch refs/heads/fix
locked

worktree /path/to/prunable-no-reason
HEAD 4444555566667777888899990000111122223333
detached
prunable
`,
			expected: []Worktree{
				{
					Path:   "/path/to/locked-no-reason",
					Head:   "3333444455556666777788889999000011112222",
					Branch: "fix",
					Locked: true,
				},
				{
					Path:     "/path/to/prunable-no-reason",
					Head:     "4444555566667777888899990000111122223333",
					Detached: true,
					Prunable: true,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := ParseWorktreeList([]byte(tc.input))
			if len(actual) != len(tc.expected) {
				t.Fatalf("expected %d worktrees, got %d", len(tc.expected), len(actual))
			}
			for i := range actual {
				if actual[i] != tc.expected[i] {
					t.Errorf("item %d mismatch:\ngot:  %+v\nwant: %+v", i, actual[i], tc.expected[i])
				}
			}
		})
	}
}

func TestWorktreeSpecialPathAndBase(t *testing.T) {
	runner, root := setupTestRepo(t)
	ctx := context.Background()
	base, err := runner.run(ctx, root, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	name := "中文 space\tline\nnext"
	if runtime.GOOS == "windows" {
		name = "中文 space"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := runner.WorktreeAdd(ctx, root, path, "special-path", "HEAD"); err != nil {
		t.Fatal(err)
	}
	list, err := runner.WorktreeList(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, wt := range list {
		if normalizePath(t, wt.Path) == normalizePath(t, path) {
			found = true
			if wt.Head+"\n" != string(base) || wt.Branch != "special-path" {
				t.Fatalf("wrong worktree base/branch: %+v", wt)
			}
		}
	}
	if !found {
		t.Fatalf("special path not preserved: %+v", list)
	}
	if err := runner.WorktreeRemove(ctx, root, "--force", false); err == nil {
		t.Fatal("option-like relative path was accepted")
	}
	if err := runner.WorktreeRemove(ctx, root, path, false); err != nil {
		t.Fatal(err)
	}
}

func TestHasSubmodules(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. 无 .gitmodules
	if HasSubmodules(tmpDir) {
		t.Errorf("expected HasSubmodules to be false for empty directory")
	}

	// 2. 有 .gitmodules 文件
	gitmodulesPath := filepath.Join(tmpDir, ".gitmodules")
	if err := os.WriteFile(gitmodulesPath, []byte("[submodule \"foo\"]\n\tpath = foo\n\turl = https://example.com/foo.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasSubmodules(tmpDir) {
		t.Errorf("expected HasSubmodules to be true when .gitmodules exists")
	}

	// 3. .gitmodules 是目录时应返回 false
	tmpDir2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir2, ".gitmodules"), 0755); err != nil {
		t.Fatal(err)
	}
	if HasSubmodules(tmpDir2) {
		t.Errorf("expected HasSubmodules to be false when .gitmodules is a directory")
	}
}

func TestWorktreeInitSubmodulesOption(t *testing.T) {
	// 测试全局开关与局部选项
	SetInitSubmodules(false)
	if GetInitSubmodules() {
		t.Fatalf("expected globalInitSubmodules to be false")
	}
	SetInitSubmodules(true)
	if !GetInitSubmodules() {
		t.Fatalf("expected globalInitSubmodules to be true")
	}
	SetInitSubmodules(false)

	runner, root := setupTestRepo(t)
	ctx := context.Background()
	wtPath := filepath.Join(t.TempDir(), "wt-no-submodule")
	// 仓库中没有 .gitmodules，即便开启选项也不应报错
	if err := runner.WorktreeAdd(ctx, root, wtPath, "branch-test-submodule", "HEAD", WithInitSubmodules(true)); err != nil {
		t.Fatalf("WorktreeAdd with WithInitSubmodules failed: %v", err)
	}
}
