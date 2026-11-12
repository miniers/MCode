package mcodeui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestRenderComposerQuotaStrip_WithWindows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "agy"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "lkliuss@gmail.com · Gemini Models", UsedPercent: 42.4, Active: true, ResetsAt: now.Add(2*time.Hour + 30*time.Minute)},
		{Label: "lkliuss@gmail.com · Gemini Models 7d", UsedPercent: 58, Active: true},
		{Label: "lkliuss@gmail.com · Weekly · Opus", UsedPercent: 80, Active: true},
	})
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th)
	}, 400, 400)

	// 额度条每个窗口渲染为「短标签 + 迷你进度条 + 已用百分比」。
	if !tt.HasText("5h") || !tt.HasText("42%") {
		t.Fatalf("expected 5-hour readout '5h' + '42%%', texts: %q", tt.Texts())
	}
	if !tt.HasText("周") || !tt.HasText("58%") {
		t.Fatalf("expected weekly readout '周' + '58%%', texts: %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "lkliuss@gmail.com") {
			t.Fatalf("strip must not show the account name, texts: %q", tt.Texts())
		}
	}
	// 两个窗口各有一条迷你进度条（5h、周），尺寸 18×3 由渲染代码固定。
	for _, label := range []string{"agy-5h", "agy-周"} {
		if _, ok := tt.Find("composer-quota-gauge-" + label); !ok {
			t.Fatalf("expected progress bar %q, texts: %q", label, tt.Texts())
		}
	}

	// 每个供应商组各自可点击（可访问标签为「额度 <provider>」）：点击时记录被点的 provider 并打开只含该 provider 的弹层。
	if err := tt.Click("额度 agy"); err != nil {
		t.Fatalf("click quota provider group failed: %v", err)
	}
	if !s.composerView.quotaOpen || s.composerView.quotaPopoverProvider != "agy" {
		t.Fatalf("expected popover anchored on agy, open=%v provider=%q", s.composerView.quotaOpen, s.composerView.quotaPopoverProvider)
	}
	if _, ok := tt.Find("额度详情"); !ok || !tt.HasText("58%") {
		t.Fatalf("expected quota popover content, texts: %q", tt.Texts())
	}
	if !tt.HasText("2 小时 30 分钟后重置") {
		t.Fatalf("expected reset countdown, texts: %q", tt.Texts())
	}
	if tt.HasText("lkliuss@gmail.com · 在用") {
		t.Fatalf("single account should not have a header, texts: %q", tt.Texts())
	}
	texts := tt.Texts()
	var listTexts []string
	for i, text := range texts {
		if text == "额度列表" {
			listTexts = texts[i+1:]
			break
		}
	}
	for _, want := range []string{"5h", "周", "42%", "58%"} {
		found := false
		for _, text := range listTexts {
			if text == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("popover must list every window, missing %q, texts: %q", want, texts)
		}
	}

	// 再次点击同一组关闭弹层并清空锚定的 provider。
	if err := tt.Click("额度 agy"); err != nil {
		t.Fatalf("click quota provider group again failed: %v", err)
	}
	if s.composerView.quotaOpen || s.composerView.quotaPopoverProvider != "" {
		t.Fatalf("expected popover closed and provider cleared, open=%v provider=%q", s.composerView.quotaOpen, s.composerView.quotaPopoverProvider)
	}
}

