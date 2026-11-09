package apisource

import (
	"strings"

	"mcode/internal/config"
	"mcode/internal/harness"
)

// NormalizeBeeModel 只解析能够唯一识别的历史来源前缀，不修改配置。
func NormalizeBeeModel(cfg config.Config, secrets config.SecretStore, provider, model string) (string, string, bool) {
	if harness.IsCLICatalogProvider(provider) {
		return provider, model, false
	}
	head, rest, ok := strings.Cut(model, "/")
	if !ok || head == "" || rest == "" {
		return provider, model, false
	}
	if provider == "mcode" {
		if head == "anthropic" {
			if cfg.Providers.Anthropic.IsEnabled() {
				return head, rest, true
			}
			return provider, model, false
		}
		kind, matches := "", 0
		for _, target := range MagpieTargets(cfg, secrets) {
			if target.ProviderID != head {
				continue
			}
			for _, ep := range cfg.Providers.Custom {
				if ep.ID == target.Endpoint && ep.Enabled && ep.Magpie {
					kind = config.CustomEndpointProviderKind(ep)
					matches++
				}
			}
		}
		if matches == 1 {
			return kind, rest, true
		}
	} else if provider == "" {
		matches := 0
		for _, source := range Resolve(cfg, secrets) {
			if source.Kind == head {
				matches++
			}
		}
		if matches == 1 {
			return head, rest, true
		}
	}
	return provider, model, false
}
