package mcodeui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func waitMagpie(t *testing.T, tt *ui.Tester, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待条件超时: %v", tt.Texts())
}

func magpiePageWithFake(t *testing.T, fake *FakeClient, connected bool) (*ui.Tester, *SettingsState) {
	t.Helper()
	if connected {
		fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "测试网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true}}
		fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&routeTestClient{FakeClient: fake}))
	tt := ui.NewTester(func(c *ui.Context) {
		id, _ := settingsTreeSheetState(st)
		switch id {
		case "magpie-gateway":
			sheetMagpieGateway.Body(c, st)
		case "magpie-personas":
			sheetMagpiePersonas.Body(c, st)
		case "provider":
			renderProviderSheetBody(c, st)
		default:
			renderPageMagpie(c, st)
		}
	}, 1000, 4000)
	waitMagpie(t, tt, func() bool { _, known := magpieConnected(st); return known })
	tt.Frame()
	return tt, st
}

func magpiePageTester(t *testing.T, connected bool) (*ui.Tester, *SettingsState, *FakeClient) {
	fake := NewFakeClient()
	tt, st := magpiePageWithFake(t, fake, connected)
	return tt, st, fake
}

// clickMagpieRoute 在 Magpie 页直接点选某个 agent 行内的路由分段。
func clickMagpieRoute(t *testing.T, tt *ui.Tester, st *SettingsState, agent, choice string) {
	t.Helper()
	name := map[string]string{"claude": "Claude", "codex": "Codex", "pi": "pi"}[agent]
	waitMagpie(t, tt, func() bool { _, known := magpieAgentRouteValue(st, agent); return known })
	var control ui.Rect
	var found bool
	waitMagpie(t, tt, func() bool { control, found = tt.Find(magpieAgentRouteLabel(name)); return found })
	clickMagpieRouteOption(t, tt, control, agent, choice)
}

func clickMagpieRouteOption(t *testing.T, tt *ui.Tester, control ui.Rect, agent, choice string) {
	t.Helper()
	options := magpieAgentRouteChoice(agent).options
	for i, option := range options {
		if option == choice {
			tt.ClickAt(control.X+control.W*(float32(i)+0.5)/float32(len(options)), control.Y+control.H/2)
			return
		}
	}
	t.Fatalf("智能体 %s 没有路由 %s", agent, choice)
}

func TestMagpieModelPickerVisibilityToggle(t *testing.T) {
	rows := pageMagpie.Sections[2].Rows
	if len(rows) != 2 || rows[0].ID != "row-magpie-model-picker-visible" || rows[1].ID != "row-magpie-model-matrix" {
		t.Fatalf("模型设置行错误: %#v", rows)
	}
	control, ok := rows[0].Control.(toggleControl)
	if !ok {
		t.Fatal("模型选择器可见性应使用开关")
	}
	tt, st, _ := magpiePageTester(t, true)
	st.mu.Lock()
	st.providerRows.available = map[string]bool{"claude": true, "codex": true, "pi": true}
	st.providerRows.loaded = true
	st.mu.Unlock()
	if control.b.value(&st.cfg) {
		t.Fatal("默认应隐藏 Magpie 模型")
	}
	waitMagpie(t, tt, func() bool {
		return tt.HasText("在模型选择器中显示") && tt.HasText("网关没有返回模型")
	})
	st.mu.Lock()
	st.agentMagpieFor("codex").loading = true
	st.mu.Unlock()
	tt.Frame()
	if tt.HasText("网关没有返回模型") {
		t.Fatal("加载中不应提示网关没有返回模型")
	}
	st.mu.Lock()
	st.agentMagpieFor("codex").loading = false
	st.mu.Unlock()
	if err := tt.Click("切换在模型选择器中显示"); err != nil {
		t.Fatal(err)
	}
	if st.cfg.Providers.HideMagpieModelsInPicker || !control.b.value(&st.cfg) {
		t.Fatal("开启开关后应显示模型")
	}
	tt.Frame()
	if !tt.HasText("网关没有返回模型") {
		t.Fatal("关闭开关后矩阵仍应渲染")
	}
}

