package mcodeui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type routeTestClient struct {
	*FakeClient

	gate    chan struct{} // 非 nil 时 SetAgentMagpieMode 先等它关闭
	started chan string   // 非 nil 时每次保存前写入 "agent:mode"
}

func (f *routeTestClient) SetAgentMagpieMode(ctx context.Context, agent, mode string) error {
	if f.started != nil {
		f.started <- agent + ":" + mode
	}
	if f.gate != nil {
		<-f.gate
	}
	if err := f.FakeClient.SetAgentMagpieMode(ctx, agent, mode); err != nil {
		return err
	}
	setting, err := f.FakeClient.AgentMagpie(ctx, agent)
	if err == nil {
		setting.Mode = mode
		f.SetAgentMagpie(setting)
	}
	return err
}

// routeTestSetting 给路由测试留一个已勾选的 Magpie 模型：没有勾选时只有「直连」可选。
func routeTestSetting(agent, mode string) AgentMagpieSetting {
	return AgentMagpieSetting{
		Agent:  agent,
		Mode:   mode,
		Global: AgentMagpieGlobal{Supported: true},
		Endpoints: []PiMagpieEndpoint{{
			ID:         "gw",
			Name:       "测试网关",
			ProviderID: "magpie-gw",
			Models:     []PiMagpieModel{{ID: "m1"}},
			Selected:   []string{"m1"},
		}},
	}
}

// routeControlTesterWith 只渲染 Magpie 页里某个 agent 的路由行，便于单独验证路由控件。
func routeControlTesterWith(t *testing.T, client *routeTestClient, agent string) (*ui.Tester, *SettingsState) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	row := magpieAgentRouteRow(agent)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("route-section-test")).FillWidth().Children(func() {
			renderSettingsTreeSetting(c, st, row)
		})
	}, 1000, 4000)
	waitFor(t, tt, func() bool { _, known := magpieAgentRouteValue(st, agent); return known })
	if agent == "pi" {
		// pi 的勾选来自 piMagpie：等它加载完，后续用例手工改写快照时才不会被在途请求覆盖。
		waitFor(t, tt, func() bool {
			_, _, loaded, _, _ := magpieEndpointsFor(st, "pi")
			return loaded
		})
	}
	return tt, st
}

// waitRouteIdle 等到路由控件不再处于保存中，避免下一次点击落在禁用态。
func waitRouteIdle(t *testing.T, tt *ui.Tester, st *SettingsState, agent string) {
	t.Helper()
	waitFor(t, tt, func() bool {
		_, busy := magpieAgentRouteChoice(agent).get(st)
		return !busy
	})
}

func TestMagpieAgentRouteValue(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "pi"} {
		for _, tc := range []struct{ mode, want string }{{"inject", "仅 mcode"}, {"", "仅 mcode"}, {"unknown", "仅 mcode"}, {"global", "全局"}, {"off", "直连"}} {
			t.Run(agent+"/"+tc.mode, func(t *testing.T) {
				st := &SettingsState{}
				st.agentMagpieFor(agent).loaded = true
				st.agentMagpieFor(agent).setting.Mode = tc.mode
				if got, known := magpieAgentRouteValue(st, agent); !known || got != tc.want {
					t.Fatalf("路由 = %q, %v，期望 %q, true", got, known, tc.want)
				}
				st.agentMagpieFor(agent).loading = true
				if got, known := magpieAgentRouteValue(st, agent); known || got != "" {
					t.Fatalf("加载中路由 = %q, %v", got, known)
				}
				st.agentMagpieFor(agent).loading = false
				st.agentMagpieFor(agent).err = "失败"
				if got, known := magpieAgentRouteValue(st, agent); known || got != "" {
					t.Fatalf("加载失败路由 = %q, %v", got, known)
				}
			})
		}
	}
	if value, known := magpieAgentRouteValue(&SettingsState{}, "other"); known || value != "" {
		t.Fatalf("未知 agent 路由 = %q, %v", value, known)
	}
}

