package mcodeui

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestComposerQuotaPopoverHorizontalPadding(t *testing.T) {
	src, err := os.ReadFile("composer_usage_panel.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ui.Column(c.Key(\"composer-quota-popover-panel\")).\n\t\tLabel(\"额度详情\").\n\t\tFillWidth().\n\t\tPadding(Space4, 0)",
		"ui.Row(c.Key(\"composer-quota-popover-header\")).\n\t\t\tFillWidth().\n\t\t\tPadding(0, Space4)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("额度浮层水平内边距约定缺失: %s", want)
		}
	}
}

// 场景 1：formatTokenCount 紧凑格式化各量级 token 数量。
func TestFormatTokenCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{950, "950"},
		{1000, "1k"},
		{1500, "1.5k"},
		{76000, "76k"},
		{76500, "76.5k"},
		{200000, "200k"},
		{1200000, "1.2M"},
		{2000000, "2M"},
		{1000000000, "1B"},
		{1500000000, "1.5B"},
		{-950, "-950"},
		{-76000, "-76k"},
	}

	for _, tc := range tests {
		got := formatTokenCount(tc.input)
		if got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// 场景 2：formatQuotaResetCountdown 格式化各种到期区间的倒计时文案。
func TestFormatQuotaResetCountdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)

	// 零值
	if got := formatQuotaResetCountdown(now, time.Time{}); got != "" {
		t.Errorf("zero time want empty, got %q", got)
	}

	// 已过期 / 即将到期
	if got := formatQuotaResetCountdown(now, now.Add(-10*time.Second)); got != "即将重置" {
		t.Errorf("past time want '即将重置', got %q", got)
	}

	// 2 小时 30 分
	if got := formatQuotaResetCountdown(now, now.Add(2*time.Hour+30*time.Minute)); got != "2 时 30 分后重置" {
		t.Errorf("2h30m want '2 时 30 分后重置', got %q", got)
	}

	// 4 小时 37 分
	if got := formatQuotaResetCountdown(now, now.Add(4*time.Hour+37*time.Minute)); got != "4 时 37 分后重置" {
		t.Errorf("4h37m want '4 时 37 分后重置', got %q", got)
	}

	// 2 小时整
	if got := formatQuotaResetCountdown(now, now.Add(2*time.Hour)); got != "2 小时后重置" {
		t.Errorf("2h want '2 小时后重置', got %q", got)
	}

	// 45 分钟
	if got := formatQuotaResetCountdown(now, now.Add(45*time.Minute)); got != "45 分后重置" {
		t.Errorf("45m want '45 分后重置', got %q", got)
	}

	// 不足 1 分钟取 1 分
	if got := formatQuotaResetCountdown(now, now.Add(20*time.Second)); got != "1 分后重置" {
		t.Errorf("20s want '1 分后重置', got %q", got)
	}

	// 2 天后
	got2d := formatQuotaResetCountdown(now, now.Add(48*time.Hour))
	if !strings.Contains(got2d, "重置") {
		t.Errorf("2d want containing '重置', got %q", got2d)
	}

	// 10 天后
	got10d := formatQuotaResetCountdown(now, now.Add(240*time.Hour))
	if !strings.Contains(got10d, "重置") {
		t.Errorf("10d want containing '重置', got %q", got10d)
	}
}

// 弹窗行里的窗口名用周期短名，但认不出周期或只限某个模型的窗口保留原名。
func TestQuotaPopoverWindowName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		window ProviderQuotaWindow
		want   string
	}{
		{window: ProviderQuotaWindow{Label: "5-hour limit"}, want: "5h"},
		{window: ProviderQuotaWindow{Label: "Weekly · all models"}, want: "周"},
		{window: ProviderQuotaWindow{Label: "Weekly · Opus"}, want: "Weekly · Opus"},
		{window: ProviderQuotaWindow{Label: "Credits"}, want: "Credits"},
	}
	for _, tc := range tests {
		if got := quotaPopoverWindowName(tc.window); got != tc.want {
			t.Errorf("quotaPopoverWindowName(%q) = %q, want %q", tc.window.Label, got, tc.want)
		}
	}
}

// 场景 3：验证上下文用量比例、百分比计算与格式化组合。
func TestUsageContextCalculation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	used := int64(76000)
	limit := int64(200000)

	frac := contextUsageFraction(used, limit)
	if math.Abs(frac-0.38) > 0.001 {
		t.Fatalf("expected frac ~0.38, got %v", frac)
	}

	pct := int(math.Round(contextUsagePercent(used, limit)))
	if pct != 38 {
		t.Fatalf("expected pct 38, got %d", pct)
	}

	got := fmt.Sprintf("%s / %s (%d%%)", formatTokenCount(used), formatTokenCount(limit), pct)
	if got != "76k / 200k (38%)" {
		t.Fatalf("expected '76k / 200k (38%%)', got %q", got)
	}
}

// 场景 4：模型目录里没有该模型的上限时，renderUsagePanel 不再用固定默认预算顶分母，
// 直接显示“暂无上下文窗口数据”；没有额度数据时省略额度分段。
func TestRenderUsagePanel_UnknownContextLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	s := newChatState(fake)
	th := &Thread{ID: "t1", Provider: "anthropic"}

	SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)

	if !tt.HasText("上下文窗口") {
		t.Fatalf("expected header '上下文窗口'")
	}
	if !tt.HasText("暂无上下文窗口数据") {
		t.Fatalf("unknown context limit must be reported as missing data, got %q", tt.Texts())
	}
	if tt.HasText("0 / 200k (0%)") {
		t.Fatalf("unknown context limit must not fall back to the default budget")
	}
	if tt.HasText("额度") {
		t.Fatalf("expected quota section to be omitted when none")
	}
}