func TestMagpieInstalledAgentSections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available map[string]bool
		codex     bool
	}{
		{name: "没有已安装的智能体", available: map[string]bool{"claude": false, "codex": false, "pi": false}},
		{name: "仅安装 Codex", available: map[string]bool{"claude": false, "codex": true, "pi": false}, codex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetAgentMagpie(AgentMagpieSetting{Agent: "codex", Endpoints: []PiMagpieEndpoint{{ID: "gw", Models: []PiMagpieModel{{ID: "model-a", Name: "测试模型"}}}}})
			tt, st := magpiePageWithFake(t, fake, true)
			st.mu.Lock()
			st.providerRows.available = tc.available
			st.providerRows.versions = nil
			st.providerRows.loaded = true
			st.mu.Unlock()
			if tc.codex {
				waitMagpie(t, tt, func() bool { return tt.HasText("切换 Codex 测试模型") })
				if countExactText(tt.Texts(), "Codex") != 2 {
					t.Fatalf("应显示 Codex 行和矩阵列: %v", tt.Texts())
				}
			} else {
				tt.Frame()
				// 内置引擎始终有一列，模型分组与矩阵不随 CLI 智能体的安装状态消失。
				for _, text := range []string{"智能体", "sec-magpie-agents"} {
					if countExactText(tt.Texts(), text) > 0 {
						t.Fatalf("没有已安装的 CLI 智能体时不应显示 %q: %v", text, tt.Texts())
					}
				}
				for _, text := range []string{"模型", "在模型选择器中显示", "mcode"} {
					if countExactText(tt.Texts(), text) < 1 {
						t.Fatalf("应显示 %q: %v", text, tt.Texts())
					}
				}
			}
			for _, text := range []string{"Claude", "pi", "Claude 路由", "pi 路由", "切换 Claude 测试模型", "切换 pi 测试模型"} {
				if countExactText(tt.Texts(), text) > 0 {
					t.Fatalf("未安装的智能体不应显示 %q: %v", text, tt.Texts())
				}
			}
		})
	}
}

func TestMagpieModelPickerVisibilitySearchIndex(t *testing.T) {
	for _, query := range []string{"在模型选择器中显示", "Magpie", "模型选择器", "显示"} {
		count := 0
		for _, hit := range matchSettingsItems(query) {
			if hit.Item.ID == "row-magpie-model-picker-visible" && hit.Item.Page == "magpie" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("搜索 %q 命中 %d 次，期望 1 次", query, count)
		}
	}
}

func TestSettingsMagpie_ThreeSections(t *testing.T) {
	if len(pageMagpie.Sections) != 3 || pageMagpie.Sections[0].ID != "sec-magpie-gateways" || pageMagpie.Sections[1].ID != "sec-magpie-agents" || pageMagpie.Sections[2].ID != "sec-magpie-models" {
		t.Fatal("Magpie 应分为网关、智能体、模型三个分组")
	}
	rows := pageMagpie.Sections[0].Rows
	if rows[len(rows)-1].ID != "row-magpie-personas" {
		t.Fatal("人设应位于网关组最后")
	}
	tt, _, _ := magpiePageTester(t, true)
	waitMagpie(t, tt, func() bool {
		return tt.HasText("网关") && tt.HasText("智能体") && tt.HasText("模型") && tt.HasText("人设") && tt.HasText("Claude") && tt.HasText("Codex") && tt.HasText("pi") && tt.HasText("添加网关")
	})
	if countExactText(tt.Texts(), "人设") != 1 {
		t.Fatalf("主页面人设标题应只出现一次: %v", tt.Texts())
	}
	order := []string{"网关", "测试网关", "添加网关", "人设", "智能体", "模型"}
	i := 0
	for _, text := range tt.Texts() {
		if i < len(order) && text == order[i] {
			i++
		}
	}
	if i != len(order) {
		t.Fatalf("页面分节顺序错误: %v", tt.Texts())
	}
}

func TestSettingsMagpie_DisconnectedOnlyForm(t *testing.T) {
	tt, _, _ := magpiePageTester(t, false)
	waitMagpie(t, tt, func() bool {
		return tt.HasText("接口地址（BaseURL）") && tt.HasText("密钥（API Key）") && tt.HasText("连接")
	})
	for _, text := range []string{"智能体", "人设", "Magpie 提供额度、人设和模型注入。先添加一个网关地址。"} {
		if tt.HasText(text) {
			t.Fatalf("未连接页面不应显示 %q: %v", text, tt.Texts())
		}
	}
}

