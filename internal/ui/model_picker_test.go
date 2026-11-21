package mcodeui

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/magpieinject"
	"mcode/internal/testenv"
)

func TestModelPickerKeepsMagpieModels(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "magpie", Name: "网关", Enabled: true, Magpie: true},
		{ID: "second", Name: "网关", Enabled: true, Magpie: true},
		{ID: "normal", Name: "Magpie 普通端点", Enabled: true},
	}
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{
		"claude": {Mode: config.MagpieModeGlobal, Selections: []config.MagpieSelection{{EndpointID: "magpie", Models: []string{"shared"}}}},
		"codex":  {Mode: config.MagpieModeGlobal, Selections: []config.MagpieSelection{{EndpointID: "second", Models: []string{"shared"}}}},
	}
	targets := magpieinject.Targets(cfg, nil, nil)
	if len(targets) != 2 {
		t.Fatalf("网关数量 = %d", len(targets))
	}
	piModels := []ModelInfo{{ID: targets[0].ProviderID + "/one"}, {ID: targets[1].ProviderID + "/two"}, {ID: "native/model"}}
	claudeModels := []ModelInfo{{ID: "shared", Detail: "网关 · Magpie"}, {ID: "shared", Detail: "原生"}, {ID: "other", Detail: "网关 · Magpie"}}
	codexModels := []ModelInfo{{ID: "shared", Detail: "网关 · Magpie"}}
	providers := []ProviderInfo{
		{ID: "magpie", Models: []ModelInfo{{ID: "one"}}},
		{ID: "custom-second", Models: []ModelInfo{{ID: "two"}}},
		{ID: "custom-normal", Models: []ModelInfo{{ID: "ordinary"}}},
		{ID: "pi", Models: piModels},
		{ID: "claude", Models: claudeModels},
		{ID: "codex", Models: codexModels},
	}
	original := make([]ProviderInfo, len(providers))
	for i, provider := range providers {
		original[i] = provider
		original[i].Models = append([]ModelInfo(nil), provider.Models...)
	}
	got := filterModelPickerMagpie(providers, newModelPickerMagpieFilter(cfg))
	want := original
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("过滤结果 = %#v，期望 %#v", got, want)
	}
	if !reflect.DeepEqual(providers, original) {
		t.Fatal("过滤修改了原始模型列表")
	}
}

func modelPickerTestProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true},
				{ID: "claude-3-5-haiku", Name: "Claude 3.5 Haiku"},
			},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models: []ModelInfo{
				{ID: "o3-mini", Name: "o3-mini"},
			},
		},
	}
}

// 测试“默认”模型不再以裸的 Default/默认 作为模型名，而是显示实际解析出的模型名。
func TestModelDisplayIdentity_DefaultShowsResolvedName(t *testing.T) {
	providers := modelPickerTestProviders()

	cases := []struct {
		name         string
		provider     string
		model        string
		wantName     string
		wantIsDflt   bool
		wantProvName string
	}{
		{"空模型 ID 解析默认模型", "claude", "", "Claude 3.7 Sonnet", true, "Claude"},
		{"标记为默认的模型 ID", "claude", "claude-3-7-sonnet", "Claude 3.7 Sonnet", true, "Claude"},
		{"显式非默认模型", "claude", "claude-3-5-haiku", "Claude 3.5 Haiku", false, "Claude"},
		{"无默认模型的 provider 回退首个模型", "codex", "", "o3-mini", true, "Codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, isDefault, provName := modelDisplayIdentity(providers, tc.provider, tc.model)
			if name != tc.wantName || isDefault != tc.wantIsDflt || provName != tc.wantProvName {
				t.Fatalf("modelDisplayIdentity(%q, %q) = (%q, %v, %q), want (%q, %v, %q)",
					tc.provider, tc.model, name, isDefault, provName, tc.wantName, tc.wantIsDflt, tc.wantProvName)
			}
			if got := modelPickerDisplayName(providers, tc.provider, tc.model); got != tc.wantName {
				t.Fatalf("modelPickerDisplayName(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.wantName)
			}
			if got := currentModelDisplayName(providers, tc.provider, tc.model); got != tc.wantName {
				t.Fatalf("currentModelDisplayName(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.wantName)
			}
		})
	}
}

// 测试解析不出模型名时显示“默认模型”，不出现裸的“默认”。
func TestModelDisplayIdentity_UnresolvedDefault(t *testing.T) {
	if got := modelPickerDisplayName(nil, "", ""); got != "默认模型" {
		t.Fatalf("empty selection = %q, want 默认模型", got)
	}
	providers := []ProviderInfo{{ID: "claude", Name: "Claude"}}
	if got := modelPickerDisplayName(providers, "claude", ""); got != "默认模型" {
		t.Fatalf("provider without models = %q, want 默认模型", got)
	}
	if got := modelPickerDisplayName(providers, "claude", "unknown-model"); got != "unknown-model" {
		t.Fatalf("unresolved explicit model = %q, want unknown-model", got)
	}
}

// 测试胶囊文案（签名不变）：默认模型带出实际模型名，再拼接首字母大写的英文推理强度。
func TestModelCapsuleLabel_DefaultShowsResolvedName(t *testing.T) {
	providers := modelPickerTestProviders()
	cases := []struct {
		provider, model, effort string
		want                    string
	}{
		{"claude", "", "high", "Claude 3.7 Sonnet High"},
		{"claude", "claude-3-7-sonnet", "", "Claude 3.7 Sonnet"},
		{"claude", "claude-3-5-haiku", "low", "Claude 3.5 Haiku Low"},
	}
	for _, tc := range cases {
		got := modelCapsuleLabel(modelPickerDisplayName(providers, tc.provider, tc.model), tc.effort)
		if got != tc.want {
			t.Fatalf("capsule for (%q, %q, %q) = %q, want %q", tc.provider, tc.model, tc.effort, got, tc.want)
		}
	}
}

// 测试卡片说明行模型名为“<模型名>”，默认模型显示为“Claude 3.7 Sonnet”。
func TestModelPickerModelTitle(t *testing.T) {
	providers := modelPickerTestProviders()
	lead := modelPickerSideInfo(ModelPickerProps{Providers: providers, Selection: ModelSelection{Provider: "claude"}}, ModelPickerSideLead)
	if got := modelPickerModelTitle(providers, lead); got != "Claude 3.7 Sonnet" {
		t.Fatalf("default lead title = %q", got)
	}
	lead = modelPickerSideInfo(ModelPickerProps{Providers: providers, Selection: ModelSelection{Provider: "claude", Model: "claude-3-5-haiku"}}, ModelPickerSideLead)
	if got := modelPickerModelTitle(providers, lead); got != "Claude 3.5 Haiku" {
		t.Fatalf("explicit lead title = %q", got)
	}
}

// 测试配对时卡片说明行：主导侧 caption 只含主导模型名，不含“+”与 Provider 名；执行侧 caption 等于执行模型名。
func TestModelPickerSideCaptionPaired(t *testing.T) {
	providers := modelPickerTestProviders()
	props := ModelPickerProps{
		Mode:      ModelPickerSwarm,
		SwarmOn:   true,
		Providers: providers,
		Selection: ModelSelection{Provider: "codex", Model: "o3-mini"},
		Bees:      ModelSelection{Provider: "claude", Model: "claude-3-5-haiku"},
	}
	leadCaption := modelPickerSideCaption(props, ModelPickerSideLead)
	if leadCaption != "o3-mini" {
		t.Fatalf("lead caption = %q, want %q", leadCaption, "o3-mini")
	}
	if strings.Contains(leadCaption, "+") || strings.Contains(leadCaption, "Codex") {
		t.Fatalf("lead caption %q should not contain '+' or provider name", leadCaption)
	}
	beesCaption := modelPickerSideCaption(props, ModelPickerSideBees)
	if beesCaption != "Claude 3.5 Haiku" {
		t.Fatalf("bees caption = %q, want %q", beesCaption, "Claude 3.5 Haiku")
	}
}

// 测试禁用原因：整体禁用优先，其次按侧别给出保存中提示，未禁用返回空串。
func TestModelSideDisabledReason(t *testing.T) {
	if got := modelSideDisabledReason(false, false); got != "" {
		t.Fatalf("enabled side = %q, want empty", got)
	}
	if got := modelSideDisabledReason(true, false); got != "操作进行中，暂不可修改模型设置" {
		t.Fatalf("overall disabled = %q", got)
	}
	leadDisabled := effortCardLockedReason(ModelPickerProps{LeadDisabled: true}, ModelPickerSideLead)
	beesDisabled := effortCardLockedReason(ModelPickerProps{LeadDisabled: true}, ModelPickerSideBees)
	if leadDisabled != "正在保存该侧模型偏好，暂不可修改" || beesDisabled != "" {
		t.Fatalf("per-side reason = (%q, %q)", leadDisabled, beesDisabled)
	}
}

// 测试分段标签：档位用英文名（与 modelEffortTitle 一致），档位为空时只显示角色名，执行侧另给“关闭”态。
func TestSwarmSideTabLabel_BeesOff(t *testing.T) {
	if got := swarmSideTabLabel("bees", "high", true); got != "子 · 关闭" {
		t.Fatalf("bees off = %q", got)
	}
	if got := swarmSideTabLabel("bees", "high", false); got != "子 · High" {
		t.Fatalf("bees on = %q", got)
	}
	if got := swarmSideTabLabel("bees", "", false); got != "子" {
		t.Fatalf("bees without effort = %q", got)
	}
	if got := swarmSideTabLabel("lead", "medium", false); got != "主 · Medium" {
		t.Fatalf("lead with effort = %q", got)
	}
	if got := swarmSideTabLabel("lead", "", false); got != "主" {
		t.Fatalf("lead without effort = %q", got)
	}
}

// 测试分段标签用的档位：未选档位时回退模型默认档位，选了档位则取所选档位。
func TestModelPickerSideEffort(t *testing.T) {
	side := modelPickerSide{
		Model:     ModelInfo{Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high"},
		Selection: ModelSelection{},
	}
	if got := modelPickerSideEffort(side); got != "high" {
		t.Fatalf("fallback to default effort = %q, want high", got)
	}
	side.Selection.Effort = "low"
	if got := modelPickerSideEffort(side); got != "low" {
		t.Fatalf("selected effort = %q, want low", got)
	}
	side.Model.Efforts = nil
	side.Model.DefaultEffort = "medium"
	side.Selection.Effort = ""
	if got := modelPickerSideEffort(side); got != "medium" {
		t.Fatalf("no effort list = %q, want medium", got)
	}
}

// 测试卡片渲染：卡片说明行展示模型名，档位名以文字呈现。
func TestRenderModelPickerCard_DefaultIdentityVisible(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(func() {
		modelPickerStatesMu.Lock()
		modelPickerStates = make(map[string]*ModelPickerState)
		modelPickerStatesMu.Unlock()
	})

	providers := modelPickerTestProviders()
	providers[0].Models[0].Efforts = []string{"low", "medium", "high", "xhigh", "max"}
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "t-model-picker-",
		ScopeKey:  "t-model-picker",
		State:     &ModelPickerState{},
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet", Effort: "high"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(300).Children(func() {
			renderModelPickerCard(c, props, props.State, ModelPickerSideLead)
		})
	}, 320, 640)
	tt.Frame()

	for _, want := range []string{"Claude 3.7 Sonnet"} {
		if !tt.HasText(want) {
			t.Fatalf("expected %q in card, got %v", want, tt.Texts())
		}
	}
	// 滑块胶囊显示英文短档位名；旧版“当前：”行与轨道下方档位列表不再出现，单卡也不渲染独立的槽位档位文字。
	info, ok := tt.Access("推理强度")
	if !ok || info.Value != "High" {
		t.Fatalf("expected slider value High, got (%+v, %v)", info, ok)
	}
	if tt.HasText("当前：") || tt.HasText("低") || tt.HasText("超高") || tt.HasText("Low") || tt.HasText("High") {
		t.Fatalf("unexpected tier list text in card, got %v", tt.Texts())
	}
}

func TestModelPickerFlyoutSelection(t *testing.T) {
	testenv.TempHome(t)
	entry, capture := setupPopoverFlyoutTest(t)
	providers := modelPickerTestProviders()
	providers[0].Models[0].SupportsFast = true
	state := &ModelPickerState{}
	fast, picked := false, false
	props := ModelPickerProps{KeyPrefix: "flyout-model-", ScopeKey: "flyout-model", State: state, Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"}, CapsuleLabel: "Claude",
		OnToggleFast: func(ModelPickerSide) { fast = true }, OnPickModel: func(ModelPickerSide, ProviderInfo, ModelInfo) { picked = true; state.Reset() }}
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		props.Invalidate = c.Services().Invalidate
		renderModelPicker(c, props)
	}, 400, 500)
	if err := host.Click("选择模型"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Width != 368 || capture.options[0].Placement != mygo.PlacementTop {
		t.Fatalf("窗口参数: %+v", capture.options)
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "flyout-model-model-popover"), 368, 600)
	if err := menu.Click("快速模式"); err != nil {
		t.Fatal(err)
	}
	if !fast || !state.MenuOpen {
		t.Fatal("快速模式应保持浮层打开")
	}
	if !menu.HasText("选择其他模型") || menu.HasText("model-list-search-input") {
		t.Fatalf("首屏应为强度卡片: %v", menu.Texts())
	}
	if err := menu.Click("选择其他模型"); err != nil {
		t.Fatal(err)
	}
	// 换页入场动画还没跑完时内容仍在偏移中：推进时钟等它落位再点搜索框。
	for i := 0; i < 20; i++ {
		menu.Advance(50 * time.Millisecond)
		menu.Frame()
		if menu.Click("model-list-search-input") == nil {
			break
		}
	}
	menu.Type("Sonnet")
	if state.searchQuery != "Sonnet" {
		t.Fatalf("搜索输入失败: %q", state.searchQuery)
	}
	if !menu.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("缺少模型: %v", menu.Texts())
	}
	if menu.HasText("Claude 3.5 Haiku") {
		t.Fatalf("搜索结果不应包含未匹配的模型: %v", menu.Texts())
	}
	if err := menu.Click("Claude 3.7 Sonnet"); err != nil {
		t.Fatal(err)
	}
	host.Frame()
	if !picked || state.MenuOpen || len(capture.destroyedWindows) != 1 {
		t.Fatal("选模型后浮层未关闭")
	}
}

