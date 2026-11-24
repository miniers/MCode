package mcodeui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/providers/agy/quota"
	"mcode/internal/testenv"
)

// quotaPopoverWindows 造出多账号、多窗口的额度数据，用来把弹层撑高。
func quotaPopoverWindows(accounts int) []ProviderQuotaWindow {
	var windows []ProviderQuotaWindow
	for i := 0; i < accounts; i++ {
		account := fmt.Sprintf("claude · 账号%d", i)
		windows = append(windows,
			ProviderQuotaWindow{Label: account + " · 5 hours", Account: account, Period: "5h", UsedPercent: float64(10 + i)},
			ProviderQuotaWindow{Label: account + " · 7 days", Account: account, Period: "7d", UsedPercent: float64(20 + i)},
		)
	}
	return windows
}

// quotaPopoverTester 把额度条放在窗口底部，模拟 composer 里的真实位置。
func quotaPopoverTester(s *chatState, th *Thread, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("quota-popover-host")).Fill().Children(func() {
			ui.Box(c.Key("quota-popover-spacer")).Grow(1)
			renderComposerQuotaStrip(c, s, th)
		})
	}, width, height)
}

func TestComposerQuotaPopoverHeightCappedAndScrolls(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}
	SetChatQuotaWindowsForProvider(s, th.Provider, quotaPopoverWindows(8))
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	const width, height = 480, 360
	tt := quotaPopoverTester(s, th, width, height)
	if err := tt.Click("额度 " + s.providerDisplayName(th.Provider)); err != nil {
		t.Fatalf("打开额度弹层失败: %v", err)
	}

	// 所有账号与窗口都已渲染（滚动容器只裁可视区域，不丢条目）。
	if !tt.HasText("账号7") {
		t.Fatalf("弹层应列出全部账号，texts: %q", tt.Texts())
	}
	panel, ok := tt.Find("额度详情")
	if !ok {
		t.Fatal("找不到额度弹层面板")
	}
	// 16 行额度的内容远高于窗口：面板必须被限高，而不是靠窗口裁剪。
	// 未被限高时面板会铺满整窗（约 height-16），因此这里留出 32 像素余量。
	if panel.H > height-32 {
		t.Fatalf("弹层没有被限高: panel=%+v，窗口高 %d", panel, height)
	}
	list, ok := tt.Find("额度列表")
	if !ok {
		t.Fatal("额度列表应放在可滚动容器里")
	}
	if list.H <= 0 || list.H > panel.H-40 {
		t.Fatalf("列表应是固定标题下方的滚动视口: panel=%+v list=%+v", panel, list)
	}
}

func TestComposerQuotaPopoverRefreshRequestsProviderReload(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	gate := make(chan struct{})
	fake.refreshProviderQuotaGate = gate
	fake.providerQuotaResult = []ProviderQuotaWindow{{Label: "claude · 5 hours", Account: "claude", Period: "5h", UsedPercent: 12}}
	s := newChatState(fake)
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}
	SetChatQuotaWindowsForProvider(s, th.Provider, quotaPopoverWindows(1))
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	tt := quotaPopoverTester(s, th, 480, 400)
	if err := tt.Click("额度 " + s.providerDisplayName(th.Provider)); err != nil {
		t.Fatalf("打开额度弹层失败: %v", err)
	}
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatalf("点击刷新失败: %v", err)
	}

	// 刷新进行中：保持纯图标无文字标签，重复点击不会再发一次请求。
	waitFor(t, tt, func() bool { return composerQuotaRefreshing(s, th.Provider) })
	if slices.Contains(tt.Texts(), "刷新中…") || slices.Contains(tt.Texts(), "刷新") {
		t.Fatalf("刷新按钮不应显示文字标签，texts=%q", tt.Texts())
	}
	// 界面在 goroutine 调用 fake 之前就已进入刷新中，这里要等 fake 真正收到请求，
	// 否则下面的长度断言会受 goroutine 调度时机影响而偶发失败。
	waitFor(t, tt, func() bool { return len(fake.RefreshProviderQuotaCalls()) >= 1 })
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatalf("点击刷新中按钮失败: %v", err)
	}
	if calls := fake.RefreshProviderQuotaCalls(); len(calls) != 1 || calls[0] != "pi:magpie-jp" {
		t.Fatalf("刷新请求的 provider = %v，想要 [pi:magpie-jp]", calls)
	}

	close(gate)
	waitFor(t, tt, func() bool { return !composerQuotaRefreshing(s, th.Provider) })
	if calls := fake.RefreshProviderQuotaCalls(); len(calls) != 1 {
		t.Fatalf("刷新完成后仍不应重复请求，calls = %v", calls)
	}
}

