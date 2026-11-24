package mcodeui

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSwarmPersonaLookup(t *testing.T) {
	// 精确匹配
	pBram := findSwarmPersona("Bram")
	if pBram.Name != "Bram" || pBram.Asset != "swarm-bee-02.svg" {
		t.Errorf("findSwarmPersona(Bram) = %+v, want Bram and swarm-bee-02.svg", pBram)
	}

	// 轮次后缀匹配
	pBram2 := findSwarmPersona("Bram 2")
	if pBram2.Asset != "swarm-bee-02.svg" {
		t.Errorf("findSwarmPersona(Bram 2) = %+v, want swarm-bee-02.svg", pBram2)
	}

	// 大小写不敏感
	pCora := findSwarmPersona("cora")
	if pCora.Name != "Cora" || pCora.Asset != "swarm-bee-03.svg" {
		t.Errorf("findSwarmPersona(cora) = %+v, want Cora and swarm-bee-03.svg", pCora)
	}

	// 未知名称兜底
	pCustom := findSwarmPersona("CustomAgent")
	if !strings.HasPrefix(pCustom.Asset, "swarm-bee-") {
		t.Errorf("findSwarmPersona(CustomAgent) asset = %v, want swarm-bee-*.svg", pCustom.Asset)
	}
}

// newFloatTestState 构造带两个子代理的主会话状态，供面板渲染测试使用。
func newFloatTestState(t *testing.T) *chatState {
	t.Helper()
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "lead-thread-1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "lead-thread-1"
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead-thread-1",
		Bees: []SwarmBee{
			{ThreadID: "h-birch", Name: "Birch", Task: "实现浮窗卡片", Status: "running", Activity: "编写代码中", Tokens: 1280, ToolCalls: 3},
			{ThreadID: "h-isle", Name: "Isle", Task: "核对自适应布局", Status: "completed", Summary: "三档布局核对无误"},
		},
	})
	swarmPanelMu.Unlock()
	resetFloat := func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, "lead-thread-1")
		delete(swarmFloatDismissedBees, "lead-thread-1")
		delete(swarmBeeDoneSince, "lead-thread-1")
		delete(swarmBeeAutoHideHandled, "lead-thread-1")
		delete(swarmBeeSeenActive, "lead-thread-1")
		delete(swarmBeeKeptOpen, "lead-thread-1")
		for key, timer := range swarmAutoHideTimers {
			if strings.HasPrefix(key, "lead-thread-1|") {
				timer.Stop()
				delete(swarmAutoHideTimers, key)
			}
		}
		swarmFloatMu.Unlock()
	}
	resetFloat()
	// 夹具模拟本次会话里看着跑过的子代理，预置 seen-active，避免已完成子线程被当成启动前已完成而在首帧收起。
	swarmFloatMu.Lock()
	swarmBeeSeenActive["lead-thread-1"] = map[string]bool{"h-birch": true, "h-isle": true}
	swarmFloatMu.Unlock()
	t.Cleanup(func() { closeEmbeddedBees(s); resetFloat() })
	return s
}

// TestRenderSwarmBeeFloats_StripShowsRunningBee 验证面板顶栏显示子代理计数和运行中的子代理。
func TestRenderSwarmBeeFloats_StripShowsRunningBee(t *testing.T) {
	s := newFloatTestState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeFloats(c, s)
	}, 1600, 900)

	if !tt.HasText("2") {
		t.Fatal("顶栏应显示子代理计数 2")
	}
	if !tt.HasText("Birch") || !tt.HasText("正在工作") {
		t.Fatal("顶栏应显示运行中的子代理 Birch")
	}
}

func TestSwarmFloatAcceptanceProgress(t *testing.T) {
	s := newFloatTestState(t)
	hp := getSwarmPanelState(s)
	setStep := func(step int) {
		swarmPanelMu.Lock()
		st := hp.state
		st.Bees = slices.Clone(st.Bees)
		st.Bees[0].Acceptance = []string{"true", "go test ./internal/ui/"}
		st.Bees[0].AcceptanceStep = step
		st.Bees[0].AcceptanceStartedAt = time.Now()
		hp.setStateLocked(st)
		swarmPanelMu.Unlock()
	}
	setStep(2)
	tt := ui.NewTester(func(c *ui.Context) { renderSwarmBeeFloats(c, s) }, 1600, 900)
	tt.Frame()
	if !tt.HasText("验收 2/2 · go test ./internal/ui/") {
		t.Fatalf("浮窗未显示验收进度: %v", tt.Texts())
	}
	setStep(0)
	tt.Frame()
	if _, ok := tt.Find("验收进度"); ok {
		t.Fatal("验收结束后浮窗仍显示进度条")
	}
}

// TestSwarmFloatFrameReservesSpaceBesideComposer 验证面板按场景从聊天列右侧让出宽度，关闭后归还。
func TestSwarmFloatFrameReservesSpaceBesideComposer(t *testing.T) {
	s := newFloatTestState(t)
	var reserve panelReserve
	tt := ui.NewTester(func(c *ui.Context) {
		reserve = swarmFloatReserve(c, s)
	}, 1600, 900)
	_ = tt
	if reserve.trailing != panelDefaultWidth+panelSideGap || reserve.leading != 0 {
		t.Fatalf("reserve = %+v，应只从右侧让出面板宽度", reserve)
	}

	dismissSwarmFloatPanel("lead-thread-1", getActiveSwarmBees(getSwarmPanelState(s).state))
	ui.NewTester(func(c *ui.Context) {
		reserve = swarmFloatReserve(c, s)
	}, 1600, 900)
	if reserve != (panelReserve{}) {
		t.Fatalf("关闭面板后不应让出宽度: %+v", reserve)
	}
}

// TestSwarmFloatFrameExcludesSidePane 验证浮窗的主区宽度同样扣掉右侧分栏：
// 打开文件管理面板后 paneW 少掉面板宽度与 sidePaneHandleWidth 拖动手柄，面板停靠位置也不越过新的主区右缘。
func TestSwarmFloatFrameExcludesSidePane(t *testing.T) {
	s := newFloatTestState(t)
	// 打开自动分散展示，让不开分栏时两个子代理分别停到左右两侧。
	withFloatSettings(t, s, func(st *SettingsState) { st.cfg.Orchestration.AutoPopsBees = true })
	var frame swarmFloatFrame
	ui.NewTester(func(c *ui.Context) {
		frame = swarmFloatFrameFor(c, s)
	}, 1600, 900)
	baseW := frame.scene.layout.paneW
	if baseW <= 0 {
		t.Fatalf("不开分栏时 paneW = %v，应大于 0", baseW)
	}

	const paneW float32 = 400
	toggleFileManager(s)
	inspector := inspectorSession(s)
	s.mu.Lock()
	inspector.Width = paneW
	s.mu.Unlock()

	ui.NewTester(func(c *ui.Context) {
		frame = swarmFloatFrameFor(c, s)
	}, 1600, 900)
	if got, want := frame.scene.layout.paneW, baseW-paneW-sidePaneHandleWidth; got != want {
		t.Fatalf("打开分栏后 paneW = %v，want %v", got, want)
	}
	if len(frame.scene.slots) == 0 {
		t.Fatal("应至少停靠一个面板")
	}
	w := frame.scene.layout.size().w
	for id := range frame.scene.slots {
		origin, ok := frame.scene.restOrigin(id)
		if !ok {
			t.Fatalf("面板 %v 缺少停靠位置", id)
		}
		if origin.x+w > frame.scene.layout.paneW {
			t.Fatalf("面板 %v 右缘 %v 超出主区宽度 %v", id, origin.x+w, frame.scene.layout.paneW)
		}
	}
}