// 测试模型行：默认行显示实际解析出的模型名，不再带“默认”徽章。
func TestRenderModelRow_DefaultIdentity(t *testing.T) {
	testenv.TempHome(t)
	providers := modelPickerTestProviders()
	props := ModelPickerProps{KeyPrefix: "t-row-"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(300).Children(func() {
			renderModelPickerListRow(c, props, modelRowOptions{
				Provider: providers[0],
				Model:    providers[0].Models[0],
			})
		})
	}, 320, 160)
	tt.Frame()

	if !tt.HasText("Claude 3.7 Sonnet") || tt.HasText("默认") {
		t.Fatalf("expected default row label and no badge, got %v", tt.Texts())
	}
}

// 测试 resolveModelPickerSelection 在 provider 为空但指定了具体模型时，能从 providers 自动反推 provider。
func TestResolveModelPickerSelection_DeduceProviderFromModel(t *testing.T) {
	providers := modelPickerTestProviders()

	// 传入空 provider 但有模型 ID，应自动推断为该模型所属的 provider
	sel := resolveModelPickerSelection(providers, ModelSelection{Model: "claude-3-5-haiku"})
	if sel.Provider != "claude" {
		t.Fatalf("expected provider claude, got %q", sel.Provider)
	}

	selCodex := resolveModelPickerSelection(providers, ModelSelection{Model: "o3-mini"})
	if selCodex.Provider != "codex" {
		t.Fatalf("expected provider codex, got %q", selCodex.Provider)
	}
}

// 测试恢复记忆时放行已固定的自定义模型：目录外的 ID 因 pin 保留，未固定时仍回退目录首项。
func TestProviderHasPinnedModelAllowsCustomModel(t *testing.T) {
	resetLastSelectionMergeState(t)
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet"}}}}
	pins := []ModelPin{{Provider: "claude", ModelID: "claude-custom-x"}}
	if providerHasModel(providers[0], "claude-custom-x") {
		t.Fatal("测试前提错误：claude-custom-x 不应在目录里")
	}
	if !providerHasPinnedModel("claude", "claude-custom-x", pins) {
		t.Fatal("已固定的自定义模型应被 providerHasPinnedModel 识别")
	}
	if !modelUsable(providers[0], "claude-custom-x", pins) {
		t.Fatal("已固定的自定义模型应视为可用")
	}
	if modelUsable(providers[0], "claude-custom-x", nil) {
		t.Fatal("未固定的目录外模型不应视为可用")
	}
	if modelUsable(providers[0], "claude-custom-x", []ModelPin{{Provider: "codex", ModelID: "claude-custom-x"}}) {
		t.Fatal("其它 provider 的 pin 不应放行")
	}

	last := ModelSelection{Provider: "claude", Model: "claude-custom-x"}
	setLastModelSelection(&last)
	defer setLastModelSelection(nil)

	if got := resolveModelPickerSelectionForProject(providers, ModelSelection{Provider: "claude"}, "", pins); got.Model != "claude-custom-x" {
		t.Fatalf("已固定的自定义模型应被保留，got %+v", got)
	}
	if got := resolveModelPickerSelectionForProject(providers, ModelSelection{Provider: "claude"}, "", nil); got.Model != "sonnet" {
		t.Fatalf("未固定的目录外模型应回退目录首项，got %+v", got)
	}

	pairs := []SwarmPair{{ID: "pinned-lead", LeadProvider: "claude", LeadModel: "claude-custom-x", WorkerProvider: "claude", WorkerModel: "sonnet"}}
	if ready := readySwarmPairs(config.Config{}, pairs, providers, pins); len(ready) != 1 || ready[0].ID != "pinned-lead" {
		t.Fatalf("pin 应让配对保留，got %+v", ready)
	}
	if ready := readySwarmPairs(config.Config{}, pairs, providers, nil); len(ready) != 0 {
		t.Fatalf("未固定时配对应按原逻辑被过滤，got %+v", ready)
	}
}

// 卡片页点击顶栏「编排开关」异步开启编排：设置拉回窗口，待 SwarmOn 生效后子代理槽展开。
func TestModelPickerMenuBody_KeepsBeesTabUntilSwarmOn(t *testing.T) {
	state := &ModelPickerState{MenuOpen: true}
	swarmOn := false
	toggled := false
	props := func() ModelPickerProps {
		return ModelPickerProps{
			Mode:          ModelPickerSwarm,
			KeyPrefix:     "t-",
			ScopeKey:      "t1",
			State:         state,
			Providers:     modelPickerTestProviders(),
			Selection:     ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
			SwarmOn:       swarmOn,
			OnToggleSwarm: func(on bool) { toggled = on },
		}
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerMenuBody(c, props(), state)
	}, modelPickerPanelWidth, 400)
	tt.Frame()
	if magpieTestHasExactText(tt, "子") {
		t.Fatalf("Swarm 关闭时不应渲染子代理槽: %v", tt.Texts())
	}
	if err := tt.Click("编排开关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !toggled {
		t.Fatal("点编排开关应请求开启编排")
	}
	if !state.reopenAfterSwarm {
		t.Fatal("点编排开关开启时应记录 reopenAfterSwarm 拉回意图")
	}
	swarmOn = true
	tt.Frame()
	if !magpieTestHasExactText(tt, "子") {
		t.Fatalf("Swarm 生效后应展示子代理槽: %v", tt.Texts())
	}
}

// pi 这类 CLI 的模型 id 本身带斜杠（目录里是 magpie/jp/group/…），会话里可能存着去掉
// provider 前缀的裸 id。两种形状都要查到目录行，否则推理强度卡片会退化成
// “该模型未提供可调的推理强度”，用户看不到滑杆。
func TestModelPickerCard_PiSlashModelIDKeepsEfforts(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(func() {
		modelPickerStatesMu.Lock()
		modelPickerStates = make(map[string]*ModelPickerState)
		modelPickerStatesMu.Unlock()
	})

	providers := []ProviderInfo{{
		ID:        "pi",
		Name:      "Pi",
		Available: true,
		Models: []ModelInfo{{
			ID:            "magpie/jp/group/auto-deepseek-v4-1-flash",
			Name:          "Deepseek V4.1 Flash · jp",
			IsDefault:     true,
			Efforts:       []string{"low", "high", "max"},
			DefaultEffort: "low",
		}},
	}}

	full := "magpie/jp/group/auto-deepseek-v4-1-flash"
	for _, model := range []string{full, "jp/group/auto-deepseek-v4-1-flash"} {
		t.Run(model, func(t *testing.T) {
			props := ModelPickerProps{
				Mode:      ModelPickerSimple,
				KeyPrefix: "t-pi-effort-",
				ScopeKey:  "t-pi-effort",
				State:     &ModelPickerState{},
				Providers: providers,
				Selection: ModelSelection{Provider: "pi", Model: model},
			}

			side := modelPickerSideInfo(props, ModelPickerSideLead)
			if got := strings.Join(side.Model.Efforts, ","); got != "low,high,max" {
				t.Fatalf("模型 %q 的推理强度 = %q，期望 low,high,max", model, got)
			}
			if side.Model.Name != "Deepseek V4.1 Flash · jp" || side.Model.DefaultEffort != "low" {
				t.Fatalf("模型 %q 未解析到目录行: %+v", model, side.Model)
			}

			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(300).Children(func() {
					renderModelPickerCard(c, props, props.State, ModelPickerSideLead)
				})
			}, 320, 640)
			tt.Frame()
			info, ok := tt.Access("推理强度")
			if !ok || info.Value != "Low" {
				t.Fatalf("模型 %q 的推理强度滑块默认档位应为 Low，实际 (%+v, %v)，texts=%v", model, info, ok, tt.Texts())
			}
		})
	}
}

// 供应商关闭后，模型选择器一级页不再显示它固定的模型；配置里的 ModelList 保持原样，
// 供应商重新启用后条目自动回来。
func TestModelPickerPinnedEntriesSkipDisabledProvider(t *testing.T) {
	providers := modelPickerTestProviders()
	pins := []ModelPin{
		{Provider: "claude", ModelID: "claude-3-7-sonnet"},
		{Provider: "codex", ModelID: "o3-mini"},
	}
	props := ModelPickerProps{
		Providers:       providers,
		Pins:            pins,
		Selection:       ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		ProviderEnabled: map[string]bool{"claude": true, "codex": false},
	}

	entries := modelPickerPinnedEntries(
		props.Pins,
		filterProvidersByEnabled(props.Providers, props.ProviderEnabled),
	)
	if len(entries) != 1 || entries[0].Provider.ID != "claude" {
		t.Fatalf("关闭 codex 后一级页应只剩 claude，实际 %+v", entries)
	}
	// 只过滤展示，持久化的固定模型列表不受影响。
	if len(props.Pins) != 2 {
		t.Fatalf("ModelList 被改动了：%+v", props.Pins)
	}
	visibleProviders := filterProvidersByEnabled(props.Providers, modelPickerDisabledProviders(props, props.Selection.Provider, props.Bees.Provider))
	if len(visibleProviders) != 1 || visibleProviders[0].ID != "claude" {
		t.Fatalf("关闭 codex 后分组列表应只剩 claude，实际 %+v", visibleProviders)
	}

	// 重新启用后固定模型重新出现。
	props.ProviderEnabled["codex"] = true
	entries = modelPickerPinnedEntries(
		props.Pins,
		filterProvidersByEnabled(props.Providers, props.ProviderEnabled),
	)
	if len(entries) != 2 {
		t.Fatalf("重新启用 codex 后应显示两个固定模型，实际 %+v", entries)
	}
}

// 当前会话正在使用的供应商即使已关闭也不能被隐藏，否则已有会话的选择器会空掉。
func TestModelPickerKeepsDisabledProviderInUse(t *testing.T) {
	providers := modelPickerTestProviders()
	pins := []ModelPin{{Provider: "codex", ModelID: "o3-mini"}}
	props := ModelPickerProps{
		Providers:       providers,
		Pins:            pins,
		Selection:       ModelSelection{Provider: "codex", Model: "o3-mini"},
		ProviderEnabled: map[string]bool{"codex": false},
	}

	disabled := modelPickerDisabledProviders(props, props.Selection.Provider, props.Bees.Provider)
	if len(disabled) != 0 {
		t.Fatalf("当前使用的 provider 不应进入隐藏集合：%+v", disabled)
	}
	visibleProviders := filterProvidersByEnabled(props.Providers, disabled)
	if len(visibleProviders) != 2 {
		t.Fatalf("当前会话使用的 codex 应保留在分组中，实际 %+v", visibleProviders)
	}
	entries := modelPickerPinnedEntries(props.Pins, filterProvidersByEnabled(visibleProviders, props.ProviderEnabled))
	if len(entries) != 0 {
		t.Fatalf("已关闭的 codex 不应显示在精选中，实际 %+v", entries)
	}
}

func TestModelPickerPinnedEntriesRequireExactCatalogID(t *testing.T) {
	providers := []ProviderInfo{{ID: "pi", Models: []ModelInfo{{ID: "magpie/example", Name: "Magpie"}}}}
	pins := []ModelPin{{Provider: "pi", ModelID: "example"}}
	if entries := modelPickerPinnedEntries(pins, providers); len(entries) != 0 {
		t.Fatalf("目录中没有精确 ID 时不应显示收藏，实际 %+v", entries)
	}
	providers[0].Models = append(providers[0].Models, ModelInfo{ID: "example", Name: "Example"})
	entries := modelPickerPinnedEntries(pins, providers)
	if len(entries) != 1 || entries[0].Model.Name != "Example" {
		t.Fatalf("模型重新进入目录后应显示收藏，实际 %+v", entries)
	}
	if len(pins) != 1 || pins[0].ModelID != "example" {
		t.Fatalf("收藏数据不应被修改，实际 %+v", pins)
	}
}

func TestModelPickerListPageSkipsMissingPinnedModel(t *testing.T) {
	for _, present := range []bool{false, true} {
		name := "目录中缺失"
		if present {
			name = "重新进入目录"
		}
		t.Run(name, func(t *testing.T) {
			providers := modelPickerTestProviders()
			if present {
				providers[0].Models = append(providers[0].Models, ModelInfo{ID: "magpie/old", Name: "Magpie Old"})
			}
			props := ModelPickerProps{
				KeyPrefix: "t-pinned-catalog-", ScopeKey: "t-pinned-catalog-" + name,
				Providers: providers,
				Pins: []ModelPin{
					{Provider: "claude", ModelID: "claude-3-7-sonnet"},
					{Provider: "claude", ModelID: "magpie/old"},
				},
			}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(300).Children(func() {
					renderModelPickerListPage(c, props, &ModelPickerState{}, ModelPickerSideLead)
				})
			}, 320, 640)
			tt.Frame()
			// 目录缺失时列表里不应出现该收藏行。
			if !tt.HasText("Claude 3.7 Sonnet") || tt.HasText("Magpie Old") != present {
				t.Fatalf("一级页收藏显示不符合目录状态：%v", tt.Texts())
			}
		})
	}
}

