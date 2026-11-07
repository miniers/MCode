package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// magpieDefaultBaseURL 与 config.DefaultMagpieBaseURL 保持一致；UI 层不 import config，
// 这里保留一份字面量，改默认地址时两处要一起改。
const magpieDefaultBaseURL = "http://127.0.0.1:3425/v1"

// magpieHubState 是「Magpie 网关」区块自己的快照与交互状态。
// 网关列表复用 st.endpoints 的端点数据，这里只额外记录 claude、codex 与 cline 的 Magpie 快照，
// 避免和供应商小节里按 agent 分开的 Magpie 状态互相覆盖。
type magpieHubState struct {
	sheetGatewayID string
	sheetSaving    bool

	claudeLoaded  bool
	claudeLoading bool
	claude        AgentMagpieSetting

	codexLoaded  bool
	codexLoading bool
	codex        AgentMagpieSetting

	clineLoaded  bool
	clineLoading bool
	cline        AgentMagpieSetting
}

// markMagpieHubClaudeStale 让 hub 里的 agent 快照在下一帧重新读取。
// 函数名沿用 claude 时期的叫法（调用点都写好了），现在同时覆盖 codex 与 cline。
func markMagpieHubClaudeStale(st *SettingsState) {
	st.mu.Lock()
	st.magpieHub.claudeLoaded = false
	st.magpieHub.codexLoaded = false
	st.magpieHub.clineLoaded = false
	st.mu.Unlock()
	st.invalidate()
}

// magpieGatewayStatusKind 决定网关状态胶囊的语义颜色。
type magpieGatewayStatusKind int

const (
	magpieGatewayStatusUnknown magpieGatewayStatusKind = iota
	magpieGatewayStatusOK
	magpieGatewayStatusDanger
)

// magpieGatewayStatus 是网关头部状态胶囊的内容：文字加语义类型。
type magpieGatewayStatus struct {
	Text string
	Kind magpieGatewayStatusKind
}

// magpieGatewayStatusOf 汇总一个网关的当前状态：未启用 / 未设置 API Key / 检测中 / 连接失败 / 已连接。
func magpieGatewayStatusOf(ep CustomEndpoint, pi []PiMagpieEndpoint) magpieGatewayStatus {
	if !ep.Enabled {
		return magpieGatewayStatus{Text: "未启用"}
	}
	if !ep.HasAPIKey {
		return magpieGatewayStatus{Text: "未设置 API Key"}
	}
	var match *PiMagpieEndpoint
	for i := range pi {
		if pi[i].ID == ep.ID {
			match = &pi[i]
			break
		}
	}
	if match == nil {
		return magpieGatewayStatus{Text: "检测中…"}
	}
	if match.Err != "" {
		return magpieGatewayStatus{Text: "连接失败：" + match.Err, Kind: magpieGatewayStatusDanger}
	}
	return magpieGatewayStatus{
		Text: fmt.Sprintf("已连接 · %d 个模型", len(match.Models)),
		Kind: magpieGatewayStatusOK,
	}
}

// renderMagpieHubStatusPill 渲染网关状态徽标。
func renderMagpieHubStatusPill(c *ui.Context, key string, status magpieGatewayStatus) {
	if strings.TrimSpace(status.Text) == "" {
		return
	}
	tone := ToneNeutral
	switch status.Kind {
	case magpieGatewayStatusOK:
		tone = ToneSuccess
	case magpieGatewayStatusDanger:
		tone = ToneDanger
	}
	Badge(c, key, status.Text, tone)
}

func magpieKeyPlaceholder(hasSavedKey bool) string {
	if hasSavedKey {
		return "留空则保留已保存的 Key"
	}
	return "填写网关 API Key（必填）"
}