func TestMagpieAgentRouteControl_Switch(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		tt, st, fake := agentMagpiePageTester(t, routeTestSetting("claude", "inject"))
		clickMagpiePageRoute(t, tt, "Claude", "直连")
		waitFor(t, tt, func() bool {
			calls := fake.SetAgentMagpieModeCalls()
			value, known := magpieAgentRouteValue(st, "claude")
			return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Mode == "off" && len(fake.AgentMagpieCalls()) >= 2 && known && value == "直连"
		})
		clickMagpiePageRoute(t, tt, "Claude", "仅 mcode")
		waitFor(t, tt, func() bool {
			calls := fake.SetAgentMagpieModeCalls()
			return len(calls) == 2 && calls[1].Agent == "claude" && calls[1].Mode == "inject"
		})
	})
	t.Run("pi确认", func(t *testing.T) {
		tt, _, fake := agentMagpiePageTester(t, routeTestSetting("pi", "off"))
		clickMagpiePageRoute(t, tt, "pi", "全局")
		waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
		if len(fake.SetAgentMagpieModeCalls()) != 0 {
			t.Fatal("确认前不能保存")
		}
		if err := tt.Click("写入全局配置"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool {
			calls := fake.SetAgentMagpieModeCalls()
			return len(calls) == 1 && calls[0].Agent == "pi" && calls[0].Mode == "global"
		})
	})
	t.Run("失败回滚", func(t *testing.T) {
		fake := NewFakeClient()
		fake.setAgentMagpieModeErr = errors.New("保存失败")
		fake.SetAgentMagpie(routeTestSetting("claude", "inject"))
		tt, st := magpiePageWithFake(t, fake, true)
		waitFor(t, tt, func() bool { _, known := magpieAgentRouteValue(st, "claude"); return known })
		clickMagpiePageRoute(t, tt, "Claude", "直连")
		waitFor(t, tt, func() bool {
			value, known := magpieAgentRouteValue(st, "claude")
			return known && value == "仅 mcode" && tt.HasText("切换路由失败: 保存失败")
		})
	})
}

// TestMagpieAgentRouteControl_SavingDisablesChoice 覆盖：选「仅 mcode」后保存未完成前
// 路由控件处于禁用状态，再点「直连」不会发出第二次写入。
func TestMagpieAgentRouteControl_SavingDisablesChoice(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAgentMagpie(routeTestSetting("claude", "off"))
	client := &routeTestClient{FakeClient: fake, gate: make(chan struct{}), started: make(chan string, 4)}
	tt, st := routeControlTesterWith(t, client, "claude")

	if err := tt.Click("仅 mcode"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-client.started:
		if got != "claude:inject" {
			t.Fatalf("首次保存 = %q，期望 claude:inject", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("保存请求没有发出")
	}
	if _, busy := magpieAgentRouteChoice("claude").get(st); !busy {
		t.Fatal("写入完成前路由控件应处于禁用状态")
	}
	tt.Frame()
	if err := tt.Click("直连"); err != nil {
		t.Fatalf("保存途中点击失败: %v", err)
	}
	close(client.gate)

	waitFor(t, tt, func() bool { return len(fake.SetAgentMagpieModeCalls()) == 1 })
	tt.Frame()
	if calls := fake.SetAgentMagpieModeCalls(); len(calls) != 1 || calls[0].Mode != "inject" {
		t.Fatalf("保存途中不应再次写入: %v", calls)
	}
	if value, known := magpieAgentRouteValue(st, "claude"); !known || value != "仅 mcode" {
		t.Fatalf("保存后路由 = %q, %v，期望仅 mcode, true", value, known)
	}
}

func TestMagpieAgentRouteDescription(t *testing.T) {
	inject := map[string]string{
		"claude": "在 mcode 中选用 Magpie 模型时经网关；不改 ~/.claude 配置",
		"codex":  "使用 Codex 自身的登录与配置",
		"pi":     "mcode 启动的 pi 可用 Magpie 模型；不改 pi 自身配置",
	}
	for _, agent := range []string{"claude", "codex", "pi"} {
		title := magpieAgentTitle(agent)
		for _, tc := range []struct {
			mode, path    string
			known, saving bool
			want          string
		}{
			{mode: "inject", known: true, want: inject[agent]},
			{mode: "", known: true, want: inject[agent]},
			{mode: "unknown", known: true, want: inject[agent]},
			{mode: "off", known: true, want: "使用 " + title + " 自身的登录与配置"},
			{mode: "global", known: true, want: "已写入 " + title + " 全局配置，mcode 外启动的 " + title + " 也经 Magpie"},
			{mode: "global", path: " /tmp/config ", known: true, want: "已写入 /tmp/config，mcode 外启动的 " + title + " 也经 Magpie"},
			{mode: "global", path: "  ", known: true, want: "已写入 " + title + " 全局配置，mcode 外启动的 " + title + " 也经 Magpie"},
			{want: "正在加载路由…"},
			{saving: true, want: "正在切换路由…"},
			{known: true, saving: true, want: "正在切换路由…"},
		} {
			t.Run(agent+"/"+tc.mode+"/"+tc.want, func(t *testing.T) {
				snapshot := AgentMagpieSetting{Mode: tc.mode, Global: AgentMagpieGlobal{Path: tc.path}}
				if got := magpieAgentRouteDescription(agent, snapshot, tc.known, tc.saving); got != tc.want {
					t.Fatalf("说明 = %q，期望 %q", got, tc.want)
				}
			})
		}
	}
}

func TestMagpieAgentRouteControl_LoadError(t *testing.T) {
	fake := NewFakeClient()
	fake.agentMagpieErr = errors.New("读取失败")
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	row := magpieAgentRouteRow("claude")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSetting(c, st, row) }, 1000, 1000)
	waitFor(t, tt, func() bool { return tt.HasText("加载 Magpie 设置失败: 读取失败") })
	for _, text := range []string{"正在加载路由…", "使用 Claude 自身的登录与配置", "在 mcode 中选用 Magpie 模型时经网关；不改 ~/.claude 配置", "已写入 Claude 全局配置，mcode 外启动的 Claude 也经 Magpie"} {
		if tt.HasText(text) {
			t.Fatalf("加载失败不应显示 %q", text)
		}
	}
	if _, busy := magpieAgentRouteChoice("claude").get(st); !busy {
		t.Fatal("加载失败应禁用路由")
	}
	// 加载失败时控件仍在，只禁用；点击不会误发保存请求。
	if _, ok := tt.Find(magpieAgentRouteLabel("Claude")); !ok {
		t.Fatal("加载失败时仍应渲染路由控件")
	}
	if err := tt.Click("直连"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("加载失败不得保存")
	}
}