// 上下文用量取最新一轮的输入 + 输出；会话累计计费仍在下方“输入/输出/缓存”里单独展示。
func TestRenderUsagePanel_UsesLatestTurnForContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.providers = []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 200_000}},
	}}
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 1200, Output: 80}},
		Total: TokenUsage{Input: 1200, Output: 80},
	}
	SetChatQuotaWindowsForProvider(s, "claude", nil)
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, &Thread{ID: "t1", Provider: "claude"})
	}, 400, 400)
	if !tt.HasText("1.3k / 200k (1%)") {
		t.Fatalf("context usage must come from the latest turn, got %q", tt.Texts())
	}
	if !tt.HasText("输入 1.2k · 输出 80 · 缓存命中 0") {
		t.Fatal("recorded token usage must stay visible")
	}
}

// agy 一轮里的输入是整轮多步累计（可远超窗口上限），面板占用要取单次请求的 ContextTokens。
func TestRenderUsagePanel_PrefersSingleRequestContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.providers = []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 1_000_000}},
	}}
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 1_900_000, Output: 948_000, Cached: 1_500_000, ContextTokens: 190_000}},
		Total: TokenUsage{Input: 9_100_000, Output: 948_000},
	}
	SetChatQuotaWindowsForProvider(s, "claude", nil)
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, &Thread{ID: "t1", Provider: "claude"})
	}, 400, 400)
	if !tt.HasText("190k / 1M (19%)") {
		t.Fatalf("context usage must come from ContextTokens, got %q", tt.Texts())
	}
}

// 场景 5：renderUsagePanel 存在配额数据时渲染额度信息与倒计时。
func TestRenderUsagePanel_WithQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	s := newChatState(fake)
	th := &Thread{ID: "t1", Provider: "anthropic"}

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	windows := []ProviderQuotaWindow{
		{
			Label:       "Claude 3.7 限额",
			UsedPercent: 62.0,
			ResetsAt:    now.Add(2*time.Hour + 30*time.Minute),
		},
	}
	SetChatQuotaWindowsForProvider(s, th.Provider, windows)
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)

	if !tt.HasText("上下文窗口") {
		t.Fatalf("expected header '上下文窗口'")
	}
	if !tt.HasText("额度") {
		t.Fatalf("expected section '额度'")
	}
	if !tt.HasText("Claude 3.7 限额") {
		t.Fatalf("expected window label 'Claude 3.7 限额'")
	}
	if !tt.HasText("已用 62%") {
		t.Fatal("expected '已用 62%'：额度百分比默认显示已用")
	}
	if !tt.HasText("2 时 30 分后重置") {
		t.Fatalf("expected countdown '2 时 30 分后重置'")
	}
}

// 弹窗内百分比默认显示已用额度，点一下变「剩余」，再点回「已用」；所有窗口共用一个开关。
func TestRenderUsagePanel_QuotaPercentToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "anthropic"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{{Label: "5 小时限额", UsedPercent: 62}})
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	render := func() *ui.Tester {
		return ui.NewTester(func(c *ui.Context) {
			renderUsagePanel(c, s, th)
		}, 400, 400)
	}

	tt := render()
	if !tt.HasText("已用 62%") || tt.HasText("剩余 38%") {
		t.Fatalf("quota percent must default to used, texts: %q", tt.Texts())
	}
	if err := tt.Click("已用 62%"); err != nil {
		t.Fatalf("click percent failed: %v", err)
	}
	if !s.composerView.quotaShowRemaining {
		t.Fatal("click must turn quotaShowRemaining on")
	}
	tt = render()
	if !tt.HasText("剩余 38%") || tt.HasText("已用 62%") {
		t.Fatalf("quota percent must show remaining after toggle, texts: %q", tt.Texts())
	}
	if err := tt.Click("剩余 38%"); err != nil {
		t.Fatalf("click percent again failed: %v", err)
	}
	if s.composerView.quotaShowRemaining {
		t.Fatal("second click must turn quotaShowRemaining back off")
	}
}

// 场景 7：会话 provider 有余额缓存时用量面板展示余额行；余额不足时金额用错误色并标注「余额不足」，
// 没有余额数据（不支持或未加载）时整行省略。
func TestRenderUsagePanel_Credits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	th := &Thread{ID: "t1", Provider: "deepseek"}

	healthy := ProviderCredits{Provider: "deepseek", Total: 12.5, Currency: "USD", AmountText: "$12.50", Usable: true}
	s := newChatState(NewFakeClient())
	SetChatCreditsForProvider(s, th.Provider, healthy)

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if !tt.HasText("余额") || !tt.HasText("$12.50") {
		t.Fatalf("expected balance row with amount, got %v", tt.Texts())
	}
	if tt.HasText("余额不足") {
		t.Fatal("healthy credits must not be marked as depleted")
	}

	depleted := ProviderCredits{Provider: "deepseek", Total: 0, Currency: "USD", AmountText: "$0.00", Usable: true}
	s2 := newChatState(NewFakeClient())
	SetChatCreditsForProvider(s2, th.Provider, depleted)
	var colorOK bool
	tt2 := ui.NewTester(func(c *ui.Context) {
		colorOK = creditsAmountColor(c, depleted) == ThemeDanger(c) &&
			creditsAmountColor(c, healthy) == ThemePrimaryText(c)
		renderUsagePanel(c, s2, th)
	}, 400, 400)
	if !tt2.HasText("$0.00") || !tt2.HasText("余额不足") {
		t.Fatalf("depleted credits must render amount and '余额不足', got %v", tt2.Texts())
	}
	if !colorOK {
		t.Fatal("depleted credits must use the error color")
	}

	s3 := newChatState(NewFakeClient())
	tt3 := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s3, th)
	}, 400, 400)
	if tt3.HasText("余额") {
		t.Fatalf("no credits data must omit the balance row, got %v", tt3.Texts())
	}
}