// 列表页搜索词匹配：空查询全部命中；模型名大小写不敏感；provider 名与模型 ID 也能命中；不相关词不命中。
func TestModelPickerQueryMatch(t *testing.T) {
	prov := ProviderInfo{
		ID:   "claude",
		Name: "Claude Code",
		Models: []ModelInfo{
			{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", Detail: "日常编码默认档", IsDefault: true},
		},
	}
	m := prov.Models[0]

	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{name: "空查询", query: "", want: true},
		{name: "空白查询", query: "   ", want: true},
		{name: "按模型名大小写不敏感", query: "SONNET", want: true},
		{name: "按 provider 名", query: "claude code", want: true},
		{name: "按模型 ID", query: "3-7-sonnet", want: true},
		{name: "不匹配", query: "gpt-5", want: false},
	}
	for _, tc := range cases {
		if got := modelPickerQueryMatch(prov, m, tc.query); got != tc.want {
			t.Fatalf("%s：查询 %q 期望 %v，实际 %v", tc.name, tc.query, tc.want, got)
		}
	}
}

// 分组折叠状态按 scopeKey/side/provider 分别记录，互不影响，且可往返切换。
func TestModelPanelProviderCollapsed(t *testing.T) {
	const scope = "t-collapse"
	t.Cleanup(func() {
		setModelPanelProviderCollapsed(scope, "lead", "claude", false)
		setModelPanelProviderCollapsed(scope, "bees", "claude", false)
		setModelPanelProviderCollapsed(scope, "lead", "codex", false)
	})

	if isModelPanelProviderCollapsed(scope, "lead", "claude") {
		t.Fatalf("初始状态应为展开")
	}
	setModelPanelProviderCollapsed(scope, "lead", "claude", true)
	if !isModelPanelProviderCollapsed(scope, "lead", "claude") {
		t.Fatalf("设置折叠后应读到折叠状态")
	}
	if isModelPanelProviderCollapsed(scope, "bees", "claude") {
		t.Fatalf("另一侧的折叠状态不应受影响")
	}
	if isModelPanelProviderCollapsed(scope, "lead", "codex") {
		t.Fatalf("另一个 provider 的折叠状态不应受影响")
	}
	setModelPanelProviderCollapsed(scope, "lead", "claude", false)
	if isModelPanelProviderCollapsed(scope, "lead", "claude") {
		t.Fatalf("取消折叠后应回到展开状态")
	}
}

// TestModelCapsuleTwoLine 验证编组胶囊的双行布局：主代理在上、子代理在下，且不出现 · 分隔符。
func TestModelCapsuleTwoLine(t *testing.T) {
	testenv.TempHome(t)
	providers := capsuleTestProviders()

	render := func(props ModelPickerProps) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(420).Children(func() {
				renderModelPicker(c, props)
				ui.Text(c, "胶囊高度探针")
			})
		}, 460, 320)
		tt.Frame()
		return tt
	}

	tt := render(capsuleTestProps(providers))
	if !tt.HasText("Opus 5.5") || !tt.HasText("V4.1 Flash") {
		t.Fatalf("双行胶囊应同时显示主代理与子代理短名, got %v", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if text == "·" {
			t.Fatalf("双行布局不应出现 · 分隔符, got %v", tt.Texts())
		}
	}

	leadRect, ok := tt.Find("Opus 5.5")
	if !ok {
		t.Fatalf("未找到主代理短名, got %v", tt.Texts())
	}
	beeRect, ok := tt.Find("V4.1 Flash")
	if !ok {
		t.Fatalf("未找到子代理短名, got %v", tt.Texts())
	}
	if beeRect.Y <= leadRect.Y {
		t.Fatalf("子代理应排在主代理下一行: lead.y=%v bee.y=%v", leadRect.Y, beeRect.Y)
	}
	// 探针紧贴胶囊下方，它的 Y 就是胶囊占位高度，不超过 ControlMD（与发送按钮同高）。
	probeRect, ok := tt.Find("胶囊高度探针")
	if !ok {
		t.Fatalf("未找到胶囊高度探针, got %v", tt.Texts())
	}
	if probeRect.Y > ControlMD {
		t.Fatalf("双行胶囊占位高度应不超过 ControlMD（与发送按钮同高）: got %v", probeRect.Y)
	}

	hidden := capsuleTestProps(providers)
	hidden.HideBeeName = true
	if tt := render(hidden); tt.HasText("V4.1 Flash") {
		t.Fatalf("窄宽度时应隐藏子代理短名, got %v", tt.Texts())
	}

	follow := capsuleTestProps(providers)
	follow.Bees = ModelSelection{}
	if tt := render(follow); tt.HasText("V4.1 Flash") {
		t.Fatalf("子代理跟随主代理时不应显示子代理短名, got %v", tt.Texts())
	}
}

func TestModelPickerListPageLockedAgents(t *testing.T) {
	providers := modelPickerTestProviders()
	for _, locked := range []bool{false, true} {
		t.Run(fmt.Sprintf("hasMessages=%v", locked), func(t *testing.T) {
			state := &ModelPickerState{}
			scope := "agent-group-lock-" + fmt.Sprint(locked)
			state.setListOpen(scope, ModelPickerSideLead, true)
			props := ModelPickerProps{
				Mode: ModelPickerSwarm, KeyPrefix: "agent-group-", ScopeKey: scope,
				Providers: providers, HasMessages: locked,
				Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
			}
			tt := ui.NewTester(func(c *ui.Context) {
				renderModelPickerListPage(c, props, state, ModelPickerSideLead)
			}, 330, 640)
			settleModelPickerPage(tt)
			if !tt.HasText("Claude 3.7 Sonnet") {
				t.Fatalf("应默认展开当前模型所属 Agent: %v", tt.Texts())
			}
			if locked {
				if tt.HasText("Codex") {
					t.Fatalf("已有消息后不应出现其他 Agent 分组: %v", tt.Texts())
				}
				return
			}
			if tt.HasText("o3-mini") {
				t.Fatalf("未展开前不应显示其他 Agent 的模型: %v", tt.Texts())
			}
			if err := tt.Click("Codex"); err != nil {
				t.Fatalf("点击 Agent 分组失败：%v", err)
			}
			settleModelPickerPage(tt)
			if !state.openCardSet || state.openCard != "codex" || !tt.HasText("o3-mini") {
				t.Fatalf("应展开 Codex 的模型: open=%q set=%v texts=%v", state.openCard, state.openCardSet, tt.Texts())
			}
		})
	}
}

func TestModelPickerExcludesMcode(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "开头", input: []string{"mcode", "claude", "codex"}, want: []string{"claude", "codex"}},
		{name: "中间", input: []string{"claude", "mcode", "codex"}, want: []string{"claude", "codex"}},
		{name: "没有 mcode", input: []string{"claude", "codex"}, want: []string{"claude", "codex"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providers := make([]ProviderInfo, len(tc.input))
			for i, id := range tc.input {
				providers[i].ID = id
			}
			got := filterModelPickerMagpie(providers, modelPickerMagpieFilter{})
			if len(got) != len(tc.want) {
				t.Fatalf("分组数量 = %d，期望 %d", len(got), len(tc.want))
			}
			for i, provider := range got {
				if provider.ID != tc.want[i] {
					t.Fatalf("排序结果 = %+v，期望 %v", got, tc.want)
				}
				if providers[i].ID != tc.input[i] {
					t.Fatalf("入参被修改：%+v", providers)
				}
			}
		})
	}
}

func TestModelPickerKeepsEndpointAndOldThreadLabel(t *testing.T) {
	cfg := config.Config{}
	providers := []ProviderInfo{
		{ID: "mcode", Models: []ModelInfo{{ID: "old", Name: "旧模型"}}},
		{ID: "custom-gw", Models: []ModelInfo{{ID: "new", Name: "网关模型"}}},
	}
	visible := filterModelPickerMagpie(providers, newModelPickerMagpieFilter(cfg))
	if len(visible) != 1 || visible[0].ID != "custom-gw" {
		t.Fatalf("候选来源错误: %+v", visible)
	}
	pins := []ModelPin{{Provider: "mcode", ModelID: "old"}, {Provider: "custom-gw", ModelID: "new"}}
	entries := modelPickerPinnedEntries(pins, visible)
	if len(entries) != 1 || entries[0].Provider.ID != "custom-gw" {
		t.Fatalf("固定模型错误: %+v", entries)
	}
	if name := modelPickerDisplayName(providers, "mcode", "old"); name != "旧模型" {
		t.Fatalf("旧线程名称错误: %s", name)
	}
}

// 选中模型行后列表页关闭、回到强度卡片，档位调节发生在卡片上的滑块里。
func TestModelPickerSelectedRowChangesEffort(t *testing.T) {
	providers := modelPickerTestProviders()
	providers[0].Models[0].Efforts = []string{"low", "high"}
	selected := ""
	picked := false
	props := ModelPickerProps{KeyPrefix: "effort-test-", ScopeKey: "effort-test", State: &ModelPickerState{}, Providers: providers,
		Selection:   ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet", Effort: "low"},
		OnEffort:    func(_ ModelPickerSide, effort string) { selected = effort },
		OnPickModel: func(ModelPickerSide, ProviderInfo, ModelInfo) { picked = true },
	}
	props.State.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
	// 先把换页动画推到终点：动画期间列表页对命中测试失效，点击会落到滑块下面一页。
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerMenuBody(c, props, props.State) }, 330, 600)
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("缺少模型列表: %v", tt.Texts())
	}
	if err := tt.Click("Claude 3.7 Sonnet"); err != nil {
		t.Fatal(err)
	}
	if !picked {
		t.Fatalf("点击模型行应选用该模型")
	}
	if props.State.listOpen(props.ScopeKey, ModelPickerSideLead) {
		t.Fatalf("选中模型后应回到强度卡片")
	}
	tt.Advance(time.Second)
	tt.Frame()
	clickEffortSlider(t, tt, 1)
	if selected != "high" {
		t.Fatalf("强度=%q", selected)
	}
}

func TestModelPickerCardUsesSelectedSide(t *testing.T) {
	providers := modelPickerTestProviders()
	providers[0].Models[0].Efforts = []string{"low", "high"}
	providers[1].Models[0].Efforts = []string{"low", "high"}
	state := &ModelPickerState{MenuOpen: true}
	var gotSide ModelPickerSide
	var gotEffort string
	props := ModelPickerProps{Mode: ModelPickerSwarm, KeyPrefix: "side-test-", ScopeKey: "side-test", State: state,
		Providers: providers, SwarmOn: true,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet", Effort: "low"},
		Bees:      ModelSelection{Provider: "codex", Model: "o3-mini", Effort: "low"},
		OnEffort:  func(side ModelPickerSide, effort string) { gotSide, gotEffort = side, effort },
	}
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerMenuBody(c, props, state) }, modelPickerPanelWidth, 600)
	if err := tt.Click("子"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("o3-mini") || !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("双槽卡片应同时保留主/子代理模型: %v", tt.Texts())
	}
	clickEffortSlider(t, tt, 1)
	if gotSide != ModelPickerSideBees || gotEffort != "high" {
		t.Fatalf("强度写入侧别=%v，强度=%q", gotSide, gotEffort)
	}
}

func TestModelPickerFollowLeadOnlyOnBeesList(t *testing.T) {
	providers := modelPickerTestProviders()
	for _, side := range []ModelPickerSide{ModelPickerSideLead, ModelPickerSideBees} {
		state := &ModelPickerState{}
		scope := "follow-rail-" + fmt.Sprint(side)
		props := ModelPickerProps{
			Mode: ModelPickerSwarm, KeyPrefix: "follow-rail-", ScopeKey: scope, State: state,
			Providers: providers, SwarmOn: true,
			Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
			Bees:      ModelSelection{Provider: "codex", Model: "o3-mini"},
		}
		state.setListOpen(scope, side, true)
		tt := ui.NewTester(func(c *ui.Context) {
			renderModelPickerListPage(c, props, state, side)
		}, 330, 640)
		if got := tt.HasText("跟随主"); got != (side == ModelPickerSideBees) {
			t.Fatalf("side=%v 的竖栏跟随入口存在=%v", side, got)
		}
	}
}

func TestModelPickerLeadListRestoresPairs(t *testing.T) {
	providers := modelPickerTestProviders()
	pair := SwarmPair{ID: "pair-1", Name: "深度思考", LeadProvider: "codex", LeadModel: "o3-mini",
		WorkerProvider: "claude", WorkerModel: "claude-3-7-sonnet"}
	var applied SwarmPair
	state := &ModelPickerState{MenuOpen: true}
	props := ModelPickerProps{
		Mode: ModelPickerSwarm, KeyPrefix: "pairs-", ScopeKey: "pairs-test", State: state,
		Providers: providers, SwarmOn: true, Pairs: []SwarmPair{pair},
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		OnApplyPair: func(p SwarmPair) {
			applied = p
		},
	}
	state.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerListPage(c, props, state, ModelPickerSideLead) }, 330, 640)
	settleModelPickerPage(tt)
	if !tt.HasText("配对") || magpieTestHasExactText(tt, "模型") {
		t.Fatalf("主导侧列表应保留配对分组并去掉「模型」小节标题: %v", tt.Texts())
	}
	if err := tt.Click("深度思考"); err != nil {
		t.Fatalf("点击配对失败：%v", err)
	}
	if applied.ID != "pair-1" {
		t.Fatalf("配对未应用: %+v", applied)
	}
	if state.MenuOpen || state.listOpen(props.ScopeKey, ModelPickerSideLead) {
		t.Fatal("应用配对后应关闭整个弹层")
	}
}