func TestRenderComposerQuotaStrip_MultipleAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := newChatState(NewFakeClient())
	setSwarmEnabledForTest(s, true)
	th := &Thread{ID: "t1", Provider: "agy", BeesProvider: "claude"}
	SetChatQuotaWindowsForProvider(s, "agy", []ProviderQuotaWindow{
		{Label: "a@example.com · Gemini Models", UsedPercent: 10},
		{Label: "b@example.com · Gemini Models", UsedPercent: 20, Active: true},
		{Label: "b@example.com · Gemini Models 7d", UsedPercent: 30, Active: true},
	})
	defer SetChatQuotaWindowsForProvider(s, "agy", nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th)
	}, 400, 400)

	// 只有后端标了 Active 的 b@ 账户算在用，a@ 即使排在前面也不展示。
	if !tt.HasText("5h") || !tt.HasText("20%") || tt.HasText("10%") {
		t.Fatalf("expected active account readout, texts: %q", tt.Texts())
	}
	// 这一帧渲染 bee 的 claude 组时它还没有额度缓存，额度条会真的去请求一次；等这次请求结束再注入，
	// 否则它的空结果会在注入之后落地并覆盖掉注入的窗口，claude 组随即消失。
	waitForAutoDormant(t, func() bool {
		loading, _ := composerQuotaStatus(s, "claude")
		return !loading
	})
	SetChatQuotaWindowsForProvider(s, "claude", []ProviderQuotaWindow{
		{Label: "c@example.com · 5-hour limit", UsedPercent: 90, Active: true},
	})

	tt = ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th)
	}, 400, 400)
	if !tt.HasText("20%") || !tt.HasText("｜") || !tt.HasText("90%") {
		t.Fatalf("expected one group per provider separated by '｜', texts: %q", tt.Texts())
	}
	// 竖线是组外的独立文本（HasText 已断言），每个供应商组按可访问标签「额度 <provider>」查找。
	if _, ok := tt.Find("额度 agy"); !ok {
		t.Fatalf("expected clickable group for agy, texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("额度 claude"); !ok {
		t.Fatalf("expected clickable group for claude, texts: %q", tt.Texts())
	}

	// 点击 claude 组只打开 claude 的弹层：标题与窗口都归属 claude，agy 的窗口不出现。
	if err := tt.Click("额度 claude"); err != nil {
		t.Fatalf("click claude group failed: %v", err)
	}
	if s.composerView.quotaPopoverProvider != "claude" {
		t.Fatalf("expected popover anchored on claude, got %q", s.composerView.quotaPopoverProvider)
	}
	popover := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaPopover(c, s, th, s.composerView.quotaPopoverProvider, 0)
	}, 400, 400)
	if !popover.HasText("90%") {
		t.Fatalf("expected claude quota section, texts: %q", popover.Texts())
	}
	if popover.HasText("10%") || popover.HasText("20%") || popover.HasText("30%") {
		t.Fatalf("popover must only show the clicked provider, texts: %q", popover.Texts())
	}
}

func TestRenderComposerQuotaStrip_WithoutWindows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := newChatState(NewFakeClient())
	SetChatQuotaWindowsForProvider(s, "claude", nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, &Thread{ID: "t1", Provider: "claude"})
	}, 400, 400)

	if len(tt.Texts()) != 0 {
		t.Fatalf("expected no render without quota windows, texts: %q", tt.Texts())
	}
}

func TestRenderComposerQuotaStrip_AgyGeminiOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "agy"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "acc · Gemini Models", UsedPercent: 18, Account: "acc", Active: true},
		{Label: "acc · Gemini Models 7d", UsedPercent: 62, Account: "acc", Active: true},
		{Label: "acc · Claude and GPT models", UsedPercent: 0, Account: "acc"},
		{Label: "acc · Claude and GPT models 7d", UsedPercent: 0, Account: "acc"},
	})
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th)
	}, 400, 400)

	if !tt.HasText("5h") || !tt.HasText("18%") {
		t.Fatalf("expected 5-hour readout '5h' + '18%%', texts: %q", tt.Texts())
	}
	if !tt.HasText("周") || !tt.HasText("62%") {
		t.Fatalf("expected weekly readout '周' + '62%%', texts: %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "Claude") {
			t.Fatalf("strip must not show Claude windows, texts: %q", tt.Texts())
		}
	}
	for _, label := range []string{"composer-quota-gauge-agy-5h", "composer-quota-gauge-agy-周"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("expected progress bar %q, texts: %q", label, tt.Texts())
		}
	}
	var gaugeCount int
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "composer-quota-gauge-") {
			gaugeCount++
		}
	}
	if gaugeCount != 2 {
		t.Fatalf("expected exactly two gauges, got %d, texts: %q", gaugeCount, tt.Texts())
	}
}