// quotaPopoverRowWindows 造出 2 个账号 × 2 个窗口的额度：同一组的两个窗口重置时间不同，
// 组标题不显示共同倒计时，倒计时落在各自的行里；已用比例都低于已过去比例，避免触发配速
// 预测把倒计时收进 Tooltip，否则就量不到行内文字。
func quotaPopoverRowWindows(now time.Time) []ProviderQuotaWindow {
	return []ProviderQuotaWindow{
		{Label: "claude · 账号0 · 5 hours", Account: "claude · 账号0", Period: "5h", UsedPercent: 5, ResetsAt: now.Add(3*time.Hour + 43*time.Minute + 30*time.Second)},
		{Label: "claude · 账号0 · 7 days", Account: "claude · 账号0", Period: "7d", UsedPercent: 10, ResetsAt: now.Add(6 * 24 * time.Hour)},
		{Label: "claude · 账号1 · 5 hours", Account: "claude · 账号1", Period: "5h", UsedPercent: 50, ResetsAt: now.Add(2*time.Hour + 30*time.Second)},
		{Label: "claude · 账号1 · 7 days", Account: "claude · 账号1", Period: "7d", UsedPercent: 12, ResetsAt: now.Add(5 * 24 * time.Hour)},
	}
}

func TestComposerQuotaPopoverUsesSharedGroups(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}
	windows := quotaPopoverRowWindows(time.Now())
	for i := range windows {
		windows[i].Source, windows[i].Provider = "magpie", "claude"
	}
	SetChatQuotaWindowsForProvider(s, th.Provider, windows)
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		})
	}, 480, 480)
	if !tt.HasText(s.providerDisplayName(th.Provider) + " · Claude") {
		t.Fatalf("单组浮窗标题不正确: %q", tt.Texts())
	}
	if !tt.HasText("账号0") || !tt.HasText("账号1") || tt.HasText("claude · 账号") {
		t.Fatalf("账号名不应包含供应商前缀: %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if text == "Claude" {
			t.Fatal("单组浮窗不应显示供应商组标题")
		}
	}
	for _, text := range []string{"5%", "10%", "50%", "12%"} {
		if !tt.HasText(text) {
			t.Fatalf("缺少共享额度行 %q: %q", text, tt.Texts())
		}
	}
	t.Run("普通供应商单组标题", func(t *testing.T) {
		th := &Thread{ID: "t2", Provider: "claude"}
		SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{{Label: "5 hours", UsedPercent: 42}})
		t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })
		tt := ui.NewTester(func(c *ui.Context) {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		}, 480, 480)
		title := s.providerDisplayName(th.Provider)
		if !tt.HasText(title) || tt.HasText(title+" · "+title) {
			t.Fatalf("普通供应商标题不应重复: %q", tt.Texts())
		}
	})
}

