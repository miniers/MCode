package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func stewardModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	return catalogModelSelection(cfg.Orchestration.Steward.Provider, cfg.Orchestration.Steward.Model, catalog)
}

// stewardTaskModelSelection 返回总管下发任务的模型选择，提供方不在目录里时为空（自动）。
func stewardTaskModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	return catalogModelSelection(cfg.Orchestration.Steward.TaskProvider, cfg.Orchestration.Steward.TaskModel, catalog)
}

func catalogModelSelection(providerID, model string, catalog modelPickerCatalog) ModelSelection {
	for _, provider := range catalog.Providers {
		if provider.ID == providerID {
			if !modelUsable(provider, model, nil) {
				model = ""
			}
			return ModelSelection{Provider: provider.ID, Model: model}
		}
	}
	return ModelSelection{}
}

// renderStewardComposerModel 与设置页共用前台模型配置和保存路径。
func renderStewardComposerModel(c *ui.Context, s *chatState) {
	st := getChatSettings(s)
	if st == nil {
		return
	}
	catalog := frontdeskModelPickerCatalog(settingsModelPickerCatalog(st), st.cfg)
	sel := stewardModelSelection(st.cfg, catalog)
	renderSwarmModelCapsuleCatalog(c, st, "composer-steward-model", "composer-steward-model", sel, "自动", "选择前台模型", func(next ModelSelection) {
		st.cfg.Orchestration.Steward.Provider = next.Provider
		st.cfg.Orchestration.Steward.Model = next.Model
		st.save()
	}, catalog, true)
}
