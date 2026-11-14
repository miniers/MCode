package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/kanban"
	"mcode/internal/testenv"
)

type fakeDriver struct {
	mu         sync.Mutex
	nextID     int
	sessions   map[string]kanban.SessionState
	onStart    func(kanban.SessionRequest)
	relations  map[string]fakeRelation
	followUps  []fakeFollowUp
	interrupts []string
	onFollowUp func(threadID, text string) error
}

type fakeRelation struct {
	parentThreadID string
	isHelper       bool
}

type fakeFollowUp struct {
	threadID string
	text     string
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		sessions:  make(map[string]kanban.SessionState),
		relations: make(map[string]fakeRelation),
	}
}

func (d *fakeDriver) SetThreadRelation(threadID, parentThreadID string, isHelper bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.relations == nil {
		d.relations = make(map[string]fakeRelation)
	}
	d.relations[threadID] = fakeRelation{
		parentThreadID: parentThreadID,
		isHelper:       isHelper,
	}
}

func (d *fakeDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.followUps = append(d.followUps, fakeFollowUp{threadID: threadID, text: text})
	if d.onFollowUp != nil {
		return d.onFollowUp(threadID, text)
	}
	return nil
}

func (d *fakeDriver) InterruptSession(ctx context.Context, threadID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.interrupts = append(d.interrupts, threadID)
	return nil
}

func (d *fakeDriver) StartSession(ctx context.Context, req kanban.SessionRequest) (string, error) {
	if d.onStart != nil {
		d.onStart(req)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	id := fmt.Sprintf("bee-thread-%d", d.nextID)
	d.sessions[id] = kanban.SessionState{
		Running: true,
	}
	return id, nil
}

func (d *fakeDriver) SessionState(ctx context.Context, threadID string) (kanban.SessionState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.sessions[threadID]
	if !ok {
		return kanban.SessionState{}, fmt.Errorf("session not found: %s", threadID)
	}
	return st, nil
}

func (d *fakeDriver) setSessionState(threadID string, st kanban.SessionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions[threadID] = st
}

func initTestRepo(t *testing.T) (*git.Runner, string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	dir := t.TempDir()
	runner := git.NewRunner(gitBin, 4)
	ctx := context.Background()

	runCmd := func(args ...string) {
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

	runCmd("init")
	runCmd("config", "core.autocrlf", "false")
	runCmd("config", "user.name", "test-user")
	runCmd("config", "user.email", "test@localhost")
	// Runner 使用缓存的登录环境；用仓库配置隔离测试，避免后续 t.Setenv 失效而访问真实远端。
	runCmd("config", "protocol.https.allow", "never")

	baseFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(baseFile, []byte("# Demo Repo\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runCmd("add", "README.md")
	runCmd("commit", "-m", "initial commit")

	return runner, dir
}

// (a) 两个 bee 改不同文件，Tick 后两者都 Merged，主工作区出现两处改动，ConflictFiles 为空
func TestManager_TwoBees_DifferentFiles(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-diff-01"
	driver.onStart = func(req kanban.SessionRequest) {
		if req.LeadThreadID != leadID {
			t.Errorf("子会话未携带 Lead ID: %q", req.LeadThreadID)
		}
		if owner, ok := mgr.LeadThreadForWorkDir(req.WorkDir); !ok || owner != leadID {
			t.Fatalf("启动 provider 前 worktree 必须已能反查所属项目: %q, %v", owner, ok)
		}
	}
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Edit a.txt", Prompt: "P1"},
			{Name: "Bee-2", Task: "Edit b.txt", Prompt: "P2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(bees) != 2 {
		t.Fatalf("expected 2 bees, got %d", len(bees))
	}

	// 模拟两个 bee 在各自的 worktree 中修改不同文件
	st := mgr.State(leadID)
	wt1 := st.Bees[0].WorktreePath
	wt2 := st.Bees[1].WorktreePath

	fileA := filepath.Join(wt1, "a.txt")
	if err := os.WriteFile(fileA, []byte("change from bee 1\n"), 0644); err != nil {
		t.Fatalf("write fileA in wt1: %v", err)
	}

	fileB := filepath.Join(wt2, "b.txt")
	if err := os.WriteFile(fileB, []byte("change from bee 2\n"), 0644); err != nil {
		t.Fatalf("write fileB in wt2: %v", err)
	}

	// 将两个会话置为完成状态
	driver.setSessionState(st.Bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 1 completed",
	})
	driver.setSessionState(st.Bees[1].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 2 completed",
	})

	mgr.Tick(ctx)

	after := mgr.State(leadID)
	if len(after.Bees) != 2 {
		t.Fatalf("expected 2 bees after tick, got %d", len(after.Bees))
	}

	for _, h := range after.Bees {
		if h.Status != StatusCompleted {
			t.Errorf("bee %s: expected status completed, got %s", h.Name, h.Status)
		}
		if !h.Merged {
			t.Errorf("bee %s: expected Merged=true, got false, mergeError: %s", h.Name, h.MergeError)
		}
		if len(h.ConflictFiles) != 0 {
			t.Errorf("bee %s: expected no ConflictFiles, got %v", h.Name, h.ConflictFiles)
		}
		wantFile := "a.txt"
		if h.Name == "Bee-2" {
			wantFile = "b.txt"
		}
		if len(h.ChangedFiles) != 1 || h.ChangedFiles[0] != wantFile {
			t.Fatalf("bee %s: 改动文件 = %v，预期 %s", h.Name, h.ChangedFiles, wantFile)
		}
	}
	after.Bees[0].ChangedFiles[0] = "mutated"
	if mgr.State(leadID).Bees[0].ChangedFiles[0] == "mutated" {
		t.Fatal("State 返回的文件列表未独立复制")
	}

	// 验证主工作区出现两处改动
	contentA, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt in main dir: %v", err)
	}
	if string(contentA) != "change from bee 1\n" {
		t.Errorf("unexpected content for a.txt: %s", string(contentA))
	}

	contentB, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatalf("read b.txt in main dir: %v", err)
	}
	if string(contentB) != "change from bee 2\n" {
		t.Errorf("unexpected content for b.txt: %s", string(contentB))
	}
}

// (b) 两个 bee 改同一文件的同一行：第二个三方冲突，ConflictFiles 包含该文件且不算已合入
func TestManager_TwoBees_SameLineConflict(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	// 在主分支准备初始文件并提交
	sharedFile := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(sharedFile, []byte("line 1\nline 2 original\nline 3\n"), 0644); err != nil {
		t.Fatalf("write shared.txt: %v", err)
	}

	gitBin, _ := exec.LookPath("git")
	cmdAdd := exec.CommandContext(ctx, gitBin, "add", "shared.txt")
	cmdAdd.Dir = dir
	_ = cmdAdd.Run()

	cmdCommit := exec.CommandContext(ctx, gitBin, "commit", "-m", "add shared.txt")
	cmdCommit.Dir = dir
	cmdCommit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test-user",
		"GIT_AUTHOR_EMAIL=test@localhost",
		"GIT_COMMITTER_NAME=test-user",
		"GIT_COMMITTER_EMAIL=test@localhost",
	)
	_ = cmdCommit.Run()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-conflict-02"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Edit line 2 bee 1", Prompt: "P1"},
			{Name: "Bee-2", Task: "Edit line 2 bee 2", Prompt: "P2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	st := mgr.State(leadID)
	wt1 := st.Bees[0].WorktreePath
	wt2 := st.Bees[1].WorktreePath

	// Bee 1 改第 2 行
	if err := os.WriteFile(filepath.Join(wt1, "shared.txt"), []byte("line 1\nline 2 from bee 1\nline 3\n"), 0644); err != nil {
		t.Fatalf("write in wt1: %v", err)
	}
	// Bee 2 也改第 2 行
	if err := os.WriteFile(filepath.Join(wt2, "shared.txt"), []byte("line 1\nline 2 from bee 2\nline 3\n"), 0644); err != nil {
		t.Fatalf("write in wt2: %v", err)
	}

	// 先让 Bee 1 完成并合并
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 1 done",
	})
	mgr.Tick(ctx)

	st1 := mgr.State(leadID)
	if !st1.Bees[0].Merged {
		t.Fatalf("expected bee 1 merged cleanly, mergeError: %s", st1.Bees[0].MergeError)
	}
	if len(st1.Bees[0].ConflictFiles) != 0 {
		t.Fatalf("expected bee 1 ConflictFiles empty, got %v", st1.Bees[0].ConflictFiles)
	}

	// 再让 Bee 2 完成并合并，此时将发生冲突并交回
	driver.setSessionState(bees[1].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 2 done",
	})
	mgr.Tick(ctx)

	st2 := mgr.State(leadID)
	h2 := st2.Bees[1]
	// 冲突被交回：生成接手的新 bee，处于 running 状态，ConflictHandbacks 为 1
	if h2.ThreadID == bees[1].ThreadID {
		t.Fatalf("冲突交回后应生成新的会话 ThreadID")
	}
	if h2.Status != StatusRunning {
		t.Errorf("expected bee 2 running after handback, got %s", h2.Status)
	}
	if h2.Merged {
		t.Errorf("交回的 bee 不应标记为已合入: %+v", h2)
	}
	if h2.ConflictHandbacks != 1 {
		t.Fatalf("expected bee 2 ConflictHandbacks to be 1, got %d", h2.ConflictHandbacks)
	}
	if !strings.Contains(h2.Prompt, "冲突标记") {
		t.Fatalf("expected bee 2 prompt to contain conflict warning, got: %s", h2.Prompt)
	}

	// 主工作区文件与落地前完全一致（即 bee 1 的改动），且不含冲突标记
	content, readErr := os.ReadFile(sharedFile)
	if readErr != nil {
		t.Fatalf("read shared.txt: %v", readErr)
	}
	expectedContent := "line 1\nline 2 from bee 1\nline 3\n"
	if string(content) != expectedContent {
		t.Fatalf("主工作区应与落地前完全一致且不含冲突标记，实际: %q", string(content))
	}
	if strings.Contains(string(content), "<<<<<<<") || strings.Contains(string(content), ">>>>>>>") {
		t.Fatalf("主工作区不应包含冲突标记: %q", string(content))
	}

	// bee 2 的 worktree 中保留冲突标记
	wt2Content, err := os.ReadFile(filepath.Join(wt2, "shared.txt"))
	if err != nil {
		t.Fatalf("read wt2 shared.txt: %v", err)
	}
	if !strings.Contains(string(wt2Content), "<<<<<<<") || !strings.Contains(string(wt2Content), ">>>>>>>") {
		t.Fatalf("bee 2 worktree should contain conflict markers, got: %q", string(wt2Content))
	}
}