func TestComposerQuotaGroupModels(t *testing.T) {
	windows := []ProviderQuotaWindow{
		{Label: "5 hours", Account: "claude · a", Source: "magpie", Provider: "claude", Last: true},
		{Label: "5 hours", Account: "codex · b", Source: "magpie", Provider: "codex", Active: true},
	}
	groups := composerQuotaGroupModels("pi:magpie", "Magpie", windows, "codex · b", "claude · a", time.Now())
	if len(groups) != 2 || groups[0].IconID != "codex" || groups[1].IconID != "claude" {
		t.Fatalf("供应商排序或图标错误: %+v", groups)
	}
	if !groups[0].Accounts[0].IsActive || !groups[1].Accounts[0].IsRecent {
		t.Fatalf("账号状态错误: %+v", groups)
	}
	if groups[0].Accounts[0].DisplayName != "b" || groups[1].Accounts[0].DisplayName != "a · 最近使用" {
		t.Fatalf("账号名不应包含供应商前缀: %+v", groups)
	}
	fallback := composerQuotaGroupModels("pi:magpie-jp", "magpie-jp", quotaPopoverRowWindows(time.Now()), "", "", time.Now())
	if len(fallback) != 1 || len(fallback[0].Accounts) != 2 || fallback[0].Accounts[0].DisplayName != "账号0" || fallback[0].Accounts[1].DisplayName != "账号1" {
		t.Fatalf("缺少供应商元数据时账号名不应包含前缀: %+v", fallback)
	}
	direct := composerQuotaGroupModels("codex", "Codex", []ProviderQuotaWindow{{Label: "5 hours", Account: "a"}}, "", "", time.Now())
	if len(direct) != 1 || direct[0].Title != "Codex" || !direct[0].Accounts[0].IsActive {
		t.Fatalf("单供应商模型错误: %+v", direct)
	}
}

func TestComposerQuotaGroupModels_SkipsAllDisabledProviderGroup(t *testing.T) {
	now := time.Now()
	windows := []ProviderQuotaWindow{
		{Label: "5 hours", Account: "claude · disabled@example.com", Source: "magpie", Provider: "claude", Disabled: true},
		{Label: "5 hours", Account: "codex · a@example.com", Source: "magpie", Provider: "codex"},
	}
	groups := composerQuotaGroupModels("pi:magpie", "Magpie", windows, "", "", now)
	if len(groups) != 1 {
		t.Fatalf("应只返回一个未停用分组，实际=%d: %+v", len(groups), groups)
	}
	if groups[0].Provider != "magpie:codex" {
		t.Fatalf("返回的 Provider = %q，想要 %q", groups[0].Provider, "magpie:codex")
	}
	if len(groups[0].Accounts) != 1 || groups[0].Accounts[0].DisplayName != "a@example.com" {
		t.Fatalf("账号 DisplayName 应去掉前缀，实际=%+v", groups[0].Accounts)
	}

	// 验证不会因下标错位拿到已丢弃分组的 Windows（两个分组同账号名时，不应按前一组 Provider 去前缀）。
	mismatched := composerQuotaGroupModels("pi:magpie", "Magpie", []ProviderQuotaWindow{
		{Label: "5 hours", Account: "claude · shared@example.com", Source: "magpie", Provider: "claude", Disabled: true},
		{Label: "5 hours", Account: "claude · shared@example.com", Source: "magpie", Provider: "codex"},
	}, "", "", now)
	if len(mismatched) != 1 || mismatched[0].Provider != "magpie:codex" {
		t.Fatalf("错位验证分组结果错误: %+v", mismatched)
	}
	if len(mismatched[0].Accounts) != 1 || mismatched[0].Accounts[0].DisplayName != "claude · shared@example.com" {
		t.Fatalf("不应拿已丢弃分组的 Provider 去掉当前分组账号前缀，实际=%+v", mismatched[0].Accounts)
	}
}

func TestQuotaGroupTitleVisible(t *testing.T) {
	if quotaGroupTitleVisible(1, quotaGroupListOptions{HideSingleGroupTitle: true}) ||
		!quotaGroupTitleVisible(2, quotaGroupListOptions{HideSingleGroupTitle: true}) ||
		!quotaGroupTitleVisible(1, quotaGroupListOptions{}) {
		t.Fatal("供应商标题显示条件错误")
	}
}

