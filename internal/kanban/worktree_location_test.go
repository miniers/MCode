package kanban

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
	"mcode/internal/git"
)

func TestTaskWorktreePathLocations(t *testing.T) {
	for _, location := range []string{config.WorktreeLocationCentral, config.WorktreeLocationProject} {
		for _, existing := range []string{"none", "central", "project", "both"} {
			t.Run(location+"/"+existing, func(t *testing.T) {
				data, repo := t.TempDir(), t.TempDir()
				svc, st := setupAcceptanceService(t, filepath.Join(data, "worktrees"))
				svc.opts.DataDir = data
				svc.opts.Projects = &mockProjects{dir: repo}
				createAcceptanceTask(t, st, "task-1", "")
				cfg := config.Defaults()
				cfg.SourceControl.WorktreeLocation = location
				if err := config.Save(data, cfg); err != nil {
					t.Fatal(err)
				}
				central := filepath.Join(data, "worktrees", "task-1")
				project := filepath.Join(repo, ".mcode", "worktrees", "task-1")
				if existing == "central" || existing == "both" {
					if err := os.MkdirAll(central, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if existing == "project" || existing == "both" {
					if err := os.MkdirAll(project, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				want := central
				if existing == "project" || (existing == "none" && location == config.WorktreeLocationProject) {
					want = project
				}
				if got := svc.TaskWorktreePath("task-1"); got != want {
					t.Fatalf("路径 %q，期望 %q", got, want)
				}
				if !svc.IsTaskWorktreeDir(filepath.Join(project, "src")) {
					t.Fatal("应识别项目内任务目录")
				}
				if svc.IsTaskWorktreeDir(filepath.Dir(project)) {
					t.Fatal("worktree 根目录不应识别为任务目录")
				}
			})
		}
	}
}

func TestTaskWorktreeProjectStartAndResume(t *testing.T) {
	repo := initMergeTestRepo(t)
	data := t.TempDir()
	st := openMergeTestStore(t)
	svc := newMergeTestService(t, st, repo, filepath.Join(data, "worktrees"), newMockDriver())
	svc.opts.DataDir = data
	// 项目可以是仓库内的子目录，worktree 仍应建在仓库根下。
	subdir := filepath.Join(repo, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.opts.Projects = &mockProjects{dir: subdir}
	cfg := config.Defaults()
	cfg.SourceControl.WorktreeLocation = config.WorktreeLocationProject
	if err := config.Save(data, cfg); err != nil {
		t.Fatal(err)
	}
	task := createAcceptanceTask(t, st, "task-project", "")
	started, err := svc.StartTask(context.Background(), task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := svc.opts.Git.RepoRoot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".mcode", "worktrees", task.ID)
	if got := svc.TaskWorktreePath(task.ID); got != want {
		t.Fatalf("任务路径 %q，期望 %q", got, want)
	}
	if out := execGit(t, repo, "status", "--porcelain"); out != "" {
		t.Fatalf("主仓库应保持干净：%s", out)
	}
	// 设置改回集中后启动同一任务，必须复用原目录与分支。
	cfg.SourceControl.WorktreeLocation = config.WorktreeLocationCentral
	if err := config.Save(data, cfg); err != nil {
		t.Fatal(err)
	}
	started.Status = string(TaskStatusTodo)
	reset, err := st.UpdateTask(context.Background(), started, started.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartTask(context.Background(), reset.ID, reset.Revision); err != nil {
		t.Fatal(err)
	}
	if got := svc.TaskWorktreePath(task.ID); got != want {
		t.Fatalf("应继续使用已有路径：%q", got)
	}
	if !git.IsManagedWorktreePath(want) {
		t.Fatalf("项目内路径应受管：%q", want)
	}
}