// ShowGroup 控制左侧图标槽：true 时显示 Provider 图标，false 时按需显示已收藏星标；
// 未开启 ShowGroupName 时两类行均只显示模型名、不显示 5B 行内分组名。
func TestModelPickerListRowPinnedAndGroupStar(t *testing.T) {
	providers := modelPickerTestProviders()
	prov := providers[0]
	pinned := ModelInfo{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"}
	plain := ModelInfo{ID: "dewuopenai/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"}
	props := ModelPickerProps{KeyPrefix: "row-pin-", Pins: []ModelPin{{Provider: "claude", ModelID: pinned.ID}}}

	render := func(m ModelInfo, showGroup bool) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(300).Background(modelPickerPanelBg).Children(func() {
				renderModelPickerListRow(c, props, modelRowOptions{KeyPrefix: "lead-", Provider: prov, Model: m, ShowGroup: showGroup})
			})
		}, 320, 120)
		tt.Frame()
		return tt
	}

	pinnedTT := render(pinned, true)
	if !pinnedTT.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("ShowGroup=true 时应显示模型名: %v", pinnedTT.Texts())
	}
	if pinnedTT.HasText("claude") {
		t.Fatalf("未开启 ShowGroupName 时不应显示行内分组名: %v", pinnedTT.Texts())
	}
	if _, ok := pinnedTT.Find("已收藏"); ok {
		t.Fatalf("ShowGroup=true 时左侧图标槽画供应商图标，不应显示已收藏星标: %v", pinnedTT.AccessAll())
	}

	plainTT := render(plain, true)
	if !plainTT.HasText("DeepSeek V4.1 Flash") {
		t.Fatalf("ShowGroup=true 时应显示模型名: %v", plainTT.Texts())
	}
	if plainTT.HasText("dewuopenai/deepseek-v4.1-flash") || plainTT.HasText("dewuopenai") {
		t.Fatalf("未开启 ShowGroupName 时不应显示行内分组名: %v", plainTT.Texts())
	}
	if _, ok := plainTT.Find("已收藏"); ok {
		t.Fatalf("ShowGroup=true 时不应显示已收藏星标")
	}

	noGroupTT := render(pinned, false)
	if !noGroupTT.HasText("Claude 3.7 Sonnet") || noGroupTT.HasText("claude") {
		t.Fatalf("ShowGroup=false 且未开启 ShowGroupName 时应只显示模型名不显示分组名: %v", noGroupTT.Texts())
	}
	starRect, ok := noGroupTT.Find("已收藏")
	if !ok {
		t.Fatalf("分组内收藏模型应显示星标: %v", noGroupTT.AccessAll())
	}
	if rowRect, ok := findTextRect(t, noGroupTT, "Claude 3.7 Sonnet"); !ok || starRect.X+starRect.W >= rowRect.X+rowRect.W/2 {
		t.Fatalf("已收藏星标应位于模型名左侧，star=%+v row=%+v ok=%v", starRect, rowRect, ok)
	}

	plainNoGroupTT := render(plain, false)
	if _, ok := plainNoGroupTT.Find("已收藏"); ok {
		t.Fatalf("分组内未收藏模型不应显示星标")
	}
}

func TestModelPickerTabSwitchKeepsListOpen(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{MenuOpen: true}
	props := ModelPickerProps{
		Mode: ModelPickerSwarm, KeyPrefix: "tab-keep-", ScopeKey: "tab-keep-test", State: state,
		Providers: providers, SwarmOn: true,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		Bees:      ModelSelection{Provider: "codex", Model: "o3-mini"},
	}
	state.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerMenuBody(c, props, state) }, 330, 640)
	settleModelPickerPage(tt)
	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("主导侧列表未展开: %v", tt.Texts())
	}
	if err := tapTextErr(t, tt, "子"); err != nil {
		t.Fatalf("点击子代理分段失败：%v", err)
	}
	if state.Tab != "bees" {
		t.Fatalf("分段未切换: %q", state.Tab)
	}
	if !state.listOpen(props.ScopeKey, ModelPickerSideBees) || state.listOpen(props.ScopeKey, ModelPickerSideLead) {
		t.Fatalf("切换分段后应留在列表页: bees=%v lead=%v",
			state.listOpen(props.ScopeKey, ModelPickerSideBees), state.listOpen(props.ScopeKey, ModelPickerSideLead))
	}
	settleModelPickerPage(tt)
	if !tt.HasText("o3-mini") {
		t.Fatalf("切换分段后应直接看到执行侧模型: %v", tt.Texts())
	}
}

func TestModelPickerBackButtonReturnsToEffortCard(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{openCard: "codex", openCardSet: true}
	props := ModelPickerProps{
		Mode: ModelPickerSwarm, KeyPrefix: "back-", ScopeKey: "back-test", State: state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	state.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerListPage(c, props, state, ModelPickerSideLead) }, 330, 640)
	settleModelPickerPage(tt)
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("点击返回失败：%v", err)
	}
	if state.listOpen(props.ScopeKey, ModelPickerSideLead) {
		t.Fatal("返回后应关闭模型列表页")
	}
	if state.searchQuery != "" || state.openCard != "" || state.openCardSet {
		t.Fatalf("返回后应清空查询与分组展开状态: %q %q %v", state.searchQuery, state.openCard, state.openCardSet)
	}
}

// 主代理入口打开列表后点「子代理」：编排是异步开启的，浮层会在宿主重建时被当成外部点击关掉。
// 开关生效后弹层要自己回到执行侧，否则用户第一次点击看起来只是把列表关掉了。
func TestModelPickerReopensBeesAfterSwarmEnable(t *testing.T) {
	testenv.TempHome(t)
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	var toggles []bool
	props := ModelPickerProps{
		Mode:          ModelPickerSwarm,
		SwarmOn:       false,
		KeyPrefix:     "t-reopen-",
		ScopeKey:      "t-reopen",
		State:         state,
		Providers:     providers,
		Selection:     ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		Bees:          ModelSelection{Provider: "codex", Model: "o3-mini"},
		CapsuleLabel:  "Sonnet 3.7",
		OnToggleSwarm: func(on bool) { toggles = append(toggles, on) },
	}
	render := func() *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(420).Children(func() {
				renderModelPicker(c, props)
				ui.Text(c, "列表关闭探针")
			})
		}, 460, 320)
		tt.Frame()
		return tt
	}

	tt := render()
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("打开模型列表失败: %v", err)
	}
	settleModelPickerPage(tt)
	if !state.MenuOpen || !tt.HasText("编排") {
		t.Fatalf("列表未打开: open=%v texts=%v", state.MenuOpen, tt.Texts())
	}

	if err := tt.Click("编排开关"); err != nil {
		t.Fatalf("点击编排开关失败: %v", err)
	}
	if len(toggles) != 1 || !toggles[0] {
		t.Fatalf("点击编排开关应请求开启编排, got %v", toggles)
	}
	if !state.reopenAfterSwarm {
		t.Fatalf("点击编排开关应记下等开关生效后拉回浮层的意图")
	}

	// 模拟异步开启过程中的宿主重建把浮层关掉。
	state.MenuOpen = false
	props.SwarmOn = true
	render()
	if !state.MenuOpen || state.Tab != "bees" {
		t.Fatalf("开关生效后弹层应回到执行侧, open=%v tab=%q", state.MenuOpen, state.Tab)
	}

	// 意图只兑现一次：用户再关掉就不再自己弹回来。
	state.MenuOpen = false
	render()
	if state.MenuOpen {
		t.Fatalf("重新打开意图不应反复触发")
	}
}

// 等太久还没开出编排就不再拉回弹层，避免用户已经去做别的事时面板突然弹出。
func TestModelPickerSwarmReopenExpires(t *testing.T) {
	testenv.TempHome(t)
	providers := modelPickerTestProviders()
	state := &ModelPickerState{MenuOpen: false, reopenAfterSwarm: true, reopenDeadline: time.Now().Add(-time.Second)}
	props := ModelPickerProps{
		Mode:         ModelPickerSwarm,
		SwarmOn:      true,
		KeyPrefix:    "t-reopen-expire-",
		ScopeKey:     "t-reopen-expire",
		State:        state,
		Providers:    providers,
		Selection:    ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		Bees:         ModelSelection{Provider: "codex", Model: "o3-mini"},
		CapsuleLabel: "Sonnet 3.7",
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(420).Children(func() {
			renderModelPicker(c, props)
			ui.Text(c, "列表关闭探针")
		})
	}, 460, 320)
	tt.Frame()
	if state.MenuOpen {
		t.Fatalf("等待窗口过期后不应把弹层拉回来")
	}
}

func TestModelPickerGrouped_DefaultOnlyCurrentAgentOpen(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "grouped-default-open"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "grouped-default-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("默认应展开当前 Agent 的模型: %v", tt.Texts())
	}
	if tt.HasText("o3-mini") {
		t.Fatalf("默认不应展开其他 Agent 的模型: %v", tt.Texts())
	}
	if err := tt.Click("展开 Codex"); err != nil {
		t.Fatalf("点击展开 Codex 分组失败: %v", err)
	}
	settleModelPickerPage(tt)
	if !tt.HasText("o3-mini") {
		t.Fatalf("点击其他 Agent 分组标题后应展示其模型: %v", tt.Texts())
	}
}

func TestModelPickerGrouped_LeadHasMessagesHidesOtherAgents(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "grouped-has-messages"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:        ModelPickerSwarm,
		KeyPrefix:   "grouped-locked-",
		ScopeKey:    scope,
		State:       state,
		Providers:   providers,
		HasMessages: true,
		Selection:   ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !tt.HasText("Claude") || !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("已有消息时应保留当前 Agent 分组: %v", tt.Texts())
	}
	if tt.HasText("Codex") {
		t.Fatalf("Lead 侧 HasMessages=true 时不应出现其他 Agent 分组标题: %v", tt.Texts())
	}
}

func TestModelPickerGrouped_EmptyProviderCatalogNotice(t *testing.T) {
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, UnavailableReason: "未安装 Claude Code CLI"},
		{ID: "codex", Name: "Codex", Available: true, Models: []ModelInfo{{ID: "o3-mini", Name: "o3-mini"}}},
		{ID: "gemini", Name: "Gemini", Available: true},
	}
	state := &ModelPickerState{}
	scope := "grouped-empty-notice"
	state.setListOpen(scope, ModelPickerSideLead, true)
	refreshed := 0
	props := ModelPickerProps{
		Mode:             ModelPickerSimple,
		KeyPrefix:        "grouped-notice-",
		ScopeKey:         scope,
		State:            state,
		Providers:        providers,
		Selection:        ModelSelection{Provider: "codex", Model: "o3-mini"},
		OnRefreshCatalog: func() { refreshed++ },
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !tt.HasText("Claude · 未安装 Claude Code CLI") || !tt.HasText("重新探测") {
		t.Fatalf("空目录且有原因的可用 provider 应显示原因与重新探测: %v", tt.Texts())
	}
	if tt.HasText("Gemini") {
		t.Fatalf("没有原因文案的空目录不应画分组: %v", tt.Texts())
	}
	if err := tt.Click("重新探测"); err != nil {
		t.Fatalf("点击重新探测失败: %v", err)
	}
	tt.Frame()
	if refreshed != 1 {
		t.Fatalf("重新探测回调应被调用一次，实际 %d", refreshed)
	}

	// 搜索态不画目录说明块。
	searchState := &ModelPickerState{searchQuery: "claude"}
	searchState.setListOpen(scope, ModelPickerSideLead, true)
	searchProps := props
	searchProps.State = searchState
	searchTT := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, searchProps, searchState, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(searchTT)
	if searchTT.HasText("重新探测") {
		t.Fatalf("搜索态不应画重新探测入口: %v", searchTT.Texts())
	}

	// 未传 OnRefreshCatalog 时保留原因说明但不画重新探测按钮。
	noBtnProps := props
	noBtnProps.OnRefreshCatalog = nil
	noBtnTT := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, noBtnProps, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(noBtnTT)
	if !noBtnTT.HasText("Claude · 未安装 Claude Code CLI") || noBtnTT.HasText("重新探测") {
		t.Fatalf("未传 OnRefreshCatalog 时应展示原因但不画重新探测按钮: %v", noBtnTT.Texts())
	}
}

