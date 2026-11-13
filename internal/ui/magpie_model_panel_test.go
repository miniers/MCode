package mcodeui

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestMagpieModelPanelShowsOperationError(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	ps := newModelsPageState()
	ps.opErr = "模型操作失败"
	lib := &modelsLibraryView{ps: &ps}
	st.piMagpie.loaded = true
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "gateway"}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderMagpieModelPanel(c, st, lib, ProviderInfo{}, "pi", magpieModelPanelOptions{})
	}, 1000, 2000)
	if !tt.HasText(ps.opErr) {
		t.Fatal("面板没有显示模型操作错误")
	}
}

// magpieModelPanelTester 直接渲染指定 agent 的 Magpie 模型面板。
func magpieModelPanelTester(t *testing.T, fake *FakeClient, setting AgentMagpieSetting, catalog ProviderInfo) (*ui.Tester, *SettingsState) {
	t.Helper()
	magpieOwnModelGroups.Lock()
	delete(magpieOwnModelGroups.expanded, setting.Agent)
	magpieOwnModelGroups.Unlock()
	t.Cleanup(func() {
		magpieOwnModelGroups.Lock()
		delete(magpieOwnModelGroups.expanded, setting.Agent)
		magpieOwnModelGroups.Unlock()
	})
	testenv.TempHome(t)
	prevDelays := piMagpieCatalogRefreshDelays
	piMagpieCatalogRefreshDelays = nil
	t.Cleanup(func() { piMagpieCatalogRefreshDelays = prevDelays })

	fake.SetAgentMagpie(setting)
	if setting.Agent == "pi" {
		fake.SetPiMagpieEndpoints(setting.Endpoints)
	}
	providers := []ProviderInfo{catalog}
	fake.SetProviders(providers)
	chat := newChatState(fake)
	chat.providers = providers
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("magpie-panel-test")).Children(func() {
			renderMagpieAgentModels(c, st, modelsPageLibrary(c, st), catalog, setting.Agent)
		})
	}, 1000, 4000)
	waitModelsPageLoaded(t, tt, state)
	return tt, st
}

// magpieTestSelectDefault 通过下拉框选择默认模型。
func magpieTestSelectDefault(t *testing.T, tt *ui.Tester, _, label string) {
	t.Helper()
	clickWhenSettled(t, tt, "选择默认模型")
	waitFor(t, tt, func() bool { return magpieTestHasExactText(tt, label) })
	if err := tt.Click(label); err != nil {
		t.Fatalf("选择默认模型 %q 失败: %v", label, err)
	}
}

// magpieTestHasExactText 判断页面是否存在与 want 完全相同的文本。
func magpieTestHasExactText(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if text == want {
			return true
		}
	}
	return false
}

// magpieTestCountExactText 统计页面上与 want 完全相同的文本出现次数。
func magpieTestCountExactText(tt *ui.Tester, want string) int {
	count := 0
	for _, text := range tt.Texts() {
		if text == want {
			count++
		}
	}
	return count
}