// threadQuotaProvider 决定额度 key：pi 与 mcode 线程按模型第一段折算成 magpie 端点 key，
// magpie 与 magpie2…magpie4 端点会话本身就以 provider 命名。
func TestThreadQuotaProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name string
		th   *Thread
		want string
	}{
		{name: "nil 会话", th: nil, want: ""},
		{name: "非 pi provider", th: &Thread{Provider: "agy", Model: "gpt-5"}, want: "agy"},
		{name: "pi 无斜杠模型", th: &Thread{Provider: "pi", Model: "default"}, want: "pi"},
		{name: "pi 空模型", th: &Thread{Provider: "pi"}, want: "pi"},
		{
			name: "pi 带斜杠模型",
			th:   &Thread{Provider: "pi", Model: "magpie-jp/auto/deepseek-v4.1-flash"},
			want: "pi:magpie-jp",
		},
		{
			name: "pi 单层模型",
			th:   &Thread{Provider: "pi", Model: "magpie-us/auto"},
			want: "pi:magpie-us",
		},
		{name: "mcode 无斜杠模型", th: &Thread{Provider: "mcode", Model: "default"}, want: "mcode"},
		{name: "mcode 空模型", th: &Thread{Provider: "mcode"}, want: "mcode"},
		{
			name: "mcode 带斜杠模型",
			th:   &Thread{Provider: "mcode", Model: "magpie-jp/auto/deepseek-v4.1-flash"},
			want: "mcode:magpie-jp",
		},
		{name: "magpie 端点 provider", th: &Thread{Provider: "magpie", Model: "gpt-5"}, want: "magpie"},
		{name: "magpie2 端点 provider", th: &Thread{Provider: "magpie2", Model: "claude"}, want: "magpie2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := threadQuotaProvider(tt.th); got != tt.want {
				t.Fatalf("threadQuotaProvider(%+v) = %q, want %q", tt.th, got, tt.want)
			}
		})
	}
}

// TestQuotaAccountsSameProvider 校验同一 provider 的两个账号（账户名带「 · 标识」，
// 如 magpie 里登了两个 Claude 账号）分组时不会被合并；没有 Active/Last 线索时额度条
// 不挑账户，不按返回顺序取第一组。
func TestQuotaAccountsSameProvider(t *testing.T) {
	windows := []ProviderQuotaWindow{
		{ID: "claude · a@example.com/5 hours", Account: "claude · a@example.com", Label: "5 hours", UsedPercent: 10},
		{ID: "claude · a@example.com/7 days", Account: "claude · a@example.com", Label: "7 days", UsedPercent: 20},
		{ID: "claude · b@example.com/5 hours", Account: "claude · b@example.com", Label: "5 hours", UsedPercent: 30},
		{ID: "claude · b@example.com/7 days", Account: "claude · b@example.com", Label: "7 days", UsedPercent: 40},
	}

	groups := groupQuotaWindowsByAccount(windows)
	if len(groups) != 2 {
		t.Fatalf("分组数 = %d，想要 2：%+v", len(groups), groups)
	}
	for i, want := range []string{"claude · a@example.com", "claude · b@example.com"} {
		if groups[i].account != want {
			t.Errorf("第 %d 组账户 = %q，想要 %q", i, groups[i].account, want)
		}
	}
	// 账户名不属于窗口名，去掉前缀后仍是窗口名本身。
	if groups[0].windows[0].Label != "5 hours" || groups[1].windows[0].Label != "5 hours" {
		t.Errorf("窗口名 = %q / %q，想要 5 hours", groups[0].windows[0].Label, groups[1].windows[0].Label)
	}

	// 没有 Active 标记时不能凭返回顺序认定在用账号，也不再有模型名到账户的猜测。
	if got := (&chatState{}).quotaInUseAccount("pi:magpie-jp", windows); got != "" {
		t.Errorf("没有 Active 时不应判定在用账号，得到 %q", got)
	}
	if got := quotaRecentAccount(windows); got != "" {
		t.Errorf("没有 Last/LastServedAt 时不应判定最近使用，得到 %q", got)
	}
	if got := quotaAccountWindows("", windows); got != nil {
		t.Errorf("账户为空时应返回 nil，得到 %+v", got)
	}
}

// TestQuotaInUseAccountSkipsDisabled 校验已停用（Disabled）的账号不参与在用账号判定：
// 有 Active 账号时用它，全部停用时不判在用，标题也不显示「在用」。
func TestQuotaInUseAccountSkipsDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	windows := []ProviderQuotaWindow{
		{ID: "gemini", Period: "5h", Label: "系统账号 · Gemini Models", Account: "系统账号", UsedPercent: 40, Disabled: true},
		{ID: "gemini-7d", Period: "7d", Label: "a@example.com · Gemini Models 7d", Account: "a@example.com", UsedPercent: 10, Active: true},
	}
	client := NewFakeClient()
	client.providerQuotaResult = windows
	s := newChatState(client)
	th := &Thread{ID: "t1", Provider: "agy"}
	SetChatQuotaWindowsForProvider(s, th.Provider, windows)
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	if got := s.quotaInUseAccount(th.Provider, windows); got != "a@example.com" {
		t.Fatalf("在用账号 = %q，想要 a@example.com", got)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if !tt.HasText("系统账号 · 已停止") {
		t.Fatalf("停用账号标题应标『已停止』，texts: %q", tt.Texts())
	}
	if !tt.HasText("a@example.com · 在用") {
		t.Fatalf("在用账号标题应标『在用』，texts: %q", tt.Texts())
	}

	// 全部账号停用：不判定在用账号，标题只有「已停止」。
	allDisabled := []ProviderQuotaWindow{
		{ID: "gemini", Period: "5h", Label: "系统账号 · Gemini Models", Account: "系统账号", UsedPercent: 40, Disabled: true},
	}
	s2 := newChatState(NewFakeClient())
	if got := s2.quotaInUseAccount("agy", allDisabled); got != "" {
		t.Fatalf("全部停用时不应判定在用账号，得到 %q", got)
	}
	if groups := groupQuotaWindowsByAccount(allDisabled); len(groups) != 1 || !groups[0].disabled || groups[0].active {
		t.Fatalf("分组应带上停用标记且不算在用：%+v", groups)
	}
}

