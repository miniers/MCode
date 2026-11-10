package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 1. Normalize 在 Tiers 为 nil 时自动填充默认档位。
func TestNormalizeTiersNilFillsDefaults(t *testing.T) {
	cfg := Defaults()
	cfg.Orchestration.Tiers = nil
	cfg.Orchestration.DefaultTier = ""

	norm := Normalize(cfg)

	if norm.Orchestration.Tiers == nil {
		t.Fatal("expected non-nil Tiers after Normalize")
	}
	expected := DefaultSwarmTiers()
	if len(norm.Orchestration.Tiers) != len(expected) {
		t.Fatalf("expected %d tiers, got %d", len(expected), len(norm.Orchestration.Tiers))
	}
	for i, exp := range expected {
		got := norm.Orchestration.Tiers[i]
		if got.ID != exp.ID || got.Name != exp.Name || got.ReadOnly != exp.ReadOnly || got.Description != exp.Description {
			t.Errorf("tier[%d] mismatch: expected %+v, got %+v", i, exp, got)
		}
	}
	if norm.Orchestration.DefaultTier != DefaultSwarmTierID {
		t.Errorf("expected DefaultTier %q, got %q", DefaultSwarmTierID, norm.Orchestration.DefaultTier)
	}
}

// 1b. DefaultSwarmTiers 返回只读、杂活、执行、攻坚四档，并带各自的默认推理强度。
func TestDefaultSwarmTiersOrderAndEffort(t *testing.T) {
	want := []struct {
		id     string
		effort string
	}{
		{"read", "medium"},
		{"chore", "medium"},
		{DefaultSwarmTierID, "medium"},
		{"deep", "high"},
	}

	got := DefaultSwarmTiers()
	if len(got) != len(want) {
		t.Fatalf("expected %d default tiers, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Effort != w.effort {
			t.Errorf("tier[%d] = (id=%q effort=%q)，想要 (id=%q effort=%q)", i, got[i].ID, got[i].Effort, w.id, w.effort)
		}
		if got[i].Provider != "" || got[i].Model != "" {
			t.Errorf("tier[%d] 的 Provider/Model 应默认为空，得到 %+v", i, got[i])
		}
	}
	if DefaultSwarmTiers()[0].ReadOnly != true {
		t.Error("read 档应为只读")
	}
	if DefaultSwarmTiers()[1].ReadOnly {
		t.Error("chore 档应为可写")
	}
	if DefaultSwarmTiers()[1].Name != "杂活" {
		t.Errorf("chore 档名称应为杂活，得到 %q", DefaultSwarmTiers()[1].Name)
	}
}

// 2. Normalize 保持显式非 nil 的空切片不变（除了 DefaultTier）。
func TestNormalizeTiersExplicitEmptyKept(t *testing.T) {
	cfg := Defaults()
	cfg.Orchestration.Tiers = []SwarmTier{}
	cfg.Orchestration.DefaultTier = "edit"

	norm := Normalize(cfg)

	if norm.Orchestration.Tiers == nil {
		t.Fatal("expected non-nil empty Tiers, got nil")
	}
	if len(norm.Orchestration.Tiers) != 0 {
		t.Fatalf("expected 0 tiers, got %d", len(norm.Orchestration.Tiers))
	}
	if norm.Orchestration.DefaultTier != "" {
		t.Errorf("expected empty DefaultTier when Tiers is empty, got %q", norm.Orchestration.DefaultTier)
	}
}

