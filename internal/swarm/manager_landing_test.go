package swarm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/git"
)

// (a) 主工作区第 1 行有未提交改动，bee 改第 3 行，落地后主工作区同时有两处改动且 Merged 为 true。
func TestManager_Landing_MergeTreeNoConflict(t *testing.T) {
	t.Parallel()
	runner, repoDir := initTestRepo(t)
	ctx := context.Background()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	runGit := func(dir string, args ...string) {
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test-user",
			"GIT_AUTHOR_EMAIL=test@localhost",
			"GIT_COMMITTER_NAME=test-user",
			"GIT_COMMITTER_EMAIL=test@localhost",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
	}

	// 初始提交 4 行文本文件
	initialContent := "line 1: original\nline 2: original\nline 3: original\nline 4: original\n"
	linesFile := filepath.Join(repoDir, "lines.txt")
	if err := os.WriteFile(linesFile, []byte(initialContent), 0644); err != nil {
		t.Fatalf("write lines.txt: %v", err)
	}
	runGit(repoDir, "add", "lines.txt")
	runGit(repoDir, "commit", "-m", "add lines.txt")

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-land-a"
	spec := BeeSpec{
		Name:   "Bee-A",
		Task:   "Modify line 3",
		Prompt: "Modify line 3",
	}
	bee, err := mgr.startSingleBee(ctx, leadID, "proj-1", repoDir, spec, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}

	// 1. 主工作区第 1 行发生未提交改动
	leadContent := "line 1: modified by lead\nline 2: original\nline 3: original\nline 4: original\n"
	if err := os.WriteFile(linesFile, []byte(leadContent), 0644); err != nil {
		t.Fatalf("write modified lines.txt in repoDir: %v", err)
	}

	// 2. bee 在其 worktree 中修改第 3 行
	beeLinesFile := filepath.Join(bee.WorktreePath, "lines.txt")
	beeContent := "line 1: original\nline 2: original\nline 3: modified by bee\nline 4: original\n"
	if err := os.WriteFile(beeLinesFile, []byte(beeContent), 0644); err != nil {
		t.Fatalf("write modified lines.txt in bee worktree: %v", err)
	}

	// 登记已合入 bee 避免 dirtyMergePaths 阻塞本测试
	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		bees: []Bee{
			{
				Index:        99,
				Status:       StatusCompleted,
				Merged:       true,
				ChangedFiles: []string{"lines.txt"},
			},
			bee,
		},
	}
	mgr.mu.Unlock()

	// 执行落地合并
	ok := mgr.mergeBee(ctx, leadID, repoDir, &bee)
	if !ok {
		t.Fatalf("mergeBee expected true, got false; waitSince=%v err=%s", bee.MergeWaitSince, bee.MergeError)
	}
	if !bee.Merged {
		t.Fatalf("expected bee.Merged == true, got false; error: %s", bee.MergeError)
	}
	if len(bee.ConflictFiles) > 0 {
		t.Fatalf("expected no conflicts, got %v", bee.ConflictFiles)
	}
	if bee.LandedPatchPath == "" {
		t.Errorf("expected LandedPatchPath to be set")
	}

	// 验证主工作区同时包含两处改动
	finalBytes, err := os.ReadFile(linesFile)
	if err != nil {
		t.Fatalf("read final lines.txt: %v", err)
	}
	finalContent := string(finalBytes)
	expectedFinal := "line 1: modified by lead\nline 2: original\nline 3: modified by bee\nline 4: original\n"
	if finalContent != expectedFinal {
		t.Fatalf("expected merged content:\n%q\ngot:\n%q", expectedFinal, finalContent)
	}
}

