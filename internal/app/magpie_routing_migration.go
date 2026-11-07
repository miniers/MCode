package app

import (
	"mcode/internal/config"
	"mcode/internal/magpiecfg"
)

// migrateMagpieRoutingV1 恢复旧版 Cline 接管，并收敛旧版路由模式。
func migrateMagpieRoutingV1(dataDir string, cfg *config.Config) error {
	if cfg.Providers.MagpieRoutingVersion >= 1 {
		return nil
	}
	applied, err := magpiecfg.Applied(dataDir, config.ProviderCline)
	if err != nil {
		return err
	}
	if applied {
		if err := magpiecfg.Disconnect(dataDir, config.ProviderCline); err != nil {
			return err
		}
	}
	migrate := func(providers *config.ProvidersConfig) {
		if codex, ok := providers.MagpieAgents[config.ProviderCodex]; ok && codex.Mode == config.MagpieModeInject {
			codex.Mode = config.MagpieModeOff
			providers.MagpieAgents[config.ProviderCodex] = codex
		}
		delete(providers.MagpieAgents, config.ProviderCline)
		providers.MagpieRoutingVersion = 1
	}
	if err := config.Update(dataDir, func(latest *config.Config) error {
		migrate(&latest.Providers)
		return nil
	}); err != nil {
		return err
	}
	migrate(&cfg.Providers)
	return nil
}
