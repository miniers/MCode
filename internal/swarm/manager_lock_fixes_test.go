package swarm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/kanban"
)

type lockFixResumeResult struct {
	n   int
	err error
}

func lockFixShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func lockFixWriteWrapper(t *testing.T, body string) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 git 替身失败: %v", err)
	}
	return wrapper
}

func lockFixRunGit(t *testing.T, gitBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// 验证 recordPartialWork 在 DiffCachedNames 返回错误（半成品状态未知）时，
// 自动重试保留原有 worktree，不调用 WorktreeRemove 也不删除目录。
func TestRetryKeepsWorktreeWhenPartialWorkUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell git wrapper test runs on POSIX")
	}
	t.Parallel()

	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	driver.onFollowUp = func(string, string) error {
		return errors.New("session closed")
	}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			RetryFailed:   true,
			WorkspaceMode: "worktree",
		}
	})

	const leadID = "lead-retry-partial-unknown"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-partial-unknown",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Partial", Task: "partial task", Prompt: "partial prompt"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(bees) != 1 || bees[0].WorktreePath == "" {
		t.Fatalf("期望创建 1 个带 worktree 的 bee，实际 %+v", bees)
	}
	wtPath := bees[0].WorktreePath
	partialFile := filepath.Join(wtPath, "partial.txt")
	if err := os.WriteFile(partialFile, []byte("uncommitted partial\n"), 0o644); err != nil {
		t.Fatalf("写入半成品文件失败: %v", err)
	}

	stateDir := t.TempDir()
	diffCallsFile := filepath.Join(stateDir, "diff_calls")
	removeCallsFile := filepath.Join(stateDir, "remove_calls")
	script := "#!/bin/sh\nset -eu\n" +
		"real_git=" + lockFixShellQuote(runner.Bin) + "\n" +
		"diff_calls=" + lockFixShellQuote(diffCallsFile) + "\n" +
		"remove_calls=" + lockFixShellQuote(removeCallsFile) + "\n" +
		"if [ \"$#\" -ge 3 ] && [ \"$1\" = \"diff\" ] && [ \"$2\" = \"--cached\" ] && [ \"$3\" = \"--name-only\" ]; then\n" +
		"  printf '1\\n' >> \"$diff_calls\"\n" +
		"  printf 'simulated diff cached names error\\n' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"if [ \"$#\" -ge 2 ] && [ \"$1\" = \"worktree\" ] && [ \"$2\" = \"remove\" ]; then\n" +
		"  printf '1\\n' >> \"$remove_calls\"\n" +
		"fi\n" +
		"exec \"$real_git\" \"$@\"\n"
	mgr.opts.Git = git.NewRunner(lockFixWriteWrapper(t, script), 4)

	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "transient failure",
	})

	mgr.Tick(ctx)

	diffRaw, err := os.ReadFile(diffCallsFile)
	if err != nil || strings.Count(string(diffRaw), "\n") != 1 {
		t.Fatalf("期望调用 1 次 DiffCachedNames，实际 err=%v raw=%q", err, string(diffRaw))
	}
	if _, err := os.Stat(removeCallsFile); !os.IsNotExist(err) {
		t.Fatalf("半成品状态未知时不应调用 WorktreeRemove，stat err=%v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("半成品状态未知时应保留 worktree 目录，stat err=%v", err)
	}
	if _, err := os.Stat(partialFile); err != nil {
		t.Fatalf("worktree 里的半成品文件不应被删，stat err=%v", err)
	}
	st := mgr.State(leadID)
	if len(st.Bees) != 1 || st.Bees[0].Status != StatusRunning || st.Bees[0].WorktreePath != wtPath || st.Bees[0].ThreadID == bees[0].ThreadID {
		t.Fatalf("重试出的 bee 应复用原 worktree，实际 %+v", st.Bees)
	}
}

