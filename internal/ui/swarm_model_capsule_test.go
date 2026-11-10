package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/apisource"
	"mcode/internal/config"
)

func TestSwarmLegacySelectionShowsSource(t *testing.T) {
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{ID: "jp", Name: "日本来源", Enabled: true, Magpie: true},
	}}}
	st := &SettingsState{cfg: cfg}
	target := apisource.MagpieTargets(cfg, nil)[0]
	sel := normalizeSwarmSelection(st, ModelSelection{Provider: "mcode", Model: target.ProviderID + "/auto/model"})
	providers := []ProviderInfo{{ID: "custom-jp", Name: "日本来源", Models: []ModelInfo{{ID: "auto/model", Name: "model"}}}}
	if sel.Provider != "custom-jp" || sel.Model != "auto/model" {
		t.Fatalf("selection = %+v", sel)
	}
	if got := modelProviderDisplayName(providers, sel.Provider); got != "日本来源" {
		t.Fatalf("display = %q", got)
	}
}

// newSwarmCapsuleTester 构造带有 Mock 供应商及可用模型数据的 SettingsState 与 ui.Tester。
func newSwarmCapsuleTester(t *testing.T) (*SettingsState, *ui.Tester) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	t.Cleanup(func() {
		modelPickerStatesMu.Lock()
		modelPickerStates = make(map[string]*ModelPickerState)
		modelPickerStatesMu.Unlock()
	})

	fake := NewFakeClient()
	providers := []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true},
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
	fake.SetProviders(providers)

	state, _ := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	setChatSettings(state, st)

	state.mu.Lock()
	state.providers = append([]ProviderInfo(nil), providers...)
	state.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		provs := settingsModelPickerCatalog(st).Providers
		hasModels := false
		for _, p := range provs {
			if len(p.Models) > 0 {
				hasModels = true
				break
			}
		}
		if hasModels {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() {
			renderPageSwarm(c, st)
		})
	}, 1200, 6000)
	tt.Frame()

	return st, tt
}

// TestSwarmEmptySelection 验证空模型选择判定辅助函数。
func TestSwarmEmptySelection(t *testing.T) {
	if !swarmEmptySelection(ModelSelection{}) {
		t.Fatal("empty ModelSelection should be considered empty")
	}
	if !swarmEmptySelection(ModelSelection{Provider: "claude"}) {
		t.Fatal("selection with only provider should be considered empty")
	}
	if !swarmEmptySelection(ModelSelection{Provider: "claude", Model: "   "}) {
		t.Fatal("selection with whitespace model should be considered empty")
	}
	if swarmEmptySelection(ModelSelection{Provider: "claude", Model: "claude-3-7-sonnet"}) {
		t.Fatal("selection with valid model should not be empty")
	}
}

