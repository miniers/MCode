package mcodeui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/reviewcheck"
)

var iconArrowRight = parseLucideSVG(`<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>`)

// swarmPairsUIState 维护模型配对弹层与编辑草稿的临时状态。
type swarmPairsUIState struct {
	openPopoverKey string // "" 表示关闭，"add" 表示新增配对，"edit-<id>" 表示编辑指定配对
	isNew          bool
	draft          config.SwarmPair
	presetOpen     bool
}

// getSwarmUIState 返回编排页的弹层状态：第一次访问时在 st.mu 锁内创建，
// 页面关闭后由 SettingsState.Close 清空。
func getSwarmUIState(st *SettingsState) *swarmPairsUIState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.swarmPage == nil {
		st.swarmPage = &swarmPairsUIState{}
	}
	return st.swarmPage
}

// swarmSettingsProviders 获取当前可用的 Provider 列表，若无则使用默认 fallback 列表。
func swarmSettingsProviders(st *SettingsState) []ProviderInfo {
	if st == nil {
		return []ProviderInfo{
			{ID: config.ProviderClaude, Name: "Claude"},
			{ID: config.ProviderCodex, Name: "Codex"},
			{ID: config.ProviderAgy, Name: "agy"},
		}
	}
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()

	var providers []ProviderInfo
	if chat != nil {
		chat.mu.Lock()
		providers = append([]ProviderInfo(nil), chat.providers...)
		chat.mu.Unlock()
	}
	if len(providers) == 0 {
		providers = []ProviderInfo{
			{ID: config.ProviderClaude, Name: "Claude"},
			{ID: config.ProviderCodex, Name: "Codex"},
			{ID: config.ProviderAgy, Name: "agy"},
		}
	}
	return providers
}

// cloneSwarmPair 深拷贝草稿配对：BeeTiers 是 map、两个 Effort 是指针，
// 浅拷贝会让编辑器的改动直接写进运行态配置，点「取消」也已经在 cfg 里改了档位映射（S-094）。
func cloneSwarmPair(p config.SwarmPair) config.SwarmPair {
	copyPtr := func(s *string) *string {
		if s == nil {
			return nil
		}
		v := *s
		return &v
	}
	p.LeadEffort = copyPtr(p.LeadEffort)
	p.WorkerEffort = copyPtr(p.WorkerEffort)
	if p.BeeTiers != nil {
		tiers := make(map[string]config.SwarmTierOverride, len(p.BeeTiers))
		for id, ov := range p.BeeTiers {
			if ov.FastMode != nil {
				f := *ov.FastMode
				ov.FastMode = &f
			}
			tiers[id] = ov
		}
		p.BeeTiers = tiers
	}
	return p
}

// swarmPairSave 保存草稿配对。新建时保存调用 AddPair，编辑时保存调用 UpdatePair，然后 st.save() 并返回配对项。
func swarmPairSave(st *SettingsState, draft config.SwarmPair, isNew bool) config.SwarmPair {
	if st == nil {
		return draft
	}
	var res config.SwarmPair
	if isNew {
		res = st.cfg.Orchestration.AddPair(draft)
	} else {
		if st.cfg.Orchestration.UpdatePair(draft) {
			res = draft
		} else {
			res = st.cfg.Orchestration.AddPair(draft)
		}
	}
	st.save()
	return res
}

// swarmPairDelete 按 ID 删除配对项并持久化保存。
func swarmPairDelete(st *SettingsState, id string) bool {
	if st == nil {
		return false
	}
	ok := st.cfg.Orchestration.RemovePair(id)
	if ok {
		st.save()
	}
	return ok
}

// swarmPairTitle 计算配对标题。未指定自定义名称时显示“主导模型短名 + 执行模型短名”；
// Name 是旧版自动生成的名字（全名标题或全名快速保存名）时也按未命名处理，直接显示短名。
func swarmPairTitle(p config.SwarmPair, providers []ProviderInfo) string {
	if p.Name != "" && !swarmPairIsAutoName(p, providers) {
		return p.Name
	}
	leadModel := swarmPairModelShortLabel(providers, p.LeadProvider, p.LeadModel, "任意模型")
	workerModel := swarmPairModelShortLabel(providers, swarmWorkerProvider(p), p.WorkerModel, "跟随主代理")
	return leadModel + " + " + workerModel
}

// swarmPairFullTitle 按旧版规则用模型全名拼出配对标题，用于识别历史保存的自动名称。
func swarmPairFullTitle(p config.SwarmPair, providers []ProviderInfo) string {
	lead := swarmPairModelLabel(providers, p.LeadProvider, p.LeadModel, "任意模型")
	worker := swarmPairModelLabel(providers, swarmWorkerProvider(p), p.WorkerModel, "跟随主代理")
	return lead + " + " + worker
}

// swarmPairIsAutoName 判断配对名是否为自动生成：旧版全名标题，或旧版桌面快速保存用全名拼出的默认名。
// 两种情况都按未命名处理，标题显示短名；自定义名一律保留。
func swarmPairIsAutoName(p config.SwarmPair, providers []ProviderInfo) bool {
	if p.Name == "" {
		return false
	}
	if p.Name == swarmPairFullTitle(p, providers) {
		return true
	}
	return p.Name == swarmPairLegacyQuickSaveName(p, providers)
}

// swarmPairLegacyQuickSaveName 按旧版快速保存的规则用模型全名拼出默认名，用于识别历史自动名。
// 执行侧 WorkerModel 为空表示跟随主导，这一侧不参与拼接。
func swarmPairLegacyQuickSaveName(p config.SwarmPair, providers []ProviderInfo) string {
	leadName := strings.TrimSpace(modelPickerDisplayName(providers, p.LeadProvider, p.LeadModel))
	workerName := ""
	if p.WorkerModel != "" {
		workerName = strings.TrimSpace(modelPickerDisplayName(providers, strings.TrimSpace(swarmWorkerProvider(p)), p.WorkerModel))
	}
	switch {
	case leadName == "" && workerName == "":
		return "模型配对"
	case workerName == "":
		return leadName
	case leadName == "":
		return workerName
	}
	return leadName + " + " + workerName
}

