package kanban

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
	"mcode/internal/testenv"
)

// TestAutoAcceptMergesFinishedTask 覆盖自动接受：无 [命令]/[你验] 的任务完成后走评审接受触发合并，
// 最终状态为 done，改动进入主工作区。
func TestAutoAcceptMergesFinishedTask(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	repo := initMergeTestRepo(t)
	wtDir := t.TempDir()
	st := openMergeTestStore(t)
	driver := newMockDriver()
	svc := newMergeTestService(t, st, repo, wtDir, driver)
	scheduler := NewGoalScheduler(svc, time.Minute)

	taskID := "task-auto-merge"
	branch := "mcode/kanban-" + taskBranchPrefix(taskID)
	wtPath := filepath.Join(wtDir, taskID)
	execGit(t, repo, "worktree", "add", "-b", branch, wtPath)
	if err := os.WriteFile(filepath.Join(wtPath, "app.txt"), []byte("line1\nline2\nfeature\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 文件失败: %v", err)
	}

	threadID := "thread-auto-merge"
	seedSchedulerGoal(t, ctx, st, store.Goal{
		ID:        "goal-auto-merge",
		ProjectID: "proj-1",
		Title:     "自动接受合并",
		Policy:    map[string]any{"autoAccept": true, "maxConcurrency": 1},
	}, store.Task{ID: taskID, Title: "自动合并任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false, LastAssistantText: "已完成执行。"})

	beforeHead := execGit(t, repo, "rev-parse", "HEAD")
	scheduler.Tick(ctx)

	latest, err := st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusDone) {
		t.Fatalf("期望自动接受后状态为 done，实际: %s", latest.Status)
	}
	if latest.ReviewStatus == nil || *latest.ReviewStatus != "passed" {
		t.Fatalf("期望 ReviewStatus 为 passed，实际: %v", latest.ReviewStatus)
	}
	if afterHead := execGit(t, repo, "rev-parse", "HEAD"); afterHead == beforeHead {
		t.Fatalf("期望自动接受触发合并、主仓库 HEAD 前进，实际仍为 %s", beforeHead)
	}
	content, err := os.ReadFile(filepath.Join(repo, "app.txt"))
	if err != nil {
		t.Fatalf("读取主工作区文件失败: %v", err)
	}
	if !strings.Contains(string(content), "feature") {
		t.Errorf("期望 worktree 改动已合入主工作区，实际内容:\n%s", content)
	}
}

// TestAutoAcceptKeepsGatedTaskInReview 覆盖含 [命令] 或 [你验] 的任务：自动接受不生效，
// 任务停在 review，既不合并也不标记 done。
func TestAutoAcceptKeepsGatedTaskInReview(t *testing.T) {
	cases := []struct {
		name     string
		criteria string
	}{
		{name: "含命令", criteria: "- [命令] `go test ./...`"},
		{name: "含你验", criteria: "- [你验] 手工点一遍界面"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testenv.TempHome(t)
			ctx := context.Background()

			repo := initMergeTestRepo(t)
			wtDir := t.TempDir()
			st := openMergeTestStore(t)
			driver := newMockDriver()
			svc := newMergeTestService(t, st, repo, wtDir, driver)
			scheduler := NewGoalScheduler(svc, time.Minute)

			taskID := "task-gated-" + tc.name
			branch := "mcode/kanban-" + taskBranchPrefix(taskID)
			wtPath := filepath.Join(wtDir, taskID)
			execGit(t, repo, "worktree", "add", "-b", branch, wtPath)
			if err := os.WriteFile(filepath.Join(wtPath, "app.txt"), []byte("line1\nline2\nfeature\n"), 0o644); err != nil {
				t.Fatalf("修改 worktree 文件失败: %v", err)
			}

			threadID := "thread-gated-" + tc.name
			seedSchedulerGoal(t, ctx, st, store.Goal{
				ID:        "goal-gated-" + tc.name,
				ProjectID: "proj-1",
				Title:     "受控验收",
				Policy:    map[string]any{"autoAccept": true, "maxConcurrency": 1},
			}, store.Task{
				ID:                 taskID,
				Title:              "受控验收任务",
				Status:             string(TaskStatusInProgress),
				ThreadID:           &threadID,
				AcceptanceCriteria: tc.criteria,
			})
			driver.setThreadState(threadID, SessionState{Running: false, LastAssistantText: "已完成执行。"})

			beforeHead := execGit(t, repo, "rev-parse", "HEAD")
			scheduler.Tick(ctx)

			latest, err := st.GetTask(ctx, taskID)
			if err != nil {
				t.Fatalf("获取任务失败: %v", err)
			}
			if latest.Status != string(TaskStatusReview) {
				t.Fatalf("期望含验收条目的任务停在 review，实际: %s", latest.Status)
			}
			if latest.ReviewStatus != nil && *latest.ReviewStatus == "passed" {
				t.Fatalf("期望不标记验收通过，实际 ReviewStatus: %v", *latest.ReviewStatus)
			}
			if afterHead := execGit(t, repo, "rev-parse", "HEAD"); afterHead != beforeHead {
				t.Errorf("期望未自动合并，实际 HEAD %s -> %s", beforeHead, afterHead)
			}
			if _, err := os.Stat(wtPath); err != nil {
				t.Errorf("期望保留 worktree，Stat err=%v", err)
			}
		})
	}
}

