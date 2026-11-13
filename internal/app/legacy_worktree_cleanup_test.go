package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/store"
	"mcode/internal/testenv"
)

// newLegacyCleanupRepo 建一个带一次提交的临时仓库，并把它用的 git runner 一起返回。
func newLegacyCleanupRepo(t *testing.T) (*git.Runner, string) {
	t.Helper()
	testenv.TempHome(t)
	t.Setenv("GIT_AUTHOR_NAME", "test-user")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@localhost")
	t.Setenv("GIT_COMMITTER_NAME", "test-user")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@localhost")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	mustGit(t, root, "init", root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# legacy cleanup\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	mustGit(t, root, "add", "README.md")
	mustGit(t, root, "commit", "-m", "initial commit")
	return git.NewRunner("", 4), root
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// useLegacyCleanupPurgeRoots 把 hydra 目录注册为受管 worktree 基目录。
func useLegacyCleanupPurgeRoots(t *testing.T, dirs ...string) {
	t.Helper()
	git.SetWorktreePurgeRoots(dirs...)
	t.Cleanup(func() { git.SetWorktreePurgeRoots() })
}

func newLegacyCleanupWorktree(t *testing.T, runner *git.Runner, root, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := runner.WorktreeAdd(context.Background(), root, path, name, ""); err != nil {
		t.Fatalf("WorktreeAdd %s: %v", name, err)
	}
	return path
}

func newLegacyCleanupStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "legacy-cleanup.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func noInUse(string) bool { return false }

func TestCleanupLegacyHydraWorktrees(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "hydra-worktrees")
	useLegacyCleanupPurgeRoots(t, hydraDir)
	ctx := context.Background()

	// a. hydraDir 下已合并且干净：删除。
	cleanPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-clean")

	// b. 已合并但有未提交改动 / 未跟踪文件：保留。
	untrackedPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-untracked")
	if err := os.WriteFile(filepath.Join(untrackedPath, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}
	modifiedPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-modified")
	if err := os.WriteFile(filepath.Join(modifiedPath, "README.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatalf("write modified: %v", err)
	}

	// c. 未合并：保留目录与分支。
	unmergedPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-unmerged")
	if err := os.WriteFile(filepath.Join(unmergedPath, "feature.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	mustGit(t, unmergedPath, "add", "feature.txt")
	mustGit(t, unmergedPath, "commit", "-m", "feature work")

	// d. hydraDir 之外已合并且干净：保留。
	outsidePath := newLegacyCleanupWorktree(t, runner, root, t.TempDir(), "feat-outside")

	// e. 在使用中：保留。
	inUsePath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-in-use")
	inUse := func(path string) bool { return legacyResolvedPath(path) == legacyResolvedPath(inUsePath) }

	removed, kept, err := cleanupLegacyHydraWorktrees(ctx, runner, hydraDir, []string{root}, inUse)
	if err != nil {
		t.Fatalf("cleanupLegacyHydraWorktrees: %v", err)
	}
	// 共 6 个副本：1 个可删；hydraDir 之下 4 个应保留（未跟踪、已修改、未合入、使用中）；
	// hydraDir 之外的副本既不删除也不计入 kept。
	if removed != 1 || kept != 4 {
		t.Fatalf("removed/kept = %d/%d，期望 1/4（目录外的不计入 kept）", removed, kept)
	}
	if _, err := os.Stat(cleanPath); !os.IsNotExist(err) {
		t.Fatalf("已合并且干净的副本应被删除: %v", err)
	}
	for _, p := range []string{untrackedPath, modifiedPath, unmergedPath, outsidePath, inUsePath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("应保留的副本 %s 不存在: %v", p, err)
		}
	}
	mustGit(t, root, "rev-parse", "--verify", "refs/heads/feat-unmerged")
}

func TestRunLegacyHydraCleanupOnceMarksDoneAndSkipsSecondScan(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "hydra-worktrees")
	useLegacyCleanupPurgeRoots(t, hydraDir)
	ctx := context.Background()
	st := newLegacyCleanupStore(t)

	firstPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-first")
	roots := func(context.Context) ([]string, error) { return []string{root}, nil }
	if err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse); err != nil {
		t.Fatalf("runLegacyHydraCleanupOnce: %v", err)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("首次运行的已合并副本应被删除: %v", err)
	}
	if _, ok, err := st.GetSetting(ctx, legacyHydraCleanupSettingKey); err != nil || !ok {
		t.Fatalf("应写入完成标记: ok=%v err=%v", ok, err)
	}

	// 第二次运行不应再扫描，新加的合格副本必须还在。
	secondPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-second")
	if err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse); err != nil {
		t.Fatalf("第二次 runLegacyHydraCleanupOnce: %v", err)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("第二次运行不应删除任何副本: %v", err)
	}
}

func TestRunLegacyHydraCleanupOnceScanFailureKeepsMarkerUnset(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "hydra-worktrees")
	useLegacyCleanupPurgeRoots(t, hydraDir)
	ctx := context.Background()
	st := newLegacyCleanupStore(t)
	newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-scan-fail")

	missingRoot := filepath.Join(t.TempDir(), "not-a-repo")
	roots := func(context.Context) ([]string, error) { return []string{root, missingRoot}, nil }
	err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse)
	if err == nil {
		t.Fatal("扫描失败时应返回错误，让本次不写完成标记")
	}
	if _, ok, getErr := st.GetSetting(ctx, legacyHydraCleanupSettingKey); getErr != nil || ok {
		t.Fatalf("扫描失败不应写入标记: ok=%v err=%v", ok, getErr)
	}
}

