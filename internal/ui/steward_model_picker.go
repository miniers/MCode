package mcodeui

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/frontdesk"
)

var newStewardSecretStore = func() config.SecretStore { return config.NewSecretStore("mcode") }

func (st *SettingsState) stewardAutoLabel(catalog modelPickerCatalog, sel ModelSelection) string {
	var key strings.Builder
	add := func(value string) { key.WriteString(strconv.Quote(value)) }
	add(st.cfg.Orchestration.Steward.Provider)
	add(st.cfg.Orchestration.Steward.Model)
	for _, provider := range catalog.Providers {
		add(provider.ID)
		for _, model := range provider.Models {
			add(model.ID)
		}
		key.WriteByte(';')
	}
	cacheKey := key.String()
	st.stewardLabelMu.Lock()
	defer st.stewardLabelMu.Unlock()
	if st.stewardLabelKey != cacheKey {
		if st.stewardSecrets == nil {
			st.stewardSecrets = newStewardSecretStore()
		}
		st.stewardLabel = stewardModelEmptyLabel(st.cfg, catalog, sel, st.stewardSecrets)
		st.stewardLabelKey = cacheKey
	}
	return st.stewardLabel
}

func stewardModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	return catalogModelSelection(cfg.Orchestration.Steward.Provider, cfg.Orchestration.Steward.Model, catalog)
}

// stewardTaskModelSelection 返回总管下发任务的模型选择，提供方不在目录里时为空（自动）。
func stewardTaskModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	return catalogModelSelection(cfg.Orchestration.Steward.TaskProvider, cfg.Orchestration.Steward.TaskModel, catalog)
}

// stewardTaskPair 返回任务模型选中的编排配对；配对已删除或当前目录未就绪时视为没选。
func stewardTaskPair(cfg config.Config, ready []SwarmPair) (SwarmPair, bool) {
	id := cfg.Orchestration.Steward.TaskPairID
	for _, p := range ready {
		if id != "" && p.ID == id {
			return p, true
		}
	}
	return SwarmPair{}, false
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

func stewardModelEmptyLabel(cfg config.Config, catalog modelPickerCatalog, sel ModelSelection, secrets config.SecretStore) string {
	if !swarmEmptySelection(sel) {
		return "自动"
	}
	var sources []apisource.Source
	for _, src := range apisource.Resolve(cfg, secrets) {
		for _, provider := range catalog.Providers {
			if provider.ID != src.Kind {
				continue
			}
			if len(src.Models) == 0 {
				for _, model := range provider.Models {
					src.Models = append(src.Models, model.ID)
				}
			}
			sources = append(sources, src)
			break
		}
	}
	src, model, err := frontdesk.SelectSource(sources, secrets, sel.Provider, "")
	if err != nil {
		return "自动"
	}
	return modelShortDisplayName(catalog.Providers, src.Kind, model)
}

// renderStewardComposerModel 与设置页共用前台模型配置和保存路径。
func renderStewardComposerModel(c *ui.Context, s *chatState) {
	st := getChatSettings(s)
	if st == nil {
		return
	}
	catalog := frontdeskModelPickerCatalog(settingsModelPickerCatalog(st), st.cfg)
	sel := stewardModelSelection(st.cfg, catalog)
	renderSwarmModelCapsuleCatalog(c, st, "composer-steward-model", "composer-steward-model", sel, st.stewardAutoLabel(catalog, sel), "选择前台模型", func(next ModelSelection) {
		st.cfg.Orchestration.Steward.Provider = next.Provider
		st.cfg.Orchestration.Steward.Model = next.Model
		st.save()
	}, catalog, true)
}
