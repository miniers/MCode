package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"mcode/internal/testenv"
)

func strPtr(s string) *string {
	return &s
}

// 1. Defaults 的新默认值。
func TestModelDefaults(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()

	if cfg.Models.PinLimit != 30 {
		t.Errorf("expected Models.PinLimit 30, got %d", cfg.Models.PinLimit)
	}
	if !cfg.Session.SkillsInjectPromptEnabled() {
		t.Errorf("expected Session.SkillsInjectPromptEnabled() true, got false")
	}
	if !cfg.Orchestration.AutoMerge {
		t.Errorf("expected Orchestration.AutoMerge true, got false")
	}
	if cfg.Orchestration.AutoPush {
		t.Errorf("expected Swarm.AutoPush false, got true")
	}
	if cfg.Orchestration.AlwaysBees {
		t.Errorf("expected Swarm.AlwaysBees false, got true")
	}
	if !cfg.Orchestration.LeadPlansOnly {
		t.Errorf("expected Swarm.LeadPlansOnly true, got false")
	}
	if !cfg.Orchestration.AutoClearFinished {
		t.Errorf("expected Swarm.AutoClearFinished true, got false")
	}
	if !cfg.Orchestration.AutoHidesIdleBees {
		t.Errorf("expected Swarm.AutoHidesIdleBees true, got false")
	}
	if !cfg.Orchestration.AutoPopsBees {
		t.Errorf("expected Swarm.AutoPopsBees true, got false")
	}
	if cfg.Orchestration.ArchivesCodexBees {
		t.Errorf("expected Swarm.ArchivesCodexBees false, got true")
	}
	if cfg.Models.Pins != nil {
		t.Errorf("expected General.ModelList nil, got %+v", cfg.Models.Pins)
	}
	if cfg.Models.Preferences != nil {
		t.Errorf("expected General.ModelPreferences nil, got %+v", cfg.Models.Preferences)
	}
	if cfg.Orchestration.Pairs != nil {
		t.Errorf("expected Swarm.Pairs nil, got %+v", cfg.Orchestration.Pairs)
	}
}