// TestSwarmModelCapsule_PairEditorLeadSelection 验证在配对编辑器中点击 Lead 胶囊、展开模型列表选择模型并保存持久化。
func TestSwarmModelCapsule_PairEditorLeadSelection(t *testing.T) {
	st, tt := newSwarmCapsuleTester(t)

	// 1. 打开模型配对编辑器
	if err := tt.Click("添加配对"); err != nil {
		t.Fatalf("click '添加配对' failed: %v", err)
	}
	tt.Frame()
	if !tt.HasText("添加模型配对") {
		t.Fatalf("expected '添加模型配对' popover to be open")
	}

	// 2. 按无障碍名点击主导模型胶囊
	if err := tt.Click("选择主代理模型"); err != nil {
		t.Fatalf("click '选择主代理模型' capsule failed: %v", err)
	}
	tt.Frame()

	// 3. 断言 fixture provider 中的模型名称出现在已展开的模型列表中
	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("expected 'Claude 3.7 Sonnet' to appear in model list: %v", tt.Texts())
	}

	// 4. 点击该模型完成选择
	if err := tt.Click("Claude 3.7 Sonnet"); err != nil {
		t.Fatalf("click 'Claude 3.7 Sonnet' failed: %v", err)
	}
	tt.Frame()

	// 5. 断言模型列表已收起（未选中的 o3-mini 不再可见），且 Lead 草稿已更新
	if tt.HasText("o3-mini") {
		t.Fatal("expected model list to be closed after picking model, but 'o3-mini' is still visible")
	}
	draft := getSwarmUIState(st).draft
	if draft.LeadModel != "claude-3-7-sonnet" || draft.LeadProvider != config.ProviderClaude {
		t.Fatalf("expected Lead draft to update with picked model, got prov=%q mdl=%q", draft.LeadProvider, draft.LeadModel)
	}

	// 6. 滚动到底部并保存（断言外层配对编辑器弹层保持打开）
	if !tt.HasText("添加模型配对") {
		t.Fatal("expected '添加模型配对' popover to remain open after picking model")
	}
	title, ok := tt.Find("添加模型配对")
	if !ok {
		t.Fatal("pair editor must be visible")
	}
	tt.Scroll(title.X+100, title.Y+60, 0, 10000)
	tt.Frame()

	if err := tt.Click("保存"); err != nil {
		t.Fatalf("click '保存' failed: %v", err)
	}
	tt.Frame()

	// 7. 从磁盘重新加载配置，验证 LeadModel 与 LeadProvider 已持久化保存
	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded.Orchestration.Pairs) != 1 {
		t.Fatalf("expected 1 pair saved, got %d", len(loaded.Orchestration.Pairs))
	}
	pair := loaded.Orchestration.Pairs[0]
	if pair.LeadModel != "claude-3-7-sonnet" {
		t.Fatalf("expected LeadModel to be 'claude-3-7-sonnet', got %q", pair.LeadModel)
	}
	if pair.LeadProvider != config.ProviderClaude {
		t.Fatalf("expected LeadProvider to be %q, got %q", config.ProviderClaude, pair.LeadProvider)
	}
}

// TestSwarmModelCapsule_TierMappingOverride 验证在配对编辑器中为深度档选择覆盖模型并保存持久化。
func TestSwarmModelCapsule_TierMappingOverride(t *testing.T) {
	st, tt := newSwarmCapsuleTester(t)

	// 1. 打开模型配对编辑器
	if err := tt.Click("添加配对"); err != nil {
		t.Fatalf("click '添加配对' failed: %v", err)
	}
	tt.Frame()
	title, ok := tt.Find("添加模型配对")
	if !ok {
		t.Fatal("pair editor must be visible")
	}

	// 2. 按无障碍名点击“深度”档位的映射胶囊
	if err := tt.Click("选择深度档模型"); err != nil {
		t.Fatalf("click '选择深度档模型' capsule failed: %v", err)
	}
	tt.Frame()

	// 3. 断言 fixture provider 中的模型名称出现在已展开的模型列表中
	if !tt.HasText("o3-mini") {
		t.Fatalf("expected 'o3-mini' to appear in tier model list: %v", tt.Texts())
	}

	// 4. 点击选择 o3-mini 模型
	if err := tt.Click("o3-mini"); err != nil {
		t.Fatalf("click 'o3-mini' failed: %v", err)
	}
	tt.Frame()

	// 5. 滚动到底部并保存（断言外层配对编辑弹层仍然可见）
	title, ok = tt.Find("添加模型配对")
	if !ok {
		t.Fatal("pair editor must be visible after picking tier model")
	}
	tt.Scroll(title.X+100, title.Y+60, 0, 10000)
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("click '保存' failed: %v", err)
	}
	tt.Frame()

	// 6. 从磁盘重新加载配置，验证 BeeTiers["deep"].Model 成功持久化
	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded.Orchestration.Pairs) != 1 {
		t.Fatalf("expected 1 pair saved, got %d", len(loaded.Orchestration.Pairs))
	}
	pair := loaded.Orchestration.Pairs[0]
	if pair.BeeTiers == nil {
		t.Fatalf("expected BeeTiers to be non-nil")
	}
	deepOverride, ok := pair.BeeTiers["deep"]
	if !ok {
		t.Fatalf("expected BeeTiers['deep'] override to exist, got: %+v", pair.BeeTiers)
	}
	if deepOverride.Model != "o3-mini" {
		t.Fatalf("expected BeeTiers['deep'].Model to be 'o3-mini', got %q", deepOverride.Model)
	}
	if deepOverride.Provider != config.ProviderCodex {
		t.Fatalf("expected BeeTiers['deep'].Provider to be %q, got %q", config.ProviderCodex, deepOverride.Provider)
	}
}

