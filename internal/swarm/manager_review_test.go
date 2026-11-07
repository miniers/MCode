package swarm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcode/internal/kanban"
)

// humanReviewSettings 让管理器按人工审查档运行，替代已删除的 Options.ReviewBeforeMerge。
// 未显式注入的设置字段保持 Settings() 的默认值，与旧测试依赖的口径一致。
func humanReviewSettings(m *Manager) *Manager {
	m.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
			ReviewMode:    "human",
		}
	})
	return m
}

func pendingReviewBee(t *testing.T) (*Manager, string, Bee) {
	t.Helper()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	m := humanReviewSettings(NewManager(Options{
		Git: runner, Driver: driver, WorktreeDir: t.TempDir(),
		Persister: newFakePersister(),
	}))
	bees, err := m.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: "lead-review", LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Aspen", Task: "新增文件"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bees[0].WorktreePath, "review.txt"), []byte("reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{LastAssistantText: "已新增 review.txt"})
	m.Tick(context.Background())
	bee := m.State("lead-review").Bees[0]
	if !bee.PendingMerge {
		t.Fatal("bee 未进入待审查状态")
	}
	return m, dir, bee
}

func TestReviewMerge_UsesPersistedSnapshot(t *testing.T) {
	m, dir, bee := pendingReviewBee(t)
	ctx := context.Background()
	reviewed, err := m.MergeDiff(ctx, "lead-review", bee.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewed) == 0 {
		t.Fatal("缺少审查补丁")
	}
	// 返回值和 State 快照不能反向修改管理器内的补丁。
	reviewed[0] = '!'
	bee.MergePatch[0] = '?'
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "review.txt"), []byte("not reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bee.WorktreePath, "extra.txt"), []byte("not reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	restarted := NewManager(m.opts)
	if err := restarted.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	patch, err := restarted.MergeDiff(ctx, "lead-review", bee.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(patch, []byte("not reviewed")) || patch[0] != 'd' {
		t.Fatalf("审查快照被改变: %s", patch)
	}
	approved, err := restarted.ApproveMerge(ctx, "lead-review", bee.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if !approved.Merged || approved.PendingMerge {
		t.Fatalf("批准状态错误: %+v", approved)
	}
	got, err := os.ReadFile(filepath.Join(dir, "review.txt"))
	if err != nil || string(got) != "reviewed\n" {
		t.Fatalf("合入了未审查内容: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("不应合入新增文件: %v", err)
	}
	after, err := restarted.MergeDiff(ctx, "lead-review", bee.ThreadID)
	if err != nil || !bytes.Equal(patch, after) {
		t.Fatalf("批准后的差异不再是审查快照: %v", err)
	}
}

func TestReviewMerge_ApplyFailureRemainsPending(t *testing.T) {
	m, dir, bee := pendingReviewBee(t)
	ctx := context.Background()
	m.mu.Lock()
	m.leads["lead-review"].bees[0].TargetWorkDir = filepath.Join(dir, "missing")
	m.mu.Unlock()
	failed, err := m.ApproveMerge(ctx, "lead-review", bee.ThreadID)
	if err == nil || failed.MergeError == "" || !failed.PendingMerge || failed.Merged {
		t.Fatalf("合并失败应返回错误并保留待审查: %+v, %v", failed, err)
	}
	if err := m.ClearFinished(ctx, "lead-review"); err != nil {
		t.Fatal(err)
	}
	if len(m.State("lead-review").Bees) != 1 {
		t.Fatal("失败的待审查 bee 被清理")
	}
	m.mu.Lock()
	m.leads["lead-review"].bees[0].TargetWorkDir = dir
	m.mu.Unlock()
	approved, err := m.ApproveMerge(ctx, "lead-review", bee.ThreadID)
	if err != nil || !approved.Merged || approved.MergeError != "" {
		t.Fatalf("修复目标后应可重试: %+v, %v", approved, err)
	}
}

func setupReviewBeeWithFile(t *testing.T, leadID, initialContent, beeContent string) (*Manager, string, Bee) {
	t.Helper()
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	fileInMain := filepath.Join(dir, "review.txt")
	if err := os.WriteFile(fileInMain, []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CommitPaths(ctx, dir, []string{"review.txt"}, "base commit"); err != nil {
		t.Fatal(err)
	}

	driver := newFakeDriver()
	m := humanReviewSettings(NewManager(Options{
		Git: runner, Driver: driver, WorktreeDir: t.TempDir(),
		Persister: newFakePersister(),
	}))
	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID, LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Aspen", Task: "修改文件"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bees[0].WorktreePath, "review.txt"), []byte(beeContent), 0644); err != nil {
		t.Fatal(err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{LastAssistantText: "已修改 review.txt"})
	m.Tick(ctx)
	bee := m.State(leadID).Bees[0]
	if !bee.PendingMerge {
		t.Fatal("bee 未进入待审查状态")
	}
	return m, dir, bee
}

func TestReviewMerge_RefusesWhenTargetHasLocalEdits(t *testing.T) {
	ctx := context.Background()
	baseContent := "line 1\nline 2\nline 3\n"

	// (a) 用户的未提交改动与 bee 的改动在同一文件的不同行时，批准成功，主工作区同时保留两处改动
	{
		beeContent := "line 1\nline 2\nline 3 from bee\n"
		m, dir, bee := setupReviewBeeWithFile(t, "lead-review-clean", baseContent, beeContent)
		userFile := filepath.Join(dir, "review.txt")
		userContent := "line 1 from user\nline 2\nline 3\n"
		if err := os.WriteFile(userFile, []byte(userContent), 0644); err != nil {
			t.Fatal(err)
		}

		approved, err := m.ApproveMerge(ctx, "lead-review-clean", bee.ThreadID)
		if err != nil || !approved.Merged || approved.PendingMerge {
			t.Fatalf("不同行改动应批准成功: %+v, %v", approved, err)
		}
		expected := "line 1 from user\nline 2\nline 3 from bee\n"
		got, err := os.ReadFile(userFile)
		if err != nil || string(got) != expected {
			t.Fatalf("主工作区应同时保留两处改动，期望:\n%q\n实际:\n%q, err: %v", expected, string(got), err)
		}
	}

	// (b) 两者在同一行时，批准返回包含「冲突」的错误，主工作区文件与批准前完全一致、不含冲突标记，bee 仍处于待审查状态
	{
		beeContent := "line 1\nline 2 from bee\nline 3\n"
		m, dir, bee := setupReviewBeeWithFile(t, "lead-review-conflict", baseContent, beeContent)
		userFile := filepath.Join(dir, "review.txt")
		userContent := "line 1\nline 2 from user\nline 3\n"
		if err := os.WriteFile(userFile, []byte(userContent), 0644); err != nil {
			t.Fatal(err)
		}

		got, err := m.ApproveMerge(ctx, "lead-review-conflict", bee.ThreadID)
		if err == nil || !strings.Contains(err.Error(), "冲突") {
			t.Fatalf("冲突时应返回包含「冲突」的错误: %v", err)
		}
		if got.Merged {
			t.Fatalf("冲突时不应标记为 Merged: %+v", got)
		}
		stBee := m.State("lead-review-conflict").Bees[0]
		if !stBee.PendingMerge {
			t.Fatalf("冲突后 bee 应保持 PendingMerge: %+v", stBee)
		}
		if len(stBee.ConflictFiles) == 0 {
			t.Fatalf("冲突后 ConflictFiles 应包含冲突文件: %+v", stBee)
		}
		content, err := os.ReadFile(userFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != userContent {
			t.Fatalf("主工作区文件应与批准前完全一致，期望:\n%q\n实际:\n%q", userContent, string(content))
		}
		if strings.Contains(string(content), "<<<<<<<") {
			t.Fatalf("主工作区不应包含冲突标记: %q", string(content))
		}
	}
}

func TestReviewMerge_ConcurrentDecisions(t *testing.T) {
	m, _, bee := pendingReviewBee(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := m.ApproveMerge(context.Background(), "lead-review", bee.ThreadID)
		results <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		results <- m.DiscardMerge(context.Background(), "lead-review", bee.ThreadID)
	}()
	close(start)
	wg.Wait()
	close(results)
	var accepted, rejected int
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrNoPendingMerge):
			rejected++
		default:
			t.Fatal(err)
		}
	}
	final := m.State("lead-review").Bees[0]
	if accepted != 1 || rejected != 1 || final.PendingMerge || final.Merged == final.MergeDiscarded {
		t.Fatalf("批准与放弃必须互斥: accepted=%d rejected=%d bee=%+v", accepted, rejected, final)
	}
}

// TestReviewMerge_ReportsOnlyAfterAllDecisions 覆盖待审查期间的汇报语义：整批 bee 都等审查有结论，
// 期间不汇报；最后一个决定做出后的下一个 Tick 一次性汇报全部 bee，之后不再重复投递。
func TestReviewMerge_ReportsOnlyAfterAllDecisions(t *testing.T) {
	m, dir, first := pendingReviewBee(t)
	ctx := context.Background()
	driver := m.opts.Driver.(*fakeDriver)
	// 主会话显式登记为未运行：汇报只在主会话空闲的 tick 投递。
	driver.setSessionState("lead-review", kanban.SessionState{Running: false})
	var reports []string
	m.opts.OnReport = func(_, message string) error {
		reports = append(reports, message)
		return nil
	}
	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: "lead-review", LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Birch", Task: "另一项改动"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second := bees[0]
	if err := os.WriteFile(filepath.Join(second.WorktreePath, "other.txt"), []byte("other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	driver.setSessionState(second.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "已新增 other.txt"})
	// bee 结束后的第一次 Tick：两个 bee 都待审查，不汇报。
	m.Tick(ctx)
	if len(reports) != 0 {
		t.Fatalf("审查决定前不应汇报: %v", reports)
	}
	// 待审查结果必须保留，即使还没有汇报。
	if err := m.ClearFinished(ctx, "lead-review"); err != nil {
		t.Fatal(err)
	}
	if len(m.State("lead-review").Bees) != 2 {
		t.Fatal("审查决定前不应清理待审查结果")
	}
	if _, err := m.ApproveMerge(ctx, "lead-review", first.ThreadID); err != nil {
		t.Fatal(err)
	}
	m.Tick(ctx)
	if len(reports) != 0 {
		t.Fatalf("仍有 bee 待审查时不应汇报: %v", reports)
	}
	if err := m.DiscardMerge(ctx, "lead-review", second.ThreadID); err != nil {
		t.Fatal(err)
	}
	m.Tick(ctx)
	if len(reports) != 1 {
		t.Fatalf("审查决定完成后应一次性汇报: %v", reports)
	}
	for _, name := range []string{first.Name, second.Name} {
		if !bytes.Contains([]byte(reports[0]), []byte(name)) {
			t.Fatalf("汇报缺少 bee 名字 %s: %s", name, reports[0])
		}
	}
	for _, h := range m.State("lead-review").Bees {
		if !h.Reported {
			t.Fatalf("汇报后 bee 应标记为已汇报: %+v", h)
		}
	}
	m.Tick(ctx)
	if len(reports) != 1 {
		t.Fatalf("不应重复汇报: %v", reports)
	}
	restarted := NewManager(m.opts)
	if err := restarted.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	state := restarted.State("lead-review")
	if !state.Bees[0].Merged || !state.Bees[1].MergeDiscarded || state.Bees[1].PendingMerge {
		t.Fatalf("审查决定未保存: %+v", state)
	}
	if err := m.ClearFinished(ctx, "lead-review"); err != nil {
		t.Fatal(err)
	}
	if len(m.State("lead-review").Bees) != 0 {
		t.Fatal("审查决定完成后应允许清理")
	}
}

// TestBuildReport_DiscardedBeeMentionsAbandoned 确保已放弃合并的 bee 在汇报里标注落地状态。
func TestBuildReport_DiscardedBeeMentionsAbandoned(t *testing.T) {
	report := BuildReport([]Bee{{
		Name:           "Aspen",
		Task:           "新增文件",
		Status:         StatusCompleted,
		Merged:         false,
		PendingMerge:   false,
		MergeDiscarded: true,
		Summary:        "已新增 review.txt",
	}})
	if !bytes.Contains([]byte(report), []byte("已放弃合并")) {
		t.Fatalf("汇报应标注已放弃合并: %s", report)
	}
	if !bytes.Contains([]byte(report), []byte("Aspen")) {
		t.Fatalf("汇报应包含 bee 名字: %s", report)
	}
}
