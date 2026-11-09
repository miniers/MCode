package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// currentSwarmPair 按 Lead 与 Bees 配置精确匹配配对模板。
func TestCurrentSwarmPair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pairs := []SwarmPair{
		{
			ID:             "pair-1",
			Name:           "深度思考",
			LeadProvider:   "claude",
			LeadModel:      "claude-3-7-sonnet",
			WorkerProvider: "codex",
			WorkerModel:    "o3-mini",
		},
	}

	// 精确匹配。
	th := Thread{BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok := currentSwarmPair(pairs, th, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-1" {
		t.Fatalf("expected pair-1 match, got %+v ok=%v", got, ok)
	}

	// Lead 不匹配。
	if _, ok := currentSwarmPair(pairs, th, "codex", "o3-mini"); ok {
		t.Fatalf("lead mismatch should not match")
	}

	// Bees 模型不匹配。
	thOther := Thread{BeesProvider: "codex", BeesModel: "gpt-5"}
	if _, ok := currentSwarmPair(pairs, thOther, "claude", "claude-3-7-sonnet"); ok {
		t.Fatalf("bees mismatch should not match")
	}

	// Bees 跟随 Lead：WorkerProvider 空回退 LeadProvider，WorkerModel 空跟随配对 LeadModel。
	followPairs := []SwarmPair{
		{
			ID:           "pair-follow",
			LeadProvider: "claude",
			LeadModel:    "claude-3-7-sonnet",
		},
	}
	got, ok = currentSwarmPair(followPairs, Thread{}, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-follow" {
		t.Fatalf("follow-lead pair should match, got %+v ok=%v", got, ok)
	}

	// 多个匹配时返回第一个。
	twoPairs := append([]SwarmPair{{
		ID:             "pair-0",
		LeadProvider:   "claude",
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: "codex",
		WorkerModel:    "o3-mini",
	}}, pairs...)
	got, ok = currentSwarmPair(twoPairs, th, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-0" {
		t.Fatalf("expected first matching pair, got %+v ok=%v", got, ok)
	}
}

