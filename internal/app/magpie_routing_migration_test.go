package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mcode/internal/config"
	"mcode/internal/magpiecfg"
)

func TestMigrateMagpieRoutingV1(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.MagpieAgents = map[string]config.MagpieAgentConfig{
		config.ProviderCodex:  {Mode: config.MagpieModeInject, Selections: []config.MagpieSelection{{EndpointID: "jp", Models: []string{"model"}}}},
		config.ProviderClaude: {Mode: config.MagpieModeInject},
		config.ProviderPi:     {Mode: config.MagpieModeGlobal},
		config.ProviderCline:  {Mode: config.MagpieModeGlobal},
	}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := migrateMagpieRoutingV1(dataDir, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers.MagpieRoutingVersion != 1 || cfg.Providers.MagpieAgents[config.ProviderCodex].Mode != config.MagpieModeOff || cfg.Providers.MagpieAgents[config.ProviderClaude].Mode != config.MagpieModeInject || cfg.Providers.MagpieAgents[config.ProviderPi].Mode != config.MagpieModeGlobal {
		t.Fatalf("迁移结果 = %+v", cfg.Providers)
	}
	if _, ok := cfg.Providers.MagpieAgents[config.ProviderCline]; ok {
		t.Fatal("cline 条目未删除")
	}
	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Providers.MagpieRoutingVersion != 1 || loaded.Providers.MagpieAgents[config.ProviderCodex].Mode != config.MagpieModeOff {
		t.Fatalf("持久化结果 = %+v", loaded.Providers)
	}

	cfg.Providers.MagpieAgents[config.ProviderCodex] = config.MagpieAgentConfig{Mode: config.MagpieModeInject}
	before := cfg.Providers.MagpieAgents[config.ProviderCodex]
	if err := migrateMagpieRoutingV1(dataDir, &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Providers.MagpieAgents[config.ProviderCodex], before) {
		t.Fatal("已迁移的配置不应改变")
	}
}

func TestMigrateMagpieRoutingV1RestoresCline(t *testing.T) {
	clineDir := t.TempDir()
	t.Setenv("CLINE_DATA_DIR", clineDir)
	dataDir := t.TempDir()
	path := filepath.Join(clineDir, "settings", "providers.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"lastUsedProvider\": \"original\"\n}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := magpiecfg.ClinePlan(magpiecfg.ClineOptions{BaseURL: "https://example.test/v1", APIKey: "key", Models: []magpiecfg.ClineModel{{ID: "model"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := magpiecfg.Connect(dataDir, plan); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if err := migrateMagpieRoutingV1(dataDir, &cfg); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("文件未恢复: %q，错误: %v", restored, err)
	}
	if applied, err := magpiecfg.Applied(dataDir, config.ProviderCline); err != nil || applied {
		t.Fatalf("接管状态 = %v，错误: %v", applied, err)
	}
	if cfg.Providers.MagpieRoutingVersion != 1 {
		t.Fatalf("迁移版本 = %d，想要 1", cfg.Providers.MagpieRoutingVersion)
	}
}

func TestMigrateMagpieRoutingV1RetriesOnStateError(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "agentcfg", "cline", "applied.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if err := migrateMagpieRoutingV1(dataDir, &cfg); err == nil {
		t.Fatal("损坏的接管记录应返回错误")
	}
	if cfg.Providers.MagpieRoutingVersion != 0 {
		t.Fatal("失败时不应写迁移版本")
	}
}

func TestMigrateMagpieRoutingV1RetriesOnRestoreError(t *testing.T) {
	clineDir := t.TempDir()
	t.Setenv("CLINE_DATA_DIR", clineDir)
	dataDir := t.TempDir()
	plan, err := magpiecfg.ClinePlan(magpiecfg.ClineOptions{BaseURL: "https://example.test/v1", APIKey: "key", Models: []magpiecfg.ClineModel{{ID: "model"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := magpiecfg.Connect(dataDir, plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clineDir, "settings", "providers.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if err := migrateMagpieRoutingV1(dataDir, &cfg); err == nil {
		t.Fatal("恢复失败应返回错误")
	}
	if cfg.Providers.MagpieRoutingVersion != 0 {
		t.Fatal("恢复失败时不应写迁移版本")
	}
	if applied, err := magpiecfg.Applied(dataDir, config.ProviderCline); err != nil || !applied {
		t.Fatalf("失败后接管状态 = %v，错误: %v", applied, err)
	}
}