// TestSwarmModelCapsule_DefaultModel 验证全局协同模型的胶囊选择与跟随主代理清除。
func TestSwarmModelCapsule_DefaultModel(t *testing.T) {
	st, tt := newSwarmCapsuleTester(t)

	// 初始默认状态下显示“跟随主代理”
	if !tt.HasText("跟随主代理") {
		t.Fatalf("expected '跟随主代理' capsule text: %v", tt.Texts())
	}

	// 按无障碍名点击协同模型胶囊打开选择列表
	if err := tt.Click("选择子代理默认模型"); err != nil {
		t.Fatalf("click '选择子代理默认模型' capsule failed: %v", err)
	}
	tt.Frame()

	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("expected 'Claude 3.7 Sonnet' to appear in list: %v", tt.Texts())
	}
	if err := tt.Click("Claude 3.7 Sonnet"); err != nil {
		t.Fatalf("click 'Claude 3.7 Sonnet' failed: %v", err)
	}
	tt.Frame()

	// 断言模型选择器弹层已关闭，避免吞掉后续对清除按钮的点击
	stPicker := modelPickerStateFor("swarm-default-model-swarm-default-model")
	if stPicker.MenuOpen {
		t.Fatal("expected model picker popup to be closed after picking model")
	}
	if tt.HasText("o3-mini") {
		t.Fatal("expected model picker popup to be closed, but 'o3-mini' is still visible")
	}

	// 验证配置已更新并持久化
	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if loaded.Orchestration.DefaultModel != "claude-3-7-sonnet" || loaded.Orchestration.DefaultProvider != config.ProviderClaude {
		t.Fatalf("expected default model to persist, got prov=%q mdl=%q", loaded.Orchestration.DefaultProvider, loaded.Orchestration.DefaultModel)
	}

	// 点击“跟随主代理”按钮清除设置
	if err := tt.Click("跟随主代理"); err != nil {
		t.Fatalf("click '跟随主代理' clear button failed: %v", err)
	}
	tt.Frame()

	loaded2, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if loaded2.Orchestration.DefaultModel != "" || loaded2.Orchestration.DefaultProvider != "" {
		t.Fatalf("expected default model to be cleared, got prov=%q mdl=%q", loaded2.Orchestration.DefaultProvider, loaded2.Orchestration.DefaultModel)
	}
}

// TestSwarmModelCapsule_TierEditor 验证 Bee 档位编辑弹层中的模型胶囊及“跟随会话”重置按钮。
func TestSwarmModelCapsule_TierEditor(t *testing.T) {
	st, _ := newSwarmCapsuleTester(t)

	// 打开只读档位的编辑弹层
	uiSt := getSwarmTiersUIState(st)
	uiSt.openPopoverKey = "edit-read"
	uiSt.isNew = false
	for _, tier := range st.cfg.Orchestration.Tiers {
		if tier.ID == "read" {
			uiSt.draft = tier
			break
		}
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(360).Children(func() {
			renderSwarmTierEditorForm(c, st, uiSt, "edit-read")
		})
	}, 400, 1200)
	tt.Frame()

	// 初始状态下弹层打开
	_, ok := tt.Find("编辑子代理档位")
	if !ok {
		t.Fatal("expected tier editor popover to be open")
	}

	// 按无障碍名点击档位模型胶囊
	if err := tt.Click("选择档位模型"); err != nil {
		t.Fatalf("click '选择档位模型' capsule failed: %v", err)
	}
	tt.Frame()

	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("expected 'Claude 3.7 Sonnet' in tier editor list: %v", tt.Texts())
	}
	if err := tt.Click("Claude 3.7 Sonnet"); err != nil {
		t.Fatalf("click 'Claude 3.7 Sonnet' failed: %v", err)
	}
	tt.Frame()

	// 断言档位编辑弹层未被关闭
	if !tt.HasText("编辑子代理档位") {
		t.Fatal("tier editor popover was prematurely closed after nested model pick")
	}

	// 断言原位展开的模型列表已收起（未选中的 o3-mini 不再可见）
	if tt.HasText("o3-mini") {
		t.Fatal("expected tier model list to be closed after picking model, but 'o3-mini' is still visible")
	}

	if uiSt.draft.Model != "claude-3-7-sonnet" || uiSt.draft.Provider != config.ProviderClaude {
		t.Fatalf("expected draft to update with picked model, got prov=%q mdl=%q", uiSt.draft.Provider, uiSt.draft.Model)
	}

	// 点击保存并断言持久化
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("click '保存' failed: %v", err)
	}
	tt.Frame()

	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	var readTier *config.SwarmTier
	for i := range loaded.Orchestration.Tiers {
		if loaded.Orchestration.Tiers[i].ID == "read" {
			readTier = &loaded.Orchestration.Tiers[i]
			break
		}
	}
	if readTier == nil {
		t.Fatal("expected 'read' tier to exist in config")
	}
	if readTier.Model != "claude-3-7-sonnet" {
		t.Fatalf("expected read tier Model to be 'claude-3-7-sonnet', got %q", readTier.Model)
	}
	if readTier.Provider != config.ProviderClaude {
		t.Fatalf("expected read tier Provider to be %q, got %q", config.ProviderClaude, readTier.Provider)
	}
}