// (c) ClearFinished 后 worktree 目录不存在，git worktree list 里也没有它，对应分支也被删除。
func TestManager_ClearFinished(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-clear-03"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Task 1", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	h := bees[0]
	wtPath := h.WorktreePath
	branch := h.Branch

	// 确认 worktree 目录存在
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("expected worktree path to exist before clear: %v", err)
	}

	// 结束并合并
	driver.setSessionState(h.ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "task 1 done",
	})
	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if st.Bees[0].Status != StatusCompleted {
		t.Fatalf("expected bee completed before clear")
	}

	// 执行 ClearFinished
	if err := mgr.ClearFinished(ctx, leadID); err != nil {
		t.Fatalf("ClearFinished failed: %v", err)
	}

	// 验证 1：worktree 目录不存在
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("expected worktree directory %s to be deleted, err: %v", wtPath, err)
	}

	// 验证 2：git worktree list 里没有它
	list, err := runner.WorktreeList(ctx, dir)
	if err != nil {
		t.Fatalf("WorktreeList failed: %v", err)
	}
	for _, wt := range list {
		if wt.Path == wtPath {
			t.Errorf("worktree %s still in git worktree list", wtPath)
		}
	}

	// 验证 3：对应分支已被删除
	gitBin, _ := exec.LookPath("git")
	cmdBranch := exec.CommandContext(ctx, gitBin, "branch", "--list", branch)
	cmdBranch.Dir = dir
	out, err := cmdBranch.Output()
	if err != nil {
		t.Fatalf("git branch --list failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected branch %s to be deleted, found: %s", branch, string(out))
	}

	// 验证 4：state 中的 bees 已被清空
	stAfter := mgr.State(leadID)
	if len(stAfter.Bees) != 0 {
		t.Errorf("expected 0 bees in state after ClearFinished, got %d", len(stAfter.Bees))
	}
}

// (d) ClearFinished 只清理已结束的 bee：仍在运行的 bee 及其 worktree 保留。
func TestManager_ClearFinished_KeepsRunningBees(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})

	leadID := "lead-clear-keep"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "T1", Prompt: "P1"},
			{Name: "Bee-2", Task: "T2", Prompt: "P2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "bee 1 done",
	})
	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if st.Bees[0].Status != StatusCompleted {
		t.Fatalf("expected bee 1 completed, got %s", st.Bees[0].Status)
	}
	finishedWorktree := st.Bees[0].WorktreePath
	runningWorktree := st.Bees[1].WorktreePath

	if err := mgr.ClearFinished(ctx, leadID); err != nil {
		t.Fatalf("ClearFinished failed: %v", err)
	}

	after := mgr.State(leadID)
	if len(after.Bees) != 1 {
		t.Fatalf("expected 1 bee left after ClearFinished, got %d", len(after.Bees))
	}
	if after.Bees[0].ThreadID != bees[1].ThreadID || after.Bees[0].Status != StatusRunning {
		t.Errorf("expected running bee 2 to stay, got %+v", after.Bees[0])
	}
	if _, err := os.Stat(finishedWorktree); !os.IsNotExist(err) {
		t.Errorf("expected finished worktree %s to be deleted, err: %v", finishedWorktree, err)
	}
	if _, err := os.Stat(runningWorktree); err != nil {
		t.Errorf("expected running worktree %s to be kept, err: %v", runningWorktree, err)
	}
}

// (e) StopAll 只停止 running 的 bee：已完成的不被改写，会话中断只发给 running 的 bee。
func TestManager_StopAll_SkipsFinishedBees(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	stoppedSessions := make(map[string]bool)
	var stopMu sync.Mutex

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stoppedSessions[threadID] = true
			stopMu.Unlock()
			return nil
		},
	})

	leadID := "lead-stopall-05"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "T1", Prompt: "P1"},
			{Name: "Bee-2", Task: "T2", Prompt: "P2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "bee 1 done",
	})
	mgr.Tick(ctx)

	if err := mgr.StopAll(ctx, leadID); err != nil {
		t.Fatalf("StopAll failed: %v", err)
	}

	st := mgr.State(leadID)
	if st.Bees[0].Status != StatusCompleted {
		t.Errorf("expected finished bee 1 to stay completed, got %s", st.Bees[0].Status)
	}
	if st.Bees[1].Status != StatusStopped {
		t.Errorf("expected bee 2 stopped, got %s", st.Bees[1].Status)
	}

	stopMu.Lock()
	defer stopMu.Unlock()
	if stoppedSessions[bees[0].ThreadID] {
		t.Errorf("expected no interrupt for finished bee 1")
	}
	if !stoppedSessions[bees[1].ThreadID] {
		t.Errorf("expected interrupt for running bee 2")
	}
}

// 测试 StopBee、StopAll、ErrBeeNotFound 及 ErrLeadNotFound
func TestManager_StopBee_And_Errors(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	stoppedSessions := make(map[string]bool)
	var stopMu sync.Mutex

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stoppedSessions[threadID] = true
			stopMu.Unlock()
			return nil
		},
	})

	leadID := "lead-stop-04"

	// 错误场景 1：找不到 lead 返回 ErrLeadNotFound
	if err := mgr.StopBee(ctx, "nonexistent-lead", "bee-1"); !errors.Is(err, ErrLeadNotFound) {
		t.Errorf("expected ErrLeadNotFound, got: %v", err)
	}
	if err := mgr.StopAll(ctx, "nonexistent-lead"); !errors.Is(err, ErrLeadNotFound) {
		t.Errorf("expected ErrLeadNotFound, got: %v", err)
	}

	// 派发两个 bee
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "T1", Prompt: "P1"},
			{Name: "Bee-2", Task: "T2", Prompt: "P2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// 错误场景 2：找不到 bee 返回 ErrBeeNotFound
	if err := mgr.StopBee(ctx, leadID, "nonexistent-bee"); !errors.Is(err, ErrBeeNotFound) {
		t.Errorf("expected ErrBeeNotFound, got: %v", err)
	}

	// 验证 BeeOwner
	owner, ok := mgr.BeeOwner(bees[0].ThreadID)
	if !ok || owner != leadID {
		t.Errorf("expected BeeOwner=%s, got %s (ok=%v)", leadID, owner, ok)
	}
	_, ok = mgr.BeeOwner("unknown-thread")
	if ok {
		t.Errorf("expected BeeOwner unknown to return ok=false")
	}

	// StopBee 单个 bee
	h1 := bees[0]
	if err := mgr.StopBee(ctx, leadID, h1.ThreadID); err != nil {
		t.Fatalf("StopBee failed: %v", err)
	}

	stopMu.Lock()
	if !stoppedSessions[h1.ThreadID] {
		t.Errorf("expected StopSession to be called for %s", h1.ThreadID)
	}
	stopMu.Unlock()

	st := mgr.State(leadID)
	if st.Bees[0].Status != StatusStopped {
		t.Errorf("expected Bee-1 status stopped, got %s", st.Bees[0].Status)
	}
	if st.Bees[0].FinishedAt == nil {
		t.Errorf("expected Bee-1 FinishedAt to be non-nil")
	}
	if st.Bees[0].Merged {
		t.Errorf("expected Bee-1 Merged=false")
	}

	// StopAll 停止剩余 running 的 bee
	if err := mgr.StopAll(ctx, leadID); err != nil {
		t.Fatalf("StopAll failed: %v", err)
	}
	stAll := mgr.State(leadID)
	if stAll.Bees[1].Status != StatusStopped {
		t.Errorf("expected Bee-2 status stopped, got %s", stAll.Bees[1].Status)
	}

	// 测试 SetEnabled
	enabledState := mgr.SetEnabled(leadID, false)
	if enabledState.Enabled != false {
		t.Errorf("expected Enabled=false")
	}

	// 测试 Start 和 Stop 幂等性
	mgr.Start()
	mgr.Start()
	mgr.Stop()
	mgr.Stop()
}

