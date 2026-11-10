package config

import (
	"os"
	"reflect"
	"testing"
)

func TestMigrateV9ToV10TierPrompts(t *testing.T) {
	for _, sectionKey := range []string{"orchestration"} {
		t.Run(sectionKey, func(t *testing.T) {
			tiers := []any{
				map[string]any{"id": "read"},
				map[string]any{"id": "chore", "system_prompt": " \t"},
				map[string]any{"id": "edit", "system_prompt": "自定义提示词"},
				map[string]any{"id": "deep", "system_prompt": ""},
				map[string]any{"id": "custom"},
				map[string]any{"id": "custom2", "system_prompt": ""},
				"invalid",
			}
			raw := map[string]any{sectionKey: map[string]any{"tiers": tiers}}
			if _, err := migrateV9ToV10(raw); err != nil {
				t.Fatal(err)
			}
			for i, want := range []string{readTierSystemPrompt, choreTierSystemPrompt, "自定义提示词", deepTierSystemPrompt} {
				if got := tiers[i].(map[string]any)["system_prompt"]; got != want {
					t.Errorf("档位 %d 的提示词 = %v，期望 %q", i, got, want)
				}
			}
			if _, exists := tiers[4].(map[string]any)["system_prompt"]; exists {
				t.Fatal("非内置档不应补提示词")
			}
			if got := tiers[5].(map[string]any)["system_prompt"]; got != "" {
				t.Fatalf("非内置档的空提示词被修改：%v", got)
			}
		})
	}
}

func TestMigrateV9ToV10SkipsInvalidSections(t *testing.T) {
	for _, raw := range []map[string]any{
		{"version": 9},
		{"orchestration": "invalid"},
		{"orchestration": map[string]any{}},
		{"orchestration": map[string]any{"tiers": "invalid"}},
	} {
		before := make(map[string]any, len(raw))
		for k, v := range raw {
			before[k] = v
		}
		out, err := migrateV9ToV10(raw)
		if err != nil || !reflect.DeepEqual(out, before) {
			t.Errorf("异常输入应原样返回：%v，错误：%v", out, err)
		}
	}
}

func TestLoadMigratesV9TierTexts(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"version":9,"orchestration":{"tiers":[
		{"id":"read","name":"只读","read_only":true,"description":"只读调研：检索、阅读、总结代码与文档，不修改任何文件"},
		{"id":"edit","name":"执行","description":"自定义说明","system_prompt":"自定义提示词"},
		{"id":"deep","name":"深度","description":"只做定位、方案设计与关键决策：brief 只给现象、目标或边界而没给改法时，先复现、二分、实测或设计接口，产出带 path:line 的分步方案；复杂时把方案写成 md 文档；不改代码。","system_prompt":"  "}
	]}}`)
	if err := os.WriteFile(ConfigPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 11 {
		t.Fatalf("版本 = %d，期望 11", cfg.Version)
	}
	tiers := cfg.Orchestration.Tiers
	if len(tiers) != 3 {
		t.Fatalf("档位数量 = %d，期望 3", len(tiers))
	}
	if tiers[0].Description != readTierDescription || tiers[0].SystemPrompt != readTierSystemPrompt {
		t.Errorf("read 档未升级：%+v", tiers[0])
	}
	if tiers[1].Description != "自定义说明" || tiers[1].SystemPrompt != "自定义提示词" {
		t.Errorf("自定义文案被修改：%+v", tiers[1])
	}
	if tiers[2].Description != deepTierDescription || tiers[2].SystemPrompt != deepTierSystemPrompt {
		t.Errorf("deep 档未升级：%+v", tiers[2])
	}
}

func TestLoadV10KeepsClearedTierPrompts(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"version":10,"orchestration":{"tiers":[
		{"id":"read","name":"只读","read_only":true},
		{"id":"edit","name":"执行","system_prompt":""}
	]}}`)
	if err := os.WriteFile(ConfigPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range cfg.Orchestration.Tiers {
		if tier.SystemPrompt != "" {
			t.Errorf("%s 档的空提示词被回填：%q", tier.ID, tier.SystemPrompt)
		}
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range loaded.Orchestration.Tiers {
		if tier.SystemPrompt != "" {
			t.Errorf("%s 档的空提示词保存后被回填：%q", tier.ID, tier.SystemPrompt)
		}
	}
}