// swarmPairSummary 生成配对项的摘要文本（显示推理强度及快速模式标签）。
func swarmPairSummary(p config.SwarmPair) string {
	var parts []string
	var leadDesc string
	if p.LeadEffort != nil && *p.LeadEffort != "" {
		leadDesc = effortLabel(*p.LeadEffort)
	}
	if p.LeadFastMode {
		if leadDesc != "" {
			leadDesc += " · 快速"
		} else {
			leadDesc = "快速"
		}
	}
	if leadDesc != "" {
		parts = append(parts, "主代理: "+leadDesc)
	}

	var workerDesc string
	if p.WorkerEffort != nil && *p.WorkerEffort != "" {
		workerDesc = effortLabel(*p.WorkerEffort)
	}
	if p.WorkerFastMode {
		if workerDesc != "" {
			workerDesc += " · 快速"
		} else {
			workerDesc = "快速"
		}
	}
	if workerDesc != "" {
		parts = append(parts, "子代理: "+workerDesc)
	}

	if len(parts) == 0 {
		return "默认强度"
	}
	return strings.Join(parts, " · ")
}

// findSwarmModelInfo 查找指定提供方与模型的 ModelInfo，未指定模型时聚合该提供方的推理档位。
func findSwarmModelInfo(providers []ProviderInfo, providerID, modelID string) ModelInfo {
	var targetProv *ProviderInfo
	for i := range providers {
		if providers[i].ID == providerID {
			targetProv = &providers[i]
			break
		}
	}
	if targetProv == nil {
		return ModelInfo{}
	}
	if modelID != "" {
		if m, ok := findModelInfo(targetProv.Models, modelID); ok {
			return m
		}
	}
	effortsMap := make(map[string]bool)
	var efforts []string
	var defEffort string
	supportsFast := false
	for _, m := range targetProv.Models {
		if defEffort == "" && m.DefaultEffort != "" {
			defEffort = m.DefaultEffort
		}
		if m.SupportsFast {
			supportsFast = true
		}
		for _, eff := range m.Efforts {
			if !effortsMap[eff] {
				effortsMap[eff] = true
				efforts = append(efforts, eff)
			}
		}
	}
	return ModelInfo{
		ID:            modelID,
		Name:          modelID,
		Efforts:       efforts,
		DefaultEffort: defEffort,
		SupportsFast:  supportsFast,
	}
}