// 2. 旧配置 JSON：v7 的 general 双写字段在迁移时兜底到 orchestration。
func TestLegacyConfigMigration(t *testing.T) {
	testenv.TempHome(t)

	loadRaw := func(t *testing.T, raw string) Config {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(ConfigPath(dir), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		return cfg
	}

	// 用例 A：orchestration 缺键时，用 v7 general 的双写字段兜底。
	cfgA := loadRaw(t, `{
		"version": 7,
		"general": {
			"provider": "claude",
			"swarm_send_to_bees": true,
			"swarm_lead_plans_only": true
		}
	}`)
	if !cfgA.Orchestration.AlwaysBees {
		t.Errorf("expected legacy always_bees migrated to true, got false")
	}
	if !cfgA.Orchestration.LeadPlansOnly {
		t.Errorf("expected legacy lead_plans_only migrated to true, got false")
	}

	// 用例 B：orchestration 已显式指定 lead_plans_only: false 时，以新键为准。
	cfgB := loadRaw(t, `{
		"version": 7,
		"general": {
			"provider": "claude",
			"swarm_send_to_bees": true,
			"swarm_lead_plans_only": true
		},
		"swarm": {
			"lead_plans_only": false
		}
	}`)
	if cfgB.Orchestration.LeadPlansOnly {
		t.Errorf("expected explicit false to win over the legacy double write")
	}
	// always_bees 未在 swarm 中指定，应仍从 general 兜底。
	if !cfgB.Orchestration.AlwaysBees {
		t.Errorf("expected legacy always_bees migrated to true, got false")
	}

	// 用例 C：orchestration 已显式指定 always_bees: false。
	cfgC := loadRaw(t, `{
		"version": 7,
		"general": {
			"provider": "claude",
			"swarm_send_to_bees": true,
			"swarm_lead_plans_only": false
		},
		"swarm": {
			"always_bees": false
		}
	}`)
	if cfgC.Orchestration.AlwaysBees {
		t.Errorf("expected explicit false to win over the legacy double write")
	}
}

// 3. JSON 往返测试。
func TestConfigJSONRoundTrip(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()
	cfg.Models.Pins = []ModelPin{
		{Provider: "claude", ModelID: "claude-3-7-sonnet"},
		{Provider: "codex", ModelID: "gpt-4o"},
	}
	cfg.Models.Preferences = map[string]ModelPreference{
		"claude/claude-3-7-sonnet": {
			Effort:   strPtr("high"),
			FastMode: true,
		},
	}
	cfg.Orchestration.AutoPush = true
	cfg.Orchestration.Pairs = []SwarmPair{
		{
			ID:             "pair-1",
			Name:           "Claude + GPT-4o",
			LeadProvider:   "claude",
			LeadModel:      "claude-3-7-sonnet",
			LeadEffort:     strPtr("high"),
			LeadFastMode:   true,
			WorkerProvider: "codex",
			WorkerModel:    "gpt-4o",
			WorkerEffort:   strPtr("low"),
			WorkerFastMode: false,
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if !reflect.DeepEqual(decoded.Models.Pins, cfg.Models.Pins) {
		t.Errorf("ModelList mismatch: expected %+v, got %+v", cfg.Models.Pins, decoded.Models.Pins)
	}
	prefExpected := cfg.Models.Preferences["claude/claude-3-7-sonnet"]
	prefDecoded := decoded.Models.Preferences["claude/claude-3-7-sonnet"]
	if !reflect.DeepEqual(prefDecoded, prefExpected) {
		t.Errorf("ModelPreferences mismatch: expected %+v, got %+v", prefExpected, prefDecoded)
	}
	if decoded.Orchestration.AutoPush != cfg.Orchestration.AutoPush {
		t.Errorf("Swarm.AutoPush mismatch: expected %v, got %v", cfg.Orchestration.AutoPush, decoded.Orchestration.AutoPush)
	}
	if !reflect.DeepEqual(decoded.Orchestration.Pairs, cfg.Orchestration.Pairs) {
		t.Errorf("Swarm.Pairs mismatch: expected %+v, got %+v", cfg.Orchestration.Pairs, decoded.Orchestration.Pairs)
	}
}

// 4. ModelList 30 项上限、去重与操作测试。
func TestModelListLimitAndOperations(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()

	// 传入 35 个不同 pin，验证截断到 30
	pins := make([]ModelPin, 0, 40)
	for i := 1; i <= 35; i++ {
		pins = append(pins, ModelPin{
			Provider: "codex",
			ModelID:  fmt.Sprintf("model-%02d", i),
		})
	}
	// 添加重复项与空项
	pins = append(pins, ModelPin{Provider: "codex", ModelID: "model-01"})
	pins = append(pins, ModelPin{Provider: "", ModelID: "invalid"})
	pins = append(pins, ModelPin{Provider: "invalid", ModelID: ""})

	cfg.Models.ReorderModelList(pins)

	if len(cfg.Models.Pins) != 30 {
		t.Fatalf("expected ModelList length 30, got %d", len(cfg.Models.Pins))
	}
	if cfg.Models.Pins[0].ModelID != "model-01" {
		t.Errorf("expected first model to be model-01, got %s", cfg.Models.Pins[0].ModelID)
	}
	if cfg.Models.Pins[29].ModelID != "model-30" {
		t.Errorf("expected 30th model to be model-30, got %s", cfg.Models.Pins[29].ModelID)
	}

	// 此时已满 30，AddModelPin 应该返回 false 并不作修改
	added := cfg.Models.AddModelPin(ModelPin{Provider: "codex", ModelID: "model-31"})
	if added {
		t.Errorf("expected AddModelPin to return false when list is full")
	}
	if len(cfg.Models.Pins) != 30 {
		t.Errorf("expected list length unchanged at 30, got %d", len(cfg.Models.Pins))
	}

	// 无效 pin 添加返回 false
	if cfg.Models.AddModelPin(ModelPin{Provider: "", ModelID: "m"}) {
		t.Errorf("expected AddModelPin to reject empty provider")
	}
	if cfg.Models.AddModelPin(ModelPin{Provider: "p", ModelID: ""}) {
		t.Errorf("expected AddModelPin to reject empty model_id")
	}

	// 删除项
	removed := cfg.Models.RemoveModelPin(ModelPin{Provider: "codex", ModelID: "model-01"})
	if !removed {
		t.Errorf("expected RemoveModelPin to return true for existing pin")
	}
	if len(cfg.Models.Pins) != 29 {
		t.Errorf("expected list length 29 after remove, got %d", len(cfg.Models.Pins))
	}
	// 再次删除相同项应返回 false
	if cfg.Models.RemoveModelPin(ModelPin{Provider: "codex", ModelID: "model-01"}) {
		t.Errorf("expected RemoveModelPin to return false for already removed pin")
	}

	// 现在列表长度 29，可以添加一个新 pin
	if !cfg.Models.AddModelPin(ModelPin{Provider: "claude", ModelID: "new-model"}) {
		t.Errorf("expected AddModelPin to succeed when under limit")
	}
	// 重复添加相同 pin 返回 false
	if cfg.Models.AddModelPin(ModelPin{Provider: "claude", ModelID: "new-model"}) {
		t.Errorf("expected AddModelPin to reject duplicate pin")
	}

	// Normalize 钳制测试：ModelListLimit=99 钳回 30
	cfg.Models.PinLimit = 99
	normCfg := Normalize(cfg)
	if normCfg.Models.PinLimit != 30 {
		t.Errorf("expected Normalize to clamp ModelListLimit 99 to 30, got %d", normCfg.Models.PinLimit)
	}

	// ModelListLimit=0 钳回 30
	cfg.Models.PinLimit = 0
	normCfg2 := Normalize(cfg)
	if normCfg2.Models.PinLimit != 30 {
		t.Errorf("expected Normalize to clamp ModelListLimit 0 to 30, got %d", normCfg2.Models.PinLimit)
	}
}

// HasModelPin 只认完全命中的 provider/model 组合，空值一律视为未固定。
func TestHasModelPin(t *testing.T) {
	m := ModelsConfig{Pins: []ModelPin{
		{Provider: "claude", ModelID: "claude-custom-x"},
		{Provider: "codex", ModelID: "gpt-custom-y"},
	}}
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"claude", "claude-custom-x", true},
		{"codex", "gpt-custom-y", true},
		{"claude", "gpt-custom-y", false},
		{"claude", "not-pinned", false},
		{"", "claude-custom-x", false},
		{"claude", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := m.HasModelPin(tc.provider, tc.model); got != tc.want {
			t.Errorf("HasModelPin(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
	if (ModelsConfig{}).HasModelPin("claude", "claude-custom-x") {
		t.Error("空固定列表不应命中")
	}
}

// 5. 模型偏好设置读写与深拷贝测试。
func TestModelPreferences(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()
	pin := ModelPin{Provider: "claude", ModelID: "claude-3-7-sonnet"}

	// 初始状态取值应为零值
	initial := cfg.Models.ModelPreferenceFor(pin)
	if initial.Effort != nil || initial.FastMode {
		t.Fatalf("expected initial preference zero value, got %+v", initial)
	}

	// Set 后 Get 等值
	effortVal := "high"
	cfg.Models.SetModelPreference(pin, ModelPreference{
		Effort:   &effortVal,
		FastMode: true,
	})

	got := cfg.Models.ModelPreferenceFor(pin)
	if got.Effort == nil || *got.Effort != "high" || !got.FastMode {
		t.Fatalf("expected effort high and fast_mode true, got %+v", got)
	}

	// 修改外部 effortVal 指针内容，确认不影响已存偏好（避免外部指针别名逃逸）
	effortVal = "medium"
	gotAfterMod := cfg.Models.ModelPreferenceFor(pin)
	if gotAfterMod.Effort == nil || *gotAfterMod.Effort != "high" {
		t.Errorf("expected stored effort to remain 'high' after external variable changed, got %+v", gotAfterMod)
	}

	// 零值删除该键
	cfg.Models.SetModelPreference(pin, ModelPreference{
		Effort:   nil,
		FastMode: false,
	})
	gotZero := cfg.Models.ModelPreferenceFor(pin)
	if gotZero.Effort != nil || gotZero.FastMode {
		t.Errorf("expected zero value after deleting preference, got %+v", gotZero)
	}
	if _, exists := cfg.Models.Preferences[pin.Key()]; exists {
		t.Errorf("expected key %q to be deleted from ModelPreferences map", pin.Key())
	}

	// 空 Provider 或 ModelID 被忽略
	cfg.Models.SetModelPreference(ModelPin{Provider: "", ModelID: "test"}, ModelPreference{FastMode: true})
	cfg.Models.SetModelPreference(ModelPin{Provider: "test", ModelID: ""}, ModelPreference{FastMode: true})
	if len(cfg.Models.Preferences) != 0 {
		t.Errorf("expected empty provider/model_id to be ignored, got %d entries", len(cfg.Models.Preferences))
	}
}

// 6. Swarm 配对增删改测试。
func TestSwarmPairs(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()

	// AddPair 生成非空 ID
	p1 := cfg.Orchestration.AddPair(SwarmPair{
		Name:           "Pair 1",
		LeadProvider:   "claude",
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: "codex",
		WorkerModel:    "gpt-4o",
	})
	if p1.ID == "" {
		t.Fatalf("expected AddPair to generate non-empty ID")
	}

	// AddPair 传入已存在的 ID 时应重新生成唯一 ID
	p2 := cfg.Orchestration.AddPair(SwarmPair{
		ID:             p1.ID,
		Name:           "Pair 2",
		LeadProvider:   "codex",
		WorkerProvider: "claude",
	})
	if p2.ID == "" || p2.ID == p1.ID {
		t.Fatalf("expected AddPair to regenerate unique ID when ID conflict occurs, got %s vs %s", p2.ID, p1.ID)
	}

	// 查找 Pair
	found, ok := cfg.Orchestration.Pair(p1.ID)
	if !ok || found.Name != "Pair 1" {
		t.Errorf("expected to find Pair 1 by ID, got ok=%v, found=%+v", ok, found)
	}

	// UpdatePair 原位更新
	p1.Name = "Updated Pair 1"
	if !cfg.Orchestration.UpdatePair(p1) {
		t.Errorf("expected UpdatePair to succeed for existing ID")
	}
	updatedFound, ok := cfg.Orchestration.Pair(p1.ID)
	if !ok || updatedFound.Name != "Updated Pair 1" {
		t.Errorf("expected updated name, got ok=%v, name=%s", ok, updatedFound.Name)
	}

	// UpdatePair 不存在的 ID
	if cfg.Orchestration.UpdatePair(SwarmPair{ID: "non-existent"}) {
		t.Errorf("expected UpdatePair to fail for non-existent ID")
	}

	// RemovePair 删除
	if !cfg.Orchestration.RemovePair(p1.ID) {
		t.Errorf("expected RemovePair to succeed for existing ID")
	}
	if _, ok := cfg.Orchestration.Pair(p1.ID); ok {
		t.Errorf("expected Pair 1 to be removed")
	}
	// 再次删除不存在的 ID
	if cfg.Orchestration.RemovePair(p1.ID) {
		t.Errorf("expected RemovePair to fail when ID does not exist")
	}
}

// 7. 配置持久化与磁盘读写往返测试。
func TestConfigPersistenceRoundTrip(t *testing.T) {
	testenv.TempHome(t)

	dataDir := t.TempDir()

	cfg := Defaults()
	cfg.Models.Pins = []ModelPin{
		{Provider: "claude", ModelID: "claude-3-7-sonnet"},
	}
	cfg.Models.Preferences = map[string]ModelPreference{
		"claude/claude-3-7-sonnet": {
			Effort:   strPtr("high"),
			FastMode: true,
		},
	}
	cfg.Orchestration.AutoPush = true
	p := cfg.Orchestration.AddPair(SwarmPair{
		Name:           "Test Pair",
		LeadProvider:   "claude",
		WorkerProvider: "codex",
	})

	if err := Save(dataDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.Models.Pins) != 1 || loaded.Models.Pins[0].ModelID != "claude-3-7-sonnet" {
		t.Errorf("loaded ModelList mismatch: %+v", loaded.Models.Pins)
	}
	if pref := loaded.Models.ModelPreferenceFor(loaded.Models.Pins[0]); pref.Effort == nil || *pref.Effort != "high" || !pref.FastMode {
		t.Errorf("loaded ModelPreference mismatch: %+v", pref)
	}
	if !loaded.Orchestration.AutoPush {
		t.Errorf("expected loaded Orchestration.AutoPush true")
	}
	if lp, ok := loaded.Orchestration.Pair(p.ID); !ok || lp.Name != "Test Pair" {
		t.Errorf("loaded Swarm.Pair mismatch: ok=%v, pair=%+v", ok, lp)
	}
}

// 8. Normalize 过滤 Pairs 无效项测试。
func TestNormalizePairsFilter(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()
	cfg.Orchestration.Pairs = []SwarmPair{
		{ID: "", LeadProvider: "claude", WorkerProvider: "codex"},           // ID 为空，应被过滤
		{ID: "pair-valid", LeadProvider: "claude", WorkerProvider: ""},      // 有效
		{ID: "pair-empty-providers", LeadProvider: "", WorkerProvider: ""},  // 两个 provider 均为空，应被过滤
		{ID: "pair-worker-only", LeadProvider: "", WorkerProvider: "codex"}, // 有效
	}

	norm := Normalize(cfg)
	if len(norm.Orchestration.Pairs) != 2 {
		t.Fatalf("expected 2 valid pairs after Normalize, got %d", len(norm.Orchestration.Pairs))
	}
	if norm.Orchestration.Pairs[0].ID != "pair-valid" {
		t.Errorf("expected first pair to be pair-valid, got %s", norm.Orchestration.Pairs[0].ID)
	}
	if norm.Orchestration.Pairs[1].ID != "pair-worker-only" {
		t.Errorf("expected second pair to be pair-worker-only, got %s", norm.Orchestration.Pairs[1].ID)
	}
}

// 9. Defaults 经 Normalize 后切片与映射字段仍保持 nil。
func TestDefaultsNormalizeNilFields(t *testing.T) {
	testenv.TempHome(t)

	cfg := Normalize(Defaults())
	if cfg.Models.Pins != nil {
		t.Errorf("expected General.ModelList nil, got %+v", cfg.Models.Pins)
	}
	if cfg.Models.Preferences != nil {
		t.Errorf("expected General.ModelPreferences nil, got %+v", cfg.Models.Preferences)
	}
	if cfg.Orchestration.Pairs != nil {
		t.Errorf("expected Swarm.Pairs nil, got %+v", cfg.Orchestration.Pairs)
	}
}

func TestModelPreferenceReadIsIndependent(t *testing.T) {
	testenv.TempHome(t)

	var g ModelsConfig
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	g.SetModelPreference(pin, ModelPreference{Effort: strPtr("high")})
	pref := g.ModelPreferenceFor(pin)
	*pref.Effort = "low"
	if got := g.ModelPreferenceFor(pin); got.Effort == nil || *got.Effort != "high" {
		t.Fatalf("editing returned preference mutated stored value: %+v", got)
	}
}

func TestSwarmPairEditsAreIndependent(t *testing.T) {
	testenv.TempHome(t)

	var h OrchestrationConfig
	input := SwarmPair{LeadProvider: "claude", LeadEffort: strPtr("high"), WorkerEffort: strPtr("medium")}
	added := h.AddPair(input)
	*input.LeadEffort = "low"
	*added.WorkerEffort = "low"
	pair, ok := h.Pair(added.ID)
	if !ok || *pair.LeadEffort != "high" || *pair.WorkerEffort != "medium" {
		t.Fatalf("AddPair retained external pointers: %+v", pair)
	}
	*pair.LeadEffort = "medium"
	stored, _ := h.Pair(added.ID)
	if *stored.LeadEffort != "high" {
		t.Fatal("editing returned pair changed stored pair before UpdatePair")
	}
	if !h.UpdatePair(pair) {
		t.Fatal("UpdatePair failed")
	}
	*pair.LeadEffort = "low"
	stored, _ = h.Pair(added.ID)
	if *stored.LeadEffort != "medium" {
		t.Fatal("UpdatePair retained external pointers")
	}
}

func TestModelListDeduplicationAndCustomLimit(t *testing.T) {
	testenv.TempHome(t)

	a := ModelPin{Provider: "claude", ModelID: "a"}
	b := ModelPin{Provider: "codex", ModelID: "b"}
	c := ModelPin{Provider: "codex", ModelID: "c"}
	g := ModelsConfig{PinLimit: 2}
	g.ReorderModelList([]ModelPin{a, a, {}, b, c})
	if !reflect.DeepEqual(g.Pins, []ModelPin{a, b}) {
		t.Fatalf("unexpected model list: %+v", g.Pins)
	}
	g.ReorderModelList([]ModelPin{b, a})
	if !reflect.DeepEqual(g.Pins, []ModelPin{b, a}) {
		t.Fatalf("reorder did not preserve requested order: %+v", g.Pins)
	}
}

func TestNewDefaultEnabledSwitchesCanBeDisabled(t *testing.T) {
	testenv.TempHome(t)

	cfg := Defaults()
	if err := json.Unmarshal([]byte(`{
		"session":{"skills_injects_prompt":false}
	}`), &cfg); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if err := Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session.SkillsInjectPromptEnabled() {
		t.Fatal("explicit false was replaced by an enabled default")
	}
}
