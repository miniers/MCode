package swarm

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// restoreLeadForResume 造一条「重启前 running 被中断 + 一条仍在排队」的主会话记录并完成恢复。
func restoreLeadForResume(t *testing.T, leadThreadID string) (*Manager, *fakePersister, string) {
	t.Helper()
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	persister := newFakePersister()

	rec := LeadRecord{
		LeadThreadID: leadThreadID,
		Enabled:      true,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		NextIndex:    2,
		// 请求开始时间是很久以前：重启期间耗掉的预算不应算进续跑。
		RequestStartedAt: time.Now().Add(-10 * time.Hour),
		Bees: []Bee{
			{
				ThreadID:      "bee-run-1",
				Index:         0,
				Name:          "Aspen",
				Task:          "运行中被重启",
				Prompt:        "运行中被重启",
				Status:        StatusRunning,
				ProjectID:     "proj-1",
				TargetWorkDir: dir,
				Reported:      true,
			},
			{
				ThreadID: "bee-done-2",
				Index:    1,
				Name:     "Birch",
				Task:     "已完成",
				Status:   StatusCompleted,
				Reported: true,
			},
		},
		Queue: []QueuedBeeRecord{
			{
				ProjectID:   "proj-1",
				LeadWorkDir: dir,
				Index:       2,
				Spec:        BeeSpec{Name: "Cedar", Task: "排队中", Prompt: "排队中"},
			},
		},
	}
	if err := persister.SaveLead(ctx, rec); err != nil {
		t.Fatalf("SaveLead failed: %v", err)
	}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	return mgr, persister, dir
}

// 验证 Restore 给被中断的 bee 打上 Interrupted 标记、排队条目进 heldQueue 而不进派发队列，
// 并且 State.Interrupted 同时统计两者。
func TestManager_Restore_MarksInterruptedAndHoldsQueue(t *testing.T) {
	const leadID = "lead-resume-restore"
	mgr, persister, _ := restoreLeadForResume(t, leadID)

	st := mgr.State(leadID)
	if len(st.Bees) != 2 {
		t.Fatalf("期望恢复 2 个 bee，实际 %d", len(st.Bees))
	}
	if !st.Bees[0].Interrupted {
		t.Fatalf("被重启打断的 bee 应带 Interrupted 标记：%+v", st.Bees[0])
	}
	if st.Bees[0].Status != StatusStopped {
		t.Fatalf("被打断的 bee 应为 stopped，实际 %s", st.Bees[0].Status)
	}
	if st.Bees[1].Interrupted {
		t.Fatalf("已完成的 bee 不应带 Interrupted 标记：%+v", st.Bees[1])
	}
	if st.Interrupted != 2 {
		t.Fatalf("期望 State.Interrupted=2（1 条被打断 bee + 1 条排队条目），实际 %d", st.Interrupted)
	}

	// 恢复出的排队条目不能被自动派发。
	if got := mgr.QueueLength(); got != 0 {
		t.Fatalf("恢复出的排队条目不应进入 m.queue，实际队列长度 %d", got)
	}
	if st.Queued != 0 {
		t.Fatalf("期望 State.Queued=0，实际 %d", st.Queued)
	}

	// 落盘记录里排队条目仍在，下次重启不会丢。
	persisted := persister.leads[leadID]
	if len(persisted.Queue) != 1 {
		t.Fatalf("期望落盘 1 条排队记录，实际 %d", len(persisted.Queue))
	}
	if persisted.Queue[0].Spec.Name != "Cedar" || persisted.Queue[0].Index != 2 {
		t.Fatalf("排队记录内容不符：%+v", persisted.Queue[0])
	}
}