// 额度请求的 ctx 携带当前会话 ID，后端据此把「在用」标到该会话绑定的账号上。
func TestWithQuotaThreadRoundTrip(t *testing.T) {
	if got := QuotaThread(context.Background()); got != "" {
		t.Fatalf("未注入时应为空串，得到 %q", got)
	}
	ctx := WithQuotaThread(context.Background(), "thread-1")
	if got := QuotaThread(ctx); got != "thread-1" {
		t.Fatalf("会话 ID 未正确往返，得到 %q", got)
	}
	// 空 ID 不注入：后端拿空串只能走路由目标兜底，不如直接不写值。
	if got := QuotaThread(WithQuotaThread(context.Background(), "")); got != "" {
		t.Fatalf("空会话 ID 不应注入，得到 %q", got)
	}
	var nilCtx context.Context
	if got := QuotaThread(nilCtx); got != "" {
		t.Fatalf("nil ctx 应为空串，得到 %q", got)
	}
	if WithQuotaThread(nilCtx, "thread-1") != nil {
		t.Fatal("nil ctx 应原样返回")
	}
}

// TestQuotaMagpieAccountAttribution 校验 Magpie 卡片只在后端给出唯一 Active 账户组时认「在用」：
// 多个 Active 组通常来自旧后端的 provider 级标记，视为不可信；没有 Active 时用 Last/LastServedAt
// 判定「最近使用」，都没有时返回空串，不按返回顺序取第一组，也不按模型名猜。
func TestQuotaMagpieAccountAttribution(t *testing.T) {
	readAt := time.Now().Add(-3 * time.Minute)
	a := "a@b.com"
	b := "{{EMAIL_d949a3a8}}"
	window := func(account string, used float64) ProviderQuotaWindow {
		return ProviderQuotaWindow{
			ID: account + "/5 hours", Period: "5h", Label: "5 hours", Account: account,
			UsedPercent: used, Source: "magpie", Plan: "Plus", ReadAt: readAt,
		}
	}

	t.Run("恰好一个 Active 组", func(t *testing.T) {
		windows := []ProviderQuotaWindow{window(a, 85), window(b, 0)}
		windows[0].Active = true
		if got := (&chatState{}).quotaInUseAccount("codex", windows); got != a {
			t.Fatalf("在用账号 = %q，想要 %q", got, a)
		}
	})

	t.Run("多个 Active 组视为未知", func(t *testing.T) {
		windows := []ProviderQuotaWindow{window(a, 85), window(b, 0)}
		windows[0].Active = true
		windows[1].Active = true
		if got := (&chatState{}).quotaInUseAccount("codex", windows); got != "" {
			t.Fatalf("多个 Active 组不应判定在用账号，得到 %q", got)
		}
	})

	t.Run("没有 Active 时不回退第一组，取 Last 标最近使用", func(t *testing.T) {
		windows := []ProviderQuotaWindow{window(a, 85), window(b, 0)}
		windows[1].Last = true
		windows[1].LastServedAt = readAt
		if got := (&chatState{}).quotaInUseAccount("codex", windows); got != "" {
			t.Fatalf("没有 Active 不应判定在用账号，得到 %q", got)
		}
		if got := quotaRecentAccount(windows); got != b {
			t.Fatalf("最近使用账号 = %q，想要 %q", got, b)
		}
	})

	t.Run("没有 Last 时按 LastServedAt 取最新", func(t *testing.T) {
		windows := []ProviderQuotaWindow{window(a, 85), window(b, 0)}
		windows[0].LastServedAt = readAt.Add(-time.Minute)
		windows[1].LastServedAt = readAt
		if got := quotaRecentAccount(windows); got != b {
			t.Fatalf("最近使用账号 = %q，想要 %q", got, b)
		}
	})

	t.Run("没有 Active 也没有最近应答时未知", func(t *testing.T) {
		windows := []ProviderQuotaWindow{window(a, 85), window(b, 0)}
		if got := (&chatState{}).quotaInUseAccount("codex", windows); got != "" {
			t.Fatalf("在用账号应为空，得到 %q", got)
		}
		if got := quotaRecentAccount(windows); got != "" {
			t.Fatalf("最近使用账号应为空，得到 %q", got)
		}
	})
}

// TestQuotaMagpieAccountMarkers 校验弹层标记：唯一 Active 组标「在用」，没有 Active 时
// Last 组标「最近使用」；两者都没有时顶部提示账号未知，且保留卡片套餐与读取时间。
func TestQuotaMagpieAccountMarkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	readAt := time.Now().Add(-3 * time.Minute)
	a := "a@b.com"
	b := "{{EMAIL_d949a3a8}}"
	base := []ProviderQuotaWindow{
		{ID: a + "/5 hours", Period: "5h", Label: "5 hours", Account: a, UsedPercent: 85,
			Source: "magpie", Plan: "Plus", ReadAt: readAt},
		{ID: b + "/30 days", Period: "30d", Label: "30 days", Account: b, UsedPercent: 0,
			Source: "magpie", Plan: "Free"},
	}

	render := func(t *testing.T, windows []ProviderQuotaWindow) *ui.Tester {
		t.Helper()
		client := NewFakeClient()
		client.providerQuotaResult = windows
		s := newChatState(client)
		th := &Thread{ID: "t1", Provider: "codex"}
		SetChatQuotaWindowsForProvider(s, th.Provider, windows)
		t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })
		return ui.NewTester(func(c *ui.Context) {
			renderUsagePanel(c, s, th)
		}, 420, 640)
	}

	t.Run("唯一 Active 标在用", func(t *testing.T) {
		windows := append([]ProviderQuotaWindow{}, base...)
		windows[0].Active = true
		tt := render(t, windows)
		if !tt.HasText(a+" · Plus") || !tt.HasText("· 在用") {
			t.Fatalf("在用账号标题应带套餐与「在用」，texts: %q", tt.Texts())
		}
		if tt.HasText("本会话账号未知") || tt.HasText("· 最近使用") {
			t.Fatalf("有在用账号时不应提示未知或最近使用，texts: %q", tt.Texts())
		}
	})

	t.Run("Last 标最近使用", func(t *testing.T) {
		windows := append([]ProviderQuotaWindow{}, base...)
		windows[1].Last = true
		windows[1].LastServedAt = readAt
		tt := render(t, windows)
		if tt.HasText("· 在用") {
			t.Fatalf("没有 Active 时不应标在用，texts: %q", tt.Texts())
		}
		if !tt.HasText(b+" · Free") || !tt.HasText("· 最近使用") {
			t.Fatalf("最近使用的账号应带标记，texts: %q", tt.Texts())
		}
	})

	t.Run("没有任何线索时提示账号未知", func(t *testing.T) {
		tt := render(t, append([]ProviderQuotaWindow{}, base...))
		if !tt.HasText("本会话账号未知，以下为全部账号") {
			t.Fatalf("账号未知时应在顶部提示，texts: %q", tt.Texts())
		}
		if tt.HasText("· 在用") || tt.HasText("· 最近使用") {
			t.Fatalf("未知时不应标在用或最近使用，texts: %q", tt.Texts())
		}
	})
}