func TestManager_LeadThreadForWorkDir(t *testing.T) {
	mgr := NewManager(Options{})

	lead1 := "lead-thread-1"
	lead2 := "lead-thread-2"

	mgr.mu.Lock()
	mgr.leads[lead1] = &leadState{
		leadThreadID: lead1,
		bees: []Bee{
			{
				ThreadID:     "bee-1-1",
				WorktreePath: filepath.Clean("/workspace/bee-1"),
			},
		},
	}
	mgr.leads[lead2] = &leadState{
		leadThreadID: lead2,
		bees: []Bee{
			{
				ThreadID:     "bee-2-1",
				WorktreePath: filepath.Clean("/workspace/bee-2"),
			},
		},
	}
	mgr.mu.Unlock()

	// 1. 精确匹配
	if id, ok := mgr.LeadThreadForWorkDir("/workspace/bee-1"); !ok || id != lead1 {
		t.Fatalf("expected (%s, true), got (%s, %v)", lead1, id, ok)
	}

	// 2. 子目录匹配
	if id, ok := mgr.LeadThreadForWorkDir(filepath.Join("/workspace/bee-1", "subdir", "nested")); !ok || id != lead1 {
		t.Fatalf("expected (%s, true) for subdir, got (%s, %v)", lead1, id, ok)
	}

	// 3. 类似前缀但不是子目录（无分隔符），应当不匹配
	if id, ok := mgr.LeadThreadForWorkDir("/workspace/bee-1-suffix"); ok {
		t.Fatalf("expected false for similar prefix without sep, got (%s, %v)", id, ok)
	}

	// 4. 第二个 lead 精确匹配与子目录匹配
	if id, ok := mgr.LeadThreadForWorkDir("/workspace/bee-2"); !ok || id != lead2 {
		t.Fatalf("expected (%s, true), got (%s, %v)", lead2, id, ok)
	}
	if id, ok := mgr.LeadThreadForWorkDir(filepath.Join("/workspace/bee-2", "file.go")); !ok || id != lead2 {
		t.Fatalf("expected (%s, true), got (%s, %v)", lead2, id, ok)
	}

	// 5. 空路径和不存在的路径
	if _, ok := mgr.LeadThreadForWorkDir(""); ok {
		t.Fatalf("expected false for empty path")
	}
	if _, ok := mgr.LeadThreadForWorkDir("/other/path"); ok {
		t.Fatalf("expected false for other path")
	}
}

type fakePersister struct {
	mu    sync.Mutex
	leads map[string]LeadRecord
}

func newFakePersister() *fakePersister {
	return &fakePersister{
		leads: make(map[string]LeadRecord),
	}
}

func (p *fakePersister) SaveLead(ctx context.Context, rec LeadRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leads[rec.LeadThreadID] = rec
	return nil
}

func (p *fakePersister) LoadLeads(ctx context.Context) ([]LeadRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	res := make([]LeadRecord, 0, len(p.leads))
	for _, rec := range p.leads {
		res = append(res, rec)
	}
	return res, nil
}

// 验证 DispatchFromReply 派发后，bee 结束时 Tick 只调用一次 OnReport，消息以 ReportPrefix 开头且包含 bee 名
func TestManager_DispatchFromReply_OnReport(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	var reports []string
	var reportMu sync.Mutex
	persister := newFakePersister()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		Persister:   persister,
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})

	leadID := "lead-reply-report"
	reply := "Here is the delegation plan:\n```swarm\n" + `[
  {
    "bee": "Ives",
    "task": "Fix parser",
    "prompt": "Please fix the parser bug"
  }
]
` + "```\nEnd of plan."

	bees, notices, err := mgr.DispatchFromReply(ctx, ReplyDispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		ReplyText:    reply,
	})
	if err != nil {
		t.Fatalf("DispatchFromReply failed: %v", err)
	}
	if len(notices) != 0 {
		t.Errorf("expected 0 notices, got %v", notices)
	}
	if len(bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(bees))
	}
	// "Ives" 未知名字，回落到第一个顺序名字 "Aspen"
	if bees[0].Name != "Aspen" {
		t.Errorf("expected bee name 'Aspen', got %s", bees[0].Name)
	}

	// 模拟该 bee 完成
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "parser fixed successfully",
	})

	// 第一次 Tick：bee 结束并自动合并，应触发 OnReport
	mgr.Tick(ctx)

	reportMu.Lock()
	count1 := len(reports)
	var firstReport string
	if count1 > 0 {
		firstReport = reports[0]
	}
	reportMu.Unlock()

	if count1 != 1 {
		t.Fatalf("expected OnReport to be called once, got %d times", count1)
	}
	if !strings.HasPrefix(firstReport, ReportPrefix) {
		t.Errorf("expected report message to start with %q, got: %s", ReportPrefix, firstReport)
	}
	if !strings.Contains(firstReport, "Aspen") {
		t.Errorf("expected report message to contain bee name 'Aspen', got: %s", firstReport)
	}

	// 第二次 Tick：没有新结束的 bee，不应再次调用 OnReport
	mgr.Tick(ctx)

	reportMu.Lock()
	count2 := len(reports)
	reportMu.Unlock()

	if count2 != 1 {
		t.Errorf("expected OnReport not to be called again, count: %d", count2)
	}
}

// 验证 Restore 会把 running 改为 stopped，更新 Summary 并恢复 nextIndex
func TestManager_Restore_StopsRunningAndRestoresNextIndex(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	persister := newFakePersister()

	leadID := "lead-restore-test"
	initialLead := LeadRecord{
		LeadThreadID: leadID,
		Enabled:      true,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		NextIndex:    17,
		Bees: []Bee{
			{
				ThreadID:     "bee-running-1",
				Index:        0,
				Name:         "Aspen",
				Task:         "Running task",
				Status:       StatusRunning,
				Summary:      "was running",
				WorktreePath: filepath.Join(dir, "wt-0"),
				Branch:       "mcode/swarm/lead-res/0",
				BaseCommit:   "abc",
				Reported:     false,
			},
			{
				ThreadID:     "bee-done-2",
				Index:        1,
				Name:         "Birch",
				Task:         "Done task",
				Status:       StatusCompleted,
				Summary:      "already completed",
				WorktreePath: filepath.Join(dir, "wt-1"),
				Branch:       "mcode/swarm/lead-res/1",
				BaseCommit:   "abc",
				Reported:     true,
			},
		},
	}
	if err := persister.SaveLead(ctx, initialLead); err != nil {
		t.Fatalf("SaveLead failed: %v", err)
	}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})

	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	st := mgr.State(leadID)
	if len(st.Bees) != 2 {
		t.Fatalf("expected 2 bees restored, got %d", len(st.Bees))
	}

	// 验证 running 变为 stopped，FinishedAt != nil，Summary 前面包含中断提示，Reported 为 true
	h0 := st.Bees[0]
	if h0.Status != StatusStopped {
		t.Errorf("expected bee 0 status stopped, got %s", h0.Status)
	}
	if h0.FinishedAt == nil {
		t.Errorf("expected bee 0 FinishedAt to be non-nil")
	}
	if !strings.HasPrefix(h0.Summary, "MCode 重启，子代理已中断") {
		t.Errorf("expected bee 0 Summary prefix 'MCode 重启，子代理已中断', got: %s", h0.Summary)
	}
	if h0.Reported {
		t.Errorf("interrupted bee must still be reported to its lead")
	}

	// 验证已完成的 bee 状态未变
	h1 := st.Bees[1]
	if h1.Status != StatusCompleted {
		t.Errorf("expected bee 1 status completed, got %s", h1.Status)
	}

	// 验证 nextIndex 被恢复：新派发的 bee 从 17 开始
	newBees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Next-Bee", Task: "T3", Prompt: "P3"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(newBees) != 1 {
		t.Fatalf("expected 1 new bee, got %d", len(newBees))
	}
	if newBees[0].Index != 17 {
		t.Errorf("expected new bee Index=17, got %d", newBees[0].Index)
	}

	// 验证已落盘更新
	persistedLead := persister.leads[leadID]
	if persistedLead.Bees[0].Status != StatusStopped {
		t.Errorf("expected persisted bee 0 to be stopped, got %s", persistedLead.Bees[0].Status)
	}
}

