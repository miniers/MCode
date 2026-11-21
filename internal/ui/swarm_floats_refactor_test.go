package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func resetSwarmFloatRefactorGlobals(leadID string) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	delete(swarmFloatPanelDismissed, leadID)
	delete(swarmFloatDismissedBees, leadID)
	delete(swarmSelectedBeeID, leadID)
	delete(swarmTeamDock, leadID)
	delete(swarmAutoDocks, leadID)
	delete(swarmAutoHeld, leadID)
	delete(swarmAutoForced, leadID)
	delete(swarmStackOrder, leadID)
	delete(swarmAutoPopsOverride, leadID)
	delete(swarmFloatForced, leadID)
	delete(swarmFloatForcedHadBees, leadID)
	delete(swarmBeeDoneSince, leadID)
	delete(swarmBeeAutoHideHandled, leadID)
	delete(swarmBeeSeenActive, leadID)
	delete(swarmBeeKeptOpen, leadID)
	delete(swarmMenuOpen, leadID+"|"+string(teamPanelID))
}

func TestSwarmFloatMenuAndUndock(t *testing.T) {
	const leadID = "lead-refactor-undock"

	t.Run("放回团队面板与弹出独立浮窗双向闭环", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = leadID
		withFloatSettings(t, s, func(st *SettingsState) { st.cfg.Orchestration.AutoPopsBees = true })
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			Available:    true,
			Enabled:      true,
			LeadThreadID: leadID,
			Bees: []SwarmBee{
				{ThreadID: "h-birch", Name: "Birch", Task: "实现菜单", Status: "running"},
				{ThreadID: "h-isle", Name: "Isle", Task: "验证布局", Status: "running"},
			},
		})
		swarmPanelMu.Unlock()
		resetSwarmFloatRefactorGlobals(leadID)
		swarmFloatMu.Lock()
		swarmBeeSeenActive[leadID] = map[string]bool{"h-birch": true, "h-isle": true}
		swarmFloatMu.Unlock()
		t.Cleanup(func() {
			closeEmbeddedBees(s)
			resetSwarmFloatRefactorGlobals(leadID)
		})

		// 初始分散态：Birch 在团队面板，Isle 在独立浮窗。
		var frame swarmFloatFrame
		tt := ui.NewTesterWith(func(c *ui.Context) {
			frame = swarmFloatFrameFor(c, s)
			renderSwarmBeeFloats(c, s)
		}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()
		if len(frame.scene.teamBees) != 1 || len(frame.scene.autoBees) != 1 || frame.scene.autoBees[0].ThreadID != "h-isle" {
			t.Fatalf("初始应分散为 1 团队 + 1 独立浮窗，got team=%d auto=%d", len(frame.scene.teamBees), len(frame.scene.autoBees))
		}

		// 点击「放回团队面板」写入 swarmAutoHeld，Isle 回到团队面板。
		if err := tt.Click("把 Isle 放回团队面板"); err != nil {
			t.Fatalf("点击放回团队面板失败: %v", err)
		}
		tt.Frame()
		if !isSwarmAutoHeld(leadID, "h-isle") {
			t.Fatal("放回团队面板后应记录 swarmAutoHeld")
		}
		if len(frame.scene.teamBees) != 2 || len(frame.scene.autoBees) != 0 {
			t.Fatalf("放回后应全部留在团队面板，got team=%d auto=%d", len(frame.scene.teamBees), len(frame.scene.autoBees))
		}

		// 即使临时覆盖为聚合态（false），releaseSwarmAutoHeld 也能单只弹出为独立浮窗。
		swarmFloatMu.Lock()
		swarmAutoPopsOverride[leadID] = false
		swarmFloatMu.Unlock()
		releaseSwarmAutoHeld(leadID, "h-isle")
		tt.Frame()
		if isSwarmAutoHeld(leadID, "h-isle") {
			t.Fatal("releaseSwarmAutoHeld 应清除 swarmAutoHeld 标记")
		}
		if len(frame.scene.autoBees) != 1 || frame.scene.autoBees[0].ThreadID != "h-isle" {
			t.Fatalf("聚合覆盖下 releaseSwarmAutoHeld 后应单只弹出 Isle，got auto=%+v", frame.scene.autoBees)
		}

		// 再放回团队面板，点击顶栏「弹出为独立浮窗」按钮可再次弹出。
		holdSwarmAutoBee(leadID, "h-isle")
		tt.Frame()
		if err := tt.Click("弹出为独立浮窗"); err != nil {
			t.Fatalf("点击顶栏弹出为独立浮窗失败: %v", err)
		}
		tt.Frame()
		if len(frame.scene.autoBees) != 1 || frame.scene.autoBees[0].ThreadID != "h-isle" {
			t.Fatalf("点击顶栏弹出后 Isle 应回到独立浮窗，got auto=%+v", frame.scene.autoBees)
		}

		// 放回并在聚合模式下用 toggleSwarmAutoPops 切到分散模式，会一并清空 swarmAutoHeld。
		holdSwarmAutoBee(leadID, "h-isle")
		swarmFloatMu.Lock()
		swarmAutoPopsOverride[leadID] = false
		swarmFloatMu.Unlock()
		toggleSwarmAutoPops(s, leadID)
		tt.Frame()
		if isSwarmAutoHeld(leadID, "h-isle") {
			t.Fatal("切换到分散模式时应清空 swarmAutoHeld")
		}
		if len(frame.scene.autoBees) != 1 || frame.scene.autoBees[0].ThreadID != "h-isle" {
			t.Fatalf("切换分散模式后 Isle 应弹出为独立浮窗，got auto=%+v", frame.scene.autoBees)
		}
	})

	t.Run("单运行中无已结束时不渲染清理行且菜单项左对齐", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = leadID
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			Available:    true,
			Enabled:      true,
			LeadThreadID: leadID,
			Bees: []SwarmBee{
				{ThreadID: "h-birch", Name: "Birch", Task: "唯一运行中的任务", Status: "running"},
			},
		})
		swarmPanelMu.Unlock()
		resetSwarmFloatRefactorGlobals(leadID)
		swarmFloatMu.Lock()
		swarmBeeSeenActive[leadID] = map[string]bool{"h-birch": true}
		swarmFloatMu.Unlock()
		t.Cleanup(func() {
			closeEmbeddedBees(s)
			resetSwarmFloatRefactorGlobals(leadID)
		})

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmBeeFloats(c, s)
		}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if err := tt.Click("子代理列表，共 1 个"); err != nil {
			t.Fatalf("打开菜单失败: %v", err)
		}
		tt.Frame()

		if tt.HasText("清理已结束") || tt.HasText("全部停止") {
			t.Fatalf("1 个运行中且无已结束时不应渲染全部停止或清理已结束: %v", tt.Texts())
		}
		if _, ok := tt.Find("swarm-menu-divider"); ok {
			t.Fatal("无管理动作时不应渲染管理分割线")
		}

		menuRect, ok := tt.Find("swarm-bees-menu-" + leadID + "|" + string(teamPanelID))
		if !ok {
			t.Fatal("找不到菜单面板")
		}
		scatterRect, ok := tt.Find("展开为独立浮窗")
		if !ok {
			t.Fatal("找不到展开为独立浮窗菜单项")
		}
		panelRect, ok := tt.Find("打开汇总面板")
		if !ok {
			t.Fatal("找不到打开汇总面板菜单项")
		}
		if scatterRect.X-menuRect.X > 36 || panelRect.X-menuRect.X > 36 {
			t.Fatalf("菜单项应左对齐: menu=%+v scatter=%+v panel=%+v", menuRect, scatterRect, panelRect)
		}
	})

	t.Run("点击菜单行内弹出图标不冒泡触发整行切换并关闭菜单", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = leadID
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			Available:    true,
			Enabled:      true,
			LeadThreadID: leadID,
			Bees: []SwarmBee{
				{ThreadID: "h-birch", Name: "Birch", Task: "任务一", Status: "running"},
				{ThreadID: "h-isle", Name: "Isle", Task: "任务二", Status: "completed"},
			},
		})
		swarmPanelMu.Unlock()
		resetSwarmFloatRefactorGlobals(leadID)
		swarmFloatMu.Lock()
		swarmBeeSeenActive[leadID] = map[string]bool{"h-birch": true, "h-isle": true}
		swarmSelectedBeeID[leadID] = "h-birch"
		swarmFloatMu.Unlock()
		t.Cleanup(func() {
			closeEmbeddedBees(s)
			resetSwarmFloatRefactorGlobals(leadID)
		})

		var frame swarmFloatFrame
		tt := ui.NewTesterWith(func(c *ui.Context) {
			frame = swarmFloatFrameFor(c, s)
			renderSwarmBeeFloats(c, s)
		}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if err := tt.Click("子代理列表，共 2 个"); err != nil {
			t.Fatalf("打开菜单失败: %v", err)
		}
		tt.Frame()

		if err := tt.Click("弹出 Isle 为独立浮窗"); err != nil {
			t.Fatalf("点击行内弹出图标失败: %v", err)
		}
		tt.Frame()

		swarmFloatMu.Lock()
		selected := swarmSelectedBeeID[leadID]
		menuOpen := *swarmMenuOpen[leadID+"|"+string(teamPanelID)]
		swarmFloatMu.Unlock()
		if selected != "h-birch" {
			t.Fatalf("点击行内弹出图标不应冒泡切换当前选中项，got %q want h-birch", selected)
		}
		if menuOpen {
			t.Fatal("执行行内动作后应收起菜单")
		}
		if len(frame.scene.autoBees) != 1 || frame.scene.autoBees[0].ThreadID != "h-isle" {
			t.Fatalf("点击行内弹出图标后 Isle 应弹出为独立浮窗，got %+v", frame.scene.autoBees)
		}
	})
}