// renderSwarmPairEditorPopover 渲染添加与编辑模型配对的 Popover 弹层。
func renderSwarmPairEditorPopover(c *ui.Context, st *SettingsState, uiSt *swarmPairsUIState, popKey string, providers []ProviderInfo) {
	providers = append([]ProviderInfo(nil), providers...)
	for _, id := range []string{uiSt.draft.LeadProvider, uiSt.draft.WorkerProvider} {
		if id != "" {
			if _, found := findProviderInfo(providers, id); !found {
				providers = append(providers, ProviderInfo{ID: id, Name: id})
			}
		}
	}
	ui.Scroll(c.Key("swarm-editor-scroll-" + popKey)).Width(settingsEditorPopoverWidth).MaxHeight(settingsEditorPopoverMaxHeight).Children(func() {
		ui.Column(c.Key("swarm-editor-card-"+popKey)).
			Width(settingsEditorPopoverWidth).
			Padding(settingsCardPad, Space5).
			Gap(Space4).
			Background(ThemeSurface(c)).
			Border(Hairline, ThemeBorder(c)).
			Radius(RadiusMD).
			Shadow(0, 6, 20, 0, ThemeOverlay(c, 0.18)).
			Children(func() {
				formTitle := "编辑模型配对"
				if uiSt.isNew {
					formTitle = "添加模型配对"
				}
				ui.Text(c, formTitle).
					FontSize(TypeProse).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))

				// 1. 配对名称输入框
				ui.Column(c.Key("field-name-" + popKey)).
					Gap(Space1).
					Children(func() {
						ui.Text(c, "配对名称").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
						textFunc(func(*SettingsState) (string, bool) {
							return uiSt.draft.Name, false
						}, func(_ *SettingsState, text string) {
							uiSt.draft.Name = text
						}, "自定义配对名称（可选）").render(c, st, setting{ID: "swarm-pair-name-" + popKey, Title: "配对名称"})
					})

				// 2. 主导区
				ui.Row(c.Key("sec-lead-" + popKey)).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Children(func() {
						ui.Text(c, "主代理模型").
							FontSize(TypeFootnote).
							FontWeight(600).
							TextColor(ThemePrimaryText(c))

						leadEffort := ""
						if uiSt.draft.LeadEffort != nil {
							leadEffort = *uiSt.draft.LeadEffort
						}
						leadSel := ModelSelection{
							Provider: uiSt.draft.LeadProvider,
							Model:    uiSt.draft.LeadModel,
							Effort:   leadEffort,
							Fast:     uiSt.draft.LeadFastMode,
						}
						renderSwarmModelCapsuleEx(c, st, "swarm-pair-lead-capsule", "swarm-draft-lead-"+popKey, leadSel, "选择模型", "选择主代理模型", true, func(next ModelSelection) {
							uiSt.draft.LeadProvider = next.Provider
							uiSt.draft.LeadModel = next.Model
							if next.Effort != "" {
								v := next.Effort
								uiSt.draft.LeadEffort = &v
							} else {
								uiSt.draft.LeadEffort = nil
							}
							uiSt.draft.LeadFastMode = next.Fast
							st.invalidate()
						})
					})

				// 3. 执行区
				ui.Row(c.Key("sec-worker-" + popKey)).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Children(func() {
						ui.Text(c, "子代理模型").
							FontSize(TypeFootnote).
							FontWeight(600).
							TextColor(ThemePrimaryText(c))

						workerEffort := ""
						if uiSt.draft.WorkerEffort != nil {
							workerEffort = *uiSt.draft.WorkerEffort
						}
						workerSel := ModelSelection{
							Provider: uiSt.draft.WorkerProvider,
							Model:    uiSt.draft.WorkerModel,
							Effort:   workerEffort,
							Fast:     uiSt.draft.WorkerFastMode,
						}
						ui.Row(c.Key("wrap-worker-capsule-" + popKey)).
							AlignItems(ui.Center).
							Gap(Space1).
							Children(func() {
								renderSwarmModelCapsuleEx(c, st, "swarm-pair-worker-capsule", "swarm-draft-worker-"+popKey, workerSel, "与主代理相同", "选择子代理模型", true, func(next ModelSelection) {
									uiSt.draft.WorkerProvider = next.Provider
									uiSt.draft.WorkerModel = next.Model
									if next.Effort != "" {
										v := next.Effort
										uiSt.draft.WorkerEffort = &v
									} else {
										uiSt.draft.WorkerEffort = nil
									}
									uiSt.draft.WorkerFastMode = next.Fast
									st.invalidate()
								})

								if !swarmEmptySelection(workerSel) {
									btnClear := ui.ButtonBase(c.Key("btn-clear-worker-"+popKey)).
										Label("与主代理相同").
										Padding(settingsBadgePadY, Space3).
										Radius(RadiusSM).
										Background(ui.Transparent)
									if btnClear.Hovered() {
										btnClear.Background(ThemeHover(c))
									}
									if btnClear.Clicked() {
										uiSt.draft.WorkerProvider = ""
										uiSt.draft.WorkerModel = ""
										uiSt.draft.WorkerEffort = nil
										uiSt.draft.WorkerFastMode = false
										st.invalidate()
									}
									btnClear.Children(func() {
										ui.Text(c, "与主代理相同").
											FontSize(TypeCaption).
											TextColor(ThemeSecondaryText(c))
									})
								}
							})
					})

				// 4. 档位映射区
				if len(st.cfg.Orchestration.Tiers) > 0 {
					ui.Column(c.Key("sec-tier-mappings-" + popKey)).
						Gap(Space2).
						Children(func() {
							ui.Row(c.Key("header-tier-mappings-" + popKey)).
								AlignItems(ui.Center).
								Gap(Space3).
								Children(func() {
									ui.Text(c, "档位映射").
										FontSize(TypeFootnote).
										FontWeight(600).
										TextColor(ThemePrimaryText(c))
								})

							for _, t := range st.cfg.Orchestration.Tiers {
								tier := t
								var override config.SwarmTierOverride
								var hasOverride bool
								if uiSt.draft.BeeTiers != nil {
									override, hasOverride = uiSt.draft.BeeTiers[tier.ID]
								}

								fastVal := false
								if override.FastMode != nil {
									fastVal = *override.FastMode
								}
								tierSel := ModelSelection{
									Provider: override.Provider,
									Model:    override.Model,
									Effort:   override.Effort,
									Fast:     fastVal,
								}
								globalCapsuleText := swarmModelCapsuleText(providers, tier.Provider, tier.Model, tier.Effort, tier.FastMode)
								emptyLabel := "继承 · " + globalCapsuleText
								tierName := tier.Name
								if tierName == "" {
									tierName = tier.ID
								}

								ui.Row(c.Key("tier-map-row-" + tier.ID + "-" + popKey)).
									AlignItems(ui.Center).
									Justify(ui.SpaceBetween).
									Children(func() {
										renderSwarmTierChip(c, "tier-map-chip-"+tier.ID+"-"+popKey, tier.Name, tier.ID, tier.ReadOnly)

										ui.Row(c.Key("tier-map-right-" + tier.ID + "-" + popKey)).
											Label("tier-map-capsule-" + tier.ID).
											AlignItems(ui.Center).
											Gap(Space1).
											Children(func() {
												renderSwarmModelCapsuleEx(c, st, "swarm-pair-tier-capsule-"+tier.ID, "tier-map-"+tier.ID+"-"+popKey, tierSel, emptyLabel, "选择"+tierName+"档模型", true, func(next ModelSelection) {
													f := next.Fast
													if uiSt.draft.BeeTiers == nil {
														uiSt.draft.BeeTiers = make(map[string]config.SwarmTierOverride)
													}
													uiSt.draft.BeeTiers[tier.ID] = config.SwarmTierOverride{
														Provider: next.Provider,
														Model:    next.Model,
														Effort:   next.Effort,
														FastMode: &f,
													}
													st.invalidate()
												})

												if hasOverride {
													btnReset := ui.ButtonBase(c.Key("btn-reset-tier-map-"+tier.ID+"-"+popKey)).
														Label("恢复继承").
														Tooltip("恢复继承").
														Size(ControlXS, ControlXS).
														Radius(RadiusPill).
														Center()
													if btnReset.Hovered() {
														btnReset.Background(ThemeHover(c))
													} else {
														btnReset.Background(ui.Transparent)
													}
													if btnReset.Clicked() {
														delete(uiSt.draft.BeeTiers, tier.ID)
														if len(uiSt.draft.BeeTiers) == 0 {
															uiSt.draft.BeeTiers = nil
														}
														st.invalidate()
													}
													btnReset.Children(func() {
														ui.Icon(c, iconRotateCcw).
															Size(settingsTinyIcon, settingsTinyIcon).
															TextColor(ThemeSecondaryText(c))
													})
												}
											})
									})
							}
						})
				}

				// 5. 底部按钮（取消、保存）
				ui.Row(c.Key("actions-" + popKey)).
					Justify(ui.End).
					Gap(Space3).
					Children(func() {
						btnCancel := ui.ButtonBase(c.Key("btn-cancel-"+popKey)).
							Label("取消").
							Padding(settingsPillPadX, Space4).
							Radius(RadiusSM).
							Background(ThemeHover(c))
						if btnCancel.Clicked() {
							uiSt.openPopoverKey = ""
							st.invalidate()
						}
						btnCancel.Children(func() {
							ui.Text(c, "取消").FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
						})

						btnSave := ui.ButtonBase(c.Key("btn-save-"+popKey)).
							Label("保存").
							Padding(settingsPillPadX, Space4).
							Radius(RadiusSM).
							Background(ThemeAccent(c))
						if btnSave.Clicked() {
							swarmPairSave(st, uiSt.draft, uiSt.isNew)
							uiSt.openPopoverKey = ""
							st.invalidate()
						}
						btnSave.Children(func() {
							ui.Text(c, "保存").FontSize(TypeBody).TextColor(ThemeOnAccent(c))
						})
					})
			})
	})
}

func resetSwarmPairCapabilities(pair *config.SwarmPair, lead bool) {
	if lead {
		pair.LeadEffort, pair.LeadFastMode = nil, false
	}
	if !lead || (swarmWorkerProvider(*pair) == pair.LeadProvider && pair.WorkerModel == "") {
		pair.WorkerEffort, pair.WorkerFastMode = nil, false
	}
}