// TestSwarmPairEditor_StaysOpenAfterNestedPick 验证在配对编辑器内选模型后，
// 外层配对编辑器弹层保持打开状态，且 Lead 草稿正确更新，点击保存能正常持久化。
func TestSwarmPairEditor_StaysOpenAfterNestedPick(t *testing.T) {
	st, tt := newSwarmCapsuleTester(t)

	// 1. 打开模型配对编辑器
	if err := tt.Click("添加配对"); err != nil {
		t.Fatalf("click '添加配对' failed: %v", err)
	}
	tt.Frame()
	if !tt.HasText("添加模型配对") {
		t.Fatalf("expected '添加模型配对' popover to be open")
	}

	// 2. 点击主导模型胶囊
	if err := tt.Click("选择主代理模型"); err != nil {
		t.Fatalf("click '选择主代理模型' failed: %v", err)
	}
	tt.Frame()

	// 3. 点击选择模型
	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("expected 'Claude 3.7 Sonnet' in picker list: %v", tt.Texts())
	}
	if err := tt.Click("Claude 3.7 Sonnet"); err != nil {
		t.Fatalf("click 'Claude 3.7 Sonnet' failed: %v", err)
	}
	tt.Frame()

	// 4. 断言配对编辑器弹层依然打开，列表已收起，且 Lead 草稿已更新
	if !tt.HasText("添加模型配对") {
		t.Fatal("pair editor popover was prematurely closed after nested model pick")
	}
	if tt.HasText("o3-mini") {
		t.Fatal("expected model picker list to be closed after picking model, but 'o3-mini' is still visible")
	}
	uiSt := getSwarmUIState(st)
	if uiSt.draft.LeadModel != "claude-3-7-sonnet" {
		t.Fatalf("expected draft LeadModel to be 'claude-3-7-sonnet', got %q", uiSt.draft.LeadModel)
	}
	if uiSt.draft.LeadProvider != config.ProviderClaude {
		t.Fatalf("expected draft LeadProvider to be %q, got %q", config.ProviderClaude, uiSt.draft.LeadProvider)
	}

	// 5. 点击保存并验证持久化
	title, ok := tt.Find("添加模型配对")
	if !ok {
		t.Fatal("pair editor title must be visible")
	}
	tt.Scroll(title.X+100, title.Y+60, 0, 10000)
	tt.Frame()

	if err := tt.Click("保存"); err != nil {
		t.Fatalf("click '保存' failed: %v", err)
	}
	tt.Frame()

	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded.Orchestration.Pairs) != 1 {
		t.Fatalf("expected 1 pair saved, got %d", len(loaded.Orchestration.Pairs))
	}
	if loaded.Orchestration.Pairs[0].LeadModel != "claude-3-7-sonnet" {
		t.Fatalf("expected LeadModel to be 'claude-3-7-sonnet', got %q", loaded.Orchestration.Pairs[0].LeadModel)
	}
	if loaded.Orchestration.Pairs[0].LeadProvider != config.ProviderClaude {
		t.Fatalf("expected LeadProvider to be %q, got %q", config.ProviderClaude, loaded.Orchestration.Pairs[0].LeadProvider)
	}
}
