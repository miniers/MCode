package mcodeui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/testenv"
)

// effectiveTierModel 优先级单元测试：session override > pair override > global tier > 跟随 Bee 模型。
func TestEffectiveTierModel_Precedence(t *testing.T) {
	fastFalse := false
	fastTrue := true

	globalTier := config.SwarmTier{
		ID:       "read",
		Name:     "只读",
		Provider: "claude",
		Model:    "claude-3-5-haiku",
		Effort:   "low",
		FastMode: true,
	}

	// 1. 无任何 override，使用 global tier 配置
	prov, mdl, eff, fast := effectiveTierModel(globalTier, nil, nil)
	if prov != "claude" || mdl != "claude-3-5-haiku" || eff != "low" || !fast {
		t.Fatalf("global tier only failed: prov=%s mdl=%s eff=%s fast=%v", prov, mdl, eff, fast)
	}

	// 2. Pair 覆盖生效，优先级高于 global tier
	pairOverrides := map[string]config.SwarmTierOverride{
		"read": {
			Model:    "claude-3-7-sonnet",
			Effort:   "medium",
			FastMode: &fastFalse,
		},
	}
	prov, mdl, eff, fast = effectiveTierModel(globalTier, pairOverrides, nil)
	if prov != "claude" || mdl != "claude-3-7-sonnet" || eff != "medium" || fast {
		t.Fatalf("pair override failed: prov=%s mdl=%s eff=%s fast=%v", prov, mdl, eff, fast)
	}

	// 3. 会话覆盖生效，优先级高于 pair 覆盖与 global tier
	sessionOverrides := map[string]config.SwarmTierOverride{
		"read": {
			Provider: "codex",
			Model:    "o3-mini",
			Effort:   "high",
			FastMode: &fastTrue,
		},
	}
	prov, mdl, eff, fast = effectiveTierModel(globalTier, pairOverrides, sessionOverrides)
	if prov != "codex" || mdl != "o3-mini" || eff != "high" || !fast {
		t.Fatalf("session override precedence failed: prov=%s mdl=%s eff=%s fast=%v", prov, mdl, eff, fast)
	}

	// 4. 全局与覆盖均未指定 Model 时回退空串（由 swarmModelCapsuleText 渲染为跟随会话子代理模型）
	emptyTier := config.SwarmTier{ID: "edit", Name: "执行"}
	prov, mdl, eff, fast = effectiveTierModel(emptyTier, nil, nil)
	if mdl != "" {
		t.Fatalf("empty tier should return empty model, got %q", mdl)
	}
	disp := swarmModelCapsuleText(nil, prov, mdl, eff, fast)
	if disp != "跟随会话子代理模型" {
		t.Fatalf("expected 跟随会话子代理模型, got %q", disp)
	}
}

// tierModelDiffLines 只在档位实际模型与会话子线程模型不同时输出一行，未绑定模型的档位不输出。
func TestTierModelDiffLines(t *testing.T) {
	tiers := []config.SwarmTier{
		{ID: "read", Name: "只读", Provider: "codex", Model: "o3-mini"},
		{ID: "edit", Name: "执行", Provider: "claude", Model: "claude-3-7-sonnet"},
		{ID: "review", Name: "审查"},
	}

	// 1. 只列出与会话子线程模型不同的档位，未绑定模型的档位不列出。
	lines := tierModelDiffLines(tiers, nil, nil, "claude", "claude-3-7-sonnet")
	if len(lines) != 1 || lines[0] != "只读档位：codex/o3-mini" {
		t.Fatalf("unexpected lines: %#v", lines)
	}

	// 2. 全部档位与会话子线程模型相同 → 无输出。
	same := []config.SwarmTier{{ID: "edit", Name: "执行", Provider: "claude", Model: "claude-3-7-sonnet"}}
	if lines := tierModelDiffLines(same, nil, nil, "claude", "claude-3-7-sonnet"); len(lines) != 0 {
		t.Fatalf("same model should produce no line, got %#v", lines)
	}

	// 3. 会话覆盖把档位拉回会话子线程模型 → 无输出；配对覆盖另配模型 → 有输出。
	sessionOverrides := map[string]config.SwarmTierOverride{
		"read": {Provider: "claude", Model: "claude-3-7-sonnet"},
	}
	if lines := tierModelDiffLines(tiers, nil, sessionOverrides, "claude", "claude-3-7-sonnet"); len(lines) != 0 {
		t.Fatalf("session override should produce no line, got %#v", lines)
	}
	pairOverrides := map[string]config.SwarmTierOverride{
		"review": {Provider: "codex", Model: "o3-mini"},
	}
	lines = tierModelDiffLines(tiers, pairOverrides, nil, "claude", "claude-3-7-sonnet")
	if len(lines) != 2 || lines[1] != "审查档位：codex/o3-mini" {
		t.Fatalf("unexpected lines with pair override: %#v", lines)
	}
}

