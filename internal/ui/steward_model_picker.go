package mcodeui

import "github.com/egoist/mygo/ui"

// renderStewardComposerModel 与设置页共用前台模型配置和保存路径。
func renderStewardComposerModel(c *ui.Context, s *chatState) {
	st := getChatSettings(s)
	if st == nil {
		return
	}
	sel := ModelSelection{
		Provider: st.cfg.Orchestration.Steward.Provider,
		Model:    st.cfg.Orchestration.Steward.Model,
	}
	renderSwarmModelCapsuleCatalog(c, st, "composer-steward-model", "composer-steward-model", sel, "跟随通用设置", "选择前台模型", func(next ModelSelection) {
		st.cfg.Orchestration.Steward.Provider = next.Provider
		st.cfg.Orchestration.Steward.Model = next.Model
		st.save()
	}, frontdeskModelPickerCatalog(settingsModelPickerCatalog(st), st.cfg), true)
}