// renderSwarmPairRow 渲染单条模型配对行。
func renderSwarmPairRow(c *ui.Context, st *SettingsState, uiSt *swarmPairsUIState, p config.SwarmPair, providers []ProviderInfo) {
	ui.Row(c.Key("row-swarm-pair-"+p.ID)).
		Padding(settingsRowPadY, settingsRowPadX).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Children(func() {
			ui.Row(c.Key("content-swarm-pair-" + p.ID)).
				Gap(Space4).
				AlignItems(ui.Center).
				Grow(1).
				Children(func() {
					ui.Row(c.Key("icons-swarm-pair-" + p.ID)).
						Gap(Space1).
						AlignItems(ui.Center).
						Children(func() {
							renderProviderIcon(c, p.LeadProvider, 16)
							ui.Icon(c, iconArrowRight).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
							workerProv := p.WorkerProvider
							if workerProv == "" {
								workerProv = p.LeadProvider
							}
							renderProviderIcon(c, workerProv, 12)
						})

					ui.Column(c.Key("texts-swarm-pair-" + p.ID)).
						Gap(Space0).
						Children(func() {
							ui.Text(c, swarmPairTitle(p, providers)).
								FontSize(TypeBody).
								FontWeight(500).
								TextColor(ThemePrimaryText(c))
							ui.Text(c, swarmPairSummary(p)).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						})
				})

			ui.Row(c.Key("actions-swarm-pair-" + p.ID)).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					btnEdit := ui.ButtonBase(c.Key("btn-swarm-pair-edit-"+p.ID)).
						Label("编辑").
						Padding(settingsBadgePadY, settingsCompactPadX).
						Radius(RadiusSM).
						Background(ThemeHover(c))
					// 只在编辑器尚未打开时初始化草稿：弹层打开期间再次点“编辑”不能把
					// 已修改的草稿重置回配对原值。
					if btnEdit.Clicked() && uiSt.openPopoverKey != "edit-"+p.ID {
						uiSt.openPopoverKey = "edit-" + p.ID
						uiSt.isNew = false
						// 必须深拷贝：BeeTiers 是 map，直接赋值会让草稿写穿到运行态配置，
						// 点「取消」也已经在 cfg 里改了档位映射，下一次 save() 就落盘（S-094）。
						uiSt.draft = cloneSwarmPair(p)
						st.invalidate()
					}
					btnEdit.Children(func() {
						ui.Text(c, "编辑").FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
					})

					popKey := "edit-" + p.ID
					wasOpen := (uiSt.openPopoverKey == popKey)
					isPopOpen := wasOpen
					appPopover(c.Key("popover-swarm-pair-"+popKey), btnEdit, &isPopOpen, nil, func() {
						renderSwarmPairEditorPopover(c, st, uiSt, popKey, providers)
					})
					// 草稿弹层只能通过取消/保存关闭；外部点击（含嵌套模型选择器）不能丢弃草稿。
					if wasOpen && uiSt.openPopoverKey == popKey {
						isPopOpen = true
					}

					btnDelete := ui.ButtonBase(c.Key("btn-swarm-pair-delete-"+p.ID)).
						Label("删除").
						Padding(settingsBadgePadY, settingsCompactPadX).
						Radius(RadiusSM).
						Background(ui.Transparent)
					if btnDelete.Hovered() {
						btnDelete.Background(ThemeDanger(c).Alpha(0.12))
					}
					if btnDelete.Clicked() {
						swarmPairDelete(st, p.ID)
						st.invalidate()
					}
					btnDelete.Children(func() {
						ui.Text(c, "删除").FontSize(TypeFootnote).TextColor(ThemeDanger(c))
					})
				})
		})
}

// formatSwarmRequestTimeoutText 把请求总时长预算（分钟）格式化为「3 小时」「2 小时 30 分钟」「45 分钟」。
func formatSwarmRequestTimeoutText(minutes int) string {
	if minutes <= 0 {
		minutes = config.DefaultSwarmRequestTimeoutMinutes
	}
	hours := minutes / 60
	mins := minutes % 60
	switch {
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%d 小时", hours)
	default:
		return fmt.Sprintf("%d 分钟", mins)
	}
}

