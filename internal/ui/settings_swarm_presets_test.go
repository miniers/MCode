package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSwarmPreset_AllAvailableReady(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	providers := []ProviderInfo{
		{
			ID:        config.ProviderClaude,
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "Fable", Name: "Fable"},
				{ID: "Opus", Name: "Opus"},
			},
		},
		{
			ID:        config.ProviderCodex,
			Name:      "Codex",
			Available: true,
			Models: []ModelInfo{
				{ID: "Astra", Name: "Astra"},
				{ID: "Terra", Name: "Terra"},
				{ID: "Sol", Name: "Sol"},
				{ID: "Luna", Name: "Luna"},
			},
		},
		{
			ID:        config.ProviderAgy,
			Name:      "Antigravity",
			Available: true,
			Models: []ModelInfo{
				{ID: "Gemini 3.8 Flash", Name: "Gemini 3.8 Flash"},
			},
		},
	}

	states := evaluateSwarmPresets(providers, nil)
	if len(states) != len(swarmPresets) {
		t.Fatalf("expected %d presets, got %d", len(swarmPresets), len(states))
	}

	supportedIDs := map[string]bool{
		"claude-fable-opus":        true,
		"claude-opus-gemini-flash": true,
		"astra-terra":              true,
		"astra-sol":                true,
		"sol-luna":                 true,
		"astra-astra":              true,
		"claude-opus-opus":         true,
		"claude-fable-fable":       true,
	}

	for _, s := range states {
		if supportedIDs[s.Preset.ID] {
			if !s.Ready {
				t.Errorf("preset %s expected Ready, got Waiting with missing: %v", s.Preset.ID, s.Missing)
			}
			if len(s.Missing) != 0 {
				t.Errorf("preset %s expected empty missing, got %v", s.Preset.ID, s.Missing)
			}
		} else {
			if s.Ready {
				t.Errorf("unsupported preset %s expected Waiting, got Ready", s.Preset.ID)
			}
			if len(s.Missing) == 0 {
				t.Errorf("unsupported preset %s expected non-empty Missing", s.Preset.ID)
			}
		}
	}
}

func TestSwarmPreset_CodexUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	providers := []ProviderInfo{
		{
			ID:        config.ProviderClaude,
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "Fable", Name: "Fable"},
				{ID: "Opus", Name: "Opus"},
			},
		},
		{
			ID:        config.ProviderCodex,
			Name:      "Codex",
			Available: false, // 设为不可用
			Models: []ModelInfo{
				{ID: "Astra", Name: "Astra"},
				{ID: "Terra", Name: "Terra"},
				{ID: "Sol", Name: "Sol"},
				{ID: "Luna", Name: "Luna"},
			},
		},
		{
			ID:        config.ProviderAgy,
			Name:      "Antigravity",
			Available: true,
			Models: []ModelInfo{
				{ID: "Gemini 3.8 Flash", Name: "Gemini 3.8 Flash"},
			},
		},
	}

	states := evaluateSwarmPresets(providers, nil)

	codexPresetIDs := map[string]bool{
		"astra-spark": true,
		"astra-terra": true,
		"astra-sol":   true,
		"sol-luna":    true,
		"astra-astra": true,
	}

	for _, s := range states {
		if codexPresetIDs[s.Preset.ID] {
			if s.Ready {
				t.Errorf("preset %s with codex unavailable expected Waiting, got Ready", s.Preset.ID)
			}
			if len(s.Missing) == 0 {
				t.Errorf("preset %s expected non-empty Missing", s.Preset.ID)
			}
			foundCodexNote := false
			for _, m := range s.Missing {
				if strings.Contains(strings.ToLower(m), "codex") {
					foundCodexNote = true
					break
				}
			}
			if !foundCodexNote {
				t.Errorf("preset %s missing list %v does not contain Codex note", s.Preset.ID, s.Missing)
			}
		}
	}
}