// 验证 DispatchFromReply 中 "dune"、"Ember 2" 精确采用名字与项目解析
func TestManager_DispatchFromReply_NameAndProject(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		ResolveProject: func(ctx context.Context, ref string) (projectID, workDir string, ok bool) {
			if ref == "known-proj" {
				return "proj-known-id", dir, true
			}
			return "", "", false
		},
	})

	leadID := "lead-reply-names"
	reply := "```swarm\n" + `[
  {
    "bee": "dune",
    "prompt": "Prompt 1",
    "project": "known-proj"
  },
  {
    "bee": "Ember 2",
    "prompt": "Prompt 2",
    "repo": "unknown-repo"
  }
]
` + "```"

	bees, notices, err := mgr.DispatchFromReply(ctx, ReplyDispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-lead",
		LeadWorkDir:  dir,
		ReplyText:    reply,
	})
	if err != nil {
		t.Fatalf("DispatchFromReply failed: %v", err)
	}
	if len(bees) != 2 {
		t.Fatalf("expected 2 bees, got %d", len(bees))
	}

	// "dune" -> "Dune"
	if bees[0].Name != "Dune" {
		t.Errorf("expected 'Dune', got %s", bees[0].Name)
	}
	if bees[0].ProjectID != "proj-known-id" {
		t.Errorf("expected ProjectID 'proj-known-id', got %s", bees[0].ProjectID)
	}
	if bees[0].TargetWorkDir != dir {
		t.Errorf("expected TargetWorkDir %s, got %s", dir, bees[0].TargetWorkDir)
	}

	// 名字的显示轮次不参与派发。
	if bees[1].Name != "Ember" {
		t.Errorf("expected 'Ember', got %s", bees[1].Name)
	}
	if bees[1].ProjectID != "proj-lead" {
		t.Errorf("expected ProjectID 'proj-lead', got %s", bees[1].ProjectID)
	}

	// notices 中应记录未找到的项目
	if len(notices) != 1 {
		t.Fatalf("expected 1 notice, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "unknown-repo") {
		t.Errorf("expected notice to mention 'unknown-repo', got: %s", notices[0])
	}
}

// 验证 BuildReport 在有 bee 和无 bee 两种情况下的输出都以 ReportPrefix 开头
func TestBuildReport_Prefix(t *testing.T) {
	// 1. 无 bee
	emptyReport := BuildReport(nil)
	if !strings.HasPrefix(emptyReport, ReportPrefix) {
		t.Fatalf("expected empty report to start with %q, got: %q", ReportPrefix, emptyReport)
	}

	// 2. 有 bee
	bees := []Bee{
		{
			Name:    "Ives",
			Task:    "Fix parser",
			Status:  StatusCompleted,
			Merged:  true,
			Summary: "Parser fixed.",
		},
	}
	report := BuildReport(bees)
	if !strings.HasPrefix(report, ReportPrefix) {
		t.Fatalf("expected report to start with %q, got: %q", ReportPrefix, report)
	}
}

// 验证 BuildReport 的「统计」行：含用时、工具调用与增删行数；全零统计时不输出该行。
func TestBuildReportIncludesStats(t *testing.T) {
	started := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(33 * time.Second)
	bee := Bee{
		Name:         "Bee-Stats",
		Task:         "统计改动",
		Status:       StatusCompleted,
		StartedAt:    started,
		FinishedAt:   &finished,
		ToolCalls:    9,
		LinesAdded:   3,
		LinesDeleted: 3,
	}
	report := BuildReport([]Bee{bee})
	want := "- 统计：{\"durationMs\":33000,\"toolCalls\":9,\"linesAdded\":3,\"linesDeleted\":3}"
	if !strings.Contains(report, want) {
		t.Fatalf("汇报中缺少统计行 %q:\n%s", want, report)
	}

	empty := Bee{Name: "Bee-NoStats", Task: "无统计", Status: StatusCompleted}
	if emptyReport := BuildReport([]Bee{empty}); strings.Contains(emptyReport, "- 统计：") {
		t.Fatalf("全零统计不应输出统计行:\n%s", emptyReport)
	}
}

// (a & b) human 审查档：bee 完成后 Tick → Status=completed、PendingMerge=true、ChangedFiles 含改动文件、目标仓库文件未改变；
// 审查未出结论前不汇报。接着 ApproveMerge → Merged=true、PendingMerge=false、目标仓库文件内容已变；
// 再 Tick 一次汇报一次且含「已合入（审查通过）」。
func TestManager_HumanReview_Approve(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	var (
		reportMu sync.Mutex
		reports  []string
	)

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})
	humanReviewSettings(mgr)

	leadID := "lead-review-approve"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Create foo.txt", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	st := mgr.State(leadID)
	wt1 := st.Bees[0].WorktreePath

	// 主会话自身空闲，确保不会被 leadTurnRunning 跳过。
	driver.setSessionState(leadID, kanban.SessionState{Running: false})

	// 在 worktree 中写入改动
	fooFileInWt := filepath.Join(wt1, "foo.txt")
	if err := os.WriteFile(fooFileInWt, []byte("hello from bee 1\n"), 0644); err != nil {
		t.Fatalf("write foo.txt in wt: %v", err)
	}

	// 让 bee 完成
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "foo created",
	})

	// 第一次 Tick
	mgr.Tick(ctx)

	st1 := mgr.State(leadID)
	h1 := st1.Bees[0]
	if h1.Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted, got %s", h1.Status)
	}
	if !h1.PendingMerge {
		t.Fatalf("expected PendingMerge=true, got false")
	}
	if len(h1.ChangedFiles) != 1 || h1.ChangedFiles[0] != "foo.txt" {
		t.Fatalf("expected ChangedFiles [foo.txt], got %v", h1.ChangedFiles)
	}

	// 目标仓库文件未改变（不存在 foo.txt）
	targetFoo := filepath.Join(dir, "foo.txt")
	if _, err := os.Stat(targetFoo); !os.IsNotExist(err) {
		t.Fatalf("expected target foo.txt not to exist yet, err: %v", err)
	}

	// 审查未出结论前不汇报：整批等审查结果，避免局部汇报抢跑。
	reportMu.Lock()
	reportsBeforeApprove := append([]string(nil), reports...)
	reportMu.Unlock()
	if len(reportsBeforeApprove) != 0 {
		t.Fatalf("expected no report while awaiting review, got %d", len(reportsBeforeApprove))
	}

	// MergeDiff 返回含该文件路径的补丁
	patch, err := mgr.MergeDiff(ctx, leadID, h1.ThreadID)
	if err != nil {
		t.Fatalf("MergeDiff failed: %v", err)
	}
	if !strings.Contains(string(patch), "foo.txt") {
		t.Fatalf("expected patch to contain 'foo.txt', got: %s", string(patch))
	}

	// 接着 ApproveMerge → Merged=true、PendingMerge=false、目标仓库文件内容已变
	approvedBee, err := mgr.ApproveMerge(ctx, leadID, h1.ThreadID)
	if err != nil {
		t.Fatalf("ApproveMerge failed: %v", err)
	}
	if !approvedBee.Merged {
		t.Fatalf("expected approvedBee.Merged=true, got false")
	}
	if approvedBee.PendingMerge {
		t.Fatalf("expected approvedBee.PendingMerge=false, got true")
	}

	// 目标仓库文件内容已变
	content, err := os.ReadFile(targetFoo)
	if err != nil {
		t.Fatalf("read target foo.txt: %v", err)
	}
	if string(content) != "hello from bee 1\n" {
		t.Fatalf("unexpected content in target foo.txt: %s", string(content))
	}

	// 再 Tick 一次后 OnReport 被调用
	mgr.Tick(ctx)

	reportMu.Lock()
	numReports2 := len(reports)
	var finalReport string
	if numReports2 > 0 {
		finalReport = reports[0]
	}
	reportMu.Unlock()

	if numReports2 != 1 {
		t.Fatalf("expected OnReport to be called once after approval and Tick, got %d", numReports2)
	}
	if !strings.Contains(finalReport, "Bee-1") {
		t.Errorf("expected report to contain Bee-1, got: %s", finalReport)
	}
	if !strings.Contains(finalReport, "已合入（审查通过）") {
		t.Errorf("expected report to mention review approval, got: %s", finalReport)
	}
}