func TestManager_Restore_PreservesFinishedAt(t *testing.T) {
	const leadID = "lead-resume-finished-at"
	ctx := context.Background()
	finishedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	waitSince := finishedAt.Add(time.Minute)
	restoredAt := finishedAt.Add(time.Hour)
	persister := newFakePersister()
	if err := persister.SaveLead(ctx, LeadRecord{
		LeadThreadID: leadID,
		Bees: []Bee{
			{ThreadID: "waiting", Status: StatusRunning, FinishedAt: &finishedAt, MergeWaitSince: &waitSince},
			{ThreadID: "running", Status: StatusRunning},
		},
	}); err != nil {
		t.Fatalf("SaveLead failed: %v", err)
	}
	mgr := NewManager(Options{Persister: persister, Now: func() time.Time { return restoredAt }})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	bees := mgr.State(leadID).Bees
	if len(bees) != 2 {
		t.Fatalf("期望恢复 2 个 bee，实际 %d", len(bees))
	}
	if bees[0].FinishedAt == nil || !bees[0].FinishedAt.Equal(finishedAt) {
		t.Fatalf("落地等待的结束时间应保持不变，实际 %v", bees[0].FinishedAt)
	}
	if bees[1].FinishedAt == nil || !bees[1].FinishedAt.Equal(restoredAt) {
		t.Fatalf("运行中的结束时间应为恢复时刻，实际 %v", bees[1].FinishedAt)
	}
}

// 验证 DismissInterrupted 清掉中断标记与 heldQueue，并写回持久化记录。
func TestManager_DismissInterrupted(t *testing.T) {
	const leadID = "lead-resume-dismiss"
	mgr, persister, _ := restoreLeadForResume(t, leadID)

	if got := mgr.DismissInterrupted(leadID); got != 2 {
		t.Fatalf("期望清除 2 条（1 个 bee + 1 条排队），实际 %d", got)
	}

	st := mgr.State(leadID)
	if st.Interrupted != 0 {
		t.Fatalf("放弃续跑后 State.Interrupted 应为 0，实际 %d", st.Interrupted)
	}
	if st.Bees[0].Interrupted {
		t.Fatalf("放弃续跑后不应再带 Interrupted 标记：%+v", st.Bees[0])
	}
	if st.Bees[0].Status != StatusStopped {
		t.Fatalf("放弃续跑后 bee 应保持 stopped，实际 %s", st.Bees[0].Status)
	}
	if !strings.Contains(st.Bees[0].Summary, "已放弃续跑") {
		t.Fatalf("放弃续跑后 Summary 应记下「已放弃续跑」，实际 %q", st.Bees[0].Summary)
	}
	if len(persister.leads[leadID].Queue) != 0 {
		t.Fatalf("放弃续跑后落盘排队记录应清空，实际 %+v", persister.leads[leadID].Queue)
	}

	// 再放弃一次已经没有可清理的条目。
	if got := mgr.DismissInterrupted(leadID); got != 0 {
		t.Fatalf("第二次放弃应返回 0，实际 %d", got)
	}
}

// 验证没有中断项时 ResumeInterrupted 返回 0 且不报错；主会话不存在时返回 ErrLeadNotFound。
func TestManager_ResumeInterrupted_NoInterrupted(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{Persister: newFakePersister()})
	mgr.SetEnabled("lead-resume-none", true)

	n, err := mgr.ResumeInterrupted(ctx, "lead-resume-none")
	if err != nil {
		t.Fatalf("没有中断项时不应报错，实际 %v", err)
	}
	if n != 0 {
		t.Fatalf("没有中断项时应返回 0，实际 %d", n)
	}

	if _, err := mgr.ResumeInterrupted(ctx, "lead-resume-missing"); !errors.Is(err, ErrLeadNotFound) {
		t.Fatalf("主会话不存在时应返回 ErrLeadNotFound，实际 %v", err)
	}
}