func TestQuotaResetDayInPopoverAndTray(t *testing.T) {
	testenv.TempHome(t)
	cst := time.FixedZone("CST", 8*3600)
	// 2026-11-21 12:00:00 +08:00（周六）
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, cst)
	oldNow := usagePanelNow
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = oldNow })

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}

	// a@example.com：周窗口已用 60%，重置在 4 天后（周三），按当前速率 2 天后（周一）用尽。
	// b@example.com：周窗口已用 18%，重置在 5 天后（周四），节奏正常。
	// c@example.com：周窗口重置时刻为零，不占重置日列宽。
	windows := []ProviderQuotaWindow{
		{Label: "claude · a@example.com · 5 hours", Account: "claude · a@example.com", Source: "magpie", Provider: "claude", Period: "5h", UsedPercent: 10, ResetsAt: now.Add(4 * time.Hour)},
		{Label: "claude · a@example.com · 7 days", Account: "claude · a@example.com", Source: "magpie", Provider: "claude", Period: "7d", UsedPercent: 60, ResetsAt: now.Add(4 * 24 * time.Hour)},
		{Label: "claude · b@example.com · 5 hours", Account: "claude · b@example.com", Source: "magpie", Provider: "claude", Period: "5h", UsedPercent: 5, ResetsAt: now.Add(4 * time.Hour)},
		{Label: "claude · b@example.com · 7 days", Account: "claude · b@example.com", Source: "magpie", Provider: "claude", Period: "7d", UsedPercent: 18, ResetsAt: now.Add(5 * 24 * time.Hour)},
		{Label: "claude · c@example.com · 5 hours", Account: "claude · c@example.com", Source: "magpie", Provider: "claude", Period: "5h", UsedPercent: 15, ResetsAt: now.Add(3 * time.Hour)},
		{Label: "claude · c@example.com · 7 days", Account: "claude · c@example.com", Source: "magpie", Provider: "claude", Period: "7d", UsedPercent: 20},
	}
	SetChatQuotaWindowsForProvider(s, th.Provider, windows)
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		})
	}, 480, 560)
	tt.Frame()

	if tt.HasText("本周重置") {
		t.Fatalf("浮层里不应再出现「本周重置」: %q", tt.Texts())
	}
	if tt.HasText("周: 周一用尽 · 周三重置") {
		t.Fatalf("新版列表右列应显示紧凑词，未悬停时不应显示长文案: %q", tt.Texts())
	}

	mustFind := func(tester *ui.Tester, text string) ui.Rect {
		r, ok := tester.Find(text)
		if !ok {
			t.Fatalf("缺少文字 %q: %q", text, tester.Texts())
		}
		return r
	}
	centerY := func(r ui.Rect) float32 { return r.Y + r.H/2 }

	p60 := mustFind(tt, "60%")
	p18 := mustFind(tt, "18%")
	sMon := mustFind(tt, "周一用尽")
	s4h := mustFind(tt, "4h 重置")
	s3h := mustFind(tt, "3h 重置")

	if !(sMon.X > p60.X) || !(s4h.X > p18.X) {
		t.Fatalf("状态列应位于百分比右侧：p60=%+v mon=%+v p18=%+v h4=%+v", p60, sMon, p18, s4h)
	}
	if d := centerY(sMon) - centerY(p60); d > 2 || d < -2 {
		t.Fatalf("周一用尽应与 60%% 同行：mon=%+v p60=%+v", sMon, p60)
	}
	if d := centerY(s4h) - centerY(p18); d > 2 || d < -2 {
		t.Fatalf("4h 重置应与 18%% 同行：h4=%+v p18=%+v", s4h, p18)
	}
	if d := (sMon.X + sMon.W) - (s4h.X + s4h.W); d > 1 || d < -1 {
		t.Fatalf("多账号状态列右边缘应对齐：mon=%+v h4=%+v", sMon, s4h)
	}
	if d := (sMon.X + sMon.W) - (s3h.X + s3h.W); d > 1 || d < -1 {
		t.Fatalf("多账号状态列右边缘应对齐：mon=%+v h3=%+v", sMon, s3h)
	}

	// 悬停 a@example.com 行并在停留超过原生 tooltip 延迟后应展示完整用尽与重置日详情。
	rA := mustFind(tt, "a@example.com")
	tt.Move(rA.X+rA.W/2, rA.Y+rA.H/2)
	tt.Frame()
	tt.Advance(700 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("周 60% · 周一用尽 · 周三重置") {
		t.Fatalf("悬停提前用尽账号行应展示完整详情，texts=%q", tt.Texts())
	}

	// 托盘宽度（400px）下不溢出、不换行。
	trayModel := buildTrayQuotaModel([]TrayQuotaGroup{{
		Provider: "claude",
		Title:    "Claude",
		Windows: []ProviderQuotaWindow{
			{Account: "a@example.com", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: now.Add(4 * time.Hour)},
			{Account: "a@example.com", Label: "Weekly", Period: "7d", UsedPercent: 60, ResetsAt: now.Add(4 * 24 * time.Hour)},
		},
	}}, now, now, false)
	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	ttTray := ui.NewTester(func(c *ui.Context) {
		panel.renderModelView(c, trayModel)
	}, 400, 480)
	ttTray.Frame()

	trayPct := mustFind(ttTray, "60%")
	trayMon := mustFind(ttTray, "周一用尽")
	if trayMon.X+trayMon.W > 400 {
		t.Fatalf("托盘宽度下状态列不应溢出：mon=%+v", trayMon)
	}
	if d := centerY(trayMon) - centerY(trayPct); d > 2 || d < -2 {
		t.Fatalf("托盘宽度下状态列不应换行：mon=%+v pct=%+v", trayMon, trayPct)
	}
}