func TestModelPickerGrouped_TierEditingClearsAfterPick(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{TierEditing: "read"}
	scope := "grouped-tier-edit"
	state.setListOpen(scope, ModelPickerSideBees, true)
	var gotTier string
	var gotOverride *config.SwarmTierOverride
	props := ModelPickerProps{
		Mode:      ModelPickerSwarm,
		KeyPrefix: "grouped-tier-",
		ScopeKey:  scope,
		State:     state,
		SwarmOn:   true,
		Providers: providers,
		Tiers:     []config.SwarmTier{{ID: "read", Name: "只读", Provider: "claude", Model: "claude-3-7-sonnet"}},
		Bees:      ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		OnSetTierOverride: func(tierID string, ov *config.SwarmTierOverride) {
			gotTier = tierID
			gotOverride = ov
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideBees)
	}, 330, 640)
	settleModelPickerPage(tt)

	if err := tt.Click("Claude 3.5 Haiku"); err != nil {
		t.Fatalf("点击档位模型失败: %v", err)
	}
	if gotTier != "read" || gotOverride == nil || gotOverride.Provider != "claude" || gotOverride.Model != "claude-3-5-haiku" {
		t.Fatalf("未按预期调用 OnSetTierOverride: tier=%q ov=%+v", gotTier, gotOverride)
	}
	if state.TierEditing != "" {
		t.Fatalf("选择档位模型后应清空 TierEditing, got %q", state.TierEditing)
	}
}

func TestModelPickerGrouped_EmptySelectionChecksOnlyFirstRow(t *testing.T) {
	providers := []ProviderInfo{{
		ID:        "codex",
		Name:      "Codex",
		Available: true,
		Models: []ModelInfo{
			{ID: "o3-mini", Name: "o3-mini"},
			{ID: "gpt-5", Name: "GPT-5"},
		},
	}}
	state := &ModelPickerState{}
	scope := "grouped-empty-sel"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "grouped-empty-sel-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "codex", Model: ""},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	first, ok1 := tt.Access("o3-mini")
	second, ok2 := tt.Access("GPT-5")
	if !ok1 || !ok2 {
		t.Fatalf("模型行未找到: ok1=%v ok2=%v", ok1, ok2)
	}
	if !first.Checked {
		t.Fatalf("Selection.Model 为空且无 IsDefault 时应选中第一行: %+v", first)
	}
	if second.Checked {
		t.Fatalf("Selection.Model 为空且无 IsDefault 时不应选中第二行: %+v", second)
	}
}

func TestModelPickerGrouped_BeesSwarmFollowLeadRow(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "grouped-follow-lead"
	state.setListOpen(scope, ModelPickerSideBees, true)
	followed := false
	props := ModelPickerProps{
		Mode:      ModelPickerSwarm,
		KeyPrefix: "grouped-follow-",
		ScopeKey:  scope,
		State:     state,
		SwarmOn:   true,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		Bees:      ModelSelection{Provider: "codex", Model: "o3-mini"},
		OnFollowLead: func() {
			followed = true
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideBees)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !tt.HasText("跟随主") {
		t.Fatalf("Bees 侧 Swarm 模式应显示「跟随主」行: %v", tt.Texts())
	}
	if err := tt.Click("跟随主"); err != nil {
		t.Fatalf("点击跟随主失败: %v", err)
	}
	if !followed {
		t.Fatal("点击「跟随主」应调用 OnFollowLead")
	}
}

func TestModelPickerGrouped_TiersCollapsedByDefault(t *testing.T) {
	providers := modelPickerTestProviders()
	tiers := []config.SwarmTier{
		{ID: "read", Name: "只读", ReadOnly: true},
	}

	state := &ModelPickerState{}
	scope := "grouped-tiers-collapsed"
	state.setListOpen(scope, ModelPickerSideBees, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSwarm,
		KeyPrefix: "grouped-tiers-",
		ScopeKey:  scope,
		State:     state,
		SwarmOn:   true,
		Providers: providers,
		Tiers:     tiers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		Bees:      ModelSelection{Provider: "codex", Model: "o3-mini"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideBees)
	}, 330, 640)
	settleModelPickerPage(tt)

	if tt.HasText("只读") {
		t.Fatalf("档位区默认应折叠，不应直接显示「只读」: %v", tt.Texts())
	}
	if _, ok := tt.Find("展开 档位"); !ok {
		t.Fatalf("档位区默认折叠时应能找到「展开 档位」: %v", tt.AccessAll())
	}
	if err := tt.Click("展开 档位"); err != nil {
		t.Fatalf("点击「展开 档位」失败: %v", err)
	}
	for i := 0; i < 30; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("展开档位区动画结束后不应继续请求下一帧")
	}
	if !tt.HasText("只读") {
		t.Fatalf("展开档位区后应显示「只读」: %v", tt.Texts())
	}

	editState := &ModelPickerState{TierEditing: "read"}
	editScope := "grouped-tiers-editing"
	editState.setListOpen(editScope, ModelPickerSideBees, true)
	editProps := props
	editProps.KeyPrefix = "grouped-tiers-edit-"
	editProps.ScopeKey = editScope
	editProps.State = editState
	editTT := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, editProps, editState, ModelPickerSideBees)
	}, 330, 640)
	settleModelPickerPage(editTT)

	if !editTT.HasText("只读") {
		t.Fatalf("预设 TierEditing 时应默认展开档位区并可见「只读」: %v", editTT.Texts())
	}
}

func TestModelPickerGrouped_ExpandMotionSettles(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "grouped-motion-settle"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "grouped-motion-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if err := tt.Click("展开 Codex"); err != nil {
		t.Fatalf("点击展开 Codex 失败: %v", err)
	}
	for i := 0; i < 30; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("展开分组动画结束后不应继续请求下一帧")
	}
}

func TestModelPickerGroupedDefaultOpenFallback(t *testing.T) {
	cases := []struct {
		name      string
		providers []ProviderInfo
		selection ModelSelection
		wantOpen  string
		wantHide  string
	}{
		{
			name:      "empty selection provider falls back to first with models",
			providers: modelPickerTestProviders(),
			selection: ModelSelection{Provider: "", Model: ""},
			wantOpen:  "Claude 3.7 Sonnet",
			wantHide:  "o3-mini",
		},
		{
			name:      "missing selection provider falls back to first with models",
			providers: modelPickerTestProviders(),
			selection: ModelSelection{Provider: "missing-provider", Model: "x"},
			wantOpen:  "Claude 3.7 Sonnet",
			wantHide:  "o3-mini",
		},
		{
			name: "skips empty provider before first with models",
			providers: []ProviderInfo{
				{ID: "claude", Name: "Claude", Available: true},
				{ID: "codex", Name: "Codex", Available: true, Models: []ModelInfo{{ID: "o3-mini", Name: "o3-mini"}}},
			},
			selection: ModelSelection{Provider: "claude"},
			wantOpen:  "o3-mini",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &ModelPickerState{}
			scope := "grouped-fallback-" + tc.name
			state.setListOpen(scope, ModelPickerSideLead, true)
			props := ModelPickerProps{
				Mode:      ModelPickerSimple,
				KeyPrefix: "grouped-fallback-",
				ScopeKey:  scope,
				State:     state,
				Providers: tc.providers,
				Selection: tc.selection,
			}
			tt := ui.NewTester(func(c *ui.Context) {
				renderModelPickerListPage(c, props, state, ModelPickerSideLead)
			}, 330, 640)
			settleModelPickerPage(tt)

			if !tt.HasText(tc.wantOpen) {
				t.Fatalf("应默认展开首个有模型的 Agent 并显示 %q: %v", tc.wantOpen, tt.Texts())
			}
			if tc.wantHide != "" && tt.HasText(tc.wantHide) {
				t.Fatalf("不应同时展开其他 Agent 模型 %q: %v", tc.wantHide, tt.Texts())
			}
		})
	}
}

func TestModelPickerGrouped_PopoverDrawOverDuringExpand(t *testing.T) {
	testenv.TempHome(t)
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	var picked ModelInfo
	props := ModelPickerProps{
		Mode:         ModelPickerSimple,
		KeyPrefix:    "grouped-popover-drawover-",
		ScopeKey:     "grouped-popover-drawover",
		State:        state,
		Providers:    providers,
		Selection:    ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		CapsuleLabel: "Sonnet 3.7",
		OnPickModel: func(_ ModelPickerSide, _ ProviderInfo, m ModelInfo) {
			picked = m
			state.Reset()
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(420).Height(740).Children(func() {
			ui.Box(c).Grow(1)
			renderModelPicker(c, props)
		})
	}, 460, 760)
	tt.Frame()

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("打开模型弹层失败: %v", err)
	}
	settleModelPickerPage(tt)

	if err := tt.Click("选择其他模型"); err != nil {
		t.Fatalf("进入列表页失败: %v", err)
	}
	settleModelPickerPage(tt)

	if err := tt.Click("展开 Codex"); err != nil {
		t.Fatalf("展开 Codex 分组失败: %v", err)
	}
	// 推进少数帧，使分组展开动画仍在行进并暴露模型行。
	for i := 0; i < 3; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}

	if err := tt.Click("o3-mini"); err != nil {
		t.Fatalf("展开动画进行中点击模型行失败: %v", err)
	}
	tt.Frame()
	if picked.ID != "o3-mini" {
		t.Fatalf("点击模型行后应选中 o3-mini，实际: %+v", picked)
	}
}