func TestSwarmPreset_AddedDetection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	target := swarmPresets[0] // claude-fable-opus
	existing := []config.SwarmPair{
		{
			ID:             "existing-pair-1",
			Name:           "Existing Pair",
			LeadProvider:   target.Pair.LeadProvider,
			LeadModel:      target.Pair.LeadModel,
			WorkerProvider: target.Pair.WorkerProvider,
			WorkerModel:    target.Pair.WorkerModel,
		},
	}

	states := evaluateSwarmPresets(nil, existing)
	if len(states) == 0 {
		t.Fatalf("expected states, got empty")
	}

	foundTarget := false
	for _, s := range states {
		if s.Preset.ID == target.ID {
			foundTarget = true
			if !s.Added {
				t.Errorf("preset %s should have Added=true", s.Preset.ID)
			}
		} else {
			if s.Added {
				t.Errorf("preset %s should have Added=false", s.Preset.ID)
			}
		}
	}
	if !foundTarget {
		t.Fatalf("target preset %s not found in states", target.ID)
	}
}

func TestSwarmPreset_AddPresetPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	preset := swarmPresets[1] // claude-opus-gemini-flash
	added := addSwarmPreset(st, preset)

	if added.ID == "" {
		t.Fatalf("expected added pair to have non-empty ID")
	}
	if added.LeadProvider != preset.Pair.LeadProvider || added.LeadModel != preset.Pair.LeadModel {
		t.Errorf("added pair lead mismatch: got %s/%s, expected %s/%s",
			added.LeadProvider, added.LeadModel, preset.Pair.LeadProvider, preset.Pair.LeadModel)
	}
	if added.WorkerProvider != preset.Pair.WorkerProvider || added.WorkerModel != preset.Pair.WorkerModel {
		t.Errorf("added pair worker mismatch: got %s/%s, expected %s/%s",
			added.WorkerProvider, added.WorkerModel, preset.Pair.WorkerProvider, preset.Pair.WorkerModel)
	}

	// 检查内存状态
	foundInMem := false
	for _, p := range st.cfg.Orchestration.Pairs {
		if p.ID == added.ID {
			foundInMem = true
			break
		}
	}
	if !foundInMem {
		t.Errorf("added pair %s not found in st.cfg.Orchestration.Pairs", added.ID)
	}

	// 验证持久化：从磁盘重新加载配置
	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	foundOnDisk := false
	for _, p := range loaded.Orchestration.Pairs {
		if p.ID == added.ID {
			foundOnDisk = true
			if p.LeadProvider != added.LeadProvider || p.WorkerProvider != added.WorkerProvider {
				t.Errorf("persisted pair mismatch: got %s/%s, expected %s/%s",
					p.LeadProvider, p.WorkerProvider, added.LeadProvider, added.WorkerProvider)
			}
			break
		}
	}
	if !foundOnDisk {
		t.Errorf("added pair %s not found in loaded config.json", added.ID)
	}
}

func TestSwarmPreset_MissingModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	providers := []ProviderInfo{
		{
			ID:        config.ProviderClaude,
			Name:      "Claude",
			Available: true,
			Models:    nil, // 无模型
		},
	}

	states := evaluateSwarmPresets(providers, nil)
	var fableOpus swarmPresetState
	for _, s := range states {
		if s.Preset.ID == "claude-fable-opus" {
			fableOpus = s
			break
		}
	}

	if fableOpus.Ready {
		t.Errorf("expected claude-fable-opus to be Waiting when models are missing")
	}
	foundMissingModel := false
	for _, m := range fableOpus.Missing {
		if strings.Contains(m, "缺少模型") {
			foundMissingModel = true
			break
		}
	}
	if !foundMissingModel {
		t.Errorf("expected Missing to contain '缺少模型', got %v", fableOpus.Missing)
	}
}