// (c) DiscardMerge → MergeDiscarded=true、目标仓库未改变；待审查期间不汇报，放弃后下一个 Tick 汇报一次且含「已放弃合并」。
func TestManager_HumanReview_Discard(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	var (
		reportMu sync.Mutex
		reports  []string
	)

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})
	humanReviewSettings(mgr)

	leadID := "lead-review-discard"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Discard", Task: "Create discard.txt", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	st := mgr.State(leadID)
	wt1 := st.Bees[0].WorktreePath

	// 主会话自身空闲，确保不会被 leadTurnRunning 跳过。
	driver.setSessionState(leadID, kanban.SessionState{Running: false})

	discardFileInWt := filepath.Join(wt1, "discard.txt")
	if err := os.WriteFile(discardFileInWt, []byte("discard content\n"), 0644); err != nil {
		t.Fatalf("write discard.txt in wt: %v", err)
	}

	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "discard task done",
	})

	mgr.Tick(ctx)

	st1 := mgr.State(leadID)
	if !st1.Bees[0].PendingMerge {
		t.Fatalf("expected PendingMerge=true")
	}
	if st1.Bees[0].Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted, got %s", st1.Bees[0].Status)
	}
	// 审查未出结论前不汇报。
	reportMu.Lock()
	pendingReports := append([]string(nil), reports...)
	reportMu.Unlock()
	if len(pendingReports) != 0 {
		t.Fatalf("expected no report while awaiting review, got %d", len(pendingReports))
	}

	// 调用 DiscardMerge
	if err := mgr.DiscardMerge(ctx, leadID, bees[0].ThreadID); err != nil {
		t.Fatalf("DiscardMerge failed: %v", err)
	}

	st2 := mgr.State(leadID)
	h := st2.Bees[0]
	if !h.MergeDiscarded {
		t.Errorf("expected MergeDiscarded=true")
	}
	if h.PendingMerge {
		t.Errorf("expected PendingMerge=false")
	}
	if h.Merged {
		t.Errorf("expected Merged=false")
	}

	// 目标仓库未改变
	targetDiscard := filepath.Join(dir, "discard.txt")
	if _, err := os.Stat(targetDiscard); !os.IsNotExist(err) {
		t.Errorf("expected target discard.txt not to exist, err: %v", err)
	}

	// 再 Tick 一次
	mgr.Tick(ctx)

	reportMu.Lock()
	numReports := len(reports)
	var reportMsg string
	if numReports > 0 {
		reportMsg = reports[0]
	}
	reportMu.Unlock()

	if numReports != 1 {
		t.Fatalf("expected OnReport to be called once, got %d", numReports)
	}
	if !strings.Contains(reportMsg, "Bee-Discard") {
		t.Errorf("expected report to contain 'Bee-Discard', got: %s", reportMsg)
	}
	if !strings.Contains(reportMsg, "已放弃合并") {
		t.Errorf("expected report to mention discard, got: %s", reportMsg)
	}
}

// (d) 对非待审查 bee 调用 ApproveMerge/DiscardMerge 返回 ErrNoPendingMerge（errors.Is）。
func TestManager_HumanReview_ErrNoPendingMerge(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})

	leadID := "lead-no-pending"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Task 1", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// 当前 bee 处于 running，非 PendingMerge
	_, errApprove := mgr.ApproveMerge(ctx, leadID, bees[0].ThreadID)
	if !errors.Is(errApprove, ErrNoPendingMerge) {
		t.Errorf("expected ErrNoPendingMerge for running bee on ApproveMerge, got: %v", errApprove)
	}

	errDiscard := mgr.DiscardMerge(ctx, leadID, bees[0].ThreadID)
	if !errors.Is(errDiscard, ErrNoPendingMerge) {
		t.Errorf("expected ErrNoPendingMerge for running bee on DiscardMerge, got: %v", errDiscard)
	}
}

// (e) ClearFinished 不移除 PendingMerge 的 bee。
func TestManager_HumanReview_ClearFinished_PreservesPending(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})
	humanReviewSettings(mgr)

	leadID := "lead-clear-preserve"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Task 1", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	st := mgr.State(leadID)
	wtPath := st.Bees[0].WorktreePath

	// 写入改动
	if err := os.WriteFile(filepath.Join(wtPath, "keep.txt"), []byte("keep content\n"), 0644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}

	// 结束 bee
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "done",
	})
	mgr.Tick(ctx)

	st1 := mgr.State(leadID)
	if !st1.Bees[0].PendingMerge {
		t.Fatalf("expected PendingMerge=true")
	}

	// 调用 ClearFinished
	if err := mgr.ClearFinished(ctx, leadID); err != nil {
		t.Fatalf("ClearFinished failed: %v", err)
	}

	// 验证 bee 仍存在
	st2 := mgr.State(leadID)
	if len(st2.Bees) != 1 {
		t.Fatalf("expected 1 bee remaining, got %d", len(st2.Bees))
	}
	if !st2.Bees[0].PendingMerge {
		t.Errorf("expected PendingMerge to still be true")
	}

	// 验证 worktree 依然存在
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("expected worktree to still exist, err: %v", err)
	}
}

// (f) 冲突用例：目标仓库在 bee 完成后修改同一行，ApproveMerge 后 ConflictFiles 非空、不置 Merged。
func TestManager_HumanReview_Conflict(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	// 在主分支准备初始文件并提交
	sharedFile := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(sharedFile, []byte("line 1\nline 2 original\nline 3\n"), 0644); err != nil {
		t.Fatalf("write shared.txt: %v", err)
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	cmdAdd := exec.CommandContext(ctx, gitBin, "add", "shared.txt")
	cmdAdd.Dir = dir
	_ = cmdAdd.Run()

	cmdCommit := exec.CommandContext(ctx, gitBin, "commit", "-m", "add shared.txt")
	cmdCommit.Dir = dir
	cmdCommit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test-user",
		"GIT_AUTHOR_EMAIL=test@localhost",
		"GIT_COMMITTER_NAME=test-user",
		"GIT_COMMITTER_EMAIL=test@localhost",
	)
	_ = cmdCommit.Run()

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})
	humanReviewSettings(mgr)

	leadID := "lead-review-conflict"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "Edit line 2 bee 1", Prompt: "P1"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	st := mgr.State(leadID)
	wt1 := st.Bees[0].WorktreePath

	// Bee 1 修改第 2 行
	if err := os.WriteFile(filepath.Join(wt1, "shared.txt"), []byte("line 1\nline 2 from bee 1\nline 3\n"), 0644); err != nil {
		t.Fatalf("write shared.txt in wt1: %v", err)
	}

	// 让 bee 完成
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 1 done",
	})
	mgr.Tick(ctx)

	st1 := mgr.State(leadID)
	if !st1.Bees[0].PendingMerge {
		t.Fatalf("expected PendingMerge=true")
	}

	// 目标仓库在 bee 完成后修改同一行
	if err := os.WriteFile(sharedFile, []byte("line 1\nline 2 from target dir\nline 3\n"), 0644); err != nil {
		t.Fatalf("write shared.txt in dir: %v", err)
	}
	// 提交目标侧改动，模拟主工作区在 bee 完成后已有冲突的提交
	gitTestRun(t, dir, "commit", "-am", "target edits line 2")

	// 调用 ApproveMerge：对象层整合发现冲突，返回错误并保持待审查，主工作区不被改动。
	approvedBee, err := mgr.ApproveMerge(ctx, leadID, bees[0].ThreadID)
	if err == nil {
		t.Fatalf("冲突时应返回错误")
	}
	if approvedBee.Merged {
		t.Fatalf("expected Merged=false on conflict")
	}
	if !strings.Contains(approvedBee.MergeError, "与主工作区最新内容冲突") {
		t.Errorf("应记录冲突原因，实际: %q", approvedBee.MergeError)
	}
	if len(approvedBee.ConflictFiles) == 0 {
		t.Fatalf("expected ConflictFiles to be non-empty, got %v", approvedBee.ConflictFiles)
	}
	if approvedBee.ConflictFiles[0] != "shared.txt" {
		t.Fatalf("expected ConflictFiles to contain 'shared.txt', got %v", approvedBee.ConflictFiles)
	}
	if !approvedBee.PendingMerge {
		t.Fatalf("expected PendingMerge=true")
	}
	content, readErr := os.ReadFile(sharedFile)
	if readErr != nil {
		t.Fatalf("read shared.txt: %v", readErr)
	}
	if string(content) != "line 1\nline 2 from target dir\nline 3\n" {
		t.Errorf("主工作区不应被改动: %q", string(content))
	}
	if strings.Contains(string(content), "<<<<<<<") {
		t.Errorf("主工作区不应被改动: %q", string(content))
	}
}

