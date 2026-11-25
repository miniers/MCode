package swarm

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/kanban"
)

const landingBase = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"

// TestMain 在测试二进制被临时链接成 git 替身时走替身分支，否则正常跑测试。
func TestMain(m *testing.M) {
	if cfg, ok := gitWrapperConfigPath(); ok {
		os.Exit(runGitWrapperHelper(cfg))
	}
	home, err := os.MkdirTemp("", "mcode-swarm-test-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		_ = os.RemoveAll(home)
		fmt.Fprintf(os.Stderr, "set HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("USERPROFILE", home); err != nil {
		_ = os.RemoveAll(home)
		fmt.Fprintf(os.Stderr, "set USERPROFILE: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanupTestRepoTemplate()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// gitWrapperConfigPath 判断当前进程是否是 git 替身：替身旁边放着配置文件。
func gitWrapperConfigPath() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	cfg := filepath.Join(filepath.Dir(exe), "git-wrapper.conf")
	if _, err := os.Stat(cfg); err != nil {
		return "", false
	}
	return cfg, true
}

// runGitWrapperHelper 把命令转发给真实 git；首次 update-ref HEAD 时改用配置里的外部提交，
// 用于模拟落地期间 HEAD 被外部移动。
func runGitWrapperHelper(cfg string) int {
	realGit, other, flag := "", "", ""
	if data, err := os.ReadFile(cfg); err == nil {
		fields := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(fields) == 3 {
			realGit, other, flag = fields[0], fields[1], fields[2]
		}
	}
	if realGit == "" {
		if path, err := exec.LookPath("git"); err == nil {
			realGit = path
		}
	}
	if realGit == "" {
		return 127
	}
	args := os.Args[1:]
	if other == "fail-reset" && len(args) >= 4 && args[0] == "reset" && args[1] == "-q" {
		if err := os.Remove(flag); err == nil {
			fmt.Fprintln(os.Stderr, "模拟索引同步失败")
			return 1
		}
	}
	if len(args) >= 6 && args[0] == "update-ref" && args[3] == "HEAD" && other != "" && other != "fail-reset" {
		if err := os.Remove(flag); err == nil {
			// 先替外部把 HEAD 挪走，再让原来的 update-ref 因旧值不匹配而失败。
			if code := runGitCommand(realGit, []string{"update-ref", "HEAD", other, args[5]}); code != 0 {
				return code
			}
		}
	}
	return runGitCommand(realGit, args)
}

func runGitCommand(realGit string, args []string) int {
	cmd := exec.Command(realGit, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// linkOrCopyFile 优先硬链接测试二进制，跨卷时退回复制。
func linkOrCopyFile(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeGitWrapper(t *testing.T, dir, realGit, other, flag string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		wrapper := filepath.Join(dir, "git.exe")
		if err := linkOrCopyFile(os.Args[0], wrapper); err != nil {
			t.Fatalf("创建 git 替身失败: %v", err)
		}
		cfg := filepath.Join(dir, "git-wrapper.conf")
		if err := os.WriteFile(cfg, []byte(realGit+"\n"+other+"\n"+flag+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return wrapper
	}
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
	}
	var body string
	if other == "fail-reset" {
		body = fmt.Sprintf(`#!/bin/sh
set -eu
real_git=%s
flag=%s
if [ "$#" -ge 4 ] && [ "$1" = "reset" ] && [ "$2" = "-q" ] && rm "$flag" 2>/dev/null; then
  printf '模拟索引同步失败\n' >&2
  exit 1
fi
exec "$real_git" "$@"
`, quote(realGit), quote(flag))
	} else {
		body = fmt.Sprintf(`#!/bin/sh
set -eu
real_git=%s
other=%s
flag=%s
if [ "$#" -ge 6 ] && [ "$1" = "update-ref" ] && [ "$4" = "HEAD" ] && rm "$flag" 2>/dev/null; then
  "$real_git" update-ref HEAD "$other" "$6"
fi
exec "$real_git" "$@"
`, quote(realGit), quote(other), quote(flag))
	}
	wrapper := filepath.Join(dir, "git")
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatalf("创建 git 替身失败: %v", err)
	}
	return wrapper
}

func newCommitLandingManager(t *testing.T) (*Manager, *fakeDriver, string) {
	t.Helper()
	runner, root := initTestRepo(t)
	writePipelineFile(t, root, "lines.txt", landingBase)
	gitTestRun(t, root, "add", "lines.txt")
	gitTestRun(t, root, "commit", "-qm", "base lines")
	driver := newFakeDriver()
	m := NewManager(Options{Git: runner, Driver: driver, WorktreeDir: filepath.Join(t.TempDir(), "worktrees")})
	m.SetSettingsFunc(func() Settings { return Settings{AutoMerge: true} })
	return m, driver, root
}

func startCommitLandingBee(t *testing.T, m *Manager, root, lead string, index int) Bee {
	t.Helper()
	bee, err := m.startSingleBee(context.Background(), lead, "project", root, BeeSpec{Name: fmt.Sprintf("Bee-%d", index), Task: "修改文本", Prompt: "修改文本"}, index, lead)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	if m.leads[lead] == nil {
		m.leads[lead] = &leadState{leadThreadID: lead, leadWorkDir: root, enabled: true}
	}
	m.leads[lead].bees = append(m.leads[lead].bees, bee)
	m.mu.Unlock()
	return bee
}

func landCommitBee(m *Manager, root, lead string, bee *Bee) bool {
	mu := m.getLeadMergeMutex(lead)
	mu.Lock()
	defer mu.Unlock()
	return m.mergeBee(context.Background(), lead, root, bee)
}

func assertLandingFile(t *testing.T, root, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "lines.txt"))
	if err != nil || string(data) != want {
		t.Fatalf("文件=%q want=%q err=%v", data, want, err)
	}
}

func assertLandingCommit(t *testing.T, m *Manager, root string, bee Bee) {
	t.Helper()
	if !bee.Merged || bee.LandedCommit == "" || bee.LandedPatchPath == "" {
		t.Fatalf("落地失败: %+v", bee)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != bee.LandedCommit {
		t.Fatalf("HEAD=%s want=%s", got, bee.LandedCommit)
	}
}

func TestManager_CommitLanding_TwoBees(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	a := startCommitLandingBee(t, m, root, "lead-two", 0)
	b := startCommitLandingBee(t, m, root, "lead-two", 1)
	aContent := strings.Replace(landingBase, "one\n", "bee one\n", 1)
	bContent := strings.Replace(landingBase, "nine\n", "bee nine\n", 1)
	writePipelineFile(t, a.WorktreePath, "lines.txt", aContent)
	writePipelineFile(t, b.WorktreePath, "lines.txt", bContent)
	if !landCommitBee(m, root, "lead-two", &a) {
		t.Fatal("首个 bee 不应等待")
	}
	assertLandingCommit(t, m, root, a)
	if got := gitTestRun(t, root, "show", a.LandedCommit+":lines.txt"); got != strings.TrimSpace(aContent) {
		t.Fatal("首个提交含其他改动")
	}
	if !landCommitBee(m, root, "lead-two", &b) {
		t.Fatal("第二个 bee 不应等待")
	}
	assertLandingCommit(t, m, root, b)
	if got := gitTestRun(t, root, "diff", a.LandedCommit, b.LandedCommit); strings.Contains(got, "+bee one") || !strings.Contains(got, "+bee nine") {
		t.Fatalf("第二个提交的改动不独立: %s", got)
	}
	if got := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); got != "2" {
		t.Fatalf("提交数=%s", got)
	}
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("工作区不干净: %s", got)
	}
}

func TestManager_CommitLanding_Handback(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	a := startCommitLandingBee(t, m, root, "lead-conflict", 0)
	b := startCommitLandingBee(t, m, root, "lead-conflict", 1)
	b.Acceptance = []string{"true"}
	first := strings.Replace(landingBase, "one\n", "first\n", 1)
	writePipelineFile(t, a.WorktreePath, "lines.txt", first)
	writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "second\n", 1))
	if !landCommitBee(m, root, "lead-conflict", &a) {
		t.Fatal("首个 bee 等待")
	}
	if !landCommitBee(m, root, "lead-conflict", &b) || b.Merged || b.ConflictHandbacks != 1 {
		t.Fatalf("未交回: %+v", b)
	}
	if len(b.Acceptance) != 1 || b.Acceptance[0] != "true" {
		t.Fatalf("冲突交回丢失验收命令：%v", b.Acceptance)
	}
	if b.BaseCommit != a.LandedCommit {
		t.Fatalf("交回基线=%s want=%s", b.BaseCommit, a.LandedCommit)
	}
	if got := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); got != "1" {
		t.Fatalf("提交数=%s", got)
	}
	assertLandingFile(t, root, first)
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("工作区不干净: %s", got)
	}
}