// 档位列表渲染、档位编辑、选择模型下发 SetThreadBeeTiers 及全部恢复重置测试。
func TestComposerBeeTiers_RenderAndPick(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(func() {
		resetComposerModelPickerState("t1")
	})
	resetComposerModelPickerState("t1")

	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: true})
	fake.SetProviders([]ProviderInfo{
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
				{ID: "o3-mini", Name: "o3-mini", IsDefault: false},
			},
		},
	})
	th := Thread{
		ID:           "t1",
		ProjectID:    "p1",
		Title:        "测试会话1",
		Provider:     "claude",
		Model:        "claude-3-7-sonnet",
		BeesProvider: "claude",
		BeesModel:    "claude-3-7-sonnet",
		UpdatedAt:    time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	on := true
	st.cfg.Orchestration.Enabled = &on
	st.cfg.Orchestration.Tiers = []config.SwarmTier{
		{ID: "read", Name: "只读", ReadOnly: true, Description: "只读检索"},
		{ID: "edit", Name: "执行", Description: "执行变更"},
	}
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.threads) > 0 && state.selectedThreadID == "t1"
	})

	// 打开模型选择弹层
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	// 切换到 Bees 执行侧分段
	// 该夹具的模型没有档位列表，子代理分段只显示角色名“子代理”。
	waitFor(t, tt, func() bool {
		return tt.HasText("子代理")
	})
	if err := tt.Click("子代理"); err != nil {
		t.Fatalf("click bees tab failed: %v", err)
	}

	// 点击卡片进入二级列表页
	if err := tt.Click("选择其他模型"); err != nil {
		t.Fatalf("click model title failed: %v", err)
	}

	// 1. 验证档位与徽章正常渲染
	waitFor(t, tt, func() bool {
		return tt.HasText("档位") && tt.HasText("只读") && tt.HasText("执行")
	})

	// 2. 点击「只读」档位行，进入该档位编辑状态，展示 Banner 行与取消按钮
	if err := tt.Click("只读"); err != nil {
		t.Fatalf("click 只读 tier row failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("正在为「只读」档选择模型") && tt.HasText("取消")
	})

	// 3. 点击模型 "o3-mini"，应触发 OnSetTierOverride 并异步调用 SetThreadBeeTiers
	if err := tt.Click("o3-mini"); err != nil {
		t.Fatalf("click o3-mini failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		tid, tiersJSON, ok := fake.LastBeeTiersCall()
		if !ok || tid != "t1" {
			return false
		}
		var parsed map[string]config.SwarmTierOverride
		if err := json.Unmarshal([]byte(tiersJSON), &parsed); err != nil {
			return false
		}
		ov, exists := parsed["read"]
		return exists && ov.Provider == "codex" && ov.Model == "o3-mini"
	})

	// 4. 点击「全部恢复」按钮，下发空字符串清空会话档位覆盖
	waitFor(t, tt, func() bool {
		return tt.HasText("全部恢复")
	})
	if err := tt.Click("全部恢复"); err != nil {
		t.Fatalf("click 全部恢复 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		tid, tiersJSON, ok := fake.LastBeeTiersCall()
		return ok && tid == "t1" && tiersJSON == ""
	})
}

// 单档位重置按钮测试：点击重置按钮单项清除会话档位覆盖。
func TestComposerBeeTiers_SingleTierReset(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(func() {
		resetComposerModelPickerState("t1")
	})
	resetComposerModelPickerState("t1")

	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: true})
	fake.SetProviders([]ProviderInfo{
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
				{ID: "o3-mini", Name: "o3-mini", IsDefault: false},
			},
		},
	})
	th := Thread{
		ID:           "t1",
		ProjectID:    "p1",
		Title:        "测试会话1",
		Provider:     "claude",
		Model:        "claude-3-7-sonnet",
		BeesProvider: "claude",
		BeesModel:    "claude-3-7-sonnet",
		BeeTiers:     `{"read":{"provider":"codex","model":"o3-mini"}}`,
		UpdatedAt:    time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	on := true
	st.cfg.Orchestration.Enabled = &on
	st.cfg.Orchestration.Tiers = []config.SwarmTier{
		{ID: "read", Name: "只读", ReadOnly: true},
	}
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.threads) > 0 && state.selectedThreadID == "t1"
	})

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("子代理")
	})
	if err := tt.Click("子代理"); err != nil {
		t.Fatalf("click bees tab failed: %v", err)
	}
	if err := tt.Click("选择其他模型"); err != nil {
		t.Fatalf("click model title failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("恢复")
	})
	if err := tt.Click("恢复"); err != nil {
		t.Fatalf("click 恢复 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		tid, tiersJSON, ok := fake.LastBeeTiersCall()
		return ok && tid == "t1" && tiersJSON == ""
	})
}