func TestMagpieAgentRouteControl_Options(t *testing.T) {
	for _, tc := range []struct {
		agent, mode string
		options     []string
		selected    int
	}{
		{"claude", "inject", []string{"直连", "仅 mcode", "全局"}, 1},
		{"codex", "inject", []string{"直连", "仅 mcode", "全局"}, 1},
		{"codex", "", []string{"直连", "仅 mcode", "全局"}, 1},
		{"pi", "inject", []string{"直连", "仅 mcode", "全局"}, 1},
		{"pi", "off", []string{"直连", "仅 mcode", "全局"}, 0},
		{"pi", "global", []string{"直连", "仅 mcode", "全局"}, 2},
	} {
		t.Run(tc.agent+"/"+tc.mode, func(t *testing.T) {
			tt, st, _ := agentMagpiePageTester(t, routeTestSetting(tc.agent, tc.mode))
			choice := magpieAgentRouteChoice(tc.agent)
			if len(choice.options) != len(tc.options) {
				t.Fatalf("分段数 = %d，期望 %d", len(choice.options), len(tc.options))
			}
			for i, option := range tc.options {
				if choice.options[i] != option || !tt.HasText(option) {
					t.Fatalf("缺少选项 %q", option)
				}
			}
			if idx, busy := choice.get(st); busy || idx != tc.selected {
				t.Fatalf("分段选择 = %d, %v", idx, busy)
			}
		})
	}
}

// TestMagpieAgentRouteControl_NoModels 覆盖：没有任何 Magpie 勾选时「仅 mcode」「全局」
// 不可用，点击也不发切换请求；勾选后同一控件即可切换。
func TestMagpieAgentRouteControl_NoModels(t *testing.T) {
	setting := routeTestSetting("claude", "off")
	setting.Endpoints[0].Selected = nil
	fake := NewFakeClient()
	fake.SetAgentMagpie(setting)
	client := &routeTestClient{FakeClient: fake}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	row := magpieAgentRouteRow("claude")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSetting(c, st, row) }, 1000, 2000)
	waitFor(t, tt, func() bool { _, known := magpieAgentRouteValue(st, "claude"); return known })
	for _, option := range []string{"仅 mcode", "全局"} {
		if err := tt.Click(option); err != nil {
			t.Fatalf("点击 %s 失败: %v", option, err)
		}
	}
	tt.Frame()
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("没有勾选时不得切换路由")
	}
	// 勾选一个模型后同一分段即可切换。
	st.mu.Lock()
	st.agentMagpieFor("claude").setting.Endpoints[0].Selected = []string{"m1"}
	st.mu.Unlock()
	st.invalidate()
	tt.Frame()
	if err := tt.Click("仅 mcode"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Mode == agentMagpieModeInject
	})
}