func TestManager_CommitLanding_UserChanges(t *testing.T) {
	t.Parallel()
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged=%v", staged), func(t *testing.T) {
			t.Parallel()
			m, _, root := newCommitLandingManager(t)
			var reports []string
			m.opts.OnReport = func(_, message string) error { reports = append(reports, message); return nil }
			bee := startCommitLandingBee(t, m, root, "lead-user", 0)
			user := strings.Replace(landingBase, "one\n", "user one\n", 1)
			writePipelineFile(t, root, "lines.txt", user)
			var index string
			if staged {
				gitTestRun(t, root, "add", "lines.txt")
				index = gitTestRun(t, root, "rev-parse", ":lines.txt")
			}
			writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
			if !landCommitBee(m, root, "lead-user", &bee) {
				t.Fatal("不同行改动不应等待")
			}
			assertLandingCommit(t, m, root, bee)
			if got := gitTestRun(t, root, "show", "HEAD:lines.txt"); strings.Contains(got, "user one") || !strings.Contains(got, "bee nine") {
				t.Fatalf("提交混入用户改动: %s", got)
			}
			assertLandingFile(t, root, strings.Replace(user, "nine\n", "bee nine\n", 1))
			if staged {
				if got := gitTestRun(t, root, "rev-parse", ":lines.txt"); got != index {
					t.Fatal("暂存内容被改写")
				}
				if !anyContains(reports, "未同步暂存区") {
					t.Fatalf("缺少暂存保留通知: %v", reports)
				}
			} else if got := gitTestRun(t, root, "diff", "--cached"); got != "" {
				t.Fatalf("用户改动不应被暂存: %s", got)
			}
		})
	}
}

