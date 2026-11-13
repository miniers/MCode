package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestMigrateV12ToV13(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"enable", `{"orchestration":{"always_bees":false,"retry_failed":false,"lead_plans_only":false,"auto_clear_finished":false,"auto_hides_idle_bees":false,"auto_pops_bees":false}}`, `{"orchestration":{"always_bees":false,"retry_failed":true,"lead_plans_only":true,"auto_clear_finished":true,"auto_hides_idle_bees":true,"auto_pops_bees":true}}`},
		{"always bees", `{"orchestration":{"always_bees":true,"retry_failed":false,"lead_plans_only":false,"auto_clear_finished":false,"auto_hides_idle_bees":false,"auto_pops_bees":false}}`, `{"orchestration":{"always_bees":true,"retry_failed":true,"lead_plans_only":false,"auto_clear_finished":true,"auto_hides_idle_bees":true,"auto_pops_bees":true}}`},
		{"missing keys", `{"orchestration":{"always_bees":false}}`, `{"orchestration":{"always_bees":false}}`},
		{"missing orchestration", `{}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw, want map[string]any
			if err := json.Unmarshal([]byte(tc.input), &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			got, err := migrateV12ToV13(raw)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("迁移结果：%v，错误：%v，期望：%v", got, err, want)
			}
		})
	}
}

func TestLoadMigratesV12Orchestration(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"version":12,"orchestration":{"retry_failed":false,"lead_plans_only":false,"auto_clear_finished":false,"auto_hides_idle_bees":false,"auto_pops_bees":false}}`)
	if err := os.WriteFile(ConfigPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Orchestration
	if cfg.Version != Version || !o.RetryFailed || !o.LeadPlansOnly || !o.AutoClearFinished || !o.AutoHidesIdleBees || !o.AutoPopsBees {
		t.Fatalf("迁移结果：version=%d，orchestration=%+v", cfg.Version, o)
	}
}