// 3. DefaultTier 回退逻辑：空或不存在时回退到 edit，无 edit 时回退到首个可写档位。
func TestNormalizeDefaultTierFallback(t *testing.T) {
	// Case 3a: DefaultTier 不存在且有 edit 档位，回退至 edit
	cfg := Defaults()
	cfg.Orchestration.Tiers = DefaultSwarmTiers()
	cfg.Orchestration.DefaultTier = "nonexistent"
	norm := Normalize(cfg)
	if norm.Orchestration.DefaultTier != DefaultSwarmTierID {
		t.Errorf("expected DefaultTier %q, got %q", DefaultSwarmTierID, norm.Orchestration.DefaultTier)
	}

	// Case 3b: 没有 edit 档位，回退到第一个 ReadOnly=false 的档位
	cfg.Orchestration.Tiers = []SwarmTier{
		{ID: "read", Name: "只读", ReadOnly: true},
		{ID: "worker", Name: "工作", ReadOnly: false},
		{ID: "worker2", Name: "工作2", ReadOnly: false},
	}
	cfg.Orchestration.DefaultTier = ""
	norm = Normalize(cfg)
	if norm.Orchestration.DefaultTier != "worker" {
		t.Errorf("expected DefaultTier to fallback to first non-readonly tier %q, got %q", "worker", norm.Orchestration.DefaultTier)
	}

	// Case 3c: 显式指定合法 DefaultTier，保留（转为小写）
	cfg.Orchestration.DefaultTier = "WORKER2"
	norm = Normalize(cfg)
	if norm.Orchestration.DefaultTier != "worker2" {
		t.Errorf("expected DefaultTier to be lowercased to %q, got %q", "worker2", norm.Orchestration.DefaultTier)
	}
}

func TestNormalizeDefaultTierLanding(t *testing.T) {
	tests := []struct {
		name  string
		tiers []SwarmTier
		input string
		want  string
	}{
		{name: "deep", input: "deep", want: "deep"},
		{name: "read", input: "read", want: "edit"},
		{name: "chore", input: "chore", want: "chore"},
		{name: "no edit", tiers: []SwarmTier{
			{ID: "read", ReadOnly: true},
			{ID: "limited", WritePaths: []string{"docs/**"}},
			{ID: "worker"},
			{ID: "worker2"},
		}, input: "limited", want: "worker"},
		{name: "restricted edit", tiers: []SwarmTier{
			{ID: "edit", WritePaths: []string{"docs/**"}},
			{ID: "worker"},
		}, input: "edit", want: "worker"},
		{name: "no landing tier", tiers: []SwarmTier{
			{ID: "read", ReadOnly: true},
			{ID: "limited", WritePaths: []string{"docs/**"}},
		}, input: "limited", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			if tt.tiers != nil {
				cfg.Orchestration.Tiers = tt.tiers
			}
			cfg.Orchestration.DefaultTier = tt.input
			if got := Normalize(cfg).Orchestration.DefaultTier; got != tt.want {
				t.Errorf("DefaultTier = %q, want %q", got, tt.want)
			}
		})
	}
}

// 4. Normalize 过滤 SwarmPair.BeeTiers 中不存在的档位键，并先小写键。
func TestNormalizeUnknownPairBeeTiersDropped(t *testing.T) {
	fastTrue := true
	cfg := Defaults()
	cfg.Orchestration.Tiers = []SwarmTier{
		{ID: "edit", Name: "执行", ReadOnly: false},
		{ID: "read", Name: "只读", ReadOnly: true},
	}
	cfg.Orchestration.Pairs = []SwarmPair{
		{
			ID:             "p1",
			LeadProvider:   "claude",
			WorkerProvider: "codex",
			BeeTiers: map[string]SwarmTierOverride{
				"EDIT":    {Model: "gpt-4o", FastMode: &fastTrue},
				"unknown": {Model: "gpt-3.5"},
				"READ":    {Effort: "low"},
			},
		},
	}

	norm := Normalize(cfg)
	if len(norm.Orchestration.Pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(norm.Orchestration.Pairs))
	}
	ht := norm.Orchestration.Pairs[0].BeeTiers
	if len(ht) != 2 {
		t.Fatalf("expected 2 tier overrides after dropping unknown, got %d (%+v)", len(ht), ht)
	}
	if _, ok := ht["unknown"]; ok {
		t.Error("expected 'unknown' tier key to be dropped")
	}
	editOverride, ok := ht["edit"]
	if !ok {
		t.Fatal("expected 'edit' tier key to be present after lowercasing")
	}
	if editOverride.Model != "gpt-4o" || editOverride.FastMode == nil || !*editOverride.FastMode {
		t.Errorf("unexpected edit override: %+v", editOverride)
	}
	if _, ok := ht["read"]; !ok {
		t.Fatal("expected 'read' tier key to be present after lowercasing")
	}
}