func TestManager_CommitLanding_UserConflictWaitsForTick(t *testing.T) {
	t.Parallel()
	m, driver, root := newCommitLandingManager(t)
	bee := startCommitLandingBee(t, m, root, "lead-wait", 0)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	user := strings.Replace(landingBase, "one\n", "user\n", 1)
	writePipelineFile(t, root, "lines.txt", user)
	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})
	m.Tick(context.Background())
	got := m.State("lead-wait").Bees[0]
	if got.Status != StatusRunning || got.MergeWaitSince == nil || !strings.Contains(got.MergeError, "未提交改动冲突") {
		t.Fatalf("未保持 merge-wait: %+v", got)
	}
	if head := gitTestRun(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatal("等待时移动了 HEAD")
	}
	assertLandingFile(t, root, user)
	writePipelineFile(t, root, "lines.txt", landingBase)
	m.Tick(context.Background())
	got = m.State("lead-wait").Bees[0]
	assertLandingCommit(t, m, root, got)
	assertLandingFile(t, root, beeText)
}

func TestManager_CommitLanding_BaseIsHead(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	head := gitTestRun(t, root, "rev-parse", "HEAD")
	writePipelineFile(t, root, "lines.txt", "user dirty\n")
	writePipelineFile(t, root, "user.txt", "untracked\n")
	bee := startCommitLandingBee(t, m, root, "lead-base", 0)
	if bee.BaseCommit != head {
		t.Fatalf("基线=%s want=%s", bee.BaseCommit, head)
	}
	assertLandingFile(t, bee.WorktreePath, landingBase)
	if _, err := os.Stat(filepath.Join(bee.WorktreePath, "user.txt")); !os.IsNotExist(err) {
		t.Fatalf("用户未提交文件进入 bee 副本: %v", err)
	}
}

