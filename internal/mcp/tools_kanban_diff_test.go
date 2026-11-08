package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/git"
	"mcode/internal/kanban"
	"mcode/internal/store"
)

// setupDiffTestMCP 构造带 git runner 与 worktree 目录的看板服务，
// 让 kanban_task_diff 能在真实 worktree 上跑 `git diff HEAD`。
func setupDiffTestMCP(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	projDir := t.TempDir()
	if err := s.UpsertProject(context.Background(), store.Project{
		ID:   "proj-test-1",
		Name: "test-proj",
		Path: projDir,
	}); err != nil {
		t.Fatalf("插入测试项目失败: %v", err)
	}

	worktreeDir := t.TempDir()
	svc := kanban.NewService(kanban.Options{
		Store:       s,
		Driver:      newTestDriver(),
		Projects:    &testProjects{dir: projDir},
		Git:         git.NewRunner("", 2),
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { svc.Close() })

	return New(Options{Store: s, Kanban: svc}), s, worktreeDir
}

// createDiffTask 在存储里登记一个属于测试项目的任务。
func createDiffTask(t *testing.T, s *store.Store, taskID string) {
	t.Helper()
	projectID := "proj-test-1"
	if err := s.CreateTask(context.Background(), store.Task{
		ID:        taskID,
		ProjectID: &projectID,
		Title:     taskID,
	}); err != nil {
		t.Fatalf("创建任务 %s 失败: %v", taskID, err)
	}
}

// diffTestGit 在 dir 下执行 git 命令，失败即终止测试。
func diffTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initDiffWorktree 在 worktree 根下建一个带一次提交的任务仓库，返回仓库路径。
func initDiffWorktree(t *testing.T, worktreeDir, taskID, content string) string {
	t.Helper()
	repo := filepath.Join(worktreeDir, taskID)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("创建 worktree 目录失败: %v", err)
	}
	diffTestGit(t, repo, "init")
	diffTestGit(t, repo, "config", "user.name", "Diff Test")
	diffTestGit(t, repo, "config", "user.email", "diff@example.com")
	writeDiffFile(t, repo, content)
	diffTestGit(t, repo, "add", ".")
	diffTestGit(t, repo, "commit", "-m", "init")
	return repo
}

// writeDiffFile 写入 worktree 里的受版本控制文件。
func writeDiffFile(t *testing.T, repo, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
}

func TestKanbanTaskDiffRequiresTaskID(t *testing.T) {
	srv, st, _ := setupDiffTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	text, isError := callThreadTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{})
	if !isError {
		t.Fatalf("缺少 task_id 时应报错: %q", text)
	}
	if code := toolErrorCode(ToolResult{Text: text, IsError: true}); code != CodeInvalidArguments {
		t.Fatalf("错误码期望 %s, 实际为 %q: %s", CodeInvalidArguments, code, text)
	}

	// 任务不存在时给 not_found，而不是误报“没有代码变更”。
	stText, stError := callThreadTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{"task_id": "no-such-task"})
	if !stError {
		t.Fatalf("不存在的任务应报错: %q", stText)
	}
	if code := toolErrorCode(ToolResult{Text: stText, IsError: true}); code != CodeNotFound {
		t.Fatalf("错误码期望 %s, 实际为 %q: %s", CodeNotFound, code, stText)
	}
}

func TestKanbanTaskDiffReturnsWorktreeChanges(t *testing.T) {
	srv, st, worktreeDir := setupDiffTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")
	createDiffTask(t, st, "task-1")
	repo := initDiffWorktree(t, worktreeDir, "task-1", "line1\nline2\n")
	writeDiffFile(t, repo, "line1\nline3\n")

	text, isError := callThreadTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{"task_id": " task-1 "})
	if isError {
		t.Fatalf("读取 diff 失败: %q", text)
	}
	if !strings.Contains(text, "-line2") || !strings.Contains(text, "+line3") {
		t.Fatalf("diff 内容不符合预期: %q", text)
	}
	if strings.Contains(text, kanbanTaskDiffTruncatedMark) {
		t.Fatalf("未超过上限时不应标注截断: %q", text)
	}
}

func TestKanbanTaskDiffEmptyWhenNoChanges(t *testing.T) {
	srv, st, worktreeDir := setupDiffTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")
	// 1. 任务没有 worktree。
	createDiffTask(t, st, "task-noworktree")
	text, isError := callThreadTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{"task_id": "task-noworktree"})
	if isError || text != "该任务没有代码变更" {
		t.Fatalf("没有 worktree 时应返回空变更文案: %q error=%v", text, isError)
	}

	// 2. worktree 干净，没有未提交改动。
	createDiffTask(t, st, "task-clean")
	initDiffWorktree(t, worktreeDir, "task-clean", "line1\n")
	text, isError = callThreadTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{"task_id": "task-clean"})
	if isError || text != "该任务没有代码变更" {
		t.Fatalf("worktree 无改动时应返回空变更文案: %q error=%v", text, isError)
	}
}

