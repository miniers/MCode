package app

import (
	"strings"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote/wire"
)

// apiSourceCatalog 返回当前启用来源的模型目录。
func apiSourceCatalog(cfg config.Config, secrets config.SecretStore, kind string, legacyModelIDs []string) []wire.ProviderModel {
	for _, source := range apisource.Resolve(cfg, secrets) {
		if source.Kind != kind {
			continue
		}
		if source.Magpie {
			for _, endpoint := range cfg.Providers.Custom {
				if endpoint.Enabled && endpoint.Magpie && config.CustomEndpointProviderKind(endpoint) == kind {
					for _, target := range apisource.MagpieTargets(cfg, secrets) {
						if target.Endpoint == endpoint.ID {
							return orderCatalog(magpieCatalogTarget(target), source.DefaultModel)
						}
					}
				}
			}
			return nil
		}
		ids := append([]string(nil), source.Models...)
		if kind != "anthropic" {
			ids = append(ids, legacyModelIDs...)
		}
		if id := strings.TrimSpace(source.DefaultModel); id != "" {
			ids = append(ids, id)
		}
		rows := make([]wire.ProviderModel, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, wire.ProviderModel{ID: id, Name: id})
		}
		return orderCatalog(rows, source.DefaultModel)
	}
	return nil
}

// magpieCatalogTarget 从端点缓存构造裸模型 ID 的目录。
func magpieCatalogTarget(target magpieinject.Target) []wire.ProviderModel {
	if strings.TrimSpace(target.APIKey) == "" {
		return nil
	}
	models, hit := magpie.CachedModels(target.BaseURL, target.APIKey)
	if !hit {
		warmMagpieModels(target)
		return nil
	}
	rows := make([]wire.ProviderModel, 0, len(models))
	for _, model := range models {
		rows = append(rows, wire.ProviderModel{
			ID: model.ID, Name: magpie.ShortName(model), Efforts: mcodeEfforts(model),
			ContextLimit: model.ContextWindow, Cost: magpieModelCost(model),
		})
	}
	return rows
}

// orderCatalog 去重并将存在于目录中的默认模型移到首位。
func orderCatalog(rows []wire.ProviderModel, defaultID string) []wire.ProviderModel {
	defaultID = strings.TrimSpace(defaultID)
	seen := make(map[string]bool, len(rows))
	result := make([]wire.ProviderModel, 0, len(rows))
	defaultIndex := -1
	for _, row := range rows {
		row.ID = strings.TrimSpace(row.ID)
		if row.ID == "" || seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		row.IsDefault = false
		if row.ID == defaultID {
			defaultIndex = len(result)
		}
		result = append(result, row)
	}
	if defaultIndex > 0 {
		row := result[defaultIndex]
		copy(result[1:defaultIndex+1], result[:defaultIndex])
		result[0] = row
	}
	if len(result) > 0 {
		result[0].IsDefault = true
	}
	return result
}