func TestComposerQuotaRedesignPopoverWidthAndRefreshIconButton(t *testing.T) {
	testenv.TempHome(t)
	if quotaPopoverWidth != 456 {
		t.Fatalf("quotaPopoverWidth = %v, want 456", quotaPopoverWidth)
	}
	setAppReduceMotion(t, true)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "codex"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 hours", Period: "5h", Plan: "plus", UsedPercent: 18, ResetsAt: time.Now().Add(3 * time.Hour)},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	tt := ui.NewTesterWith(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, th, th.Provider, 0)
		})
	}, 480, 320, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})

	if slices.Contains(tt.Texts(), "刷新") || slices.Contains(tt.Texts(), "刷新中…") {
		t.Fatalf("刷新按钮应为纯图标不含文字，texts=%q", tt.Texts())
	}
	if !tt.HasText("· plus") {
		t.Fatalf("单组有 Plan 时顶栏应保留「· plus」，texts=%q", tt.Texts())
	}
	if tt.HasText("自动选号") || tt.HasText("指定") {
		t.Fatalf("非可选账号供应商不应显示顶栏路由分段，texts=%q", tt.Texts())
	}

	refreshBtn, ok := tt.AccessByRole(ui.RoleButton, "刷新额度")
	if !ok {
		t.Fatalf("找不到无障碍标签为「刷新额度」的刷新按钮")
	}
	if refreshBtn.Bounds.W != ControlSM || refreshBtn.Bounds.H != ControlSM {
		t.Fatalf("刷新按钮应为 ControlSM 正方形，bounds=%+v", refreshBtn.Bounds)
	}
	countLabel := func() int {
		n := 0
		for _, txt := range tt.Texts() {
			if txt == "刷新额度" {
				n++
			}
		}
		return n
	}
	if got := countLabel(); got != 1 {
		t.Fatalf("悬停前「刷新额度」应仅作为无障碍标签出现 1 次，实际=%d，texts=%q", got, tt.Texts())
	}
	tt.Move(refreshBtn.Bounds.X+refreshBtn.Bounds.W/2, refreshBtn.Bounds.Y+refreshBtn.Bounds.H/2)
	tt.Advance(time.Second)
	if got := countLabel(); got < 2 {
		t.Fatalf("悬停刷新按钮后应展示 Tooltip「刷新额度」，出现次数=%d，texts=%q", got, tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("静止状态下刷新按钮不应请求后续帧")
	}

	s.mu.Lock()
	if s.quotaRefreshingProviders == nil {
		s.quotaRefreshingProviders = make(map[string]bool)
	}
	s.quotaRefreshingProviders[th.Provider] = true
	s.mu.Unlock()
	tt.Frame()
	if tt.HasText("刷新中…") {
		t.Fatalf("加载中刷新按钮仍不应展示文字，texts=%q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("ReduceMotion 开启时加载中刷新按钮不应请求后续帧")
	}

	t.Run("正常动效下加载中旋转且不重建组件树、结束后静止", func(t *testing.T) {
		setAppReduceMotion(t, false)
		fixedNow := time.Now()
		oldNow := usagePanelNow
		usagePanelNow = func() time.Time { return fixedNow }
		t.Cleanup(func() { usagePanelNow = oldNow })
		s.mu.Lock()
		s.quotaRefreshingProviders[th.Provider] = false
		s.mu.Unlock()
		builds := 0
		ttSpin := ui.NewTester(func(c *ui.Context) {
			builds++
			ui.Box(c).Width(quotaPopoverWidth).Children(func() {
				renderComposerQuotaPopover(c, s, th, th.Provider, 0)
			})
		}, 480, 320)
		ttSpin.Frame()
		ttSpin.Advance(DurBase + 50*time.Millisecond)
		ttSpin.Frame()
		if ttSpin.FramePending() {
			t.Fatal("静止态下刷新按钮不应请求后续帧")
		}

		s.mu.Lock()
		s.quotaRefreshingProviders[th.Provider] = true
		s.mu.Unlock()
		ttSpin.Frame()
		beforeBuilds := builds
		img1 := slices.Clone(ttSpin.Image().Pix)
		ttSpin.Advance(150 * time.Millisecond)
		img2 := slices.Clone(ttSpin.Image().Pix)
		if builds != beforeBuilds {
			t.Fatalf("加载中旋转不应重建组件树：%d → %d", beforeBuilds, builds)
		}
		if ttSpin.FramePending() {
			t.Fatal("Draw 旋转应只重画图标，不应通过组件树请求下一帧")
		}
		if slices.Equal(img1, img2) {
			t.Fatal("加载中推进时钟后刷新图标像素应随旋转变化")
		}

		s.mu.Lock()
		s.quotaRefreshingProviders[th.Provider] = false
		s.mu.Unlock()
		ttSpin.Frame()
		afterBuilds := builds
		img3 := slices.Clone(ttSpin.Image().Pix)
		ttSpin.Advance(150 * time.Millisecond)
		img4 := slices.Clone(ttSpin.Image().Pix)
		if builds != afterBuilds || ttSpin.FramePending() {
			t.Fatalf("加载结束后不应继续重绘：builds=%d->%d pending=%v", afterBuilds, builds, ttSpin.FramePending())
		}
		if !slices.Equal(img3, img4) {
			t.Fatal("加载结束后推进时钟像素不应再变化")
		}
	})
}

func TestComposerQuotaRedesignHeaderRouteSegmentedAndSelection(t *testing.T) {
	th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
	fake, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))
	s.composerView.quotaOpen = true
	s.composerView.quotaPopoverKey = "agy"

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, currentTestThreadSnapshot(s, th.ID, th), "agy", 0)
		})
	}, 480, 480)

	if _, ok := tt.AccessByRole(ui.RoleButton, "自动"); ok {
		t.Fatal("顶栏改版后不应再有独立的「自动」行按钮")
	}
	if _, ok := tt.Find("composer-quota-account-auto"); ok {
		t.Fatal("不应再渲染 composer-quota-account-auto")
	}
	segRect, ok := tt.Find("账号路由模式")
	if !ok {
		t.Fatalf("accountSelectable 时顶栏应出现「账号路由模式」分段，texts=%q", tt.Texts())
	}
	if segRect.H > ControlSM {
		t.Fatalf("分段胶囊高度不应超过 ControlSM (%v)，实际=%v", ControlSM, segRect.H)
	}
	refreshBtn, ok := tt.AccessByRole(ui.RoleButton, "刷新额度")
	if !ok {
		t.Fatal("找不到刷新按钮")
	}
	if segRect.X+segRect.W > refreshBtn.Bounds.X {
		t.Fatalf("分段胶囊应位于刷新按钮左侧：seg=%+v refresh=%+v", segRect, refreshBtn.Bounds)
	}

	autoBtn, ok := tt.AccessByRole(ui.RoleButton, "自动选号")
	if !ok || !autoBtn.Checked || autoBtn.Disabled {
		t.Fatalf("currentAccountID 为空时「自动选号」应选中且可用: ok=%v node=%+v", ok, autoBtn)
	}
	manualBtn, ok := tt.AccessByRole(ui.RoleButton, "指定")
	if !ok || manualBtn.Checked || manualBtn.Disabled {
		t.Fatalf("currentAccountID 为空时「指定」应未选中且可用: ok=%v node=%+v", ok, manualBtn)
	}

	// 当前在用账号为 个人账号 (acc-1)，点「指定」应优先以 acc-1 调用选择。
	if err := tt.Click("指定"); err != nil {
		t.Fatalf("点击「指定」失败: %v", err)
	}
	if got := currentTestThreadSnapshot(s, th.ID, th).Account; got != "acc-1" {
		t.Fatalf("点击「指定」后线程 Account = %q, want %q", got, "acc-1")
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetThreadAccountCalls()
		return len(calls) == 1 && calls[0].ThreadID == "t1" && calls[0].AccountID == "acc-1"
	})

	s.composerView.quotaOpen = true
	s.composerView.quotaPopoverKey = "agy"
	tt.Frame()
	autoBtn, _ = tt.AccessByRole(ui.RoleButton, "自动选号")
	manualBtn, _ = tt.AccessByRole(ui.RoleButton, "指定")
	if autoBtn.Checked || !manualBtn.Checked {
		t.Fatalf("指定账号后应切换选中态: auto=%+v manual=%+v", autoBtn, manualBtn)
	}

	// 点「自动选号」应以空串调用选择。
	if err := tt.Click("自动选号"); err != nil {
		t.Fatalf("点击「自动选号」失败: %v", err)
	}
	if got := currentTestThreadSnapshot(s, th.ID, th).Account; got != "" {
		t.Fatalf("点击「自动选号」后线程 Account 应为空，实际=%q", got)
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetThreadAccountCalls()
		return len(calls) == 2 && calls[1].ThreadID == "t1" && calls[1].AccountID == ""
	})
}

