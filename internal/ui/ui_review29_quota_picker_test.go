package mcodeui

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestUIReview29SessionStatsConcurrentSwarm(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", Provider: "openai", Model: "gpt-5"}}
	s.providers = []ProviderInfo{{
		ID: "openai",
		Models: []ModelInfo{{
			ID:   "gpt-5",
			Cost: &ModelCost{Input: 10, Output: 20},
		}},
	}}
	s.threadUsage = ThreadUsage{
		Requests: 1,
		Total:    TokenUsage{Input: 1000, Output: 500},
	}
	s.mu.Unlock()

	hp := getSwarmPanelState(s)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			bees := []SwarmBee{{ThreadID: "b1", Tokens: 100, ToolCalls: 2}}
			if i%2 == 0 {
				bees = append(bees, SwarmBee{ThreadID: "b2", Tokens: 200, ToolCalls: 3})
			}
			swarmPanelMu.Lock()
			hp.setStateLocked(SwarmState{Bees: bees})
			swarmPanelMu.Unlock()
		}
	}()

	tt := ui.NewTester(func(c *ui.Context) {
		renderSessionStatsPopover(c, s, "t1", "review29-stats", true)
	}, 360, 480)
	for i := 0; i < 40; i++ {
		tt.Frame()
	}
	close(stop)
	wg.Wait()

	if !tt.HasText("估算费用") {
		t.Fatalf("会话统计应保留估算费用行: %v", tt.Texts())
	}
	if hasExactText(tt, "费用") {
		t.Fatalf("会话统计不应再显示重复的费用分组标题: %v", tt.Texts())
	}
}

type creditsTimeoutFakeClient struct {
	*FakeClient
	mu       sync.Mutex
	hasDeadl bool
	block    bool
}

func (c *creditsTimeoutFakeClient) ProviderCredits(ctx context.Context, provider string) (ProviderCredits, error) {
	_, ok := ctx.Deadline()
	c.mu.Lock()
	c.hasDeadl = ok
	block := c.block
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return ProviderCredits{}, ctx.Err()
	}
	return c.FakeClient.ProviderCredits(ctx, provider)
}

func TestUIReview29CreditsErrorAndRetry(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.providerCreditsErr = errors.New("credits boom")
	client := &creditsTimeoutFakeClient{FakeClient: fake}

	s := newChatState(client)
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "deepseek"}
	SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
		{Label: "5 hours", Period: "5h", UsedPercent: 25},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("host")).Fill().Children(func() {
			ui.Box(c.Key("spacer")).Grow(1)
			renderComposerQuotaStrip(c, s, th)
		})
	}, 520, 400)

	if err := tt.Click("额度 " + th.Provider); err != nil {
		t.Fatalf("打开额度弹层失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("credits boom") })

	client.mu.Lock()
	hasDeadline := client.hasDeadl
	client.mu.Unlock()
	if !hasDeadline {
		t.Fatal("ProviderCredits 必须传入带超时的 context")
	}

	// 关闭后再次打开应照常重试，而不是被静默缓存挡住。
	if err := tt.Click("额度 " + th.Provider); err != nil {
		t.Fatalf("关闭额度弹层失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("额度 " + th.Provider); err != nil {
		t.Fatalf("再次打开额度弹层失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.providerCreditsCalls) >= 2
	})

	t.Run("context usage panel shows credits error", func(t *testing.T) {
		s2 := newChatState(client)
		t.Cleanup(s2.close)
		tt2 := ui.NewTester(func(c *ui.Context) {
			renderUsagePanel(c, s2, &Thread{ID: "t2", Provider: "deepseek"})
		}, 400, 400)
		waitFor(t, tt2, func() bool { return tt2.HasText("credits boom") })
	})

	t.Run("settingsRPCTimeout triggers deadline error", func(t *testing.T) {
		orig := settingsRPCTimeout
		settingsRPCTimeout = 20 * time.Millisecond
		t.Cleanup(func() { settingsRPCTimeout = orig })

		timeoutClient := &creditsTimeoutFakeClient{
			FakeClient: NewFakeClient(),
			block:      true,
		}
		s3 := newChatState(timeoutClient)
		t.Cleanup(s3.close)
		tt3 := ui.NewTester(func(c *ui.Context) {
			renderUsagePanel(c, s3, &Thread{ID: "t3", Provider: "deepseek"})
		}, 400, 400)
		waitFor(t, tt3, func() bool { return tt3.HasText("余额读取超时") })
	})
}

func TestUIReview29ReduceMotionEndsImmediately(t *testing.T) {
	setAppReduceMotion(t, true)
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}

	t.Run("quota ring", func(t *testing.T) {
		used := 10.0
		actual := ui.NewTesterWith(func(c *ui.Context) {
			renderComposerQuotaRing(c, "rm-ring", "额度", &quotaRingWindow{used: used}, nil)
		}, 40, 40, options)
		used = 85.0
		actual.Frame()

		expected := ui.NewTesterWith(func(c *ui.Context) {
			color := quotaLevelColor(c, used)
			ui.Box(c.Key("expected")).Size(IconMD, IconMD).Children(func() {
				ui.Image(c, makeQuotaRingSVG(isDark(c), used/100, 0, true, false, color, ThemeSecondaryText(c))).
					Size(IconMD, IconMD).
					Fit(ui.Contain)
			})
		}, 40, 40, options)
		expected.Frame()

		if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
			t.Fatal("开启 ReduceMotion 后额度环应在第一帧即到达终值")
		}
		if actual.FramePending() {
			t.Fatal("开启 ReduceMotion 后额度环不应再请求下一帧")
		}
	})

	t.Run("model picker page", func(t *testing.T) {
		depth := 0
		state := &ModelPickerState{}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			page := ui.Column(c.Key("rm-page"))
			modelPickerPageTransition(c, page, state, depth)
			page.Children(func() { ui.Text(c, "模型列表页") })
		}, 240, 120, options)
		depth = 1
		tt.Frame()
		if !tt.HasText("模型列表页") {
			t.Fatal("模型选择器翻页未渲染目标页")
		}
		if tt.FramePending() {
			t.Fatal("开启 ReduceMotion 后模型选择器翻页不应再请求下一帧")
		}
	})

	t.Run("effort signal", func(t *testing.T) {
		current := "low"
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderEffortSignal(c, "rm-signal", EffortSliderCardProps{
				Efforts:       []string{"low", "medium", "high"},
				Current:       current,
				DefaultEffort: "medium",
			})
		}, 80, 40, options)
		current = "high"
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("开启 ReduceMotion 后推理强度信号格不应再请求下一帧")
		}
	})
}