// 5. Validate 拒绝重复 ID、非法 ID 以及全只读档位列表。
func TestValidateTiers(t *testing.T) {
	// 重复 ID
	cfg := Defaults()
	cfg.Orchestration.Tiers = []SwarmTier{
		{ID: "edit", Name: "执行 1", ReadOnly: false},
		{ID: "edit", Name: "执行 2", ReadOnly: false},
	}
	cfg.Orchestration.DefaultTier = "edit"
	if err := Validate(cfg); err == nil {
		t.Error("expected error for duplicate tier IDs, got nil")
	}

	// 空 ID
	cfg = Defaults()
	cfg.Orchestration.Tiers = []SwarmTier{
		{ID: "", Name: "空ID", ReadOnly: false},
	}
	cfg.Orchestration.DefaultTier = ""
	if err := Validate(cfg); err == nil {
		t.Error("expected error for empty tier ID, got nil")
	}

	// 非法格式 ID（包含大写、特殊符号、以破折号开头等）
	for _, badID := range []string{"EDIT", "-invalid", "bad@id", "toolong123456789012345678901234567890"} {
		cfg = Defaults()
		cfg.Orchestration.Tiers = []SwarmTier{
			{ID: badID, Name: "非法", ReadOnly: false},
		}
		cfg.Orchestration.DefaultTier = badID
		if err := Validate(cfg); err == nil {
			t.Errorf("expected error for invalid tier ID %q, got nil", badID)
		}
	}

	// 全只读列表
	cfg = Defaults()
	cfg.Orchestration.Tiers = []SwarmTier{
		{ID: "r1", Name: "只读1", ReadOnly: true},
		{ID: "r2", Name: "只读2", ReadOnly: true},
	}
	cfg.Orchestration.DefaultTier = "r1"
	if err := Validate(cfg); err == nil {
		t.Error("expected error when all tiers are read-only, got nil")
	}

	// DefaultTier 不在 Tiers 中
	cfg = Defaults()
	cfg.Orchestration.Tiers = []SwarmTier{
		{ID: "edit", Name: "执行", ReadOnly: false},
	}
	cfg.Orchestration.DefaultTier = "other"
	if err := Validate(cfg); err == nil {
		t.Error("expected error when DefaultTier does not exist, got nil")
	}

	// 合法配置
	cfg = Defaults()
	cfg.Orchestration.Tiers = DefaultSwarmTiers()
	cfg.Orchestration.DefaultTier = DefaultSwarmTierID
	if err := Validate(cfg); err != nil {
		t.Errorf("expected valid tiers to pass Validate, got %v", err)
	}
}