// TestSwarmFloatDismissAndReappear 验证关闭浮窗与派出新 bee 时自动重新出现机制。
func TestSwarmFloatDismissAndReappear(t *testing.T) {
	leadID := "lead-dismiss-test"
	defer func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		swarmFloatMu.Unlock()
	}()
	initialBees := []SwarmBee{
		{ThreadID: "bee-1", Name: "Birch", Status: "running"},
	}

	// 初始状态下不应被判定为关闭
	if isSwarmFloatPanelDismissed(leadID, initialBees) {
		t.Fatalf("initially should not be dismissed")
	}

	// 用户点击关闭
	dismissSwarmFloatPanel(leadID, initialBees)
	if !isSwarmFloatPanelDismissed(leadID, initialBees) {
		t.Fatalf("should be dismissed after user close")
	}

	// 相同 bee 仍保持关闭
	if !isSwarmFloatPanelDismissed(leadID, initialBees) {
		t.Fatalf("same bees should stay dismissed")
	}

	// 派出了新的 bee，应自动解除关闭状态并重新显示
	updatedBees := []SwarmBee{
		{ThreadID: "bee-1", Name: "Birch", Status: "running"},
		{ThreadID: "bee-2", Name: "Isle", Status: "running"},
	}
	if isSwarmFloatPanelDismissed(leadID, updatedBees) {
		t.Fatalf("new bee dispatched should automatically make float reappear")
	}
}

func withFloatSettings(t *testing.T, s *chatState, mutate func(*SettingsState)) *SettingsState {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, s)
	mutate(st)
	setChatSettings(s, st)
	return st
}

// TestRenderSwarmBeeFloats_AutoPopsSplitsPanels 验证“自动弹出子代理面板”开启后，第二个子代理有自己的面板，
// 左右两侧都让出宽度。
func TestRenderSwarmBeeFloats_AutoPopsSplitsPanels(t *testing.T) {
	s := newFloatTestState(t)
	withFloatSettings(t, s, func(st *SettingsState) { st.cfg.Orchestration.AutoPopsBees = true })

	var reserve panelReserve
	tt := ui.NewTester(func(c *ui.Context) {
		reserve = swarmFloatReserve(c, s)
		renderSwarmBeeFloats(c, s)
	}, 1800, 900)
	if reserve.leading == 0 || reserve.trailing == 0 {
		t.Fatalf("两侧都有面板，reserve = %+v", reserve)
	}
	// 团队面板只留第一个子代理（Birch），Isle 单独成面板，两者的名字同时可见。
	if !tt.HasText("Birch") || !tt.HasText("Isle") {
		t.Fatal("两个子代理应各有面板")
	}
}

// TestRenderSwarmBeeFloats_ShowsTimeline 验证面板显示对话时间线。
func TestRenderSwarmBeeFloats_ShowsTimeline(t *testing.T) {
	s := newFloatTestState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeFloats(c, s)
	}, 1600, 900)
	if _, ok := tt.Find("swarm-progress"); ok {
		t.Fatal("面板不应包含进度视图")
	}
	if tt.HasText("任务目标: 实现浮窗卡片") {
		t.Fatal("对话视图不应再显示进度视图的任务目标条")
	}
	if !tt.HasText("正在执行任务…") {
		t.Fatal("没有记录时对话视图应显示等待提示")
	}
}

// TestSwarmBeeShouldAutoHide 验证单个子线程的自动收起判定：它自己进入终态且满 5 秒才收起，
// 与其它子线程无关；还在运行、在等用户回答或审批、在等人工合并、或这批完成已经处理过时都不收起。
func TestSwarmBeeShouldAutoHide(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	done := SwarmBee{ThreadID: "h-aspen", Name: "Aspen", Status: "completed"}

	tests := []struct {
		name    string
		bee     SwarmBee
		waiting bool
		since   time.Time
		now     time.Time
		handled bool
		want    bool
	}{
		{"终态且已满 5 秒", done, false, base, base.Add(swarmAutoHideDelay), false, true},
		{"刚好满 5 秒才收起", done, false, base, base.Add(5 * time.Second), false, true},
		{"还差 1 秒", done, false, base, base.Add(4 * time.Second), false, false},
		{"停止属于终态", SwarmBee{Status: "stopped"}, false, base, base.Add(time.Minute), false, true},
		{"失败也收起", SwarmBee{Status: "failed"}, false, base, base.Add(time.Minute), false, true},
		{"仍在运行", SwarmBee{Status: "running"}, false, base, base.Add(time.Minute), false, false},
		{"在等用户回答或审批", SwarmBee{Status: "running"}, true, base, base.Add(time.Minute), false, false},
		// 终态 bee 不可能再回答残留的等待请求，不应因此永久留在浮窗里。
		{"终态 bee 的残留等待不阻止收起", done, true, base, base.Add(time.Minute), false, true},
		{"在等人工合并", SwarmBee{Status: "completed", PendingMerge: true}, false, base, base.Add(time.Minute), false, false},
		{"待定稿不自动收起", SwarmBee{Status: "completed", AwaitingFinalize: true}, false, base, base.Add(time.Minute), false, false},
		{"已经自动收起过", done, false, base, base.Add(time.Minute), true, false},
		{"用户手动打开过（标记为已处理）", done, false, time.Time{}, base.Add(time.Minute), true, false},
		{"完成时刻未记录", done, false, time.Time{}, base.Add(time.Minute), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := swarmBeeShouldAutoHide(tc.bee, tc.waiting, tc.since, tc.now, tc.handled); got != tc.want {
				t.Fatalf("swarmBeeShouldAutoHide() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSwarmVisibleBees 验证已自动收起的子线程不再出现在浮窗里，其余子线程照常显示。
func TestSwarmVisibleBees(t *testing.T) {
	leadID := "lead-visible-test"
	bees := []SwarmBee{{ThreadID: "h-aspen"}, {ThreadID: "h-cove"}}
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		swarmFloatMu.Unlock()
	})

	got := swarmVisibleBees(leadID, bees)
	if len(got) != 2 || got[0].ThreadID != "h-aspen" || got[1].ThreadID != "h-cove" {
		t.Fatalf("没有关闭记录时应全部显示：%v", got)
	}
	swarmAutoDismissBees(leadID, bees, []string{"h-aspen"})
	got = swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-cove" {
		t.Fatalf("收起的子线程不应再显示：%v", got)
	}
}

// TestTrackSwarmAutoHideSelfHealsStickyHandled 验证残留的 handled 标记会自愈：
// 标记为已处理但子线程仍在浮窗里可见时，清掉标记并按发现时刻重新计时，满 5 秒后照常收起。
func TestTrackSwarmAutoHideSelfHealsStickyHandled(t *testing.T) {
	const leadID = "lead-autohide-sticky-handled"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})
	bees := []SwarmBee{{ThreadID: "h-bee-done", Name: "Done", Status: "completed"}}

	swarmFloatMu.Lock()
	swarmBeeAutoHideHandled[leadID] = map[string]bool{"h-bee-done": true}
	swarmBeeDoneSince[leadID] = map[string]time.Time{"h-bee-done": time.Now().Add(-30 * time.Second)}
	swarmFloatMu.Unlock()

	trackSwarmAutoHide(s, leadID, bees)

	swarmFloatMu.Lock()
	stillHandled := swarmBeeAutoHideHandled[leadID]["h-bee-done"]
	swarmFloatMu.Unlock()
	if stillHandled {
		t.Fatal("浮窗里仍可见的子线程被残留 handled 标记压住，自愈应清掉它")
	}
	if got := swarmVisibleBees(leadID, bees); len(got) != 1 {
		t.Fatalf("自愈后本帧应重新计时、保持可见，实际 %v", got)
	}

	// 重新计时满 5 秒后，下一帧照常收起。
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID]["h-bee-done"] = time.Now().Add(-6 * time.Second)
	swarmFloatMu.Unlock()
	trackSwarmAutoHide(s, leadID, bees)
	if got := swarmVisibleBees(leadID, bees); len(got) != 0 {
		t.Fatalf("自愈并重新计时满 5 秒后应收起，仍可见：%v", got)
	}
}