func TestComposerQuotaRedesignManualFallbackAndAllDisabled(t *testing.T) {
	t.Run("无在用账号时选首个未禁用账号", func(t *testing.T) {
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
		windows := []ProviderQuotaWindow{
			{Label: "账号1 · " + quota.GroupGemini, Account: "账号1", AccountID: "acc-1", Period: "5h", UsedPercent: 100},
			{Label: "账号2 · " + quota.GroupGemini, Account: "账号2", AccountID: "acc-2", Period: "5h", UsedPercent: 20},
		}
		fake, s := newAgyAccountTestState(t, th, windows)
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c).Width(quotaPopoverWidth).Children(func() {
				renderComposerQuotaPopover(c, s, currentTestThreadSnapshot(s, th.ID, th), "agy", 0)
			})
		}, 480, 480)

		if err := tt.Click("指定"); err != nil {
			t.Fatalf("点击「指定」失败: %v", err)
		}
		if got := currentTestThreadSnapshot(s, th.ID, th).Account; got != "acc-2" {
			t.Fatalf("应回退选首个未禁用账号 acc-2，实际=%q", got)
		}
		waitFor(t, tt, func() bool {
			calls := fake.SetThreadAccountCalls()
			return len(calls) == 1 && calls[0].AccountID == "acc-2"
		})
	})

	t.Run("全部禁用时点指定不动作", func(t *testing.T) {
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
		windows := []ProviderQuotaWindow{
			{Label: "账号1 · " + quota.GroupGemini, Account: "账号1", AccountID: "acc-1", Period: "5h", UsedPercent: 100},
			{Label: "账号2 · " + quota.GroupGemini, Account: "账号2", AccountID: "acc-2", Period: "5h", UsedPercent: 100},
		}
		fake, s := newAgyAccountTestState(t, th, windows)
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c).Width(quotaPopoverWidth).Children(func() {
				renderComposerQuotaPopover(c, s, currentTestThreadSnapshot(s, th.ID, th), "agy", 0)
			})
		}, 480, 480)

		if err := tt.Click("指定"); err != nil {
			t.Fatalf("点击「指定」失败: %v", err)
		}
		if got := currentTestThreadSnapshot(s, th.ID, th).Account; got != "" {
			t.Fatalf("无可用账号时点「指定」不应修改 Account，实际=%q", got)
		}
		if calls := fake.SetThreadAccountCalls(); len(calls) != 0 {
			t.Fatalf("无可用账号时不应发起 SetThreadAccount，calls=%+v", calls)
		}
	})
}