func TestModelPickerRowNameTrimsAuto(t *testing.T) {
	cases := []struct {
		name string
		prov ProviderInfo
		in   string
		want string
	}{
		{
			name: "trims_auto_suffix",
			prov: ProviderInfo{ID: "claude", Models: []ModelInfo{{ID: "m1", Name: "claude-opus-5-5 (auto)"}}},
			in:   "claude-opus-5-5 (auto)",
			want: "claude-opus-5-5",
		},
		{
			name: "keeps_plain_name",
			prov: ProviderInfo{ID: "claude", Models: []ModelInfo{{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"}}},
			in:   "Claude 3.7 Sonnet",
			want: "Claude 3.7 Sonnet",
		},
		{
			name: "keeps_auto_when_provider_has_same_base_name",
			prov: ProviderInfo{
				ID: "claude",
				Models: []ModelInfo{
					{ID: "m1", Name: "claude-opus-5-5 (auto)"},
					{ID: "m2", Name: "claude-opus-5-5"},
				},
			},
			in:   "claude-opus-5-5 (auto)",
			want: "claude-opus-5-5 (auto)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelPickerRowName(tc.prov, tc.in); got != tc.want {
				t.Fatalf("modelPickerRowName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModelPickerListRowShowsTrimmedName(t *testing.T) {
	prov := ProviderInfo{
		ID:   "magpie",
		Name: "Magpie",
		Models: []ModelInfo{
			{ID: "jp/auto/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash (auto)"},
		},
	}
	props := ModelPickerProps{KeyPrefix: "row-trim-auto-"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(300).Background(modelPickerPanelBg).Children(func() {
			renderModelPickerListRow(c, props, modelRowOptions{
				KeyPrefix: "lead-",
				Provider:  prov,
				Model:     prov.Models[0],
			})
		})
	}, 320, 120)
	tt.Frame()

	texts := tt.Texts()
	// 可见文本只来自 ui.Text，完整原名只留在 Label/Tooltip 上。
	if !slices.Contains(texts, "DeepSeek V4.1 Flash") {
		t.Fatalf("屏幕文本应显示去掉 (auto) 后的名字: %v", texts)
	}
	if _, ok := tt.Find("DeepSeek V4.1 Flash (auto)"); !ok {
		t.Fatalf("按钮 Label 应保留完整原名以便查找/点击: %v", tt.AccessAll())
	}
}

func TestModelPickerAccordionSingleOpen(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "accordion-single-open"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "accordion-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Pins:      []ModelPin{{Provider: "claude", ModelID: "claude-3-7-sonnet"}},
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-5-haiku"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if err := tt.Click("Claude"); err != nil {
		t.Fatalf("点击 Claude 标题失败：%v", err)
	}
	settleModelPickerPage(tt)
	if tt.HasText("Claude 3.5 Haiku") {
		t.Fatalf("再次点击已展开的 Claude 后应全部收起: %v", tt.Texts())
	}

	if err := tt.Click("Claude"); err != nil {
		t.Fatalf("展开 Claude 标题失败：%v", err)
	}
	settleModelPickerPage(tt)
	if !tt.HasText("Claude 3.5 Haiku") || tt.HasText("o3-mini") {
		t.Fatalf("展开 Claude 后应仅显示 Claude 模型: %v", tt.Texts())
	}

	if err := tt.Click("Codex"); err != nil {
		t.Fatalf("点击 Codex 标题失败：%v", err)
	}
	settleModelPickerPage(tt)
	if tt.HasText("Claude 3.5 Haiku") || tt.HasText("Claude 3.7 Sonnet") || !tt.HasText("o3-mini") {
		t.Fatalf("点击 Codex 后应关闭 Claude 并展开 Codex: %v", tt.Texts())
	}

	if err := tt.Click("收藏"); err != nil {
		t.Fatalf("点击收藏标题失败：%v", err)
	}
	settleModelPickerPage(tt)
	if tt.HasText("Claude 3.5 Haiku") || tt.HasText("o3-mini") || !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("点击收藏后应关闭 Agent 分组并显示收藏行: %v", tt.Texts())
	}
}

func TestModelPickerDefaultOpenCardPrefersPinned(t *testing.T) {
	providers := modelPickerTestProviders()
	pins := []ModelPin{
		{Provider: "claude", ModelID: "claude-3-7-sonnet"},
		{Provider: "codex", ModelID: "o3-mini"},
	}

	t.Run("当前模型在收藏里默认仅展开收藏", func(t *testing.T) {
		state := &ModelPickerState{}
		scope := "default-open-pinned"
		state.setListOpen(scope, ModelPickerSideLead, true)
		props := ModelPickerProps{
			Mode:      ModelPickerSimple,
			KeyPrefix: "def-pinned-",
			ScopeKey:  scope,
			State:     state,
			Providers: providers,
			Pins:      pins,
			Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderModelPickerListPage(c, props, state, ModelPickerSideLead)
		}, 330, 640)
		settleModelPickerPage(tt)
		if !tt.HasText("Claude 3.7 Sonnet") || !tt.HasText("o3-mini") {
			t.Fatalf("应默认展开收藏卡片: %v", tt.Texts())
		}
		if tt.HasText("Claude 3.5 Haiku") {
			t.Fatalf("收藏默认展开时当前 Agent 卡片应保持收起: %v", tt.Texts())
		}
	})

	t.Run("当前模型不在收藏里默认展开当前Agent", func(t *testing.T) {
		state := &ModelPickerState{}
		scope := "default-open-agent"
		state.setListOpen(scope, ModelPickerSideLead, true)
		props := ModelPickerProps{
			Mode:      ModelPickerSimple,
			KeyPrefix: "def-agent-",
			ScopeKey:  scope,
			State:     state,
			Providers: providers,
			Pins:      pins,
			Selection: ModelSelection{Provider: "claude", Model: "claude-3-5-haiku"},
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderModelPickerListPage(c, props, state, ModelPickerSideLead)
		}, 330, 640)
		settleModelPickerPage(tt)
		if !tt.HasText("Claude 3.5 Haiku") || !tt.HasText("Claude 3.7 Sonnet") {
			t.Fatalf("应默认展开当前 Agent 卡片: %v", tt.Texts())
		}
		if tt.HasText("o3-mini") {
			t.Fatalf("当前 Agent 默认展开时收藏卡片应保持收起: %v", tt.Texts())
		}
	})
}

func TestModelPickerQuotaOnAgentHeader(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "quota-header"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "quota-hdr-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		QuotaUsed: func(id string) (float64, bool) {
			if id == "codex" {
				return 35, true
			}
			return 0, false
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if tt.HasText("o3-mini") {
		t.Fatalf("Codex 初始应处于收起状态: %v", tt.Texts())
	}
	if !magpieTestHasExactText(tt, "35%") {
		t.Fatalf("Agent 收起时标题行也应显示配额 35%%: %v", tt.Texts())
	}

	if err := tt.Click("Codex"); err != nil {
		t.Fatalf("点击 Codex 标题失败：%v", err)
	}
	settleModelPickerPage(tt)
	if !tt.HasText("o3-mini") {
		t.Fatalf("点击 Codex 后应展开模型行: %v", tt.Texts())
	}
	count := 0
	for _, txt := range tt.Texts() {
		if txt == "35%" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("展开后 35%% 应仅在标题行出现一次，实际 %d 次: %v", count, tt.Texts())
	}
}

func settleFrames(t *testing.T, tt *ui.Tester) {
	t.Helper()
	for range 300 {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		if !tt.FramePending() {
			return
		}
	}
	t.Fatal("卡片切换动效结束后不应继续请求下一帧")
}

func TestModelPickerCardToggleAnimationSettles(t *testing.T) {
	t.Run("正常动效点开另一张卡片后推进时钟到结束不再请求帧", func(t *testing.T) {
		setAppReduceMotion(t, false)
		providers := modelPickerTestProviders()
		state := &ModelPickerState{}
		scope := "card-toggle-settle"
		state.setListOpen(scope, ModelPickerSideLead, true)
		props := ModelPickerProps{
			Mode:      ModelPickerSimple,
			KeyPrefix: "toggle-settle-",
			ScopeKey:  scope,
			State:     state,
			Providers: providers,
			Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderModelPickerListPage(c, props, state, ModelPickerSideLead)
		}, 330, 640)
		settleModelPickerPage(tt)
		if tt.FramePending() {
			t.Fatal("初始入场落位后不应继续请求帧")
		}

		if err := tt.Click("Codex"); err != nil {
			t.Fatalf("点击 Codex 标题失败：%v", err)
		}
		if !tt.FramePending() {
			t.Fatal("切换展开卡片后应启动折叠与旋转动效")
		}

		// 逐帧推进时钟，等待弹簧与退出过渡完全落位。
		settleFrames(t, tt)
		if tt.HasText("Claude 3.7 Sonnet") || !tt.HasText("o3-mini") {
			t.Fatalf("切换后应仅展开 Codex 模型: %v", tt.Texts())
		}

		// 静止时再推进时钟也不应重启动画。
		tt.Advance(DurBase)
		if tt.FramePending() {
			t.Fatal("静止状态下不应请求下一帧")
		}
	})

	t.Run("单张卡片收起时保留父节点并播放Exit动画", func(t *testing.T) {
		setAppReduceMotion(t, false)
		open := true
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("single-card-host")).Width(300).Background(modelPickerPanelBg).Children(func() {
				renderModelPickerCollapsibleCard(c, "single-", "-claude", "Claude", open, nil, nil,
					func() { open = !open },
					func() {
						ui.Text(c, "Claude 3.7 Sonnet").FontSize(uiFont(TypeBody)).TextColor(modelPickerTextPrimary)
					},
				)
			})
		}, 320, 200)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("初始展开静止时不应请求帧")
		}
		open = false
		tt.Frame()
		if !tt.FramePending() {
			t.Fatal("收起卡片后应播放 Exit 收起与箭头旋转动效")
		}
		tt.Advance(DurSnap / 2)
		midImg := tt.Image()
		tt.Advance(DurSlow + 200*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("收起动画结束后不应继续请求下一帧")
		}
		endImg := tt.Image()
		if midImg.Bounds() == endImg.Bounds() && slices.Equal(midImg.Pix, endImg.Pix) {
			t.Fatal("收起过渡途中帧与结束后帧像素完全一致，说明未播放过渡动画")
		}
	})

	t.Run("减少动效下切换卡片立即落位", func(t *testing.T) {
		setAppReduceMotion(t, true)
		providers := modelPickerTestProviders()
		state := &ModelPickerState{}
		scope := "card-toggle-reduced"
		state.setListOpen(scope, ModelPickerSideLead, true)
		props := ModelPickerProps{
			Mode:      ModelPickerSimple,
			KeyPrefix: "toggle-reduced-",
			ScopeKey:  scope,
			State:     state,
			Providers: providers,
			Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderModelPickerListPage(c, props, state, ModelPickerSideLead)
		}, 330, 640, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("减少动效下初始静止时不应请求帧")
		}
		if err := tt.Click("Codex"); err != nil {
			t.Fatalf("点击 Codex 标题失败：%v", err)
		}
		if tt.FramePending() {
			t.Fatal("减少动效下切换卡片不应请求动画帧")
		}
		if tt.HasText("Claude 3.7 Sonnet") || !tt.HasText("o3-mini") {
			t.Fatalf("减少动效下切换后应立即展开 Codex: %v", tt.Texts())
		}
	})
}

func TestGetProviderQuotaUsedPrefersComposerCache(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	SetChatQuotaWindowsForProvider(s, "codex", []ProviderQuotaWindow{
		{Label: "5h", UsedPercent: 35, Active: true},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, "codex", nil) })

	got, ok := getProviderQuotaUsed(s, "codex")
	if !ok || got != 35 {
		t.Fatalf("getProviderQuotaUsed(codex) = (%v, %v), want (35, true)", got, ok)
	}
}

func TestModelPickerOpenListCallsOnOpenList(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{MenuOpen: true}
	calls := 0
	props := ModelPickerProps{
		Mode:       ModelPickerSimple,
		KeyPrefix:  "open-list-cb-",
		ScopeKey:   "open-list-cb",
		State:      state,
		Providers:  providers,
		Selection:  ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		OnOpenList: func() { calls++ },
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerMenuBody(c, props, state)
	}, 330, 600)
	if err := tt.Click("选择其他模型"); err != nil {
		t.Fatalf("点击选择其他模型失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("OnOpenList 调用次数 = %d, want 1", calls)
	}
	if !state.listOpen(props.ScopeKey, ModelPickerSideLead) {
		t.Fatal("点击选择其他模型后应进入列表页")
	}
}

// 5B 行内紧跟分组名：Agent 分组卡片（ShowGroup=false）仅带有效上游前缀的模型显示「· 分组」；
// 跨 Agent 收藏卡片（ShowGroup=true）无前缀时回退展示「· <providerID>」；未开启 ShowGroupName 则不显示。
func TestModelPickerListRowGroupNameOptIn(t *testing.T) {
	providers := modelPickerTestProviders()
	prov := providers[0]
	cases := []struct {
		name          string
		model         ModelInfo
		showGroup     bool
		showGroupName bool
		wantGroup     string
	}{
		{name: "Agent卡内无前缀不显示", model: ModelInfo{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"}, showGroup: false, showGroupName: true, wantGroup: ""},
		{name: "收藏卡无前缀回退Provider", model: ModelInfo{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"}, showGroup: true, showGroupName: true, wantGroup: "· Claude"},
		{name: "Agent卡内有前缀展示", model: ModelInfo{ID: "bulita_claude/claude-opus-5.5", Name: "Claude Opus 5.5"}, showGroup: false, showGroupName: true, wantGroup: "· bulita_claude"},
		{name: "关闭ShowGroupName不显示", model: ModelInfo{ID: "bulita_claude/claude-opus-5.5", Name: "Claude Opus 5.5"}, showGroup: true, showGroupName: false, wantGroup: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			props := ModelPickerProps{KeyPrefix: "row-group-", Pins: nil}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(300).Background(modelPickerPanelBg).Children(func() {
					renderModelPickerListRow(c, props, modelRowOptions{
						KeyPrefix:     "lead-",
						Provider:      prov,
						Model:         tc.model,
						ShowGroup:     tc.showGroup,
						ShowGroupName: tc.showGroupName,
					})
				})
			}, 320, 120)
			tt.Frame()
			if !tt.HasText(tc.model.Name) {
				t.Fatalf("缺少模型名: %v", tt.Texts())
			}
			if tc.wantGroup == "" {
				if slices.Contains(tt.Texts(), "· Claude") || slices.Contains(tt.Texts(), "· bulita_claude") {
					t.Fatalf("不应显示分组名: %v", tt.Texts())
				}
				return
			}
			if !slices.Contains(tt.Texts(), tc.wantGroup) {
				t.Fatalf("缺少分组名 %q: %v", tc.wantGroup, tt.Texts())
			}
		})
	}
}

// 搜索态保留 Agent 分组卡片标题：带上游前缀的模型展示「· 分组」，普通直连模型不冗余重复 Agent ID。
func TestModelPickerSearchShowsGroupName(t *testing.T) {
	providers := modelPickerTestProviders()
	providers[0].Models = append(providers[0].Models, ModelInfo{ID: "bulita_claude/claude-opus-5.5", Name: "Claude Opus 5.5"})
	providers[1].Models = append(providers[1].Models, ModelInfo{ID: "dewuopenai/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"})
	state := &ModelPickerState{MenuOpen: true, searchQuery: "claude"}
	props := ModelPickerProps{Mode: ModelPickerSwarm, KeyPrefix: "search-group-", ScopeKey: "search-group-test", State: state,
		Providers: providers, SwarmOn: true,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		Bees:      ModelSelection{Provider: "codex", Model: "o3-mini"},
	}
	state.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerListPage(c, props, state, ModelPickerSideLead) }, 330, 640)
	settleModelPickerPage(tt)
	for _, want := range []string{"Claude 3.7 Sonnet", "Claude 3.5 Haiku", "Claude Opus 5.5", "· bulita_claude"} {
		if !slices.Contains(tt.Texts(), want) {
			t.Fatalf("搜索态缺少 %q: %v", want, tt.Texts())
		}
	}
	if slices.Contains(tt.Texts(), "· claude") {
		t.Fatalf("Agent 分组卡片内普通直连模型不应冗余显示「· claude」: %v", tt.Texts())
	}
	if tt.HasText("DeepSeek V4.1 Flash") || tt.HasText("dewuopenai") {
		t.Fatalf("未命中的模型不应出现在搜索结果里: %v", tt.Texts())
	}
}

// 列表页分组卡片：Agent 卡内仅带前缀模型展示「· 分组」，普通模型不重复 Agent ID；跨 Agent 收藏卡无前缀也回退显示「· <provider>」。
func TestModelPickerGroupedListShowsGroupName(t *testing.T) {
	providers := modelPickerTestProviders()
	providers[0].Models = append(providers[0].Models, ModelInfo{ID: "bulita_claude/claude-opus-5.5", Name: "Claude Opus 5.5"})
	providers[1].Models = append(providers[1].Models, ModelInfo{ID: "dewuopenai/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"})
	pins := []ModelPin{{Provider: "claude", ModelID: "claude-3-7-sonnet"}}
	selection := ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"}
	render := func(openCard string) *ui.Tester {
		state := &ModelPickerState{MenuOpen: true, openCard: openCard, openCardSet: true}
		props := ModelPickerProps{Mode: ModelPickerSimple, KeyPrefix: "grouped-name-", ScopeKey: "grouped-name-test", State: state,
			Providers: providers, Pins: pins, Selection: selection,
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(320).Children(func() {
				renderModelPickerGroupedList(c, props, state, ModelPickerSideLead, selection, providers, modelPickerPinnedEntries(pins, providers), "", nil)
			})
		}, 340, 640)
		tt.Frame()
		return tt
	}

	claudeCard := render("claude")
	for _, want := range []string{"Claude 3.7 Sonnet", "Claude Opus 5.5", "· bulita_claude"} {
		if !slices.Contains(claudeCard.Texts(), want) {
			t.Fatalf("Claude 卡内缺少 %q: %v", want, claudeCard.Texts())
		}
	}
	if slices.Contains(claudeCard.Texts(), "· claude") {
		t.Fatalf("Claude 卡内普通模型不应冗余显示「· claude」: %v", claudeCard.Texts())
	}

	codexCard := render("codex")
	for _, want := range []string{"o3-mini", "DeepSeek V4.1 Flash", "· dewuopenai"} {
		if !slices.Contains(codexCard.Texts(), want) {
			t.Fatalf("Codex 卡内缺少 %q: %v", want, codexCard.Texts())
		}
	}
	if slices.Contains(codexCard.Texts(), "· codex") {
		t.Fatalf("Codex 卡内普通模型不应冗余显示「· codex」: %v", codexCard.Texts())
	}

	pinnedCard := render(modelPickerPinnedCardID)
	for _, want := range []string{"Claude 3.7 Sonnet", "· Claude"} {
		if !slices.Contains(pinnedCard.Texts(), want) {
			t.Fatalf("收藏卡内无前缀模型应回退显示 %q: %v", want, pinnedCard.Texts())
		}
	}
}