// (b) 两边改同一行时，Merged 为 false、ConflictFiles 非空、主工作区文件与落地前完全一致、没有冲突标记。
func TestManager_Landing_MergeTreeConflict(t *testing.T) {
	t.Parallel()
	runner, repoDir := initTestRepo(t)
	ctx := context.Background()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	runGit := func(dir string, args ...string) {
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test-user",
			"GIT_AUTHOR_EMAIL=test@localhost",
			"GIT_COMMITTER_NAME=test-user",
			"GIT_COMMITTER_EMAIL=test@localhost",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
	}

	// 初始提交 3 行文本
	initialContent := "line 1: original\nline 2: original\nline 3: original\n"
	linesFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(linesFile, []byte(initialContent), 0644); err != nil {
		t.Fatalf("write conflict.txt: %v", err)
	}
	runGit(repoDir, "add", "conflict.txt")
	runGit(repoDir, "commit", "-m", "add conflict.txt")

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-land-b"
	spec := BeeSpec{
		Name:   "Bee-B",
		Task:   "Modify line 2",
		Prompt: "Modify line 2",
	}
	bee, err := mgr.startSingleBee(ctx, leadID, "proj-1", repoDir, spec, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}

	// 1. 主工作区在第 2 行产生未提交改动
	leadContent := "line 1: original\nline 2: modified by lead\nline 3: original\n"
	if err := os.WriteFile(linesFile, []byte(leadContent), 0644); err != nil {
		t.Fatalf("write modified conflict.txt in repoDir: %v", err)
	}

	// 2. bee 在第 2 行也做出修改（引发冲突）
	beeLinesFile := filepath.Join(bee.WorktreePath, "conflict.txt")
	beeContent := "line 1: original\nline 2: modified by bee\nline 3: original\n"
	if err := os.WriteFile(beeLinesFile, []byte(beeContent), 0644); err != nil {
		t.Fatalf("write modified conflict.txt in bee worktree: %v", err)
	}

	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		bees:         []Bee{bee},
	}
	mgr.mu.Unlock()

	// 执行落地合并
	ok := mgr.mergeBee(ctx, leadID, repoDir, &bee)
	if !ok {
		t.Fatalf("mergeBee expected true, got false")
	}
	if bee.Merged {
		t.Fatalf("expected bee.Merged == false on conflict, got true")
	}
	if bee.ConflictHandbacks != 1 {
		t.Fatalf("expected bee.ConflictHandbacks == 1 on handback, got %d", bee.ConflictHandbacks)
	}
	if bee.Status != StatusRunning {
		t.Fatalf("expected bee.Status == StatusRunning on handback, got %s", bee.Status)
	}
	// 验证 bee worktree 中写入了冲突标记
	wtBytes, err := os.ReadFile(beeLinesFile)
	if err != nil {
		t.Fatalf("read beeLinesFile: %v", err)
	}
	if !strings.Contains(string(wtBytes), "<<<<<<<") || !strings.Contains(string(wtBytes), ">>>>>>>") {
		t.Fatalf("expected conflict markers in bee worktree, got:\n%s", string(wtBytes))
	}

	// 验证主工作区文件与落地前完全一致，且无冲突标记
	currentBytes, err := os.ReadFile(linesFile)
	if err != nil {
		t.Fatalf("read conflict.txt: %v", err)
	}
	currentContent := string(currentBytes)
	if currentContent != leadContent {
		t.Fatalf("expected content unchanged:\n%q\ngot:\n%q", leadContent, currentContent)
	}
	if strings.Contains(currentContent, "<<<<<<<") || strings.Contains(currentContent, ">>>>>>>") {
		t.Fatalf("found conflict markers in target workdir file")
	}
}