func TestManager_CommitLanding_HeadMoved(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	bee := startCommitLandingBee(t, m, root, "lead-cas", 0)
	old := gitTestRun(t, root, "rev-parse", "HEAD")
	tree := gitTestRun(t, root, "rev-parse", "HEAD^{tree}")
	other, err := m.opts.Git.CommitTree(context.Background(), root, tree, old, "外部提交")
	if err != nil {
		t.Fatal(err)
	}
	realGit := m.opts.Git.Bin
	scriptDir := t.TempDir()
	flag := filepath.Join(scriptDir, "once")
	if err := os.WriteFile(flag, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// 非 Windows 下使用轻量 sh 替身：在 -race 模式下重复启动整个测试二进制开销过高。
	wrapper := writeGitWrapper(t, scriptDir, realGit, other, flag)
	m.opts.Git = git.NewRunner(wrapper, 4)
	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	if landCommitBee(m, root, "lead-cas", &bee) || bee.MergeWaitSince == nil {
		t.Fatalf("CAS 失败应等待: %+v", bee)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != other {
		t.Fatalf("HEAD=%s want=%s", got, other)
	}
	assertLandingFile(t, root, landingBase)
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("CAS 失败污染 index: %s", got)
	}
	if !landCommitBee(m, root, "lead-cas", &bee) {
		t.Fatal("重试仍等待")
	}
	assertLandingCommit(t, m, root, bee)
	assertLandingFile(t, root, beeText)
}

func TestLandingIndexSyncFailureAfterCommitIsNotMergeError(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-index-sync"
	bee := startCommitLandingBee(t, m, root, lead, 0)
	old := gitTestRun(t, root, "rev-parse", "HEAD")
	dir := t.TempDir()
	flag := filepath.Join(dir, "once")
	if err := os.WriteFile(flag, nil, 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := writeGitWrapper(t, dir, m.opts.Git.Bin, "fail-reset", flag)
	m.opts.Git = git.NewRunner(wrapper, 4)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee\n", 1))
	bee.MergeError = "旧错误"
	if !landCommitBee(m, root, lead, &bee) {
		t.Fatal("落地不应等待")
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatalf("未触发索引同步失败: %v", err)
	}
	assertLandingCommit(t, m, root, bee)
	if bee.LandedCommit == old || bee.MergeError != "" {
		t.Fatalf("落地状态错误: %+v", bee)
	}
}

func TestManager_CommitLanding_SerialAcrossManagers(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	second := NewManager(Options{Git: m.opts.Git, Driver: newFakeDriver(), WorktreeDir: filepath.Join(t.TempDir(), "worktrees")})
	second.SetSettingsFunc(func() Settings { return Settings{AutoMerge: true} })
	a := startCommitLandingBee(t, m, root, "lead-a", 0)
	b := startCommitLandingBee(t, second, root, "lead-b", 0)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	writePipelineFile(t, a.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
	writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); landCommitBee(m, root, "lead-a", &a) }()
	go func() { defer wg.Done(); landCommitBee(second, root, "lead-b", &b) }()
	wg.Wait()
	if !a.Merged || !b.Merged {
		t.Fatalf("落地失败: a=%+v b=%+v", a, b)
	}
	if got := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); got != "2" {
		t.Fatalf("提交数=%s", got)
	}
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("工作区不干净: %s", got)
	}
}

func TestManager_CommitLanding_ConflictMarkersAndTimeout(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	now := time.Now()
	m.opts.Now = func() time.Time { return now }
	m.opts.SettleTimeout = time.Minute
	bee := startCommitLandingBee(t, m, root, "lead-marker", 0)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	markers := strings.Replace(landingBase, "one\n", "<<<<<<< user\na\n=======\nb\n>>>>>>> other\n", 1)
	writePipelineFile(t, root, "lines.txt", markers)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee\n", 1))
	if landCommitBee(m, root, "lead-marker", &bee) {
		t.Fatal("冲突标记应等待")
	}
	assertLandingFile(t, root, markers)
	now = now.Add(2 * time.Minute)
	if !landCommitBee(m, root, "lead-marker", &bee) || bee.Merged || bee.MergeWaitSince != nil {
		t.Fatalf("超时未结束: %+v", bee)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != before {
		t.Fatal("超时移动 HEAD")
	}
	assertLandingFile(t, root, markers)
}

func initLeadForCommitLanding(t *testing.T, m *Manager, root, lead string) {
	t.Helper()
	m.mu.Lock()
	if m.leads[lead] == nil {
		m.leads[lead] = &leadState{leadThreadID: lead, leadWorkDir: root, enabled: true}
	}
	m.mu.Unlock()
	m.BeginLeadRequest(lead)
}

func TestManager_CommitLanding_FoldSameRequest(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-fold-same-1234"
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	initLeadForCommitLanding(t, m, root, lead)
	reqID := m.requestID(lead)
	if reqID == "" {
		t.Fatal("应生成请求 ID")
	}

	a := startCommitLandingBee(t, m, root, lead, 0)
	b := startCommitLandingBee(t, m, root, lead, 1)
	writePipelineFile(t, a.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
	writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))

	if !landCommitBee(m, root, lead, &a) {
		t.Fatal("首个 bee 不应等待")
	}
	firstSHA := a.LandedCommit
	if firstSHA == "" {
		t.Fatal("首个 bee 未记录 LandedCommit")
	}

	if !landCommitBee(m, root, lead, &b) {
		t.Fatal("第二个 bee 不应等待")
	}
	head := gitTestRun(t, root, "rev-parse", "HEAD")
	if head == firstSHA {
		t.Fatal("折叠后应生成新提交哈希")
	}
	if parent := gitTestRun(t, root, "rev-parse", "HEAD^"); parent != before {
		t.Fatalf("HEAD^=%s want=%s", parent, before)
	}
	if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "1" {
		t.Fatalf("提交数=%s want=1", count)
	}
	content := gitTestRun(t, root, "show", "HEAD:lines.txt")
	if !strings.Contains(content, "bee one") || !strings.Contains(content, "bee nine") {
		t.Fatalf("折叠提交未包含两份改动: %s", content)
	}
	msg := gitTestRun(t, root, "show", "-s", "--format=%B", head)
	if !strings.Contains(msg, "- Bee-0: 修改文本") || !strings.Contains(msg, "- Bee-1: 修改文本") {
		t.Fatalf("提交说明缺少 bee 列表: %q", msg)
	}
	if !hasRequestTrailer(msg, reqID) {
		t.Fatalf("提交说明缺少请求 trailer %q: %q", requestTrailer(reqID), msg)
	}
	bees := m.State(lead).Bees
	if len(bees) != 2 || bees[0].LandedCommit != head || bees[1].LandedCommit != head {
		t.Fatalf("两个 bee 的 LandedCommit 均应等于 HEAD(%s): %+v", head, bees)
	}
	foldRef := fmt.Sprintf("refs/mcode/swarm-fold/%s/%d", leadPrefixID(lead), b.Index)
	if got := gitTestRun(t, root, "rev-parse", foldRef); got != firstSHA {
		t.Fatalf("追溯引用 %s=%s want=%s", foldRef, got, firstSHA)
	}
}