// TestQuotaSoleAccountReadouts 校验只有一个未停用账户组时的额度展示：没有账号名的窗口组
// （如 workbuddy 的 Credits）与只有一张 Magpie 卡片的单账号 provider 都直接展示读数，
// 不再因为找不到在用账号而显示额度未知；多账号且没有 Active/Last 时仍然未知。
func TestQuotaSoleAccountReadouts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	readAt := time.Now().Add(-3 * time.Minute)
	email := "a@x.com"

	renderStrip := func(t *testing.T, provider string, windows []ProviderQuotaWindow) (*chatState, *ui.Tester) {
		t.Helper()
		client := NewFakeClient()
		client.providerQuotaResult = windows
		s := newChatState(client)
		th := &Thread{ID: "t1", Provider: provider}
		SetChatQuotaWindowsForProvider(s, provider, windows)
		t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, provider, nil) })
		tt := ui.NewTester(func(c *ui.Context) {
			renderComposerQuotaStrip(c, s, th)
		}, 420, 400)
		return s, tt
	}

	t.Run("没有账号名的窗口组直接显示读数", func(t *testing.T) {
		windows := []ProviderQuotaWindow{
			{ID: "credits", Label: "Credits", UsedPercent: 40, Source: "magpie"},
			{ID: "extra-credits", Label: "Extra credits", UsedPercent: 60, Source: "magpie"},
		}
		// 空账号要能取到无名组，否则额度条拿到窗口后仍会渲染成未知。
		if got := quotaAccountWindows("", windows); len(got) != 2 {
			t.Fatalf("空账号应取到 2 个无名窗口，得到 %+v", got)
		}
		s, tt := renderStrip(t, "workbuddy", windows)
		if tt.HasText("额度未知") {
			t.Fatalf("单组无名窗口不应显示额度未知，texts: %q", tt.Texts())
		}
		if !tt.HasText("40%") || !tt.HasText("60%") {
			t.Fatalf("应显示无名窗口的读数，texts: %q", tt.Texts())
		}
		// 弹层同样视为在用：不提示未知，窗口名照常列出。
		if err := tt.Click("额度 workbuddy"); err != nil {
			t.Fatalf("点击额度组失败：%v", err)
		}
		if _, ok := tt.Find("额度详情"); !ok || !tt.HasText("额度") || !tt.HasText("40%") || !tt.HasText("60%") {
			t.Fatalf("弹层应列出无名窗口的额度读数，texts: %q", tt.Texts())
		}
		if tt.HasText("本会话账号未知") || tt.HasText("· 最近使用") {
			t.Fatalf("单组无名窗口不应提示账号未知或最近使用，texts: %q", tt.Texts())
		}
		// 悬停提示无法从渲染树取到 Tooltip，直接校验拼接函数：
		// 无名分组有窗口时只写「额度」，不写账号未知。
		plans := quotaStripPlans(s, []quotaStripGroup{{provider: "workbuddy"}})
		if len(plans) != 1 {
			t.Fatalf("应得到 1 个无名单组计划：%+v", plans)
		}
		if got := quotaStripGroupTooltip("workbuddy", plans[0]); got != "workbuddy 额度" {
			t.Fatalf("无名单组悬停提示 = %q, want %q", got, "workbuddy 额度")
		}
	})

	t.Run("单个具名账号没有 Active 也视为在用", func(t *testing.T) {
		windows := []ProviderQuotaWindow{
			{ID: email + "/5 hours", Period: "5h", Label: "5 hours", Account: email,
				UsedPercent: 85, Source: "magpie"},
		}
		s, tt := renderStrip(t, "codex", windows)
		if !tt.HasText("5h") || !tt.HasText("85%") {
			t.Fatalf("单账号应显示读数，texts: %q", tt.Texts())
		}
		// 悬停提示只在 recent 为真时追加「最近使用」，这里直接校验计划字段。
		plans := quotaStripPlans(s, []quotaStripGroup{{provider: "codex"}})
		if len(plans) != 1 || plans[0].account != email || plans[0].recent || len(plans[0].windows) != 1 {
			t.Fatalf("单账号应直接当作用在账号且不标最近使用：%+v", plans)
		}
		if err := tt.Click("额度 codex"); err != nil {
			t.Fatalf("点击额度组失败：%v", err)
		}
		if _, ok := tt.Find("额度详情"); !ok || !tt.HasText("85%") {
			t.Fatalf("弹层应显示单账号读数，texts: %q", tt.Texts())
		}
		if tt.HasText("本会话账号未知") || tt.HasText("· 最近使用") {
			t.Fatalf("单账号不应提示账号未知或最近使用，texts: %q", tt.Texts())
		}
		// 账号未知时不能把唯一具名账号的窗口借给空串。
		if got := quotaAccountWindows("", windows); got != nil {
			t.Fatalf("空串不应命中具名账号组，得到 %+v", got)
		}
	})

	t.Run("两个账号都没有线索时仍然未知", func(t *testing.T) {
		other := "{{EMAIL_d949a3a8}}"
		windows := []ProviderQuotaWindow{
			{ID: email + "/5 hours", Period: "5h", Label: "5 hours", Account: email,
				UsedPercent: 85, Source: "magpie", Plan: "Plus", ReadAt: readAt},
			{ID: other + "/30 days", Period: "30d", Label: "30 days", Account: other,
				UsedPercent: 0, Source: "magpie", Plan: "Free"},
		}
		s, tt := renderStrip(t, "codex", windows)
		plans := quotaStripPlans(s, []quotaStripGroup{{provider: "codex"}})
		if len(plans) != 1 || plans[0].account != "" || plans[0].recent || len(plans[0].windows) != 0 {
			t.Fatalf("多账号无线索时不应挑账号：%+v", plans)
		}
		if !tt.HasText("额度未知") || tt.HasText("85%") {
			t.Fatalf("多账号无线索时应显示额度未知，texts: %q", tt.Texts())
		}
		if got := quotaStripGroupTooltip("codex", plans[0]); !strings.Contains(got, "本会话账号未知") {
			t.Fatalf("多账号无线索时悬停提示应写账号未知，得到 %q", got)
		}
	})
	t.Run("claude 两个账号都没有应答线索时仍然未知", func(t *testing.T) {
		windows := []ProviderQuotaWindow{
			{ID: "session", Period: "5h", Label: "5 hours", Account: "a@x",
				UsedPercent: 12, Source: "magpie", Provider: "claude", User: "a@x", Kind: "subscription"},
			{ID: "session", Period: "5h", Label: "5 hours", Account: "b@y",
				UsedPercent: 85, Source: "magpie", Provider: "claude", User: "b@y", Kind: "subscription"},
		}
		_, tt := renderStrip(t, "claude", windows)
		if !tt.HasText("额度未知") || tt.HasText("85%") {
			t.Fatalf("多账号没有应答线索时应显示额度未知，texts: %q", tt.Texts())
		}
	})

	t.Run("claude 有最近应答线索时显示该账号读数", func(t *testing.T) {
		servedAt := time.Now().Add(-time.Minute)
		windows := []ProviderQuotaWindow{
			{ID: "session", Period: "5h", Label: "5 hours", Account: "a@x",
				UsedPercent: 12, Source: "magpie", Provider: "claude", User: "a@x", Kind: "subscription"},
			{ID: "session", Period: "5h", Label: "5 hours", Account: "b@y",
				UsedPercent: 85, Source: "magpie", Provider: "claude", User: "b@y", Kind: "subscription",
				Last: true, LastServedAt: servedAt},
		}
		_, tt := renderStrip(t, "claude", windows)
		if !tt.HasText("85%") || tt.HasText("额度未知") {
			t.Fatalf("有最近应答线索时应显示该账号读数，texts: %q", tt.Texts())
		}
	})
}