// resetSwarmAutoHideFor 清空某个主会话的自动收起记账与到期唤醒，供单测前后归零。
func resetSwarmAutoHideFor(leadID string) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	delete(swarmFloatPanelDismissed, leadID)
	delete(swarmFloatDismissedBees, leadID)
	delete(swarmFloatForced, leadID)
	delete(swarmFloatForcedHadBees, leadID)
	delete(swarmFloatPending, leadID)
	delete(swarmBeeDoneSince, leadID)
	delete(swarmBeeAutoHideHandled, leadID)
	delete(swarmBeeSeenActive, leadID)
	delete(swarmBeeKeptOpen, leadID)
	for key, timer := range swarmAutoHideTimers {
		if strings.HasPrefix(key, leadID+"|") {
			timer.Stop()
			delete(swarmAutoHideTimers, key)
		}
	}
}

// TestTrackSwarmAutoHideHidesAlreadyDoneAtStartup 验证重开应用后第一次观察到的子线程全都已结束时，
// 首帧就把它们全部收起（不等 5 秒），浮窗随之隐藏。
func TestTrackSwarmAutoHideHidesAlreadyDoneAtStartup(t *testing.T) {
	const leadID = "lead-restart-all-done"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})
	bees := []SwarmBee{
		{ThreadID: "h-birch", Name: "Birch", Status: "completed"},
		{ThreadID: "h-isle", Name: "Isle", Status: "failed"},
	}

	trackSwarmAutoHide(s, leadID, bees)
	if got := swarmVisibleBees(leadID, bees); len(got) != 0 {
		t.Fatalf("启动前结束的子线程应全部收起：%v", got)
	}
	swarmFloatMu.Lock()
	dismissed := swarmFloatDismissedBees[leadID]["h-isle"]
	panelDismissed := swarmFloatPanelDismissed[leadID]
	swarmFloatMu.Unlock()
	if !dismissed || !panelDismissed {
		t.Fatal("失败的子线程和整个浮窗都应标记为已收起")
	}
}

func TestTrackSwarmAutoHideHidesFailedBee(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seenActive bool
		forced     bool
	}{
		{name: "seen running", seenActive: true},
		{name: "forced", forced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leadID := "lead-failed-" + tc.name
			resetSwarmAutoHideFor(leadID)
			s := newChatState(NewFakeClient())
			t.Cleanup(func() {
				closeEmbeddedBees(s)
				resetSwarmAutoHideFor(leadID)
			})
			bees := []SwarmBee{{ThreadID: "h-bee", Name: "Bee", Status: "running"}}
			if tc.seenActive {
				trackSwarmAutoHide(s, leadID, bees)
			}
			if tc.forced {
				swarmFloatMu.Lock()
				swarmFloatForced[leadID] = true
				swarmFloatMu.Unlock()
			}
			bees[0].Status = "failed"
			trackSwarmAutoHide(s, leadID, bees)
			if got := swarmVisibleBees(leadID, bees); len(got) != 1 {
				t.Fatalf("延迟到期前失败的子线程应保持可见：%v", got)
			}
			advanceSwarmAutoHideDone(leadID, bees)
			trackSwarmAutoHide(s, leadID, bees)
			if got := swarmVisibleBees(leadID, bees); len(got) != 0 {
				t.Fatalf("延迟到期后失败的子线程应收起：%v", got)
			}
		})
	}
}

// TestTrackSwarmAutoHideHidesOnlyAlreadyDoneBee 验证重开应用时还在跑的子线程照常显示，
// 只把已经结束的那个首帧收起。
func TestTrackSwarmAutoHideHidesOnlyAlreadyDoneBee(t *testing.T) {
	const leadID = "lead-restart-mixed"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})
	bees := []SwarmBee{
		{ThreadID: "h-birch", Name: "Birch", Status: "running"},
		{ThreadID: "h-isle", Name: "Isle", Status: "completed"},
	}

	trackSwarmAutoHide(s, leadID, bees)
	got := swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-birch" {
		t.Fatalf("只应收起已结束的子线程，仍可见：%v", got)
	}
	if !swarmFloatVisible(leadID, bees) {
		t.Fatal("还有运行中的子线程时浮窗应继续显示")
	}
}

// TestTrackSwarmAutoHideKeepsDelayForBeeSeenRunning 验证本进程里看着跑起来的子线程结束时不立即收起，
// 仍按 swarmAutoHideDelay 延迟收起。
func TestTrackSwarmAutoHideKeepsDelayForBeeSeenRunning(t *testing.T) {
	const leadID = "lead-restart-seen-running"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})

	trackSwarmAutoHide(s, leadID, []SwarmBee{{ThreadID: "h-birch", Name: "Birch", Status: "running"}})
	done := []SwarmBee{{ThreadID: "h-birch", Name: "Birch", Status: "completed"}}
	trackSwarmAutoHide(s, leadID, done)
	if got := swarmVisibleBees(leadID, done); len(got) != 1 {
		t.Fatalf("跑过再结束的子线程应先保留在浮窗里：%v", got)
	}

	// 把进入终态的时刻往前挪，模拟延迟已过。
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID]["h-birch"] = time.Now().Add(-swarmAutoHideDelay - time.Second)
	swarmFloatMu.Unlock()
	trackSwarmAutoHide(s, leadID, done)
	if got := swarmVisibleBees(leadID, done); len(got) != 0 {
		t.Fatalf("满 %s 后应收起：%v", swarmAutoHideDelay, got)
	}
}

// TestTrackSwarmAutoHideKeepsAwaitingFinalizeBee 验证处于待定稿的已完成设计子代理不会在首帧或超时后被收起，
// 定稿（AwaitingFinalize 变为 false）后才按 swarmAutoHideDelay 正常收起。
func TestTrackSwarmAutoHideKeepsAwaitingFinalizeBee(t *testing.T) {
	const leadID = "lead-awaiting-finalize"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})

	bees := []SwarmBee{{ThreadID: "h-design", Name: "Design", Status: "completed", AwaitingFinalize: true}}
	trackSwarmAutoHide(s, leadID, bees)

	swarmFloatMu.Lock()
	dismissed := swarmFloatDismissedBees[leadID]["h-design"]
	swarmFloatMu.Unlock()
	if dismissed {
		t.Fatal("待定稿的设计子代理首次被观察时不应进入已收起列表")
	}
	if got := swarmVisibleBees(leadID, bees); len(got) != 1 {
		t.Fatalf("待定稿的设计子代理应保持在浮窗里：%v", got)
	}

	// 人为把 doneSince 改早到超过 swarmAutoHideDelay，再跑一帧仍不应收起。
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID] = map[string]time.Time{"h-design": time.Now().Add(-swarmAutoHideDelay - time.Second)}
	swarmFloatMu.Unlock()
	trackSwarmAutoHide(s, leadID, bees)
	swarmFloatMu.Lock()
	dismissed = swarmFloatDismissedBees[leadID]["h-design"]
	swarmFloatMu.Unlock()
	if dismissed {
		t.Fatal("待定稿的设计子代理即使 doneSince 超过延迟也不应被收起")
	}
	if got := swarmVisibleBees(leadID, bees); len(got) != 1 {
		t.Fatalf("待定稿的设计子代理超时后仍应可见：%v", got)
	}

	// 用户定稿后（AwaitingFinalize 改为 false），满 swarmAutoHideDelay 会被收起。
	bees[0].AwaitingFinalize = false
	trackSwarmAutoHide(s, leadID, bees)
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID]["h-design"] = time.Now().Add(-swarmAutoHideDelay - time.Second)
	swarmFloatMu.Unlock()
	trackSwarmAutoHide(s, leadID, bees)
	swarmFloatMu.Lock()
	dismissed = swarmFloatDismissedBees[leadID]["h-design"]
	swarmFloatMu.Unlock()
	if !dismissed {
		t.Fatal("定稿后满延迟应进入已收起列表")
	}
	if got := swarmVisibleBees(leadID, bees); len(got) != 0 {
		t.Fatalf("定稿后满 %s 应收起：%v", swarmAutoHideDelay, got)
	}
}