// 额度条不再按模型名映射订阅账户：bee 用 magpie 下 claude 账户的模型时，额度条仍只展示
// 后端唯一标为 Active 的账户，不会跳到 claude/cline 的数字。
func TestRenderComposerQuotaStrip_IgnoresBeeModelForAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	windows := []ProviderQuotaWindow{
		{ID: "commandcode-plan/session", Period: "5h", Label: "session", Account: "commandcode-plan", UsedPercent: 12, Active: true},
		{ID: "claude/session", Period: "5h", Label: "session", Account: "claude", UsedPercent: 37},
		{ID: "cline/session", Period: "5h", Label: "session", Account: "cline", UsedPercent: 55},
	}

	for _, beeModel := range []string{"magpie-jp/auto/claude-opus-4-5", "magpie-jp/auto/cline-gpt-x", "magpie-jp/auto/deepseek-v4.1-flash"} {
		t.Run(beeModel, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			setSwarmEnabledForTest(s, true)
			SetChatQuotaWindowsForProvider(s, "pi:magpie-jp", windows)
			defer SetChatQuotaWindowsForProvider(s, "pi:magpie-jp", nil)
			th := &Thread{
				ID: "t1", Provider: "pi", Model: "magpie-jp/auto/deepseek-v4.1-flash",
				BeesProvider: "pi", BeesModel: beeModel,
			}

			tt := ui.NewTester(func(c *ui.Context) {
				renderComposerQuotaStrip(c, s, th)
			}, 400, 400)

			if !tt.HasText("12%") {
				t.Fatalf("expected active account readout 12%%, texts: %q", tt.Texts())
			}
			if tt.HasText("37%") || tt.HasText("55%") {
				t.Fatalf("must not pick another account by model name, texts: %q", tt.Texts())
			}
		})
	}
}

func setSwarmEnabledForTest(s *chatState, enabled bool) {
	panel := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	panel.state.Enabled = enabled
	swarmPanelMu.Unlock()
}

// 子代理关闭时额度条只展示主代理的额度，不列 bee 供应商的那一组。
func TestRenderComposerQuotaStrip_SwarmOffHidesBeeGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := newChatState(NewFakeClient())
	SetChatQuotaWindowsForProvider(s, "claude", []ProviderQuotaWindow{
		{Label: "5h", Period: "5h", UsedPercent: 11, Account: "claude-acc", Active: true},
	})
	SetChatQuotaWindowsForProvider(s, "codex#gpt-5", []ProviderQuotaWindow{
		{Label: "5h", Period: "5h", UsedPercent: 77, Account: "codex-acc", Active: true},
	})
	defer SetChatQuotaWindowsForProvider(s, "claude", nil)
	defer SetChatQuotaWindowsForProvider(s, "codex#gpt-5", nil)
	th := &Thread{ID: "t1", Provider: "claude", BeesProvider: "codex", BeesModel: "gpt-5"}

	render := func() *ui.Tester {
		return ui.NewTester(func(c *ui.Context) {
			renderComposerQuotaStrip(c, s, th)
		}, 400, 400)
	}

	if tt := render(); !tt.HasText("11%") || tt.HasText("77%") {
		t.Fatalf("swarm off: want only lead quota, texts: %q", tt.Texts())
	} else if _, ok := tt.Find("claude"); ok {
		t.Fatal("单供应商不应显示图标")
	}
	setSwarmEnabledForTest(s, true)
	if tt := render(); !tt.HasText("11%") || !tt.HasText("77%") {
		t.Fatalf("swarm on: want lead and bee quota, texts: %q", tt.Texts())
	} else {
		for _, provider := range []string{"claude", "codex"} {
			if _, ok := tt.Find(provider); !ok {
				t.Fatalf("多供应商缺少 %s 图标无障碍标签", provider)
			}
		}
	}
}