// truncateReviewCheckCommand 把检查命令裁到 config.MaxReviewCheckCommandLen 字节以内，
// 并避免切断多字节字符，与配置归一化的截断规则一致。
func truncateReviewCheckCommand(s string) string {
	if len(s) <= config.MaxReviewCheckCommandLen {
		return s
	}
	cut := config.MaxReviewCheckCommandLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

var pageSwarm = settingsPage{
	ID: "swarm", Title: "编排", Icon: IconSwarm(),
	Sections: []settingsSection{
		{ID: "swarm.main", Title: "", Rows: []setting{
			{
				ID: "row-swarm-enabled", Title: "启用编排", Lock: "swarm-enabled",
				Aliases: []string{"由主代理拆分任务，派给多个子代理并行执行，完成后统一审查合并", "编排", "子代理"},
				Control: toggle(ptr(func(c *config.Config) **bool { return &c.Orchestration.Enabled }, true)),
			},
		}},
		{ID: "swarm.pairs", Title: "模型配对", Rows: []setting{
			{ID: "block-swarm-pairs", Aliases: []string{"模型配对", "编排", "子代理"}, Control: customFullWidth(renderSwarmPairsBody)},
		}},
		{ID: "swarm.tiers", Title: "子代理档位", Rows: []setting{
			{ID: "block-swarm-tiers", Aliases: []string{"子代理档位", "编排", "子代理"}, Control: customFullWidth(renderSwarmTiersBody)},
		}},
		{ID: "swarm.bees", Title: "子代理", Rows: []setting{
			{
				ID: "row-swarm-workspace-mode", Title: "工作空间模式", Lock: "swarm-workspace-mode",
				Aliases: []string{"每个子代理在独立工作树里改代码，互不干扰"},
				Control: swarmPickerControl{
					key: "picker-swarm-workspace-mode",
					options: []visualPickerOption{
						{value: config.WorkspaceModeWorktree, label: "独立副本 (Worktree)", icon: IconGitBranch()},
						{value: config.WorkspaceModeShared, label: "共享工作区 (Shared)", icon: IconFolder()},
					},
					value: func(st *SettingsState) string {
						mode := st.cfg.Orchestration.WorkspaceMode
						if mode == "" {
							mode = config.WorkspaceModeWorktree
						}
						return mode
					},
					set: func(st *SettingsState, mode string) {
						commitSettingsControl(st, func(cfg *config.Config) { cfg.Orchestration.WorkspaceMode = mode })
					},
				},
			},
			{
				ID: "row-swarm-retry-failed", Title: "失败时自动重试", Lock: "swarm-retry-failed",
				Aliases: []string{"子代理遇到异常失败时自动重试一次"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.RetryFailed }, true)),
			},
			{
				ID: "row-swarm-auto-clear-finished", Title: "自动清理已完成子代理", Lock: "swarm-auto-clear-finished",
				Aliases: []string{"面板只留进行中的子代理"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.AutoClearFinished }, true)),
			},
			{
				ID: "row-swarm-auto-hides-idle-bees", Title: "侧边栏自动隐藏空闲子代理", Lock: "swarm-auto-hides-idle-bees",
				Aliases: []string{"空闲子代理收起，点开侧边栏可展开"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.AutoHidesIdleBees }, true)),
			},
			{
				ID: "row-swarm-shows-bee-details", Title: "显示子代理详细步骤",
				Aliases: []string{"关闭时只显示进度，开启时显示每个执行步骤"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.ShowsBeeDetails }, true)),
			},
			{
				ID: "row-swarm-auto-pops-bees", Title: "自动弹出子代理面板",
				Aliases: []string{"关闭时集中显示，开启时为空间允许的子代理分别显示面板"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.AutoPopsBees }, true)),
			},
			{
				ID: "row-swarm-archives-codex-bees", Title: "Codex 子会话历史", Lock: "swarm-archives-codex-bees",
				Aliases: []string{"只归档成功完成的 Codex 子会话，可后续恢复"},
				Control: swarmPickerControl{
					key: "picker-swarm-archives-codex-bees",
					options: []visualPickerOption{
						{value: "keep", label: "保持可见", icon: IconArchive()},
						{value: "archive", label: "自动归档", icon: IconArchive()},
					},
					value: func(st *SettingsState) string {
						if st.cfg.Orchestration.ArchivesCodexBees {
							return "archive"
						}
						return "keep"
					},
					set: func(st *SettingsState, value string) {
						commitSettingsControl(st, func(cfg *config.Config) { cfg.Orchestration.ArchivesCodexBees = value == "archive" })
					},
				},
			},
		}},
		{ID: "swarm.messages", Title: "消息流向", Rows: []setting{
			{
				ID: "row-swarm-auto-delegate", Title: "自动委托", Lock: "swarm-auto-delegate",
				Aliases: []string{"需要拆分任务时由主代理自动派给子代理"},
				Control: toggle(ptr(func(c *config.Config) **bool { return &c.Orchestration.AutoDelegate }, true)),
			},
			{
				ID: "row-swarm-lead-plans-only", Title: "主代理只规划", Lock: "swarm-lead-plans-only",
				Aliases: []string{"开启后主代理只负责规划和审查，所有改动都派给子代理（每个线程可单独切换）"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.LeadPlansOnly }, true)),
				Enabled: func(st *SettingsState) bool {
					return !(st.cfg.Orchestration.AlwaysBees && !st.cfg.Orchestration.LeadPlansOnly)
				},
			},
			{
				ID: "row-swarm-send-to-bees", Title: "消息直接交给子代理", Lock: "swarm-send-to-bees",
				Aliases: []string{"开启后发送的消息直接交给子代理处理"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.AlwaysBees }, false)),
				Enabled: func(st *SettingsState) bool {
					return !(st.cfg.Orchestration.LeadPlansOnly && !st.cfg.Orchestration.AlwaysBees)
				},
			},
			swarmForwardQuestionSetting(),
		}},
		{ID: "swarm.models", Title: "子代理默认模型", Rows: []setting{
			swarmDefaultModelSetting(),
		}},
		{ID: "swarm.done", Title: "子代理完成时", Rows: []setting{
			{
				ID: "block-swarm-review",
				Aliases: []string{
					"AI 审查依次走确定性检查、规则放行、单次模型审查，必要时才起审查会话；人工审查等你批准；两种都在有结论后才汇报给主代理",
					"留空自动选择最便宜的可用审查模型",
					"合并前在子代理工作副本里运行，失败直接驳回；留空用全局默认，全局也为空时 Go 项目自动跑 go build 和 go vet",
					"子代理完成时", "编排", "子代理",
				},
				Control: customFullWidth(renderSwarmReviewBody),
			},
			{
				ID: "row-swarm-auto-merge", Title: "自动合并", Lock: "swarm-auto-merge",
				Aliases: []string{"默认开启；全部完成后合并到当前分支并清理副本"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.AutoMerge }, true)),
			},
			{
				ID: "row-swarm-auto-push", Title: "自动推送", Lock: "swarm-auto-push",
				Aliases: []string{"默认关闭；开启后自动推送分支并创建 PR"},
				Control: toggle(val(func(c *config.Config) *bool { return &c.Orchestration.AutoPush }, false)),
			},
		}},
		{ID: "swarm.steward", Title: "总管", Rows: []setting{{ID: "row-steward-open", Title: "总管", Aliases: []string{"默默", "昵称", "跟进", "总调度", "steward", "待处理", "分诊"}, Control: link(func(st *SettingsState) string {
			if st.cfg.Orchestration.Steward.Enabled {
				return "开启"
			}
			return "关闭"
		}, "steward")}}},
		advancedOrchestration,
	},
}

func swarmForwardQuestionSetting() setting {
	return setting{
		ID: "row-swarm-auto-forward-question", Title: "无人回答时转给主代理", Lock: "swarm-auto-forward-question",
		Aliases: []string{"超时后由主代理先答，拿不准再交给你"},
		Control: swarmPickerControl{
			key: "picker-swarm-auto-forward-question",
			options: []visualPickerOption{
				{value: "0", label: "关闭", icon: IconPower()},
				{value: "60", label: "1 分钟", icon: IconTimer()},
				{value: "180", label: "3 分钟", icon: IconTimer()},
				{value: "300", label: "5 分钟", icon: IconTimer()},
				{value: "600", label: "10 分钟", icon: IconTimer()},
			},
			value: func(st *SettingsState) string {
				return fmt.Sprintf("%d", st.cfg.Orchestration.AutoForwardQuestionSeconds)
			},
			set: func(st *SettingsState, value string) {
				seconds := 0
				fmt.Sscanf(value, "%d", &seconds)
				if seconds == st.cfg.Orchestration.AutoForwardQuestionSeconds {
					return
				}
				commitSettingsControl(st, func(cfg *config.Config) { cfg.Orchestration.AutoForwardQuestionSeconds = seconds })
			},
		},
	}
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageSwarm) }

// renderPageSwarm 渲染 Swarm 设置页：多代理编排总开关、模型配对、并发与工作区、消息流向、默认模型及完成策略。
func renderPageSwarm(c *ui.Context, st *SettingsState) { renderSettingsTreePage(c, st, pageSwarm) }

