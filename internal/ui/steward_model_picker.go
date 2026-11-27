package mcodeui

import (
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/frontdesk"
)

var (
	newStewardSecretStore         = func() config.SecretStore { return config.NewSecretStore("mcode") }
	computeStewardModelEmptyLabel = stewardModelEmptyLabel
)

type stewardLabelAsyncState struct {
	loading    bool
	loadingKey string
	latestKey  string
	gen        uint64
}

var (
	stewardLabelAsyncMu     sync.Mutex
	stewardLabelAsyncStates = make(map[*SettingsState]*stewardLabelAsyncState)
)

func getStewardLabelAsyncState(st *SettingsState) *stewardLabelAsyncState {
	stewardLabelAsyncMu.Lock()
	defer stewardLabelAsyncMu.Unlock()
	state, ok := stewardLabelAsyncStates[st]
	if !ok {
		state = &stewardLabelAsyncState{}
		stewardLabelAsyncStates[st] = state
	}
	return state
}

func (st *SettingsState) stewardAutoLabel(catalog modelPickerCatalog, sel ModelSelection) string {
	if st == nil {
		return ""
	}
	var key strings.Builder
	add := func(value string) { key.WriteString(strconv.Quote(value)) }
	st.cfgMu.Lock()
	stewardProvider := st.cfg.Orchestration.Steward.Provider
	stewardModel := st.cfg.Orchestration.Steward.Model
	st.cfgMu.Unlock()
	add(stewardProvider)
	add(stewardModel)
	for _, provider := range catalog.Providers {
		add(provider.ID)
		for _, model := range provider.Models {
			add(model.ID)
		}
		key.WriteByte(';')
	}
	cacheKey := key.String()

	async := getStewardLabelAsyncState(st)
	st.stewardLabelMu.Lock()
	async.latestKey = cacheKey
	label := st.stewardLabel
	needStart := st.stewardLabelKey != cacheKey && (!async.loading || async.loadingKey != cacheKey)
	var secrets config.SecretStore
	var gen uint64
	if needStart {
		async.loading = true
		async.loadingKey = cacheKey
		async.gen++
		gen = async.gen
		secrets = st.stewardSecrets
	}
	st.stewardLabelMu.Unlock()

	if needStart {
		st.cfgMu.Lock()
		cfg := cloneConfig(st.cfg)
		st.cfgMu.Unlock()
		catCopy := modelPickerCatalog{
			Providers:    append([]ProviderInfo(nil), catalog.Providers...),
			MagpieFilter: catalog.MagpieFilter,
		}
		go func() {
			if secrets == nil {
				secrets = newStewardSecretStore()
			}
			computed := computeStewardModelEmptyLabel(cfg, catCopy, sel, secrets)

			st.stewardLabelMu.Lock()
			if st.stewardSecrets == nil {
				st.stewardSecrets = secrets
			}
			stale := async.gen != gen || async.latestKey != cacheKey
			if !stale {
				st.stewardLabel = computed
				st.stewardLabelKey = cacheKey
				async.loading = false
				async.loadingKey = ""
			}
			st.stewardLabelMu.Unlock()

			if !stale {
				st.invalidate()
			}
		}()
	}

	return label
}

func stewardModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	sel := catalogModelSelection(cfg.Orchestration.Steward.Provider, cfg.Orchestration.Steward.Model, catalog)
	if sel.Model != "" {
		sel.Effort = cfg.Orchestration.Steward.Effort
	}
	return sel
}

// stewardTaskModelSelection 返回总管下发任务的模型选择，提供方不在目录里时为空（自动）。
func stewardTaskModelSelection(cfg config.Config, catalog modelPickerCatalog) ModelSelection {
	sel := catalogModelSelection(cfg.Orchestration.Steward.TaskProvider, cfg.Orchestration.Steward.TaskModel, catalog)
	if sel.Model != "" {
		sel.Effort = cfg.Orchestration.Steward.TaskEffort
	}
	return sel
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
		st.cfg.Orchestration.Steward.Effort = next.Effort
		st.save()
	}, catalog, false)
}
