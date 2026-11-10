package mcodeui

import (
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// swarmTiersUIState 维护 Bee 档位编辑弹层与草稿状态。
type swarmTiersUIState struct {
	editor         *swarmTierEditorSession
	openPopoverKey string // "" 表示关闭，"add" 表示新增，"edit-<id>" 表示编辑指定档位
	isNew          bool
	draft          config.SwarmTier
	// writePathsText 是写入白名单的多行文本缓冲，保存时解析回 draft.WritePaths。
	writePathsText string
	saveError      string
}

// formatWritePaths 把写入白名单按行拼接，供多行输入框展示。
func formatWritePaths(paths []string) string {
	return strings.Join(paths, "\n")
}

// parseWritePaths 把多行文本拆成写入白名单：去掉首尾空白、丢弃空行与重复项并保持顺序；
// 全部为空时返回 nil（表示不限制写入路径）。
func parseWritePaths(text string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		p := strings.TrimSpace(line)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// getSwarmTiersUIState 返回档位编辑器的弹层状态：第一次访问时在 st.mu 锁内创建，
// 页面关闭后由 SettingsState.Close 清空。
func getSwarmTiersUIState(st *SettingsState) *swarmTiersUIState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.swarmTiersPage == nil {
		st.swarmTiersPage = &swarmTiersUIState{}
	}
	return st.swarmTiersPage
}

// saveTier 保存档位。先在副本上调用 config.Validate，若校验失败返回错误并不落盘；
// 合法则写入 st.cfg.Orchestration.Tiers 并持久化保存。
func saveTier(st *SettingsState, draft config.SwarmTier, isNew bool, errStr *string) bool {
	if st == nil {
		return false
	}
	// 读改写整段用同一组锁包住：读 st.cfg 和写回要看到同一份快照，顺序固定 mu → cfgMu。
	// save 在两把锁之外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	cfgCopy := st.cfg
	oldTiers := st.cfg.Orchestration.Tiers
	newTiers := make([]config.SwarmTier, len(st.cfg.Orchestration.Tiers))
	copy(newTiers, st.cfg.Orchestration.Tiers)
	if isNew {
		newTiers = append(newTiers, draft)
	} else {
		found := false
		for i, t := range newTiers {
			if t.ID == draft.ID {
				newTiers[i] = draft
				found = true
				break
			}
		}
		if !found {
			newTiers = append(newTiers, draft)
		}
	}
	cfgCopy.Orchestration.Tiers = newTiers
	if err := config.Validate(cfgCopy); err != nil {
		if errStr != nil {
			*errStr = err.Error()
		}
		st.cfgMu.Unlock()
		st.mu.Unlock()
		return false
	}
	st.cfg.Orchestration.Tiers = newTiers
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if !st.save() {
		st.mu.Lock()
		st.cfgMu.Lock()
		if reflect.DeepEqual(st.cfg.Orchestration.Tiers, newTiers) {
			st.cfg.Orchestration.Tiers = oldTiers
		}
		if errStr != nil {
			*errStr = st.errorMessage
		}
		st.cfgMu.Unlock()
		st.mu.Unlock()
		return false
	}
	return true
}

// defaultSwarmTier 按 ID 查找内置档位的默认配置。
func defaultSwarmTier(id string) (config.SwarmTier, bool) {
	for _, tier := range config.DefaultSwarmTiers() {
		if tier.ID == id {
			return tier, true
		}
	}
	return config.SwarmTier{}, false
}

func swarmTierDraftIsDefault(draft config.SwarmTier, writePathsText string) bool {
	defaultTier, ok := defaultSwarmTier(draft.ID)
	if !ok {
		return false
	}
	if draft.ReadOnly {
		draft.WritePaths = nil
	} else {
		draft.WritePaths = parseWritePaths(writePathsText)
	}
	if len(defaultTier.WritePaths) == 0 {
		defaultTier.WritePaths = nil
	}
	return reflect.DeepEqual(draft, defaultTier)
}

// deleteTier 按 ID 删除档位，并级联从所有模型配对的 BeeTiers 中移除该档位映射。
// 删除会让配置非法（如只剩只读档）时不改内存并返回 false，避免界面上已经删掉、落盘却失败（S-095）。
func deleteTier(st *SettingsState, id string) bool {
	if st == nil {
		return false
	}
	// 读改写整段用同一组锁包住：读 st.cfg 和写回要看到同一份快照，顺序固定 mu → cfgMu。
	// save 与后面的回滚都放在锁外单独加锁，持锁时不调 save。
	st.mu.Lock()
	st.cfgMu.Lock()
	idx := -1
	for i, t := range st.cfg.Orchestration.Tiers {
		if t.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		st.cfgMu.Unlock()
		st.mu.Unlock()
		return false
	}
	newTiers := make([]config.SwarmTier, 0, len(st.cfg.Orchestration.Tiers)-1)
	newTiers = append(newTiers, st.cfg.Orchestration.Tiers[:idx]...)
	newTiers = append(newTiers, st.cfg.Orchestration.Tiers[idx+1:]...)
	newPairs := make([]config.SwarmPair, len(st.cfg.Orchestration.Pairs))
	for i, p := range st.cfg.Orchestration.Pairs {
		p = cloneSwarmPair(p)
		delete(p.BeeTiers, id)
		if len(p.BeeTiers) == 0 {
			p.BeeTiers = nil
		}
		newPairs[i] = p
	}

	cfgCopy := st.cfg
	cfgCopy.Orchestration.Tiers = newTiers
	cfgCopy.Orchestration.Pairs = newPairs
	if err := config.Validate(cfgCopy); err != nil {
		st.cfgMu.Unlock()
		st.mu.Unlock()
		return false
	}

	oldTiers, oldPairs := st.cfg.Orchestration.Tiers, st.cfg.Orchestration.Pairs
	prevErr := st.errorMessage
	st.cfg.Orchestration.Tiers = newTiers
	st.cfg.Orchestration.Pairs = newPairs
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.save()
	// st.errorMessage 与 cfg 共用 cfgMu，回滚判断与回滚写入都要在它下面做。
	st.mu.Lock()
	st.cfgMu.Lock()
	saveErr := st.errorMessage
	if saveErr != "" && saveErr != prevErr {
		// 落盘失败时回滚，内存里不留「已经删掉」的档位列表。
		st.cfg.Orchestration.Tiers = oldTiers
		st.cfg.Orchestration.Pairs = oldPairs
		st.cfgMu.Unlock()
		st.mu.Unlock()
		return false
	}
	st.cfgMu.Unlock()
	st.mu.Unlock()
	disposeSwarmTierEditorIfEditing(st, id)
	return true
}

// swarmTierDeleteGuard 判断档位能否删除：默认档不可删，最后一个可写档也不可删。
// 只读位取运行态配置里的实际值，忽略草稿里刚勾上的只读，
// 否则先勾只读再点删除就能删掉唯一的可写档位（S-095）。
func swarmTierDeleteGuard(st *SettingsState, id string, draftReadOnly bool) (disabled bool, tooltip string) {
	actualReadOnly := draftReadOnly
	nonReadOnlyCount := 0
	for _, t := range st.cfg.Orchestration.Tiers {
		if t.ID == id {
			actualReadOnly = t.ReadOnly
		}
		if !t.ReadOnly {
			nonReadOnlyCount++
		}
	}
	switch {
	case id == st.cfg.Orchestration.DefaultTier:
		return true, "默认档位不可删除"
	case !actualReadOnly && nonReadOnlyCount <= 1:
		return true, "至少保留一个可写档位"
	}
	return false, "删除档位"
}

// renderSwarmTierEditorForm 渲染共享草稿。
func renderSwarmTierEditorForm(c *ui.Context, st *SettingsState, uiSt *swarmTiersUIState, popKey string) {
	card := ui.Column(c.Key("swarm-tier-editor-card-"+popKey)).
		FillWidth().
		Padding(Space4, Space5).
		Gap(Space4).
		Background(ThemeSurface(c)).
		Radius(RadiusMD)
	card.Children(func() {
		formTitle := "编辑子代理档位"
		if uiSt.isNew {
			formTitle = "添加子代理档位"
		}
		// 标题按顶边对齐：按文本查找时取到的是单个字形行高的矩形，胶囊定位与滚动
		// 都要求它从弹层顶边开始。
		ui.Row(c.Key("swarm-tier-editor-bee-" + popKey)).
			AlignItems(ui.Start).
			Children(func() {
				ui.Text(c, formTitle).
					FontSize(TypeProse).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
			})

		// 1. ID 字段（新建时可编辑，仅支持小写字母与数字）
		ui.Column(c.Key("field-tier-id-" + popKey)).
			Gap(Space1).
			Children(func() {
				ui.Text(c, "档位 ID").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
				TextField(c, TextFieldProps{
					Key: "swarm-tier-id-" + popKey + "-input", Value: &uiSt.draft.ID,
					Label: "编辑档位 ID", Placeholder: "英文小写标识，如 read / edit / deep",
					Disabled: !uiSt.isNew, FillWidth: true,
					OnChange: func(text string) { uiSt.draft.ID = strings.ToLower(strings.TrimSpace(text)) },
				})
			})

		// 2. 名称
		ui.Column(c.Key("field-tier-name-" + popKey)).
			Gap(Space1).
			Children(func() {
				ui.Text(c, "档位名称").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
				TextField(c, TextFieldProps{
					Key: "swarm-tier-name-" + popKey + "-input", Value: &uiSt.draft.Name,
					Label: "编辑档位名称", Placeholder: "如：只读 / 执行 / 深度", FillWidth: true,
				})
			})

		// 3. 用途说明（多行，此文本用于指引 Lead 决策）
		ui.Column(c.Key("field-tier-desc-" + popKey)).
			Gap(Space1).
			Children(func() {
				ui.Text(c, "用途说明").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
				TextField(c, TextFieldProps{
					Key:         "area-swarm-tier-desc-" + popKey,
					Value:       &uiSt.draft.Description,
					Label:       "用途说明",
					Placeholder: "向主代理说明该档位的适用场景（展示给主代理）",
					Multiline:   true,
					MinLines:    2,
					MaxLines:    5,
					FillWidth:   true,
				})
			})

		// 4. 只读 switch 及辅助说明
		ui.Row(c.Key("row-swarm-tier-readonly-" + popKey)).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Children(func() {
				ui.Column(c.Key("col-swarm-tier-readonly-text-" + popKey)).
					Gap(Space0).
					MaxWidth(260).Grow(1).
					Children(func() {
						ui.Text(c, "只读模式").
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemePrimaryText(c))
						ui.Text(c, "只读档子代理在独立工作树里运行，改动一律丢弃").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
					})
				toggleFunc(func(*SettingsState) (bool, bool) {
					return uiSt.draft.ReadOnly, false
				}, func(_ *SettingsState, on bool) {
					uiSt.draft.ReadOnly = on
				}).render(c, st, setting{ID: "swarm-tier-readonly-" + popKey, Title: "只读模式"})
			})

		// 4.1 写入白名单（只读档没有写权限，不渲染）
		if !uiSt.draft.ReadOnly {
			ui.Column(c.Key("sec-tier-writepaths-" + popKey)).
				Gap(Space1).
				Children(func() {
					ui.Text(c, "写入白名单").
						FontSize(TypeFootnote).
						FontWeight(500).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, "每行一个路径通配，留空则不限制").
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))
					TextField(c, TextFieldProps{
						Key:         "area-swarm-tier-writepaths-" + popKey,
						Value:       &uiSt.writePathsText,
						Label:       "写入白名单",
						Placeholder: "每行一个路径通配，留空表示不限制",
						Multiline:   true,
						MinLines:    2,
						MaxLines:    5,
						FillWidth:   true,
					})
				})
		}

		// 5. 模型配置
		ui.Column(c.Key("sec-tier-model-" + popKey)).
			Gap(Space2).
			Children(func() {
				ui.Text(c, "模型配置").
					FontSize(TypeFootnote).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))

				tierSel := ModelSelection{
					Provider: uiSt.draft.Provider,
					Model:    uiSt.draft.Model,
					Effort:   uiSt.draft.Effort,
					Fast:     uiSt.draft.FastMode,
				}
				ui.Row(c.Key("row-tier-capsule-" + popKey)).
					Label("tier-model-capsule").
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						renderSwarmModelCapsuleEx(c, st, "swarm-tier-model-capsule", "tier-model-"+popKey, tierSel, "跟随会话子代理模型", "选择档位模型", func(next ModelSelection) {
							uiSt.draft.Provider = next.Provider
							uiSt.draft.Model = next.Model
							uiSt.draft.Effort = next.Effort
							uiSt.draft.FastMode = next.Fast
							st.invalidate()
						})

						if !swarmEmptySelection(tierSel) {
							btnClear := ui.ButtonBase(c.Key("btn-clear-tier-model-"+popKey)).
								Label("跟随会话").
								Padding(Space1, Space4).
								Radius(RadiusSM).
								Background(ui.Transparent)
							if btnClear.Hovered() {
								btnClear.Background(ThemeHover(c))
							}
							if btnClear.Clicked() {
								uiSt.draft.Provider = ""
								uiSt.draft.Model = ""
								uiSt.draft.Effort = ""
								uiSt.draft.FastMode = false
								st.invalidate()
							}
							btnClear.Children(func() {
								ui.Text(c, "跟随会话").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							})
						}
					})
			})

		// 7. 档位要求多行输入（可选）
		ui.Column(c.Key("field-system-prompt-" + popKey)).
			Gap(Space1).
			Children(func() {
				ui.Text(c, "档位要求 (可选)").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
				TextField(c, TextFieldProps{
					Key:         "area-swarm-tier-prompt-" + popKey,
					Value:       &uiSt.draft.SystemPrompt,
					Label:       "档位要求",
					Placeholder: "写给该档位子代理的额外要求",
					Multiline:   true,
					MinLines:    2,
					MaxLines:    5,
					FillWidth:   true,
				})
			})

		// 保存校验错误提示
		if uiSt.saveError != "" {
			ui.Row(c.Key("row-tier-save-error-" + popKey)).
				Gap(Space1).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, IconOctagonAlert()).
						Size(Space4, Space4).
						TextColor(ThemeDanger(c))
					ui.Text(c, uiSt.saveError).
						FontSize(TypeCaption).
						TextColor(ThemeDanger(c)).
						Grow(1)
				})
		}

		// 8. 底部操作区：删除、重置、取消、保存
		ui.Row(c.Key("actions-tier-" + popKey)).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Children(func() {
				if !uiSt.isNew {
					ui.Row(c.Key("actions-left-" + popKey)).Gap(Space3).AlignItems(ui.Center).Children(func() {
						deleteDisabled, deleteTooltip := swarmTierDeleteGuard(st, uiSt.draft.ID, uiSt.draft.ReadOnly)

						btnDelete := ui.ButtonBase(c.Key("btn-tier-delete-"+popKey)).
							Label("删除").
							Tooltip(deleteTooltip).
							Disabled(deleteDisabled).
							Padding(Space1, Space4).
							Radius(RadiusSM).
							Background(ui.Transparent)
						if !deleteDisabled && btnDelete.Hovered() {
							btnDelete.Background(ThemeDanger(c).Alpha(0.12))
						}
						if (!deleteDisabled) && btnDelete.Clicked() {
							if !deleteTier(st, uiSt.draft.ID) {
								uiSt.saveError = "删除档位失败：至少保留一个可写档位"
							}
							st.invalidate()
						}
						btnDelete.Children(func() {
							textColor := ThemeDanger(c)
							if deleteDisabled {
								textColor = ThemeSecondaryText(c).Alpha(0.5)
							}
							ui.Text(c, "删除").FontSize(TypeFootnote).TextColor(textColor)
						})

						if defaultTier, ok := defaultSwarmTier(uiSt.draft.ID); ok {
							resetDisabled := swarmTierDraftIsDefault(uiSt.draft, uiSt.writePathsText)
							btnReset := ui.ButtonBase(c.Key("btn-tier-reset-"+popKey)).
								Label("重置").
								Tooltip("恢复默认配置").
								Disabled(resetDisabled).
								Padding(Space1, Space4).
								Radius(RadiusSM).
								Background(ui.Transparent)
							if !resetDisabled && btnReset.Hovered() {
								btnReset.Background(ThemeHover(c))
							}
							if !resetDisabled && btnReset.Clicked() {
								uiSt.draft = defaultTier
								uiSt.writePathsText = formatWritePaths(defaultTier.WritePaths)
								uiSt.saveError = ""
								st.invalidate()
							}
							btnReset.Children(func() {
								textColor := ThemeSecondaryText(c)
								if resetDisabled {
									textColor = textColor.Alpha(0.5)
								}
								ui.Text(c, "重置").FontSize(TypeFootnote).TextColor(textColor)
							})
						}
					})
				} else {
					ui.Box(c.Key("spacer-delete-" + popKey))
				}

				ui.Row(c.Key("actions-right-" + popKey)).
					Gap(Space3).
					AlignItems(ui.Center).
					Children(func() {
						btnCancel := ui.ButtonBase(c.Key("btn-tier-cancel-"+popKey)).
							Label("取消").
							Padding(Space1, Space4).
							Radius(RadiusSM).
							Background(ThemeHover(c))
						if btnCancel.Clicked() {
							disposeSwarmTierEditor(st)
							st.invalidate()
						}
						btnCancel.Children(func() {
							ui.Text(c, "取消").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
						})

						btnSave := ui.ButtonBase(c.Key("btn-tier-save-"+popKey)).
							Label("保存").
							Padding(Space1, Space4).
							Radius(RadiusSM).
							Background(ThemeAccent(c))
						if btnSave.Clicked() {
							uiSt.draft.ID = strings.ToLower(strings.TrimSpace(uiSt.draft.ID))
							if uiSt.draft.ReadOnly {
								uiSt.draft.WritePaths = nil
							} else {
								uiSt.draft.WritePaths = parseWritePaths(uiSt.writePathsText)
							}
							if saveTier(st, uiSt.draft, uiSt.isNew, &uiSt.saveError) {
								disposeSwarmTierEditor(st)
							}
							st.invalidate()
						}
						btnSave.Children(func() {
							ui.Text(c, "保存").FontSize(TypeFootnote).TextColor(ThemeOnAccent(c))
						})
					})
			})
	})
}