func TestSwarmPreset_ResolvesCatalogIDsAndSupportedEfforts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	providers := []ProviderInfo{
		{ID: config.ProviderClaude, Available: true, Models: []ModelInfo{
			{ID: "claude-opus-5-4", Name: "Opus 5.4", Efforts: []string{"high"}},
			{ID: "claude-opus-5-5", Name: "Opus 5.5", Efforts: []string{"medium", "high"}},
			{ID: "claude-opus-5-5-tie", Name: "Opus 5.5"},
		}},
		{ID: config.ProviderAgy, Available: true, Models: []ModelInfo{
			{ID: "gemini-3-flash", Name: "Gemini 3 Flash"},
		}},
	}
	for _, state := range evaluateSwarmPresets(providers, nil) {
		switch state.Preset.ID {
		case "claude-opus-gemini-flash":
			pair := state.Preset.Pair
			if !state.Ready || pair.LeadModel != "claude-opus-5-5" || pair.WorkerModel != "gemini-3-flash" {
				t.Fatalf("preset must resolve catalog IDs and Gemini fallback: %+v", state)
			}
			if pair.LeadEffort == nil || *pair.LeadEffort != "high" || pair.WorkerEffort != nil {
				t.Fatalf("preset must retain only supported efforts: %+v", pair)
			}
			st := NewSettingsState(settingsTestDir(t), nil, nil)
			addSwarmPreset(st, state.Preset)
			loaded, err := config.Load(st.dataDir)
			if err != nil || len(loaded.Orchestration.Pairs) != 1 || loaded.Orchestration.Pairs[0].WorkerModel != "gemini-3-flash" {
				t.Fatalf("resolved model ID must persist: %+v, %v", loaded.Orchestration.Pairs, err)
			}
		case "claude-opus-opus":
			if state.Preset.Pair.LeadEffort != nil {
				t.Fatal("unsupported max effort must use model default")
			}
		}
	}
	// 解析不能修改全局预设组合模板。
	if swarmPresets[1].Pair.LeadModel != "Opus" || *swarmPresets[1].Pair.WorkerEffort != "medium" {
		t.Fatal("preset template was mutated")
	}
}

func TestSwarmPreset_AddedWithInheritedWorkerProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pair := swarmPresets[0].Pair
	existing := pair
	existing.WorkerProvider = ""
	if !isSwarmPairAdded(pair, []config.SwarmPair{existing}) {
		t.Fatal("same-provider preset must recognize inherited worker provider")
	}
}

func TestSwarmPreset_ReadyPresetClickPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	providers := []ProviderInfo{
		{ID: config.ProviderClaude, Available: true, Models: []ModelInfo{
			{ID: "claude-fable-5-1", Name: "Fable 5.1"},
			{ID: "opus", Name: "Opus"},
		}},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmPresetsPanel(c, st, providers)
	}, 900, 900)
	tt.Frame()
	if err := tt.Click("预设组合"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("就绪") {
		t.Fatal("available preset must be ready")
	}
	if err := tt.Click("添加配对"); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil || len(loaded.Orchestration.Pairs) != 1 || loaded.Orchestration.Pairs[0].LeadModel != "claude-fable-5-1" {
		t.Fatalf("preset click must persist resolved pair: %+v, %v", loaded.Orchestration.Pairs, err)
	}
	if err := tt.Click("预设组合"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("已添加") {
		t.Fatal("added preset must no longer offer another add action")
	}
}

func TestSwarmPreset_Flyout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entry, capture := setupPopoverFlyoutTest(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	providers := []ProviderInfo{{ID: config.ProviderClaude, Available: true, Models: []ModelInfo{
		{ID: "fable", Name: "Fable"}, {ID: "opus", Name: "Opus"},
	}}}
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		renderSwarmPresetsPanel(c, st, providers)
	}, 900, 900)
	if err := host.Click("预设组合"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Width != int(settingsPresetPopoverWidth) || capture.options[0].Content == nil {
		t.Fatalf("浮层参数错误: %+v", capture.options)
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "swarm-preset-popover"), 480, 4000)
	first, ok := menu.Find(swarmPresets[0].Title)
	if !ok {
		t.Fatalf("缺少预设: %v", menu.Texts())
	}
	last, ok := menu.Find(swarmPresets[len(swarmPresets)-1].Title)
	if !ok || last.Y-first.Y <= 520 {
		t.Fatalf("预设列表未按自然高度展开: 首项 %+v，末项 %+v", first, last)
	}
	if err := menu.Click("添加配对"); err != nil {
		t.Fatal(err)
	}
	if getSwarmUIState(st).presetOpen || len(capture.destroyedWindows) != 1 {
		t.Fatal("应用预设后未关闭浮层")
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil || len(loaded.Orchestration.Pairs) != 1 {
		t.Fatalf("预设未保存: %+v, %v", loaded.Orchestration.Pairs, err)
	}
}