// magpieTestHasModelIDs 判断已记录的一次保存是否命中 endpointID 且勾选集合与 want 完全一致。
func magpieTestHasModelIDs(calls []setAgentMagpieModelsCall, endpointID string, want []string) bool {
	for _, call := range calls {
		if call.EndpointID != endpointID || len(call.ModelIDs) != len(want) {
			continue
		}
		matched := true
		for i, id := range want {
			if call.ModelIDs[i] != id {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// TestMagpieModelPanelShowsOnlyEnabled 覆盖：已启用列表只显示勾选的模型，
// 44 个未勾选模型不会平铺到页面上。
func TestMagpieModelPanelShowsOnlyEnabled(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()

	models := make([]PiMagpieModel, 0, 44)
	for i := 1; i <= 44; i++ {
		models = append(models, PiMagpieModel{ID: fmt.Sprintf("auto/model-%d", i), Name: fmt.Sprintf("模型 %d", i)})
	}
	setting := AgentMagpieSetting{
		Agent: "claude",
		Mode:  "inject",
		Endpoints: []PiMagpieEndpoint{{
			ID:         "main",
			Name:       "主网关",
			ProviderID: "magpie-main",
			Models:     models,
			Selected:   []string{"auto/model-1"},
		}},
	}

	tt, _ := magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool {
		return tt.HasText("已启用（1）") && tt.HasText("模型 1")
	})
	if tt.HasText("收藏 模型 1") || tt.HasText("取消收藏 模型 1") {
		t.Fatal("Magpie 面板不应显示收藏按钮")
	}
	for _, text := range tt.Texts() {
		if text == "模型 2" || text == "模型 44" {
			t.Fatalf("未勾选的模型不应平铺在页面上，出现了 %q: %v", text, tt.Texts())
		}
	}
}

// TestMagpieModelPanelDisableUnchecks 覆盖：点击「停用 X」会把模型从勾选里去掉。
func TestMagpieModelPanelDisableUnchecks(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Endpoints[0].Selected = []string{"auto/sonnet", "auto/haiku"}

	tt, _ := magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return tt.HasText("停用 JP Haiku") })
	if err := tt.Click("停用 JP Haiku"); err != nil {
		t.Fatalf("点击停用按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return magpieTestHasModelIDs(fake.SetAgentMagpieModelsCalls(), "jp", []string{"auto/sonnet"})
	})
}

// TestMagpieModelPanel_默认模型下拉框保存 覆盖：claude 的默认模型下拉框选择后保存。
func TestMagpieModelPanel_默认模型下拉框保存(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Endpoints[0].Selected = []string{"auto/sonnet", "auto/haiku"}
	setting.Endpoints[1].Selected = []string{"auto/opus"}
	setting.DefaultEndpointID = "us"
	setting.DefaultModel = "auto/opus"

	tt, _ := magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return tt.HasText("US Opus · US 网关") })
	magpieTestSelectDefault(t, tt, "claude", "JP Sonnet · JP 网关")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieDefaultModelCalls()
		return len(calls) > 0 && calls[len(calls)-1].Agent == "claude" &&
			calls[len(calls)-1].EndpointID == "jp" && calls[len(calls)-1].ModelID == "auto/sonnet"
	})

}

// TestMagpieModelPanel_pi没有默认模型下拉框 覆盖：pi 只显示注入说明。
func TestMagpieModelPanel_pi没有默认模型下拉框(t *testing.T) {
	testenv.TempHome(t)
	piEndpoints := []PiMagpieEndpoint{{
		ID:         "jp",
		Name:       "JP 网关",
		ProviderID: "magpie-jp",
		Models:     []PiMagpieModel{{ID: "auto/sonnet", Name: "JP Sonnet"}},
		Selected:   []string{"auto/sonnet"},
	}}
	piTT, _ := magpieModelPanelTester(t, NewFakeClient(), AgentMagpieSetting{Agent: "pi", Endpoints: piEndpoints}, ProviderInfo{ID: "pi", Name: "Pi", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, piTT, func() bool {
		return piTT.HasText("已启用（1）") && piTT.HasText("已启用的模型会注入 mcode 内启动的 pi")
	})
	if _, ok := piTT.Find("选择默认模型"); ok {
		t.Fatal("pi 不应显示默认模型下拉框")
	}
}

// TestMagpieModelPanelAddPopover 覆盖：添加模型浮层列出未勾选模型、勾选会启用、搜索可过滤。
func TestMagpieModelPanelAddPopover(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()

	tt, _ := magpieModelPanelTester(t, fake, agentMagpieTestSetting(), ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return tt.HasText("已启用（1）") })
	clickWhenSettled(t, tt, "添加模型")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("JP Haiku")
		return ok
	})
	if err := tt.Click("JP Haiku"); err != nil {
		t.Fatalf("勾选 JP Haiku 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return magpieTestHasModelIDs(fake.SetAgentMagpieModelsCalls(), "jp", []string{"auto/sonnet", "auto/haiku"})
	})

	// 搜索 haiku 后，未勾选的 US Opus 应被过滤掉。
	if err := tt.Click("input-provider-models-search-magpie-add-claude"); err != nil {
		t.Fatalf("聚焦搜索框失败: %v", err)
	}
	tt.Type("haiku")
	waitFor(t, tt, func() bool { return !tt.HasText("US Opus") })
	if err := tt.Click("clear-provider-models-search-magpie-add-claude"); err != nil {
		t.Fatalf("清空搜索失败: %v", err)
	}
	if err := tt.Click("input-provider-models-search-magpie-add-claude"); err != nil {
		t.Fatalf("再次聚焦搜索框失败: %v", err)
	}
	tt.Type("zzz")
	waitFor(t, tt, func() bool { return tt.HasText("无匹配模型") })
}