// 搜索态复用列表页的分组卡片：保留命中的 Agent 标题行、强制展开，只列命中模型。
func TestModelPickerSearchKeepsAgentHeaders(t *testing.T) {
	providers := modelPickerTestProviders()
	providers[1].Models = append(providers[1].Models, ModelInfo{ID: "google-antigravity/claude-sonnet-4-6", Name: "Claude Sonnet 4.6"})
	// 查询词要避开 Provider 名（命中 Provider 名会列出它的全部模型）。
	state := &ModelPickerState{MenuOpen: true, searchQuery: "sonnet"}
	props := ModelPickerProps{Mode: ModelPickerSimple, KeyPrefix: "search-header-", ScopeKey: "search-header-test", State: state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	state.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
	tt := ui.NewTester(func(c *ui.Context) { renderModelPickerListPage(c, props, state, ModelPickerSideLead) }, 330, 640)
	settleModelPickerPage(tt)
	// 两个 Agent 都有命中：标题行都要在，用来分辨模型属于哪个 Agent。
	for _, want := range []string{"Claude", "Codex", "Claude 3.7 Sonnet", "Claude Sonnet 4.6"} {
		if !tt.HasText(want) {
			t.Fatalf("搜索态缺少 %q: %v", want, tt.Texts())
		}
	}
	// 未命中的模型不出现（codex 的 o3-mini、claude 的 Haiku 名里没有 sonnet）。
	if tt.HasText("o3-mini") || tt.HasText("Claude 3.5 Haiku") {
		t.Fatalf("未命中的模型不应出现在搜索里: %v", tt.Texts())
	}
	// 卡片强制展开：命中行直接可见，不需要再点标题。
	if !slices.Contains(tt.Texts(), "· google-antigravity") {
		t.Fatalf("搜索命中行应直接展开并显示「· google-antigravity」: %v", tt.Texts())
	}
}

// 搜索命中收藏里的模型时保留「收藏」分组，只列命中的收藏行；没命中则不显示收藏分组。
func TestModelPickerSearchPinnedGroup(t *testing.T) {
	providers := modelPickerTestProviders()
	props := ModelPickerProps{Mode: ModelPickerSimple, KeyPrefix: "search-pinned-", ScopeKey: "search-pinned-test",
		Providers: providers,
		Pins:      []ModelPin{{Provider: "claude", ModelID: "claude-3-7-sonnet"}},
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	render := func(query string) *ui.Tester {
		state := &ModelPickerState{MenuOpen: true, searchQuery: query}
		props.State = state
		state.setListOpen(props.ScopeKey, ModelPickerSideLead, true)
		tt := ui.NewTester(func(c *ui.Context) { renderModelPickerListPage(c, props, state, ModelPickerSideLead) }, 330, 640)
		settleModelPickerPage(tt)
		return tt
	}

	hit := render("claude")
	if !hit.HasText("收藏") {
		t.Fatalf("命中收藏模型时应显示收藏分组: %v", hit.Texts())
	}
	var sonnetRows []ui.AccessInfo
	for _, acc := range hit.AccessAll() {
		if acc.Role == ui.RoleButton && acc.Name == "Claude 3.7 Sonnet" {
			sonnetRows = append(sonnetRows, acc)
		}
	}
	if len(sonnetRows) != 2 {
		t.Fatalf("收藏分组与 Agent 分组应各有一个可点击行, got %d (%+v)", len(sonnetRows), sonnetRows)
	}
	if !slices.Contains(hit.Texts(), "· Claude") {
		t.Fatalf("收藏卡片命中的跨 Agent 行应回退显示「· Claude」: %v", hit.Texts())
	}
	if !hit.HasText("Claude 3.5 Haiku") {
		t.Fatalf("Claude 分组卡片应同时命中 Claude 3.5 Haiku: %v", hit.Texts())
	}

	miss := render("o3-mini")
	if miss.HasText("收藏") {
		t.Fatalf("收藏未命中时不应显示收藏分组: %v", miss.Texts())
	}
}

func TestModelPickerListRow5BInlineGroupLayout(t *testing.T) {
	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true}
	cases := []struct {
		name      string
		model     ModelInfo
		isChecked bool
		iconSize  float32
	}{
		{
			name: "checked_long_model_and_group_fits_330px",
			model: ModelInfo{
				ID:   "bulita_claude/super-long-model-id",
				Name: "bulita_claude/Super-Long-Model-Name-With-Many-Segments-For-5B-Inline-Layout-Check",
			},
			isChecked: true,
			iconSize:  IconSM,
		},
		{
			name: "fast_long_model_and_long_group_fits_330px",
			model: ModelInfo{
				ID:           "very_long_upstream_gateway_group/super-long-model-id",
				Name:         "Super-Long-Model-Name-That-Must-Truncate-Before-Pushing-Zap-Out-Of-Row (auto)",
				SupportsFast: true,
			},
			isChecked: false,
			iconSize:  12,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			props := ModelPickerProps{KeyPrefix: "row-5b-"}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(330).Background(modelPickerPanelBg).Children(func() {
					renderModelPickerListRow(c, props, modelRowOptions{
						KeyPrefix:     "lead-",
						Provider:      prov,
						Model:         tc.model,
						IsChecked:     tc.isChecked,
						ShowGroupName: true,
					})
				})
			}, 330, 120)
			tt.Frame()

			btnInfo, ok := tt.Access(tc.model.Name)
			if !ok {
				t.Fatalf("未找到行按钮 %q: %v", tc.model.Name, tt.AccessAll())
			}
			if btnInfo.Bounds.X < 0 || btnInfo.Bounds.X+btnInfo.Bounds.W > 330.5 {
				t.Fatalf("行容器超出 330px 边界: %+v", btnInfo.Bounds)
			}

			groupName := modelPickerRowGroup(prov, tc.model, false)
			displayName := modelPickerStripGroupPrefix(modelPickerRowName(prov, tc.model.Name), groupName)
			nameRect, ok := tt.Find(displayName)
			if !ok {
				t.Fatalf("未找到模型名文本 %q: %v", displayName, tt.Texts())
			}
			groupRect, ok := tt.Find("· " + groupName)
			if !ok {
				t.Fatalf("未找到分组名文本 %q: %v", "· "+groupName, tt.Texts())
			}
			if nameRect.H > 22 || groupRect.H > 22 {
				t.Fatalf("模型名与分组名应保持单行: name=%+v group=%+v", nameRect, groupRect)
			}
			if groupRect.X+0.5 < nameRect.X+nameRect.W {
				t.Fatalf("分组名应紧跟在模型名右侧，不应重叠: name=%+v group=%+v", nameRect, groupRect)
			}
			// 右侧状态图标在 -label-wrap 外侧且 Shrink(0)，检查其预留空间完全在按钮边界内。
			if groupRect.X+groupRect.W+Space2+tc.iconSize+Space3 > btnInfo.Bounds.X+btnInfo.Bounds.W+0.5 {
				t.Fatalf("右侧状态图标被挤出容器边界: group=%+v btn=%+v iconSize=%v", groupRect, btnInfo.Bounds, tc.iconSize)
			}
		})
	}
}

func TestModelPickerGroupNameExtractionAndStripPrefix(t *testing.T) {
	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true}
	groupCases := []struct {
		id       string
		fallback bool
		want     string
	}{
		{id: "bulita_claude/claude-opus-5.5", fallback: false, want: "bulita_claude"},
		{id: "bulita_claude/claude-opus-5.5", fallback: true, want: "bulita_claude"},
		{id: "bulita_grok/grok-4.6", fallback: false, want: "bulita_grok"},
		{id: "foo/", fallback: false, want: ""},
		{id: "foo/", fallback: true, want: "Claude"},
		{id: "/bar", fallback: false, want: ""},
		{id: "/bar", fallback: true, want: "Claude"},
		{id: "auto/claude-sonnet", fallback: false, want: ""},
		{id: "auto/claude-sonnet", fallback: true, want: "Claude"},
		{id: "GROUP/claude-sonnet", fallback: false, want: ""},
		{id: "GROUP/claude-sonnet", fallback: true, want: "Claude"},
		{id: "claude-3-7-sonnet", fallback: false, want: ""},
		{id: "claude-3-7-sonnet", fallback: true, want: "Claude"},
	}
	for _, tc := range groupCases {
		if got := modelPickerRowGroup(prov, ModelInfo{ID: tc.id}, tc.fallback); got != tc.want {
			t.Fatalf("modelPickerRowGroup(%q, fallback=%v) = %q, want %q", tc.id, tc.fallback, got, tc.want)
		}
	}

	stripCases := []struct {
		display string
		group   string
		want    string
	}{
		{display: "bulita_grok/grok-4.6", group: "bulita_grok", want: "grok-4.6"},
		{display: "bulita_grok/", group: "bulita_grok", want: "bulita_grok/"},
		{display: "grok-4.6", group: "bulita_grok", want: "grok-4.6"},
		{display: "bulita_grok/grok-4.6", group: "", want: "bulita_grok/grok-4.6"},
	}
	for _, tc := range stripCases {
		if got := modelPickerStripGroupPrefix(tc.display, tc.group); got != tc.want {
			t.Fatalf("modelPickerStripGroupPrefix(%q, %q) = %q, want %q", tc.display, tc.group, got, tc.want)
		}
	}

	// Tooltip 使用 " · " 分隔符，且展示名已含前缀或与分组同名时不重复拼接。
	tipCases := []struct {
		name      string
		model     ModelInfo
		showGroup bool
		wantTip   string
	}{
		{
			name:      "distinct_group_uses_middots",
			model:     ModelInfo{ID: "bulita_claude/claude-opus-5.5", Name: "Claude Opus 5.5"},
			showGroup: false,
			wantTip:   "Claude Opus 5.5 · bulita_claude",
		},
		{
			name:      "prefixed_name_dedups_tooltip",
			model:     ModelInfo{ID: "bulita_grok/grok-4.6", Name: "bulita_grok/grok-4.6"},
			showGroup: false,
			wantTip:   "bulita_grok/grok-4.6",
		},
		{
			name:      "same_name_as_group_dedups_tooltip",
			model:     ModelInfo{ID: "claude-default", Name: "Claude"},
			showGroup: true,
			wantTip:   "Claude",
		},
	}
	for _, tc := range tipCases {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Width(320).Children(func() {
					renderModelPickerListRow(c, ModelPickerProps{KeyPrefix: "tip-"}, modelRowOptions{
						Provider:      prov,
						Model:         tc.model,
						ShowGroup:     tc.showGroup,
						ShowGroupName: true,
					})
				})
			}, 340, 120)
			tt.Frame()
			acc, ok := tt.Access(tc.model.Name)
			if !ok {
				t.Fatalf("未找到行 %q: %v", tc.model.Name, tt.AccessAll())
			}
			if acc.Description != tc.wantTip {
				t.Fatalf("Tooltip = %q, want %q", acc.Description, tc.wantTip)
			}
		})
	}
}

func TestModelPickerRowGroupFallbackPrefersName(t *testing.T) {
	namedProv := ProviderInfo{ID: "custom-432db46a", Name: "magpie_jp", Available: true}
	blankNameProv := ProviderInfo{ID: "custom-432db46a", Name: "  ", Available: true}
	cases := []struct {
		name     string
		prov     ProviderInfo
		model    ModelInfo
		fallback bool
		want     string
	}{
		{
			name:     "fallback_prefers_provider_name",
			prov:     namedProv,
			model:    ModelInfo{ID: "deepseek-v4.1-flash"},
			fallback: true,
			want:     "magpie_jp",
		},
		{
			name:     "fallback_uses_id_when_name_blank",
			prov:     blankNameProv,
			model:    ModelInfo{ID: "deepseek-v4.1-flash"},
			fallback: true,
			want:     "custom-432db46a",
		},
		{
			name:     "prefix_takes_precedence_over_provider_name",
			prov:     namedProv,
			model:    ModelInfo{ID: "bulita/deepseek"},
			fallback: true,
			want:     "bulita",
		},
		{
			name:     "no_fallback_returns_empty_without_prefix",
			prov:     namedProv,
			model:    ModelInfo{ID: "deepseek-v4.1-flash"},
			fallback: false,
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelPickerRowGroup(tc.prov, tc.model, tc.fallback); got != tc.want {
				t.Fatalf("modelPickerRowGroup(%+v, %q, fallback=%v) = %q, want %q", tc.prov, tc.model.ID, tc.fallback, got, tc.want)
			}
		})
	}
}

