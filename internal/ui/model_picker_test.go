package mcodeui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/magpieinject"
)

func TestModelPickerHideMagpieModels(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers.HideMagpieModelsInPicker = true
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "magpie", Name: "网关", Enabled: true, Magpie: true},
		{ID: "second", Name: "网关", Enabled: true, Magpie: true},
		{ID: "normal", Name: "Magpie 普通端点", Enabled: true},
	}
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{
		"claude": {Mode: config.MagpieModeGlobal, Selections: []config.MagpieSelection{{EndpointID: "magpie", Models: []string{"shared"}}}},
		"codex": {Mode: config.MagpieModeGlobal, Selections: []config.MagpieSelection{{EndpointID: "second", Models: []string{"shared"}}}},
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
	// 独立直连 Magpie 的端点整体隐藏；pi/claude/codex 下的网关注入行全部保留。
	want := []ProviderInfo{
		{ID: "custom-normal", Models: []ModelInfo{{ID: "ordinary"}}},
		{ID: "pi", Models: piModels},
		{ID: "claude", Models: claudeModels},
		{ID: "codex", Models: codexModels},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("过滤结果 = %#v，期望 %#v", got, want)
	}
	if !reflect.DeepEqual(providers, original) {
		t.Fatal("过滤修改了原始模型列表")
	}
	cfg.Providers.HideMagpieModelsInPicker = false
	if got := filterModelPickerMagpie(providers, newModelPickerMagpieFilter(cfg)); !reflect.DeepEqual(got, original) {
		t.Fatal("关闭隐藏后应恢复全部模型")
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
	if got := swarmSideTabLabel("bees", "high", true); got != "子代理 · 关闭" {
		t.Fatalf("bees off = %q", got)
	}
	if got := swarmSideTabLabel("bees", "high", false); got != "子代理 · High" {
		t.Fatalf("bees on = %q", got)
	}
	if got := swarmSideTabLabel("bees", "", false); got != "子代理" {
		t.Fatalf("bees without effort = %q", got)
	}
	if got := swarmSideTabLabel("lead", "medium", false); got != "主代理 · Medium" {
		t.Fatalf("lead with effort = %q", got)
	}
	if got := swarmSideTabLabel("lead", "", false); got != "主代理" {
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
	t.Setenv("HOME", t.TempDir())
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
	// 档位名只以英文大标题呈现，“当前：”行与轨道下方的档位文字都不再出现。
	if !tt.HasText("High") {
		t.Fatalf("expected tier title High, got %v", tt.Texts())
	}
	if tt.HasText("当前：") || tt.HasText("低") || tt.HasText("超高") || tt.HasText("Low") {
		t.Fatalf("unexpected tier list text in card, got %v", tt.Texts())
	}
}

// 测试模型行：默认行显示实际解析出的模型名，不再带“默认”徽章。
func TestRenderModelRow_DefaultIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	providers := modelPickerTestProviders()
	props := ModelPickerProps{KeyPrefix: "t-row-"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(300).Children(func() {
			renderModelRow(c, props, modelRowOptions{
				Provider:     providers[0],
				Model:        providers[0].Models[0],
				ShowSubtitle: true,
				SubtitleText: providers[0].Name,
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
	if ready := readySwarmPairs(pairs, providers, pins); len(ready) != 1 || ready[0].ID != "pinned-lead" {
		t.Fatalf("pin 应让配对保留，got %+v", ready)
	}
	if ready := readySwarmPairs(pairs, providers, nil); len(ready) != 0 {
		t.Fatalf("未固定时配对应按原逻辑被过滤，got %+v", ready)
	}
}

// Swarm 开关异步生效：点执行段后 SwarmOn 仍为 false 的那几帧不能把 Tab 打回主导，开关生效后要切到执行侧。
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
	}, 330, 400)
	tt.Frame()
	if err := tt.Click("子代理 · 关闭"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !toggled {
		t.Fatal("点执行段应请求开启编排")
	}
	if state.Tab != "bees" {
		t.Fatalf("Swarm 尚未生效时 Tab = %q, want bees", state.Tab)
	}
	swarmOn = true
	tt.Frame()
	if state.Tab != "bees" {
		t.Fatalf("Swarm 生效后 Tab = %q, want bees", state.Tab)
	}
}

// pi 这类 CLI 的模型 id 本身带斜杠（目录里是 magpie/jp/group/…），会话里可能存着去掉
// provider 前缀的裸 id。两种形状都要查到目录行，否则推理强度卡片会退化成
// “该模型未提供可调的推理强度”，用户看不到滑杆。
func TestModelPickerCard_PiSlashModelIDKeepsEfforts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
			if tt.HasText("该模型未提供可调的推理强度") {
				t.Fatalf("模型 %q 应渲染滑杆，而不是空态文案: %v", model, tt.Texts())
			}
			if !tt.HasText("Low") {
				t.Fatalf("模型 %q 的卡片应显示默认档位 Low，实际 %v", model, tt.Texts())
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
			if !tt.HasText("Claude 3.7 Sonnet") || tt.HasText("Magpie Old") != present || tt.HasText("magpie/old") {
				t.Fatalf("一级页收藏显示不符合目录状态：%v", tt.Texts())
			}
		})
	}
}

// 二级列表默认只展示当前渠道的主模型，其余收进分组底部的“更多模型（N）”，点击后展开。
func TestRenderModelListSecondaryModelsCollapsed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		setModelPanelProviderMoreOpen("t-secondary", "lead", "cline", false)
	})

	providers := []ProviderInfo{{
		ID:        "cline",
		Name:      "Cline",
		Available: true,
		Models: []ModelInfo{
			{ID: "cline-pass/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", IsDefault: true},
			{ID: "cline-pass/opus-5", Name: "Opus 5"},
			{ID: "cline-free/glm-5.3", Name: "GLM-5.3", Secondary: true},
			{ID: "cline-free/kimi-k3", Name: "Kimi K3", Secondary: true},
		},
	}}
	props := ModelPickerProps{KeyPrefix: "t-secondary-", ScopeKey: "t-secondary"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(300).Children(func() {
			renderModelList(c, props, "lead-more-", "lead", providers, "",
				func(ProviderInfo, int, ModelInfo) bool { return false },
				func(ProviderInfo) bool { return false },
				nil)
		})
	}, 320, 640)
	tt.Frame()

	if !tt.HasText("DeepSeek V4.1 Flash") || !tt.HasText("Opus 5") {
		t.Fatalf("主模型应默认展示: %v", tt.Texts())
	}
	if !tt.HasText("更多模型（2）") {
		t.Fatalf("次要模型应收进“更多模型（2）”: %v", tt.Texts())
	}
	if tt.HasText("GLM-5.3") || tt.HasText("Kimi K3") {
		t.Fatalf("折叠时不应展示次要模型: %v", tt.Texts())
	}
	if err := tt.Click("更多模型（2）"); err != nil {
		t.Fatalf("点击更多模型行失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("GLM-5.3") && tt.HasText("Kimi K3") })
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
	t.Setenv("HOME", t.TempDir())
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

// 可用但模型目录为空的供应商在选择器里给出原因和“重新探测”入口；
// 没有原因文案的空目录仍不画分组，搜索态也不画说明块。
func TestRenderModelList_ProviderCatalogNotice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() {
		modelPickerStatesMu.Lock()
		modelPickerStates = make(map[string]*ModelPickerState)
		modelPickerStatesMu.Unlock()
	})

	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, UnavailableReason: "未安装 Claude Code CLI"},
		{ID: "codex", Name: "Codex", Available: true},
	}
	render := func(props ModelPickerProps, query string) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(320).Children(func() {
				renderModelList(c, props, "lead-more-", "lead", providers, query,
					func(ProviderInfo, int, ModelInfo) bool { return false },
					func(ProviderInfo) bool { return false },
					nil)
			})
		}, 340, 360)
		tt.Frame()
		return tt
	}

	refreshed := 0
	props := ModelPickerProps{
		Mode:      ModelPickerSimple,
		KeyPrefix: "t-notice-",
		ScopeKey:  "t-notice",
		State:     &ModelPickerState{},
		Providers: providers,
		OnRefreshCatalog: func() {
			refreshed++
		},
	}
	tt := render(props, "")
	if !tt.HasText("Claude · 未安装 Claude Code CLI") {
		t.Fatalf("应展示供应商与目录原因: %v", tt.Texts())
	}
	if tt.HasText("Codex") {
		t.Fatalf("没有原因文案的空目录不应画分组: %v", tt.Texts())
	}
	if err := tt.Click("重新探测"); err != nil {
		t.Fatalf("点击重新探测失败: %v", err)
	}
	tt.Frame()
	if refreshed != 1 {
		t.Fatalf("重新探测回调应被调用一次，实际 %d", refreshed)
	}

	// 搜索态保持原样：不画说明块。
	if search := render(props, "claude"); search.HasText("重新探测") {
		t.Fatalf("搜索态不应画重新探测入口: %v", search.Texts())
	}

	// 没有传 OnRefreshCatalog 时说明仍在，但不画按钮。
	props.OnRefreshCatalog = nil
	noButton := render(props, "")
	if !noButton.HasText("Claude · 未安装 Claude Code CLI") {
		t.Fatalf("未传回调时仍应展示原因: %v", noButton.Texts())
	}
	if noButton.HasText("重新探测") {
		t.Fatalf("未传 OnRefreshCatalog 时不应画重新探测按钮: %v", noButton.Texts())
	}
}

// 内置 mcode 分组只在当前这一侧本身就选了 mcode 时出现。
func TestFilterLegacyMcodeProviders(t *testing.T) {
	providers := []ProviderInfo{{ID: "mcode"}, {ID: "claude"}}
	if got := filterLegacyMcodeProviders(providers, "mcode"); len(got) != 2 {
		t.Fatalf("mcode 会话应保留 mcode 分组：%+v", got)
	}
	got := filterLegacyMcodeProviders(providers, "claude")
	if len(got) != 1 || got[0].ID != "claude" {
		t.Fatalf("其他会话不应出现 mcode 分组：%+v", got)
	}
}