// 会话显式选中配对时按 PairID 认定：同组合的多条配对可分别高亮，模型被换掉时回退到按模型推断。
func TestCurrentSwarmPair_PrefersThreadPairID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := SwarmPair{
		ID:             "pair-1",
		LeadProvider:   "claude",
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: "codex",
		WorkerModel:    "o3-mini",
	}
	// 同组合的第二条配对：模型完全一致，只能靠 PairID 区分。
	second := first
	second.ID = "pair-2"
	second.Name = "同组合第二条"
	otherLead := SwarmPair{
		ID:             "pair-3",
		LeadProvider:   "claude",
		LeadModel:      "claude-4-opus",
		WorkerProvider: "codex",
		WorkerModel:    "o3-mini",
	}
	pairs := []SwarmPair{first, second, otherLead}

	// PairID 指向第二条：返回第二条，而不是列表里的第一条。
	thSecond := Thread{PairID: "pair-2", BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok := currentSwarmPair(pairs, thSecond, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-2" {
		t.Fatalf("expected pair-2 by thread PairID, got %+v ok=%v", got, ok)
	}

	// PairID 为空：回退到按模型推断，取第一条。
	thNone := Thread{BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok = currentSwarmPair(pairs, thNone, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-1" {
		t.Fatalf("expected inferred pair-1, got %+v ok=%v", got, ok)
	}

	// PairID 对应的配对与当前 Lead 模型不一致：改按模型推断。
	thStale := Thread{PairID: "pair-2", BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok = currentSwarmPair(pairs, thStale, "claude", "claude-4-opus")
	if !ok || got.ID != "pair-3" {
		t.Fatalf("stale PairID should fall back to inference, got %+v ok=%v", got, ok)
	}

	// 显式配对的 Lead 一致但 Bees 模型已换：同样回退到按新模型推断出的配对。
	beesSwapped := first
	beesSwapped.ID = "pair-bees"
	beesSwapped.WorkerModel = "gpt-5"
	swapPairs := []SwarmPair{first, beesSwapped}
	thSwap := Thread{PairID: "pair-1", BeesProvider: "codex", BeesModel: "gpt-5"}
	got, ok = currentSwarmPair(swapPairs, thSwap, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-bees" {
		t.Fatalf("swapped bees model should fall back to pair-bees, got %+v ok=%v", got, ok)
	}

	// Bees 也一致时仍返回显式配对。
	thBeesOK := Thread{PairID: "pair-1", BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok = currentSwarmPair(swapPairs, thBeesOK, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-1" {
		t.Fatalf("matching bees model should keep explicit pair-1, got %+v ok=%v", got, ok)
	}

	// 同组合的两条配对显式选第二条：返回第二条。
	thSameCombo := Thread{PairID: "pair-2", BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok = currentSwarmPair(pairs, thSameCombo, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-2" {
		t.Fatalf("same-combo PairID should return the second pair, got %+v ok=%v", got, ok)
	}

	// 配对 LeadModel 为空视为通配：只要求 LeadProvider 相等，Bees 仍需一致。
	wildcard := SwarmPair{ID: "pair-wild", LeadProvider: "claude", WorkerProvider: "codex", WorkerModel: "o3-mini"}
	thWild := Thread{PairID: "pair-wild", BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok = currentSwarmPair([]SwarmPair{wildcard}, thWild, "claude", "claude-4-opus")
	if !ok || got.ID != "pair-wild" {
		t.Fatalf("wildcard LeadModel pair should match by PairID, got %+v ok=%v", got, ok)
	}

	// 通配配对仍需 Bees 一致：Bees 模型对不上时不认定。
	thWildMismatch := Thread{PairID: "pair-wild", BeesProvider: "codex", BeesModel: "gpt-5"}
	if _, ok := currentSwarmPair([]SwarmPair{wildcard}, thWildMismatch, "claude", "claude-4-opus"); ok {
		t.Fatalf("wildcard pair with mismatched bees model should not match")
	}

	// 推断时精确匹配优先于通配：通配配对在列表前面也不抢先。
	thWildInfer := Thread{BeesProvider: "codex", BeesModel: "o3-mini"}
	got, ok = currentSwarmPair([]SwarmPair{wildcard, first}, thWildInfer, "claude", "claude-3-7-sonnet")
	if !ok || got.ID != "pair-1" {
		t.Fatalf("exact pair should win over wildcard, got %+v ok=%v", got, ok)
	}
}

// readySwarmPairs 只保留两侧 provider 与模型都能解析的配对。
func TestReadySwarmPairs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	providers := []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models:    []ModelInfo{{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"}},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models:    []ModelInfo{{ID: "o3-mini", Name: "o3-mini"}},
		},
	}
	pairs := []SwarmPair{
		{ID: "ready", LeadProvider: "claude", LeadModel: "claude-3-7-sonnet", WorkerProvider: "codex", WorkerModel: "o3-mini"},
		{ID: "missing-lead-provider", LeadProvider: "gemini", LeadModel: "gemini-3-pro", WorkerProvider: "codex", WorkerModel: "o3-mini"},
		{ID: "missing-worker-provider", LeadProvider: "claude", LeadModel: "claude-3-7-sonnet", WorkerProvider: "pi", WorkerModel: "pi-1"},
		{ID: "missing-lead-model", LeadProvider: "claude", LeadModel: "claude-4-opus", WorkerProvider: "codex", WorkerModel: "o3-mini"},
		{ID: "missing-worker-model", LeadProvider: "claude", LeadModel: "claude-3-7-sonnet", WorkerProvider: "codex", WorkerModel: "gpt-5"},
	}

	ready := readySwarmPairs(config.Config{}, pairs, providers, nil)
	if len(ready) != 1 || ready[0].ID != "ready" {
		t.Fatalf("expected only 'ready', got %+v", ready)
	}
}

func TestReadySwarmPairsLegacyMcodeWorker(t *testing.T) {
	cfg := config.Config{}
	cfg.Providers.Custom = []config.CustomEndpointConfig{{ID: "432db46a", Name: "magpie_jp", Enabled: true, Magpie: true}}
	providers := []ProviderInfo{
		{ID: "claude", Models: []ModelInfo{{ID: "sonnet"}}},
		{ID: "custom-432db46a", Models: []ModelInfo{{ID: "auto/deepseek-v4.1-flash"}}},
	}
	pair := SwarmPair{ID: "legacy", LeadProvider: "claude", LeadModel: "sonnet", WorkerProvider: "mcode", WorkerModel: "magpie-jp/auto/deepseek-v4.1-flash", BeeTiers: map[string]config.SwarmTierOverride{"deep": {Provider: "mcode", Model: "magpie-jp/auto/other"}}}
	ready := readySwarmPairs(cfg, []SwarmPair{pair}, providers, nil)
	if len(ready) != 1 || ready[0].WorkerProvider != pair.WorkerProvider || ready[0].WorkerModel != pair.WorkerModel || ready[0].BeeTiers["deep"] != pair.BeeTiers["deep"] {
		t.Fatalf("旧配对应就绪并保留原始执行侧：%+v", ready)
	}

	disabled := cfg
	disabled.Providers.Custom = []config.CustomEndpointConfig{{ID: "432db46a", Name: "magpie_jp", Magpie: true}}
	if ready := readySwarmPairs(disabled, []SwarmPair{pair}, providers, nil); len(ready) != 0 {
		t.Fatalf("端点禁用时不应就绪：%+v", ready)
	}
	pins := []ModelPin{{Provider: pair.WorkerProvider, ModelID: pair.WorkerModel}}
	if ready := readySwarmPairs(cfg, []SwarmPair{pair}, providers[:1], pins); len(ready) != 0 {
		t.Fatalf("目标来源不存在时固定模型不能放行：%+v", ready)
	}
	otherModels := []ProviderInfo{providers[0], {ID: "custom-432db46a", Models: []ModelInfo{{ID: "other"}}}}
	if ready := readySwarmPairs(cfg, []SwarmPair{pair}, otherModels, pins); len(ready) != 1 {
		t.Fatalf("原始来源的固定模型应放行：%+v", ready)
	}
	unknown := pair
	unknown.WorkerModel = "unknown/auto/deepseek-v4.1-flash"
	if ready := readySwarmPairs(cfg, []SwarmPair{unknown}, providers, nil); len(ready) != 0 {
		t.Fatalf("未知旧来源不应就绪：%+v", ready)
	}
	lead := SwarmPair{ID: "lead", LeadProvider: "mcode", LeadModel: pair.WorkerModel}
	if ready := readySwarmPairs(cfg, []SwarmPair{lead}, providers, nil); len(ready) != 0 {
		t.Fatalf("主导侧原来源不存在时不应就绪：%+v", ready)
	}
	withMcode := append(append([]ProviderInfo(nil), providers...), ProviderInfo{ID: "mcode"})
	if ready := readySwarmPairs(cfg, []SwarmPair{lead}, withMcode, nil); len(ready) != 1 || ready[0].WorkerModel != "" {
		t.Fatalf("执行侧应跟随主导侧并保留原值：%+v", ready)
	}
}

// 会话开启 Swarm 时联动打开全局总开关；关闭时不动全局开关；锁定时不写全局。
func TestEnableSwarmWithGlobal_EnablesGlobalSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: false})
	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	off := false
	st.cfg.Orchestration.Enabled = &off
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == "t1"
	})

	state.enableSwarmWithGlobal(true)

	if !st.cfg.Orchestration.IsEnabled() {
		t.Fatalf("global swarm switch should be turned on")
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.setSwarmEnabledCalls) == 1 &&
			fake.setSwarmEnabledCalls[0].ThreadID == "t1" &&
			fake.setSwarmEnabledCalls[0].Enabled
	})

	// 关闭会话 Swarm 时不改全局开关。
	state.enableSwarmWithGlobal(false)
	if !st.cfg.Orchestration.IsEnabled() {
		t.Fatalf("disabling session swarm must keep global switch untouched")
	}
}