// renderMagpieGatewayForm 渲染 Magpie 网关的行内编辑表单（复用自定义端点的草稿状态）。
func renderMagpieGatewayForm(c *ui.Context, st *SettingsState, id, keyDraft, webKeyDraft, personaURLDraft, personasJSONDraft string,
	editHasWebKey, editHasAPIKey, personaTestBusy, personaTestShown bool, personaTestErr string, personaTestResult PersonaSourceTestResult,
	quotaLoading bool, quotaErr string, quotaList []string, editQuotaMap map[string]bool,
	busy bool, testing, saving bool, editErr string, testErr string, testResult *CustomEndpointTestResult, modes ...string) {

	mode := "hub"
	if len(modes) > 0 {
		mode = modes[0]
	}
	if mode == "hub" {
		renderSettingsDivider(c, "div-hub-form")

		title := "添加 Magpie 网关"
		if id != "" {
			title = "编辑 Magpie 网关"
		}
		ui.Row(c.Key("row-hub-form-title")).
			Padding(Space4, Space5, Space1, Space5).
			Children(func() {
				ui.Text(c, title).
					FontSize(TypeProse).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
			})

	}
	renderFields := func() {
	if mode == "hub" || mode == "gateway" {
		renderSettingRow(c, "row-magpie-gateway-name", "名称", func() {
			TextField(c, TextFieldProps{
				Key:         "input-hub-name",
				Value:       &st.endpoints.editName,
				Label:       "网关名称",
				Placeholder: "Magpie",
				Width:       settingsTextFieldWidth,
			})
		})

		renderSettingsDivider(c, "div-hub-form-1")

	}
	if mode != "persona" {
		renderSettingRow(c, "row-magpie-gateway-baseurl", "接口地址（BaseURL）", func() {
			textFunc(
				func(*SettingsState) (string, bool) { return st.endpoints.editBaseURL, false },
				func(st *SettingsState, v string) {
					st.mu.Lock()
					st.endpoints.editBaseURL = v
					st.mu.Unlock()
				},
				magpieDefaultBaseURL,
			).render(c, st, setting{ID: "hub-baseurl", Title: "网关 BaseURL"})
		})

		renderSettingsDivider(c, "div-hub-form-2")

		if mode != "connect" {
			renderSettingRow(c, "row-magpie-gateway-model", "默认模型", func() {
				textFunc(
					func(*SettingsState) (string, bool) { return st.endpoints.editModel, false },
					func(st *SettingsState, v string) {
						st.mu.Lock()
						st.endpoints.editModel = v
						st.mu.Unlock()
					},
					"自动",
				).render(c, st, setting{ID: "hub-model", Title: "网关默认模型"})
			})

			renderSettingsDivider(c, "div-hub-form-3")

		}
		keyPlaceholder := magpieKeyPlaceholder(editHasAPIKey)
		renderSettingRow(c, "row-magpie-gateway-apikey", "密钥（API Key）", func() {
			textFunc(
				func(*SettingsState) (string, bool) { return keyDraft, false },
				func(st *SettingsState, v string) {
					st.mu.Lock()
					st.endpoints.editAPIKey = v
					st.mu.Unlock()
				},
				keyPlaceholder,
			).withSecret().render(c, st, setting{ID: "hub-apikey", Title: "网关 API Key"})
		})

	}
	if mode == "hub" || mode == "persona" {
		// 人设来源
		if mode == "hub" {
			renderSettingsDivider(c, "div-hub-persona")

			ui.Column(c.Key("row-hub-persona-header")).
				Padding(Space4, Space5, Space4, Space5).
				Gap(Space1).
				Children(func() {
					ui.Text(c, "人设来源（可选）").
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, "本机 magpie 会自动读取；远端 magpie 请配置 web 管理接口，或粘贴人设列表").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})

		}
		renderSettingRow(c, "row-magpie-persona-weburl", "人设管理地址", func() {
			textFunc(
				func(*SettingsState) (string, bool) { return st.endpoints.editPersonaWebURL, false },
				func(st *SettingsState, v string) {
					st.mu.Lock()
					st.endpoints.editPersonaWebURL = v
					st.mu.Unlock()
				},
				"https://magpie.example.com:3430（含端口）",
			).render(c, st, setting{ID: "hub-persona-weburl", Title: "人设 web 管理地址"})
		})

		if personaWebURLNeedsHTTPSWarning(personaURLDraft) {
			ui.Row(c.Key("row-hub-persona-weburl-warn")).
				Padding(0, Space5, Space4, Space5).
				Children(func() {
					ui.Text(c, "明文 http 会暴露 web key，建议使用 https 或 SSH 隧道").
						FontSize(TypeFootnote).
						TextColor(ThemeWarning(c))
				})
		}

		renderSettingsDivider(c, "div-hub-persona-webkey")
		renderSettingRow(c, "row-magpie-persona-webkey", "人设管理密钥", func() {
			placeholder := "web 管理接口的 key"
			if editHasWebKey {
				placeholder = "已保存，留空则不修改"
			}
			textFunc(
				func(*SettingsState) (string, bool) { return webKeyDraft, false },
				func(st *SettingsState, v string) {
					st.mu.Lock()
					st.endpoints.editWebKey = v
					st.mu.Unlock()
				},
				placeholder,
			).withSecret().render(c, st, setting{ID: "hub-persona-webkey", Title: "人设 web key"})
		})

		renderSettingsDivider(c, "div-hub-persona-json")
		renderStackedSettingRow(c, "row-magpie-persona-json", "手动人设列表", func() {
			TextField(c, TextFieldProps{
				Key:         "area-hub-persona-json",
				Value:       &personasJSONDraft,
				Label:       "手动人设列表 JSON",
				Placeholder: `[{"id":"default","label":"默认"}]`,
				FillWidth:   true,
				Multiline:   true,
				MinLines:    3,
				MaxLines:    8,
				OnChange: func(text string) {
					st.mu.Lock()
					st.endpoints.editPersonasJSON = text
					st.mu.Unlock()
				},
			})
		})

		renderSettingsDivider(c, "div-hub-persona-rules")
		renderStackedSettingRow(c, "row-magpie-persona-rules", "人设规则", func() {
			ui.Checkbox(c.Key("input-hub-persona-rules"), &st.endpoints.editPersonaRules, "已在 magpie 部署 persona-rules").Tooltip("手动列表才生效")
		})

		renderSettingsDivider(c, "div-hub-persona-test")
		renderSettingRow(c, "row-magpie-persona-test", "测试人设", func() {
			testLabel := "测试并刷新"
			if personaTestBusy {
				testLabel = "测试中…"
			}
			personaTestBtn := ui.ButtonBase(c.Key("btn-hub-persona-test")).
				Label(testLabel).
				Radius(RadiusSM).
				Padding(Space1, Space4).
				Background(ThemeHover(c)).
				Disabled(busy)
			if !busy && personaTestBtn.Hovered() {
				personaTestBtn.Background(ThemeOverlay(c, 0.15))
			}
			if !busy && personaTestBtn.Clicked() {
				testMagpiePersonas(st)
			}
			personaTestBtn.Children(func() {
				ui.Text(c, "测试并刷新").
					FontSize(TypeFootnote).
					TextColor(ThemeAccent(c))
			})
		})

		if personaTestErr != "" {
			ui.Row(c.Key("row-hub-persona-test-err")).
				Padding(Space1, Space5).
				Children(func() {
					ui.Text(c, personaTestErr).
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c))
				})
		} else if personaTestShown {
			text := personaTestResult.Message
			if text == "" {
				text = "人设来源测试成功"
			}
			ui.Row(c.Key("row-hub-persona-test-res")).
				Padding(Space1, Space5).
				Children(func() {
					ui.Text(c, text).
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
		}

	}
	if mode == "hub" || mode == "gateway" {
		// 菜单栏额度
		renderSettingsDivider(c, "div-hub-quota")

		renderSettingRow(c, "row-magpie-gateway-quota", "菜单栏额度", func() {
			refreshBtn := ui.ButtonBase(c.Key("btn-hub-quota-refresh")).
				Label("刷新供应商").
				Disabled(busy).
				Radius(RadiusSM).
				Padding(Space1, Space4).
				Background(ThemeHover(c))
			if refreshBtn.Hovered() {
				refreshBtn.Background(ThemeOverlay(c, 0.15))
			}
			if refreshBtn.Clicked() && id != "" {
				loadMagpieQuotaProviders(st, id)
			}
			refreshBtn.Children(func() {
				ui.Text(c, "刷新供应商").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			})
		})

		if id == "" {
			ui.Row(c.Key("row-hub-quota-unsaved")).
				Padding(Space2, Space5, Space4, Space5).
				Children(func() {
					ui.Text(c, "保存后可选择要在菜单栏展示额度的供应商").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
		} else if quotaLoading {
			ui.Row(c.Key("row-hub-quota-loading")).
				Padding(Space2, Space5, Space4, Space5).
				Children(func() {
					ui.Text(c, "正在读取网关供应商…").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
		} else if quotaErr != "" {
			ui.Row(c.Key("row-hub-quota-err")).
				Padding(Space2, Space5, Space4, Space5).
				Children(func() {
					ui.Text(c, "读取额度供应商失败："+quotaErr).
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c))
				})
		} else if len(quotaList) == 0 {
			ui.Row(c.Key("row-hub-quota-empty")).
				Padding(Space2, Space5, Space4, Space5).
				Children(func() {
					ui.Text(c, "该网关暂无额度卡片").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
		} else {
			ui.Column(c.Key("col-hub-quota-list")).
				Padding(Space2, Space5).
				Gap(Space2).
				Children(func() {
					for _, qID := range quotaList {
						idCapture := qID
						checked := editQuotaMap[idCapture]
						cb := ui.Checkbox(c.Key("input-hub-quota-"+idCapture), &checked, trayQuotaProviderLabel(idCapture))
						if cb.Changed() {
							st.mu.Lock()
							if st.endpoints.editQuotaProviders == nil {
								st.endpoints.editQuotaProviders = make(map[string]bool)
							}
							st.endpoints.editQuotaProviders[idCapture] = checked
							st.mu.Unlock()
						}
					}
				})
		}

	}
	}
	if mode == "persona" || mode == "gateway" {
		renderSettingsSection(c, "sec-magpie-form-"+mode, "", renderFields)
	} else {
		renderFields()
	}
	// 测试结果
	if mode != "persona" && mode != "connect" && testErr != "" {
		if mode == "hub" {
			renderSettingsDivider(c, "div-hub-form-test-err")
		}
		ui.Row(c.Key("row-hub-form-test-err")).
			Padding(Space3, Space5).
			Children(func() {
				ui.Text(c, testErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			})
	} else if mode != "persona" && mode != "connect" && testResult != nil {
		if mode == "hub" {
			renderSettingsDivider(c, "div-hub-form-test-res")
		}
		text := fmt.Sprintf("连接成功 (%dms, %d 个模型)", testResult.Latency.Milliseconds(), len(testResult.Models))
		if !testResult.OK {
			text = testResult.Message
			if text == "" {
				text = "连接失败"
			}
		}
		ui.Row(c.Key("row-hub-form-test-res")).
			Padding(Space3, Space5).
			Children(func() {
				ui.Text(c, text).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			})
	}

	if editErr != "" {
		if mode == "hub" {
			renderSettingsDivider(c, "div-hub-form-edit-err")
		}
		ui.Row(c.Key("row-hub-form-edit-err")).
			Padding(Space3, Space5).
			Children(func() {
				ui.Text(c, editErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			})
	}

	if mode == "hub" {
		renderSettingsDivider(c, "div-hub-form-actions")
	}

	buttonsRow := ui.Row(c.Key("row-hub-form-buttons"))
	// 行内表单（未连接时的「连接」表单）没有行内边距，按钮自己留白，
	// 让左边与上面的表单项对齐、下边与卡片底边留出间距。sheet 里已有外壳内边距，不再叠加。
	if mode == "connect" || mode == "hub" {
		buttonsRow = buttonsRow.Padding(Space3, Space5)
	}
	buttonsRow.Gap(Space4).
		Children(func() {
			if mode == "hub" || mode == "gateway" {
				testLabel := "测试连接"
				if testing {
					testLabel = "测试中…"
				}
				testBtn := ui.ButtonBase(c.Key("btn-hub-test")).
					Label(testLabel).
					Radius(RadiusSM).
					Padding(Space2, Space4).
					Background(ThemeHover(c)).
					Disabled(busy)
				if !busy && testBtn.Hovered() {
					testBtn.Background(ThemeOverlay(c, 0.15))
				}
				if !busy && testBtn.Clicked() {
					testCustomEndpoint(st)
				}
				testBtn.Children(func() {
					ui.Text(c, "测试连接").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})

			}
			saveLabel := "保存"
			if mode == "connect" {
				saveLabel = "连接"
			}
			if saving {
				saveLabel = "保存中…"
			}
			saveBtn := ui.ButtonBase(c.Key("btn-hub-save")).
				Label(saveLabel).
				Radius(RadiusSM).
				Padding(Space2, Space4).
				Background(ThemeAccent(c)).
				Disabled(busy)
			if !busy && saveBtn.Hovered() {
				saveBtn.Background(ThemeAccent(c).Alpha(0.85))
			}
			if !busy && saveBtn.Clicked() {
				if mode == "gateway" {
					st.mu.Lock()
					st.magpieHub.sheetSaving = true
					st.mu.Unlock()
				}
				saveCustomEndpoint(st)
			}
			saveBtn.Children(func() {
				ui.Text(c, saveLabel).
					FontSize(TypeBody).
					TextColor(ThemeOnAccent(c))
			})

			if mode != "connect" {
				cancelBtn := ui.ButtonBase(c.Key("btn-hub-cancel")).
					Label("取消").
					Radius(RadiusSM).
					Padding(Space2, Space4).
					Background(ThemeHover(c)).
					Disabled(busy)
				if cancelBtn.Hovered() {
					cancelBtn.Background(ThemeOverlay(c, 0.15))
				}
				if cancelBtn.Clicked() {
					st.mu.Lock()
					closeCustomEndpointForm(st)
					st.mu.Unlock()
					if mode != "hub" {
						closeSettingsSheet(st)
					}
					st.invalidate()
				}
				cancelBtn.Children(func() {
					ui.Text(c, "取消").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
}