func TestSettingsMagpie_RouteChoice(t *testing.T) {
	t.Run("claude直连", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetAgentMagpie(AgentMagpieSetting{Agent: "claude", Mode: "inject"})
		tt, st := magpiePageWithFake(t, fake, true)
		clickMagpieRoute(t, tt, st, "claude", "直连")
		waitMagpie(t, tt, func() bool {
			calls := fake.SetAgentMagpieModeCalls()
			return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Mode == "off"
		})
	})
	t.Run("失败回滚", func(t *testing.T) {
		fake := NewFakeClient()
		fake.setAgentMagpieModeErr = errors.New("保存失败")
		fake.SetAgentMagpie(AgentMagpieSetting{Agent: "claude", Mode: "inject"})
		tt, st := magpiePageWithFake(t, fake, true)
		clickMagpieRoute(t, tt, st, "claude", "直连")
		waitMagpie(t, tt, func() bool {
			value, known := magpieAgentRouteValue(st, "claude")
			return known && value == "仅 mcode" && tt.HasText("切换路由失败: 保存失败")
		})
	})
}

func TestSettingsMagpie_AddGatewayLink(t *testing.T) {
	tt, st, _ := magpiePageTester(t, true)
	if err := tt.Click("添加网关"); err != nil {
		t.Fatal(err)
	}
	if id, _ := settingsTreeSheetState(st); id != "magpie-gateway" {
		t.Fatalf("新建网关面板未打开: %q", id)
	}
	st.mu.Lock()
	gatewayID := st.magpieHub.sheetGatewayID
	st.mu.Unlock()
	if gatewayID != "" {
		t.Fatalf("新建网关不应携带 ID: %q", gatewayID)
	}
	waitMagpie(t, tt, func() bool { return tt.HasText("网关名称") && tt.HasText("保存") && tt.HasText("取消") })
}

// TestSettingsMagpie_AgentRowsSummary 覆盖：三个智能体各自一行，行内直接展示三段路由，
// 没有 Cline；点选分段即保存。
func TestSettingsMagpie_AgentRowsSummary(t *testing.T) {
	tt, st, fake := magpiePageTester(t, true)
	for agent, title := range map[string]string{"claude": "Claude", "codex": "Codex", "pi": "pi"} {
		agent, title := agent, title
		waitMagpie(t, tt, func() bool { _, known := magpieAgentRouteValue(st, agent); return known })
		if _, ok := tt.Find(magpieAgentRouteLabel(title)); !ok {
			t.Fatalf("缺少 %s 的路由控件: %v", title, tt.Texts())
		}
	}
	if tt.HasText("Cline") {
		t.Fatalf("页面不应再有 Cline 行: %v", tt.Texts())
	}
	for _, option := range []string{"直连", "仅 mcode", "全局"} {
		// 每个分段选项在 Texts() 里出现两次（无障碍标签 + 文本），三个智能体合计 6 次。
		if count := countExactText(tt.Texts(), option); count != 6 {
			t.Fatalf("三个智能体都应有 %s，实际 %d: %v", option, count, tt.Texts())
		}
	}
	clickMagpieRoute(t, tt, st, "claude", "直连")
	waitMagpie(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 1 && calls[0].Agent == "claude" && calls[0].Mode == agentMagpieModeOff
	})
}

// TestSettingsMagpie_AgentAndModelSectionsStayWithBrokenPi 覆盖：网关已配置但 pi 端点
// 请求失败（连接状态未知）时，智能体与模型分区仍可见。
func TestSettingsMagpie_AgentAndModelSectionsStayWithBrokenPi(t *testing.T) {
	fake := NewFakeClient()
	fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "测试网关", Enabled: true, Magpie: true, HasAPIKey: true}}
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw", Err: "网关不可用"}})
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&routeTestClient{FakeClient: fake}))
	tt := ui.NewTester(func(c *ui.Context) { renderPageMagpie(c, st) }, 1000, 4000)
	waitMagpie(t, tt, func() bool {
		connected, _ := magpieConnected(st)
		_, _, loaded, _, _ := magpieEndpointsFor(st, "pi")
		return !connected && loaded && magpiePageHasGateways(st)
	})
	tt.Frame()
	for _, text := range []string{"智能体", "模型", "Claude", "Codex", "pi"} {
		if !tt.HasText(text) {
			t.Fatalf("连接状态未知时仍应显示 %q: %v", text, tt.Texts())
		}
	}
	if tt.HasText("人设") {
		t.Fatalf("未连接时不应显示人设: %v", tt.Texts())
	}
}