// 有重置时间与可推断周期的额度窗口，迷你条在时间进度位置显示竖线；没有重置时间时不显示。
func TestRenderComposerQuotaStrip_PaceMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now()
	usagePanelNow = func() time.Time { return now }
	t.Cleanup(func() { usagePanelNow = time.Now })

	s := newChatState(NewFakeClient())
	th := &Thread{ID: "t1", Provider: "claude"}

	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5-hour limit", UsedPercent: 62, Active: true, ResetsAt: now.Add(2 * time.Hour)},
	})
	defer SetChatQuotaWindowsForProvider(s, th.Provider, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th)
	}, 400, 400)
	if _, ok := tt.Find("composer-quota-gauge-claude-5h-marker"); !ok {
		t.Fatalf("expected pace marker in mini gauge, texts: %q", tt.Texts())
	}

	// 没有重置时间时保持旧渲染，不出现竖线。
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5-hour limit", UsedPercent: 62, Active: true},
	})
	tt = ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th)
	}, 400, 400)
	if _, ok := tt.Find("composer-quota-gauge-claude-5h-marker"); ok {
		t.Fatalf("must not render pace marker without reset time, texts: %q", tt.Texts())
	}
}

type quotaThreadClient struct {
	*FakeClient
	mu     sync.Mutex
	calls  map[string]string
	models map[string]string
}

func (c *quotaThreadClient) ProviderQuota(ctx context.Context, provider string) ([]ProviderQuotaWindow, error) {
	thread := QuotaThread(ctx)
	c.mu.Lock()
	c.calls[thread] = provider
	if c.models == nil {
		c.models = make(map[string]string)
	}
	c.models[thread] = QuotaModel(ctx)
	c.mu.Unlock()
	return []ProviderQuotaWindow{{Label: "5h", Period: "5h", Account: thread, UsedPercent: map[string]float64{"lead": 10, "bee1": 70}[thread], Active: true}}, nil
}

func TestQuotaStripSwarmUsesEachThread(t *testing.T) {
	client := &quotaThreadClient{FakeClient: NewFakeClient(), calls: make(map[string]string)}
	s := newChatState(client)
	s.selectedThreadID = "lead"
	setSwarmEnabledForTest(s, true)
	panel := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	panel.state.Bees = []SwarmBee{{Provider: "codex", ThreadID: "bee1"}}
	swarmPanelMu.Unlock()
	th := &Thread{ID: "different", Provider: "codex"}
	groups := quotaStripProviders(s, th)
	if len(groups) != 2 || groups[0].threadID != "" || groups[1].threadID != "bee1" {
		t.Fatalf("额度组会话不正确：%+v", groups)
	}
	for _, g := range groups {
		s.loadComposerQuotaForProvider(g.provider, g.threadID, g.model, false)
	}
	waitForAutoDormant(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.quotaLoadingProviders["codex"] && !s.quotaLoadingProviders["codex@bee1"]
	})
	client.mu.Lock()
	lead, bee := client.calls["lead"], client.calls["bee1"]
	client.mu.Unlock()
	if lead != "codex" || bee != "codex" {
		t.Fatalf("请求会话不正确：lead=%q bee=%q", lead, bee)
	}
	plans := quotaStripPlans(s, groups)
	if len(plans) != 2 || plans[0].account != "lead" || plans[0].windows[0].UsedPercent != 10 || plans[1].account != "bee1" || plans[1].windows[0].UsedPercent != 70 {
		t.Fatalf("会话额度未隔离：%+v", plans)
	}
}

