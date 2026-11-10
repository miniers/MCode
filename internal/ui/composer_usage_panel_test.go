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

func TestRenderUsagePanel_OmitsQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "claude"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{{Label: "5 小时限额", UsedPercent: 62}})

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 400, 400)
	if !tt.HasText("上下文窗口") || tt.HasText("额度") || tt.HasText("62%") || tt.HasText("38%") {
		t.Fatalf("浮窗应显示上下文窗口且不显示额度，texts: %q", tt.Texts())
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
// 有 Active 账号时用它，全部停用时不判在用。
func TestQuotaInUseAccountSkipsDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	windows := []ProviderQuotaWindow{
		{ID: "gemini", Period: "5h", Label: "系统账号 · Gemini Models", Account: "系统账号", UsedPercent: 40, Disabled: true},
		{ID: "gemini-7d", Period: "7d", Label: "a@example.com · Gemini Models 7d", Account: "a@example.com", UsedPercent: 10, Active: true},
	}
	s := newChatState(NewFakeClient())
	if got := s.quotaInUseAccount("agy", windows); got != "a@example.com" {
		t.Fatalf("在用账号 = %q，想要 a@example.com", got)
	}
	// 全部账号停用时不判定在用账号。
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

