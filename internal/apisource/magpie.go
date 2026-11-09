package apisource

import (
	"mcode/internal/config"
	"mcode/internal/magpieinject"
)

// MagpieTargets 返回所有已启用的 Magpie 端点。
func MagpieTargets(cfg config.Config, secrets config.SecretStore) []magpieinject.Target {
	return magpieinject.Targets(cfg, secrets, nil)
}