func TestComposerQuotaRedesignLockedDisablesSegmented(t *testing.T) {
	th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy", Account: "acc-2"}
	fake, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))
	s.mu.Lock()
	s.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(quotaPopoverWidth).Children(func() {
			renderComposerQuotaPopover(c, s, currentTestThreadSnapshot(s, th.ID, th), "agy", 0)
		})
	}, 480, 480)

	autoBtn, ok := tt.AccessByRole(ui.RoleButton, "自动选号")
	if !ok || !autoBtn.Disabled {
		t.Fatalf("locked 时「自动选号」应禁用: ok=%v node=%+v", ok, autoBtn)
	}
	manualBtn, ok := tt.AccessByRole(ui.RoleButton, "指定")
	if !ok || !manualBtn.Disabled {
		t.Fatalf("locked 时「指定」应禁用: ok=%v node=%+v", ok, manualBtn)
	}
	if err := tt.Click("自动选号"); err != nil {
		t.Fatalf("点击禁用分段失败: %v", err)
	}
	if got := currentTestThreadSnapshot(s, th.ID, th).Account; got != "acc-2" {
		t.Fatalf("locked 时点击不应修改线程 Account，实际=%q", got)
	}
	if calls := fake.SetThreadAccountCalls(); len(calls) != 0 {
		t.Fatalf("locked 时点击不应发起 SetThreadAccount，calls=%+v", calls)
	}
}