// 验证不同 Lead 对同一 targetWorkDir 并发调用 applyMerge 时，由 lockApply 串行化：
// 第一次 ApplyPatch3Way 放行前，第二次 ApplyPatch3Way 不会开始。
func TestApplyMergeSerializesSameTargetDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell git wrapper test runs on POSIX")
	}
	t.Parallel()

	ctx := context.Background()
	runner, dir := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("base b\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	lockFixRunGit(t, runner.Bin, dir, "add", "a.txt", "b.txt")
	lockFixRunGit(t, runner.Bin, dir, "commit", "-m", "base files")
	baseHead, err := runner.RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("RevParse HEAD: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("bee a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt patch: %v", err)
	}
	if err := runner.AddAll(ctx, dir); err != nil {
		t.Fatalf("AddAll a.txt: %v", err)
	}
	patchA, err := runner.DiffCachedBinary(ctx, dir, baseHead)
	if err != nil {
		t.Fatalf("DiffCachedBinary a.txt: %v", err)
	}
	lockFixRunGit(t, runner.Bin, dir, "reset", "-q", "--hard", baseHead)

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bee b\n"), 0o644); err != nil {
		t.Fatalf("write b.txt patch: %v", err)
	}
	if err := runner.AddAll(ctx, dir); err != nil {
		t.Fatalf("AddAll b.txt: %v", err)
	}
	patchB, err := runner.DiffCachedBinary(ctx, dir, baseHead)
	if err != nil {
		t.Fatalf("DiffCachedBinary b.txt: %v", err)
	}
	lockFixRunGit(t, runner.Bin, dir, "reset", "-q", "--hard", baseHead)

	stateDir := t.TempDir()
	claimDir := filepath.Join(stateDir, "first-claim")
	firstEntered := filepath.Join(stateDir, "first-entered")
	secondEntered := filepath.Join(stateDir, "second-entered")
	releaseFile := filepath.Join(stateDir, "release")
	callsFile := filepath.Join(stateDir, "apply-calls")

	script := "#!/bin/sh\nset -eu\n" +
		"real_git=" + lockFixShellQuote(runner.Bin) + "\n" +
		"claim_dir=" + lockFixShellQuote(claimDir) + "\n" +
		"first_entered=" + lockFixShellQuote(firstEntered) + "\n" +
		"second_entered=" + lockFixShellQuote(secondEntered) + "\n" +
		"release_file=" + lockFixShellQuote(releaseFile) + "\n" +
		"calls_file=" + lockFixShellQuote(callsFile) + "\n" +
		"if [ \"$#\" -ge 2 ] && [ \"$1\" = \"apply\" ] && [ \"$2\" = \"--3way\" ]; then\n" +
		"  if mkdir \"$claim_dir\" 2>/dev/null; then\n" +
		"    printf 'first\\n' >> \"$calls_file\"\n" +
		"    : > \"$first_entered\"\n" +
		"    while [ ! -f \"$release_file\" ]; do\n" +
		"      sleep 0.01\n" +
		"    done\n" +
		"  else\n" +
		"    printf 'second\\n' >> \"$calls_file\"\n" +
		"    : > \"$second_entered\"\n" +
		"  fi\n" +
		"fi\n" +
		"exec \"$real_git\" \"$@\"\n"

	mgr := NewManager(Options{
		Git:         git.NewRunner(lockFixWriteWrapper(t, script), 4),
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
	})

	h1 := &Bee{Index: 0, ChangedFiles: []string{"a.txt"}}
	h2 := &Bee{Index: 1, ChangedFiles: []string{"b.txt"}}

	done1 := make(chan struct{})
	done2 := make(chan struct{})
	started2 := false
	defer func() {
		_ = os.WriteFile(releaseFile, nil, 0o600)
		<-done1
		if started2 {
			<-done2
		}
	}()

	go func() {
		defer close(done1)
		mgr.applyMerge(ctx, "lead-apply-1", dir, h1, patchA)
	}()

	firstReady := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(firstEntered); err == nil {
				close(firstReady)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	select {
	case <-firstReady:
	case <-time.After(5 * time.Second):
		t.Fatal("第一次 ApplyPatch3Way 未在超时内进入")
	}

	started2 = true
	go func() {
		defer close(done2)
		mgr.applyMerge(ctx, "lead-apply-2", dir, h2, patchB)
	}()

	secondStarted := make(chan struct{})
	stopPoll := make(chan struct{})
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		for {
			select {
			case <-stopPoll:
				return
			default:
			}
			if _, err := os.Stat(secondEntered); err == nil {
				close(secondStarted)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	select {
	case <-secondStarted:
		close(stopPoll)
		<-pollDone
		t.Fatal("第一次 ApplyPatch3Way 放行前，第二次 ApplyPatch3Way 已开始")
	case <-done2:
		close(stopPoll)
		<-pollDone
		t.Fatal("第一次 ApplyPatch3Way 放行前，第二次 applyMerge 已返回")
	case <-time.After(150 * time.Millisecond):
		close(stopPoll)
		<-pollDone
	}

	if err := os.WriteFile(releaseFile, nil, 0o600); err != nil {
		t.Fatalf("写入放行标记失败: %v", err)
	}

	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("放行后第一次 applyMerge 未在超时内结束")
	}
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("放行后第二次 applyMerge 未在超时内结束")
	}

	if !h1.Merged || !h2.Merged {
		t.Fatalf("期望两次合并都成功，实际 h1=%+v h2=%+v", h1, h2)
	}
	callsRaw, err := os.ReadFile(callsFile)
	if err != nil || string(callsRaw) != "first\nsecond\n" {
		t.Fatalf("期望串行调用两次 ApplyPatch3Way，实际 err=%v calls=%q", err, string(callsRaw))
	}
}

