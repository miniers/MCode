package mcodeui

// Swarm 预设组合（Swarm Preset）：预置的主代理 / 子代理模型搭配，用户一键添加为 Swarm 组合。
//
// 外部 Provider（DeepSeek、Z.ai、Cursor、Meta 等）已原生接入；
// 预设组合仍全部列出，当对应 Provider 未启用或未配置时判定为 Waiting。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// swarmPreset 表示单个 Swarm 协同配对预设组合。
type swarmPreset struct {
	ID      string
	Title   string
	Summary string
	Pair    config.SwarmPair // 模板配置，ID 必须为空
}

// swarmPresetState 表示预设组合在当前系统环境下的就绪状态。
type swarmPresetState struct {
	Preset  swarmPreset
	Ready   bool
	Missing []string
	Added   bool
}

func swarmStringPtr(s string) *string {
	return &s
}

// swarmPresets 包含所有推荐的 Swarm 协同预设组合列表。
var swarmPresets = []swarmPreset{
	{
		ID:      "claude-fable-opus",
		Title:   "Fable 5.2 leads Opus 5.5",
		Summary: "Fable 拆解需求、把关审查，Opus 按简报写代码。适合难度高、返工代价大的任务；两边的思考强度都能单独调。",
		Pair: config.SwarmPair{
			Name:           "Fable 5.2 leads Opus 5.5",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Fable",
			WorkerProvider: config.ProviderClaude,
			WorkerModel:    "Opus",
		},
	},
	{
		ID:      "claude-opus-gemini-hydrogen",
		Title:   "Opus 5.5 leads Gemini 4 Hydrogen",
		Summary: "Opus 写清楚每份简报，Gemini Hydrogen 多路并行落地。任务能拆成很多独立小块时最划算。",
		Pair: config.SwarmPair{
			Name:           "Opus 5.5 leads Gemini 4 Hydrogen",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Opus",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderAgy,
			WorkerModel:    "Hydrogen",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "claude-opus-haiku",
		Title:   "Opus 5.5 leads Haiku 5.5",
		Summary: "Opus 拆任务、做复核，Haiku 便宜又快，适合大量并行的小改动。",
		Pair: config.SwarmPair{
			Name:           "Opus 5.5 leads Haiku 5.5",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Opus",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderClaude,
			WorkerModel:    "Haiku",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "claude-opus-deepseek",
		Title:   "Opus 5.5 leads DeepSeek V4.1 Flash",
		Summary: "方案和审查交给 Opus，体力活交给 DeepSeek V4.1 Flash，在质量和花费之间取个平衡。",
		Pair: config.SwarmPair{
			Name:           "Opus 5.5 leads DeepSeek V4.1 Flash",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Opus",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: "deepseek",
			WorkerModel:    "deepseek-flash",
			WorkerEffort:   swarmStringPtr("high"),
		},
	},
	{
		ID:      "claude-opus-glm",
		Title:   "Opus 5.5 leads GLM-5.4",
		Summary: "Opus 定方向，Z.ai 的 GLM-5.4 动手实现，适合预算有限又不想降低规划质量的场景。",
		Pair: config.SwarmPair{
			Name:           "Opus 5.5 leads GLM-5.4",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Opus",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: "zai",
			WorkerModel:    "GLM",
			WorkerEffort:   swarmStringPtr("high"),
		},
	},
	{
		ID:      "claude-fable-composer",
		Title:   "Fable 5.2 leads Composer 2.5",
		Summary: "Fable 想清楚要改什么，Cursor Composer 快速把改动写进代码，追求出活速度时用。",
		Pair: config.SwarmPair{
			Name:           "Fable 5.2 leads Composer 2.5",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Fable",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: "cursor",
			WorkerModel:    "Composer",
		},
	},
	{
		ID:      "astra-spark",
		Title:   "Astra 6.2 leads Muse Spark 1.3 Contributor",
		Summary: "Astra（high 档）负责拆分和协调，Muse Spark Contributor（medium 档）承担具体实现。",
		Pair: config.SwarmPair{
			Name:           "Astra 6.2 leads Muse Spark 1.3 Contributor",
			LeadProvider:   config.ProviderCodex,
			LeadModel:      "Astra",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: "meta",
			WorkerModel:    "Contributor",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "astra-terra",
		Title:   "Astra 6.2 leads Terra 6.1",
		Summary: "只用 OpenAI 模型：Astra 规划和复核，Terra（medium 档）写日常代码。",
		Pair: config.SwarmPair{
			Name:           "Astra 6.2 leads Terra 6.1",
			LeadProvider:   config.ProviderCodex,
			LeadModel:      "Astra",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderCodex,
			WorkerModel:    "Terra",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "astra-sol",
		Title:   "Astra 6.2 leads Sol 6.2",
		Summary: "Astra 规划和复核，Sol（medium 档）啃逻辑较重的模块。",
		Pair: config.SwarmPair{
			Name:           "Astra 6.2 leads Sol 6.2",
			LeadProvider:   config.ProviderCodex,
			LeadModel:      "Astra",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderCodex,
			WorkerModel:    "Sol",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "sol-luna",
		Title:   "Sol 6.2 leads Luna 6",
		Summary: "Sol 拿技术方案，轻快的 Luna（medium 档）批量处理琐碎改动。",
		Pair: config.SwarmPair{
			Name:           "Sol 6.2 leads Luna 6",
			LeadProvider:   config.ProviderCodex,
			LeadModel:      "Sol",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderCodex,
			WorkerModel:    "Luna",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "astra-astra",
		Title:   "Astra 6.2 high, Astra 6.2 medium",
		Summary: "同一个 Astra 分两档：high 档想方案、做审查，medium 档写代码，风格前后一致。",
		Pair: config.SwarmPair{
			Name:           "Astra 6.2 high, Astra 6.2 medium",
			LeadProvider:   config.ProviderCodex,
			LeadModel:      "Astra",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderCodex,
			WorkerModel:    "Astra",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "claude-opus-opus",
		Title:   "Opus 5.5 max, Opus 5.5 medium",
		Summary: "Opus 开到 max 档只用在规划和审查上，执行交给 medium 档的 Opus，省时也省额度。",
		Pair: config.SwarmPair{
			Name:           "Opus 5.5 max, Opus 5.5 medium",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Opus",
			LeadEffort:     swarmStringPtr("max"),
			WorkerProvider: config.ProviderClaude,
			WorkerModel:    "Opus",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "claude-fable-fable",
		Title:   "Fable 5.2 high, Fable 5.2 medium",
		Summary: "全程用 Fable：主代理 high 档、子代理 medium 档，留给最棘手的问题。",
		Pair: config.SwarmPair{
			Name:           "Fable 5.2 high, Fable 5.2 medium",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Fable",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderClaude,
			WorkerModel:    "Fable",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
	{
		ID:      "gemini-carbon-hydrogen",
		Title:   "Gemini 4 Carbon leads Gemini 4 Hydrogen",
		Summary: "只用 Gemini：Carbon 深度推理和把关，Hydrogen 快速落地。",
		Pair: config.SwarmPair{
			Name:           "Gemini 4 Carbon leads Gemini 4 Hydrogen",
			LeadProvider:   config.ProviderAgy,
			LeadModel:      "Carbon",
			LeadEffort:     swarmStringPtr("high"),
			WorkerProvider: config.ProviderAgy,
			WorkerModel:    "Hydrogen",
			WorkerEffort:   swarmStringPtr("medium"),
		},
	},
}

// missingProviderDesc 根据 Provider ID 返回缺项说明文案。
func missingProviderDesc(providerID string) string {
	switch providerID {
	case config.ProviderCodex:
		return "需要安装 Codex"
	case config.ProviderClaude:
		return "需要配置 Claude"
	case config.ProviderAgy:
		return "需要配置 agy"
	case "deepseek":
		return "需要配置 DeepSeek"
	case "zai":
		return "需要配置 Z.ai"
	case "cursor":
		return "需要配置 Cursor"
	case "meta":
		return "需要配置 Meta"
	default:
		return "MCode 暂不支持 " + providerID
	}
}

// findProviderInfo 在列表中按 ID 查找提供方信息（大小写不敏感匹配）。
func findProviderInfo(providers []ProviderInfo, id string) (ProviderInfo, bool) {
	for _, p := range providers {
		if p.ID == id || strings.EqualFold(p.ID, id) {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

var swarmModelVersion = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

// 预设组合按模型家族查找，优先名称片段，再取最新版本；同版本保留目录顺序。
func resolveSwarmPresetModel(models []ModelInfo, target string) (ModelInfo, bool) {
	var best ModelInfo
	bestVersion := -1.0
	found := false
	lowerTarget := strings.ToLower(target)
	for _, model := range models {
		if !strings.Contains(strings.ToLower(model.ID), lowerTarget) &&
			!strings.Contains(strings.ToLower(model.Name), lowerTarget) {
			continue
		}
		version := -1.0
		if raw := swarmModelVersion.FindString(model.Name); raw != "" {
			version, _ = strconv.ParseFloat(raw, 64)
		}
		if !found || version > bestVersion {
			best, bestVersion, found = model, version, true
		}
	}
	return best, found
}

func swarmPresetEffort(wanted *string, model ModelInfo) *string {
	if wanted != nil {
		for _, effort := range model.Efforts {
			if effort == *wanted {
				return swarmStringPtr(effort)
			}
		}
	}
	return nil
}

// appendMissingUnique 追加不重复的缺失说明条目。
func appendMissingUnique(list []string, item string) []string {
	for _, s := range list {
		if s == item {
			return list
		}
	}
	return append(list, item)
}

// isSwarmPairAdded 检查现有配对中是否已有相同的主导与工作方提供方与模型。
func isSwarmPairAdded(target config.SwarmPair, existing []config.SwarmPair) bool {
	for _, p := range existing {
		if p.LeadProvider == target.LeadProvider &&
			p.LeadModel == target.LeadModel &&
			swarmWorkerProvider(p) == swarmWorkerProvider(target) &&
			p.WorkerModel == target.WorkerModel {
			return true
		}
	}
	return false
}

func swarmWorkerProvider(pair config.SwarmPair) string {
	if pair.WorkerProvider == "" {
		return pair.LeadProvider
	}
	return pair.WorkerProvider
}

// evaluateSwarmPresets 根据当前已就绪的提供方列表与已有配对，评估所有预设组合的就绪与缺项状态。
func evaluateSwarmPresets(providers []ProviderInfo, existing []config.SwarmPair) []swarmPresetState {
	states := make([]swarmPresetState, 0, len(swarmPresets))

	for _, preset := range swarmPresets {
		var missing []string

		resolveSide := func(providerID string, modelID *string, effort **string) {
			provider, found := findProviderInfo(providers, providerID)
			if !found || !provider.Available {
				missing = appendMissingUnique(missing, missingProviderDesc(providerID))
				return
			}
			model, found := resolveSwarmPresetModel(provider.Models, *modelID)
			if !found {
				missing = appendMissingUnique(missing, fmt.Sprintf("缺少模型 %s", *modelID))
				return
			}
			*modelID = model.ID
			*effort = swarmPresetEffort(*effort, model)
		}
		resolveSide(preset.Pair.LeadProvider, &preset.Pair.LeadModel, &preset.Pair.LeadEffort)
		resolveSide(swarmWorkerProvider(preset.Pair), &preset.Pair.WorkerModel, &preset.Pair.WorkerEffort)

		ready := len(missing) == 0
		added := isSwarmPairAdded(preset.Pair, existing)

		states = append(states, swarmPresetState{
			Preset:  preset,
			Ready:   ready,
			Missing: missing,
			Added:   added,
		})
	}

	return states
}

// addSwarmPreset 复制预设组合模板并深拷贝 effort 指针后持久化追加到设置中。
func addSwarmPreset(st *SettingsState, r swarmPreset) config.SwarmPair {
	pair := r.Pair
	pair.ID = ""
	if r.Pair.LeadEffort != nil {
		v := *r.Pair.LeadEffort
		pair.LeadEffort = &v
	}
	if r.Pair.WorkerEffort != nil {
		v := *r.Pair.WorkerEffort
		pair.WorkerEffort = &v
	}

	st.mu.Lock()
	st.cfgMu.Lock()
	added := st.cfg.Orchestration.AddPair(pair)
	st.cfgMu.Unlock()
	st.save()
	st.mu.Unlock()

	return added
}

// renderPresetBadge 渲染预设组合项状态胶囊标签。
func renderPresetBadge(c *ui.Context, key string, text string, textColor ui.Color, bgColor ui.Color) {
	ui.Box(c.Key(key)).
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(bgColor).
		Children(func() {
			ui.Text(c, text).
				FontSize(uiFont(TypeCaption)).
				FontWeight(500).
				TextColor(textColor)
		})
}

// renderSwarmPresetsPanel 渲染 Swarm 预设组合入口按钮及弹层面板。
func renderSwarmPresetsPanel(c *ui.Context, st *SettingsState, providers []ProviderInfo) {
	uiSt := getSwarmUIState(st)
	trigger := ui.ButtonBase(c.Key("swarm-preset-trigger-btn")).
		Label("预设组合").
		Padding(settingsBadgePadY, settingsCompactPadX).
		Radius(RadiusSM).
		Background(ThemeOverlay(c, 0.08))

	if trigger.Clicked() {
		uiSt.presetOpen = !uiSt.presetOpen
	}

	trigger.Children(func() {
		ui.Row(c.Key("swarm-preset-trigger-row")).Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "预设组合").
				FontSize(uiFont(TypeFootnote)).
				FontWeight(500).
				TextColor(ThemePrimaryText(c))
		})
	})

	host := c.Services()
	native := flyoutHostWindow(host) != nil
	background := ThemeSurface(c)
	closePreset := func() {
		uiSt.presetOpen = false
		closeAppFlyoutPopover(host, "swarm-preset-popover")
		st.invalidate()
	}
	appFlyoutPopover(c, "swarm-preset-popover", trigger, &uiSt.presetOpen, nil, appFlyoutProps{Width: int(settingsPresetPopoverWidth), Background: &background}, func(c *ui.Context) {
		panel := ui.Column(c.Key("swarm-preset-panel")).
			Width(settingsPresetPopoverWidth).
			Padding(Space5).
			Gap(Space4)
		if !native {
			panel = panel.Radius(RadiusMD).Background(ThemeSurface(c)).Border(Hairline, ThemeBorder(c)).Shadow(0, 6, 20, 0, ThemeOverlay(c, 0.18))
		}
		panel.
			Children(func() {
				// 头部：标题与关闭按钮
				ui.Row(c.Key("swarm-preset-header")).
					AlignItems(ui.Center).
					Children(func() {
						ui.Column(c.Key("swarm-preset-header-text")).
							Gap(Space0).
							Grow(1).
							Children(func() {
								ui.Text(c, "编排预设组合").
									FontSize(uiFont(TypeTitle)).
									FontWeight(600).
									TextColor(ThemePrimaryText(c))
							})

						closeBtn := ui.ButtonBase(c.Key("swarm-preset-close-btn")).
							Label("关闭").
							Padding(settingsBadgePadY, settingsCompactPadX).
							Radius(RadiusXS).
							Background(ThemeOverlay(c, 0.05)).
							Children(func() {
								ui.Text(c, "关闭").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c))
							})
						if closeBtn.Clicked() {
							closePreset()
						}
					})

				// 分隔线
				ui.Box(c.Key("swarm-preset-header-div")).
					Height(Hairline).
					Background(ThemeDivider(c))

				// 获取已有配对快照进行状态对比
				st.mu.Lock()
				existingPairs := make([]config.SwarmPair, len(st.cfg.Orchestration.Pairs))
				copy(existingPairs, st.cfg.Orchestration.Pairs)
				st.mu.Unlock()

				presets := evaluateSwarmPresets(providers, existingPairs)

				ui.Column(c.Key("swarm-preset-list")).
					Gap(Space3).
					Children(func() {
						for i, item := range presets {
							preset := item.Preset
							rowKey := fmt.Sprintf("swarm-preset-row-%d-%s", i, preset.ID)

							ui.Box(c.Key(rowKey)).
								Padding(settingsCompactPadX, Space4).
								Radius(RadiusSM).
								Background(ThemeOverlay(c, 0.03)).
								Children(func() {
									ui.Row(c.Key(rowKey + "-content")).
										AlignItems(ui.Start).
										Children(func() {
											ui.Column(c.Key(rowKey + "-info")).
												Grow(1).
												Gap(Space1).
												Children(func() {
													ui.Row(c.Key(rowKey + "-title-row")).
														AlignItems(ui.Center).
														Gap(Space3).
														Children(func() {
															ui.Text(c, preset.Title).
																FontSize(uiFont(TypeBody)).
																FontWeight(600).
																TextColor(ThemePrimaryText(c))

															if !item.Added {
																if item.Ready {
																	renderPresetBadge(c, rowKey+"-badge-ready", "就绪", ThemeSuccess(c), ThemeOverlay(c, 0.12))
																} else {
																	renderPresetBadge(c, rowKey+"-badge-waiting", "等待", ThemeSecondaryText(c), ThemeOverlay(c, 0.08))
																}
															}
														})

													ui.Text(c, preset.Summary).
														FontSize(uiFont(TypeFootnote)).
														TextColor(ThemeSecondaryText(c))

													if !item.Ready && len(item.Missing) > 0 {
														ui.Column(c.Key(rowKey + "-missing-list")).
															Gap(Space0).
															Children(func() {
																for mIdx, msg := range item.Missing {
																	missKey := fmt.Sprintf("%s-missing-%d", rowKey, mIdx)
																	ui.Text(c.Key(missKey), "• "+msg).
																		FontSize(uiFont(TypeCaption)).
																		TextColor(ThemeSecondaryText(c))
																}
															})
													}
												})

											if item.Added {
												ui.ButtonBase(c.Key(rowKey+"-btn-added")).
													Label("已添加").
													Padding(settingsBadgePadY, settingsCompactPadX).
													Radius(RadiusSM).
													Disabled(true).
													Background(ThemeOverlay(c, 0.04)).
													Children(func() {
														ui.Text(c, "已添加").
															FontSize(uiFont(TypeFootnote)).
															TextColor(ThemeSecondaryText(c))
													})
											} else if item.Ready {
												addBtn := ui.ButtonBase(c.Key(rowKey+"-btn-add")).
													Label("添加配对").
													Padding(settingsBadgePadY, settingsCompactPadX).
													Radius(RadiusSM).
													Background(ThemeAccent(c)).
													Children(func() {
														ui.Text(c, "添加配对").
															FontSize(uiFont(TypeFootnote)).
															FontWeight(500).
															TextColor(ThemeOnAccent(c))
													})
												if addBtn.Clicked() {
													addSwarmPreset(st, preset)
													closePreset()
												}
											}
										})
								})
						}
					})
			})
	})
}