func TestManager_CommitLanding_FoldInterleavedLeads(t *testing.T) {
	t.Parallel()
	t.Run("ABA得3个提交", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const leadA = "lead-a-1111"
		const leadB = "lead-b-2222"
		before := gitTestRun(t, root, "rev-parse", "HEAD")
		initLeadForCommitLanding(t, m, root, leadA)
		initLeadForCommitLanding(t, m, root, leadB)

		a1 := startCommitLandingBee(t, m, root, leadA, 0)
		b1 := startCommitLandingBee(t, m, root, leadB, 0)
		a2 := startCommitLandingBee(t, m, root, leadA, 1)
		writePipelineFile(t, a1.WorktreePath, "a1.txt", "a1\n")
		writePipelineFile(t, b1.WorktreePath, "b1.txt", "b1\n")
		writePipelineFile(t, a2.WorktreePath, "a2.txt", "a2\n")

		if !landCommitBee(m, root, leadA, &a1) || !landCommitBee(m, root, leadB, &b1) || !landCommitBee(m, root, leadA, &a2) {
			t.Fatal("落地不应等待")
		}
		if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "3" {
			t.Fatalf("A-B-A 顺序应得 3 个提交，实际 %s", count)
		}
	})

	t.Run("AAB得2个提交", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const leadA = "lead-a-3333"
		const leadB = "lead-b-4444"
		before := gitTestRun(t, root, "rev-parse", "HEAD")
		initLeadForCommitLanding(t, m, root, leadA)
		initLeadForCommitLanding(t, m, root, leadB)

		a1 := startCommitLandingBee(t, m, root, leadA, 0)
		a2 := startCommitLandingBee(t, m, root, leadA, 1)
		b1 := startCommitLandingBee(t, m, root, leadB, 0)
		writePipelineFile(t, a1.WorktreePath, "a1.txt", "a1\n")
		writePipelineFile(t, a2.WorktreePath, "a2.txt", "a2\n")
		writePipelineFile(t, b1.WorktreePath, "b1.txt", "b1\n")

		if !landCommitBee(m, root, leadA, &a1) || !landCommitBee(m, root, leadA, &a2) || !landCommitBee(m, root, leadB, &b1) {
			t.Fatal("落地不应等待")
		}
		if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "2" {
			t.Fatalf("A-A-B 顺序应得 2 个提交，实际 %s", count)
		}
	})
}

func TestManager_CommitLanding_NoFoldOverUserCommit(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-user-commit"
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	initLeadForCommitLanding(t, m, root, lead)

	a := startCommitLandingBee(t, m, root, lead, 0)
	b := startCommitLandingBee(t, m, root, lead, 1)
	writePipelineFile(t, a.WorktreePath, "a.txt", "bee a\n")
	writePipelineFile(t, b.WorktreePath, "b.txt", "bee b\n")

	if !landCommitBee(m, root, lead, &a) {
		t.Fatal("首个 bee 不应等待")
	}
	writePipelineFile(t, root, "user.txt", "user change\n")
	gitTestRun(t, root, "add", "user.txt")
	gitTestRun(t, root, "commit", "-qm", "user commit")
	userSHA := gitTestRun(t, root, "rev-parse", "HEAD")

	if !landCommitBee(m, root, lead, &b) {
		t.Fatal("第二个 bee 不应等待")
	}
	if parent := gitTestRun(t, root, "rev-parse", "HEAD^"); parent != userSHA {
		t.Fatalf("应基于用户提交新建提交，HEAD^=%s want=%s", parent, userSHA)
	}
	if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "3" {
		t.Fatalf("提交数=%s want=3", count)
	}
	if got := gitTestRun(t, root, "show", userSHA+":user.txt"); got != "user change" {
		t.Fatalf("用户提交被改动: %s", got)
	}
}