// 验证 ResumeInterrupted 续跑被打断的 bee：新 bee 替换旧条目且不再带 Interrupted，
// 旧 worktree 已不存在时按新建处理，续跑出的 bee 不再自动重试。
func TestManager_ResumeInterrupted_ReplacesInterruptedBee(t *testing.T) {
	const leadID = "lead-resume-ok"
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	persister := newFakePersister()

	goneWorktree := filepath.Join(t.TempDir(), "gone")
	staleStart := time.Now().Add(-10 * time.Hour)
	if err := persister.SaveLead(ctx, LeadRecord{
		LeadThreadID:     leadID,
		Enabled:          true,
		ProjectID:        "proj-1",
		LeadWorkDir:      dir,
		NextIndex:        1,
		RequestStartedAt: staleStart,
		Bees: []Bee{
			{
				ThreadID:      "bee-old",
				Index:         0,
				Name:          "Aspen",
				Task:          "继续做",
				Prompt:        "原始 brief",
				Status:        StatusRunning,
				ProjectID:     "proj-1",
				TargetWorkDir: dir,
				WorktreePath:  goneWorktree,
				Branch:        "mcode/swarm/lead-res/0",
				BaseCommit:    "abc",
				Reported:      true,
			},
		},
	}); err != nil {
		t.Fatalf("SaveLead failed: %v", err)
	}

	driver := newFakeDriver()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if st := mgr.State(leadID); !st.Bees[0].Interrupted {
		t.Fatalf("恢复后应带 Interrupted 标记：%+v", st.Bees[0])
	}

	n, err := mgr.ResumeInterrupted(ctx, leadID)
	if err != nil {
		t.Fatalf("ResumeInterrupted failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望续跑 1 条，实际 %d", n)
	}

	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("期望替换后仍有 1 个 bee，实际 %d", len(st.Bees))
	}
	resumed := st.Bees[0]
	if resumed.ThreadID == "bee-old" {
		t.Fatalf("旧 bee 应被新 bee 替换，实际仍是 %s", resumed.ThreadID)
	}
	if resumed.Interrupted {
		t.Fatalf("续跑出的新 bee 不应带 Interrupted 标记：%+v", resumed)
	}
	if resumed.Status != StatusRunning {
		t.Fatalf("续跑出的新 bee 应为 running，实际 %s", resumed.Status)
	}
	if resumed.Index != 0 {
		t.Fatalf("续跑出的新 bee 应沿用原 Index=0，实际 %d", resumed.Index)
	}
	if st.Interrupted != 0 {
		t.Fatalf("续跑后 State.Interrupted 应为 0，实际 %d", st.Interrupted)
	}
	if got := mgr.getRetryCount(resumed.ThreadID); got != 1 {
		t.Fatalf("续跑出的 bee 应标记为已用掉重试机会，实际 %d", got)
	}

	// 重启期间消耗的时间不计入本次请求预算：resume 后起始时间应被刷新。
	mgr.mu.Lock()
	started := mgr.leads[leadID].requestStartedAt
	mgr.mu.Unlock()
	if !started.After(staleStart) {
		t.Fatalf("续跑后 requestStartedAt 应被刷新，实际仍为 %v", started)
	}

	persisted := persister.leads[leadID]
	if persisted.Bees[0].ThreadID != resumed.ThreadID || persisted.Bees[0].Interrupted {
		t.Fatalf("落盘记录应保存续跑后的新 bee：%+v", persisted.Bees[0])
	}
}

// 验证 ResumeInterrupted 会把 heldQueue 里的排队条目重新排入派发队列，从而真正派发出去。
func TestManager_ResumeInterrupted_RequeuesHeldQueue(t *testing.T) {
	const leadID = "lead-resume-queue"
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	persister := newFakePersister()

	if err := persister.SaveLead(ctx, LeadRecord{
		LeadThreadID: leadID,
		Enabled:      true,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		NextIndex:    1,
		Queue: []QueuedBeeRecord{
			{
				ProjectID:   "proj-1",
				LeadWorkDir: dir,
				Index:       0,
				Spec:        BeeSpec{Name: "Cedar", Task: "排队中", Prompt: "排队中"},
			},
		},
	}); err != nil {
		t.Fatalf("SaveLead failed: %v", err)
	}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
		Persister:   persister,
	})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	n, err := mgr.ResumeInterrupted(ctx, leadID)
	if err != nil {
		t.Fatalf("ResumeInterrupted failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望把 1 条排队条目重新入队，实际 %d", n)
	}

	st := mgr.State(leadID)
	if st.Interrupted != 0 {
		t.Fatalf("续跑后 State.Interrupted 应为 0，实际 %d", st.Interrupted)
	}
	if len(st.Bees) != 1 {
		t.Fatalf("排队条目应被派发成 1 个 bee，实际 %d", len(st.Bees))
	}
	if st.Bees[0].Name != "Cedar" || st.Bees[0].Status != StatusRunning {
		t.Fatalf("派发结果不符：%+v", st.Bees[0])
	}
	if got := mgr.QueueLength(); got != 0 {
		t.Fatalf("派发后队列应清空，实际 %d", got)
	}
}

