package git

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRepoLockSameDirSerializes(t *testing.T) {
	dir := t.TempDir()

	unlock := lockRepo(dir)
	done := false
	defer func() {
		if !done {
			unlock()
		}
	}()

	acquired := make(chan struct{})
	go func() {
		defer lockRepo(dir)()
		close(acquired)
	}()

	// 持锁期间，同一目录的第二把锁必须排队
	select {
	case <-acquired:
		t.Fatal("同一目录的锁未互斥：第二个持有者提前获取成功")
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	done = true
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("释放锁后，同一目录的等待者未获锁")
	}
}

func TestRepoLockDifferentDirsParallel(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	unlockA := lockRepo(dirA)
	defer unlockA()

	acquired := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer lockRepo(dirB)()
		close(acquired)
	}()

	// 不同目录不应互相阻塞
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("不同目录的锁互相阻塞了")
	}
	wg.Wait()
}

func TestCanonicalRepoDirNormalizes(t *testing.T) {
	base := t.TempDir()
	cleaned := canonicalRepoDir(filepath.Join(base, "repo", "..", "repo2"))
	if want := filepath.Join(base, "repo2"); cleaned != want {
		t.Fatalf("canonicalRepoDir 未清理路径: got %q, want %q", cleaned, want)
	}
	if !filepath.IsAbs(cleaned) {
		t.Fatalf("canonicalRepoDir 应返回绝对路径: %q", cleaned)
	}
	if canonicalRepoDir(base) != canonicalRepoDir(filepath.Join(base, ".")) {
		t.Fatal("同一目录的不同写法应得到相同的规范化结果")
	}
}

func TestRepoLockNormalizesAbsolutePath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	// 相对路径 "." 与绝对路径应落到同一把锁上
	unlock := lockRepo(".")
	done := false
	defer func() {
		if !done {
			unlock()
		}
	}()

	acquired := make(chan struct{})
	go func() {
		defer lockRepo(cwd)()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("绝对路径与相对路径未映射到同一把锁")
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	done = true
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("规范化后同一目录的等待者未获锁")
	}
}

// makeRepo 手工构造一个带 .git 目录的主仓库，返回仓库目录与 .git 路径。
func makeRepo(t *testing.T) (string, string) {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return repoDir, gitDir
}

// makeWorktree 手工构造一个 worktree 目录：.git 文件指向主仓库的 .git/worktrees/wt1，
// 并在该 gitdir 下写入 commondir=../..（与真实 git worktree 的结构一致）。
func makeWorktree(t *testing.T, repoDir, gitDir string) string {
	t.Helper()
	wtDir := filepath.Join(t.TempDir(), "wt1")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	wtGitDir := filepath.Join(gitDir, "worktrees", "wt1")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}
	content := "gitdir: " + wtGitDir + "\n"
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}
	return wtDir
}

func TestRepoLockKeyMainRepoAndSubdir(t *testing.T) {
	repoDir, gitDir := makeRepo(t)
	sub := filepath.Join(repoDir, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	if got, want := repoLockKey(repoDir), gitDir; got != want {
		t.Fatalf("主仓库键 = %q, want %q", got, want)
	}
	if got := repoLockKey(sub); got != repoLockKey(repoDir) {
		t.Fatalf("子目录键 = %q, want 与主仓库相同 %q", got, repoLockKey(repoDir))
	}
}

func TestRepoLockKeyWorktreeSharesMainRepo(t *testing.T) {
	repoDir, gitDir := makeRepo(t)
	wtDir := makeWorktree(t, repoDir, gitDir)

	if got := repoLockKey(wtDir); got != repoLockKey(repoDir) {
		t.Fatalf("worktree 键 = %q, want 与主仓库相同 %q", got, repoLockKey(repoDir))
	}
	if got, want := repoLockKey(wtDir), gitDir; got != want {
		t.Fatalf("worktree 键 = %q, want commondir %q", got, want)
	}
}

func TestRepoLockKeyDifferentRepos(t *testing.T) {
	repoA, _ := makeRepo(t)
	repoB, _ := makeRepo(t)

	if repoLockKey(repoA) == repoLockKey(repoB) {
		t.Fatalf("两个互不相关的仓库不应共用锁键: %q", repoLockKey(repoA))
	}
}

func TestRepoLockKeyWithoutGitFallsBack(t *testing.T) {
	dir := t.TempDir()
	if got, want := repoLockKey(dir), canonicalRepoDir(dir); got != want {
		t.Fatalf("没有 .git 的目录键 = %q, want 规范化路径 %q", got, want)
	}
}

func TestRepoLockMainRepoAndWorktreeSerialize(t *testing.T) {
	repoDir, gitDir := makeRepo(t)
	wtDir := makeWorktree(t, repoDir, gitDir)

	unlock := lockRepo(repoDir)
	done := false
	defer func() {
		if !done {
			unlock()
		}
	}()

	acquired := make(chan struct{})
	go func() {
		defer lockRepo(wtDir)()
		close(acquired)
	}()

	// 主工作区持锁期间，其 worktree 的第二把锁必须排队
	select {
	case <-acquired:
		t.Fatal("worktree 与主工作区未共用同一把锁：第二个持有者提前获取成功")
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	done = true
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("释放锁后，worktree 的等待者未获锁")
	}
}

func TestIsRepoWriteCall(t *testing.T) {
	tmpIndexEnv := []string{"GIT_INDEX_FILE=/tmp/mcode-index"}

	cases := []struct {
		name string
		env  []string
		args []string
		want bool
	}{
		{"add", nil, []string{"add", "-A", "--", "."}, true},
		{"commit", nil, []string{"commit", "-m", "x"}, true},
		{"update-ref 不是写集合内的子命令", nil, []string{"update-ref", "refs/x", "hash"}, false},
		{"跳过 -c 成对参数", nil, []string{"-c", "core.fsmonitor=false", "add", "-A"}, true},
		{"跳过 -c 粘连参数", nil, []string{"-ccore.fsmonitor=false", "reset", "--hard"}, true},
		{"读操作不加锁", nil, []string{"status", "--porcelain"}, false},
		{"rev-parse 不加锁", nil, []string{"rev-parse", "HEAD"}, false},
		{"临时 index 不需要加锁", tmpIndexEnv, []string{"add", "-A", "--", "."}, false},
		{"只有 -c 没有子命令", nil, []string{"-c", "core.fsmonitor=false"}, false},
		{"空参数", nil, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRepoWriteCall(tc.env, tc.args); got != tc.want {
				t.Fatalf("isRepoWriteCall(%v, %v) = %v, want %v", tc.env, tc.args, got, tc.want)
			}
		})
	}
}
