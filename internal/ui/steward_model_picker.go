package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func stewardModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	for _, provider := range catalog.Providers {
		if provider.ID == cfg.Orchestration.Steward.Provider {
			model := cfg.Orchestration.Steward.Model
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