// renderSwarmTierRow 渲染单个 Bee 档位条目。
func renderSwarmTierRow(c *ui.Context, st *SettingsState, uiSt *swarmTiersUIState, tier config.SwarmTier, providers []ProviderInfo) {
	popKey := "edit-" + tier.ID
	rowBtn := ui.ButtonBase(c.Key("row-swarm-tier-"+tier.ID)).
		Label(tier.Name).
		Padding(settingsRowPadY, settingsRowPadX).
		Radius(RadiusSM).
		Grow(1)
	if rowBtn.Hovered() {
		rowBtn.Background(ThemeHover(c))
	} else {
		rowBtn.Background(ui.Transparent)
	}
	if rowBtn.Clicked() {
		openSwarmTierEditor(c, st, uiSt, rowBtn, popKey, tier)
	}
	updateSwarmTierEditorAnchor(c, st, rowBtn, popKey)

	rowBtn.Children(func() {
		ui.Row(c.Key("row-swarm-tier-content-" + tier.ID)).
			Gap(Space4).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Grow(1).
			Children(func() {
				// 左侧列：Chip + ID + 只读标签，下方为用途说明
				ui.Column(c.Key("col-swarm-tier-left-" + tier.ID)).
					Gap(Space1).
					Grow(1).
					Children(func() {
						ui.Row(c.Key("row-swarm-tier-top-" + tier.ID)).
							Gap(Space3).
							AlignItems(ui.Center).
							Children(func() {
								renderSwarmTierChip(c, "chip-"+tier.ID, tier.Name, tier.ID, tier.ReadOnly)
								ui.Text(c, tier.ID).
									FontSize(TypeCaption).
									TextColor(ThemeSecondaryText(c))
								if config.BuiltinTierCustomized(tier) {
									ui.Box(c.Key("tier-customized-"+tier.ID)).
										Size(Space2, Space2).Radius(RadiusPill).Background(ThemeAccent(c)).Label("已修改").Tooltip("已修改").Shrink(0)
								}
								if tier.ReadOnly {
									ui.Text(c, "只读").
										FontSize(TypeCaption).
										TextColor(ThemeSecondaryText(c)).
										Padding(Hairline, Space1).
										Radius(RadiusXS).
										Background(ThemeOverlay(c, 0.08))
								}
							})
						if tier.Description != "" {
							ui.Text(c, tier.Description).
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						}
					})

				// 右侧列：模型胶囊
				if tier.Provider == "" && tier.Model == "" {
					ui.Row(c.Key("tier-capsule-"+tier.ID)).
						Padding(Space1, Space3).
						Radius(RadiusMD).
						Border(Hairline, ThemeBorder(c).Alpha(0.4)).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, "跟随会话子代理模型").
								FontSize(TypeFootnote).
								Italic().
								TextColor(ThemeSecondaryText(c))
						})
				} else {
					ui.Row(c.Key("tier-capsule-"+tier.ID)).
						Padding(Space1, Space3).
						Radius(RadiusMD).
						Border(Hairline, ThemeBorder(c)).
						AlignItems(ui.Center).
						Gap(Space2).
						Children(func() {
							renderProviderIcon(c, tier.Provider, 14)
							txt := swarmModelCapsuleText(providers, tier.Provider, tier.Model, tier.Effort, tier.FastMode)
							ui.Text(c, txt).
								FontSize(TypeFootnote).
								TextColor(ThemePrimaryText(c))
						})
				}
			})
	})
}