// 6. JSON 序列化与反序列化 SwarmPair 及其 BeeTiers（包括 FastMode 指针）。
func TestSwarmPairBeeTiersJSONRoundTrip(t *testing.T) {
	fastTrue := true
	fastFalse := false
	original := SwarmPair{
		ID:             "pair-1",
		Name:           "配对 1",
		LeadProvider:   "claude",
		LeadModel:      "claude-3-5-sonnet",
		WorkerProvider: "codex",
		WorkerModel:    "gpt-4o",
		BeeTiers: map[string]SwarmTierOverride{
			"edit": {
				Provider: "codex",
				Model:    "gpt-4o-mini",
				Effort:   "medium",
				FastMode: &fastTrue,
			},
			"read": {
				Provider: "claude",
				Model:    "claude-3-haiku",
				FastMode: &fastFalse,
			},
			"deep": {
				Effort:   "high",
				FastMode: nil,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal SwarmPair failed: %v", err)
	}

	var decoded SwarmPair
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal SwarmPair failed: %v", err)
	}

	if decoded.ID != original.ID || decoded.Name != original.Name {
		t.Errorf("basic fields mismatch: expected %+v, got %+v", original, decoded)
	}
	if len(decoded.BeeTiers) != len(original.BeeTiers) {
		t.Fatalf("BeeTiers length mismatch: expected %d, got %d", len(original.BeeTiers), len(decoded.BeeTiers))
	}

	for k, exp := range original.BeeTiers {
		got, ok := decoded.BeeTiers[k]
		if !ok {
			t.Fatalf("missing key %q in decoded BeeTiers", k)
		}
		if got.Provider != exp.Provider || got.Model != exp.Model || got.Effort != exp.Effort {
			t.Errorf("override for %q mismatch: expected %+v, got %+v", k, exp, got)
		}
		if exp.FastMode == nil {
			if got.FastMode != nil {
				t.Errorf("expected nil FastMode for %q, got %v", k, *got.FastMode)
			}
		} else {
			if got.FastMode == nil {
				t.Fatalf("expected non-nil FastMode for %q, got nil", k)
			}
			if *got.FastMode != *exp.FastMode {
				t.Errorf("FastMode for %q mismatch: expected %v, got %v", k, *exp.FastMode, *got.FastMode)
			}
		}
	}
}

// 7. SwarmPair.clone() 对 BeeTiers map 及其 FastMode 指针的独立性深拷贝。
func TestSwarmPairCloneIndependence(t *testing.T) {
	fastTrue := true
	p1 := SwarmPair{
		ID:   "p1",
		Name: "Pair 1",
		BeeTiers: map[string]SwarmTierOverride{
			"edit": {
				Model:    "m1",
				FastMode: &fastTrue,
			},
		},
	}

	p2 := p1.clone()

	// 修改 p2 的 map 与内部指针
	newFast := false
	p2.BeeTiers["edit"] = SwarmTierOverride{
		Model:    "m2-changed",
		FastMode: &newFast,
	}
	p2.BeeTiers["new-tier"] = SwarmTierOverride{Model: "extra"}

	// 验证 p1 未受任何影响
	if len(p1.BeeTiers) != 1 {
		t.Fatalf("p1 BeeTiers modified when p2 added a key: len=%d", len(p1.BeeTiers))
	}
	edit1 := p1.BeeTiers["edit"]
	if edit1.Model != "m1" {
		t.Errorf("p1 Model changed to %q", edit1.Model)
	}
	if edit1.FastMode == nil || !*edit1.FastMode {
		t.Errorf("p1 FastMode pointer mutated, got %+v", edit1.FastMode)
	}
}

// 8. 内置档位均不限制写入路径。
func TestDefaultSwarmTiersDeepWritePaths(t *testing.T) {
	for _, tier := range DefaultSwarmTiers() {
		if len(tier.WritePaths) != 0 {
			t.Errorf("%s 档不该有写入白名单，得到 %v", tier.ID, tier.WritePaths)
		}
	}

	// 每次调用返回新切片，改一次的结果不影响下一次。
	first := defaultDeepWritePaths()
	first[0] = "mutated"
	if second := defaultDeepWritePaths(); second[0] != "docs/superpowers/plans/**/*.md" {
		t.Fatalf("历史默认白名单被上一次调用改动了: %v", second)
	}
}

func TestUpgradeRecentBuiltinTierTexts(t *testing.T) {
	tiers := []SwarmTier{
		{ID: "read", Description: "只读调研：回答 brief 列出的具体问题，如符号在哪、谁调用、历史上改过什么", SystemPrompt: "你是只读调研档。逐条回答 brief 里的问题，每条发现写成 path:line，后面紧跟用反引号包住的源码原文。可以用 grep、git log、git blame 等只读命令。不推测根因，不给改法；brief 没问到的不展开。"},
		{ID: "deep", Name: "深度", Description: "定位与设计：brief 只给现象、目标或边界、没给改法时用，产出可转交默认档位执行的分步方案", SystemPrompt: "你是定位与设计档，不改代码。先读代码与 git 历史，必要时跑有范围的测试来复现，找到根因。方案写成分步改法：每步给出 path:line、具体改法、验收标准和要补的测试，写到改代码的子代理可以原样照做。有多种方案时列出取舍并给推荐。"},
		{ID: "deep", Name: "自定义名称"},
		{ID: "deep", Name: " 深度 "},
	}
	got := UpgradeBuiltinTierTexts(tiers)
	if got[0].Description != readTierDescription || got[0].SystemPrompt != readTierSystemPrompt {
		t.Errorf("read 档未升级：%+v", got[0])
	}
	if got[1].Name != "攻坚" || got[1].Description != deepTierDescription || got[1].SystemPrompt != deepTierSystemPrompt {
		t.Errorf("deep 档未升级：%+v", got[1])
	}
	if got[2].Name != "自定义名称" || got[3].Name != " 深度 " {
		t.Errorf("自定义名称被修改：%+v", got[2:])
	}
	if DefaultSwarmTiers()[3].Name != "攻坚" {
		t.Error("deep 档默认名称应为攻坚")
	}
}

// 9. v5→v6 迁移给缺 write_paths 键的 deep 档补默认白名单；已有该键（含空数组）保持不动，
// swarm/tiers 缺失或类型不对时原样返回且不报错。
func TestMigrateV5ToV6DeepWritePaths(t *testing.T) {
	run := func(t *testing.T, raw map[string]any) map[string]any {
		t.Helper()
		out, err := migrateV5ToV6(raw)
		if err != nil {
			t.Fatalf("migrateV5ToV6 failed: %v", err)
		}
		return out
	}

	// 缺键的 deep 档补上默认值。
	raw := map[string]any{
		"swarm": map[string]any{
			"tiers": []any{
				map[string]any{"id": "read", "read_only": true},
				map[string]any{"id": "deep", "description": "自定义描述"},
			},
		},
	}
	out := run(t, raw)
	tiers := out["swarm"].(map[string]any)["tiers"].([]any)
	deepTier := tiers[1].(map[string]any)
	if !reflect.DeepEqual(deepTier["write_paths"], []any{"docs/superpowers/plans/**/*.md"}) {
		t.Fatalf("deep 档的 write_paths = %v，想要默认白名单", deepTier["write_paths"])
	}
	if deepTier["description"] != "自定义描述" {
		t.Errorf("迁移不该改 deep 的 description，得到 %v", deepTier["description"])
	}
	if _, exists := tiers[0].(map[string]any)["write_paths"]; exists {
		t.Errorf("其他档位不该被补上 write_paths: %v", tiers[0])
	}

	// 已有 write_paths 键（含空数组）时不动。
	emptyRaw := map[string]any{
		"swarm": map[string]any{
			"tiers": []any{map[string]any{"id": "deep", "write_paths": []any{}}},
		},
	}
	emptyOut := run(t, emptyRaw)
	emptyTier := emptyOut["swarm"].(map[string]any)["tiers"].([]any)[0].(map[string]any)
	if got, ok := emptyTier["write_paths"].([]any); !ok || len(got) != 0 {
		t.Fatalf("已有空 write_paths 应保持为空，得到 %v", emptyTier["write_paths"])
	}

	customRaw := map[string]any{
		"swarm": map[string]any{
			"tiers": []any{map[string]any{"id": "deep", "write_paths": []any{"custom/*.md"}}},
		},
	}
	customOut := run(t, customRaw)
	customTier := customOut["swarm"].(map[string]any)["tiers"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(customTier["write_paths"], []any{"custom/*.md"}) {
		t.Fatalf("已有自定义 write_paths 应保持原样，得到 %v", customTier["write_paths"])
	}

	// swarm 或 tiers 缺失、类型不对时原样返回。
	for _, broken := range []map[string]any{
		{"version": 5},
		{"swarm": "nope"},
		{"swarm": map[string]any{"tiers": "nope"}},
		{"swarm": map[string]any{}},
	} {
		out := run(t, broken)
		if !reflect.DeepEqual(out, broken) {
			t.Errorf("异常输入应原样返回，得到 %v", out)
		}
	}
}

func TestUpgradeBuiltinTierTexts(t *testing.T) {
	defaults := DefaultSwarmTiers()
	oldDescriptions := []string{
		"只读调研：检索、阅读、总结代码与文档，不修改任何文件",
		"机械活：补丁重放或重做已有补丁、改名、格式化、修测试或补测试、文档与文案修改；brief 已给出文件与改法",
		"执行步骤已写清楚的明确修改，单文件或少量文件",
		"只做定位、方案设计与关键决策：brief 只给现象、目标或边界而没给改法时，先复现、二分、实测或设计接口，产出带 path:line 的分步方案；复杂时把方案写成 md 文档；不改代码。",
	}
	tiers := make([]SwarmTier, len(defaults))
	copy(tiers, defaults)
	for i := range tiers {
		tiers[i].Description = "  " + oldDescriptions[i] + "  "
		tiers[i].SystemPrompt = ""
	}
	tiers = append(tiers,
		SwarmTier{ID: "edit", Description: "自定义说明", SystemPrompt: "自定义提示词"},
		SwarmTier{ID: "custom", Description: oldDescriptions[0]},
	)
	original := append([]SwarmTier(nil), tiers...)
	got := UpgradeBuiltinTierTexts(tiers)
	for i, def := range defaults {
		if got[i].Description != def.Description || got[i].SystemPrompt != "" {
			t.Errorf("档位 %s 的升级结果不符：%+v", def.ID, got[i])
		}
	}
	if !reflect.DeepEqual(got[len(defaults):], original[len(defaults):]) {
		t.Errorf("自定义内容被修改：%+v", got[len(defaults):])
	}
	if !reflect.DeepEqual(tiers, original) {
		t.Fatal("输入切片被修改")
	}
	if !reflect.DeepEqual(UpgradeBuiltinTierTexts(got), got) {
		t.Fatal("重复升级应保持不变")
	}
	if UpgradeBuiltinTierTexts(nil) != nil || UpgradeBuiltinTierTexts([]SwarmTier{}) == nil {
		t.Fatal("应保留 nil 与空切片的区别")
	}
}

func TestUpgradeBuiltinDeepTierTexts(t *testing.T) {
	const (
		oldDesc   = "定位与设计：brief 只给现象、目标或边界、没给改法时用，产出可交给 edit 档的分步方案"
		oldPrompt = "你是定位与设计档，不改代码。先读代码与 git 历史，必要时跑有范围的测试来复现，找到根因。汇报写清根因和分步改法：每步给出 path:line、具体改法、验收标准和要补的测试，可以原样转交执行档。有多种方案时列出取舍并给推荐。超过约 5 步时写入 docs/superpowers/plans/<主题>.md，并在汇报里给出路径。"
	)
	var def SwarmTier
	for _, d := range DefaultSwarmTiers() {
		if d.ID == "deep" {
			def = d
		}
	}
	for _, bad := range []string{"超过约 5 步", "可以原样转交执行档", "edit 档"} {
		if strings.Contains(def.Description+def.SystemPrompt, bad) {
			t.Errorf("deep 默认文案不应包含 %q", bad)
		}
	}

	got := UpgradeBuiltinTierTexts([]SwarmTier{
		{ID: "deep", Description: oldDesc, SystemPrompt: oldPrompt},
		{ID: "deep", Description: "自定义说明", SystemPrompt: "自定义提示词"},
		{ID: "deep", Description: oldDesc, SystemPrompt: ""},
	})
	if got[0].Description != def.Description || got[0].SystemPrompt != def.SystemPrompt {
		t.Errorf("旧默认值应升级为当前默认值：%+v", got[0])
	}
	if got[1].Description != "自定义说明" || got[1].SystemPrompt != "自定义提示词" {
		t.Errorf("自定义文本应保持不变：%+v", got[1])
	}
	if got[2].Description != def.Description || got[2].SystemPrompt != "" {
		t.Errorf("空提示词应保持为空：%+v", got[2])
	}
}

func TestBuiltinTierCustomized(t *testing.T) {
	for _, def := range DefaultSwarmTiers() {
		if BuiltinTierCustomized(def) {
			t.Errorf("默认档位 %s 不应标记为已修改", def.ID)
		}
		modified := def
		modified.Description = " " + def.Description + " "
		modified.SystemPrompt = "\n" + def.SystemPrompt + "\t"
		modified.Provider, modified.Model, modified.Effort, modified.FastMode = "p", "m", "low", true
		modified.WritePaths = []string{"other/**"}
		if BuiltinTierCustomized(modified) {
			t.Errorf("空白和模型配置不应影响 %s 的标记", def.ID)
		}
		modified.Description = "自定义说明"
		if !BuiltinTierCustomized(modified) {
			t.Errorf("%s 的说明已修改", def.ID)
		}
		modified.Description = def.Description
		modified.SystemPrompt = ""
		if !BuiltinTierCustomized(modified) {
			t.Errorf("%s 的提示词已清空", def.ID)
		}
		modified.SystemPrompt = "自定义提示词"
		if !BuiltinTierCustomized(modified) {
			t.Errorf("%s 的提示词已修改", def.ID)
		}
	}
	if BuiltinTierCustomized(SwarmTier{ID: "custom", Description: "自定义"}) {
		t.Fatal("非内置档位不应标记")
	}
}