// 该轮上报了费用时，用量面板在累计 token 行下方多显示一行“本轮费用”，金额保留 4 位小数。
func TestRenderUsagePanel_TurnCost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.providers = []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 200_000}},
	}}
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 1200, Output: 80, ContextTokens: 1200, CostUSD: 0.1234}},
		Total: TokenUsage{Input: 1200, Output: 80},
	}
	SetChatQuotaWindowsForProvider(s, "claude", nil)
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, &Thread{ID: "t1", Provider: "claude"})
	}, 400, 400)
	if !tt.HasText("本轮费用 $0.1234") {
		t.Fatalf("expected turn cost line, got %q", tt.Texts())
	}
	if !tt.HasText("输入 1.2k · 输出 80 · 缓存命中 0") {
		t.Fatal("token usage line must stay visible next to the cost line")
	}
}

// provider 未上报费用（CostUSD 为 0）时整行不出现，面板与改造前一致。
func TestRenderUsagePanel_TurnCostHiddenWhenZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.providers = []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 200_000}},
	}}
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 1200, Output: 80, ContextTokens: 1200}},
		Total: TokenUsage{Input: 1200, Output: 80},
	}
	SetChatQuotaWindowsForProvider(s, "claude", nil)
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, &Thread{ID: "t1", Provider: "claude"})
	}, 400, 400)
	if tt.HasText("本轮费用") {
		t.Fatalf("cost line must be omitted when CostUSD is 0, got %q", tt.Texts())
	}
	if !tt.HasText("输入 1.2k · 输出 80 · 缓存命中 0") {
		t.Fatal("token usage line must stay visible")
	}
}

// 上下文窗口上限来自后端上报的 ContextWindow，优先于模型目录里的静态值。
func TestRenderUsagePanel_PrefersReportedContextWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.providers = []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 200_000}},
	}}
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 1200, Output: 80, ContextTokens: 1200, ContextWindow: 1_000_000}},
		Total: TokenUsage{Input: 1200, Output: 80},
	}
	SetChatQuotaWindowsForProvider(s, "claude", nil)
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, &Thread{ID: "t1", Provider: "claude"})
	}, 400, 400)
	if !tt.HasText("1.2k / 1M (0%)") {
		t.Fatalf("context limit must come from reported ContextWindow, got %q", tt.Texts())
	}
}

// 额度弹窗的大进度条在可推断周期且有重置时间时显示时间进度竖线；没有重置时间时不显示。
func TestRenderUsagePanel_QuotaPaceMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "anthropic"}
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 小时限额", UsedPercent: 62, ResetsAt: now.Add(2 * time.Hour)},
	})
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if _, ok := tt.Find("quota-pace-marker"); !ok {
		t.Fatalf("expected pace marker in quota bar, texts: %q", tt.Texts())
	}

	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 小时限额", UsedPercent: 62},
	})
	tt = ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if _, ok := tt.Find("quota-pace-marker"); ok {
		t.Fatalf("must not render pace marker without reset time, texts: %q", tt.Texts())
	}
}