// TestSettings_AgentMagpie_PiGlobalRoute 覆盖：pi 有三段路由，选「全局」先弹确认框，
// 取消不写；确认后写入全局配置。
func TestSettings_AgentMagpie_PiGlobalRoute(t *testing.T) {
	setting := routeTestSetting("pi", agentMagpieModeInject)
	setting.Global = AgentMagpieGlobal{Supported: true, State: "none", Path: "/home/me/.pi/models.json"}
	tt, st, fake := agentMagpiePageTester(t, setting)
	waitFor(t, tt, func() bool {
		value, known := magpieAgentRouteValue(st, "pi")
		_, _, loaded, _, _ := magpieEndpointsFor(st, "pi")
		_, found := tt.Find(magpieAgentRouteLabel("pi"))
		return known && loaded && value == "仅 mcode" && found
	})
	if options := magpieAgentRouteChoice("pi").options; len(options) != 3 || options[2] != "全局" {
		t.Fatalf("pi 应有三段路由: %v", options)
	}
	clickMagpiePageRoute(t, tt, "pi", "全局")
	waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("确认前不得写入")
	}
	if !tt.HasText("写全局配置会把 测试网关 的 API Key 以明文写入 /home/me/.pi/models.json。") {
		t.Fatalf("缺少 pi 全局确认文案: %v", tt.Texts())
	}
	if !tt.HasText("mcode 内外的 pi 都会改用该 key 与 Magpie provider，不再使用原来的登录") {
		t.Fatalf("缺少 pi 登录说明: %v", tt.Texts())
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("写入全局配置") })
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("取消不得写入")
	}
	clickMagpiePageRoute(t, tt, "pi", "全局")
	waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
	if err := tt.Click("写入全局配置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 1 && calls[0].Agent == "pi" && calls[0].Mode == agentMagpieModeGlobal
	})
}

// TestSettings_AgentMagpie_ToggleFailureReloads 覆盖：勾选保存失败后回滚勾选并重读设置。
func TestSettings_AgentMagpie_ToggleFailureReloads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.setAgentMagpieModelsErr = errors.New("保存失败")
	tt, _ := agentMagpieClaudeTester(t, fake, agentMagpieTestSetting())
	waitFor(t, tt, func() bool { return tt.HasText("已启用（1）") })
	before := len(fake.AgentMagpieCalls())
	if err := tt.Click("添加模型"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("JP Haiku"); return ok })
	if err := tt.Click("JP Haiku"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return len(fake.AgentMagpieCalls()) > before && tt.HasText("保存失败")
	})
	if !tt.HasText("已启用（1）") {
		t.Fatalf("失败后应回到原来的勾选: %v", tt.Texts())
	}
}

// agentMagpieTestSetting 构造 claude 的两个 Magpie 端点：jp 两个模型（一个已勾选），us 一个模型。
func agentMagpieTestSetting() AgentMagpieSetting {
	return AgentMagpieSetting{
		Agent: "claude",
		Mode:  "inject",
		Endpoints: []PiMagpieEndpoint{
			{
				ID:         "jp",
				Name:       "JP 网关",
				ProviderID: "magpie-jp",
				Models: []PiMagpieModel{
					{ID: "auto/sonnet", Name: "JP Sonnet"},
					{ID: "auto/haiku", Name: "JP Haiku"},
				},
				Selected: []string{"auto/sonnet"},
			},
			{
				ID:         "us",
				Name:       "US 网关",
				ProviderID: "magpie-us",
				Models:     []PiMagpieModel{{ID: "auto/opus", Name: "US Opus"}},
			},
		},
	}
}

// agentMagpieClaudeTester 渲染模型页并展开 claude 行，返回渲染器与设置状态。
func agentMagpieClaudeTester(t *testing.T, fake *FakeClient, setting AgentMagpieSetting) (*ui.Tester, *SettingsState) {
	return agentMagpieClaudeTesterWithModels(t, fake, setting, []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}})
}

// agentMagpieClaudeTesterWithModels 同 agentMagpieClaudeTester，但可指定 claude 目录里的模型。
func agentMagpieClaudeTesterWithModels(t *testing.T, fake *FakeClient, setting AgentMagpieSetting, models []ModelInfo) (*ui.Tester, *SettingsState) {
	t.Helper()
	return magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: models})
}