// 验证 ResumeInterrupted 在释放 lead mergeMu 后才调用 processQueue（获取 dispatchMu），
// 避免与 dispatchMu → mergeMu 形成 AB-BA 死锁。
func TestResumeInterruptedReleasesMergeMuBeforeQueue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled: true,
			MaxBees: 2,
		}
	})

	const lead = "lead-resume-release-mergemu"
	mgr.SetEnabled(lead, true)
	mgr.mu.Lock()
	mgr.leads[lead].leadWorkDir = dir
	mgr.leads[lead].projectID = "proj-resume-lock"
	mgr.leads[lead].heldQueue = []queuedBee{
		{
			leadThreadID: lead,
			projectID:    "proj-resume-lock",
			leadWorkDir:  dir,
			index:        0,
			spec: BeeSpec{
				Name:     "Bee-Queued",
				Task:     "queued task",
				Prompt:   "queued prompt",
				ReadOnly: true,
			},
		},
	}
	mgr.mu.Unlock()

	mgr.dispatchMu.Lock()
	dispatchLocked := true
	defer func() {
		if dispatchLocked {
			mgr.dispatchMu.Unlock()
		}
	}()

	done := make(chan lockFixResumeResult, 1)
	go func() {
		n, err := mgr.ResumeInterrupted(ctx, lead)
		done <- lockFixResumeResult{n: n, err: err}
	}()

	// 先等到 heldQueue 已经进入 m.queue，确保 ResumeInterrupted 已经进入持 mergeMu 段并准备走 processQueue。
	deadline := time.Now().Add(2 * time.Second)
	for mgr.QueueLength() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("等待 ResumeInterrupted 将 heldQueue 移入队列超时")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mergeMu := mgr.getLeadMergeMutex(lead)
	acquired := false
	for time.Now().Before(deadline) {
		if mergeMu.TryLock() {
			mergeMu.Unlock()
			acquired = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !acquired {
		t.Fatal("持有 dispatchMu 时未能在 2 秒内获取 mergeMu，ResumeInterrupted 未先释放 mergeMu")
	}

	dispatchLocked = false
	mgr.dispatchMu.Unlock()

	select {
	case res := <-done:
		if res.err != nil || res.n != 1 {
			t.Fatalf("ResumeInterrupted 返回异常: n=%d err=%v", res.n, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("释放 dispatchMu 后 ResumeInterrupted 未在 5 秒内结束")
	}
}