// quotaGroupSharedCountdown 只在组内所有带重置时间的窗口文案都相同时返回该文案：
// 文案不同或有窗口根本没有重置时间时不能把倒计时提到组标题行。
func TestQuotaGroupSharedCountdown(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)

	tests := []struct {
		name    string
		windows []ProviderQuotaWindow
		want    string
	}{
		{
			name: "全部相同返回文案",
			windows: []ProviderQuotaWindow{
				{ResetsAt: now.Add(2 * time.Hour)},
				{ResetsAt: now.Add(2 * time.Hour)},
			},
			want: "2 小时后重置",
		},
		{
			name: "文案不同返回空串",
			windows: []ProviderQuotaWindow{
				{ResetsAt: now.Add(2 * time.Hour)},
				{ResetsAt: now.Add(45 * time.Minute)},
			},
			want: "",
		},
		{
			name: "无重置时间的窗口不参与比较",
			windows: []ProviderQuotaWindow{
				{ResetsAt: now.Add(2 * time.Hour)},
				{},
				{ResetsAt: now.Add(2 * time.Hour)},
			},
			want: "2 小时后重置",
		},
		{
			name:    "没有窗口带重置时间返回空串",
			windows: []ProviderQuotaWindow{{}, {}},
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaGroupSharedCountdown(tc.windows, now); got != tc.want {
				t.Fatalf("quotaGroupSharedCountdown(%+v) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// 同一账号组内两个窗口的重置时间一致时，倒计时只在组标题行显示一次，窗口行不再重复。
func TestRenderUsagePanel_QuotaSharedCountdownOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "codex"}
	account := "a@example.com"
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{ID: account + "/5h", Label: "5 hours", Account: account, UsedPercent: 20, ResetsAt: now.Add(2 * time.Hour)},
		{ID: account + "/7d", Label: "7 days", Account: account, UsedPercent: 40, ResetsAt: now.Add(2 * time.Hour)},
	})
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if got := strings.Count(strings.Join(tt.Texts(), "\n"), "2 小时后重置"); got != 1 {
		t.Fatalf("共同倒计时应只出现一次，得到 %d 次：%q", got, tt.Texts())
	}
	if !tt.HasText("5h") || !tt.HasText("周") {
		t.Fatalf("两个窗口都要列出来（周期短名），texts: %q", tt.Texts())
	}
}

// 两个窗口重置时间不同时不能合并：两条倒计时都留在各自的窗口行里。
func TestRenderUsagePanel_QuotaDistinctCountdowns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "codex"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 小时限额", UsedPercent: 20, ResetsAt: now.Add(2 * time.Hour)},
		{Label: "7 天限额", UsedPercent: 40, ResetsAt: now.Add(45 * time.Minute)},
	})
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if !tt.HasText("2 小时后重置") || !tt.HasText("45 分后重置") {
		t.Fatalf("重置时间不同的窗口各自显示倒计时，texts: %q", tt.Texts())
	}
}