// 补齐单元测试 1：MaxBees 排队测试。并发上限为 1 时，超出上限的 bee 进入排队，前序 bee 完成后自动出队启动。
func TestManager_MaxBeesQueue(t *testing.T) {
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
			Enabled:       true,
			AutoDelegate:  true,
			MaxBees:       1, // 限制最大并发数为 1
			WorkspaceMode: "worktree",
		}
	})

	leadID := "lead-queue-test"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-queue",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "task 1", Prompt: "p1"},
			{Name: "Bee-2", Task: "task 2", Prompt: "p2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// 此时应只有 1 个 bee 立即启动
	if len(bees) != 1 {
		t.Fatalf("expected 1 bee launched immediately, got %d", len(bees))
	}
	if mgr.QueueLength() != 1 {
		t.Fatalf("expected 1 bee queued, got %d", mgr.QueueLength())
	}

	st := mgr.State(leadID)
	if len(st.Bees) != 1 || st.Bees[0].Status != StatusRunning {
		t.Fatalf("expected 1 running bee in state, got %+v", st.Bees)
	}

	// 完成第一个 bee
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 1 done",
	})
	mgr.Tick(ctx)

	// 第一个完成后，第二个 bee 应当自动出队并启动
	if mgr.QueueLength() != 0 {
		t.Fatalf("expected queue length to be 0 after tick, got %d", mgr.QueueLength())
	}
	st2 := mgr.State(leadID)
	if len(st2.Bees) != 2 {
		t.Fatalf("expected 2 bees in state now, got %d", len(st2.Bees))
	}
	if st2.Bees[0].Status != StatusCompleted {
		t.Errorf("bee 0 should be completed, got %v", st2.Bees[0].Status)
	}
	if st2.Bees[1].Status != StatusRunning {
		t.Errorf("bee 1 should be running, got %v", st2.Bees[1].Status)
	}

	// 完成第二个 bee
	driver.setSessionState(st2.Bees[1].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "bee 2 done",
	})
	mgr.Tick(ctx)

	st3 := mgr.State(leadID)
	if st3.Bees[1].Status != StatusCompleted {
		t.Errorf("bee 1 should be completed, got %v", st3.Bees[1].Status)
	}
}

// 补齐单元测试 2：WorkspaceMode=shared 共享主工作区测试。不建 worktree，直接在主工作区执行且不需要合并。
func TestManager_WorkspaceModeShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()

	var startedWorkDir string
	driver.onStart = func(req kanban.SessionRequest) {
		startedWorkDir = req.WorkDir
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
			WorkspaceMode: "shared", // 共享主工作区模式
		}
	})

	leadID := "lead-shared-test"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-shared",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Shared", Task: "task shared", Prompt: "pshared"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(bees))
	}

	// 验证 WorktreePath 为空且启动目录为主工作区目录
	if bees[0].WorktreePath != "" {
		t.Errorf("expected empty WorktreePath in shared mode, got %q", bees[0].WorktreePath)
	}
	if startedWorkDir != dir {
		t.Errorf("expected session workdir to be %q, got %q", dir, startedWorkDir)
	}

	// 直接在主工作区写文件模拟改动
	if err := os.WriteFile(filepath.Join(dir, "shared_output.txt"), []byte("shared content\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// 触发 bee 完成
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "shared work completed",
	})
	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if st.Bees[0].Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted, got %v", st.Bees[0].Status)
	}
	if !st.Bees[0].Merged {
		t.Errorf("expected Merged=true in shared mode")
	}
	if st.Bees[0].PendingMerge {
		t.Errorf("expected PendingMerge=false in shared mode")
	}
}

// 补齐单元测试 3：RetryFailed 失败自动重试测试。bee 失败后先续接原会话；续接之后再失败仍可换新 bee 重试一次。
// 续接不占用新 bee 的重试额度，所以「续接一次 + 新 bee 一次」都能走到。
func TestManager_RetryFailed(t *testing.T) {
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
			Enabled:       true,
			AutoDelegate:  true,
			RetryFailed:   true, // 开启失败自动重试
			WorkspaceMode: "worktree",
		}
	})

	leadID := "lead-retry-test"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-retry",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Retry", Task: "task retry", Prompt: "pretry"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	firstThreadID := bees[0].ThreadID

	// 模拟首次执行失败
	driver.setSessionState(firstThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "transient network failure",
	})

	// 执行 Tick
	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(st.Bees))
	}
	// 首次失败只发一条「中断续接」催促：原会话继续跑，ThreadID 不变，状态回到运行中
	if st.Bees[0].Status != StatusRunning {
		t.Fatalf("expected bee to stay StatusRunning after continue nudge, got %v", st.Bees[0].Status)
	}
	if st.Bees[0].ThreadID != firstThreadID {
		t.Fatalf("expected continue nudge to reuse ThreadID %s, got %s", firstThreadID, st.Bees[0].ThreadID)
	}

	driver.mu.Lock()
	followUps := append([]fakeFollowUp(nil), driver.followUps...)
	driver.mu.Unlock()
	if len(followUps) != 1 {
		t.Fatalf("expected 1 followUp sent, got %d", len(followUps))
	}
	if followUps[0].threadID != firstThreadID {
		t.Errorf("expected followUp on original thread %s, got %s", firstThreadID, followUps[0].threadID)
	}
	if !strings.Contains(followUps[0].text, "继续") {
		t.Errorf("expected followUp text to ask for continuation, got %q", followUps[0].text)
	}

	// 模拟续接后的原会话再次失败：续接只用一次，但没占用重试额度，应换上一个新 bee
	driver.setSessionState(firstThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "second failure",
	})

	mgr.Tick(ctx)

	st2 := mgr.State(leadID)
	if st2.Bees[0].Status != StatusRunning {
		t.Fatalf("expected StatusRunning after spawning new bee, got %v", st2.Bees[0].Status)
	}
	secondThreadID := st2.Bees[0].ThreadID
	if secondThreadID == firstThreadID {
		t.Fatalf("expected new ThreadID after second failure, but got same %s", firstThreadID)
	}

	// 模拟新 bee 执行成功
	driver.setSessionState(secondThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "success on retry",
	})

	mgr.Tick(ctx)

	st3 := mgr.State(leadID)
	if st3.Bees[0].Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted after retry succeeded, got %v", st3.Bees[0].Status)
	}
	if st3.Bees[0].Summary != "success on retry" {
		t.Errorf("expected summary 'success on retry', got %q", st3.Bees[0].Summary)
	}
}

// TestManager_RetryFailed_NoThirdAttempt：续接与新 bee 各只有一次机会，之后直接失败，不会无限重试。
func TestManager_RetryFailed_NoThirdAttempt(t *testing.T) {
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
			Enabled:       true,
			AutoDelegate:  true,
			RetryFailed:   true,
			WorkspaceMode: "worktree",
		}
	})

	leadID := "lead-retry-no-third"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-retry-no-third",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Retry-NoThird", Task: "task no third", Prompt: "pnothird"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	firstThreadID := bees[0].ThreadID

	// 首次失败：续接原会话
	driver.setSessionState(firstThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "first failure",
	})
	mgr.Tick(ctx)
	if st := mgr.State(leadID); st.Bees[0].Status != StatusRunning || st.Bees[0].ThreadID != firstThreadID {
		t.Fatalf("expected continue nudge to keep ThreadID %s, got %v/%s", firstThreadID, st.Bees[0].Status, st.Bees[0].ThreadID)
	}

	// 续接后再次失败：换上第一个新 bee
	driver.setSessionState(firstThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "second failure",
	})
	mgr.Tick(ctx)
	st := mgr.State(leadID)
	secondThreadID := st.Bees[0].ThreadID
	if st.Bees[0].Status != StatusRunning || secondThreadID == firstThreadID {
		t.Fatalf("expected new bee after second failure, got %v/%s", st.Bees[0].Status, secondThreadID)
	}

	// 新 bee 没有续接记录，会先被续接一次；之后不再有新会话，直接失败收尾
	for i := 0; i < 3; i++ {
		current := mgr.State(leadID).Bees[0].ThreadID
		driver.setSessionState(current, kanban.SessionState{
			Running:       false,
			Failed:        true,
			FailureReason: "repeated failure",
		})
		mgr.Tick(ctx)
	}

	if st := mgr.State(leadID); st.Bees[0].Status != StatusFailed {
		t.Fatalf("expected StatusFailed after exhausting retries, got %v", st.Bees[0].Status)
	}

	driver.mu.Lock()
	sessionCount := driver.nextID
	driver.mu.Unlock()
	if sessionCount > 2 {
		t.Errorf("expected at most 2 sessions, got %d", sessionCount)
	}
}