// Magpie 面板里的同一个浮层也要能被 Esc 关掉，关掉后能再次打开。
func TestMagpieModelPanelAddPopoverClosesOnEscape(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	tt, _ := magpieModelPanelTester(t, fake, agentMagpieTestSetting(), ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return tt.HasText("已启用（1）") })
	searchKey := "input-provider-models-search-magpie-add-claude"

	clickWhenSettled(t, tt, "添加模型")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(searchKey)
		return ok
	})
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(searchKey)
		return !ok
	})
	clickWhenSettled(t, tt, "添加模型")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(searchKey)
		return ok
	})
}

// TestMagpieModelPanelOwnGroupPicksNativeModel 覆盖：自身配置分组显示目录里的模型。
func TestMagpieModelPanelOwnGroupPicksNativeModel(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	tt, _ := magpieModelPanelTester(t, fake, agentMagpieTestSetting(), ProviderInfo{
		ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "claude-native-x", Name: "Native X"}},
	})

	waitFor(t, tt, func() bool { return tt.HasText("claude 自身配置") && tt.HasText("1 个模型") })
	if tt.HasText("Native X") {
		t.Fatal("自身配置组默认折叠，不应显示组内模型")
	}
	clickWhenSettled(t, tt, "展开 claude 自身配置")
	waitFor(t, tt, func() bool { return tt.HasText("Native X") })
	clickWhenSettled(t, tt, "收起 claude 自身配置")
	waitFor(t, tt, func() bool { return !tt.HasText("Native X") })
}

// TestMagpieModelPanelOwnGroupExcludesMagpiePins 覆盖：Magpie 模型属于网关组，不属于自身配置组。
func TestMagpieModelPanelOwnGroupExcludesMagpiePins(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "magpie-jp/auto/sonnet"}
	if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	setting := agentMagpieTestSetting()
	catalog := ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{
		{ID: "claude-native-x", Name: "Native X"},
		{ID: "magpie-jp/auto/sonnet", Name: "JP Sonnet"},
	}}
	tt, _ := magpieModelPanelTester(t, fake, setting, catalog)
	waitFor(t, tt, func() bool { return tt.HasText("claude 自身配置") })
	clickWhenSettled(t, tt, "展开 claude 自身配置")
	waitFor(t, tt, func() bool {
		return tt.HasText("已启用（1）") && tt.HasText("Native X") && tt.HasText("JP Sonnet")
	})
	groups := buildMagpieModelGroups(catalog.Models, setting.Endpoints, setting.Agent)
	var inMagpie, inOwn bool
	for _, group := range groups {
		for _, row := range group.Rows {
			if row.Model.ID == pin.ModelID {
				if group.Endpoint == nil {
					inOwn = true
				} else {
					inMagpie = true
				}
			}
		}
	}
	if !inMagpie || inOwn {
		t.Fatalf("Magpie 模型应只属于网关组: inMagpie=%v, inOwn=%v", inMagpie, inOwn)
	}
}