func TestSwarmBeeWindowArchivedThread(t *testing.T) {
	fake := NewFakeClient()
	parent := newChatState(fake)
	t.Cleanup(parent.close)

	const archivedID = "bee-archived-1"
	now := time.Now()
	parent.mu.Lock()
	parent.selectedThreadID = "lead-archived"
	parent.threads = []Thread{
		{ID: "lead-archived", Title: "主会话"},
		{
			ID:             archivedID,
			ParentThreadID: "lead-archived",
			Title:          "Birch",
			IsHelper:       true,
			LastStatus:     "completed",
			Provider:       "anthropic",
			Model:          "claude-sonnet-4-5",
			TotalDuration:  42 * time.Second,
			UpdatedAt:      now,
		},
	}
	parent.mu.Unlock()

	// 模拟 AutoClearFinished 已将该 bee 从 SwarmState.Bees 清除。
	hp := getSwarmPanelState(parent)
	swarmPanelMu.Lock()
	hp.loadedFor = "lead-archived"
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead-archived",
		Bees:         nil,
	})
	swarmPanelMu.Unlock()

	bee, found := findBeeForWindow(parent, archivedID)
	if !found {
		t.Fatal("已从 SwarmState.Bees 清理但仍在 threads 中的子代理应能回查解析出历史 SwarmBee")
	}
	if bee.ThreadID != archivedID || bee.Name != "Birch" || bee.Status != swarmStatusCompleted {
		t.Fatalf("解析出的历史 SwarmBee 不符: %+v", bee)
	}
	if bee.Provider != "anthropic" || bee.Model != "claude-sonnet-4-5" {
		t.Fatalf("历史 SwarmBee 缺少模型信息: provider=%q model=%q", bee.Provider, bee.Model)
	}
	if bee.StartedAt.IsZero() || bee.FinishedAt == nil {
		t.Fatalf("历史 SwarmBee 应保留时间信息: started=%v finished=%v", bee.StartedAt, bee.FinishedAt)
	}

	if _, missing := findBeeForWindow(parent, "bee-not-exist"); missing {
		t.Fatal("不存在的 beeID 不应返回 found=true")
	}

	fake.SetMessages(archivedID, []Message{
		{ID: "m1", Role: "assistant", Text: "历史子代理执行记录"},
	})
	child := newEmbeddedBeeState(parent)
	t.Cleanup(func() { releaseEmbeddedBeeState(child) })
	syncEmbeddedBeeState(parent, child, bee)

	// 等待 FakeClient History 异步加载完成（如未立即写入则直接兜底校验视图不退回空态）。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		child.mu.Lock()
		n := len(child.messages)
		child.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderBeeWindow(c, parent, archivedID, child)
	}, 600, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
	tt.Frame()

	if tt.HasText("子代理已结束或不存在") {
		t.Fatal("历史子代理窗口不应显示空态提示")
	}
	if !tt.HasText("Birch") || !tt.HasText("已完成") {
		t.Fatalf("历史子代理窗口顶栏应显示名称与状态: %v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("已结束的历史子代理窗口不应继续排秒级重绘")
	}
}