// 全局开关被启动参数锁定时不写入全局配置，仅切换会话。
func TestEnableSwarmWithGlobal_LockedGlobalKeepsValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: false})
	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), map[string]bool{"swarm-enabled": true}, state)
	off := false
	st.cfg.Orchestration.Enabled = &off
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == "t1"
	})

	state.enableSwarmWithGlobal(true)

	if st.cfg.Orchestration.IsEnabled() {
		t.Fatalf("locked global switch must not be modified")
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.setSwarmEnabledCalls) == 1 && fake.setSwarmEnabledCalls[0].Enabled
	})
}

// C-HY-19：applySwarmPair 切换 Lead provider/model、写入 Bees 偏好、联动开启 Swarm 并关闭弹层。
func TestApplySwarmPair_SwitchesLeadAndWritesBeesPrefs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	composerModelPickerState.MenuOpen = false
	t.Cleanup(func() {
		composerModelPickerState.MenuOpen = false
		clearModelCapsuleSnapshot("t1")
	})

	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: false})
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models:    []ModelInfo{{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true}},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models:    []ModelInfo{{ID: "o3-mini", Name: "o3-mini"}},
		},
	})
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		UpdatedAt: time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	off := false
	st.cfg.Orchestration.Enabled = &off
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.threads) > 0 && state.selectedThreadID == "t1"
	})

	leadEffort := "high"
	workerEffort := "medium"
	pair := SwarmPair{
		ID:             "pair-1",
		Name:           "深度思考",
		LeadProvider:   "codex",
		LeadModel:      "o3-mini",
		LeadEffort:     &leadEffort,
		WorkerProvider: "claude",
		WorkerModel:    "claude-3-7-sonnet",
		WorkerEffort:   &workerEffort,
	}
	state.applySwarmPair("t1", pair)

	// Bees 字段与 Lead 强度一次性写入 ComposerPrefs。
	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" &&
			prefs.BeesProvider == "claude" &&
			prefs.BeesModel == "claude-3-7-sonnet" &&
			prefs.BeesEffort == "medium" &&
			prefs.Effort == "high"
	})

	// Lead 侧走 SetThreadAgent 切换 provider 与模型。
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.SetAgentCalls) == 0 {
			return false
		}
		last := fake.SetAgentCalls[len(fake.SetAgentCalls)-1]
		return last.ThreadID == "t1" && last.Provider == "codex" && last.Model == "o3-mini"
	})

	// 全局开关联动打开，弹层被关闭。
	if !st.cfg.Orchestration.IsEnabled() {
		t.Fatalf("applySwarmPair should turn on global swarm switch")
	}
	if composerModelPickerState.MenuOpen {
		t.Fatalf("applySwarmPair should close the model panel")
	}

	// 选中的配对要写入后端并在本地会话上记住，选中态才能按 PairID 认定。
	state.mu.Lock()
	localPairID := ""
	for _, th := range state.threads {
		if th.ID == "t1" {
			localPairID = th.PairID
		}
	}
	state.mu.Unlock()
	if localPairID != "pair-1" {
		t.Fatalf("local thread PairID should be pair-1, got %q", localPairID)
	}
	waitFor(t, tt, func() bool {
		call, ok := fake.LastSwarmPairIDCall()
		return ok && call.ThreadID == "t1" && call.PairID == "pair-1"
	})
}