func TestQuotaStripBeeModelWithoutSession(t *testing.T) {
	client := &quotaThreadClient{FakeClient: NewFakeClient(), calls: make(map[string]string)}
	s := newChatState(client)
	t.Cleanup(s.close)
	s.selectedThreadID = "lead"
	setSwarmEnabledForTest(s, true)
	th := &Thread{ID: "lead", Provider: "claude", BeesProvider: "codex", BeesModel: "auto/gpt-x"}
	groups := quotaStripProviders(s, th)
	if len(groups) != 2 || groups[1] != (quotaStripGroup{provider: "codex", model: "auto/gpt-x"}) {
		t.Fatalf("未启动 bee 应按自己的模型分组: %+v", groups)
	}
	for _, g := range groups {
		s.loadComposerQuotaForProvider(g.provider, g.threadID, g.model, false)
	}
	waitForAutoDormant(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.quotaLoadingProviders["claude"] && !s.quotaLoadingProviders["codex#auto/gpt-x"]
	})
	client.mu.Lock()
	provider, model, lead := client.calls[""], client.models[""], client.calls["lead"]
	client.mu.Unlock()
	if provider != "codex" || model != "auto/gpt-x" || lead != "claude" {
		t.Fatalf("模型请求不应借用 Lead 会话: provider=%q model=%q lead=%q", provider, model, lead)
	}
	if got := quotaGroupWindows(s, groups[1]); len(got) != 1 || got[0].Account != "" {
		t.Fatalf("模型缓存读取不正确: %+v", got)
	}
	other := quotaStripGroup{provider: "codex", model: "auto/other"}
	if got := quotaGroupWindows(s, other); len(got) != 0 {
		t.Fatalf("不同模型不可共用缓存: %+v", got)
	}
	if got := getChatQuotaWindowsForProvider(s, "codex"); len(got) != 0 {
		t.Fatalf("模型缓存不可写入 provider 共用缓存: %+v", got)
	}
	popover := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaPopover(c, s, th, "codex", 0, groups[1])
	}, 480, 400)
	if !popover.HasText("0%") {
		t.Fatalf("弹层应读取模型缓存: %q", popover.Texts())
	}
}

func TestQuotaStripBeeSessionReplacesModelGroup(t *testing.T) {
	client := &quotaThreadClient{FakeClient: NewFakeClient(), calls: make(map[string]string)}
	s := newChatState(client)
	t.Cleanup(s.close)
	s.selectedThreadID = "lead"
	setSwarmEnabledForTest(s, true)
	panel := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	panel.state.Bees = []SwarmBee{{Provider: "codex", ThreadID: "bee1", Model: "auto/gpt-x"}}
	swarmPanelMu.Unlock()
	th := &Thread{ID: "lead", Provider: "claude", BeesProvider: "codex", BeesModel: "auto/gpt-x"}
	groups := quotaStripProviders(s, th)
	if len(groups) != 2 || groups[1] != (quotaStripGroup{provider: "codex", threadID: "bee1"}) {
		t.Fatalf("已启动 bee 应只保留真实会话组: %+v", groups)
	}
	g := groups[1]
	s.loadComposerQuotaForProvider(g.provider, g.threadID, g.model, false)
	waitForAutoDormant(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.quotaLoadingProviders["codex@bee1"]
	})
	client.mu.Lock()
	provider, model, count := client.calls["bee1"], client.models["bee1"], len(client.calls)
	client.mu.Unlock()
	if provider != "codex" || model != "" || count != 1 {
		t.Fatalf("已启动 bee 应只请求自己的会话: provider=%q model=%q count=%d", provider, model, count)
	}
}

func TestRenderComposerQuotaStrip_BalanceText(t *testing.T) {
	for _, window := range []ProviderQuotaWindow{
		{Display: "$12.30", Text: true},
		{Display: "$12.30"},
		{Display: "$12.30", Text: true, Period: "5h"},
	} {
		for _, tier := range []composerChinSize{composerChinFull, composerChinNarrow, composerChinIcons} {
			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)
			SetChatQuotaWindowsForProvider(s, "codex", []ProviderQuotaWindow{window})
			tt := ui.NewTester(func(c *ui.Context) {
				renderComposerQuotaStrip(c, s, &Thread{ID: "lead", Provider: "codex"}, tier)
			}, 400, 400)
			if !tt.HasText("$12.30") || tt.HasText("0%") {
				t.Fatalf("余额应只显示文字: %q", tt.Texts())
			}
		}
	}
}

func TestWithQuotaModelRoundTrip(t *testing.T) {
	if QuotaModel(context.Background()) != "" || QuotaModel(nil) != "" {
		t.Fatal("无模型上下文应返回空串")
	}
	ctx := WithQuotaThread(context.Background(), "lead")
	ctx = WithQuotaModel(ctx, "auto/gpt-x")
	if QuotaModel(ctx) != "auto/gpt-x" || QuotaThread(ctx) != "lead" {
		t.Fatal("模型与会话上下文应各自保留")
	}
	if WithQuotaModel(nil, "auto/gpt-x") != nil || WithQuotaModel(ctx, "") != ctx {
		t.Fatal("空上下文或空模型不应新增上下文")
	}
}
