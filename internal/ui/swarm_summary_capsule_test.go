package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/swarm"
	"mcode/internal/testenv"
)

func TestSwarmReportCapsuleReopen(t *testing.T) {
	testenv.TempHome(t)
	const leadID = "lead-report-reopen"

	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		delete(swarmSelectedBeeID, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmBeeKeptOpen, leadID)
		delete(swarmPulseTime, leadID+"|bee-active")
		for k := range swarmCapsuleOpen {
			delete(swarmCapsuleOpen, k)
		}
		for k := range swarmCapsuleDismissed {
			delete(swarmCapsuleDismissed, k)
		}
		swarmFloatMu.Unlock()
	})

	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	t.Run("matchSwarmReportBee回查已清理子线程", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		t.Cleanup(s.close)

		s.mu.Lock()
		s.selectedThreadID = leadID
		s.threads = []Thread{
			{ID: "bee-other-lead", ParentThreadID: "other-lead", Title: "Ives", IsHelper: true, UpdatedAt: now.Add(time.Hour)},
			{ID: "bee-old", ParentThreadID: leadID, Title: "Ives: 旧任务", IsHelper: true, UpdatedAt: now.Add(-10 * time.Minute)},
			{ID: "bee-latest", ParentThreadID: leadID, Title: "Ives", IsHelper: true, UpdatedAt: now},
		}
		s.mu.Unlock()

		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         nil, // 模拟 AutoClearFinished 已清除
		})
		swarmPanelMu.Unlock()

		reportText := swarm.BuildReport([]swarm.Bee{
			{
				Name:          "Ives",
				Task:          "修复汇报重开",
				Status:        swarm.StatusCompleted,
				Summary:       "已完成修复。",
				ChangedFiles:  []string{"swarm.go"},
				ConflictFiles: []string{"conflict.go"},
			},
		})
		_, entries := parseSwarmReport(reportText)
		if len(entries) != 1 || entries[0].BeeID != "" || !entries[0].filesRecorded {
			t.Fatalf("汇报解析结果不符合前置条件: %+v", entries)
		}

		got := matchSwarmReportBee(s, hp, entries[0])
		if got == nil {
			t.Fatal("matchSwarmReportBee 返回 nil")
		}
		if got.ThreadID != "bee-latest" {
			t.Fatalf("回查 ThreadID = %q，期望 bee-latest", got.ThreadID)
		}
		if len(got.ChangedFiles) != 1 || got.ChangedFiles[0] != "swarm.go" {
			t.Fatalf("ChangedFiles 丢失: %+v", got.ChangedFiles)
		}
		if len(got.ConflictFiles) != 1 || got.ConflictFiles[0] != "conflict.go" {
			t.Fatalf("ConflictFiles 丢失: %+v", got.ConflictFiles)
		}
	})

	t.Run("汇报卡片打开独立窗口与查看会话（已清理出Bees）", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		t.Cleanup(s.close)

		s.mu.Lock()
		s.selectedThreadID = leadID
		s.threads = []Thread{
			{ID: "bee-cleared", ParentThreadID: leadID, Title: "Dune", IsHelper: true, UpdatedAt: now},
		}
		s.mu.Unlock()

		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         nil,
		})
		swarmPanelMu.Unlock()

		var openedWinID string
		prevOpener := beeWindowOpener
		beeWindowOpener = func(_ *chatState, beeID string) {
			openedWinID = beeID
		}
		t.Cleanup(func() { beeWindowOpener = prevOpener })

		_, entries := parseSwarmReport(swarm.BuildReport([]swarm.Bee{
			{Name: "Dune", Task: "验证已清理卡片重开", Status: swarm.StatusFailed, Summary: "执行失败"},
		}))

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmReportEntryCard(c, s, hp, "msg-cleared", 0, entries[0])
		}, 600, 400, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if !tt.HasText("打开浮窗") || !tt.HasText("查看会话") {
			t.Fatalf("缺少打开浮窗/查看会话按钮，texts=%v", tt.Texts())
		}

		if err := tt.Click("打开浮窗"); err != nil {
			t.Fatalf("点击打开浮窗失败: %v", err)
		}
		if openedWinID != "bee-cleared" {
			t.Fatalf("已清理出 Bees 时应打开独立窗口，got=%q want=bee-cleared", openedWinID)
		}

		if err := tt.Click("查看会话"); err != nil {
			t.Fatalf("点击查看会话失败: %v", err)
		}
		s.mu.Lock()
		sel := s.selectedThreadID
		s.mu.Unlock()
		if sel != "bee-cleared" {
			t.Fatalf("点击查看会话后 selectedThreadID = %q，期望 bee-cleared", sel)
		}
	})

	t.Run("汇报卡片重开浮窗（仍在Bees中）", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		t.Cleanup(s.close)

		s.mu.Lock()
		s.selectedThreadID = leadID
		s.threads = []Thread{
			{ID: "bee-active", ParentThreadID: leadID, Title: "Cove", IsHelper: true, UpdatedAt: now},
		}
		s.mu.Unlock()

		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees: []SwarmBee{
				{ThreadID: "bee-active", Index: 1, Name: "Cove", Status: "completed"},
			},
		})
		swarmPanelMu.Unlock()

		dismissSwarmFloatPanel(leadID, []SwarmBee{{ThreadID: "bee-active"}})

		_, entries := parseSwarmReport(swarm.BuildReport([]swarm.Bee{
			{Name: "Cove", Task: "验证仍在 Bees 中重开浮窗", Status: swarm.StatusCompleted, Summary: "已完成"},
		}))

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmReportEntryCard(c, s, hp, "msg-active", 0, entries[0])
		}, 600, 400, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if err := tt.Click("打开浮窗"); err != nil {
			t.Fatalf("点击打开浮窗失败: %v", err)
		}
		if !swarmFloatForcedFor(leadID) {
			t.Fatal("点击打开浮窗后应置 swarmFloatForced 为 true")
		}
		swarmFloatMu.Lock()
		selBee := swarmSelectedBeeID[leadID]
		dismissed := swarmFloatDismissedBees[leadID]["bee-active"]
		swarmFloatMu.Unlock()
		if selBee != "bee-active" || dismissed {
			t.Fatalf("浮窗未重开或未选中 bee-active: selBee=%q dismissed=%v", selBee, dismissed)
		}
	})

	t.Run("时间线已失败胶囊支持查看会话", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.mu.Lock()
		s.selectedThreadID = leadID
		s.mu.Unlock()

		bee := SwarmBee{
			ThreadID: "bee-failed-cap",
			Index:    1,
			Name:     "Larch",
			Status:   "failed",
			Summary:  "失败原因",
		}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmTimelineCapsules(c, s, swarmCapsuleRowID, leadID, []SwarmBee{bee})
		}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if err := tt.Click("子线程 Larch 失败"); err != nil {
			t.Fatalf("点击时间线失败胶囊失败: %v", err)
		}
		tt.Frame()
		if !tt.HasText("打开浮窗") || !tt.HasText("查看会话") {
			t.Fatalf("时间线胶囊浮层缺少打开浮窗/查看会话按钮，texts=%v", tt.Texts())
		}
		if err := tt.Click("查看会话"); err != nil {
			t.Fatalf("点击查看会话失败: %v", err)
		}
		s.mu.Lock()
		sel := s.selectedThreadID
		s.mu.Unlock()
		if sel != "bee-failed-cap" {
			t.Fatalf("selectedThreadID = %q，期望 bee-failed-cap", sel)
		}
	})
}