// agentMagpiePageTester 在已连接的 Magpie 页面加载指定智能体设置。
func agentMagpiePageTester(t *testing.T, setting AgentMagpieSetting) (*ui.Tester, *SettingsState, *routeTestClient) {
	t.Helper()
	fake := NewFakeClient()
	fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "测试网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true}}
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
	if setting.Agent == "pi" {
		// pi 的路由可用性来自 pi 快照，与测试设置的端点保持一致。
		fake.SetPiMagpieEndpoints(setting.Endpoints)
	}
	fake.SetAgentMagpie(setting)
	client := &routeTestClient{FakeClient: fake}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageMagpie(c, st)
	}, 1000, 4000)
	waitFor(t, tt, func() bool {
		connected, known := magpieConnected(st)
		_, loaded := magpieAgentRouteValue(st, setting.Agent)
		return known && connected && loaded
	})
	tt.Frame()
	return tt, st, client
}

// clickMagpiePageRoute 在 Magpie 页直接点选某个 agent 行内的路由分段。
func clickMagpiePageRoute(t *testing.T, tt *ui.Tester, agent, option string) {
	t.Helper()
	agentID := map[string]string{"Claude": "claude", "Codex": "codex", "pi": "pi"}[agent]
	var control ui.Rect
	var ok bool
	waitFor(t, tt, func() bool { control, ok = tt.Find(magpieAgentRouteLabel(agent)); return ok })
	clickMagpieRouteOption(t, tt, control, agentID, option)
}

func TestSettings_AgentMagpie_UnifiedModelList(t *testing.T) {
	fake := NewFakeClient()
	tt, _ := agentMagpieClaudeTester(t, fake, agentMagpieTestSetting())
	waitFor(t, tt, func() bool {
		return tt.HasText("已启用（1）") && tt.HasText("JP Sonnet") && tt.HasText("claude 自身配置")
	})
	if tt.HasText("目录 Sonnet") || !tt.HasText("1 个模型") {
		t.Fatalf("自身配置应默认折叠并显示模型数量: %v", tt.Texts())
	}
	clickWhenSettled(t, tt, "展开 claude 自身配置")
	waitFor(t, tt, func() bool { return tt.HasText("目录 Sonnet") })
	if tt.HasText("JP Haiku") || tt.HasText("US Opus") || tt.HasText("Magpie 模型") {
		t.Fatalf("未启用的网关模型不应平铺或另设小节: %v", tt.Texts())
	}
	if calls := fake.AgentMagpieCalls(); len(calls) == 0 || calls[0] != "claude" {
		t.Fatalf("应按 claude 加载 Magpie 设置: %v", calls)
	}
}

// TestSettings_AgentMagpie_GlobalModeHidesOwnGroup 覆盖：写全局配置且有端点时，
// claude 自身配置组只是全局配置的回显，不再渲染；运行时注入时仍保留自身配置组。
func TestSettings_AgentMagpie_GlobalModeHidesOwnGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("global", func(t *testing.T) {
		fake := NewFakeClient()
		setting := agentMagpieTestSetting()
		setting.Mode = agentMagpieModeGlobal
		tt, _ := agentMagpieClaudeTester(t, fake, setting)

		waitFor(t, tt, func() bool {
			return tt.HasText("已启用（1）") && tt.HasText("JP Sonnet")
		})
		if tt.HasText("claude 自身配置") {
			t.Fatalf("写全局配置时不应渲染 claude 自身配置组: %v", tt.Texts())
		}
	})

	t.Run("inject", func(t *testing.T) {
		fake := NewFakeClient()
		tt, _ := agentMagpieClaudeTester(t, fake, agentMagpieTestSetting())

		waitFor(t, tt, func() bool {
			return tt.HasText("已启用（1）") && tt.HasText("claude 自身配置")
		})
		clickWhenSettled(t, tt, "展开 claude 自身配置")
		waitFor(t, tt, func() bool { return tt.HasText("目录 Sonnet") })
	})
}