// TestMagpieModelPanelFamily 覆盖：模型 ID 到系列名称的推导。
func TestMagpieModelPanelFamily(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"auto/claude-opus-5-5", "Claude"},
		{"auto/gpt-6-luna", "GPT"},
		{"auto/glm-5.3-flash", "GLM"},
		{"auto/mimo-v2.6-flash", "MiMo"},
		{"auto/unknown-model", "其他"},
	}
	for _, tc := range cases {
		if got := magpieModelFamily(tc.id); got != tc.want {
			t.Errorf("magpieModelFamily(%q) = %q，想要 %q", tc.id, got, tc.want)
		}
	}
}

// TestMagpieModelPanelRestoreAuto 覆盖：手动默认可恢复为自动。
func TestMagpieModelPanelRestoreAuto(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	setting := agentMagpieTestSetting()
	setting.Endpoints[1].Selected = []string{"auto/opus"}
	setting.DefaultEndpointID = "us"
	setting.DefaultModel = "auto/opus"

	tt, _ := magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return tt.HasText("US Opus · US 网关") && magpieTestHasExactText(tt, "默认") })
	magpieTestSelectDefault(t, tt, "claude", "使用 claude 自己的默认")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieDefaultModelCalls()
		return len(calls) > 0 && calls[len(calls)-1].EndpointID == "" && calls[len(calls)-1].ModelID == ""
	})
	waitFor(t, tt, func() bool { return tt.HasText("使用 claude 自己的默认") })
	if magpieTestHasExactText(tt, "默认") {
		t.Fatalf("页面上不应再出现「默认」徽标: %v", tt.Texts())
	}
}

// magpieTierTestSetting 构造 claude 写全局配置时的档位映射数据：
// opus、sonnet、haiku 指向 auto 端点的模型，fable 为空表示用 Claude Code 自带默认。
func magpieTierTestSetting(mode string) AgentMagpieSetting {
	return AgentMagpieSetting{
		Agent:  "claude",
		Mode:   mode,
		Global: AgentMagpieGlobal{Supported: true, State: "applied", Path: "/home/me/.claude/settings.json"},
		Endpoints: []PiMagpieEndpoint{{
			ID:         "auto",
			Name:       "自动网关",
			ProviderID: "magpie-auto",
			Models: []PiMagpieModel{
				{ID: "claude-opus-5-5", Name: "Opus 5.5"},
				{ID: "claude-sonnet-5-5", Name: "Sonnet 5.5"},
				{ID: "claude-haiku-5-5", Name: "Haiku 5.5"},
			},
			Selected: []string{"claude-sonnet-5-5"},
		}},
		Tiers: []AgentMagpieTier{
			{Tier: "opus", EndpointID: "auto", ModelID: "claude-opus-5-5"},
			{Tier: "sonnet", EndpointID: "auto", ModelID: "claude-sonnet-5-5"},
			{Tier: "haiku", EndpointID: "auto", ModelID: "claude-haiku-5-5"},
			{Tier: "fable"},
		},
	}
}

// TestMagpieModelPanelShowsTiersInGlobalMode 覆盖：写全局配置时列出各档位映射，
// 模型显示名称而不是 ID，空档位提示用 Claude Code 自带，haiku 追加后述说明。
func TestMagpieModelPanelShowsTiersInGlobalMode(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()

	tt, _ := magpieModelPanelTester(t, fake, magpieTierTestSetting("global"), ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool {
		return magpieTestHasExactText(tt, "档位映射") &&
			magpieTestHasExactText(tt, "/model opus") &&
			magpieTestHasExactText(tt, "/model fable")
	})
	for _, want := range []string{"Opus 5.5", "—（用 Claude Code 自带）", "也用于后台小任务"} {
		if !magpieTestHasExactText(tt, want) {
			t.Fatalf("档位映射应显示 %q: %v", want, tt.Texts())
		}
	}
	// opus 未勾选，只出现在档位映射行里；那里应是显示名而不是端点侧模型 ID。
	if magpieTestHasExactText(tt, "claude-opus-5-5") {
		t.Fatalf("档位映射应显示模型名而不是 ID: %v", tt.Texts())
	}
}

