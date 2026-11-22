package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestMigrateV13ToV14(t *testing.T) {
	expectedDesign, err := designTierRawV13ToV14()
	if err != nil {
		t.Fatalf("designTierRawV13ToV14 failed: %v", err)
	}

	t.Run("append design to four-tier config", func(t *testing.T) {
		var raw map[string]any
		input := `{
			"version": 13,
			"orchestration": {
				"default_tier": "edit",
				"tiers": [
					{"id": "read", "name": "只读", "read_only": true},
					{"id": "chore", "name": "杂活", "read_only": false},
					{"id": "edit", "name": "执行", "read_only": false},
					{"id": "deep", "name": "攻坚", "read_only": false}
				]
			}
		}`
		if err := json.Unmarshal([]byte(input), &raw); err != nil {
			t.Fatal(err)
		}

		got, err := migrateV13ToV14(raw)
		if err != nil {
			t.Fatalf("migrateV13ToV14 failed: %v", err)
		}
		tiers := got["orchestration"].(map[string]any)["tiers"].([]any)
		if len(tiers) != 5 {
			t.Fatalf("迁移后应有 5 个档位，实际 %d: %+v", len(tiers), tiers)
		}
		if !reflect.DeepEqual(tiers[4], expectedDesign) {
			t.Fatalf("追加的 design 档位不符：\ngot:  %#v\nwant: %#v", tiers[4], expectedDesign)
		}
	})

	t.Run("skip when design already exists", func(t *testing.T) {
		input := `{
			"version": 13,
			"orchestration": {
				"tiers": [
					{"id": "read", "name": "只读", "read_only": true},
					{"id": "design", "name": "自定义设计", "read_only": false}
				]
			}
		}`
		var raw, want map[string]any
		if err := json.Unmarshal([]byte(input), &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(input), &want); err != nil {
			t.Fatal(err)
		}

		got, err := migrateV13ToV14(raw)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("已有 design 时不应改动：got=%v, err=%v, want=%v", got, err, want)
		}
	})

	t.Run("skip when tiers missing or empty", func(t *testing.T) {
		for _, input := range []string{
			`{"version": 13}`,
			`{"version": 13, "orchestration": {}}`,
			`{"version": 13, "orchestration": {"tiers": []}}`,
			`{"version": 13, "orchestration": {"tiers": "invalid"}}`,
		} {
			var raw, want map[string]any
			if err := json.Unmarshal([]byte(input), &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(input), &want); err != nil {
				t.Fatal(err)
			}

			got, err := migrateV13ToV14(raw)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("无 tiers 或空列表时应保持原样：input=%s, got=%v, err=%v", input, got, err)
			}
		}
	})
}

func TestLoadMigratesV13DesignTierOnce(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{
		"version": 13,
		"orchestration": {
			"default_tier": "edit",
			"tiers": [
				{"id": "read", "name": "只读", "read_only": true},
				{"id": "chore", "name": "杂活", "read_only": false},
				{"id": "edit", "name": "执行", "read_only": false},
				{"id": "deep", "name": "攻坚", "read_only": false}
			]
		}
	}`)
	if err := os.WriteFile(ConfigPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load v13 config failed: %v", err)
	}
	if cfg.Version != Version {
		t.Fatalf("版本 = %d，期望 %d", cfg.Version, Version)
	}
	if len(cfg.Orchestration.Tiers) != 5 {
		t.Fatalf("迁移后应有 5 个档位，实际 %d: %+v", len(cfg.Orchestration.Tiers), cfg.Orchestration.Tiers)
	}
	design := cfg.Orchestration.Tiers[4]
	if design.ID != "design" || design.Name != "设计" || !design.SharedWorkspace || !design.Interactive || design.ReadOnly || design.Effort != "high" {
		t.Fatalf("迁移后的 design 档位不符：%+v", design)
	}

	// 用户删除 design 并保存后，再次加载不会再补回。
	cfg.Orchestration.Tiers = cfg.Orchestration.Tiers[:4]
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if len(reloaded.Orchestration.Tiers) != 4 {
		t.Fatalf("删除 design 后不应再被补回，实际 %d: %+v", len(reloaded.Orchestration.Tiers), reloaded.Orchestration.Tiers)
	}
}