// TestSettings_AgentMagpie_ToggleModel 覆盖：在「添加模型」浮层里勾选模型后，
// FakeClient 收到该端点的完整已选列表，且浮层保持打开。
func TestSettings_AgentMagpie_ToggleModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	tt, _ := agentMagpieClaudeTester(t, fake, agentMagpieTestSetting())

	waitFor(t, tt, func() bool { return tt.HasText("已启用（1）") })
	if err := tt.Click("添加模型"); err != nil {
		t.Fatalf("打开添加模型浮层失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("JP Haiku")
		return ok
	})
	if err := tt.Click("JP Haiku"); err != nil {
		t.Fatalf("勾选模型失败: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModelsCalls()
		if len(calls) == 0 {
			return false
		}
		c := calls[0]
		return c.Agent == "claude" && c.EndpointID == "jp" && len(c.ModelIDs) == 2 &&
			c.ModelIDs[0] == "auto/sonnet" && c.ModelIDs[1] == "auto/haiku"
	})
	// 切换勾选后浮层应保持打开，仍能继续勾选未启用的模型。
	if err := tt.Click("US Opus"); err != nil {
		t.Fatalf("切换勾选后浮层应保持打开，点击 US Opus 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		for _, c := range fake.SetAgentMagpieModelsCalls() {
			if c.EndpointID == "us" && len(c.ModelIDs) == 1 && c.ModelIDs[0] == "auto/opus" {
				return true
			}
		}
		return false
	})
}

func TestSettings_AgentMagpie_OwnSecondaryModelInPicker(t *testing.T) {
	fake := NewFakeClient()
	tt, _ := agentMagpieClaudeTesterWithModels(t, fake, agentMagpieTestSetting(), []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}, {ID: "haiku-old", Name: "旧 Haiku", Secondary: true}})
	waitFor(t, tt, func() bool { return tt.HasText("claude 自身配置") && tt.HasText("2 个模型") })
	if tt.HasText("目录 Sonnet") || tt.HasText("旧 Haiku") {
		t.Fatal("未展开时不应显示自身配置模型")
	}
	clickWhenSettled(t, tt, "展开 claude 自身配置")
	waitFor(t, tt, func() bool {
		return tt.HasText("目录 Sonnet") && tt.HasText("旧 Haiku")
	})
}

func TestSettings_AgentMagpie_GlobalModeConfirm(t *testing.T) {
	setting := agentMagpieTestSetting()
	setting.Mode = "off"
	tt, st, fake := agentMagpiePageTester(t, setting)
	clickMagpiePageRoute(t, tt, "Claude", "全局")
	waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("确认前应保持直连，不得写入")
	}
	if err := tt.Click("写入全局配置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		value, known := magpieAgentRouteValue(st, "claude")
		return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Mode == "global" && known && value == "全局"
	})
	waitRouteIdle(t, tt, st, "claude")
	clickMagpiePageRoute(t, tt, "Claude", "直连")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 2 && calls[1].Agent == "claude" && calls[1].Mode == "off"
	})
	if len(fake.AgentMagpieCalls()) < 2 {
		t.Fatal("成功后应重新加载设置")
	}
}

func TestSettings_AgentMagpie_GlobalModeCancel(t *testing.T) {
	setting := agentMagpieTestSetting()
	setting.Mode = "off"
	tt, _, fake := agentMagpiePageTester(t, setting)
	clickMagpiePageRoute(t, tt, "Claude", "全局")
	waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("写入全局配置") })
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("取消后应保持直连，且不得写入")
	}
}

func TestSettings_AgentMagpie_DriftedStatus(t *testing.T) {
	setting := agentMagpieTestSetting()
	setting.Mode = "global"
	setting.Global = AgentMagpieGlobal{Supported: true, State: "drifted", Path: "/home/me/.claude/settings.json", Drifted: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}, ManagedPath: "/etc/claude/managed-settings.json"}
	tt, _, fake := agentMagpiePageTester(t, setting)
	// 漂移状态现在内嵌在智能体行的路由区块里。
	waitFor(t, tt, func() bool {
		return tt.HasText("配置被改动：ANTHROPIC_API_KEY、ANTHROPIC_AUTH_TOKEN") && tt.HasText("系统管理员配置可能覆盖本设置：/etc/claude/managed-settings.json")
	})
	if tt.HasText("勾选的模型会写入") {
		t.Fatalf("底部说明已移除: %v", tt.Texts())
	}
	reapplyRect, ok := tt.Find("重新写入")
	if !ok {
		t.Fatalf("漂移时应显示重新写入: %v", tt.Texts())
	}
	keepRect, ok := tt.Find("保留我的修改")
	if !ok {
		t.Fatalf("漂移时应显示保留我的修改: %v", tt.Texts())
	}
	for _, rect := range []ui.Rect{reapplyRect, keepRect} {
		if rect.X >= 1000 {
			t.Fatalf("漂移操作按钮应落在窗口内: %+v", rect)
		}
	}
	before := len(fake.AgentMagpieCalls())
	if err := tt.Click("重新写入"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.AgentMagpieGlobalActionCalls()
		return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Action == "reapply"
	})
	waitFor(t, tt, func() bool { return len(fake.AgentMagpieCalls()) > before })
	if err := tt.Click("保留我的修改"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.AgentMagpieGlobalActionCalls()
		return len(calls) == 2 && calls[1].Agent == "claude" && calls[1].Action == "keep"
	})
}