// (d) ConflictHandbacks 已经为 1 时不再交回，退回原行为只保存补丁。
func TestManager_Landing_ConflictHandbacksExceeded_SavesPatch(t *testing.T) {
	t.Parallel()
	runner, repoDir := initTestRepo(t)
	ctx := context.Background()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	runGit := func(dir string, args ...string) {
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test-user",
			"GIT_AUTHOR_EMAIL=test@localhost",
			"GIT_COMMITTER_NAME=test-user",
			"GIT_COMMITTER_EMAIL=test@localhost",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
	}

	// 初始提交 3 行文本
	initialContent := "line 1: original\nline 2: original\nline 3: original\n"
	linesFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(linesFile, []byte(initialContent), 0644); err != nil {
		t.Fatalf("write conflict.txt: %v", err)
	}
	runGit(repoDir, "add", "conflict.txt")
	runGit(repoDir, "commit", "-m", "add conflict.txt")

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-land-exceeded"
	spec := BeeSpec{
		Name:   "Bee-Exceeded",
		Task:   "Modify line 2",
		Prompt: "Modify line 2",
	}
	bee, err := mgr.startSingleBee(ctx, leadID, "proj-1", repoDir, spec, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}

	// 模拟已交回过 1 次
	bee.ConflictHandbacks = 1

	// 主工作区修改第 2 行
	leadContent := "line 1: original\nline 2: modified by lead\nline 3: original\n"
	if err := os.WriteFile(linesFile, []byte(leadContent), 0644); err != nil {
		t.Fatalf("write modified conflict.txt in repoDir: %v", err)
	}

	// bee 在第 2 行也做出修改
	beeLinesFile := filepath.Join(bee.WorktreePath, "conflict.txt")
	beeContent := "line 1: original\nline 2: modified by bee\nline 3: original\n"
	if err := os.WriteFile(beeLinesFile, []byte(beeContent), 0644); err != nil {
		t.Fatalf("write modified conflict.txt in bee worktree: %v", err)
	}

	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		bees:         []Bee{bee},
	}
	mgr.mu.Unlock()

	startCountBefore := driver.nextID

	// 执行落地合并
	ok := mgr.mergeBee(ctx, leadID, repoDir, &bee)
	if !ok {
		t.Fatalf("mergeBee expected true, got false")
	}

	// 不再交回：未启动新 session
	if driver.nextID != startCountBefore {
		t.Fatalf("ConflictHandbacks 为 1 时不应启动新 session，driver.nextID 改变了")
	}

	// 退回原行为：未合入，写冲突文件与 MergeError
	if bee.Merged {
		t.Fatalf("expected bee.Merged == false on conflict, got true")
	}
	if len(bee.ConflictFiles) == 0 {
		t.Fatalf("expected bee.ConflictFiles to be non-empty")
	}
	if !strings.Contains(bee.MergeError, "与主工作区最新内容冲突") {
		t.Fatalf("expected conflict merge error, got: %s", bee.MergeError)
	}
	if !strings.Contains(bee.MergeError, "补丁已保存到") {
		t.Fatalf("expected patch saved path in MergeError, got: %s", bee.MergeError)
	}

	// 验证主工作区文件与落地前完全一致，且无冲突标记
	currentBytes, err := os.ReadFile(linesFile)
	if err != nil {
		t.Fatalf("read conflict.txt: %v", err)
	}
	if string(currentBytes) != leadContent {
		t.Fatalf("expected content unchanged:\n%q\ngot:\n%q", leadContent, string(currentBytes))
	}
	if strings.Contains(string(currentBytes), "<<<<<<<") || strings.Contains(string(currentBytes), ">>>>>>>") {
		t.Fatalf("found conflict markers in target workdir file")
	}
}

// (c) beeWorkDir 是非 git 临时目录、ShadowRoot 为空时，startSingleBee 返回 SharedNoGit 为 true、WorktreePath 为空。
func TestManager_Landing_NonGitSharedFallback(t *testing.T) {
	t.Parallel()
	nonGitDir := t.TempDir()
	ctx := context.Background()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	runner := git.NewRunner(gitBin, 4)
	runner.ShadowRoot = ""

	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-land-c"
	spec := BeeSpec{
		Name:   "Bee-C",
		Task:   "Non-git task",
		Prompt: "Non-git task",
	}

	bee, err := mgr.startSingleBee(ctx, leadID, "proj-c", nonGitDir, spec, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}

	if !bee.SharedNoGit {
		t.Errorf("expected bee.SharedNoGit == true, got false")
	}
	if bee.WorktreePath != "" {
		t.Errorf("expected empty WorktreePath for SharedNoGit, got %q", bee.WorktreePath)
	}
	if bee.ShadowRepo {
		t.Errorf("expected bee.ShadowRepo == false when ShadowRoot is empty, got true")
	}
	if bee.SharedNoGitReason == "" {
		t.Errorf("expected non-empty SharedNoGitReason")
	}
}