func TestRunLegacyHydraCleanupOnceMissingDirMarksDone(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "no-such-hydra")
	ctx := context.Background()
	st := newLegacyCleanupStore(t)

	roots := func(context.Context) ([]string, error) { return []string{root}, nil }
	if err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse); err != nil {
		t.Fatalf("hydraDir 不存在时不应报错: %v", err)
	}
	if _, ok, err := st.GetSetting(ctx, legacyHydraCleanupSettingKey); err != nil || !ok {
		t.Fatalf("hydraDir 不存在时应写入完成标记: ok=%v err=%v", ok, err)
	}
}

// legacyCleanupProjectID 是测试会话使用的项目 ID；threads.project_id 有外键约束，必须先建项目。
const legacyCleanupProjectID = "p1"

// upsertLegacyCleanupProject 确保会话依赖的项目存在。UpsertProject 是幂等的，
// 同一次测试里多次调用不会失败。
func upsertLegacyCleanupProject(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	p := store.Project{
		ID:        legacyCleanupProjectID,
		Name:      "p1",
		Path:      t.TempDir(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.UpsertProject(ctx, p); err != nil {
		t.Fatalf("UpsertProject %s: %v", p.ID, err)
	}
}

// createLegacyCleanupThread 建一个绑定指定 worktree 路径的会话，条件满足时可归档。
func createLegacyCleanupThread(t *testing.T, st *store.Store, id, worktreePath string, archived bool) {
	t.Helper()
	upsertLegacyCleanupProject(t, st)
	ctx := context.Background()
	now := time.Now().UTC()
	th := store.Thread{
		ID:           id,
		ProjectID:    legacyCleanupProjectID,
		Title:        id,
		Provider:     "test",
		CreatedAt:    now,
		UpdatedAt:    now,
		WorktreePath: worktreePath,
		Archived:     archived,
	}
	if err := st.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread %s: %v", id, err)
	}
}

// 会话（含已归档）绑定的副本即使 inUse 返回 false 也必须保留；
// 会话工作目录在副本内的子目录时同样保留；没有会话引用的副本照旧删除。
func TestRunLegacyHydraCleanupOnceKeepsThreadWorktreePaths(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "hydra-worktrees")
	useLegacyCleanupPurgeRoots(t, hydraDir)
	ctx := context.Background()
	st := newLegacyCleanupStore(t)

	activePath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-thread-active")
	archivedPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-thread-archived")
	subdirPath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-thread-subdir")
	// 空目录不会被 git 计入改动，副本本身仍是「已合并且干净」。
	if err := os.MkdirAll(filepath.Join(subdirPath, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	// 没有任何会话引用的副本，用于确认保护没有过度扩散。
	freePath := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-thread-free")

	createLegacyCleanupThread(t, st, "t-active", activePath, false)
	createLegacyCleanupThread(t, st, "t-archived", archivedPath, true)
	createLegacyCleanupThread(t, st, "t-subdir", filepath.Join(subdirPath, "sub"), false)

	roots := func(context.Context) ([]string, error) { return []string{root}, nil }
	if err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse); err != nil {
		t.Fatalf("runLegacyHydraCleanupOnce: %v", err)
	}
	for _, p := range []string{activePath, archivedPath, subdirPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("被会话引用的副本 %s 不应被删除: %v", p, err)
		}
	}
	if _, err := os.Stat(freePath); !os.IsNotExist(err) {
		t.Fatalf("无会话引用的已合并副本应被删除: %v", err)
	}
}

// hydraDir 路径存在但不是目录时返回错误，且不写完成标记。
func TestRunLegacyHydraCleanupOncePathNotDir(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "hydra-worktrees")
	if err := os.WriteFile(hydraDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write hydraDir file: %v", err)
	}
	ctx := context.Background()
	st := newLegacyCleanupStore(t)

	roots := func(context.Context) ([]string, error) { return []string{root}, nil }
	if err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse); err == nil {
		t.Fatal("hydraDir 不是目录时应返回错误")
	}
	if _, ok, err := st.GetSetting(ctx, legacyHydraCleanupSettingKey); err != nil || ok {
		t.Fatalf("hydraDir 不是目录时不应写入标记: ok=%v err=%v", ok, err)
	}
}

// 仓库根目录列表为空（项目列表尚未加载）时不扫描、不写标记，返回 nil。
func TestRunLegacyHydraCleanupOnceEmptyRootsKeepsMarkerUnset(t *testing.T) {
	runner, root := newLegacyCleanupRepo(t)
	hydraDir := filepath.Join(t.TempDir(), "hydra-worktrees")
	useLegacyCleanupPurgeRoots(t, hydraDir)
	ctx := context.Background()
	st := newLegacyCleanupStore(t)

	path := newLegacyCleanupWorktree(t, runner, root, hydraDir, "feat-empty-roots")
	roots := func(context.Context) ([]string, error) { return nil, nil }
	if err := runLegacyHydraCleanupOnce(ctx, st, runner, hydraDir, roots, noInUse); err != nil {
		t.Fatalf("roots 为空时不应报错: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("roots 为空时不应删除任何副本: %v", err)
	}
	if _, ok, err := st.GetSetting(ctx, legacyHydraCleanupSettingKey); err != nil || ok {
		t.Fatalf("roots 为空时不应写入标记: ok=%v err=%v", ok, err)
	}
}
