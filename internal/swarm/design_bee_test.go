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

func TestFinalizeDesignBeeFromTurn(t *testing.T) {
	t.Parallel()

	t.Run("running_design_bee_finalizes_without_extra_prompt", func(t *testing.T) {
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

		const leadID = "lead-finalize-from-turn"
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

		if err := mgr.FinalizeDesignBeeFromTurn(ctx, normalBee.ThreadID); err == nil {
			t.Fatal("非设计 bee 调用 FinalizeDesignBeeFromTurn 应报错")
		}
		if err := mgr.FinalizeDesignBeeFromTurn(ctx, "missing-bee"); err == nil {
			t.Fatal("不存在的 bee 调用 FinalizeDesignBeeFromTurn 应报错")
		}

		if err := mgr.FinalizeDesignBeeFromTurn(ctx, designBee.ThreadID); err != nil {
			t.Fatalf("运行中的设计 bee 调用 FinalizeDesignBeeFromTurn 失败: %v", err)
		}
		driver.mu.Lock()
		followUps := append([]fakeFollowUp(nil), driver.followUps...)
		driver.mu.Unlock()
		if len(followUps) != 0 {
			t.Fatalf("运行中调用不应发送 finalizeDesignPrompt，实际发送了: %+v", followUps)
		}

		got := beeByThread(t, mgr, leadID, designBee.ThreadID)
		if !got.FinalizeRequested || got.AwaitingFinalize {
			t.Fatalf("运行中调用后应置 FinalizeRequested=true 且 AwaitingFinalize=false，实际: %+v", got)
		}

		driver.setSessionState(normalBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "normal done",
		})
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "最终方案路径：.mcode/design/panel.html",
		})
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		reportMu.Lock()
		gotReports := append([]string(nil), reports...)
		reportMu.Unlock()
		if len(gotReports) != 1 {
			t.Fatalf("应直接放行汇报，实际收到 %d 次: %v", len(gotReports), gotReports)
		}
		if !strings.Contains(gotReports[0], "Bee-Design") || !strings.Contains(gotReports[0], "最终方案路径：.mcode/design/panel.html") {
			t.Fatalf("汇报应包含设计 bee 最终输出，实际:\n%s", gotReports[0])
		}
		got = beeByThread(t, mgr, leadID, designBee.ThreadID)
		if got.AwaitingFinalize || !got.Reported || !got.FinalizeRequested {
			t.Fatalf("定稿轮结束后不应再进入待定稿，应标记为已汇报，实际: %+v", got)
		}
	})

	t.Run("user_turn_clears_finalize_before_tool_call", func(t *testing.T) {
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

		const leadID = "lead-finalize-user-turn"
		bees, err := mgr.Dispatch(ctx, DispatchRequest{
			LeadThreadID: leadID,
			ProjectID:    "proj-1",
			LeadWorkDir:  dir,
			Bees: []BeeSpec{
				{Name: "Bee-Design", Task: "设计", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
		designBee := bees[0]

		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "初稿完成",
		})
		mgr.Tick(ctx)

		// 模拟用户在 bee 子会话发消息并调用 design_finalize：先 NoteBeeUserTurn，再 FinalizeDesignBeeFromTurn。
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{Running: true})
		mgr.NoteBeeUserTurn(ctx, designBee.ThreadID)
		if err := mgr.FinalizeDesignBeeFromTurn(ctx, designBee.ThreadID); err != nil {
			t.Fatalf("用户回合中调用 FinalizeDesignBeeFromTurn 失败: %v", err)
		}
		driver.setSessionState(designBee.ThreadID, kanban.SessionState{
			Running:           false,
			LastAssistantText: "用户拍板 B 方案",
		})
		clock.Advance(2 * time.Second)
		mgr.Tick(ctx)

		reportMu.Lock()
		gotReports := append([]string(nil), reports...)
		reportMu.Unlock()
		if len(gotReports) != 1 || !strings.Contains(gotReports[0], "用户拍板 B 方案") {
			t.Fatalf("用户回合定稿后应正常放行汇报，实际: %v", gotReports)
		}
	})
}