// TestMagpieModelPanelHidesTiersInInjectMode 覆盖：运行时注入不渲染档位映射。
func TestMagpieModelPanelHidesTiersInInjectMode(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()

	tt, _ := magpieModelPanelTester(t, fake, magpieTierTestSetting("inject"), ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return magpieTestHasExactText(tt, "已启用（1）") })
	if magpieTestHasExactText(tt, "档位映射") {
		t.Fatalf("运行时注入不应显示档位映射: %v", tt.Texts())
	}
}

// TestMagpieModelPanelPiHidesTiers 覆盖：pi 不渲染档位映射，即使后端给了 Tiers。
func TestMagpieModelPanelPiHidesTiers(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetAgentMagpie(AgentMagpieSetting{
		Agent:  "pi",
		Mode:   "global",
		Global: AgentMagpieGlobal{Supported: true, State: "applied"},
		Tiers:  []AgentMagpieTier{{Tier: "opus", EndpointID: "jp", ModelID: "auto/sonnet"}},
	})
	piEndpoints := []PiMagpieEndpoint{{
		ID:         "jp",
		Name:       "JP 网关",
		ProviderID: "magpie-jp",
		Models:     []PiMagpieModel{{ID: "auto/sonnet", Name: "JP Sonnet"}},
		Selected:   []string{"auto/sonnet"},
	}}

	tt, _ := magpieModelPanelTester(t, fake, AgentMagpieSetting{Agent: "pi", Mode: "global", Endpoints: piEndpoints, Global: AgentMagpieGlobal{Supported: true, State: "applied"}, Tiers: []AgentMagpieTier{{Tier: "opus", EndpointID: "jp", ModelID: "auto/sonnet"}}}, ProviderInfo{ID: "pi", Name: "Pi", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return magpieTestHasExactText(tt, "已启用（1）") })
	if magpieTestHasExactText(tt, "档位映射") {
		t.Fatalf("pi 不应显示档位映射: %v", tt.Texts())
	}
}

// TestMagpieModelPanelToggleReloadsTiers 覆盖：写全局配置时勾选变化会重读设置，
// 让后端按新勾选推导的档位映射跟着刷新。
func TestMagpieModelPanelToggleReloadsTiers(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()

	setting := magpieTierTestSetting("global")
	// 只勾选 Sonnet 时各档都跟随它，同时避免档位行与浮层勾选框出现同名文字。
	setting.Tiers = []AgentMagpieTier{
		{Tier: "opus", EndpointID: "auto", ModelID: "claude-sonnet-5-5"},
		{Tier: "sonnet", EndpointID: "auto", ModelID: "claude-sonnet-5-5"},
		{Tier: "haiku", EndpointID: "auto", ModelID: "claude-sonnet-5-5"},
		{Tier: "fable"},
	}

	tt, _ := magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "目录 Sonnet"}}})
	waitFor(t, tt, func() bool { return magpieTestHasExactText(tt, "档位映射") })
	before := len(fake.AgentMagpieCalls())
	clickWhenSettled(t, tt, "添加模型")
	// 档位行已不再显示 Haiku，此时「Haiku 5.5」唯一出现的位置是浮层里的勾选框。
	waitFor(t, tt, func() bool { return magpieTestCountExactText(tt, "Haiku 5.5") == 1 })
	if err := tt.Click("Haiku 5.5"); err != nil {
		t.Fatalf("勾选 Haiku 5.5 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return magpieTestHasModelIDs(fake.SetAgentMagpieModelsCalls(), "auto", []string{"claude-sonnet-5-5", "claude-haiku-5-5"})
	})
	waitFor(t, tt, func() bool { return len(fake.AgentMagpieCalls()) > before })
}