// TestManager_RetryFailed_FollowUpErrorFallsBackToNewBee：续接催促发不出去时，退化为新开一个 bee 重试。
func TestManager_RetryFailed_FollowUpErrorFallsBackToNewBee(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	// 让「中断续接」发送失败，模拟原会话已经关闭
	driver.onFollowUp = func(threadID, text string) error {
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
			RetryFailed:   true, // 开启失败自动重试
			WorkspaceMode: "worktree",
		}
	})

	leadID := "lead-retry-followup-error"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-retry-followup-error",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Retry-Fallback", Task: "task fallback", Prompt: "pfallback"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	firstThreadID := bees[0].ThreadID

	// 模拟首次执行失败
	driver.setSessionState(firstThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "transient network failure",
	})

	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(st.Bees))
	}
	// 续接催促失败，应该直接替换成新 bee，状态为 StatusRunning
	if st.Bees[0].Status != StatusRunning {
		t.Fatalf("expected bee to be running (new bee retry), got %v", st.Bees[0].Status)
	}
	secondThreadID := st.Bees[0].ThreadID
	if secondThreadID == firstThreadID {
		t.Fatalf("expected new session ThreadID after followUp error, but got same %s", firstThreadID)
	}

	// 模拟新 bee 执行成功
	driver.setSessionState(secondThreadID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "success after fallback",
	})

	mgr.Tick(ctx)

	st2 := mgr.State(leadID)
	if st2.Bees[0].Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted after fallback retry succeeded, got %v", st2.Bees[0].Status)
	}
	if st2.Bees[0].Summary != "success after fallback" {
		t.Errorf("expected summary 'success after fallback', got %q", st2.Bees[0].Summary)
	}
}

type fakePersisterWithRelation struct {
	mu        sync.Mutex
	leads     []LeadRecord
	relations map[string]fakeRelation
}

func (p *fakePersisterWithRelation) SaveLead(ctx context.Context, rec LeadRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leads = append(p.leads, rec)
	return nil
}

func (p *fakePersisterWithRelation) LoadLeads(ctx context.Context) ([]LeadRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leads, nil
}

func (p *fakePersisterWithRelation) SetThreadRelation(ctx context.Context, threadID, parentThreadID string, isHelper bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.relations == nil {
		p.relations = make(map[string]fakeRelation)
	}
	p.relations[threadID] = fakeRelation{parentThreadID: parentThreadID, isHelper: isHelper}
	return nil
}

// 验证派发 bee 时写入 ParentThreadID 与 IsHelper=true
func TestManager_BeeParentThreadRelation(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	persister := &fakePersisterWithRelation{}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		Persister:   persister,
		WorktreeDir: t.TempDir(),
	})
	mgr.Start()
	defer mgr.Stop()

	leadID := "lead-relation-test-01"
	bees, err := mgr.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "p1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Rel", Task: "test relation", Prompt: "p"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(bees))
	}
	thID := bees[0].ThreadID

	// 验证 driver 收到了关联
	driver.mu.Lock()
	rel, ok := driver.relations[thID]
	driver.mu.Unlock()
	if !ok {
		t.Fatalf("expected driver to receive thread relation for %s", thID)
	}
	if rel.parentThreadID != leadID || !rel.isHelper {
		t.Errorf("driver relation = %+v, want parent=%s, isHelper=true", rel, leadID)
	}

	// 验证 persister 收到了关联
	persister.mu.Lock()
	pRel, pOk := persister.relations[thID]
	persister.mu.Unlock()
	if !pOk {
		t.Fatalf("expected persister to receive thread relation for %s", thID)
	}
	if pRel.parentThreadID != leadID || !pRel.isHelper {
		t.Errorf("persister relation = %+v, want parent=%s, isHelper=true", pRel, leadID)
	}
}

// 验证 LastAssistantText 为空时先追问，仍为空则标记失败
func TestManager_EmptyAssistantNudgeAndFallback(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.Start()
	defer mgr.Stop()

	ctx := context.Background()
	leadID := "lead-empty-nudge-01"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "p1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Empty", Task: "test empty", Prompt: "p"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	thID := bees[0].ThreadID

	// 1. 模拟子会话非 running 且未失败，但 LastAssistantText 为空
	driver.setSessionState(thID, kanban.SessionState{
		Running:           false,
		Failed:            false,
		LastAssistantText: "",
	})

	// 第一次 Tick：应触发追问，保持 Running，不按完成处理
	mgr.Tick(ctx)

	driver.mu.Lock()
	followUpCount := len(driver.followUps)
	driver.mu.Unlock()
	if followUpCount != 1 {
		t.Fatalf("expected 1 followUp sent, got %d", followUpCount)
	}

	st1 := mgr.State(leadID)
	if st1.Bees[0].Status != StatusRunning {
		t.Fatalf("expected bee to remain StatusRunning after first empty nudge, got %v", st1.Bees[0].Status)
	}

	// 2. 第二次 Tick：追问后仍为空，应标记为 StatusFailed，Summary 为 '子代理未输出最终汇报'
	mgr.Tick(ctx)

	st2 := mgr.State(leadID)
	if st2.Bees[0].Status != StatusFailed {
		t.Fatalf("expected StatusFailed after repeated empty response, got %v", st2.Bees[0].Status)
	}
	if st2.Bees[0].Summary != "子代理未输出最终汇报" {
		t.Errorf("expected summary '子代理未输出最终汇报', got %q", st2.Bees[0].Summary)
	}
}

// TestIsNoticeAndBeePrompt 验证新旧两种通知与子代理任务前缀都能识别。
func TestIsNoticeAndBeePrompt(t *testing.T) {
	for _, text := range []string{ReportPrefix + "Ives 已结束工作。", NoticePrefix + "已派出 Ives", "[Hydra] 协作者汇报：Ives 已结束工作。"} {
		if !IsNotice(text) {
			t.Errorf("IsNotice(%q) = false", text)
		}
	}
	if IsNotice("普通消息 [编排]") {
		t.Error("前缀不在开头不应识别为通知")
	}
	for _, text := range []string{BeePromptPrefix + "Ives，由主代理派发", "你是 Hydra head Ives，在独立工作副本中"} {
		if !IsBeePrompt(text) {
			t.Errorf("IsBeePrompt(%q) = false", text)
		}
	}
}

// 主会话自己还在执行一轮回复时不能投递汇报：投递方只会把汇报排进主会话的跟进队列并返回成功，
// 而队列只在「正常结束且没有进行中 bee」的那一轮末尾派发，那一轮早已结束，汇报就会永久滞留，
// bee 却已被标记为已汇报而不再重发。汇报应等主会话空闲后的下一个 tick 才发送。
func TestCheckReports_WaitsForIdleLead(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	var mu sync.Mutex
	var reports []string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport: func(leadThreadID, message string) error {
			mu.Lock()
			reports = append(reports, message)
			mu.Unlock()
			return nil
		},
	})

	leadID := "lead-busy"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task 1", Prompt: "prompt 1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	// 主会话自身仍在执行一轮回复。
	driver.setSessionState(leadID, kanban.SessionState{Running: true})

	mgr.Tick(ctx)

	mu.Lock()
	pending := len(reports)
	mu.Unlock()
	if pending != 0 {
		t.Fatalf("主会话运行中不应投递汇报，实际 %d 条", pending)
	}
	if h := beeByThread(t, mgr, leadID, bee.ThreadID); h.Reported {
		t.Error("汇报未送达时不应标记为已汇报")
	}

	// 主会话空闲后的下一个 tick 补发，且只发一次。
	driver.setSessionState(leadID, kanban.SessionState{Running: false})
	mgr.Tick(ctx)

	mu.Lock()
	got := append([]string(nil), reports...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("主会话空闲后应补发一次汇报，实际 %d 条", len(got))
	}
	if !strings.HasPrefix(got[0], ReportPrefix) {
		t.Errorf("汇报应以 %q 开头，实际 %q", ReportPrefix, got[0])
	}
	if h := beeByThread(t, mgr, leadID, bee.ThreadID); !h.Reported {
		t.Error("汇报送达后应标记为已汇报")
	}

	mgr.Tick(ctx)
	mu.Lock()
	got = append([]string(nil), reports...)
	mu.Unlock()
	if len(got) != 1 {
		t.Errorf("重复 Tick 不应重复汇报，实际 %d 条", len(got))
	}
}