func TestSettings_AgentMagpie_UnsupportedMode(t *testing.T) {
	setting := agentMagpieTestSetting()
	setting.Mode = "off"
	tt, _, fake := agentMagpiePageTester(t, setting)
	clickMagpiePageRoute(t, tt, "Claude", "仅 mcode")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Mode == "inject"
	})
	if tt.HasText("写入全局配置") {
		t.Fatal("不支持全局配置时不应弹出确认")
	}
}

func TestSettings_AgentMagpie_AppliedModeDescription(t *testing.T) {
	setting := agentMagpieTestSetting()
	setting.Mode = "global"
	setting.Global = AgentMagpieGlobal{Supported: true, State: "applied", Path: "/home/me/.claude/settings.json"}
	tt, st, _ := agentMagpiePageTester(t, setting)
	if value, known := magpieAgentRouteValue(st, "claude"); !known || value != "全局" {
		t.Fatalf("全局模式应显示全局路由: %q %v", value, known)
	}
	if !tt.HasText("全局") {
		t.Fatalf("全局分段应可见: %v", tt.Texts())
	}
	if tt.HasText("配置被改动") {
		t.Fatalf("已生效时不应显示漂移: %v", tt.Texts())
	}
}

// TestFakeClient_AgentMagpieDefaults 覆盖 FakeClient 的默认返回值：未预置时按运行时注入、无端点，
// 并且可以预设加载错误。
func TestFakeClient_AgentMagpieDefaults(t *testing.T) {
	fake := NewFakeClient()
	setting, err := fake.AgentMagpie(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if setting.Agent != "claude" || setting.Mode != agentMagpieModeInject || len(setting.Endpoints) != 0 || setting.Global.Supported {
		t.Fatalf("默认设置不对: %+v", setting)
	}

	fake.agentMagpieErr = ErrNotImplemented
	if _, err := fake.AgentMagpie(context.Background(), "claude"); err == nil {
		t.Fatal("应返回预设的加载错误")
	}
}

func TestSettings_AgentMagpie_NoEndpointsPlainProviderLayout(t *testing.T) {
	fake := NewFakeClient()
	setting := AgentMagpieSetting{Agent: "claude", Mode: "inject"}
	tt, st := magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}})
	if tt.HasText("Magpie 使用方式") || tt.HasText("正在加载 Magpie 端点…") {
		t.Fatalf("没有端点时不应显示 Magpie 控件与加载提示: %v", tt.Texts())
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		state := st.agentMagpie["claude"]
		return state != nil && state.loaded
	})
	if tt.HasText("Magpie 使用方式") || tt.HasText("正在加载 Magpie 端点…") || !tt.HasText("Sonnet") {
		t.Fatalf("加载完成后仍应显示普通供应商模型: %v", tt.Texts())
	}
}