// TestReviewAcceptMergeFailureStaysReview 覆盖合并失败：任务留在 review，ReviewStatus 为 merge_failed，
// 失败原因同时写入 LastActivityText，主工作区保持原样。
func TestReviewAcceptMergeFailureStaysReview(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()

	repo := initMergeTestRepo(t)
	wtDir := t.TempDir()
	st := openMergeTestStore(t)
	svc := newMergeTestService(t, st, repo, wtDir, newMockDriver())

	taskID := "task-merge-fail"
	branch := "mcode/kanban-" + taskBranchPrefix(taskID)
	wtPath := filepath.Join(wtDir, taskID)
	execGit(t, repo, "worktree", "add", "-b", branch, wtPath)
	// worktree 与主仓库修改同一行，制造合并冲突
	if err := os.WriteFile(filepath.Join(wtPath, "app.txt"), []byte("worktree-line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("修改 worktree 文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("main-line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("修改主工作区文件失败: %v", err)
	}
	execGit(t, repo, "add", ".")
	execGit(t, repo, "commit", "-m", "main change")

	inReview := createReviewTask(t, svc, st, taskID, "proj-1", "合并失败任务")
	beforeHead := execGit(t, repo, "rev-parse", "HEAD")

	accepted, err := svc.ReviewTask(ctx, taskID, ReviewAccept, "", inReview.Revision)
	if err != nil {
		t.Fatalf("验收任务失败: %v", err)
	}
	if accepted.Status != string(TaskStatusReview) {
		t.Fatalf("期望合并失败后任务留在 review，实际: %s", accepted.Status)
	}
	if accepted.ReviewStatus == nil || *accepted.ReviewStatus != "merge_failed" {
		t.Fatalf("期望 ReviewStatus 为 merge_failed，实际: %v", accepted.ReviewStatus)
	}
	if accepted.LastActivityText == nil || !strings.Contains(*accepted.LastActivityText, "冲突") {
		t.Fatalf("期望 LastActivityText 含失败原因，实际: %v", accepted.LastActivityText)
	}

	persisted, err := st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persisted.Status != string(TaskStatusReview) || persisted.ReviewStatus == nil || *persisted.ReviewStatus != "merge_failed" {
		t.Fatalf("期望落库状态为 review/merge_failed，实际: %s / %v", persisted.Status, persisted.ReviewStatus)
	}
	if bee := execGit(t, repo, "rev-parse", "HEAD"); bee != beforeHead {
		t.Errorf("期望冲突回滚后 HEAD 不变，实际 %s -> %s", beforeHead, bee)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("期望冲突时保留 worktree，Stat err=%v", err)
	}
}
