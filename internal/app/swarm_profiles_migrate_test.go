package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
	"mcode/internal/swarm"
)

func TestMigrateLegacySwarmProfiles_MissingFile(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Defaults()
	if err := migrateLegacySwarmProfiles(dataDir, &cfg); err != nil {
		t.Fatalf("missing profiles file should be a no-op, got error: %v", err)
	}
	if len(cfg.Orchestration.Tiers) != len(config.Defaults().Orchestration.Tiers) {
		t.Fatalf("tiers modified when file missing: %+v", cfg.Orchestration.Tiers)
	}
}

func TestMigrateLegacySwarmProfiles_ImportsAndSkipsExistingAndIdempotent(t *testing.T) {
	dataDir := t.TempDir()

	// 准备初始配置并保存到磁盘
	cfg := config.Defaults()
	cfg.Orchestration.Tiers = []config.SwarmTier{
		{
			ID:          "existing",
			Name:        "Existing Tier",
			Description: "原本就有的档位",
		},
	}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	// 准备旧版 hydra-profiles.json
	profiles := []swarm.BeeProfile{
		{
			Name:         "Quick Profile",
			Provider:     "claude",
			Model:        "haiku",
			Effort:       "low",
			SystemPrompt: "be quick",
		},
		{
			Name:         "Existing", // ID 生成为 "existing"，应当跳过
			Provider:     "codex",
			Model:        "gpt-4",
			Effort:       "high",
			SystemPrompt: "ignore me",
		},
		{
			Name:     "-_Special@Name!With#Symbols-1234567890abcdefghijklmnopqrstuvwxyz", // 截断至 32，去除前导 -_
			Provider: "custom",
			Model:    "llama",
			Effort:   "medium",
		},
		{
			Name: "???", // 转换为全部 '-' 并被 TrimLeft 去除，ID 为空，跳过
		},
	}
	data, err := json.Marshal(profiles)
	if err != nil {
		t.Fatalf("marshal profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, swarm.ProfilesFileName), data, 0o600); err != nil {
		t.Fatalf("write profiles file: %v", err)
	}

	// 第一次运行：导入
	if err := migrateLegacySwarmProfiles(dataDir, &cfg); err != nil {
		t.Fatalf("first migrate run failed: %v", err)
	}

	// 检查内存中 cfg
	if len(cfg.Orchestration.Tiers) != 3 {
		t.Fatalf("期望迁移后有 3 个档位，实际有 %d: %+v", len(cfg.Orchestration.Tiers), cfg.Orchestration.Tiers)
	}

	// 验证 "existing" 未被覆盖
	if cfg.Orchestration.Tiers[0].ID != "existing" || cfg.Orchestration.Tiers[0].Name != "Existing Tier" {
		t.Errorf("现有档位被篡改: %+v", cfg.Orchestration.Tiers[0])
	}

	// 验证 "quick profile" -> "quick-profile"
	p1 := cfg.Orchestration.Tiers[1]
	if p1.ID != "quick-profile" || p1.Name != "Quick Profile" || p1.Description != "从 hydra-profiles.json 导入" ||
		p1.Provider != "claude" || p1.Model != "haiku" || p1.Effort != "low" || p1.SystemPrompt != "be quick" {
		t.Errorf("导入的 profile 字段不符: %+v", p1)
	}

	// 验证截断与清理 ID
	p2 := cfg.Orchestration.Tiers[2]
	if len(p2.ID) > 32 {
		t.Errorf("ID 长度超过 32: %d (%q)", len(p2.ID), p2.ID)
	}
	if p2.ID != "special-name-with-symbols-123456" {
		t.Errorf("ID 生成不符合预期: %q", p2.ID)
	}

	// 验证磁盘上的配置已持久化
	persisted, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if len(persisted.Orchestration.Tiers) != 3 {
		t.Fatalf("磁盘配置未持久化导入的档位: %+v", persisted.Orchestration.Tiers)
	}

	// 第二次运行：幂等，不应再添加任何条目
	if err := migrateLegacySwarmProfiles(dataDir, &cfg); err != nil {
		t.Fatalf("second migrate run failed: %v", err)
	}
	if len(cfg.Orchestration.Tiers) != 3 {
		t.Fatalf("第二遍迁移重复添加了档位: %+v", cfg.Orchestration.Tiers)
	}

	// 磁盘上的文件依然存在（不删除旧文件）
	if _, err := os.Stat(filepath.Join(dataDir, swarm.ProfilesFileName)); err != nil {
		t.Fatalf("旧版 profiles 文件不应被删除: %v", err)
	}
}