// TestTrackSwarmAutoHideHidesAgainAfterManualOpen 验证手动打开浮窗后，未被明确点开的已完成子线程
// 从打开时重新计时，满 swarmAutoHideDelay 后照常收起，而不是永久保留。
func TestTrackSwarmAutoHideHidesAgainAfterManualOpen(t *testing.T) {
	const leadID = "lead-manual-open-rehide"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})

	bees := []SwarmBee{
		{ThreadID: "h-aspen", Name: "Aspen", Status: "completed"},
		{ThreadID: "h-cove", Name: "Cove", Status: "running"},
	}
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID] = map[string]time.Time{"h-aspen": time.Now().Add(-time.Minute)}
	swarmBeeSeenActive[leadID] = map[string]bool{"h-aspen": true}
	swarmFloatMu.Unlock()

	trackSwarmAutoHide(s, leadID, bees)
	got := swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-cove" {
		t.Fatalf("已完成的子线程应先被自动收起，仍可见：%v", got)
	}

	undismissSwarmFloatPanel(leadID)
	trackSwarmAutoHide(s, leadID, bees)
	if got := swarmVisibleBees(leadID, bees); len(got) != 2 {
		t.Fatalf("手动打开后应放回已完成的子线程且不立即收起：%v", got)
	}

	// 把重新计时的时刻往前挪，模拟 5 秒已过。
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID]["h-aspen"] = time.Now().Add(-swarmAutoHideDelay - time.Second)
	swarmFloatMu.Unlock()
	trackSwarmAutoHide(s, leadID, bees)
	got = swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-cove" {
		t.Fatalf("满 %s 后应重新收起已完成的子线程：%v", swarmAutoHideDelay, got)
	}
	if !swarmFloatVisible(leadID, bees) {
		t.Fatal("还有运行中的子线程时浮窗应继续显示")
	}
}

// TestTrackSwarmAutoHideKeepsExplicitlyOpenedBee 验证用户明确点开的子线程即使早已完成也不会自动收起。
func TestTrackSwarmAutoHideKeepsExplicitlyOpenedBee(t *testing.T) {
	const leadID = "lead-manual-open-explicit"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})

	bees := []SwarmBee{
		{ThreadID: "h-aspen", Name: "Aspen", Status: "completed"},
		{ThreadID: "h-cove", Name: "Cove", Status: "running"},
	}
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID] = map[string]time.Time{"h-aspen": time.Now().Add(-time.Minute)}
	swarmFloatMu.Unlock()

	openSwarmFloatForBee(leadID, "h-aspen")
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID]["h-aspen"] = time.Now().Add(-swarmAutoHideDelay - time.Second)
	swarmFloatMu.Unlock()

	trackSwarmAutoHide(s, leadID, bees)
	if got := swarmVisibleBees(leadID, bees); len(got) != 2 {
		t.Fatalf("用户明确点开的子线程应保持显示：%v", got)
	}
}

// advanceSwarmAutoHideDone 把所有子线程的完成时刻往前挪，模拟自动收起延迟已经过了好几倍。
func advanceSwarmAutoHideDone(leadID string, bees []SwarmBee) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	dones := swarmBeeDoneSince[leadID]
	if dones == nil {
		dones = make(map[string]time.Time)
		swarmBeeDoneSince[leadID] = dones
	}
	for _, b := range bees {
		dones[b.ThreadID] = time.Now().Add(-3 * swarmAutoHideDelay)
	}
}

// TestOpenSwarmFloatForBeeKeepsPanelUntilClosed 验证用户点开一个启动前就已结束的子线程后，
// 即使时间推进到自动收起延迟的好几倍，它仍留在浮窗里，面板也不会被连带关闭；只有用户关闭才隐藏。
func TestOpenSwarmFloatForBeeKeepsPanelUntilClosed(t *testing.T) {
	const leadID = "lead-open-keeps-panel"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})
	bees := []SwarmBee{
		{ThreadID: "h-birch", Name: "Birch", Status: "completed"},
		{ThreadID: "h-isle", Name: "Isle", Status: "completed"},
		{ThreadID: "h-cove", Name: "Cove", Status: "failed"},
	}

	openSwarmFloatForBee(leadID, "h-isle")
	for frame := 0; frame < 3; frame++ {
		advanceSwarmAutoHideDone(leadID, bees)
		trackSwarmAutoHide(s, leadID, bees)
	}

	got := swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-isle" {
		t.Fatalf("只有点开的子线程应保持显示：%v", got)
	}
	if !swarmFloatVisible(leadID, bees) {
		t.Fatal("用户点开的子线程还在时，浮窗不应被连带关闭")
	}

	dismissSwarmFloatPanel(leadID, bees)
	if swarmFloatVisible(leadID, bees) {
		t.Fatal("用户关闭后浮窗应隐藏")
	}
}

// TestUndismissSwarmFloatPanelAutoHidesAllBees 验证编排按钮整体弹出仍按 5 秒规则收起：
// 整体弹出会解除时间轴里点开的保留，全部子线程到期后一起收起并关闭面板。
func TestUndismissSwarmFloatPanelAutoHidesAllBees(t *testing.T) {
	const leadID = "lead-undismiss-auto-hide"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})
	bees := []SwarmBee{
		{ThreadID: "h-birch", Name: "Birch", Status: "completed"},
		{ThreadID: "h-isle", Name: "Isle", Status: "completed"},
	}

	openSwarmFloatForBee(leadID, "h-isle")
	undismissSwarmFloatPanel(leadID)
	trackSwarmAutoHide(s, leadID, bees)
	if got := swarmVisibleBees(leadID, bees); len(got) != 2 {
		t.Fatalf("整体弹出后应放回全部子线程：%v", got)
	}

	advanceSwarmAutoHideDone(leadID, bees)
	trackSwarmAutoHide(s, leadID, bees)
	if got := swarmVisibleBees(leadID, bees); len(got) != 0 {
		t.Fatalf("整体弹出的面板满 %s 后应全部收起：%v", swarmAutoHideDelay, got)
	}
	if swarmFloatVisible(leadID, bees) {
		t.Fatal("全部收起后整体弹出的面板应关闭")
	}
}

// TestTurnWorkHeaderAccessibleLabel 验证回合折叠组头带按钮角色与说明展开/收起的无障碍名称。
func TestTurnWorkHeaderAccessibleLabel(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	item := timelineItem{kind: timelineItemTurnWork, id: "turnwork-access", workElapsed: "5.0s"}

	for _, open := range []bool{false, true} {
		s.mu.Lock()
		s.expandedTool = map[string]bool{item.id: open}
		s.mu.Unlock()
		want := "工具调用，用时 5.0s，点按展开"
		if open {
			want = "工具调用，用时 5.0s，点按收起"
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderTurnWorkItem(c, s, item, TokenUsage{}, false)
		}, 900, 600)
		if _, ok := tt.AccessByRole(ui.RoleButton, want); !ok {
			t.Fatalf("组头应带无障碍按钮标签 %q", want)
		}
	}
}