func assertRectInside(t *testing.T, name string, inner, outer ui.Rect) {
	t.Helper()
	if inner.W <= 0 || inner.H <= 0 {
		t.Fatalf("%s 尺寸无效: %+v", name, inner)
	}
	if inner.X < outer.X-0.5 || inner.Y < outer.Y-0.5 ||
		inner.X+inner.W > outer.X+outer.W+0.5 || inner.Y+inner.H > outer.Y+outer.H+0.5 {
		t.Fatalf("%s 超出容器边界: inner=%+v outer=%+v", name, inner, outer)
	}
}

func TestUIReview29NarrowLayoutBounds(t *testing.T) {
	testenv.TempHome(t)

	for _, tc := range []struct {
		name  string
		winW  int
		mainW float32
	}{
		{name: "window_948_main_360", winW: WindowMinWidth, mainW: 360},
		{name: "window_360_narrow", winW: 360, mainW: 360},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const winH = 560
			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)

			longAccount := "very-long-account-name-for-narrow-popover-overflow-check-0123456789@example.com"
			th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}
			SetChatQuotaWindowsForProvider(s, th.Provider, []ProviderQuotaWindow{
				{Label: "claude · " + longAccount + " · 5 hours", Account: "claude · " + longAccount, Source: "magpie", Provider: "claude", Period: "5h", UsedPercent: 30},
				{Label: "claude · " + longAccount + " · 7 days", Account: "claude · " + longAccount, Source: "magpie", Provider: "claude", Period: "7d", UsedPercent: 60},
			})
			t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

			tt := ui.NewTester(func(c *ui.Context) {
				ui.Row(c.Key("shell")).Fill().Children(func() {
					if float32(tc.winW) > tc.mainW {
						ui.Box(c.Key("left-pane")).Grow(1).FillHeight()
					}
					ui.Column(c.Key("main-pane")).Width(tc.mainW).FillHeight().Children(func() {
						ui.Box(c.Key("main-spacer")).Grow(1)
						ui.Row(c.Key("bottom-bar")).FillWidth().AlignItems(ui.Center).Children(func() {
							ui.Row(c.Key("quota-slot")).Grow(1).MinWidth(0).Shrink(1).ClipX().Children(func() {
								renderComposerQuotaStrip(c, s, th)
							})
							renderContextMeter(c, s, th)
						})
					})
				})
			}, tc.winW, winH)

			winRect := ui.Rect{X: 0, Y: 0, W: float32(tc.winW), H: winH}

			if err := tt.Click("额度 " + th.Provider); err != nil {
				t.Fatalf("打开额度浮层失败: %v", err)
			}
			tt.Frame()

			quotaPanel, ok := tt.Find("额度详情")
			if !ok {
				t.Fatal("找不到额度详情浮层")
			}
			assertRectInside(t, "额度详情浮层", quotaPanel, winRect)

			refreshBtn, ok := tt.Find("刷新")
			if !ok {
				t.Fatal("找不到刷新按钮")
			}
			assertRectInside(t, "刷新按钮", refreshBtn, quotaPanel)

			// 关闭额度浮层再打开上下文用量浮层。
			s.composerView.quotaOpen = false
			s.composerView.quotaPopoverKey = ""
			tt.Frame()

			if err := tt.Click("上下文用量"); err != nil {
				t.Fatalf("打开上下文用量浮层失败: %v", err)
			}
			tt.Frame()

			usagePanel, ok := tt.Find("上下文用量面板")
			if !ok {
				t.Fatal("找不到上下文用量面板")
			}
			assertRectInside(t, "上下文用量浮层", usagePanel, winRect)
		})
	}
}
