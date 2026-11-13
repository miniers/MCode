package kanban

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/store"
	"mcode/internal/testenv"
)

// orphanDriver 模拟执行会话已丢失的驱动：missing 中的线程返回 store.ErrNotFound，其余视为运行中。
type orphanDriver struct {
	missing map[string]bool
}

func (d *orphanDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	return "th-new", nil
}

func (d *orphanDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	if d.missing[threadID] {
		return SessionState{}, store.ErrNotFound
	}
	return SessionState{Running: true}, nil
}

// execGit 在 dir 下执行 git 命令，命令失败即终止测试。
func execGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initMergeTestRepo 创建带一次初始提交的临时 Git 仓库，返回仓库根目录。
func initMergeTestRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("创建仓库目录失败: %v", err)
	}
	execGit(t, repo, "init")
	execGit(t, repo, "config", "user.name", "Kanban Test")
	execGit(t, repo, "config", "user.email", "kanban@example.com")
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("写入初始文件失败: %v", err)
	}
	execGit(t, repo, "add", ".")
	execGit(t, repo, "commit", "-m", "init")
	return repo
}

// openMergeTestStore 打开测试数据库并注册清理。
func openMergeTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// newMergeTestService 构造带真实 Git runner 与 worktree 目录的看板服务。
func newMergeTestService(t *testing.T, st *store.Store, repo, wtDir string, driver SessionDriver) *Service {
	t.Helper()
	svc := NewService(Options{
		Store:       st,
		Git:         git.NewRunner("", 0),
		Driver:      driver,
		Projects:    &mockProjects{dir: repo},
		WorktreeDir: wtDir,
	})
	t.Cleanup(func() { svc.Close() })
	return svc
}