// 影子仓库已存在的非 git 目录：bee 应标记 ShadowRepo，否则自动合并流水线会拿普通 runner
// 在该目录执行 git status 并报 not a git repository；真实仓库上 ShadowRepo 仍为 false。
func TestManager_Landing_ShadowRepoBeeOnNonGitDir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	runner := git.NewRunner(gitBin, 4)
	runner.ShadowRoot = t.TempDir()
	nonGitDir := t.TempDir()
	// 预先建好影子仓库，模拟该项目上一个 bee 已经创建过影子仓库
	if _, err := runner.EnsureShadow(ctx, nonGitDir); err != nil {
		t.Fatalf("EnsureShadow failed: %v", err)
	}

	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		WorktreeLocation: func() string {
			return config.WorktreeLocationProject
		},
	})

	leadID := "lead-land-shadow"
	bee, err := mgr.startSingleBee(ctx, leadID, "proj-shadow", nonGitDir, BeeSpec{
		Name:   "Bee-Shadow",
		Task:   "Shadow task",
		Prompt: "Shadow task",
	}, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}
	if !bee.ShadowRepo {
		t.Errorf("expected bee.ShadowRepo == true for shadowed non-git dir, got false")
	}
	if bee.SharedNoGit {
		t.Errorf("expected bee.SharedNoGit == false for shadowed non-git dir, got true")
	}
	if bee.WorktreePath == "" {
		t.Errorf("expected non-empty WorktreePath for shadowed non-git dir")
	}
	if !pathUnderDir(mgr.opts.WorktreeDir, bee.WorktreePath) {
		t.Errorf("影子仓库副本应保持集中存储：%q", bee.WorktreePath)
	}
}

// 同样的影子仓库配置下，真实 git 仓库的 bee 不应被标记 ShadowRepo（走普通 worktree）。
func TestManager_Landing_RealRepoBeeOnShadowRunner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, repoDir := initTestRepo(t)
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	runner := git.NewRunner(gitBin, 4)
	runner.ShadowRoot = t.TempDir()

	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-land-real"
	bee, err := mgr.startSingleBee(ctx, leadID, "proj-real", repoDir, BeeSpec{
		Name:   "Bee-Real",
		Task:   "Real task",
		Prompt: "Real task",
	}, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}
	if bee.ShadowRepo {
		t.Errorf("expected bee.ShadowRepo == false for real repo, got true")
	}
	if bee.SharedNoGit {
		t.Errorf("expected bee.SharedNoGit == false for real repo, got true")
	}
	if bee.WorktreePath == "" {
		t.Errorf("expected non-empty WorktreePath for real repo")
	}
}