func TestManager_CommitLanding_NoFoldWhenRefContains(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-ref-contains"
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	initLeadForCommitLanding(t, m, root, lead)

	a := startCommitLandingBee(t, m, root, lead, 0)
	b := startCommitLandingBee(t, m, root, lead, 1)
	writePipelineFile(t, a.WorktreePath, "a.txt", "bee a\n")
	writePipelineFile(t, b.WorktreePath, "b.txt", "bee b\n")

	if !landCommitBee(m, root, lead, &a) {
		t.Fatal("首个 bee 不应等待")
	}
	firstSHA := a.LandedCommit
	gitTestRun(t, root, "tag", "v1.0", firstSHA)

	if !landCommitBee(m, root, lead, &b) {
		t.Fatal("第二个 bee 不应等待")
	}
	if parent := gitTestRun(t, root, "rev-parse", "HEAD^"); parent != firstSHA {
		t.Fatalf("被打 tag 后应新建提交，HEAD^=%s want=%s", parent, firstSHA)
	}
	if tagSHA := gitTestRun(t, root, "rev-parse", "v1.0"); tagSHA != firstSHA {
		t.Fatalf("tag 指向改变=%s want=%s", tagSHA, firstSHA)
	}
	if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "2" {
		t.Fatalf("提交数=%s want=2", count)
	}
}

func TestManager_CommitLanding_NoFoldNewRequest(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	m.opts.Now = func() time.Time { return now }
	const lead = "lead-new-req"
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	initLeadForCommitLanding(t, m, root, lead)

	a := startCommitLandingBee(t, m, root, lead, 0)
	b := startCommitLandingBee(t, m, root, lead, 1)
	writePipelineFile(t, a.WorktreePath, "a.txt", "bee a\n")
	writePipelineFile(t, b.WorktreePath, "b.txt", "bee b\n")

	if !landCommitBee(m, root, lead, &a) {
		t.Fatal("首个 bee 不应等待")
	}
	firstSHA := a.LandedCommit

	now = now.Add(2 * time.Second)
	m.BeginLeadRequest(lead)

	if !landCommitBee(m, root, lead, &b) {
		t.Fatal("新请求后的第二个 bee 不应等待")
	}
	if parent := gitTestRun(t, root, "rev-parse", "HEAD^"); parent != firstSHA {
		t.Fatalf("新请求应新建提交，HEAD^=%s want=%s", parent, firstSHA)
	}
	if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "2" {
		t.Fatalf("提交数=%s want=2", count)
	}
}

func TestManager_CommitLanding_NoFoldAutoPush(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	m.SetSettingsFunc(func() Settings { return Settings{AutoMerge: true, AutoPush: true} })
	const lead = "lead-autopush"
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	initLeadForCommitLanding(t, m, root, lead)

	a := startCommitLandingBee(t, m, root, lead, 0)
	b := startCommitLandingBee(t, m, root, lead, 1)
	writePipelineFile(t, a.WorktreePath, "a.txt", "bee a\n")
	writePipelineFile(t, b.WorktreePath, "b.txt", "bee b\n")

	if !landCommitBee(m, root, lead, &a) || !landCommitBee(m, root, lead, &b) {
		t.Fatal("落地不应等待")
	}
	if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "2" {
		t.Fatalf("AutoPush 开启时不应折叠，提交数=%s want=2", count)
	}
}

func TestManager_CommitLanding_FoldConcurrentBees(t *testing.T) {
	t.Parallel()
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-concurrent"
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	initLeadForCommitLanding(t, m, root, lead)

	a := startCommitLandingBee(t, m, root, lead, 0)
	b := startCommitLandingBee(t, m, root, lead, 1)
	writePipelineFile(t, a.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
	writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); landCommitBee(m, root, lead, &a) }()
	go func() { defer wg.Done(); landCommitBee(m, root, lead, &b) }()
	wg.Wait()

	if !a.Merged || !b.Merged {
		t.Fatalf("并发落地失败: a=%+v b=%+v", a, b)
	}
	if count := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); count != "1" {
		t.Fatalf("并发折叠后提交数=%s want=1", count)
	}
	content := gitTestRun(t, root, "show", "HEAD:lines.txt")
	if !strings.Contains(content, "bee one") || !strings.Contains(content, "bee nine") {
		t.Fatalf("并发折叠缺少改动: %s", content)
	}
	if st := gitTestRun(t, root, "status", "--porcelain"); st != "" {
		t.Fatalf("工作区不干净: %s", st)
	}
}