// TestSwarmCapsuleText 验证胶囊文案：名字加状态标签，汇报正文按摘要、活动、任务的顺序退回。
func TestSwarmCapsuleText(t *testing.T) {
	h := SwarmBee{Name: "Aspen", Status: "completed", Task: "实现胶囊", Activity: "编写代码中", Summary: "胶囊已接好"}
	if got := swarmCapsuleTitle(h); got != "Aspen 已完成" {
		t.Fatalf("swarmCapsuleTitle() = %q, want %q", got, "Aspen 已完成")
	}
	if got := swarmCapsuleTitle(SwarmBee{Status: "running"}); got != "子线程 运行中" {
		t.Fatalf("缺少名字时应用「子线程」：%q", got)
	}
	if got := swarmCapsuleReport(h); got != "胶囊已接好" {
		t.Fatalf("swarmCapsuleReport() = %q, want 汇报摘要优先", got)
	}
	h.Summary = "  "
	if got := swarmCapsuleReport(h); got != "编写代码中" {
		t.Fatalf("没有摘要时应退回当前活动：%q", got)
	}
	if got := swarmCapsuleReport(SwarmBee{}); got != "" {
		t.Fatalf("全空时应返回空串：%q", got)
	}
}

// TestSwarmNewlyWaiting 验证提问弹出的判定：首次进入等待弹一次，持续等待不重复弹，结束后再等待会再弹。
func TestSwarmNewlyWaiting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prev     map[string]bool
		waiting  []string
		wantNew  []string
		wantNext map[string]bool
	}{
		{
			name:     "首次等待要弹",
			waiting:  []string{"h1"},
			wantNew:  []string{"h1"},
			wantNext: map[string]bool{"h1": true},
		},
		{
			name:     "持续等待不重复弹",
			prev:     map[string]bool{"h1": true},
			waiting:  []string{"h1"},
			wantNew:  nil,
			wantNext: map[string]bool{"h1": true},
		},
		{
			name:     "新增的弹已有的不弹",
			prev:     map[string]bool{"h1": true},
			waiting:  []string{"h1", "h2"},
			wantNew:  []string{"h2"},
			wantNext: map[string]bool{"h1": true, "h2": true},
		},
		{
			name:     "不再等待从集合里删掉",
			prev:     map[string]bool{"h1": true},
			waiting:  nil,
			wantNew:  nil,
			wantNext: map[string]bool{},
		},
		{
			name:     "结束后再次等待会再弹",
			prev:     map[string]bool{},
			waiting:  []string{"h1"},
			wantNew:  []string{"h1"},
			wantNext: map[string]bool{"h1": true},
		},
		{
			name:     "空 ID 忽略",
			waiting:  []string{"", "h1"},
			wantNew:  []string{"h1"},
			wantNext: map[string]bool{"h1": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newIDs, next := swarmNewlyWaiting(tc.prev, tc.waiting)
			if !slices.Equal(newIDs, tc.wantNew) {
				t.Fatalf("newIDs = %v, want %v", newIDs, tc.wantNew)
			}
			if !maps.Equal(next, tc.wantNext) {
				t.Fatalf("next = %v, want %v", next, tc.wantNext)
			}
		})
	}
}

// TestSwarmNewlyWaitingRepeatCycle 验证完整周期：弹 -> 不重复弹 -> 结束后清空 -> 再次等待再弹。
func TestSwarmNewlyWaitingRepeatCycle(t *testing.T) {
	first, next := swarmNewlyWaiting(nil, []string{"h1"})
	if !slices.Equal(first, []string{"h1"}) {
		t.Fatalf("首次等待应弹出：%v", first)
	}
	again, next := swarmNewlyWaiting(next, []string{"h1"})
	if len(again) != 0 {
		t.Fatalf("持续等待不应重复弹：%v", again)
	}
	done, next := swarmNewlyWaiting(next, nil)
	if len(done) != 0 || len(next) != 0 {
		t.Fatalf("不再等待时应清空集合：done=%v next=%v", done, next)
	}
	repeat, _ := swarmNewlyWaiting(next, []string{"h1"})
	if !slices.Equal(repeat, []string{"h1"}) {
		t.Fatalf("结束后再次等待应再弹一次：%v", repeat)
	}
}

// TestSwarmQuestionNoAutoPop 验证子线程提问不再自动弹出浮窗或选中子线程，
// 仅在用户主动点开（openSwarmFloatForBee）时才弹出并选中。
func TestSwarmQuestionNoAutoPop(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "lead-thread-1"
	s.threads = []Thread{
		{ID: "h-birch", HasPendingRequest: true},
		{ID: "h-isle"},
	}
	bees := []SwarmBee{
		{ThreadID: "h-birch", Name: "Birch", Status: "completed"},
		{ThreadID: "h-isle", Name: "Isle", Status: "completed"},
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "lead-thread-1"
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead-thread-1",
		Bees:         bees,
	})
	swarmPanelMu.Unlock()
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, "lead-thread-1")
		delete(swarmFloatDismissedBees, "lead-thread-1")
		delete(swarmFloatForced, "lead-thread-1")
		delete(swarmSelectedBeeID, "lead-thread-1")
		delete(swarmBeeKeptOpen, "lead-thread-1")
		delete(swarmBeeDoneSince, "lead-thread-1")
		delete(swarmBeeAutoHideHandled, "lead-thread-1")
		delete(swarmBeeSeenActive, "lead-thread-1")
		delete(swarmPulseTime, "lead-thread-1|h-birch")
		swarmFloatMu.Unlock()
	})

	dismissSwarmFloatPanel("lead-thread-1", bees)
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeFloats(c, s)
	}, 1600, 900)
	tt.Frame()

	swarmFloatMu.Lock()
	selectedBee := swarmSelectedBeeID["lead-thread-1"]
	swarmFloatMu.Unlock()
	if selectedBee != "" {
		t.Fatalf("子线程提问不应自动选中子线程，got %q", selectedBee)
	}
	if swarmFloatForcedFor("lead-thread-1") {
		t.Fatal("子线程提问不应强制显示浮窗")
	}
	if swarmFloatVisible("lead-thread-1", bees) {
		t.Fatal("子线程提问不再自动弹出浮窗")
	}

	// 提问状态变化后走正常每帧更新，仍不自动弹出。
	s.mu.Lock()
	s.threads[0].HasPendingRequest = false
	s.bumpThreadsRuntimeLocked()
	s.mu.Unlock()
	tt.Frame()
	s.mu.Lock()
	s.threads[0].HasPendingRequest = true
	s.bumpThreadsRuntimeLocked()
	s.mu.Unlock()
	tt.Frame()
	if swarmFloatVisible("lead-thread-1", bees) {
		t.Fatal("再次提问仍不应自动弹出浮窗")
	}

	// 用户手动点「去回答」时仍可打开浮窗并选中该子线程。
	openSwarmFloatForBee("lead-thread-1", "h-birch")
	tt.Frame()
	swarmFloatMu.Lock()
	selectedBee = swarmSelectedBeeID["lead-thread-1"]
	swarmFloatMu.Unlock()
	if selectedBee != "h-birch" || !swarmFloatVisible("lead-thread-1", bees) {
		t.Fatalf("手动打开应弹出浮窗并选中 h-birch，selected=%q visible=%v", selectedBee, swarmFloatVisible("lead-thread-1", bees))
	}
}

// resetSwarmQuestionPopFor 清空某个主会话的选中项与激活脉冲，供单测前后归零。
func resetSwarmQuestionPopFor(leadID string) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	delete(swarmSelectedBeeID, leadID)
	for key, timer := range swarmPulseTimers {
		if strings.HasPrefix(key, leadID+"|") {
			timer.Stop()
			delete(swarmPulseTimers, key)
		}
	}
	for key := range swarmPulseTime {
		if strings.HasPrefix(key, leadID+"|") {
			delete(swarmPulseTime, key)
		}
	}
}