// TestAgentMagpieEffectiveDefault 覆盖默认模型的计算规则：手设的模型仍被勾选时生效；
// 端点不存在或模型已取消勾选时回退到自动规则（第一个勾选非空的端点的第一项）；都没有时为空。
func TestAgentMagpieEffectiveDefault(t *testing.T) {
	bothSelected := func() AgentMagpieSetting {
		setting := agentMagpieTestSetting()
		setting.Endpoints[1].Selected = []string{"auto/opus"}
		return setting
	}
	cases := []struct {
		name       string
		setting    AgentMagpieSetting
		endpointID string
		modelID    string
		manual     bool
	}{
		{
			name: "手动设置的模型仍被勾选时生效",
			setting: func() AgentMagpieSetting {
				s := bothSelected()
				s.DefaultEndpointID = "us"
				s.DefaultModel = "auto/opus"
				return s
			}(),
			endpointID: "us",
			modelID:    "auto/opus",
			manual:     true,
		},
		{
			name: "手动设置的模型已取消勾选时回退自动",
			setting: func() AgentMagpieSetting {
				s := bothSelected()
				s.DefaultEndpointID = "jp"
				s.DefaultModel = "auto/haiku"
				return s
			}(),
			endpointID: "jp",
			modelID:    "auto/sonnet",
		},
		{
			name: "手动设置的端点不存在时回退自动",
			setting: func() AgentMagpieSetting {
				s := bothSelected()
				s.DefaultEndpointID = "ghost"
				s.DefaultModel = "auto/sonnet"
				return s
			}(),
			endpointID: "jp",
			modelID:    "auto/sonnet",
		},
		{
			name: "没有任何勾选时为空",
			setting: func() AgentMagpieSetting {
				s := bothSelected()
				s.Endpoints[0].Selected = nil
				s.Endpoints[1].Selected = nil
				s.DefaultEndpointID = "jp"
				s.DefaultModel = "auto/sonnet"
				return s
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpointID, modelID, manual := agentMagpieEffectiveDefault(tc.setting)
			if endpointID != tc.endpointID || modelID != tc.modelID || manual != tc.manual {
				t.Fatalf("生效默认模型 = (%q, %q, %v)，想要 (%q, %q, %v)",
					endpointID, modelID, manual, tc.endpointID, tc.modelID, tc.manual)
			}
		})
	}
}

// TestSettings_AgentMagpie_下拉框选择默认模型 覆盖：选择模型后保存默认并显示标签。
func TestSettings_AgentMagpie_下拉框选择默认模型(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Endpoints[1].Selected = []string{"auto/opus"}
	tt, _ := agentMagpieClaudeTester(t, fake, setting)

	waitFor(t, tt, func() bool {
		return tt.HasText("使用 claude 自己的默认") && tt.HasText("US Opus")
	})
	magpieTestSelectDefault(t, tt, "claude", "US Opus · US 网关")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieDefaultModelCalls()
		if len(calls) != 1 {
			return false
		}
		c := calls[0]
		return c.Agent == "claude" && c.EndpointID == "us" && c.ModelID == "auto/opus"
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("US Opus · US 网关") && magpieTestHasExactText(tt, "默认")
	})
}

// TestSettings_AgentMagpie_下拉框恢复自动 覆盖：选择自动清空手设默认。
func TestSettings_AgentMagpie_下拉框恢复自动(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Endpoints[1].Selected = []string{"auto/opus"}
	setting.DefaultEndpointID = "us"
	setting.DefaultModel = "auto/opus"
	tt, _ := agentMagpieClaudeTester(t, fake, setting)

	waitFor(t, tt, func() bool { return tt.HasText("US Opus · US 网关") })
	magpieTestSelectDefault(t, tt, "claude", "使用 claude 自己的默认")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieDefaultModelCalls()
		if len(calls) != 1 {
			return false
		}
		c := calls[0]
		return c.Agent == "claude" && c.EndpointID == "" && c.ModelID == ""
	})
	waitFor(t, tt, func() bool { return tt.HasText("使用 claude 自己的默认") })
}

// TestSettings_AgentMagpie_InjectDefaultLine 覆盖：运行时注入且没手设默认模型时，
// 显示「使用 claude 自己的默认」，页面上不再出现旧的「默认」徽标。
func TestSettings_AgentMagpie_InjectDefaultLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Endpoints[1].Selected = []string{"auto/opus"}
	tt, _ := agentMagpieClaudeTester(t, fake, setting)

	waitFor(t, tt, func() bool { return tt.HasText("使用 claude 自己的默认") })
	for _, text := range tt.Texts() {
		if text == "默认" {
			t.Fatalf("未手设默认模型时不该显示默认徽标: %v", tt.Texts())
		}
	}
	if _, ok := tt.Find("选择默认模型"); !ok {
		t.Fatalf("应提供默认模型下拉框: %v", tt.Texts())
	}
}

// TestSettings_AgentMagpie_GlobalAutoDefaultLine 覆盖：写全局配置时下拉框显示自动选出的模型。
func TestSettings_AgentMagpie_GlobalAutoDefaultLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Mode = "global"
	tt, _ := agentMagpieClaudeTester(t, fake, setting)

	waitFor(t, tt, func() bool { return tt.HasText("自动（JP Sonnet）") })
	if !magpieTestHasExactText(tt, "默认") {
		t.Fatalf("自动选出的模型应显示默认标签: %v", tt.Texts())
	}
}