func renderSwarmPairsBody(c *ui.Context, st *SettingsState) {
	providers := swarmSettingsProviders(st)
	uiSt := getSwarmUIState(st)

	// 顶部说明文字与操作按钮
	ui.Row(c.Key("row-swarm-pairs-header")).
		Padding(settingsRowPadY, settingsRowPadX).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Children(func() {
			ui.Row(c.Key("row-swarm-pairs-header-actions")).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					btnAdd := ui.ButtonBase(c.Key("btn-swarm-pair-add")).
						Label("添加配对").
						Padding(settingsBadgePadY, settingsCompactPadX).
						Radius(RadiusSM).
						Background(ThemeHover(c))
					// 只在编辑器尚未打开时初始化草稿：弹层打开期间再次点触发按钮（含落在
					// 嵌套模型选择器背后、穿透到按钮的点击）不能丢弃已选模型。
					if btnAdd.Clicked() && uiSt.openPopoverKey != "add" {
						uiSt.openPopoverKey = "add"
						uiSt.isNew = true
						leadProv := config.ProviderClaude
						if len(providers) > 0 {
							leadProv = providers[0].ID
						}
						uiSt.draft = config.SwarmPair{
							LeadProvider: leadProv,
						}
						st.invalidate()
					}
					btnAdd.Children(func() {
						ui.Text(c, "添加配对").FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
					})

					popKey := "add"
					wasOpen := (uiSt.openPopoverKey == popKey)
					isAddOpen := wasOpen
					appPopover(c.Key("popover-swarm-pair-"+popKey), btnAdd, &isAddOpen, nil, func() {
						renderSwarmPairEditorPopover(c, st, uiSt, popKey, providers)
					})
					// 草稿弹层只能通过取消/保存关闭；外部点击（含嵌套模型选择器）不能丢弃草稿。
					if wasOpen && uiSt.openPopoverKey == popKey {
						isAddOpen = true
					}

					renderSwarmPresetsPanel(c, st, providers)
				})
		})

	renderSettingsDivider(c, "div-swarm-pairs-header-sep")

	// 逐行列出配对
	if len(st.cfg.Orchestration.Pairs) == 0 {
		ui.Row(c.Key("swarm-pairs-empty")).
			Padding(settingsRowPadY, settingsRowPadX).
			Children(func() {
				ui.Text(c, "还没有配对，点击添加配对或从预设组合添加").
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))
			})
	} else {
		for i, p := range st.cfg.Orchestration.Pairs {
			pair := p
			if i > 0 {
				renderSettingsDivider(c, fmt.Sprintf("div-swarm-pair-%d", i))
			}
			renderSwarmPairRow(c, st, uiSt, pair, providers)
		}
	}

}

func renderSwarmTiersBody(c *ui.Context, st *SettingsState) {
	providers := swarmSettingsProviders(st)
	uiSt := getSwarmTiersUIState(st)

	// 顶部说明行与操作
	ui.Row(c.Key("row-swarm-tiers-header")).
		Padding(settingsRowPadY, settingsRowPadX).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Children(func() {
			ui.Row(c.Key("row-swarm-tiers-header-actions")).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "默认档").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))

					var tierOptions []string
					tierIDToName := make(map[string]string)
					tierNameToID := make(map[string]string)
					for _, t := range st.cfg.Orchestration.Tiers {
						name := t.Name
						if name == "" {
							name = t.ID
						}
						tierOptions = append(tierOptions, name)
						tierIDToName[t.ID] = name
						tierNameToID[name] = t.ID
					}
					curDefaultName := tierIDToName[st.cfg.Orchestration.DefaultTier]
					if curDefaultName == "" && len(tierOptions) > 0 {
						curDefaultName = tierOptions[0]
					}
					selDefault := ui.Select(c.Key("sel-swarm-default-tier"), &curDefaultName, tierOptions).
						Label("默认档").Width(settingsFieldNarrowWidth)
					if selDefault.Changed() {
						newID := tierNameToID[curDefaultName]
						if newID != "" && newID != st.cfg.Orchestration.DefaultTier {
							st.cfg.Orchestration.DefaultTier = newID
							st.save()
						}
					}

					btnAdd := ui.ButtonBase(c.Key("btn-swarm-tier-add")).
						Label("添加档位").
						Padding(settingsBadgePadY, settingsCompactPadX).
						Radius(RadiusSM).
						Background(ThemeHover(c))
					if btnAdd.Clicked() && uiSt.openPopoverKey != "add" {
						uiSt.openPopoverKey = "add"
						uiSt.isNew = true
						uiSt.draft = config.SwarmTier{}
						uiSt.writePathsText = ""
						uiSt.saveError = ""
						st.invalidate()
					}
					btnAdd.Children(func() {
						ui.Text(c, "添加档位").FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
					})

					popKey := "add"
					wasOpen := (uiSt.openPopoverKey == popKey)
					isAddOpen := wasOpen
					appPopover(c.Key("popover-swarm-tier-"+popKey), btnAdd, &isAddOpen, nil, func() {
						renderSwarmTierEditorPopover(c, st, uiSt, popKey, providers)
					})
					// 草稿弹层只能通过取消/保存关闭；外部点击（含嵌套模型选择器）不能丢弃草稿。
					if wasOpen && uiSt.openPopoverKey == popKey {
						isAddOpen = true
					}
				})
		})

	renderSettingsDivider(c, "div-swarm-tiers-header-sep")

	// 档位行列表
	if len(st.cfg.Orchestration.Tiers) == 0 {
		ui.Row(c.Key("swarm-tiers-empty")).
			Padding(settingsRowPadY, settingsRowPadX).
			Children(func() {
				ui.Text(c, "暂无档位配置，点击添加档位创建").
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))
			})
	} else {
		for i, t := range st.cfg.Orchestration.Tiers {
			tier := t
			idx := i
			if idx > 0 {
				renderSettingsDivider(c, fmt.Sprintf("div-swarm-tier-%d", idx))
			}
			renderSwarmTierRow(c, st, uiSt, tier, providers)
		}
	}

	// 超过建议档位数上限时显示警示
	if len(st.cfg.Orchestration.Tiers) > config.MaxSuggestedSwarmTiers {
		renderSettingsDivider(c, "div-swarm-tiers-warning-sep")
		ui.Row(c.Key("row-swarm-tiers-warning")).
			Padding(settingsRowPadY, settingsRowPadX).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconOctagonAlert()).
					Size(IconSM, IconSM).
					TextColor(ThemeWarning(c))
				ui.Text(c, "档位超过 5 个时，主代理更容易选错档位").
					FontSize(TypeFootnote).
					TextColor(ThemeWarning(c))
			})
	}
}