// TestSwarmQuestionKeepsDismissedDoneHidden 验证用户关掉浮窗后子线程新进入等待时不再自动重弹，
// 只有用户手动点「去回答」才只放回该子线程，之前关掉的已完成子线程仍保持收起。
func TestSwarmQuestionKeepsDismissedDoneHidden(t *testing.T) {
	const leadID = "lead-question-pop-dismissed"
	resetSwarmAutoHideFor(leadID)
	resetSwarmQuestionPopFor(leadID)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = leadID
	s.threads = []Thread{
		{ID: "h-aspen"},
		{ID: "h-cove", HasPendingRequest: true},
	}
	bees := []SwarmBee{
		{ThreadID: "h-aspen", Name: "Aspen", Status: "completed"},
		{ThreadID: "h-cove", Name: "Cove", Status: "running"},
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = leadID
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: leadID,
		Bees:         bees,
	})
	swarmPanelMu.Unlock()
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
		resetSwarmQuestionPopFor(leadID)
	})

	dismissSwarmFloatPanel(leadID, bees)
	if swarmFloatVisible(leadID, bees) {
		t.Fatal("用户关闭后浮窗应先隐藏")
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeFloats(c, s)
	}, 1600, 900)
	tt.Frame()
	if swarmFloatVisible(leadID, bees) {
		t.Fatal("子线程新进入等待不应自动重新弹出浮窗")
	}

	openSwarmFloatForBee(leadID, "h-cove")
	tt.Frame()
	if !swarmFloatVisible(leadID, bees) {
		t.Fatal("用户手动打开后浮窗应显示")
	}
	got := swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-cove" {
		t.Fatalf("只应放回被打开的子线程，仍可见：%v", got)
	}
}

// TestOpenSwarmFloatForBeeKeepsOnlyOpenedBee 验证在时间轴里点开某个子线程时，只有被点的那一个回到浮窗并选中，
// 其它已经收起的子线程保持收起，关闭记录与计时都不被改动。
func TestOpenSwarmFloatForBeeKeepsOnlyOpenedBee(t *testing.T) {
	const leadID = "lead-manual-open-handled"
	resetSwarmAutoHideFor(leadID)
	resetSwarmQuestionPopFor(leadID)
	t.Cleanup(func() {
		resetSwarmAutoHideFor(leadID)
		resetSwarmQuestionPopFor(leadID)
	})

	bees := []SwarmBee{
		{ThreadID: "h-aspen", Name: "Aspen", Status: "completed"},
		{ThreadID: "h-cove", Name: "Cove", Status: "completed"},
	}
	dismissSwarmFloatPanel(leadID, bees)
	aspenDone := time.Now().Add(-time.Minute)
	swarmFloatMu.Lock()
	swarmBeeDoneSince[leadID] = map[string]time.Time{"h-aspen": aspenDone}
	swarmFloatMu.Unlock()

	openSwarmFloatForBee(leadID, "h-cove")

	if !swarmFloatForcedFor(leadID) {
		t.Fatal("手动打开应强制显示浮窗")
	}
	swarmFloatMu.Lock()
	openedHandled := swarmBeeAutoHideHandled[leadID]["h-cove"]
	aspenHandled := swarmBeeAutoHideHandled[leadID]["h-aspen"]
	aspenSince := swarmBeeDoneSince[leadID]["h-aspen"]
	selected := swarmSelectedBeeID[leadID]
	swarmFloatMu.Unlock()
	if !openedHandled {
		t.Fatal("被明确点开的子线程应记为已处理")
	}
	if aspenHandled {
		t.Fatal("未被点开的已完成子线程不应记为已处理")
	}
	if !aspenSince.Equal(aspenDone) {
		t.Fatalf("未被点开的子线程计时不应被改动，got %s want %s", aspenSince, aspenDone)
	}
	if selected != "h-cove" {
		t.Fatalf("手动打开应选中指定子线程，got %q", selected)
	}
	got := swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-cove" {
		t.Fatalf("只应放回被点开的子线程，仍可见：%v", got)
	}
	if !swarmFloatVisible(leadID, bees) {
		t.Fatal("被点开的子线程可见，浮窗应显示")
	}
}

// TestOpenSwarmFloatForBeeRestoresOnlyClickedBee 验证三个子线程全部收起后，点其中一个只放回它一个，
// 选中并加一次激活脉冲，另外两个继续收起。
func TestOpenSwarmFloatForBeeRestoresOnlyClickedBee(t *testing.T) {
	const leadID = "lead-open-clicked-of-three"
	resetSwarmAutoHideFor(leadID)
	t.Cleanup(func() {
		resetSwarmAutoHideFor(leadID)
		swarmFloatMu.Lock()
		delete(swarmPulseTime, leadID+"|h-b")
		delete(swarmSelectedBeeID, leadID)
		swarmFloatMu.Unlock()
	})

	bees := []SwarmBee{
		{ThreadID: "h-a", Name: "A", Status: "completed"},
		{ThreadID: "h-b", Name: "B", Status: "completed"},
		{ThreadID: "h-c", Name: "C", Status: "completed"},
	}
	dismissSwarmFloatPanel(leadID, bees)

	openSwarmFloatForBee(leadID, "h-b")

	got := swarmVisibleBees(leadID, bees)
	if len(got) != 1 || got[0].ThreadID != "h-b" {
		t.Fatalf("只应放回被点开的子线程，仍可见：%v", got)
	}
	swarmFloatMu.Lock()
	selected := swarmSelectedBeeID[leadID]
	_, pulsed := swarmPulseTime[leadID+"|h-b"]
	swarmFloatMu.Unlock()
	if selected != "h-b" {
		t.Fatalf("应选中被点开的子线程，got %q", selected)
	}
	if !pulsed {
		t.Fatal("被点开的子线程应记录激活脉冲")
	}
}

// TestSettleSwarmFloatForcedClosesEmptyPanel 验证强制显示期间出现过子代理后，子代理全部结束会自动关闭空面板。
func TestSettleSwarmFloatForcedClosesEmptyPanel(t *testing.T) {
	const leadID = "lead-settle-1"
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmBeeDoneSince, leadID)
		swarmFloatMu.Unlock()
	})

	undismissSwarmFloatPanel(leadID)
	if !settleSwarmFloatForced(leadID, 2) {
		t.Fatal("强制显示且有子代理时应继续显示浮窗")
	}
	if settleSwarmFloatForced(leadID, 0) {
		t.Fatal("子代理全部结束后应自动关闭空白编排面板")
	}
	if swarmFloatForcedFor(leadID) {
		t.Fatal("自动关闭后强制显示标记应被清除")
	}
}

// TestSettleSwarmFloatForcedKeepsManualEmptyPanel 验证用户手动打开且从未有子代理时，空白编排面板保留。
func TestSettleSwarmFloatForcedKeepsManualEmptyPanel(t *testing.T) {
	const leadID = "lead-settle-2"
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmBeeDoneSince, leadID)
		swarmFloatMu.Unlock()
	})

	undismissSwarmFloatPanel(leadID)
	if !settleSwarmFloatForced(leadID, 0) {
		t.Fatal("从未有子代理时手动打开的空面板应保留")
	}
	if !swarmFloatForcedFor(leadID) {
		t.Fatal("从未有子代理时强制显示标记应保留")
	}
}

