package app

import (
	"path/filepath"
	"strings"

	"mcode/internal/config"
	"mcode/internal/swarm"
)

// legacyProfileID 根据 profile 名字生成合法的档位 ID：转小写，将 [a-z0-9_-] 之外的字符替换为 '-'，
// 去除前导 '-' 与 '_'，截断至最多 32 个字符。
func legacyProfileID(name string) string {
	id := strings.ToLower(name)
	id = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, id)
	id = strings.TrimLeft(id, "-_")
	if len(id) > 32 {
		id = id[:32]
	}
	return id
}

// migrateLegacySwarmProfiles 将旧版遗留的 hydra-profiles.json 导入为 config.OrchestrationConfig.Tiers。
// 该函数具备幂等性，已存在的档位 ID 不会重复导入，旧文件保留不删除。
func migrateLegacySwarmProfiles(dataDir string, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	profilesPath := filepath.Join(dataDir, swarm.ProfilesFileName)
	profiles, err := swarm.LoadProfiles(profilesPath)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return nil
	}

	existing := make(map[string]struct{}, len(cfg.Orchestration.Tiers))
	for _, t := range cfg.Orchestration.Tiers {
		existing[t.ID] = struct{}{}
	}

	var added bool
	for _, p := range profiles {
		id := legacyProfileID(p.Name)
		if id == "" {
			continue
		}
		if _, ok := existing[id]; ok {
			continue
		}
		existing[id] = struct{}{}
		cfg.Orchestration.Tiers = append(cfg.Orchestration.Tiers, config.SwarmTier{
			ID:           id,
			Name:         p.Name,
			Description:  "从 hydra-profiles.json 导入",
			Provider:     p.Provider,
			Model:        p.Model,
			Effort:       p.Effort,
			SystemPrompt: p.SystemPrompt,
		})
		added = true
	}

	if !added {
		return nil
	}

	return config.Update(dataDir, func(latest *config.Config) error {
		latest.Orchestration.Tiers = cfg.Orchestration.Tiers
		return nil
	})
}
