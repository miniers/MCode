package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/apisource"
)

// swarmEmptySelection 判断模型选择是否为空（未指定具体模型）。
func swarmEmptySelection(sel ModelSelection) bool {
	return strings.TrimSpace(sel.Model) == ""
}

// renderSwarmModelCapsule 在 Swarm 设置中渲染统一的模型选择胶囊组件。
// 封装 ModelPickerSimple 模式的 renderModelPicker，为 Swarm 配对编辑、档位映射、
// 全局协同模型及 Bee 档位编辑提供统一的模型、推理强度及快速模式选择能力。
func renderSwarmModelCapsule(c *ui.Context, st *SettingsState, key string, sel ModelSelection, emptyLabel, triggerLabel string, onChange func(ModelSelection)) {
	renderSwarmModelCapsuleEx(c, st, key, key, sel, emptyLabel, triggerLabel, onChange)
}

// renderSwarmModelCapsuleEx 与 renderSwarmModelCapsule 相同，但把元素键与状态作用域分开：
// 弹层内的胶囊会随弹层一起移动，键用固定名称来保持跨帧一致，指针命中的位置却随之改变。
func renderSwarmModelCapsuleEx(c *ui.Context, st *SettingsState, key, scopeKey string, sel ModelSelection, emptyLabel, triggerLabel string, onChange func(ModelSelection)) {
	renderSwarmModelCapsuleCatalog(c, st, key, scopeKey, sel, emptyLabel, triggerLabel, onChange, settingsModelPickerCatalog(st), false)
}

func normalizeSwarmSelection(st *SettingsState, sel ModelSelection) ModelSelection {
	if st != nil {
		sel.Provider, sel.Model, _ = apisource.NormalizeBeeModel(st.cfg, nil, sel.Provider, sel.Model)
	}
	return sel
}

func renderSwarmModelCapsuleCatalog(c *ui.Context, st *SettingsState, key, scopeKey string, sel ModelSelection, emptyLabel, triggerLabel string, onChange func(ModelSelection), catalog modelPickerCatalog, noEffort bool) {
	renderModelPicker(c, catalog.apply(swarmModelCapsuleProps(st, key, scopeKey, sel, emptyLabel, triggerLabel, onChange, catalog, noEffort)))
}

func swarmModelCapsuleProps(st *SettingsState, key, scopeKey string, sel ModelSelection, emptyLabel, triggerLabel string, onChange func(ModelSelection), catalog modelPickerCatalog, noEffort bool) ModelPickerProps {
	providers := catalog.Providers
	sel = normalizeSwarmSelection(st, sel)

	keyPrefix := key
	if !strings.HasSuffix(keyPrefix, "-") {
		keyPrefix += "-"
	}
	scopeKey = strings.TrimSuffix(scopeKey, "-")

	empty := swarmEmptySelection(sel)
	var capsuleLabel string
	var capsuleTooltip string
	if empty {
		capsuleLabel = emptyLabel
		if capsuleLabel == "" {
			capsuleLabel = "选择模型"
		}
		capsuleTooltip = capsuleLabel
	} else {
		sel = resolveModelPickerSelection(providers, sel)
		capsuleLabel = modelShortDisplayName(providers, sel.Provider, sel.Model)
		// 胶囊只显示短名，完整模型名与强度放在悬停提示里。
		capsuleTooltip = modelCapsuleLabel(modelPickerDisplayName(providers, sel.Provider, sel.Model), sel.Effort)
	}

	state := modelPickerStateFor(keyPrefix + scopeKey)
	if state.MenuOpen && (noEffort || swarmEmptySelection(sel)) && !state.listOpen(scopeKey, ModelPickerSideLead) {
		state.setListOpen(scopeKey, ModelPickerSideLead, true)
	}

	var invalidate func()
	if st != nil {
		invalidate = st.invalidate
	}

	disabled := false
	if st != nil && (st.isLocked(key) || st.isLocked(scopeKey)) {
		disabled = true
	}

	return ModelPickerProps{
		Mode:           ModelPickerSimple,
		KeyPrefix:      keyPrefix,
		ScopeKey:       scopeKey,
		State:          state,
		Selection:      sel,
		CapsuleLabel:   capsuleLabel,
		Tooltip:        capsuleTooltip,
		NoEffortSignal: empty || noEffort,
		TriggerLabel:   triggerLabel,
		Disabled:       disabled,
		OnPickModel: func(_ ModelPickerSide, prov ProviderInfo, m ModelInfo) {
			if onChange != nil {
				onChange(ModelSelection{
					Provider: prov.ID,
					Model:    m.ID,
					Effort:   sel.Effort,
					Fast:     sel.Fast,
				})
			}
			state.MenuOpen = false
			state.setListOpen(scopeKey, ModelPickerSideLead, false)
			clearModelPanelMoreOpen(scopeKey, "lead")
			clearModelCapsuleSnapshot(scopeKey)
		},
		OnEffort: func(_ ModelPickerSide, effort string) {
			if onChange != nil {
				onChange(ModelSelection{
					Provider: sel.Provider,
					Model:    sel.Model,
					Effort:   effort,
					Fast:     sel.Fast,
				})
			}
		},
		OnToggleFast: func(ModelPickerSide) {
			if onChange != nil {
				onChange(ModelSelection{
					Provider: sel.Provider,
					Model:    sel.Model,
					Effort:   sel.Effort,
					Fast:     !sel.Fast,
				})
			}
		},
		OnReset: func(ModelPickerSide) {
			if onChange != nil {
				onChange(ModelSelection{
					Provider: sel.Provider,
					Model:    sel.Model,
				})
			}
		},
		Invalidate: invalidate,
	}
}