func TestKanbanTaskDiffTruncatesLargeDiff(t *testing.T) {
	srv, st, worktreeDir := setupDiffTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")
	createDiffTask(t, st, "task-large")
	// 约 330 KB 的多字节改动：既超过工具层 200 KB 上限，又低于服务层 512 KiB 上限，
	// 因此这条用例覆盖的是工具层截断与 rune 边界。
	repo := initDiffWorktree(t, worktreeDir, "task-large", "seed\n")
	writeDiffFile(t, repo, strings.Repeat("中文行中文行中文行\n", 12000))

	text, isError := callThreadTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{"task_id": "task-large"})
	if isError {
		t.Fatalf("读取大 diff 失败: %q", text)
	}
	if !strings.HasSuffix(text, "\n"+kanbanTaskDiffTruncatedMark) {
		t.Fatalf("截断后应在末尾追加 %s", kanbanTaskDiffTruncatedMark)
	}
	body := strings.TrimSuffix(text, "\n"+kanbanTaskDiffTruncatedMark)
	if !utf8.ValidString(body) {
		t.Fatal("截断后出现了残缺的多字节字符")
	}
	if len(body) > kanbanTaskDiffMaxBytes {
		t.Fatalf("截断后正文 %d 字节超过上限 %d", len(body), kanbanTaskDiffMaxBytes)
	}
	if len(body) < kanbanTaskDiffMaxBytes-utf8.UTFMax {
		t.Fatalf("截断后正文只有 %d 字节，过短，说明截断位置不对", len(body))
	}
}

func TestKanbanTaskDiffTextMarksServiceTruncation(t *testing.T) {
	// 服务层已经截断（truncated 为真）但内容很短时，也要追加标记。
	got := kanbanTaskDiffText("@@ -1 +1 @@\n-a\n+b\n", true)
	if !strings.HasSuffix(got, "\n"+kanbanTaskDiffTruncatedMark) {
		t.Fatalf("服务层截断时应在末尾追加标记: %q", got)
	}
	if !strings.HasPrefix(got, "@@ -1 +1 @@") {
		t.Fatalf("截断标记不应覆盖 diff 内容: %q", got)
	}
	if plain := kanbanTaskDiffText("@@ -1 +1 @@\n-a\n+b", false); strings.Contains(plain, kanbanTaskDiffTruncatedMark) {
		t.Fatalf("未截断时不应追加标记: %q", plain)
	}
	if blank := kanbanTaskDiffText("  \n\n", false); blank != "该任务没有代码变更" {
		t.Fatalf("空白 diff 应返回空变更文案: %q", blank)
	}
}

func TestKanbanTaskDiffVisibleToRoles(t *testing.T) {
	srv, st, _ := setupDiffTestMCP(t)
	const projectID = "proj-test-1"
	projectIDCopy := projectID
	planThread, reviewThread := "thread-plan", "thread-review"
	planThreadCopy := planThread
	reviewThreadCopy := reviewThread
	if err := st.CreateGoal(context.Background(), store.Goal{
		ID: "plan-goal", ProjectID: projectID, Prompt: "plan", PlanThreadID: &planThreadCopy,
	}); err != nil {
		t.Fatalf("创建规划目标失败: %v", err)
	}
	if err := st.CreateTask(context.Background(), store.Task{
		ID: "review-task", ProjectID: &projectIDCopy, Title: "review", ReviewThreadID: &reviewThreadCopy,
	}); err != nil {
		t.Fatalf("创建评审任务失败: %v", err)
	}
	for _, threadID := range []string{planThread, reviewThread, "thread-ordinary"} {
		registerThread(t, st, threadID, projectID)
	}
	for _, threadID := range []string{planThread, reviewThread} {
		if !threadToolNames(t, srv, threadID)[kanbanTaskDiffToolName] {
			t.Fatalf("%s 会话应列出 %s", threadID, kanbanTaskDiffToolName)
		}
	}

	// 普通会话按设计只用看板入口工具：经 action=task_diff 调用同一个工具。
	names := threadToolNames(t, srv, "thread-ordinary")
	if names[kanbanTaskDiffToolName] {
		t.Fatalf("普通会话不应直接看到类型化看板工具: %v", names)
	}
	createDiffTask(t, st, "task-plain")
	text, isError, _ := callKanbanEntry(t, srv, "thread-ordinary", map[string]any{
		"action": "task_diff",
		"args":   map[string]any{"task_id": "task-plain"},
	})
	if isError || text != "该任务没有代码变更" {
		t.Fatalf("普通会话经入口调用 task_diff 失败: %q error=%v", text, isError)
	}
}
