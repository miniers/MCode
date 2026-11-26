package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/kanban"
)

func newHumanApproveLandingManager(t *testing.T) (*Manager, *fakeDriver, string) {
	t.Helper()
	runner, root := initTestRepo(t)
	writePipelineFile(t, root, "lines.txt", landingBase)
	gitTestRun(t, root, "add", "lines.txt")
	gitTestRun(t, root, "commit", "-qm", "base lines")
	driver := newFakeDriver()
	m := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: filepath.Join(t.TempDir(), "worktrees"),
	})
	m.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
			AutoMerge:     true,
			ReviewMode:    "human",
		}
	})
	return m, driver, root
}

func TestManager_HumanApprove_ConflictWaitRetriesOnTick(t *testing.T) {
	t.Parallel()
	m, driver, root := newHumanApproveLandingManager(t)
	ctx := context.Background()
	const leadID = "lead-human-approve-wait"

	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  root,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "修改文本", Prompt: "修改文本"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]

	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})

	m.Tick(ctx)
	got := m.State(leadID).Bees[0]
	if got.Status != StatusCompleted || !got.PendingMerge {
		t.Fatalf("未进入待审查状态: %+v", got)
	}

	before := gitTestRun(t, root, "rev-parse", "HEAD")
	userText := strings.Replace(landingBase, "one\n", "user\n", 1)
	writePipelineFile(t, root, "lines.txt", userText)

	approved, err := m.ApproveMerge(ctx, leadID, bee.ThreadID)
	if err == nil {
		t.Fatalf("有未提交冲突时 ApproveMerge 应返回等待错误")
	}
	if !approved.PendingMerge || approved.MergeWaitSince == nil || approved.Merged {
		t.Fatalf("批准遇冲突后应保留等待状态: %+v", approved)
	}
	if head := gitTestRun(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatalf("等待时移动了 HEAD: got=%s want=%s", head, before)
	}
	assertLandingFile(t, root, userText)

	writePipelineFile(t, root, "lines.txt", landingBase)
	m.Tick(ctx)

	got = m.State(leadID).Bees[0]
	if !got.Merged || got.PendingMerge || got.MergeWaitSince != nil {
		t.Fatalf("Tick 重试后应完成合入并清除等待: %+v", got)
	}
	assertLandingCommit(t, m, root, got)
	assertLandingFile(t, root, beeText)
}

func TestManager_DiscardMerge_ClearsMergeWait(t *testing.T) {
	t.Parallel()
	m, driver, root := newHumanApproveLandingManager(t)
	ctx := context.Background()
	const leadID = "lead-discard-wait"

	bees, err := m.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  root,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "修改文本", Prompt: "修改文本"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	bee := bees[0]

	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})

	m.Tick(ctx)
	userText := strings.Replace(landingBase, "one\n", "user\n", 1)
	writePipelineFile(t, root, "lines.txt", userText)

	if _, err := m.ApproveMerge(ctx, leadID, bee.ThreadID); err == nil {
		t.Fatalf("有未提交冲突时 ApproveMerge 应返回等待错误")
	}
	if got := m.State(leadID).Bees[0]; !got.PendingMerge || got.MergeWaitSince == nil {
		t.Fatalf("前置等待状态未建立: %+v", got)
	}

	if err := m.DiscardMerge(ctx, leadID, bee.ThreadID); err != nil {
		t.Fatalf("DiscardMerge 失败: %v", err)
	}
	got := m.State(leadID).Bees[0]
	if got.MergeWaitSince != nil || got.PendingMerge || !got.MergeDiscarded || got.Merged {
		t.Fatalf("DiscardMerge 后状态异常: %+v", got)
	}

	writePipelineFile(t, root, "lines.txt", landingBase)
	m.Tick(ctx)

	got = m.State(leadID).Bees[0]
	if got.Merged || got.PendingMerge || got.MergeWaitSince != nil || !got.MergeDiscarded {
		t.Fatalf("DiscardMerge 后 Tick 不应重新合并: %+v", got)
	}
	assertLandingFile(t, root, landingBase)
}