// 验证 AutoClearFinished 打开时，被打断的 bee 与待决定的 heldQueue 都不会被自动清理，
// 否则用户来不及续跑或放弃就被清掉了。
func TestManager_AutoClearFinished_KeepsInterrupted(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoClearFinished: true}
	})

	mgr.mu.Lock()
	// 只有被打断的 bee、没有 heldQueue。
	mgr.leads["lead-int"] = &leadState{leadThreadID: "lead-int", bees: []Bee{
		{ThreadID: "h-int", Status: StatusStopped, Reported: true, Interrupted: true},
	}}
	// 已完成但重启后仍留着排队条目，同样必须保留。
	mgr.leads["lead-held"] = &leadState{
		leadThreadID: "lead-held",
		bees:         []Bee{{ThreadID: "h-held", Status: StatusCompleted, Reported: true}},
		heldQueue:    []queuedBee{{leadThreadID: "lead-held", index: 1}},
	}
	// 没有中断项的 lead 照旧清理，确认拦截只针对中断项。
	mgr.leads["lead-done"] = &leadState{leadThreadID: "lead-done", bees: []Bee{
		{ThreadID: "h-done", Status: StatusCompleted, Reported: true},
	}}
	mgr.mu.Unlock()

	mgr.autoClearFinished(ctx)

	for _, id := range []string{"lead-int", "lead-held"} {
		if got := len(mgr.State(id).Bees); got != 1 {
			t.Fatalf("%s 有中断项时不应被自动清理，实际剩 %d 个", id, got)
		}
	}
	if got := len(mgr.State("lead-done").Bees); got != 0 {
		t.Fatalf("没有中断项时仍应自动清理，实际剩 %d 个", got)
	}
}

// 验证 ClearFinished 只清掉真正结束的 bee，被打断的 bee 保留到用户续跑或放弃。
func TestManager_ClearFinished_KeepsInterruptedBee(t *testing.T) {
	const leadID = "lead-clear-interrupted"
	ctx := context.Background()
	mgr, _, _ := restoreLeadForResume(t, leadID)

	if err := mgr.ClearFinished(ctx, leadID); err != nil {
		t.Fatalf("ClearFinished failed: %v", err)
	}

	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("期望只保留被打断的 bee，实际剩 %d 个：%+v", len(st.Bees), st.Bees)
	}
	if st.Bees[0].ThreadID != "bee-run-1" || !st.Bees[0].Interrupted {
		t.Fatalf("保留的应是被重启打断的 bee：%+v", st.Bees[0])
	}
}

// 验证有被打断的 bee 或 heldQueue 时，侧栏不隐藏 bee 卡片，否则用户找不到续跑入口。
func TestManager_BeesHidden_KeepsInterrupted(t *testing.T) {
	const leadID = "lead-hidden-interrupted"
	mgr, _, _ := restoreLeadForResume(t, leadID)
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoHidesIdleBees: true}
	})

	// 被打断的 bee 与 heldQueue 都在时不可隐藏。
	if mgr.BeesHidden(leadID) {
		t.Fatal("有被打断的 bee 与 heldQueue 时不应隐藏")
	}

	// 只去掉 heldQueue，仍有被打断的 bee：仍不可隐藏。
	mgr.mu.Lock()
	mgr.leads[leadID].heldQueue = nil
	mgr.mu.Unlock()
	if mgr.BeesHidden(leadID) {
		t.Fatal("有被打断的 bee 时不应隐藏")
	}

	// 中断项全部处理完后，仅剩已结束的 bee 时才允许隐藏。
	mgr.mu.Lock()
	mgr.leads[leadID].bees = []Bee{{ThreadID: "bee-done-2", Status: StatusCompleted, Reported: true}}
	mgr.mu.Unlock()
	if !mgr.BeesHidden(leadID) {
		t.Fatal("没有中断项且 bee 都已结束时应隐藏")
	}
}