// 校验 Magpie 供应商 ID 转换为展示名称的规则。
func TestMagpieQuotaProviderName(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"claude", "Claude"},
		{"codex", "Codex"},
		{"cline", "Cline"},
		{"zai", "Z.ai"},
		{"droid", "Droid"},
		{"gemini", "Gemini"},
		{"workbuddy", "WorkBuddy"},
		{"commandcode", "Command Code"},
		{"commandcode-plan", "Command Code"},
		{"commandcode-custom", "Command Code"},
		{"deepseek", "Deepseek"},
		{"", ""},
	}
	for _, tc := range cases {
		got := magpieQuotaProviderName(tc.provider)
		if got != tc.want {
			t.Errorf("magpieQuotaProviderName(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

// 校验去掉账号名开头的 provider + " · " 前缀（大小写不敏感，去空保留原值）的规则。
func TestQuotaAccountDisplayName(t *testing.T) {
	cases := []struct {
		account  string
		provider string
		want     string
	}{
		{"claude · user@example.com", "claude", "user@example.com"},
		{"Claude · user@example.com", "claude", "user@example.com"},
		{"claude · user@example.com · pro", "claude", "user@example.com · pro"},
		{"claude · ", "claude", "claude · "}, // 去掉后为空时返回原值
		{"user@example.com", "claude", "user@example.com"},
		{"commandcode · team", "commandcode", "team"},
		{"codex · a@b.c", "claude", "codex · a@b.c"}, // provider 不匹配时保留原值
		{"", "claude", ""},
		{"user@example.com", "", "user@example.com"},
	}
	for _, tc := range cases {
		got := quotaAccountDisplayName(tc.account, tc.provider)
		if got != tc.want {
			t.Errorf("quotaAccountDisplayName(%q, %q) = %q, want %q", tc.account, tc.provider, got, tc.want)
		}
	}
}

// 校验按供应商分组与首次出现顺序保持的逻辑，非 Magpie 窗口不分组。
func TestGroupQuotaWindowsByProvider(t *testing.T) {
	// 非 Magpie 窗口返回 nil
	direct := []ProviderQuotaWindow{
		{Label: "win1", UsedPercent: 10},
		{Label: "win2", UsedPercent: 20},
	}
	if got := groupQuotaWindowsByProvider(direct); got != nil {
		t.Fatalf("非 Magpie 窗口应返回 nil，得到 %+v", got)
	}

	// Provider 为空返回 nil
	noProv := []ProviderQuotaWindow{
		{Label: "win1", Source: "magpie", Provider: ""},
	}
	if got := groupQuotaWindowsByProvider(noProv); got != nil {
		t.Fatalf("Provider 为空时应返回 nil，得到 %+v", got)
	}

	// Magpie 且 Provider 非空时按 Provider 首次出现顺序分组，组内顺序保持不变
	w1 := ProviderQuotaWindow{Label: "c1", Source: "magpie", Provider: "claude"}
	w2 := ProviderQuotaWindow{Label: "w1", Source: "magpie", Provider: "workbuddy"}
	w3 := ProviderQuotaWindow{Label: "c2", Source: "magpie", Provider: "claude"}
	w4 := ProviderQuotaWindow{Label: "cmd1", Source: "magpie", Provider: "commandcode-plan"}

	groups := groupQuotaWindowsByProvider([]ProviderQuotaWindow{w1, w2, w3, w4})
	if len(groups) != 3 {
		t.Fatalf("组数 = %d，想要 3", len(groups))
	}
	if groups[0].provider != "claude" || len(groups[0].windows) != 2 {
		t.Fatalf("第 1 组应为 claude 且含 2 个窗口，得到 %+v", groups[0])
	}
	if groups[0].windows[0].Label != "c1" || groups[0].windows[1].Label != "c2" {
		t.Fatalf("claude 组内窗口应保持原序，得到 %+v", groups[0].windows)
	}
	if groups[1].provider != "workbuddy" || len(groups[1].windows) != 1 {
		t.Fatalf("第 2 组应为 workbuddy，得到 %+v", groups[1])
	}
	if groups[2].provider != "commandcode-plan" || len(groups[2].windows) != 1 {
		t.Fatalf("第 3 组应为 commandcode-plan，得到 %+v", groups[2])
	}
}

// 校验 Magpie 多供应商窗口渲染后供应商组头（Claude、WorkBuddy）各出现一次，且账号名去掉 provider 前缀。
func TestRenderComposerQuotaSection_MagpieProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	fake := NewFakeClient()
	s := newChatState(fake)
	windows := []ProviderQuotaWindow{
		{ID: "c1", Label: "5 hours", Account: "claude · user1@example.com", Source: "magpie", Provider: "claude", UsedPercent: 20},
		{ID: "c2", Label: "5 hours", Account: "claude · user2@example.com", Source: "magpie", Provider: "claude", UsedPercent: 40},
		{ID: "w1", Label: "Credits", Account: "workbuddy · team@example.com", Source: "magpie", Provider: "workbuddy", UsedPercent: 60},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaSection(c, s, "pi:magpie-jp", windows)
	}, 400, 500)

	texts := tt.Texts()
	claudeCount := 0
	workbuddyCount := 0
	for _, text := range texts {
		if text == "Claude" {
			claudeCount++
		}
		if text == "WorkBuddy" {
			workbuddyCount++
		}
	}
	if claudeCount != 1 {
		t.Fatalf("期望供应商名 Claude 出现 1 次，实际 %d 次，texts: %q", claudeCount, texts)
	}
	if workbuddyCount != 1 {
		t.Fatalf("期望供应商名 WorkBuddy 出现 1 次，实际 %d 次，texts: %q", workbuddyCount, texts)
	}

	for _, text := range texts {
		if strings.HasPrefix(text, "claude · ") {
			t.Fatalf("账号标题不应以 'claude · ' 开头: %q", text)
		}
	}
	if !tt.HasText("user1@example.com") || !tt.HasText("user2@example.com") {
		t.Fatalf("应包含去掉 provider 前缀后的账号名，texts: %q", texts)
	}
}

// 校验在用账号所在的供应商组排在第一位（WorkBuddy 组头出现在 Claude 之前）。
func TestRenderComposerQuotaSection_ActiveProviderFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	fake := NewFakeClient()
	s := newChatState(fake)

	windows := []ProviderQuotaWindow{
		{ID: "c1", Label: "5 hours", Account: "claude · user1@example.com", Source: "magpie", Provider: "claude", UsedPercent: 20},
		{ID: "w1", Label: "Credits", Account: "workbuddy · team@example.com", Source: "magpie", Provider: "workbuddy", UsedPercent: 60, Active: true},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaSection(c, s, "pi:magpie-jp", windows)
	}, 400, 500)

	texts := tt.Texts()
	wbIdx, clIdx := -1, -1
	for i, txt := range texts {
		if txt == "WorkBuddy" && wbIdx == -1 {
			wbIdx = i
		}
		if txt == "Claude" && clIdx == -1 {
			clIdx = i
		}
	}
	if wbIdx == -1 || clIdx == -1 || wbIdx > clIdx {
		t.Fatalf("含 Active 账号的 WorkBuddy 组头应排在 Claude 之前，wbIdx=%d, clIdx=%d, texts: %q", wbIdx, clIdx, texts)
	}

	wbEl, wbOk := tt.Find("WorkBuddy")
	clEl, clOk := tt.Find("Claude")
	if wbOk && clOk && wbEl.Y > 0 && clEl.Y > 0 && wbEl.Y >= clEl.Y {
		t.Fatalf("WorkBuddy 组头 Y 坐标 (%v) 应在 Claude 组头 Y 坐标 (%v) 上方", wbEl.Y, clEl.Y)
	}
}

// 校验非 Magpie 窗口（Source 为空）不渲染供应商组头，账号标题与现状保持一致。
func TestRenderComposerQuotaSection_NonMagpieNoProviderHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	s := newChatState(fake)
	windows := []ProviderQuotaWindow{
		{ID: "c1", Label: "5 hours", Account: "a@example.com", UsedPercent: 20},
		{ID: "c2", Label: "7 days", Account: "a@example.com", UsedPercent: 40},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaSection(c, s, "codex", windows)
	}, 400, 400)

	if tt.HasText("Codex") {
		t.Fatalf("非 Magpie 窗口不应渲染供应商组头，texts: %q", tt.Texts())
	}
	if !tt.HasText("a@example.com · 在用") {
		t.Fatalf("单账号应标在用，texts: %q", tt.Texts())
	}
}

// 校验窗口行同时有倒计时和提前用尽提示时，行内只显示提前用尽文案，倒计时不占行内文本空间。
func TestRenderComposerQuotaWindowRow_PaceHidesCountdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	// 5h 窗口过去 1 小时，已用 50%，重置在 4 小时后：触发 paceStateEarly（提前用尽）
	win := ProviderQuotaWindow{
		Period:      "5h",
		Label:       "5 hours",
		UsedPercent: 50,
		ResetsAt:    now.Add(4 * time.Hour),
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaWindowRow(c, nil, 0, win, now, false, "", 0)
	}, 400, 100)

	if !tt.HasText("预计约 1 小时后用尽") {
		t.Fatalf("应显示提前用尽文案，texts: %q", tt.Texts())
	}
	if tt.HasText("4 小时后重置") {
		t.Fatalf("倒计时不应出现在行内可见文字中（已移入 Tooltip），texts: %q", tt.Texts())
	}
}
