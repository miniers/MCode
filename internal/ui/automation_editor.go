package mcodeui

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

func renderAutomationEditor(c *ui.Context, s *chatState, as *automationState, isNew bool, selectedID, formErr string, saving, confirmDelete bool, runningIDs, busyIDs map[string]bool, selectedUnread int, includeArchived, runsLoading bool, runsErr string, runs []AutomationRun, runRead map[string]time.Time, runArchived map[string]bool, openRunID string) {
	// 标题与操作栏
	ui.Row(c.Key("automation-detail-header")).
		FillWidth().
		Wrap().
		AlignItems(ui.Center).
		Children(func() {
			title := "编辑自动化"
			if isNew {
				title = "新建自动化"
			}
			ui.Text(c, title).
				FontSize(TypeTitle).
				FontWeight(600).
				TextColor(ThemePrimaryText(c)).
				Grow(1)

			// 编辑态特有操作：删除与立即运行
			if !isNew && selectedID != "" {
				ui.Row(c.Key("automation-run-now-row")).Gap(Space2).AlignItems(ui.Center).Children(func() {
					runID := selectedID
					Button(c.Key("automation-run-now"), ButtonOpts{
						Icon:     IconPlay(),
						Text:     "立即运行",
						Label:    "automation-run-now",
						Disabled: runningIDs[runID] || busyIDs[runID],
						OnClick: func() {
							s.runAutomationNow(runID)
						},
						Variant: ButtonSecondary,
					})
					if runningIDs[runID] {
						LoadingIndicator(c, nil, "", IconSM)
					}
				})

				if confirmDelete {
					Button(c.Key("automation-delete-confirm"), ButtonOpts{
						Text:  "确认删除",
						Label: "确认删除",
						OnClick: func() {
							s.deleteAutomation(selectedID)
						},
						Variant: ButtonDanger,
					})
					Button(c.Key("automation-delete-cancel"), ButtonOpts{
						Text:  "取消",
						Label: "取消",
						OnClick: func() {
							automationMu.Lock()
							as.confirmDelete = false
							automationMu.Unlock()
							s.invalidate()
						},
						Variant: ButtonSecondary,
					})
				} else {
					Button(c.Key("automation-delete"), ButtonOpts{
						Icon:  IconTrash2(),
						Text:  "删除",
						Label: "automation-delete",
						OnClick: func() {
							automationMu.Lock()
							as.confirmDelete = true
							automationMu.Unlock()
							s.invalidate()
						},
						Variant: ButtonDanger,
					})
				}
			}

			// 取消：清空选中与新建标记，回到未选中态
			Button(c.Key("automation-cancel"), ButtonOpts{
				Text:  "取消",
				Label: "取消编辑",
				OnClick: func() {
					automationMu.Lock()
					as.editSession++
					as.selectedID = ""
					as.isNew = false
					as.confirmDelete = false
					automationMu.Unlock()
					s.invalidate()
				},
				Variant: ButtonSecondary,
			})

			// 保存按钮：名称或 Prompt 为空（trim 后）或保存中时禁用
			canSave := !saving &&
				strings.TrimSpace(as.name) != "" &&
				strings.TrimSpace(as.prompt) != ""
			Button(c.Key("automation-save"), ButtonOpts{
				Text:     "保存",
				Label:    "automation-save",
				Disabled: !canSave,
				OnClick: func() {
					s.saveAutomation()
				},
				Variant: ButtonPrimary,
			})
		})

	// 表单校验错误提示
	if formErr != "" {
		ui.Row(c.Key("automation-form-err-banner")).
			FillWidth().
			Padding(Space2, Space3).
			Radius(RadiusSM).
			Background(ThemeDanger(c).Alpha(0.12)).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconOctagonAlert()).
					Size(IconSM, IconSM).
					TextColor(ThemeDanger(c))
				ui.Text(c, formErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			})
	}

	// 滚动表单主体与运行记录
	ui.Scroll(c.Key("automation-form-scroll")).
		Grow(1).
		FillWidth().
		Children(func() {
			ui.Column(c.Key("automation-form-fields")).
				FillWidth().
				Gap(Space4).
				Children(func() {
					// 名称输入
					ui.Column(c.Key("form-name-group")).
						FillWidth().
						Gap(Space1).
						Children(func() {
							ui.Text(c, "名称").
								FontSize(TypeFootnote).
								FontWeight(500).
								TextColor(ThemeSecondaryText(c))
							ui.TextInput(c.Key("automation-name-input"), &as.name).
								Placeholder("如：每周安全审计").
								Label("automation-name-input").
								FillWidth()
						})

					// Prompt 多行输入
					ui.Column(c.Key("form-prompt-group")).
						FillWidth().
						Gap(Space1).
						Children(func() {
							ui.Text(c, "Prompt 提示词").
								FontSize(TypeFootnote).
								FontWeight(500).
								TextColor(ThemeSecondaryText(c))
							ui.TextArea(c.Key("automation-prompt-input"), &as.prompt).
								Placeholder("输入要执行的任务指令...").
								Label("automation-prompt-input").
								Height(automationPromptMinHeight).
								FillWidth()
						})

					renderAutomationScheduleFields(c, s, as)

					// 停止条件 StopWhen
					ui.Column(c.Key("form-stop-when-group")).
						FillWidth().
						Gap(Space1).
						Children(func() {
							ui.Text(c, "停止条件 (StopWhen)").
								FontSize(TypeFootnote).
								FontWeight(500).
								TextColor(ThemeSecondaryText(c))
							ui.TextInput(c.Key("automation-stop-when-input"), &as.stopWhen).
								Placeholder("如：所有单元测试均通过，或任务目标达成").
								Label("automation-stop-when-input").
								FillWidth()
						})

					// 模型与权限：只在每次运行新建会话时生效，心跳模式沿用原会话设置
					heartbeat := as.runMode == "heartbeat"
					catalog := modelPickerCatalogFor(s)
					providers := catalog.Providers
					ui.Row(c.Key("form-run-settings-row")).
						FillWidth().
						Wrap().
						AlignItems(ui.Center).
						Gap(Space5).
						Children(func() {
							// 模型：复用公共模型选择组件，未选时显示「跟随默认」
							ui.Row(c.Key("form-model-row")).
								Gap(Space2).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, "模型").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
									followsDefault := strings.TrimSpace(as.provider) == "" && strings.TrimSpace(as.model) == ""
									resolved := resolveModelPickerSelection(providers, ModelSelection{
										Provider: as.provider,
										Model:    as.model,
										Effort:   as.effort,
									})
									capsule := "跟随默认"
									if !followsDefault {
										capsule = modelShortDisplayName(providers, resolved.Provider, resolved.Model)
									}
									// 胶囊只显示短名，悬停提示补上模型全名与强度；跟随默认时只保留用途说明
									tooltip := "每次运行新建会话使用的模型，跟随默认表示沿用默认模型"
									if !followsDefault {
										tooltip = modelCapsuleFullNameTip(tooltip, providers, resolved)
									}
									renderModelPicker(c, catalog.apply(ModelPickerProps{
										Mode:           ModelPickerSimple,
										KeyPrefix:      "automation-model-",
										ScopeKey:       "automation-model-" + as.selectedID,
										Selection:      resolved,
										CapsuleLabel:   capsule,
										NoEffortSignal: followsDefault,
										Tooltip:        tooltip,
										TriggerLabel:   "选择自动化的模型",
										Disabled:       heartbeat,
										OnPickModel: func(_ ModelPickerSide, prov ProviderInfo, m ModelInfo) {
											automationMu.Lock()
											as.provider = prov.ID
											as.model = m.ID
											as.formErr = ""
											automationMu.Unlock()
											s.invalidate()
										},
										OnEffort: func(_ ModelPickerSide, effort string) {
											automationMu.Lock()
											as.effort = effort
											automationMu.Unlock()
											s.invalidate()
										},
										OnReset: func(ModelPickerSide) {
											automationMu.Lock()
											as.provider = ""
											as.model = ""
											as.effort = ""
											automationMu.Unlock()
											s.invalidate()
										},
										Invalidate: s.invalidate,
									}))
								})

							// 权限：空值表示跟随默认
							ui.Row(c.Key("form-runtime-mode-row")).
								Gap(Space2).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, "权限").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
									curLabel := automationRuntimeModeLabel(as.runtimeMode)
									labels := make([]string, 0, len(automationRuntimeModeOptions))
									for _, opt := range automationRuntimeModeOptions {
										labels = append(labels, opt.Label)
									}
									modeSel := ui.Select(c.Key("automation-runtime-mode-select"), &curLabel, labels).
										Label("权限模式").
										Width(150).
										Disabled(heartbeat)
									if modeSel.Changed() {
										next := ""
										for _, opt := range automationRuntimeModeOptions {
											if opt.Label == curLabel {
												next = opt.Value
												break
											}
										}
										automationMu.Lock()
										as.runtimeMode = next
										as.formErr = ""
										automationMu.Unlock()
										s.invalidate()
									}
								})

							if heartbeat {
								ui.Text(c, "心跳模式沿用原会话设置").
									FontSize(TypeCaption).
									TextColor(ThemeSecondaryText(c))
							}
						})

						// 启用开关与运行约束（并排）
					ui.Row(c.Key("form-limits-row")).
						FillWidth().
						Wrap().
						AlignItems(ui.Center).
						Gap(Space5).
						Children(func() {
							// 启用开关
							ui.Row(c.Key("automation-enabled-toggle")).
								Gap(Space2).
								AlignItems(ui.Center).
								Children(func() {
									// 开关的标签只进无障碍树，可见文案单独给出
									ui.Switch(c.Key("automation-enabled-check"), &as.enabled).
										Label("启用该自动化")
									ui.Text(c, "启用该自动化").
										FontSize(TypeFootnote).
										TextColor(ThemePrimaryText(c))
								})

							ui.Box(c.Key("form-limits-spacer")).Grow(1)

							// 最多连续失败次数：1–10 步进器
							ui.Row(c.Key("form-max-failures-row")).
								Gap(Space2).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, "最多连续失败").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
									maxFailuresVal := 3.0
									if v, err := strconv.Atoi(strings.TrimSpace(as.maxFailuresInput)); err == nil {
										maxFailuresVal = float64(v)
									}
									if maxFailuresVal < 1 {
										maxFailuresVal = 1
									} else if maxFailuresVal > 10 {
										maxFailuresVal = 10
									}
									failuresStepper := ui.Stepper(c.Key("automation-max-failures-stepper"), &maxFailuresVal, 1, 10, 1).
										Label("最多连续失败次数")
									if failuresStepper.Changed() {
										next := strconv.Itoa(int(maxFailuresVal))
										automationMu.Lock()
										as.maxFailuresInput = next
										as.formErr = ""
										automationMu.Unlock()
										s.invalidate()
									}
									ui.Text(c, strconv.Itoa(int(maxFailuresVal))).
										FontSize(TypeFootnote).
										TextColor(ThemePrimaryText(c))
								})

							// 最大运行次数
							ui.Row(c.Key("form-max-runs-row")).
								Gap(Space2).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, "最多运行次数（0 = 不限制）").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
									ui.TextInput(c.Key("automation-max-runs-input"), &as.maxRunsInput).
										Width(110).
										Label("automation-max-runs-input")
								})
						})

					// 错过触发时补跑
					ui.Row(c.Key("form-catch-up-row")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							catchUpVal := as.catchUp
							ui.Switch(c.Key("automation-catch-up-check"), &catchUpVal).
								Label("automation-catch-up-checkbox")
							if catchUpVal != as.catchUp {
								automationMu.Lock()
								as.catchUp = catchUpVal
								as.formErr = ""
								automationMu.Unlock()
								s.invalidate()
							}
							ui.Text(c, "错过触发时补跑一次（默认开启）").
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						})

					renderAutomationRunHistory(c, s, as, isNew, selectedID, selectedUnread, includeArchived, runsLoading, runsErr, runs, runRead, runArchived, openRunID)
				})
		})
}