// swarmPickerControl 把图标选项接入设置行：锁定与条件禁用由页面树渲染器统一传入。
type swarmPickerControl struct {
	key     string
	options []visualPickerOption
	value   func(st *SettingsState) string
	set     func(st *SettingsState, value string)
}

func (p swarmPickerControl) render(c *ui.Context, st *SettingsState, s setting) {
	p.renderDisabled(c, st, s, false)
}

func (p swarmPickerControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	current := p.value(st)
	key := p.key
	if key == "" {
		key = "picker-" + s.ID
	}
	renderVisualPicker(c, key, &current, p.options, disabled, func(value string) {
		if disabled || p.set == nil {
			return
		}
		p.set(st, value)
	})
}

// swarmModelControl 把模型胶囊接入设置行：禁用时不接受选择，也不显示清除按钮。
type swarmModelControl struct {
	key        string
	value      func(st *SettingsState) ModelSelection
	set        func(st *SettingsState, sel ModelSelection)
	clear      func(st *SettingsState)
	emptyLabel string
	trigger    string
	clearLabel string
}

func (m swarmModelControl) render(c *ui.Context, st *SettingsState, s setting) {
	m.renderDisabled(c, st, s, false)
}

func (m swarmModelControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	sel := m.value(st)
	key := m.key
	if key == "" {
		key = s.ID
	}
	ui.Row(c.Key(s.ID + "-model")).AlignItems(ui.Center).Gap(Space1).Children(func() {
		renderSwarmModelCapsule(c, st, key, sel, m.emptyLabel, m.trigger, func(next ModelSelection) {
			if disabled || m.set == nil {
				return
			}
			m.set(st, next)
		})
		if m.clear == nil || m.clearLabel == "" || disabled || swarmEmptySelection(sel) {
			return
		}
		btnClear := ui.ButtonBase(c.Key(s.ID+"-clear")).
			Label(m.clearLabel).
			Padding(settingsBadgePadY, Space3).
			Radius(RadiusSM).
			Background(ui.Transparent)
		if btnClear.Hovered() {
			btnClear.Background(ThemeHover(c))
		}
		if btnClear.Clicked() {
			m.clear(st)
		}
		btnClear.Children(func() {
			ui.Text(c, m.clearLabel).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
		})
	})
}

// swarmReviewCheckControl 渲染本项目检查命令：输入只改内存，保存按钮单独落盘。
type swarmReviewCheckControl struct{}

func (swarmReviewCheckControl) render(c *ui.Context, st *SettingsState, s setting) {
	swarmReviewCheckControl{}.renderDisabled(c, st, s, false)
}

func (swarmReviewCheckControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	// 项目级检查命令：只覆盖本项目的 Command，TimeoutSeconds 与 RunTests 保留原值。
	checkDir := st.currentProjectDir()
	if checkDir == "" {
		st.reviewCheckLoaded = false
		st.reviewCheckProjectDir = ""
		st.reviewCheckCommand = ""
		st.reviewCheckErr = ""
		st.reviewCheckSaved = false
	} else if !st.reviewCheckLoaded || st.reviewCheckProjectDir != checkDir {
		checkCfg, checkErr := reviewcheck.LoadProjectConfig(st.dataDir, checkDir)
		st.reviewCheckLoaded = true
		st.reviewCheckProjectDir = checkDir
		st.reviewCheckSaved = false
		if checkErr != nil {
			st.reviewCheckErr = checkErr.Error()
			st.reviewCheckCommand = ""
		} else {
			st.reviewCheckErr = ""
			st.reviewCheckCommand = checkCfg.Command
		}
	}

	input := textFunc(func(st *SettingsState) (string, bool) {
		return st.reviewCheckCommand, false
	}, func(st *SettingsState, text string) {
		st.reviewCheckCommand = truncateReviewCheckCommand(text)
		st.reviewCheckSaved = false
	}, "留空使用全局默认").withDisabled(func(*SettingsState) bool { return checkDir == "" })

	ui.Column(c.Key(s.ID + "-wrap")).Gap(Space1).AlignItems(ui.End).Children(func() {
		ui.Row(c.Key(s.ID + "-controls")).Gap(Space3).AlignItems(ui.Center).Children(func() {
			input.renderDisabled(c, st, setting{ID: s.ID + "-text", Title: s.Title}, disabled)

			btnSave := ui.ButtonBase(c.Key("btn-save-swarm-review-check-command")).
				Label("保存检查命令").
				Padding(Space1, settingsCompactPadX).
				Radius(RadiusSM).
				Background(ThemeAccent(c))
			if checkDir == "" || disabled {
				btnSave.Disabled(true)
			}
			if btnSave.Clicked() && checkDir != "" && !disabled {
				savedCfg, loadErr := reviewcheck.LoadProjectConfig(st.dataDir, checkDir)
				if loadErr != nil {
					st.reviewCheckErr = loadErr.Error()
					st.reviewCheckSaved = false
				} else {
					savedCfg.Command = strings.TrimSpace(st.reviewCheckCommand)
					if saveErr := reviewcheck.SaveProjectConfig(st.dataDir, checkDir, savedCfg); saveErr != nil {
						st.reviewCheckErr = saveErr.Error()
						st.reviewCheckSaved = false
					} else {
						st.reviewCheckErr = ""
						st.reviewCheckSaved = true
						st.reviewCheckCommand = savedCfg.Command
					}
				}
			}
			btnSave.Children(func() {
				ui.Text(c, "保存检查命令").FontSize(TypeFootnote).TextColor(ThemeOnAccent(c))
			})
		})
		if st.reviewCheckErr != "" {
			ui.Text(c, st.reviewCheckErr).FontSize(TypeCaption).TextColor(ThemeDanger(c))
		} else if st.reviewCheckSaved {
			ui.Text(c, "已保存").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
		}
	})
}

