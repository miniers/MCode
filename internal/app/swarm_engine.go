package app

import (
	"fmt"
	"strings"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/swarm"
)

// defaultBeeSource 选择主会话可用的 API 来源，或按配置顺序选择第一个可用来源。
func defaultBeeSource(cfg config.Config, secrets config.SecretStore, lead swarm.ThreadContext) (string, string, error) {
	provider, model, _ := apisource.NormalizeBeeModel(cfg, secrets, lead.Provider, lead.Model)
	var first *apisource.Source
	for _, source := range apisource.Resolve(cfg, secrets) {
		if source.APIKey(secrets) == "" {
			continue
		}
		if first == nil {
			copy := source
			first = &copy
		}
		if source.Kind == provider {
			if strings.TrimSpace(model) == "" {
				model = source.DefaultModel
			}
			return provider, model, nil
		}
	}
	if first == nil {
		return "", "", fmt.Errorf("没有可用的 API 来源，请先在设置中配置来源和凭据")
	}
	return first.Kind, first.DefaultModel, nil
}