// 同一批多个 bee 只有部分完成时不汇报：必须等全部 bee 落定后再一次性汇报并推进。
func TestCheckReports_WaitsForWholeBatch(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	var mu sync.Mutex
	var reports []string
	var idleCalls int
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport: func(leadThreadID, message string) error {
			mu.Lock()
			reports = append(reports, message)
			mu.Unlock()
			return nil
		},
		OnBeesIdle: func(context.Context, string) {
			mu.Lock()
			idleCalls++
			mu.Unlock()
		},
	})

	leadID := "lead-batch"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Abe", Task: "task 1", Prompt: "prompt 1"},
			{Name: "Bea", Task: "task 2", Prompt: "prompt 2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	if len(bees) != 2 {
		t.Fatalf("应有 2 个 bee，实际 %d 个", len(bees))
	}
	abe, bea := bees[0], bees[1]
	if err := os.WriteFile(filepath.Join(abe.WorktreePath, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bea.WorktreePath, "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}

	// 只有 Abe 结束，Bea 还在跑：整批未落定，不得提前汇报。
	driver.setSessionState(abe.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	driver.setSessionState(bea.ThreadID, kanban.SessionState{Running: true})
	driver.setSessionState(leadID, kanban.SessionState{Running: false})

	mgr.Tick(ctx)
	mu.Lock()
	pending := len(reports)
	mu.Unlock()
	if pending != 0 {
		t.Fatalf("部分 bee 仍在运行时不应汇报，实际 %d 条", pending)
	}
	if h := beeByThread(t, mgr, leadID, abe.ThreadID); h.Reported {
		t.Error("整批未落定时不应把已结束的 bee 标记为已汇报")
	}
	if h := beeByThread(t, mgr, leadID, bea.ThreadID); h.Status != StatusRunning {
		t.Fatalf("Bea 应为 running，实际 %s", h.Status)
	}

	// 再 Tick 一次也不得提前汇报。
	mgr.Tick(ctx)
	mu.Lock()
	pending = len(reports)
	mu.Unlock()
	if pending != 0 {
		t.Fatalf("整批未落定时重复 Tick 不应汇报，实际 %d 条", pending)
	}

	// Bea 也结束后，下一 tick 一次性汇报全部未汇报的 bee。
	driver.setSessionState(bea.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	mu.Lock()
	got := append([]string(nil), reports...)
	idle := idleCalls
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("全部 bee 结束后应统一汇报一次，实际 %d 条", len(got))
	}
	if !strings.HasPrefix(got[0], ReportPrefix) {
		t.Errorf("汇报应以 %q 开头，实际 %q", ReportPrefix, got[0])
	}
	for _, name := range []string{"Abe", "Bea"} {
		if !strings.Contains(got[0], name) {
			t.Errorf("统一汇报应包含 %s，实际: %s", name, got[0])
		}
	}
	if idle == 0 {
		t.Error("全部 bee 落定后应回调 OnBeesIdle 推进排队汇报")
	}
	for _, h := range mgr.State(leadID).Bees {
		if !h.Reported {
			t.Errorf("bee %s 汇报送达后应标记为已汇报", h.Name)
		}
	}

	// 再次 Tick 不重复汇报。
	mgr.Tick(ctx)
	mu.Lock()
	got = append([]string(nil), reports...)
	mu.Unlock()
	if len(got) != 1 {
		t.Errorf("重复 Tick 不应重复汇报，实际 %d 条", len(got))
	}
}

// 待审查（PendingMerge）的 bee 在审查有结论前不汇报；ApproveMerge 之后的下一个 Tick
// 汇报一次，正文给出落地状态并标记 Reported。
func TestCheckReports_ReportsPendingMergeBee(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	var mu sync.Mutex
	var reports []string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport: func(leadThreadID, message string) error {
			mu.Lock()
			reports = append(reports, message)
			mu.Unlock()
			return nil
		},
	})
	humanReviewSettings(mgr)

	leadID := "lead-pending-report"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task 1", Prompt: "prompt 1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "pending.txt"), []byte("pending\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	driver.setSessionState(leadID, kanban.SessionState{Running: false})

	mgr.Tick(ctx)

	h := beeByThread(t, mgr, leadID, bee.ThreadID)
	if h.Status != StatusCompleted {
		t.Fatalf("bee 应为 completed，实际 %s", h.Status)
	}
	if !h.PendingMerge {
		t.Fatal("human 审查档下 bee 应处于待审查状态")
	}
	if h.ReviewState != ReviewNeedsHuman {
		t.Fatalf("待人工审查的 bee 应为 needs_human，实际 %s", h.ReviewState)
	}
	mu.Lock()
	got := append([]string(nil), reports...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("待审查期间不应汇报，实际 %d 条", len(got))
	}
	if h.Reported {
		t.Error("待审查期间不应标记 Reported")
	}

	if _, err := mgr.ApproveMerge(ctx, leadID, bee.ThreadID); err != nil {
		t.Fatalf("ApproveMerge 失败: %v", err)
	}
	mgr.Tick(ctx)

	mu.Lock()
	got = append([]string(nil), reports...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("审查通过后应触发一次汇报，实际 %d 条", len(got))
	}
	if !strings.Contains(got[0], "Aspen") {
		t.Errorf("汇报应包含 bee 名字，实际: %s", got[0])
	}
	if !strings.Contains(got[0], "已合入（审查通过）") {
		t.Errorf("汇报应包含审查通过状态，实际: %s", got[0])
	}
	h = beeByThread(t, mgr, leadID, bee.ThreadID)
	if !h.Reported {
		t.Error("汇报送达后应标记 Reported")
	}
}

// 子代理全部结束时每个 tick 都回调 OnBeesIdle，幂等由接收方保证。
func TestCheckReports_FiresOnBeesIdle(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	driver := newFakeDriver()

	var mu sync.Mutex
	var idleLeads []string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport:    func(leadThreadID, message string) error { return nil },
		OnBeesIdle: func(_ context.Context, leadThreadID string) {
			mu.Lock()
			idleLeads = append(idleLeads, leadThreadID)
			mu.Unlock()
		},
	})

	leadID := "lead-idle"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "task 1", Prompt: "prompt 1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "idle.txt"), []byte("idle\n"), 0644); err != nil {
		t.Fatalf("写入 bee 工作副本失败: %v", err)
	}
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	driver.setSessionState(leadID, kanban.SessionState{Running: false})

	mgr.Tick(ctx)

	mu.Lock()
	first := len(idleLeads)
	mu.Unlock()
	if first < 1 {
		t.Fatalf("子代理结束后应回调 OnBeesIdle，实际 %d 次", first)
	}

	mgr.Tick(ctx)

	mu.Lock()
	second := len(idleLeads)
	mu.Unlock()
	if second <= first {
		t.Fatalf("再次 Tick 仍应回调 OnBeesIdle，实际由 %d 变为 %d", first, second)
	}
}

// TestManager_BeeFailure_ReportsErrorReasonToLead 验证 bee 一轮以启动错误等失败时，
// lead 收到的回报文本和 bee 的 Summary 都包含错误原因。
func TestManager_BeeFailure_ReportsErrorReasonToLead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()

	var reports []string
	var reportMu sync.Mutex

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport: func(leadThreadID, message string) error {
			reportMu.Lock()
			reports = append(reports, message)
			reportMu.Unlock()
			return nil
		},
	})

	leadID := "lead-err-report"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-err-report",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Error", Task: "task with start error", Prompt: "perror"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	beeThreadID := bees[0].ThreadID

	const failErr = `pi: start: exec: "pi": executable file not found in $PATH`
	driver.setSessionState(beeThreadID, kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: failErr,
	})

	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(st.Bees))
	}
	if st.Bees[0].Status != StatusFailed {
		t.Fatalf("expected bee status to be StatusFailed, got %v", st.Bees[0].Status)
	}
	if !strings.Contains(st.Bees[0].Summary, "executable file not found") {
		t.Errorf("expected bee Summary to contain 'executable file not found', got %q", st.Bees[0].Summary)
	}

	reportMu.Lock()
	defer reportMu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("expected 1 report sent to lead, got %d", len(reports))
	}
	if !strings.Contains(reports[0], "executable file not found") {
		t.Errorf("expected lead report text to contain 'executable file not found', got %q", reports[0])
	}
}

func TestFormatFailureReason(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "   ",
			want: "",
		},
		{
			name: "status failed",
			in:   "failed",
			want: "",
		},
		{
			name: "status ERROR",
			in:   "ERROR",
			want: "",
		},
		{
			name: "status attention: failed",
			in:   "attention: failed",
			want: "",
		},
		{
			name: "start exec error",
			in:   `pi: start: exec: "pi": executable file not found in $PATH`,
			want: `启动失败：pi: start: exec: "pi": executable file not found in $PATH`,
		},
		{
			name: "already has prefix",
			in:   "启动失败：executable not found",
			want: "启动失败：executable not found",
		},
		{
			name: "runtime error",
			in:   "context deadline exceeded",
			want: "运行失败：context deadline exceeded",
		},
		{
			name: "truncate to 500",
			in:   strings.Repeat("a", 600),
			want: "运行失败：" + strings.Repeat("a", 500),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatFailureReason(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