// swarmDefaultModelSetting 是子代理默认模型行：派发指令未指定模型时使用。
func swarmDefaultModelSetting() setting {
	return setting{
		ID: "row-swarm-default-model", Title: "默认提供方与模型", Lock: "swarm-default-model",
		Aliases: []string{"派发指令没有指定 provider 或 model 时使用的默认值；跟随主代理表示继承主代理设置", "子代理默认模型", "编排", "子代理"},
		Control: swarmModelControl{
			key:        "swarm-default-model",
			emptyLabel: "跟随主代理",
			trigger:    "选择子代理默认模型",
			clearLabel: "跟随主代理",
			value: func(st *SettingsState) ModelSelection {
				return ModelSelection{
					Provider: st.cfg.Orchestration.DefaultProvider,
					Model:    st.cfg.Orchestration.DefaultModel,
					Effort:   st.cfg.Orchestration.DefaultEffort,
				}
			},
			set: func(st *SettingsState, next ModelSelection) {
				commitSettingsControl(st, func(cfg *config.Config) {
					cfg.Orchestration.DefaultProvider = next.Provider
					cfg.Orchestration.DefaultModel = next.Model
					cfg.Orchestration.DefaultEffort = next.Effort
				})
			},
			clear: func(st *SettingsState) {
				commitSettingsControl(st, func(cfg *config.Config) {
					cfg.Orchestration.DefaultProvider = ""
					cfg.Orchestration.DefaultModel = ""
					cfg.Orchestration.DefaultEffort = ""
				})
			},
		},
	}
}

// swarmReviewModeSetting 是合并前审查的档位选择行。
func swarmReviewModeSetting() setting {
	return setting{
		ID: "row-swarm-review-bees", Title: "合并前审查", Lock: "swarm-review-bees",
		Aliases: []string{"AI 审查依次走确定性检查、规则放行、单次模型审查，必要时才起审查会话；人工审查等你批准；两种都在有结论后才汇报给主代理"},
		Control: swarmPickerControl{
			key: "picker-swarm-review-mode",
			options: []visualPickerOption{
				{value: config.ReviewModeOff, label: "关闭", icon: IconX()},
				{value: config.ReviewModeAI, label: "AI 审查", icon: IconSparkles()},
				{value: config.ReviewModeHuman, label: "人工审查", icon: IconEye()},
			},
			value: func(st *SettingsState) string { return st.cfg.Orchestration.ReviewModeValue() },
			set: func(st *SettingsState, value string) {
				commitSettingsControl(st, func(cfg *config.Config) { cfg.Orchestration.ReviewMode = value })
			},
		},
	}
}

// swarmReviewModelSetting 是 AI 审查使用的模型行，仅在 AI 审查档出现。
func swarmReviewModelSetting() setting {
	return setting{
		ID: "row-swarm-review-model", Title: "审查模型", Lock: "swarm-review-bees",
		Aliases: []string{"留空自动选择最便宜的可用审查模型"},
		Control: swarmModelControl{
			key:        "swarm-review-model",
			emptyLabel: "自动选择",
			trigger:    "选择合并审查模型",
			clearLabel: "恢复默认",
			value: func(st *SettingsState) ModelSelection {
				return ModelSelection{
					Provider: st.cfg.Orchestration.ReviewProvider,
					Model:    st.cfg.Orchestration.ReviewModel,
					Effort:   st.cfg.Orchestration.ReviewEffort,
				}
			},
			set: func(st *SettingsState, next ModelSelection) {
				commitSettingsControl(st, func(cfg *config.Config) {
					cfg.Orchestration.ReviewProvider = next.Provider
					cfg.Orchestration.ReviewModel = next.Model
					cfg.Orchestration.ReviewEffort = next.Effort
				})
			},
			clear: func(st *SettingsState) {
				commitSettingsControl(st, func(cfg *config.Config) {
					cfg.Orchestration.ReviewProvider = ""
					cfg.Orchestration.ReviewModel = ""
					cfg.Orchestration.ReviewEffort = ""
				})
			},
		},
	}
}

// swarmReviewCheckSetting 是本项目检查命令行，仅在 AI 审查档出现。
func swarmReviewCheckSetting() setting {
	return setting{
		ID: "row-swarm-review-check-command", Title: "检查命令（本项目）", Lock: "swarm-review-bees",
		Aliases: []string{"合并前在子代理工作副本里运行，失败直接驳回；留空用全局默认，全局也为空时 Go 项目自动跑 go build 和 go vet"},
		Control: swarmReviewCheckControl{},
	}
}

// renderSwarmReviewBody 渲染审查区块：审查档固定显示，AI 档再追加模型与检查命令。
func renderSwarmReviewBody(c *ui.Context, st *SettingsState) {
	renderSettingsTreeSetting(c, st, swarmReviewModeSetting())
	if st.cfg.Orchestration.ReviewModeValue() != config.ReviewModeAI {
		return
	}
	renderSettingsTreeSetting(c, st, swarmReviewModelSetting())
	renderSettingsTreeSetting(c, st, swarmReviewCheckSetting())
}

// swarmPlanModelSetting 是总管写方案时使用的模型行。
func swarmPlanModelSetting() setting {
	return setting{
		ID: "row-steward-model-plan", Title: "方案模型",
		Aliases: []string{"总管被你要求出方案时，用这个模型起一个子代理写方案文档；空表示沿用子代理默认模型", "模型", "编排", "子代理"},
		Control: swarmModelControl{
			key:        "steward-model-plan",
			emptyLabel: "沿用子代理默认模型",
			trigger:    "选择方案模型",
			clearLabel: "沿用子代理默认模型",
			value: func(st *SettingsState) ModelSelection {
				return ModelSelection{
					Provider: st.cfg.Orchestration.Steward.PlanProvider,
					Model:    st.cfg.Orchestration.Steward.PlanModel,
					Effort:   st.cfg.Orchestration.Steward.PlanEffort,
				}
			},
			set: func(st *SettingsState, next ModelSelection) {
				commitSettingsControl(st, func(cfg *config.Config) {
					cfg.Orchestration.Steward.PlanProvider = next.Provider
					cfg.Orchestration.Steward.PlanModel = next.Model
					cfg.Orchestration.Steward.PlanEffort = next.Effort
				})
			},
			clear: func(st *SettingsState) {
				commitSettingsControl(st, func(cfg *config.Config) {
					cfg.Orchestration.Steward.PlanProvider = ""
					cfg.Orchestration.Steward.PlanModel = ""
					cfg.Orchestration.Steward.PlanEffort = ""
				})
			},
		},
	}
}

// renderSwarmPlanBody 供总管页的高级模型区复用方案模型行。
func renderSwarmPlanBody(c *ui.Context, st *SettingsState) {
	renderSettingsTreeSetting(c, st, swarmPlanModelSetting())
}