// TestSettingsMagpie_KeptGlobalConfigText 覆盖：全局配置处于 kept 状态时的提示文案。
func TestSettingsMagpie_KeptGlobalConfigText(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAgentMagpie(AgentMagpieSetting{
		Agent:  "claude",
		Mode:   "global",
		Global: AgentMagpieGlobal{Supported: true, State: "kept"},
	})
	tt, _ := magpiePageWithFake(t, fake, true)
	waitMagpie(t, tt, func() bool { return tt.HasText("已保留当前配置；关闭时不会恢复原配置") })
}

func TestSettingsMagpie_GatewayRowsLayout(t *testing.T) {
	for _, long := range []bool{false, true} {
		t.Run(map[bool]string{false: "普通", true: "长名称与错误"}[long], func(t *testing.T) {
			fake := NewFakeClient()
			name, errorText := "测试网关", ""
			if long {
				name = strings.Repeat("很长的网关名称", 30)
				errorText = strings.Repeat("connection refused ", 30)
			}
			fake.customEndpoints = []CustomEndpoint{
				{ID: "gw", Name: name, Magpie: true, Enabled: true, HasAPIKey: true},
				{ID: "ok", Name: "可用网关", Magpie: true, Enabled: true, HasAPIKey: true},
			}
			fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw", Err: errorText}, {ID: "ok"}})
			tt, _ := magpiePageWithFake(t, fake, false)
			button, ok := tt.Find("打开 " + name)
			if !ok {
				t.Fatal("网关不可定位")
			}
			gateway, ok := tt.Find(name)
			if !ok {
				t.Fatal("网关名称不可定位")
			}
			persona, ok := tt.Find("人设")
			if !ok || gateway.X-persona.X < -1 || gateway.X-persona.X > 1 || gateway.X <= button.X {
				t.Fatalf("网关与人设标题应共用左内边距: gateway=%+v persona=%+v button=%+v", gateway, persona, button)
			}
			statusText := "已连接 · 0 个模型"
			if long {
				statusText = "连接失败"
				if !tt.HasText("连接失败：" + errorText) {
					t.Fatal("完整连接错误应保留")
				}
			}
			status, ok := tt.Find(statusText)
			if !ok || gateway.X+gateway.W > status.X || status.X+status.W > 1000 {
				t.Fatalf("网关名称应在状态左侧，状态应在窗口内: name=%+v status=%+v", gateway, status)
			}
			add, ok := tt.Find("添加网关")
			if !ok || add.X > gateway.X+1 {
				t.Fatalf("添加入口应靠左: %+v", add)
			}
		})
	}
}

func TestSettingsMagpie_GatewaySheet(t *testing.T) {
	tt, st, fake := magpiePageTester(t, true)
	if err := tt.Click("打开 测试网关"); err != nil {
		t.Fatal(err)
	}
	if id, _ := settingsTreeSheetState(st); id != "magpie-gateway" {
		t.Fatalf("面板未打开: %q", id)
	}
	waitMagpie(t, tt, func() bool { return tt.HasText("网关名称") && tt.HasText("保存") })
	if err := tt.Click("网关名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("新名称")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return len(fake.saveCustomEndpointCalls) == 1 })
	fake.mu.Lock()
	saved, key := fake.saveCustomEndpointCalls[0], fake.saveCustomEndpointKeys[0]
	fake.mu.Unlock()
	if !strings.Contains(saved.Name, "新名称") || key != "" {
		t.Fatalf("名称或密钥错误: 名称=%q 密钥=%q", saved.Name, key)
	}
	waitMagpie(t, tt, func() bool { id, _ := settingsTreeSheetState(st); return id == "" })
	// 重新打开一个面板，检查取消也能关闭。
	openMagpieGatewaySheet(st, "gw")
	tt.Frame()
	waitMagpie(t, tt, func() bool { return tt.HasText("取消") })
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool { id, _ := settingsTreeSheetState(st); return id == "" })
}
