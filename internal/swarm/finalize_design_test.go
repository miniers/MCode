package swarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
)

func TestFinalizeDesignBee(t *testing.T) {
	t.Parallel()

	t.Run("defer_and_finalize_flow", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner, dir := initTestRepo(t)
		clock := newTestClock()
		driver := newFakeDriver()

		var reportMu sync.Mutex
		var reports []string
		var landedBatches []LandedBatch
		idleCalls := 0

		mgr := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			Now:         clock.Now,
			OnReport: func(_ string, msg string) error {
				reportMu.Lock()
				reports = append(reports, msg)
				reportMu.Unlock()
				return nil
			},
			OnBeesIdle: func(context.Context, string) {
				reportMu.Lock()
				idleCalls++
				reportMu.Unlock()
			},
			OnBatchLanded: func(_ context.Context, b LandedBatch) {
				reportMu.Lock()
				landedBatches = append(landedBatches, b)
				reportMu.Unlock()
			},
		})
		mgr.SetSettingsFunc(func() Settings {
			return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
		})

		const leadID = "lead-finalize-flow"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Normal", Task: "普通任务", Prompt: "完成普通任务"},
				{Name: "Bee-Design", Task: "设计任务", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		normalBee := bees[0]
		designBee := bees[1]

		// (a) 普通 bee 与设计 bee 同批结束：只汇报普通 bee，设计 bee 保持待定稿且 lead 不再忙。
		driver.setSessionState(normalBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "normal done",
		})
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "design draft 1",
		})
		mgr.Tick(ctx)

		reportMu.Lock()
		if len(reports) != 1 {
			reportMu.Unlock()
			t.Fatalf("第一轮应只收到 1 次汇报，实际 %d 次: %v", len(reports), reports)
		}
		firstReport := reports[0]
		landedFirst := append([]LandedBatch(nil), landedBatches...)
		firstIdleCalls := idleCalls
		reportMu.Unlock()

		if !strings.Contains(firstReport, "Bee-Normal") || !strings.Contains(firstReport, "normal done") {
			t.Fatalf("首轮汇报应包含普通 bee，实际:\n%s", firstReport)
		}
		if strings.Contains(firstReport, "Bee-Design") || strings.Contains(firstReport, "design draft 1") {
			t.Fatalf("首轮汇报不应包含待定稿设计 bee，实际:\n%s", firstReport)
		}
		if len(landedFirst) != 1 || len(landedFirst[0].Bees) != 1 || landedFirst[0].Bees[0].ThreadID != normalBee.ThreadID {
			t.Fatalf("首轮批次落地应只包含普通 bee，实际: %+v", landedFirst)
		}
		if firstIdleCalls == 0 {
			t.Fatal("普通 bee 与设计 bee 都结束后应触发 OnBeesIdle")
		}

		gotDesign := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if gotDesign.Status != StatusCompleted || !gotDesign.AwaitingFinalize || gotDesign.Reported || gotDesign.Delivered {
			t.Fatalf("设计 bee 应处于待定稿且未汇报/未上报落地，实际: %+v", gotDesign)
		}
		if mgr.Busy(leadID) {
			t.Fatal("仅剩待定稿设计 bee 时 lead 不应被判为忙")
		}

		// (b) 再次 Tick 若干次不会补发设计 bee 的汇报。
		for i := 0; i < 3; i++ {
			clock.Advance(2 * time.Second)
			mgr.Tick(ctx)
		}
		reportMu.Lock()
		reportCountAfterTicks := len(reports)
		reportMu.Unlock()
		if reportCountAfterTicks != 1 {
			t.Fatalf("后续 Tick 不应补发待定稿 bee 的汇报，实际 %d 次", reportCountAfterTicks)
		}
		gotDesign = beeByThread(t, mgr, leadID, designBee.ThreadID)
		if !gotDesign.AwaitingFinalize || gotDesign.Reported {
			t.Fatalf("后续 Tick 后设计 bee 仍应保持 AwaitingFinalize=true、Reported=false，实际: %+v", gotDesign)
		}

		// (c) 调 FinalizeDesignBee：置为 Running、发送固定指令；结束后 Tick 放行含「用户已定稿」的汇报。
		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err != nil {
			t.Fatalf("FinalizeDesignBee failed: %v", err)
		}
		driver.mu.Lock()
		followUps := append([]fakeFollowUp(nil), driver.followUps...)
		driver.mu.Unlock()
		if len(followUps) != 1 || followUps[0].threadID != designBee.ThreadID || followUps[0].text != finalizeDesignPrompt {
			t.Fatalf("应向设计 bee 发送定稿指令，实际: %+v", followUps)
		}
		gotDesign = beeByThread(t, mgr, leadID, designBee.ThreadID)
		if gotDesign.Status != StatusRunning || gotDesign.AwaitingFinalize || !gotDesign.FinalizeRequested || gotDesign.Reported {
			t.Fatalf("定稿后设计 bee 应回到 Running 且标记 FinalizeRequested=true，实际: %+v", gotDesign)
		}
		if !mgr.Busy(leadID) {
			t.Fatal("定稿指令发出后设计 bee 运行中，lead 应为忙")
		}

		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "最终设计稿路径：docs/design.md",
		})
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		reportMu.Lock()
		if len(reports) != 2 {
			reportMu.Unlock()
			t.Fatalf("定稿轮结束后应收到第 2 次汇报，实际 %d 次: %v", len(reports), reports)
		}
		secondReport := reports[1]
		landedAll := append([]LandedBatch(nil), landedBatches...)
		reportMu.Unlock()

		if !strings.Contains(secondReport, "Bee-Design") || !strings.Contains(secondReport, "用户已定稿") || !strings.Contains(secondReport, "docs/design.md") {
			t.Fatalf("定稿汇报应包含 Bee-Design、用户已定稿提示与最终内容，实际:\n%s", secondReport)
		}
		gotDesign = beeByThread(t, mgr, leadID, designBee.ThreadID)
		if gotDesign.Status != StatusCompleted || gotDesign.AwaitingFinalize || !gotDesign.FinalizeRequested || !gotDesign.Reported {
			t.Fatalf("定稿汇报后设计 bee 应完成且 Reported=true、AwaitingFinalize=false，实际: %+v", gotDesign)
		}
		if mgr.Busy(leadID) {
			t.Fatal("定稿汇报完成后 lead 不应继续为忙")
		}
		if len(landedAll) != 2 || len(landedAll[1].Bees) != 1 || landedAll[1].Bees[0].ThreadID != designBee.ThreadID {
			t.Fatalf("定稿完成后应触发设计 bee 的批次落地回调，实际: %+v", landedAll)
		}
	})

	t.Run("reject_invalid_targets_and_rollback_on_send_failure", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner, dir := initTestRepo(t)
		driver := newFakeDriver()

		mgr := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			OnReport:    func(string, string) error { return nil },
		})
		mgr.SetSettingsFunc(func() Settings {
			return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
		})

		const leadID = "lead-finalize-errors"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Normal", Task: "普通", Prompt: "P1"},
				{Name: "Bee-Design", Task: "设计", Prompt: "P2", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		normalBee := bees[0]
		designBee := bees[1]

		// 运行中的设计 bee（尚未进入 AwaitingFinalize）应返回错误。
		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err == nil {
			t.Fatal("未处于 AwaitingFinalize 的设计 bee 调用 FinalizeDesignBee 应返回错误")
		}
		if err := mgr.FinalizeDesignBee(ctx, "missing-lead", designBee.ThreadID); err == nil {
			t.Fatal("不存在的 lead 调用 FinalizeDesignBee 应返回错误")
		}
		if err := mgr.FinalizeDesignBee(ctx, leadID, "missing-bee"); err == nil {
			t.Fatal("不存在的 bee 调用 FinalizeDesignBee 应返回错误")
		}

		// 普通 bee 结束后仍不是设计档位，调用 FinalizeDesignBee 应返回错误。
		driver.setSessionState(normalBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "normal done",
		})
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "design draft",
		})
		mgr.Tick(ctx)

		if err := mgr.FinalizeDesignBee(ctx, leadID, normalBee.ThreadID); err == nil {
			t.Fatal("对普通 bee 调用 FinalizeDesignBee 应返回错误")
		}

		// 发送定稿指令失败时应回滚状态。
		driver.mu.Lock()
		driver.onFollowUp = func(string, string) error {
			return errors.New("send failed")
		}
		driver.mu.Unlock()

		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err == nil {
			t.Fatal("SendFollowUp 失败时 FinalizeDesignBee 应返回错误")
		}
		got := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if !got.AwaitingFinalize || got.FinalizeRequested || got.Status != StatusCompleted || !got.Merged || got.FinishedAt == nil {
			t.Fatalf("SendFollowUp 失败后应回滚到待定稿已完成状态，实际: %+v", got)
		}

		// 正常定稿并汇报后，AwaitingFinalize 已为 false，再次调用应报错。
		driver.mu.Lock()
		driver.onFollowUp = nil
		driver.mu.Unlock()
		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err != nil {
			t.Fatalf("恢复发送后 FinalizeDesignBee 不应失败: %v", err)
		}
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "final report",
		})
		mgr.Tick(ctx)

		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err == nil {
			t.Fatal("已定稿汇报的 bee 再次调用 FinalizeDesignBee 应返回错误")
		}
	})

	t.Run("reactivate_awaiting_finalize_bee_resets_flags", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner, dir := initTestRepo(t)
		clock := newTestClock()
		driver := newFakeDriver()

		var reportMu sync.Mutex
		var reports []string

		mgr := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			Now:         clock.Now,
			OnReport: func(_ string, msg string) error {
				reportMu.Lock()
				reports = append(reports, msg)
				reportMu.Unlock()
				return nil
			},
		})
		mgr.SetSettingsFunc(func() Settings {
			return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
		})

		const leadID = "lead-finalize-reactivate"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Design", Task: "设计任务", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		designBee := bees[0]

		// 第一轮结束进入 AwaitingFinalize（Reported=false, Merged=true）。
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "初稿完成",
		})
		mgr.Tick(ctx)

		got := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if !got.AwaitingFinalize || got.Reported || !got.Merged {
			t.Fatalf("第一轮结束后应处于 AwaitingFinalize 且未汇报，实际: %+v", got)
		}

		// 即使 ReviewState 意外非 None，AwaitingFinalize 的 bee 仍应被视为续跑候选。
		checkCandidate := got
		checkCandidate.ReviewState = ReviewNeedsHuman
		if !isReactivateCandidate(checkCandidate) {
			t.Fatal("AwaitingFinalize 且未中断的已完成 bee 应是续跑候选")
		}

		// 用户在设计 bee 会话里继续改稿：连续两次探测到 Running 后重新激活，且清零 AwaitingFinalize 与 FinalizeRequested。
		mgr.mu.Lock()
		for i := range mgr.leads[leadID].bees {
			if mgr.leads[leadID].bees[i].ThreadID == designBee.ThreadID {
				mgr.leads[leadID].bees[i].FinalizeRequested = true
			}
		}
		mgr.mu.Unlock()

		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           true,
			LastAssistantText: "初稿完成",
		})
		clock.Advance(reactivateProbeInterval)
		mgr.Tick(ctx)
		if mid := beeByThread(t, mgr, leadID, designBee.ThreadID); mid.Status != StatusCompleted {
			t.Fatalf("首次续跑探测不应立即激活，实际: %s", mid.Status)
		}

		clock.Advance(reactivateProbeInterval)
		mgr.Tick(ctx)
		got = beeByThread(t, mgr, leadID, designBee.ThreadID)
		if got.Status != StatusRunning || got.AwaitingFinalize || got.FinalizeRequested || got.Reported {
			t.Fatalf("续跑重新激活后应回到 Running 且清零 AwaitingFinalize/FinalizeRequested，实际: %+v", got)
		}

		// 改稿这一轮结束后应重新暂缓汇报，等待用户点定稿。
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "第二稿完成",
		})
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)
		got = beeByThread(t, mgr, leadID, designBee.ThreadID)
		if got.Status != StatusCompleted || !got.AwaitingFinalize || got.FinalizeRequested || got.Reported {
			t.Fatalf("续跑改稿结束后应重新进入待定稿暂缓汇报，实际: %+v", got)
		}
		reportMu.Lock()
		defer reportMu.Unlock()
		if len(reports) != 0 {
			t.Fatalf("续跑改稿结束后尚未定稿，不应发汇报，实际 %d 次: %v", len(reports), reports)
		}
	})
}

