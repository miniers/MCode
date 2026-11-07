package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}
	SetChatQuotaWindowsForProvider(s, th.Provider, quotaPopoverWindows(8))
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	const width, height = 480, 360
	tt := quotaPopoverTester(s, th, width, height)
	if err := tt.Click("额度 " + th.Provider); err != nil {
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
	t.Setenv("HOME", t.TempDir())
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
	if err := tt.Click("额度 " + th.Provider); err != nil {
		t.Fatalf("打开额度弹层失败: %v", err)
	}
	if err := tt.Click("刷新"); err != nil {
		t.Fatalf("点击刷新失败: %v", err)
	}

	// 刷新进行中：按钮显示「刷新中…」，重复点击不会再发一次请求。
	waitFor(t, tt, func() bool { return tt.HasText("刷新中…") })
	// 界面在 goroutine 调用 fake 之前就已切成「刷新中…」，这里要等 fake 真正收到请求，
	// 否则下面的长度断言会受 goroutine 调度时机影响而偶发失败。
	waitFor(t, tt, func() bool { return len(fake.RefreshProviderQuotaCalls()) >= 1 })
	if err := tt.Click("刷新中…"); err != nil {
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
	t.Setenv("HOME", t.TempDir())
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

func TestQuotaGroupTitleVisible(t *testing.T) {
	if quotaGroupTitleVisible(1, quotaGroupListOptions{HideSingleGroupTitle: true}) ||
		!quotaGroupTitleVisible(2, quotaGroupListOptions{HideSingleGroupTitle: true}) ||
		!quotaGroupTitleVisible(1, quotaGroupListOptions{}) {
		t.Fatal("供应商标题显示条件错误")
	}
}
