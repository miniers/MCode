package app

import (
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/providers/magpie"
)

func frontdeskSources(cfg config.Config, secrets config.SecretStore) []apisource.Source {
	sources := apisource.Resolve(cfg, secrets)
	for i := range sources {
		src := &sources[i]
		if !src.Magpie || len(src.Models) != 0 {
			continue
		}
		models, ok := magpie.CachedModels(src.BaseURL, src.APIKey(secrets))
		if !ok {
			continue
		}
		for _, model := range models {
			src.Models = append(src.Models, model.ID)
		}
	}
	return sources
}