func TestSendDesignBeeMessage(t *testing.T) {
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

	const leadID = "lead-send-design-msg"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Normal", Task: "普通", Prompt: "P1"},
			{Name: "Bee-Design", Task: "设计", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	normalBee := bees[0]
	designBee := bees[1]

	// 运行中（尚未待定稿）时转达消息应报错。
	if err := mgr.SendDesignBeeMessage(ctx, leadID, designBee.ThreadID, "选 B"); err == nil {
		t.Fatal("不在待定稿状态时 SendDesignBeeMessage 应报错")
	}

	driver.setSessionState(normalBee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "normal done",
	})
	driver.setSessionState(designBee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "初稿 A/B",
	})
	mgr.Tick(ctx)

	if err := mgr.SendDesignBeeMessage(ctx, leadID, normalBee.ThreadID, "选 B"); err == nil {
		t.Fatal("普通 bee 调用 SendDesignBeeMessage 应报错")
	}
	if err := mgr.SendDesignBeeMessage(ctx, "other-lead", designBee.ThreadID, "选 B"); err == nil {
		t.Fatal("非所属 lead 调用 SendDesignBeeMessage 应报错")
	}

	// SendFollowUp 失败时保持 AwaitingFinalize 不变。
	driver.mu.Lock()
	driver.onFollowUp = func(string, string) error { return errors.New("send failed") }
	driver.mu.Unlock()
	if err := mgr.SendDesignBeeMessage(ctx, leadID, designBee.Name, "选 B，调大圆角"); err == nil {
		t.Fatal("SendFollowUp 失败时 SendDesignBeeMessage 应报错")
	}
	got := beeByThread(t, mgr, leadID, designBee.ThreadID)
	if !got.AwaitingFinalize || got.Status != StatusCompleted {
		t.Fatalf("SendFollowUp 失败后应维持待定稿状态，实际: %+v", got)
	}

	// 按 bee 名称转达成功：置回运行态且 FinalizeRequested=false，前缀正确。
	driver.mu.Lock()
	driver.onFollowUp = nil
	driver.followUps = nil
	driver.mu.Unlock()
	if err := mgr.SendDesignBeeMessage(ctx, leadID, designBee.Name, "选 B，调大圆角"); err != nil {
		t.Fatalf("SendDesignBeeMessage failed: %v", err)
	}
	driver.mu.Lock()
	followUps := append([]fakeFollowUp(nil), driver.followUps...)
	driver.mu.Unlock()
	if len(followUps) != 1 || followUps[0].threadID != designBee.ThreadID || followUps[0].text != "主会话转达用户的回复：\n选 B，调大圆角" {
		t.Fatalf("转发给设计 bee 的文本异常: %+v", followUps)
	}
	got = beeByThread(t, mgr, leadID, designBee.ThreadID)
	if got.Status != StatusRunning || got.AwaitingFinalize || got.FinalizeRequested {
		t.Fatalf("转发后应回到运行态且保持 FinalizeRequested=false，实际: %+v", got)
	}

	// 这一轮结束后未调用 design_finalize，应回到待定稿，不汇报给主会话。
	driver.setSessionState(designBee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "已按 B 调大圆角",
	})
	clock.Advance(2 * time.Second)
	mgr.Tick(ctx)

	got = beeByThread(t, mgr, leadID, designBee.ThreadID)
	if got.Status != StatusCompleted || !got.AwaitingFinalize || got.FinalizeRequested || got.Reported {
		t.Fatalf("未定稿的修订轮结束后应回到待定稿状态，实际: %+v", got)
	}
	reportMu.Lock()
	reportCount := len(reports)
	reportMu.Unlock()
	if reportCount != 1 {
		t.Fatalf("修订轮回到待定稿时不应额外汇报，实际总汇报次数 %d", reportCount)
	}
}

func TestRecordBeeDesignFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()

	var changeMu sync.Mutex
	changes := 0

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})
	mgr.SetOnChange(func(string) {
		changeMu.Lock()
		changes++
		changeMu.Unlock()
	})

	const leadID = "lead-record-design-file"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-Normal", Task: "普通", Prompt: "P1"},
			{Name: "Bee-Design", Task: "设计", Prompt: "出初稿", SharedWorkspace: true, Interactive: true},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	normalBee := bees[0]
	designBee := bees[1]

	changeMu.Lock()
	changes = 0
	changeMu.Unlock()

	// 非设计 bee、空路径、未知 bee 不记录也不触发变更。
	mgr.RecordBeeDesignFile(normalBee.ThreadID, ".mcode/design/a.html")
	mgr.RecordBeeDesignFile(designBee.ThreadID, "   ")
	mgr.RecordBeeDesignFile("unknown-bee", ".mcode/design/a.html")

	if got := beeByThread(t, mgr, leadID, normalBee.ThreadID); len(got.DesignFiles) != 0 {
		t.Fatalf("非设计 bee 不应记录 DesignFiles，实际: %v", got.DesignFiles)
	}

	// 正常记录与去重。
	mgr.RecordBeeDesignFile(designBee.ThreadID, ".mcode/design/turn-header.html")
	mgr.RecordBeeDesignFile(designBee.ThreadID, ".mcode/design/turn-header.html")
	mgr.RecordBeeDesignFile(designBee.ThreadID, ".mcode/design/sidebar.html")

	got := beeByThread(t, mgr, leadID, designBee.ThreadID)
	if len(got.DesignFiles) != 2 || got.DesignFiles[0] != ".mcode/design/turn-header.html" || got.DesignFiles[1] != ".mcode/design/sidebar.html" {
		t.Fatalf("DesignFiles 记录或去重异常: %v", got.DesignFiles)
	}

	changeMu.Lock()
	gotChanges := changes
	changeMu.Unlock()
	if gotChanges != 2 {
		t.Fatalf("仅新增不重复路径时才应通知变更，期望 2 次，实际 %d 次", gotChanges)
	}

	// clone 深拷贝验证。
	cloned := got.clone()
	cloned.DesignFiles[0] = "mutated"
	if got.DesignFiles[0] != ".mcode/design/turn-header.html" {
		t.Fatalf("Bee.clone 应深拷贝 DesignFiles，原切片被改写为 %v", got.DesignFiles)
	}
}