func TestManager_FinalizeRequestCommit(t *testing.T) {
	t.Parallel()
	t.Run("重写说明并保持工作区暂存区与树父提交不变", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const lead = "lead-finalize-ok"
		before := gitTestRun(t, root, "rev-parse", "HEAD")
		initLeadForCommitLanding(t, m, root, lead)
		reqID := m.requestID(lead)

		a := startCommitLandingBee(t, m, root, lead, 0)
		b := startCommitLandingBee(t, m, root, lead, 1)
		writePipelineFile(t, a.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
		writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
		if !landCommitBee(m, root, lead, &a) || !landCommitBee(m, root, lead, &b) {
			t.Fatal("落地不应等待")
		}
		m.mu.Lock()
		for i := range m.leads[lead].bees {
			m.leads[lead].bees[i].Status = StatusCompleted
		}
		m.mu.Unlock()

		// 预先建好锁，在 Describe 回调中检查是否未持有 HEAD 锁和 Manager 锁。
		lockHead(root)()

		// 用户另有未提交脏文件和已暂存内容
		writePipelineFile(t, root, "dirty.txt", "dirty work\n")
		writePipelineFile(t, root, "staged.txt", "staged work\n")
		gitTestRun(t, root, "add", "staged.txt")
		stagedBlob := gitTestRun(t, root, "rev-parse", ":staged.txt")

		foldedTree := gitTestRun(t, root, "rev-parse", "HEAD^{tree}")
		foldedParent := gitTestRun(t, root, "rev-parse", "HEAD^")
		if foldedParent != before {
			t.Fatalf("前置折叠父提交=%s want=%s", foldedParent, before)
		}

		calls := 0
		m.opts.Describe = func(ctx context.Context, diff string) (string, error) {
			calls++
			if !strings.Contains(diff, "+bee one") || !strings.Contains(diff, "+bee nine") {
				t.Errorf("diff 缺少折叠改动: %s", diff)
			}
			if value, ok := headLocks.Load(filepath.Clean(root)); ok {
				mu := value.(*sync.Mutex)
				if !mu.TryLock() {
					t.Error("生成时持有 HEAD 锁")
				} else {
					mu.Unlock()
				}
			}
			if !m.mu.TryLock() {
				t.Error("生成时持有 Manager 状态锁")
			} else {
				m.mu.Unlock()
			}
			return "feat: 统一完成两项文本修改\n\nMcode-Request: fake-trailer\n", nil
		}

		ctx := context.Background()
		m.FinalizeRequestCommit(ctx, lead)
		m.FinalizeRequestCommit(ctx, lead)

		if calls != 1 {
			t.Fatalf("重复调用 Describe 次数=%d want=1", calls)
		}
		newHead := gitTestRun(t, root, "rev-parse", "HEAD")
		if gotTree := gitTestRun(t, root, "rev-parse", "HEAD^{tree}"); gotTree != foldedTree {
			t.Fatalf("tree 被改变=%s want=%s", gotTree, foldedTree)
		}
		if gotParent := gitTestRun(t, root, "rev-parse", "HEAD^"); gotParent != foldedParent {
			t.Fatalf("父提交被改变=%s want=%s", gotParent, foldedParent)
		}
		wantMsg := "feat: 统一完成两项文本修改\n\n- Bee-0: 修改文本\n- Bee-1: 修改文本\n\n" + requestTrailer(reqID)
		if gotMsg := gitTestRun(t, root, "show", "-s", "--format=%B", newHead); gotMsg != wantMsg {
			t.Fatalf("提交说明=%q want=%q", gotMsg, wantMsg)
		}
		if gotDirty, err := os.ReadFile(filepath.Join(root, "dirty.txt")); err != nil || string(gotDirty) != "dirty work\n" {
			t.Fatalf("用户脏文件被改动: %q err=%v", gotDirty, err)
		}
		if gotStaged := gitTestRun(t, root, "rev-parse", ":staged.txt"); gotStaged != stagedBlob {
			t.Fatalf("已暂存内容被改动: %s want=%s", gotStaged, stagedBlob)
		}
		bees := m.State(lead).Bees
		if len(bees) != 2 || bees[0].LandedCommit != newHead || bees[1].LandedCommit != newHead {
			t.Fatalf("bee LandedCommit 未更新为新 HEAD(%s): %+v", newHead, bees)
		}
	})

	t.Run("仍有活跃bee时不调用Describe", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const lead = "lead-finalize-active"
		initLeadForCommitLanding(t, m, root, lead)
		a := startCommitLandingBee(t, m, root, lead, 0)
		writePipelineFile(t, a.WorktreePath, "a.txt", "bee a\n")
		if !landCommitBee(m, root, lead, &a) {
			t.Fatal("落地不应等待")
		}
		// startCommitLandingBee 默认 StatusRunning
		calls := 0
		m.opts.Describe = func(context.Context, string) (string, error) {
			calls++
			return "feat: 不应生成", nil
		}
		m.FinalizeRequestCommit(context.Background(), lead)
		if calls != 0 {
			t.Fatalf("存在 StatusRunning 时不应调用 Describe，calls=%d", calls)
		}
		// 覆盖 PendingMerge / AwaitingFinalize / MergeWaitSince
		for _, mutate := range []func(*Bee){
			func(b *Bee) { b.Status = StatusCompleted; b.PendingMerge = true },
			func(b *Bee) { b.PendingMerge = false; b.AwaitingFinalize = true },
			func(b *Bee) {
				b.AwaitingFinalize = false
				now := time.Now()
				b.MergeWaitSince = &now
			},
		} {
			m.mu.Lock()
			mutate(&m.leads[lead].bees[0])
			m.mu.Unlock()
			m.FinalizeRequestCommit(context.Background(), lead)
			if calls != 0 {
				t.Fatalf("存在未落定状态时不应调用 Describe，calls=%d", calls)
			}
		}
	})

	t.Run("HEAD非本请求提交时不改动", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const lead = "lead-finalize-other-head"
		initLeadForCommitLanding(t, m, root, lead)
		a := startCommitLandingBee(t, m, root, lead, 0)
		writePipelineFile(t, a.WorktreePath, "a.txt", "bee a\n")
		if !landCommitBee(m, root, lead, &a) {
			t.Fatal("落地不应等待")
		}
		m.mu.Lock()
		m.leads[lead].bees[0].Status = StatusCompleted
		m.mu.Unlock()

		writePipelineFile(t, root, "user.txt", "user\n")
		gitTestRun(t, root, "add", "user.txt")
		gitTestRun(t, root, "commit", "-qm", "user commit")
		userHead := gitTestRun(t, root, "rev-parse", "HEAD")

		calls := 0
		m.opts.Describe = func(context.Context, string) (string, error) {
			calls++
			return "feat: 不应重写", nil
		}
		m.FinalizeRequestCommit(context.Background(), lead)
		if calls != 0 {
			t.Fatalf("HEAD 非本请求提交时不应调用 Describe，calls=%d", calls)
		}
		if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != userHead {
			t.Fatalf("HEAD 不应改动: %s want=%s", got, userHead)
		}
	})

	t.Run("diff截断至12000字节", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const lead = "lead-finalize-bounded"
		initLeadForCommitLanding(t, m, root, lead)
		bee := startCommitLandingBee(t, m, root, lead, 0)
		writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Repeat("新增文本\n", 4000))
		if !landCommitBee(m, root, lead, &bee) {
			t.Fatal("落地不应等待")
		}
		m.mu.Lock()
		m.leads[lead].bees[0].Status = StatusCompleted
		m.mu.Unlock()

		called := false
		m.opts.Describe = func(_ context.Context, diff string) (string, error) {
			called = true
			if len(diff) > 12000 || !strings.Contains(diff, "+新增文本") || !strings.HasSuffix(diff, "\n") {
				t.Errorf("diff 未按完整行截断: 字节数=%d", len(diff))
			}
			return "feat: 新增文本", nil
		}
		m.FinalizeRequestCommit(context.Background(), lead)
		if !called {
			t.Fatal("未调用 Describe")
		}
	})

	t.Run("超时回退保留原提交说明", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		m.opts.DescribeTimeout = 200 * time.Millisecond
		const lead = "lead-finalize-timeout"
		initLeadForCommitLanding(t, m, root, lead)
		bee := startCommitLandingBee(t, m, root, lead, 0)
		writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
		if !landCommitBee(m, root, lead, &bee) {
			t.Fatal("落地不应等待")
		}
		m.mu.Lock()
		m.leads[lead].bees[0].Status = StatusCompleted
		m.mu.Unlock()

		beforeHead := gitTestRun(t, root, "rev-parse", "HEAD")
		beforeMsg := gitTestRun(t, root, "show", "-s", "--format=%B", beforeHead)

		called := false
		m.opts.Describe = func(ctx context.Context, _ string) (string, error) {
			called = true
			<-ctx.Done()
			// 超时后的响应即使没有返回错误，也不能覆盖原说明。
			return "超时响应", nil
		}
		m.FinalizeRequestCommit(context.Background(), lead)
		if !called {
			t.Fatal("未调用 Describe")
		}
		if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatalf("超时不应移动 HEAD: %s want=%s", got, beforeHead)
		}
		if gotMsg := gitTestRun(t, root, "show", "-s", "--format=%B", beforeHead); gotMsg != beforeMsg {
			t.Fatalf("超时不应改写提交说明: %q want=%q", gotMsg, beforeMsg)
		}
	})
}