func TestSwarmSummaryPanelEntryAndFilter(t *testing.T) {
	testenv.TempHome(t)
	const leadID = "lead-summary-panel"

	bees := []SwarmBee{
		{ThreadID: "b-run", Index: 1, Name: "RunBee", Status: "running", Activity: "运行中任务"},
		{ThreadID: "b-rev", Index: 2, Name: "RevBee", Status: "completed", PendingMerge: true, Summary: "待合并审查"},
		{ThreadID: "b-done", Index: 3, Name: "DoneBee", Status: "completed", Merged: true, Summary: "已结束合并"},
	}

	t.Run("顶栏合并入口直达汇总面板与待审查筛选", func(t *testing.T) {
		fake := NewFakeClient()
		reviewOnly := SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees: []SwarmBee{
				{ThreadID: "b-rev", Index: 2, Name: "RevBee", Status: "completed", PendingMerge: true, Summary: "待合并审查"},
				{ThreadID: "b-done", Index: 3, Name: "DoneBee", Status: "completed", Merged: true, Summary: "已结束合并"},
			},
		}
		fake.SetSwarmState(leadID, reviewOnly)

		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.loaded = true
		hp.loadedFor = leadID
		hp.refreshed = time.Now()
		hp.setStateLocked(reviewOnly)
		swarmPanelMu.Unlock()

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmHeaderButton(c, s)
		}, 900, 120, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if _, ok := tt.Find("子代理汇总面板"); ok {
			t.Fatal("不应再存在独立四宫格按钮 chat-header-swarm-panel-btn")
		}
		if _, ok := tt.Find("chat-header-swarm-panel-btn"); ok {
			t.Fatal("不应再存在独立四宫格按钮 chat-header-swarm-panel-btn")
		}
		if !tt.HasText("1") {
			t.Fatalf("应显示待审查角标 1，实际文本：%v", tt.Texts())
		}

		// 无活跃 bee 且有待审查时点击编排按钮，打开汇总面板并切到待审查筛选
		if err := tt.Click("编排"); err != nil {
			t.Fatalf("点击编排按钮失败: %v", err)
		}
		swarmPanelMu.Lock()
		opened := hp.open
		filter := hp.filter
		swarmPanelMu.Unlock()
		if !opened || filter != swarmFilterReview {
			t.Fatalf("点击待审查角标态编排按钮后 open=%v filter=%v，期望 true / swarmFilterReview", opened, filter)
		}

		// 切到无待审查也无活跃 bee 的状态：点击编排按钮应能关闭并再次打开汇总面板
		doneOnly := SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees: []SwarmBee{
				{ThreadID: "b-done", Index: 3, Name: "DoneBee", Status: "completed", Merged: true, Summary: "已结束合并"},
			},
		}
		fake.SetSwarmState(leadID, doneOnly)
		swarmPanelMu.Lock()
		hp.setStateLocked(doneOnly)
		swarmPanelMu.Unlock()
		tt.Frame()

		if err := tt.Click("编排"); err != nil {
			t.Fatalf("再次点击编排按钮失败: %v", err)
		}
		if getSwarmPanelState(s).IsOpen() {
			t.Fatal("无待审查且无活跃 bee 时点击编排按钮应关闭已打开的汇总面板")
		}
		if err := tt.Click("编排"); err != nil {
			t.Fatalf("第三次点击编排按钮失败: %v", err)
		}
		if !getSwarmPanelState(s).IsOpen() {
			t.Fatal("无待审查且无活跃 bee 时再次点击编排按钮应打开汇总面板")
		}
	})

	t.Run("四态分段筛选、空态与Esc关闭", func(t *testing.T) {
		fake := NewFakeClient()
		st := SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         bees,
		}
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.open = true
		hp.loaded = true
		hp.loadedFor = leadID
		hp.filter = swarmFilterAll
		hp.setStateLocked(st)
		swarmPanelMu.Unlock()

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmPanel(c, s)
		}, 900, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if !tt.HasText("#1 RunBee") || !tt.HasText("#2 RevBee") || !tt.HasText("#3 DoneBee") {
			t.Fatalf("全部筛选应显示全部 3 个成员，texts=%v", tt.Texts())
		}

		// 切到「运行中」
		if err := tt.Click("运行中"); err != nil {
			t.Fatalf("点击运行中分段失败: %v", err)
		}
		tt.Frame()
		if !tt.HasText("#1 RunBee") || tt.HasText("#2 RevBee") || tt.HasText("#3 DoneBee") {
			t.Fatalf("运行中筛选结果有误，texts=%v", tt.Texts())
		}

		// 切到「待审查」
		if err := tt.Click("待审查"); err != nil {
			t.Fatalf("点击待审查分段失败: %v", err)
		}
		tt.Frame()
		if tt.HasText("#1 RunBee") || !tt.HasText("#2 RevBee") || tt.HasText("#3 DoneBee") {
			t.Fatalf("待审查筛选结果有误，texts=%v", tt.Texts())
		}

		// 切到「已结束」
		if err := tt.Click("已结束"); err != nil {
			t.Fatalf("点击已结束分段失败: %v", err)
		}
		tt.Frame()
		if tt.HasText("#1 RunBee") || !tt.HasText("#2 RevBee") || !tt.HasText("#3 DoneBee") {
			t.Fatalf("已结束筛选结果有误，texts=%v", tt.Texts())
		}

		// 当某筛选无项时展示单行空态
		swarmPanelMu.Lock()
		hp.filter = swarmFilterRunning
		hp.setStateLocked(SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         []SwarmBee{{ThreadID: "b-done", Index: 1, Name: "DoneBee", Status: "completed"}},
		})
		swarmPanelMu.Unlock()
		tt.Frame()
		if !tt.HasText("无运行中子代理") {
			t.Fatalf("筛选空态文本缺失，texts=%v", tt.Texts())
		}

		// 按 Esc 关闭面板
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if getSwarmPanelState(s).IsOpen() {
			t.Fatal("按 Esc 应关闭汇总面板")
		}
	})

	t.Run("加载中与报错分支共用固定顶栏与关闭按钮", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			loading bool
			errMsg  string
		}{
			{name: "loading", loading: true},
			{name: "error", errMsg: "加载超时"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := newChatState(NewFakeClient())
				t.Cleanup(s.close)
				s.selectedThreadID = leadID
				hp := getSwarmPanelState(s)
				swarmPanelMu.Lock()
				hp.open = true
				hp.loadedFor = leadID
				hp.loading = tc.loading
				hp.loaded = false
				hp.err = tc.errMsg
				swarmPanelMu.Unlock()

				tt := ui.NewTesterWith(func(c *ui.Context) {
					renderSwarmPanel(c, s)
				}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
				tt.Frame()

				if !tt.HasText("子代理汇总") {
					t.Fatalf("%s 分支缺少固定顶栏标题，texts=%v", tc.name, tt.Texts())
				}
				if _, ok := tt.Find("关闭汇总面板"); !ok {
					t.Fatalf("%s 分支缺少关闭按钮", tc.name)
				}
				if tc.errMsg != "" && !tt.HasText(tc.errMsg) {
					t.Fatalf("error 分支缺少错误信息 %q，texts=%v", tc.errMsg, tt.Texts())
				}

				if err := tt.Click("关闭汇总面板"); err != nil {
					t.Fatalf("%s 分支点击关闭按钮失败: %v", tc.name, err)
				}
				if getSwarmPanelState(s).IsOpen() {
					t.Fatalf("%s 分支点击关闭按钮后未关闭面板", tc.name)
				}
			})
		}
	})

	t.Run("卡片点击打开浮窗或审查且停止按钮独立可访问", func(t *testing.T) {
		t.Cleanup(func() {
			swarmFloatMu.Lock()
			delete(swarmFloatPanelDismissed, leadID)
			delete(swarmFloatDismissedBees, leadID)
			delete(swarmFloatForced, leadID)
			delete(swarmFloatForcedHadBees, leadID)
			delete(swarmSelectedBeeID, leadID)
			delete(swarmBeeAutoHideHandled, leadID)
			delete(swarmBeeKeptOpen, leadID)
			swarmFloatMu.Unlock()
		})

		fake := NewFakeClient()
		st := SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         bees,
		}
		fake.SetSwarmState(leadID, st)

		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.open = true
		hp.loaded = true
		hp.loadedFor = leadID
		hp.filter = swarmFilterAll
		hp.setStateLocked(st)
		swarmPanelMu.Unlock()

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmPanel(c, s)
		}, 900, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		tt.Frame()

		if _, ok := tt.AccessByRole(ui.RoleButton, "#1 RunBee"); !ok {
			t.Fatal("未找到左侧主体按钮 #1 RunBee")
		}
		stopNode, ok := tt.AccessByRole(ui.RoleButton, "停止")
		if !ok {
			t.Fatal("停止按钮应作为独立 RoleButton 可访问")
		}
		if stopNode.Disabled {
			t.Fatal("初始状态下停止按钮不应禁用")
		}

		openRect, okOpen := tt.Find("#1 RunBee")
		stopRect, okStop := tt.Find("停止")
		if !okOpen || !okStop {
			t.Fatalf("未找到卡片主体或停止按钮矩形: open=%v stop=%v", okOpen, okStop)
		}
		if stopRect.X < openRect.X+openRect.W {
			t.Fatalf("停止按钮应位于左侧主体按钮右侧同级区域: open=%+v stop=%+v", openRect, stopRect)
		}

		if err := tt.Click("#1 RunBee"); err != nil {
			t.Fatalf("点击 #1 RunBee 卡片主体失败: %v", err)
		}
		if !swarmFloatForcedFor(leadID) {
			t.Fatal("点击普通卡片主体应打开浮窗")
		}
		swarmFloatMu.Lock()
		selBee := swarmSelectedBeeID[leadID]
		delete(swarmFloatForced, leadID)
		delete(swarmSelectedBeeID, leadID)
		swarmFloatMu.Unlock()
		if selBee != "b-run" {
			t.Fatalf("点击 #1 RunBee 后选中 bee = %q，期望 b-run", selBee)
		}

		if err := tt.Click("停止"); err != nil {
			t.Fatalf("点击停止按钮失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			stopped := fake.StoppedBees()
			return len(stopped) == 1 && stopped[0] == "b-run"
		})
		if swarmFloatForcedFor(leadID) {
			t.Fatal("点击停止按钮不应触发打开浮窗")
		}

		tt.Frame()
		if err := tt.Click("#2 RevBee"); err != nil {
			t.Fatalf("点击 #2 RevBee 卡片主体失败: %v", err)
		}
		swarmPanelMu.Lock()
		reviewOpen := hp.reviewOpen
		reviewBeeID := hp.reviewBeeID
		swarmPanelMu.Unlock()
		if !reviewOpen || reviewBeeID != "b-rev" {
			t.Fatalf("点击待审查卡片主体应进入合并审查: reviewOpen=%v reviewBeeID=%q", reviewOpen, reviewBeeID)
		}
	})
}