// 退回 git apply --3way 落地时发生冲突：补丁涉及的文件（含带未提交改动的冲突文件、
// 已正常改动的其他文件、补丁新增的文件）都要复原到落地前的样子，冲突标记不留在主工作区。
func TestManager_ApplyMerge_ConflictRestoresDirtyFiles(t *testing.T) {
	t.Parallel()
	runner, repoDir := initTestRepo(t)
	ctx := context.Background()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}
	runGit := func(args ...string) string {
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test-user",
			"GIT_AUTHOR_EMAIL=test@localhost",
			"GIT_COMMITTER_NAME=test-user",
			"GIT_COMMITTER_EMAIL=test@localhost",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
		return string(out)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("conflict.txt", "a\nb\nc\n")
	write("other.txt", "x\n")
	runGit("add", "conflict.txt", "other.txt")
	runGit("commit", "-m", "base")

	// 构造 bee 补丁：改 conflict.txt 第 2 行、改 other.txt、新增 new.txt。
	write("conflict.txt", "a\nbee\nc\n")
	write("other.txt", "y\n")
	write("new.txt", "new\n")
	runGit("add", "-A")
	patch := []byte(runGit("diff", "--cached", "--binary"))
	runGit("reset", "-q", "--hard")
	if err := os.Remove(filepath.Join(repoDir, "new.txt")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove new.txt: %v", err)
	}

	// 用户在同一行有未提交改动。
	userContent := "a\nuser\nc\n"
	write("conflict.txt", userContent)

	mgr := NewManager(Options{Git: runner, Driver: newFakeDriver(), WorktreeDir: t.TempDir()})
	h := &Bee{Index: 0, ChangedFiles: []string{"conflict.txt", "other.txt", "new.txt"}}
	mgr.applyMerge(ctx, "lead-apply", repoDir, h, patch)

	if h.Merged {
		t.Fatalf("冲突时不应标记为已合入")
	}
	if !strings.Contains(h.MergeError, "已复原冲突文件") {
		t.Fatalf("MergeError 应说明已复原，实际: %q", h.MergeError)
	}
	assertFile := func(name, want string) {
		got, err := os.ReadFile(filepath.Join(repoDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s 应与落地前一致，期望 %q，实际 %q", name, want, string(got))
		}
	}
	assertFile("conflict.txt", userContent)
	assertFile("other.txt", "x\n")
	if _, err := os.Stat(filepath.Join(repoDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("补丁新增的 new.txt 应被删除, err=%v", err)
	}
	if status := runGit("status", "--porcelain"); strings.TrimSpace(status) != "M conflict.txt" {
		t.Fatalf("主工作区应只剩用户的未提交改动，实际 status: %q", status)
	}
}

// 写入白名单非空时，prepareMerge 只把白名单内的改动放进补丁，
// 白名单外的改动记入 DiscardedPaths 并移出暂存区，且不算合并失败。
func TestManager_PrepareMergeFiltersWritePaths(t *testing.T) {
	t.Parallel()
	runner, repoDir := initTestRepo(t)
	ctx := context.Background()

	planFile := filepath.Join(repoDir, "docs", "plans", "p.md")
	mainFile := filepath.Join(repoDir, "main.go")
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		t.Fatalf("mkdir docs/plans: %v", err)
	}
	if err := os.WriteFile(planFile, []byte("# 计划\n"), 0o644); err != nil {
		t.Fatalf("write p.md: %v", err)
	}
	if err := os.WriteFile(mainFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	runGitCmd := func(args ...string) {
		t.Helper()
		gitBin, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git 可执行文件未找到，跳过测试")
		}
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test-user",
			"GIT_AUTHOR_EMAIL=test@localhost",
			"GIT_COMMITTER_NAME=test-user",
			"GIT_COMMITTER_EMAIL=test@localhost",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
	}
	runGitCmd("add", "-A")
	runGitCmd("commit", "-m", "add docs/plans/p.md and main.go")

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-write-paths"
	spec := BeeSpec{
		Name:       "Bee-W",
		Task:       "改计划文档",
		Prompt:     "改计划文档",
		WritePaths: []string{"docs/plans/*.md"},
	}
	bee, err := mgr.startSingleBee(ctx, leadID, "proj-1", repoDir, spec, 0, leadID)
	if err != nil {
		t.Fatalf("startSingleBee failed: %v", err)
	}
	if !reflect.DeepEqual(bee.WritePaths, []string{"docs/plans/*.md"}) {
		t.Fatalf("bee 应继承档位的写入白名单，得到 %v", bee.WritePaths)
	}

	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "docs", "plans", "p.md"), []byte("# 计划 v2\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 中的 p.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 中的 main.go: %v", err)
	}

	patch, ok := mgr.prepareMerge(ctx, &bee)
	if !ok {
		t.Fatalf("prepareMerge 应成功，得到 ok=false, MergeError=%q", bee.MergeError)
	}
	if bee.MergeError != "" {
		t.Fatalf("丢弃白名单外的改动不该算合并失败，MergeError=%q", bee.MergeError)
	}
	if !reflect.DeepEqual(bee.ChangedFiles, []string{"docs/plans/p.md"}) {
		t.Fatalf("ChangedFiles = %v, 想要只含 docs/plans/p.md", bee.ChangedFiles)
	}
	if !reflect.DeepEqual(bee.DiscardedPaths, []string{"main.go"}) {
		t.Fatalf("DiscardedPaths = %v, 想要 [main.go]", bee.DiscardedPaths)
	}
	text := string(patch)
	if !strings.Contains(text, "docs/plans/p.md") {
		t.Fatalf("补丁应包含白名单内的改动，实际:\n%s", text)
	}
	if strings.Contains(text, "main.go") {
		t.Fatalf("补丁不该包含白名单外的改动，实际:\n%s", text)
	}

	// 白名单外的改动已移出暂存区，补丁与暂存区都只剩白名单内的文件。
	staged, err := runner.DiffCachedNames(ctx, bee.WorktreePath, bee.BaseCommit)
	if err != nil {
		t.Fatalf("读取暂存区文件失败: %v", err)
	}
	if !reflect.DeepEqual(staged, []string{"docs/plans/p.md"}) {
		t.Fatalf("暂存区应只剩 docs/plans/p.md，得到 %v", staged)
	}
}