// TestBeesMenuScroll 验证子代理数量较多时菜单卡片高度受限于 360，且滚动到底后最后一个子代理行可点击。
func TestBeesMenuScroll(t *testing.T) {
	s := newChatState(NewFakeClient())
	const leadID = "lead-menu-scroll"
	s.selectedThreadID = leadID
	bees := make([]SwarmBee, 20)
	seen := make(map[string]bool, len(bees))
	for i := range bees {
		id := fmt.Sprintf("h-scroll-%02d", i)
		bees[i] = SwarmBee{
			ThreadID: id,
			Name:     fmt.Sprintf("Bee %02d", i),
			Task:     "执行任务",
			Status:   "completed",
		}
		seen[id] = true
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = leadID
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: leadID,
		Bees:         bees,
	})
	swarmPanelMu.Unlock()

	reset := func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmBeeDoneSince, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmBeeSeenActive, leadID)
		delete(swarmBeeKeptOpen, leadID)
		delete(swarmMenuOpen, leadID+"|"+string(teamPanelID))
		delete(swarmSelectedBeeID, leadID)
		swarmFloatMu.Unlock()
	}
	reset()
	swarmFloatMu.Lock()
	swarmBeeSeenActive[leadID] = seen
	swarmFloatMu.Unlock()
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		reset()
	})

	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderSwarmBeeFloats(c, s)
	}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
	tt.Frame()

	if err := tt.Click("子代理列表，共 20 个"); err != nil {
		t.Fatalf("打开子代理菜单失败: %v", err)
	}
	tt.Frame()

	scrollRect, ok := tt.Find("swarm-bees-menu-scroll")
	if !ok {
		t.Fatal("缺少子代理菜单滚动区 swarm-bees-menu-scroll")
	}
	menuRect, ok := tt.Find("swarm-bees-menu-" + leadID + "|" + string(teamPanelID))
	if !ok {
		t.Fatal("找不到子代理菜单面板")
	}
	if menuRect.H <= 0 || menuRect.H > 360 {
		t.Fatalf("菜单卡片高度 = %v，期望在 (0, 360] 内", menuRect.H)
	}
	if clearRect, ok := tt.Find("清理已结束的子代理"); !ok || clearRect.H <= 0 {
		t.Fatalf("底部操作按钮应保持可见: ok=%v rect=%+v", ok, clearRect)
	}

	lastLabel := "查看 Bee 19，状态：已完成"
	if err := tt.Click(lastLabel); err == nil {
		t.Fatal("未滚动前最后一个子代理行不应可直接点击")
	}

	tt.Scroll(scrollRect.X+scrollRect.W/2, scrollRect.Y+scrollRect.H/2, 0, 10000)
	tt.Frame()

	lastRect, ok := tt.Find(lastLabel)
	if !ok || lastRect.H <= 0 {
		t.Fatalf("滚动到底后最后一个子代理行不可见: ok=%v rect=%+v", ok, lastRect)
	}
	if err := tt.Click(lastLabel); err != nil {
		t.Fatalf("滚动到底后点击最后一个子代理行失败: %v", err)
	}
	swarmFloatMu.Lock()
	selected := swarmSelectedBeeID[leadID]
	swarmFloatMu.Unlock()
	if selected != "h-scroll-19" {
		t.Fatalf("点击后选中子代理 = %q，期望 h-scroll-19", selected)
	}
}

// TestSwarmFloatPendingLead 验证发给主会话时的待派发浮窗生命周期：
// 标记后无 bees 时浮窗可见且显示“正在派发…”；出现 bees 后 pending 被清除；
// sent 为真且主会话不在运行时 pending 被清除、浮窗隐藏；未 sent 时即使主会话不在运行也保持可见；
// 用户关闭浮窗时同样清除 pending。
func TestSwarmFloatPendingLead(t *testing.T) {
	const leadID = "lead-pending-test"
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = leadID
	s.threads = []Thread{{ID: leadID, IsRunning: false}}

	hp := getSwarmPanelState(s)
	setBees := func(bees []SwarmBee) {
		swarmPanelMu.Lock()
		hp.loadedFor = leadID
		hp.setStateLocked(SwarmState{
			Available:    true,
			Enabled:      true,
			LeadThreadID: leadID,
			Bees:         bees,
		})
		swarmPanelMu.Unlock()
	}
	setBees(nil)
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})

	var frame swarmFloatFrame
	tt := ui.NewTesterWith(func(c *ui.Context) {
		frame = swarmFloatFrameFor(c, s)
		renderSwarmBeeFloats(c, s)
	}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})

	// 先模拟之前关闭过浮窗；标记待派发后应立即清除关闭记录并弹出空团队面板。
	dismissSwarmFloatPanel(leadID, nil)
	markSwarmFloatPending(leadID)
	tt.Frame()

	if !swarmFloatPendingFor(leadID) {
		t.Fatal("未 sent 时即使主会话不在运行，pending 也应保持存在")
	}
	if !frame.scene.has() || !frame.scene.showsTeam || !swarmFloatVisible(leadID, nil) {
		t.Fatalf("标记待派发且无 bees 时浮窗应可见: has=%v showsTeam=%v", frame.scene.has(), frame.scene.showsTeam)
	}
	if !tt.HasText(swarmDispatchPendingText) {
		t.Fatalf("待派发空团队面板应显示 %q, got %v", swarmDispatchPendingText, tt.Texts())
	}

	// 出现 bees 后清除 pending，交还正常流程。
	bees := []SwarmBee{{ThreadID: "h-1", Name: "Aria", Status: "running"}}
	swarmFloatMu.Lock()
	swarmBeeSeenActive[leadID] = map[string]bool{"h-1": true}
	swarmFloatMu.Unlock()
	setBees(bees)
	tt.Frame()

	if swarmFloatPendingFor(leadID) {
		t.Fatal("出现 bees 后应清除 pending")
	}
	if !frame.scene.has() || len(frame.bees) != 1 {
		t.Fatalf("交还正常流程后应显示子代理面板: has=%v bees=%d", frame.scene.has(), len(frame.bees))
	}

	// 回到无 bees 场景：sent 为真且主会话正在运行时保持可见，主会话停止运行后清除 pending 并隐藏浮窗。
	setBees(nil)
	s.mu.Lock()
	s.threads[0].IsRunning = true
	s.mu.Unlock()
	markSwarmFloatPending(leadID)
	markSwarmFloatPendingSent(leadID)
	tt.Frame()

	if !swarmFloatPendingFor(leadID) || !frame.scene.has() {
		t.Fatalf("sent 为真且主会话仍在运行时应保持显示: pending=%v has=%v", swarmFloatPendingFor(leadID), frame.scene.has())
	}

	s.mu.Lock()
	s.threads[0].IsRunning = false
	s.mu.Unlock()
	tt.Frame()

	if swarmFloatPendingFor(leadID) {
		t.Fatal("sent 为真且主会话不在运行时应清除 pending")
	}
	if frame.scene.has() || swarmFloatVisible(leadID, nil) {
		t.Fatalf("清除 pending 后浮窗应隐藏: has=%v visible=%v", frame.scene.has(), swarmFloatVisible(leadID, nil))
	}

	// 用户点击关闭浮窗时应一并清除 pending。
	markSwarmFloatPending(leadID)
	tt.Frame()
	if err := tt.Click("关闭编排面板"); err != nil {
		t.Fatalf("点击关闭面板失败: %v", err)
	}
	tt.Frame()
	if swarmFloatPendingFor(leadID) {
		t.Fatal("用户关闭浮窗后应清除 pending")
	}
	if frame.scene.has() {
		t.Fatal("用户关闭浮窗后场景应隐藏")
	}
}

// TestSwarmFloatHiddenWhenBeeSelected 验证选中子代理（bee）会话时，浮窗不显示所属主会话的子代理。
func TestSwarmFloatHiddenWhenBeeSelected(t *testing.T) {
	const (
		leadID = "lead-bee-selected-test"
		beeID  = "bee-selected-1"
	)
	resetSwarmAutoHideFor(leadID)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = beeID
	s.threads = []Thread{
		{ID: leadID, IsRunning: true},
		{ID: beeID, IsRunning: true},
	}

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = beeID
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		IsBee:        true,
		LeadThreadID: leadID,
		Bees: []SwarmBee{
			{ThreadID: beeID, Name: "Birch", Task: "实现浮窗卡片", Status: "running"},
		},
	})
	swarmPanelMu.Unlock()
	swarmFloatMu.Lock()
	swarmBeeSeenActive[leadID] = map[string]bool{beeID: true}
	swarmFloatMu.Unlock()
	t.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmAutoHideFor(leadID)
	})

	var frame swarmFloatFrame
	tt := ui.NewTesterWith(func(c *ui.Context) {
		frame = swarmFloatFrameFor(c, s)
		renderSwarmBeeFloats(c, s)
	}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
	tt.Frame()

	if len(frame.bees) != 0 {
		t.Fatalf("选中 bee 会话时 frame.bees 应为空，实际有 %d 个", len(frame.bees))
	}
	if frame.scene.has() {
		t.Fatal("选中 bee 会话时浮窗场景不应挂载")
	}
}