type finalizeHookDriver struct {
	*fakeDriver
	onSendFollowUp func(ctx context.Context, threadID, text string) error
}

func (d *finalizeHookDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	if d.onSendFollowUp != nil {
		if err := d.onSendFollowUp(ctx, threadID, text); err != nil {
			return err
		}
	}
	return d.fakeDriver.SendFollowUp(ctx, threadID, text)
}

func TestFinalizeDesignBeeNoStaleReport(t *testing.T) {
	t.Parallel()

	t.Run("no_stale_report_across_ticks", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner, dir := initTestRepo(t)
		clock := newTestClock()
		baseDriver := newFakeDriver()
		driver := &finalizeHookDriver{fakeDriver: baseDriver}

		var reportMu sync.Mutex
		var reports []string

		mgr := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			Now:         clock.Now,
			OnReport: func(_ string, msg string) error {
				reportMu.Lock()
				reports = append(reports, msg)
				reportMu.Unlock()
				return nil
			},
		})
		mgr.SetSettingsFunc(func() Settings {
			return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
		})

		const leadID = "lead-no-stale-report"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Design", Task: "设计任务", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		designBee := bees[0]

		const staleText = "旧初稿文本：design draft v1"
		const finalText = "最终定稿文本：docs/final-design.md"

		// 第一轮结束，LastAssistantText 为旧文本，进入待定稿状态。
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: staleText,
		})
		mgr.Tick(ctx)

		got := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if !got.AwaitingFinalize || got.Status != StatusCompleted || got.Reported {
			t.Fatalf("第一轮结束后应处于待定稿已完成状态，实际: %+v", got)
		}

		// 调用 FinalizeDesignBee 前穿插 Tick。
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		// 在 SendFollowUp 尚未把会话置为 Running 前穿插并发 Tick，模拟 UI 与 Tick 竞争窗口。
		driver.onSendFollowUp = func(callCtx context.Context, threadID, _ string) error {
			done := make(chan struct{})
			go func() {
				defer close(done)
				clock.Advance(2 * time.Second)
				mgr.Tick(callCtx)
			}()
			<-done

			// SendFollowUp 返回前，驱动会话已同步进入 Running（仍保留上一轮旧 LastAssistantText）。
			driver.setSessionState(threadID, kanban.SessionState{
				Running:           true,
				LastAssistantText: staleText,
			})
			return nil
		}

		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err != nil {
			t.Fatalf("FinalizeDesignBee failed: %v", err)
		}

		// 定稿轮仍在运行期间穿插 Tick，不应发出旧文本汇报。
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		reportMu.Lock()
		midReports := len(reports)
		reportMu.Unlock()
		if midReports != 0 {
			t.Fatalf("定稿轮未结束前不应发出任何汇报，实际 %d 次", midReports)
		}

		// 定稿轮完成并输出新文本后 Tick，应放行含新文本的定稿汇报。
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: finalText,
		})
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		reportMu.Lock()
		gotReports := append([]string(nil), reports...)
		reportMu.Unlock()
		if len(gotReports) != 1 {
			t.Fatalf("定稿轮结束后应只收到 1 次汇报，实际 %d 次: %v", len(gotReports), gotReports)
		}
		if !strings.Contains(gotReports[0], "用户已定稿") || !strings.Contains(gotReports[0], finalText) {
			t.Fatalf("定稿汇报应包含「用户已定稿」与新文本 %q，实际:\n%s", finalText, gotReports[0])
		}
		if strings.Contains(gotReports[0], staleText) {
			t.Fatalf("定稿汇报不应包含上一轮旧文本 %q，实际:\n%s", staleText, gotReports[0])
		}
	})

	t.Run("send_failure_keeps_awaiting_finalize", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner, dir := initTestRepo(t)
		clock := newTestClock()
		baseDriver := newFakeDriver()
		driver := &finalizeHookDriver{fakeDriver: baseDriver}

		var reportMu sync.Mutex
		var reports []string

		mgr := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			Now:         clock.Now,
			OnReport: func(_ string, msg string) error {
				reportMu.Lock()
				reports = append(reports, msg)
				reportMu.Unlock()
				return nil
			},
		})
		mgr.SetSettingsFunc(func() Settings {
			return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
		})

		const leadID = "lead-finalize-send-fail"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Design", Task: "设计任务", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		designBee := bees[0]

		const staleText = "旧初稿文本：design draft v1"
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: staleText,
		})
		mgr.Tick(ctx)

		before := beeByThread(t, mgr, leadID, designBee.ThreadID)
		driver.onSendFollowUp = func(context.Context, string, string) error {
			return errors.New("send failed")
		}

		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err == nil {
			t.Fatal("SendFollowUp 失败时 FinalizeDesignBee 应返回错误")
		}

		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		after := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if after.Status != StatusCompleted || !after.AwaitingFinalize || after.FinalizeRequested || after.Reported {
			t.Fatalf("发送失败后 bee 应保持 AwaitingFinalize 且状态未变，实际: %+v", after)
		}
		if after.Summary != before.Summary || after.Merged != before.Merged {
			t.Fatalf("发送失败后 bee 摘要与合并状态不应改变: before=%+v after=%+v", before, after)
		}

		reportMu.Lock()
		defer reportMu.Unlock()
		if len(reports) != 0 {
			t.Fatalf("发送失败后不应发出任何汇报，实际 %d 次: %v", len(reports), reports)
		}
	})

	t.Run("reactivated_during_send_still_sets_finalize_requested", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner, dir := initTestRepo(t)
		clock := newTestClock()
		baseDriver := newFakeDriver()
		driver := &finalizeHookDriver{fakeDriver: baseDriver}

		var reportMu sync.Mutex
		var reports []string

		mgr := NewManager(Options{
			Git:         runner,
			Driver:      driver,
			WorktreeDir: t.TempDir(),
			Now:         clock.Now,
			OnReport: func(_ string, msg string) error {
				reportMu.Lock()
				reports = append(reports, msg)
				reportMu.Unlock()
				return nil
			},
		})
		mgr.SetSettingsFunc(func() Settings {
			return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
		})

		const leadID = "lead-finalize-reactivate-race"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Design", Task: "设计任务", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		designBee := bees[0]

		const staleText = "旧初稿文本"
		const finalText = "定稿新文本"

		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: staleText,
		})
		mgr.Tick(ctx)

		// 模拟 SendFollowUp 已将驱动切到 Running 后，解锁期间 detectReactivatedBees 抢先将其重置为 Running（FinalizeRequested=false）。
		driver.onSendFollowUp = func(callCtx context.Context, threadID, _ string) error {
			driver.setSessionState(threadID, kanban.SessionState{
				Running:           true,
				LastAssistantText: staleText,
			})
			clock.Advance(reactivateProbeInterval)
			mgr.Tick(callCtx)
			clock.Advance(reactivateProbeInterval)
			mgr.Tick(callCtx)
			return nil
		}

		if err := mgr.FinalizeDesignBee(ctx, leadID, designBee.ThreadID); err != nil {
			t.Fatalf("FinalizeDesignBee failed: %v", err)
		}

		got := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if got.Status != StatusRunning || got.AwaitingFinalize || !got.FinalizeRequested {
			t.Fatalf("即使 SendFollowUp 期间被 detectReactivatedBees 抢先重置为 Running，仍应补上 FinalizeRequested=true，实际: %+v", got)
		}

		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: finalText,
		})
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		reportMu.Lock()
		gotReports := append([]string(nil), reports...)
		reportMu.Unlock()
		if len(gotReports) != 1 || !strings.Contains(gotReports[0], finalText) || strings.Contains(gotReports[0], staleText) {
			t.Fatalf("应只放行定稿轮新文本汇报，实际: %v", gotReports)
		}
	})
}
