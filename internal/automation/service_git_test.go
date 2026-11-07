package automation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/kanban"
)

// initGitRepo 创建带一次初始提交的临时仓库。
func initGitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestGitDiffSummary(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)

	if got := gitDiffSummary(ctx, dir); got != NoCodeChangesMarker {
		t.Fatalf("干净仓库应返回标记，实际 %q", got)
	}
	if got := gitDiffSummary(ctx, ""); got != "" {
		t.Fatalf("空目录应返回空串，实际 %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := gitDiffSummary(ctx, dir)
	for _, want := range []string{"a.txt", "new.txt", "+two"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, NoCodeChangesMarker) {
		t.Errorf("有变动时不应包含无变动标记")
	}

	// 大 patch 被截断到上限内
	big := strings.Repeat("line of text\n", 2000)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDiffSummary(ctx, dir); len(got) > gitDiffSummaryMax {
		t.Errorf("摘要长度 %d 超过上限 %d", len(got), gitDiffSummaryMax)
	}
}

func TestSettleRunRecordsChangedFiles(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	clock := newFakeClock(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	svc, s := setupTestService(t, clock, driver)
	defer s.Close()
	svc.opts.Projects = &fakeProjects{root: repo}

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "proj-1",
		Name:      "changed-files",
		Prompt:    "run",
		Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: 60}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	clock.Advance(60 * time.Second)
	svc.Tick(ctx)
	runs, _ := svc.Runs(ctx, def.ID, 1)
	threadID := *runs[0].ThreadID

	// 一个已跟踪文件被改动，一个未跟踪文件，另一处重复不应被重复计数
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver.setThreadState(threadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	svc.Tick(ctx)

	settled, _ := svc.Runs(ctx, def.ID, 1)
	if settled[0].ChangedFiles == nil || *settled[0].ChangedFiles != 2 {
		t.Fatalf("期望 ChangedFiles=2，实际 %v", settled[0].ChangedFiles)
	}
}

func TestGitChangedFilesNonRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := gitChangedFiles(context.Background(), t.TempDir()); got != nil {
		t.Errorf("非 git 目录应返回 nil，实际 %d", *got)
	}
}