func TestModelPickerSearchLockedPinnedNoEmptyText(t *testing.T) {
	providers := modelPickerTestProviders()
	state := &ModelPickerState{MenuOpen: true, searchQuery: "o3-mini"}
	scope := "search-locked-pinned"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:        ModelPickerSimple,
		KeyPrefix:   "search-locked-pinned-",
		ScopeKey:    scope,
		State:       state,
		Providers:   providers,
		HasMessages: true,
		Pins:        []ModelPin{{Provider: "codex", ModelID: "o3-mini"}},
		Selection:   ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !tt.HasText("收藏") || !tt.HasText("o3-mini") {
		t.Fatalf("锁定态搜索收藏的非当前 Agent 模型时应展示收藏卡片: %v", tt.Texts())
	}
	if tt.HasText("没有匹配的模型") || tt.HasText("没有可用的模型") {
		t.Fatalf("已渲染收藏卡片时不应再出现空状态提示: %v", tt.Texts())
	}
}

func TestModelPickerSearchHeaderNotClickableButton(t *testing.T) {
	providers := modelPickerTestProviders()
	providers[1].Models = append(providers[1].Models, ModelInfo{ID: "google-antigravity/claude-sonnet-4-6", Name: "Claude Sonnet 4.6"})
	state := &ModelPickerState{MenuOpen: true, searchQuery: "sonnet"}
	scope := "search-header-static"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "search-hdr-static-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !tt.HasText("Claude") || !tt.HasText("Codex") {
		t.Fatalf("搜索态应保留命中分组标题: %v", tt.Texts())
	}
	for _, acc := range tt.AccessAll() {
		if strings.HasPrefix(acc.Name, "折叠 ") || strings.HasPrefix(acc.Name, "展开 ") {
			t.Fatalf("搜索态分组标题不应包含折叠/展开按钮标签: %+v", acc)
		}
		if (acc.Name == "Claude" || acc.Name == "Codex") && (acc.Role == ui.RoleButton || acc.Focusable) {
			t.Fatalf("搜索态分组标题不应为可点击/可聚焦按钮: %+v", acc)
		}
	}

	// 搜索无匹配时展示「没有匹配的模型」。
	state.searchQuery = "nonexistent-model-xyz"
	tt.Frame()
	if !tt.HasText("没有匹配的模型") {
		t.Fatalf("搜索无匹配时应展示「没有匹配的模型」: %v", tt.Texts())
	}

	// 清空搜索词后恢复原先展开状态与可折叠标题按钮。
	state.searchQuery = ""
	tt.Frame()
	if !tt.HasText("Claude 3.7 Sonnet") || tt.HasText("o3-mini") {
		t.Fatalf("清空搜索词后应恢复仅展开当前 Agent: %v", tt.Texts())
	}
	if _, ok := tt.Find("折叠 Claude"); !ok {
		t.Fatalf("清空搜索词后分组标题应恢复为可折叠按钮: %v", tt.AccessAll())
	}
}

func TestMaxBeesMenuSelection(t *testing.T) {
	state := &ModelPickerState{}
	got := -1
	props := ModelPickerProps{
		KeyPrefix: "max-bees-test-",
		ScopeKey:  "max-bees-test",
		State:     state,
		MaxBees:   4,
		OnSetMaxBees: func(n int) {
			got = n
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("max-bees-test-wrap")).Width(320).Padding(Space4).Children(func() {
			renderModelPickerMaxBeesCapsule(c, props, state)
		})
	}, 360, 400)
	settleModelPickerPage(tt)

	if err := tt.Click("4 个"); err != nil {
		t.Fatalf("打开并发上限菜单失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("自动（8）") && magpieTestHasExactText(tt, "6 个")
	})
	clickWhenSettled(t, tt, "6 个")
	tt.Frame()

	if got != 6 {
		t.Fatalf("选中「6 个」后 OnSetMaxBees = %d，期望 6", got)
	}
	if state.MaxBeesOpen {
		t.Fatal("选中并发上限后应关闭菜单")
	}
}

func TestModelPickerQuotaLowRemainingHighlight(t *testing.T) {
	if text, low, tip := modelPickerQuotaDisplay(35); text != "35%" || low || !strings.Contains(tip, "剩余 65%") {
		t.Fatalf("modelPickerQuotaDisplay(35) = (%q, %v, %q)", text, low, tip)
	}
	if text, low, tip := modelPickerQuotaDisplay(90); text != "剩 10%" || !low || !strings.Contains(tip, "配额即将用尽") {
		t.Fatalf("modelPickerQuotaDisplay(90) = (%q, %v, %q)", text, low, tip)
	}
	if text, low, tip := modelPickerQuotaDisplay(100); text != "剩 0%" || !low || !strings.Contains(tip, "剩余 0%") {
		t.Fatalf("modelPickerQuotaDisplay(100) = (%q, %v, %q)", text, low, tip)
	}

	providers := modelPickerTestProviders()
	state := &ModelPickerState{}
	scope := "quota-low-highlight"
	state.setListOpen(scope, ModelPickerSideLead, true)
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "quota-low-",
		ScopeKey:  scope,
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"},
		QuotaUsed: func(id string) (float64, bool) {
			switch id {
			case "claude":
				return 35, true
			case "codex":
				return 90, true
			default:
				return 0, false
			}
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderModelPickerListPage(c, props, state, ModelPickerSideLead)
	}, 330, 640)
	settleModelPickerPage(tt)

	if !magpieTestHasExactText(tt, "35%") {
		t.Fatalf("35%% 配额应保持普通百分比文案: %v", tt.Texts())
	}
	if !magpieTestHasExactText(tt, "剩 10%") {
		t.Fatalf("90%% 配额应展示低余量告警文案「剩 10%%」: %v", tt.Texts())
	}
}

func TestModelPickerPairedRosterShowsBothModelsAndToolbarStar(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())

	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)
	win := &mygo.Window{}
	registerChatWindow(win, s)
	t.Cleanup(func() { unregisterChatWindow(win) })

	providers := modelPickerTestProviders()
	providers[0].Models[0].Efforts = []string{"low", "medium", "high"}
	providers[1].Models[0].Efforts = []string{"low", "medium", "high"}

	state := &ModelPickerState{MenuOpen: true}
	swarmOn := true
	props := func() ModelPickerProps {
		return ModelPickerProps{
			Mode:      ModelPickerSwarm,
			KeyPrefix: "paired-roster-",
			ScopeKey:  "paired-roster",
			State:     state,
			Providers: providers,
			SwarmOn:   swarmOn,
			Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet", Effort: "high"},
			Bees:      ModelSelection{Provider: "codex", Model: "o3-mini", Effort: "high"},
			OnToggleSwarm: func(on bool) {
				swarmOn = on
			},
		}
	}

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(modelPickerPanelWidth).Background(modelPickerPanelBg).Children(func() {
			renderModelPickerMenuBody(c, props(), state)
		})
	}, modelPickerPanelWidth, 400)
	tt.Frame()

	// 卡片页不再渲染顶部重复分段栏。
	if tt.HasText("主 · High") || tt.HasText("子 · High") {
		t.Fatalf("卡片页不应渲染顶部重复分段栏: %v", tt.Texts())
	}
	// 主/子两行同屏展示，且顶栏存在收藏星标。
	for _, want := range []string{"主", "Claude 3.7 Sonnet", "子", "o3-mini", "High"} {
		if !magpieTestHasExactText(tt, want) {
			t.Fatalf("缺少文案 %q: %v", want, tt.Texts())
		}
	}
	if _, ok := tt.Access("存为配对"); !ok {
		t.Fatalf("顶栏应渲染收藏配对按钮: %v", tt.AccessAll())
	}

	// 切到子代理行后，主/子模型继续同屏展示，且顶栏收藏星标依然可见并可点击保存。
	if err := tt.Click("子"); err != nil {
		t.Fatalf("点击子代理行失败: %v", err)
	}
	tt.Frame()
	if state.Tab != "bees" {
		t.Fatalf("切到子代理后 state.Tab = %q, want bees", state.Tab)
	}
	if !tt.HasText("Claude 3.7 Sonnet") || !tt.HasText("o3-mini") {
		t.Fatalf("切到子代理后主/子模型应同屏保留: %v", tt.Texts())
	}
	if err := tt.Click("存为配对"); err != nil {
		t.Fatalf("切到子代理后顶栏收藏星标仍应可点击: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Access("已保存为配对"); !ok {
		t.Fatalf("点击收藏后应切换为已保存为配对: %v", tt.AccessAll())
	}

	// 关闭编排后，子代理行收起只留主代理 1 行。
	if err := tt.Click("编排开关"); err != nil {
		t.Fatalf("点击编排开关关闭失败: %v", err)
	}
	tt.Frame()
	if swarmOn {
		t.Fatal("点击编排开关应关闭编排")
	}
	if !tt.HasText("Claude 3.7 Sonnet") || tt.HasText("o3-mini") || magpieTestHasExactText(tt, "子") {
		t.Fatalf("关闭编排后应只留主代理 1 行: %v", tt.Texts())
	}
}

func TestModelPickerResolveCanonicalizesPrefixedModel(t *testing.T) {
	providers := []ProviderInfo{{
		ID:        "magpie",
		Name:      "Magpie",
		Available: true,
		Models: []ModelInfo{{
			ID:            "auto/deepseek-v4.1-flash",
			Name:          "DeepSeek V4.1 Flash",
			Efforts:       []string{"low", "medium", "high"},
			DefaultEffort: "medium",
		}},
	}}

	gotMissingProv := resolveModelPickerSelectionWith(
		providers,
		ModelSelection{Provider: "magpie-jp", Model: "magpie-jp/auto/deepseek-v4.1-flash"},
		ModelSelection{},
		false,
		nil,
	)
	if gotMissingProv.Provider != "magpie" || gotMissingProv.Model != "auto/deepseek-v4.1-flash" {
		t.Fatalf("不存在的 provider 应归一到命中的目录项，实际 %+v", gotMissingProv)
	}

	gotEmptyProv := resolveModelPickerSelectionWith(
		providers,
		ModelSelection{Provider: "", Model: "magpie-jp/auto/deepseek-v4.1-flash"},
		ModelSelection{},
		false,
		nil,
	)
	if gotEmptyProv.Provider != "magpie" || gotEmptyProv.Model != "auto/deepseek-v4.1-flash" {
		t.Fatalf("空 provider 应归一到命中的目录项，实际 %+v", gotEmptyProv)
	}

	// provider 存在但不含该模型时，不跨 provider 改写，原样返回。
	providersWithOther := append([]ProviderInfo{{
		ID:        "claude",
		Name:      "Claude",
		Available: true,
		Models:    []ModelInfo{{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"}},
	}}, providers...)
	unmatched := ModelSelection{Provider: "claude", Model: "magpie-jp/auto/deepseek-v4.1-flash"}
	gotUnmatched := resolveModelPickerSelectionWith(providersWithOther, unmatched, ModelSelection{}, false, nil)
	if gotUnmatched != unmatched {
		t.Fatalf("provider 存在但不含该模型时应原样返回 %+v，实际 %+v", unmatched, gotUnmatched)
	}
}

func TestModelPickerSimpleUsesSingleCard(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(func() {
		modelPickerStatesMu.Lock()
		modelPickerStates = make(map[string]*ModelPickerState)
		modelPickerStatesMu.Unlock()
	})

	providers := modelPickerTestProviders()
	providers[0].Models[0].Efforts = []string{"low", "medium", "high"}
	state := &ModelPickerState{}
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "simple-single-",
		ScopeKey:  "simple-single",
		State:     state,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet", Effort: "medium"},
		Inline:    false,
		OnReset:   func(ModelPickerSide) {},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(420).Height(700).Children(func() {
			ui.Box(c).Grow(1)
			renderModelPicker(c, props)
		})
	}, 460, 740)
	tt.Frame()

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("打开模型选择器失败: %v", err)
	}
	settleModelPickerPage(tt)

	titleRect, ok := tt.Find("选择其他模型")
	if !ok {
		t.Fatalf("Simple 模式非 Inline 应渲染单卡标题按钮: texts=%v", tt.Texts())
	}
	resetRect, ok := tt.Find("重置强度与快速模式")
	if !ok {
		t.Fatalf("Simple 模式非 Inline 应渲染单卡重置按钮: texts=%v", tt.Texts())
	}
	// 单卡的重置按钮（24x24）紧跟在标题右侧同一行内，而不是顶部工具栏（26x26）独立占一行。
	if resetRect.W != effortResetButtonSize || resetRect.H != effortResetButtonSize {
		t.Fatalf("单卡重置按钮尺寸应为 %vx%v，实际 %+v", effortResetButtonSize, effortResetButtonSize, resetRect)
	}
	if resetRect.Y+resetRect.H <= titleRect.Y || resetRect.X < titleRect.X+titleRect.W-1 {
		t.Fatalf("单卡重置按钮应与标题同处一行右侧，不应落在顶部独立工具栏: title=%+v reset=%+v", titleRect, resetRect)
	}
	if tt.HasText("Med") || magpieTestHasExactText(tt, "主") {
		t.Fatalf("Simple 模式不应渲染 Roster 槽位文本: %v", tt.Texts())
	}
	if _, ok := tt.Find("打开模型列表"); ok {
		t.Fatal("Simple 模式不应渲染 Roster 槽位尾部按钮")
	}
	if _, ok := tt.Access("编排开关"); ok {
		t.Fatal("Simple 模式不应渲染 Swarm 工具栏编排开关")
	}
}