// TestSwarmPanelStripMergedSwitcher 验证顶栏合并切换器：
// 1. 顶栏只有一个切换入口，里面显示当前 bee 名字；
// 2. 点击切换器会打开 bees 菜单且不触发拖动；
// 3. 顶栏没有「弹出为独立浮窗」按钮，菜单行里保留该入口；
// 4. 在拖动手柄的空白处拖动会改变停靠位置；
// 5. 全部子代理结束且静止后不再请求下一帧。
func TestSwarmPanelStripMergedSwitcher(t *testing.T) {
	s := newFloatTestState(t)
	const leadID = "lead-thread-1"
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmTeamDock, leadID)
		delete(swarmStackOrder, leadID)
		delete(swarmSelectedBeeID, leadID)
		delete(swarmMenuOpen, leadID+"|"+string(teamPanelID))
		delete(swarmDragPos, leadID+"|"+string(teamPanelID))
		swarmFloatMu.Unlock()
	})

	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderSwarmBeeFloats(c, s)
	}, 1600, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
	tt.Frame()

	switcherRect, ok := tt.Find("子代理列表，共 2 个")
	if !ok || switcherRect.W <= 0 || switcherRect.H <= 0 {
		t.Fatalf("顶栏应存在合并后的切换入口: ok=%v rect=%+v", ok, switcherRect)
	}
	if _, ok := tt.AccessByRole(ui.RoleMenuButton, "子代理列表，共 2 个"); !ok {
		t.Fatal("切换入口应具备菜单按钮无障碍角色")
	}
	nameRect, ok := tt.Find("Birch")
	if !ok {
		t.Fatal("切换入口内应显示当前 bee 名字 Birch")
	}
	if nameRect.X < switcherRect.X || nameRect.X+nameRect.W > switcherRect.X+switcherRect.W ||
		nameRect.Y < switcherRect.Y || nameRect.Y+nameRect.H > switcherRect.Y+switcherRect.H {
		t.Fatalf("当前 bee 名字 %+v 应位于切换入口 %+v 之内", nameRect, switcherRect)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Birch 面板，可拖动"); ok {
		t.Fatal("顶栏拖拽空白区不应作为第二个切换/按钮入口")
	}
	if _, ok := tt.Find("弹出为独立浮窗"); ok {
		t.Fatal("顶栏右侧不应再有「弹出为独立浮窗」按钮")
	}
	if _, ok := tt.Find("在独立窗口打开 Birch"); !ok {
		t.Fatal("顶栏右侧应保留「在独立窗口打开」按钮")
	}
	if _, ok := tt.Find("关闭面板"); !ok {
		t.Fatal("顶栏右侧应保留「关闭面板」按钮")
	}

	// 点击切换器内的当前 bee 名字，应打开 bees 菜单且不触发面板拖动。
	if err := tt.Click("Birch"); err != nil {
		t.Fatalf("点击合并切换器失败: %v", err)
	}
	tt.Frame()

	menuKey := "swarm-bees-menu-" + leadID + "|" + string(teamPanelID)
	if _, ok := tt.Find(menuKey); !ok {
		t.Fatalf("点击切换器后应打开 bees 菜单 %q", menuKey)
	}
	swarmFloatMu.Lock()
	menuOpen := *swarmMenuOpen[leadID+"|"+string(teamPanelID)]
	dragActive := len(swarmDragPos) > 0
	dockBefore := swarmTeamDock[leadID]
	swarmFloatMu.Unlock()
	if !menuOpen {
		t.Fatal("点击切换器后菜单应处于展开态")
	}
	if dragActive || dockBefore != cornerBottomTrailing {
		t.Fatalf("点击切换器不应触发拖动: dragActive=%v dock=%v", dragActive, dockBefore)
	}
	if _, ok := tt.Find("弹出 Birch 为独立浮窗"); !ok {
		t.Fatal("bees 菜单行里应有 Birch 的「弹出为独立浮窗」入口")
	}
	if _, ok := tt.Find("弹出 Isle 为独立浮窗"); !ok {
		t.Fatal("bees 菜单行里应有 Isle 的「弹出为独立浮窗」入口")
	}

	// 点菜单外空白收起菜单，再从拖动手柄的空白处把面板拖到左上角。
	tt.ClickAt(10, 10)
	tt.Frame()

	handleRect, ok := tt.Find("Birch 面板，可拖动")
	if !ok || handleRect.W <= 0 || handleRect.H <= 0 {
		t.Fatalf("未找到顶栏空白拖动手柄: ok=%v rect=%+v", ok, handleRect)
	}
	if handleRect.X < switcherRect.X+switcherRect.W {
		t.Fatalf("空白拖动手柄 %+v 应位于切换器 %+v 右侧", handleRect, switcherRect)
	}
	hx, hy := handleRect.X+handleRect.W/2, handleRect.Y+handleRect.H/2
	tt.Press(hx, hy)
	tt.Move(hx-1000, hy-500)
	tt.Release(hx-1000, hy-500)
	tt.Frame()

	swarmFloatMu.Lock()
	dockAfter := swarmTeamDock[leadID]
	swarmFloatMu.Unlock()
	if dockAfter == cornerBottomTrailing {
		t.Fatalf("在拖动手柄空白处拖动后停靠位置应改变，仍为 %v", dockAfter)
	}
	if dockAfter != cornerTopLeading {
		t.Fatalf("拖向左上后停靠位置 = %v，want %v", dockAfter, cornerTopLeading)
	}

	// 全部子代理转为完成后，待过渡动画走完在静止状态下不再请求下一帧。
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	st := hp.state
	st.Bees = slices.Clone(st.Bees)
	st.Bees[0].Status = "completed"
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()
	tt.Frame()
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Fatal("全部子代理完成且静止后不应继续请求下一帧")
	}

	// 开启应用内减少动效时，状态切换立即到位且不请求下一帧。
	setAppReduceMotion(t, true)
	swarmPanelMu.Lock()
	st2 := hp.state
	st2.Bees = slices.Clone(st2.Bees)
	st2.Bees[0].Status = "failed"
	hp.setStateLocked(st2)
	swarmPanelMu.Unlock()
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("ReduceMotion 开启时状态切换不应继续请求下一帧")
	}
}

// TestSwarmFloatShowsClearedKeptOpenBee 验证已被清理出 Bees 但通过 openSwarmFloatForBee 打开的子代理能在浮窗显示。
func TestSwarmFloatShowsClearedKeptOpenBee(t *testing.T) {
	const leadID = "lead-cleared-kept-open"
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	s := newChatState(NewFakeClient())
	s.mu.Lock()
	s.selectedThreadID = leadID
	s.threads = []Thread{
		{ID: leadID, Title: "Lead"},
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

	t.Cleanup(func() {
		closeEmbeddedBees(s)
		s.close()
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		delete(swarmSelectedBeeID, leadID)
		delete(swarmBeeKeptOpen, leadID)
		delete(swarmBeeDoneSince, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmBeeSeenActive, leadID)
		delete(swarmPulseTime, leadID+"|bee-cleared")
		swarmFloatMu.Unlock()
	})

	openSwarmFloatForBee(leadID, "bee-cleared")

	var frame swarmFloatFrame
	tt := ui.NewTester(func(c *ui.Context) {
		frame = swarmFloatFrameFor(c, s)
		renderSwarmBeeFloats(c, s)
	}, 1600, 900)
	tt.Frame()

	found := false
	for _, b := range frame.bees {
		if b.ThreadID == "bee-cleared" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("浮窗帧应包含已清理但手动打开的子代理 bee-cleared，got=%+v", frame.bees)
	}
	if !tt.HasText("Dune") {
		t.Fatalf("浮窗渲染应显示子线程名 Dune，texts=%v", tt.Texts())
	}
}