// createReviewTask 创建 todo 任务并推进到 review，返回最新任务记录。
func createReviewTask(t *testing.T, svc *Service, st *store.Store, taskID, projectID, title string) store.Task {
	t.Helper()
	ctx := context.Background()
	task := store.Task{
		ID:        taskID,
		ProjectID: &projectID,
		Title:     title,
		Status:    string(TaskStatusTodo),
		Priority:  string(PriorityNone),
		DependsOn: []string{},
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	inReview, err := svc.MoveTask(ctx, taskID, TaskStatusReview, 1)
	if err != nil {
		t.Fatalf("移至 review 失败: %v", err)
	}
	return inReview
}

// taskBranchPrefix 返回任务分支使用的 8 位前缀。
func taskBranchPrefix(taskID string) string {
	if len(taskID) > 8 {
		return taskID[:8]
	}
	return taskID
}

func TestAutoMergeSquashSuccess(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	repo := initMergeTestRepo(t)
	wtDir := t.TempDir()
	st := openMergeTestStore(t)
	svc := newMergeTestService(t, st, repo, wtDir, newMockDriver())

	taskID := "task-merge-aaaa"
	branch := "mcode/kanban-" + taskBranchPrefix(taskID)
	wtPath := filepath.Join(wtDir, taskID)
	execGit(t, repo, "worktree", "add", "-b", branch, wtPath)
	if err := os.WriteFile(filepath.Join(wtPath, "app.txt"), []byte("line1\nline2\nfeature\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 文件失败: %v", err)
	}

	inReview := createReviewTask(t, svc, st, taskID, "proj-1", "合并成功任务")
	beforeBee := execGit(t, repo, "rev-parse", "HEAD")
	beforeCount, err := strconv.Atoi(execGit(t, repo, "rev-list", "--count", "HEAD"))
	if err != nil {
		t.Fatalf("解析提交数量失败: %v", err)
	}

	accepted, err := svc.ReviewTask(ctx, taskID, ReviewAccept, "", inReview.Revision)
	if err != nil {
		t.Fatalf("验收任务失败: %v", err)
	}
	if accepted.Status != string(TaskStatusDone) {
		t.Fatalf("期望任务状态 done，实际: %s", accepted.Status)
	}

	afterBee := execGit(t, repo, "rev-parse", "HEAD")
	if afterBee == beforeBee {
		t.Fatalf("期望主仓库 HEAD 前进，实际仍为 %s", beforeBee)
	}
	afterCount, _ := strconv.Atoi(execGit(t, repo, "rev-list", "--count", "HEAD"))
	if afterCount != beforeCount+1 {
		t.Errorf("期望主仓库新增 1 个提交，实际 %d -> %d", beforeCount, afterCount)
	}
	msg := execGit(t, repo, "log", "-1", "--pretty=%B")
	if !strings.Contains(msg, "Squash-merged from branch "+branch) {
		t.Errorf("期望提交信息包含 squash 来源分支 %s，实际:\n%s", branch, msg)
	}
	if !strings.Contains(msg, "合并成功任务") {
		t.Errorf("期望提交信息包含任务标题，实际:\n%s", msg)
	}
	content, err := os.ReadFile(filepath.Join(repo, "app.txt"))
	if err != nil {
		t.Fatalf("读取合并后文件失败: %v", err)
	}
	if !strings.Contains(string(content), "feature") {
		t.Errorf("期望 worktree 改动已合入主仓库，实际内容:\n%s", content)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("期望合并成功后清理 worktree，Stat err=%v", err)
	}
	if out := execGit(t, repo, "branch", "--list", branch); out != "" {
		t.Errorf("期望合并成功后删除任务分支，实际: %s", out)
	}
}

func TestAutoMergeRejectsDirtyMainWorktree(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	repo := initMergeTestRepo(t)
	wtDir := t.TempDir()
	st := openMergeTestStore(t)
	svc := newMergeTestService(t, st, repo, wtDir, newMockDriver())

	taskID := "task-merge-bbbb"
	branch := "mcode/kanban-" + taskBranchPrefix(taskID)
	wtPath := filepath.Join(wtDir, taskID)
	execGit(t, repo, "worktree", "add", "-b", branch, wtPath)
	if err := os.WriteFile(filepath.Join(wtPath, "app.txt"), []byte("line1\nline2\nworktree\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 文件失败: %v", err)
	}
	// 主工作区留下未提交改动
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("line1\nline2\ndirty\n"), 0o644); err != nil {
		t.Fatalf("修改主工作区文件失败: %v", err)
	}

	inReview := createReviewTask(t, svc, st, taskID, "proj-1", "脏工作区任务")
	beforeBee := execGit(t, repo, "rev-parse", "HEAD")

	accepted, err := svc.ReviewTask(ctx, taskID, ReviewAccept, "", inReview.Revision)
	if err != nil {
		t.Fatalf("验收任务失败: %v", err)
	}
	if accepted.ReviewFeedback == nil || !strings.Contains(*accepted.ReviewFeedback, "主工作区") {
		t.Fatalf("期望 ReviewFeedback 提示主工作区，实际: %v", accepted.ReviewFeedback)
	}
	// 合并失败时任务不能标 done：留在 review，卡片显示失败原因。
	if accepted.Status != string(TaskStatusReview) {
		t.Fatalf("期望拒绝合并后任务留在 review，实际: %s", accepted.Status)
	}
	if accepted.ReviewStatus == nil || *accepted.ReviewStatus != "merge_failed" {
		t.Fatalf("期望 ReviewStatus 为 merge_failed，实际: %v", accepted.ReviewStatus)
	}
	if accepted.LastActivityText == nil || !strings.Contains(*accepted.LastActivityText, "主工作区") {
		t.Fatalf("期望 LastActivityText 提示主工作区，实际: %v", accepted.LastActivityText)
	}
	if bee := execGit(t, repo, "rev-parse", "HEAD"); bee != beforeBee {
		t.Errorf("期望拒绝合并时 HEAD 不变，实际 %s -> %s", beforeBee, bee)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("期望拒绝合并时保留 worktree，Stat err=%v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo, "app.txt"))
	if err != nil {
		t.Fatalf("读取主工作区文件失败: %v", err)
	}
	if !strings.Contains(string(content), "dirty") {
		t.Errorf("期望主工作区未提交改动被保留，实际内容:\n%s", content)
	}
}

func TestAutoMergeConflictRollsBack(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	repo := initMergeTestRepo(t)
	wtDir := t.TempDir()
	st := openMergeTestStore(t)
	svc := newMergeTestService(t, st, repo, wtDir, newMockDriver())

	taskID := "task-merge-cccc"
	branch := "mcode/kanban-" + taskBranchPrefix(taskID)
	wtPath := filepath.Join(wtDir, taskID)
	execGit(t, repo, "worktree", "add", "-b", branch, wtPath)
	// worktree 与主仓库修改同一行
	if err := os.WriteFile(filepath.Join(wtPath, "app.txt"), []byte("worktree-line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("main-line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("修改主工作区文件失败: %v", err)
	}
	execGit(t, repo, "add", ".")
	execGit(t, repo, "commit", "-m", "main change")

	inReview := createReviewTask(t, svc, st, taskID, "proj-1", "冲突任务")
	beforeBee := execGit(t, repo, "rev-parse", "HEAD")

	accepted, err := svc.ReviewTask(ctx, taskID, ReviewAccept, "", inReview.Revision)
	if err != nil {
		t.Fatalf("验收任务失败: %v", err)
	}
	if accepted.ReviewFeedback == nil || !strings.Contains(*accepted.ReviewFeedback, "冲突") {
		t.Fatalf("期望 ReviewFeedback 提示冲突，实际: %v", accepted.ReviewFeedback)
	}
	// 冲突回滚后任务留在 review，ReviewStatus 为 merge_failed，失败原因写到卡片字段。
	if accepted.Status != string(TaskStatusReview) {
		t.Fatalf("期望冲突后任务留在 review，实际: %s", accepted.Status)
	}
	if accepted.ReviewStatus == nil || *accepted.ReviewStatus != "merge_failed" {
		t.Fatalf("期望 ReviewStatus 为 merge_failed，实际: %v", accepted.ReviewStatus)
	}
	if accepted.LastActivityText == nil || !strings.Contains(*accepted.LastActivityText, "冲突") {
		t.Fatalf("期望 LastActivityText 提示冲突，实际: %v", accepted.LastActivityText)
	}
	if bee := execGit(t, repo, "rev-parse", "HEAD"); bee != beforeBee {
		t.Errorf("期望冲突回滚后 HEAD 不变，实际 %s -> %s", beforeBee, bee)
	}
	if status := execGit(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("期望冲突回滚后主工作区干净，实际:\n%s", status)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("期望冲突时保留 worktree，Stat err=%v", err)
	}
}

func TestRecoverOrphanTasks(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	st := openMergeTestStore(t)
	driver := &orphanDriver{missing: map[string]bool{"th-missing": true}}
	svc := NewService(Options{Store: st, Driver: driver, Projects: &mockProjects{dir: t.TempDir()}})
	t.Cleanup(func() { svc.Close() })

	goal := store.Goal{
		ID:        "goal-orphan",
		ProjectID: "proj-1",
		Title:     "孤儿任务恢复",
		Prompt:    "孤儿任务恢复",
		Status:    string(GoalStatusPlanReview),
		Policy:    map[string]any{},
		Progress:  map[string]any{},
	}
	if err := st.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}

	projectID := "proj-1"
	threadMissing := "th-missing"
	threadLive := "th-live"
	tasks := []store.Task{
		{ID: "task-orphan-missing-thread", ProjectID: &projectID, GoalID: &goal.ID, ThreadID: &threadMissing, Title: "会话丢失", Status: string(TaskStatusInProgress), Priority: string(PriorityNone), DependsOn: []string{}},
		{ID: "task-orphan-no-thread", ProjectID: &projectID, GoalID: &goal.ID, Title: "无会话", Status: string(TaskStatusInProgress), Priority: string(PriorityNone), DependsOn: []string{}},
		{ID: "task-orphan-live", ProjectID: &projectID, GoalID: &goal.ID, ThreadID: &threadLive, Title: "会话存活", Status: string(TaskStatusInProgress), Priority: string(PriorityNone), DependsOn: []string{}},
		{ID: "task-orphan-no-goal", ProjectID: &projectID, ThreadID: &threadMissing, Title: "无目标", Status: string(TaskStatusInProgress), Priority: string(PriorityNone), DependsOn: []string{}},
		{ID: "task-orphan-todo", ProjectID: &projectID, GoalID: &goal.ID, ThreadID: &threadMissing, Title: "待办任务", Status: string(TaskStatusTodo), Priority: string(PriorityNone), DependsOn: []string{}},
	}
	for _, task := range tasks {
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("创建任务 %s 失败: %v", task.ID, err)
		}
	}

	recovered, err := svc.RecoverOrphanTasks(ctx)
	if err != nil {
		t.Fatalf("恢复孤儿任务失败: %v", err)
	}
	if recovered != 2 {
		t.Fatalf("期望恢复 2 个任务，实际: %d", recovered)
	}

	for _, id := range []string{"task-orphan-missing-thread", "task-orphan-no-thread"} {
		got, err := st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("获取任务 %s 失败: %v", id, err)
		}
		if got.Status != string(TaskStatusTodo) {
			t.Errorf("任务 %s 期望重置为 todo，实际: %s", id, got.Status)
		}
		if got.ThreadID != nil {
			t.Errorf("任务 %s 期望 ThreadID 为 nil，实际: %v", id, *got.ThreadID)
		}
		if got.LastActivityText == nil || *got.LastActivityText != "会话已丢失，任务已重置为待办" {
			t.Errorf("任务 %s 期望记录重置说明，实际: %v", id, got.LastActivityText)
		}
		if got.Revision != 2 {
			t.Errorf("任务 %s 期望 revision 2，实际: %d", id, got.Revision)
		}
	}

	untouched := map[string]string{
		"task-orphan-live":    string(TaskStatusInProgress),
		"task-orphan-no-goal": string(TaskStatusInProgress),
		"task-orphan-todo":    string(TaskStatusTodo),
	}
	for id, wantStatus := range untouched {
		got, err := st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("获取任务 %s 失败: %v", id, err)
		}
		if got.Status != wantStatus {
			t.Errorf("任务 %s 期望保持 %s，实际: %s", id, wantStatus, got.Status)
		}
	}
	live, err := st.GetTask(ctx, "task-orphan-live")
	if err != nil {
		t.Fatalf("获取存活会话任务失败: %v", err)
	}
	if live.ThreadID == nil || *live.ThreadID != threadLive {
		t.Errorf("期望存活会话任务保留 ThreadID，实际: %v", live.ThreadID)
	}

	// Driver 为 nil 时不做任何恢复
	svcNoDriver := NewService(Options{Store: st})
	t.Cleanup(func() { svcNoDriver.Close() })
	n, err := svcNoDriver.RecoverOrphanTasks(ctx)
	if err != nil {
		t.Fatalf("Driver 为 nil 时不应报错，实际: %v", err)
	}
	if n != 0 {
		t.Fatalf("Driver 为 nil 时期望恢复 0 个，实际: %d", n)
	}
}