func TestComposerQuotaRedesignRouteSegmentedTransitionSettles(t *testing.T) {
	t.Run("正常动效结束后不再请求帧", func(t *testing.T) {
		setAppReduceMotion(t, false)
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
		_, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))

		tt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c).Width(quotaPopoverWidth).Children(func() {
				renderComposerQuotaPopover(c, s, currentTestThreadSnapshot(s, th.ID, th), "agy", 0)
			})
		}, 480, 480)
		tt.Frame()

		if err := tt.Click("指定"); err != nil {
			t.Fatalf("点击「指定」失败: %v", err)
		}
		tt.Advance(DurSnap + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("分段选中过渡结束后不应继续请求下一帧")
		}
	})

	t.Run("ReduceMotion开启时立即到终点", func(t *testing.T) {
		setAppReduceMotion(t, true)
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
		_, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))

		tt := ui.NewTesterWith(func(c *ui.Context) {
			ui.Box(c).Width(quotaPopoverWidth).Children(func() {
				renderComposerQuotaPopover(c, s, currentTestThreadSnapshot(s, th.ID, th), "agy", 0)
			})
		}, 480, 480, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt.Frame()

		if err := tt.Click("指定"); err != nil {
			t.Fatalf("点击「指定」失败: %v", err)
		}
		if tt.FramePending() {
			t.Fatal("ReduceMotion 开启时切换分段不应请求后续帧")
		}
	})
}
