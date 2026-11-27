package mcodeui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
)

// mcpFoldEnabled 读出折叠开关；折叠关闭时 foldThreshold 为 -1。
func mcpFoldEnabled(st *SettingsState) (bool, bool) {
	uiSt := getMCPUIState(st)
	uiSt.mu.Lock()
	defer uiSt.mu.Unlock()
	return uiSt.foldThreshold >= 0, false
}

func setMCPFoldEnabled(st *SettingsState, on bool) {
	uiSt := getMCPUIState(st)
	uiSt.mu.Lock()
	prevThreshold := uiSt.foldThreshold
	if on {
		uiSt.foldThreshold = 0
	} else {
		uiSt.foldThreshold = -1
	}
	if !saveMCPToolPolicyLocked(uiSt) {
		uiSt.foldThreshold = prevThreshold
	}
	uiSt.mu.Unlock()
	st.invalidate()
}

// mcpFormTextField 把表单里的某个输入字段包装成回调控件的读写函数；
// 写入只改内存，输入事件本身会触发下一帧。
func mcpFormTextField(field func(*mcpPageUIState) *string) (func(*SettingsState) (string, bool), func(*SettingsState, string)) {
	get := func(st *SettingsState) (string, bool) {
		uiSt := getMCPUIState(st)
		uiSt.mu.Lock()
		defer uiSt.mu.Unlock()
		return *field(uiSt), false
	}
	set := func(st *SettingsState, text string) {
		uiSt := getMCPUIState(st)
		uiSt.mu.Lock()
		*field(uiSt) = text
		uiSt.mu.Unlock()
	}
	return get, set
}

// renderMCPNewThreadDefaultRow 渲染「新会话默认启用」开关行，
// 关掉的服务在新建会话时默认关闭；只读来源不会注入会话，不调用本函数。
func renderMCPNewThreadDefaultRow(c *ui.Context, st *SettingsState, uiSt *mcpPageUIState, srv agentconf.MCPServer, key string) {
	// 渲染路径只读缓存快照，避免逐帧读盘。
	uiSt.mu.Lock()
	enabled := !uiSt.newThreadDisabled[key]
	saveErr := uiSt.newThreadDisabledErr
	uiSt.mu.Unlock()

	if saveErr != "" {
		ui.Text(c, saveErr).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeDanger(c)).
			Margin(0, Space5, Space3, Space5)
	}

	srvKey := key + "-0"
	renderSettingRow(c, "row-mcp-newthread-"+srvKey, "新会话默认启用", func() {
		toggleFunc(
			func(*SettingsState) (bool, bool) { return enabled, false },
			func(st *SettingsState, on bool) {
				opts := st.agentConfWriteOptions()
				uiSt.mu.Lock()
				prevOff, hadKey := uiSt.newThreadDisabled[key]
				hadMap := uiSt.newThreadDisabled != nil
				if on {
					delete(uiSt.newThreadDisabled, key)
				} else {
					if uiSt.newThreadDisabled == nil {
						uiSt.newThreadDisabled = make(map[string]bool)
					}
					uiSt.newThreadDisabled[key] = true
				}
				if !saveMCPNewThreadDisabledLocked(uiSt, opts) {
					if !hadMap {
						uiSt.newThreadDisabled = nil
					} else if hadKey {
						uiSt.newThreadDisabled[key] = prevOff
					} else {
						delete(uiSt.newThreadDisabled, key)
					}
				}
				uiSt.mu.Unlock()
				st.invalidate()
			},
		).render(c, st, setting{ID: "row-mcp-newthread-" + srvKey, Title: srv.Name})
	})
}

// renderMCPCustomForm 渲染新增/编辑自定义 MCP 服务的表单。
func renderMCPCustomForm(c *ui.Context, st *SettingsState, uiSt *mcpPageUIState) {
	uiSt.mu.Lock()
	editingName := uiSt.editingServer
	trans := uiSt.transportInput
	formErr := uiSt.formError
	uiSt.mu.Unlock()

	ui.Column(c.Key("mcp-form-container")).
		Children(func() {
			if editingName != "" {
				ui.Text(c, fmt.Sprintf("编辑 MCP 服务: %s", editingName)).
					Padding(settingsRowPadY, settingsRowPadX).
					FontSize(uiFont(TypeProse)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
				renderSettingsDivider(c, "div-form-title")
			}

			if formErr != "" {
				ui.Text(c, formErr).
					Padding(Space2, settingsRowPadX).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c))
				renderSettingsDivider(c, "div-form-err")
			}

			// 1. 服务名称
			SettingRow(c, "row-form-name", "服务名称", func() {
				get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.nameInput })
				textFunc(get, set, "例如: sqlite-server").render(c, st, setting{ID: "row-form-name", Title: "服务名称"})
			})
			renderSettingsDivider(c, "div-form-transport")

			// 2. 传输方式（本地命令 stdio vs 远程地址 http）
			SettingRow(c, "row-form-transport", "传输方式", func() {
				transIdx := 0
				if strings.EqualFold(trans, "http") || strings.EqualFold(trans, "sse") {
					transIdx = 1
				}
				Segmented(c, "btn-form-trans", &transIdx, []Segment{
					{Label: "stdio"},
					{Label: "http"},
				}, func(idx int) {
					uiSt.mu.Lock()
					if idx == 1 {
						uiSt.transportInput = "http"
					} else {
						uiSt.transportInput = "stdio"
					}
					uiSt.mu.Unlock()
					st.invalidate()
				}).Label("传输方式")
			})

			// 3. 模式对应输入项
			if strings.EqualFold(trans, "http") || strings.EqualFold(trans, "sse") {
				renderSettingsDivider(c, "div-form-url")
				SettingRow(c, "row-form-url", "服务地址", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.urlInput })
					textFunc(get, set, "例如: https://mcp.example.com/sse").render(c, st, setting{ID: "row-form-url", Title: "服务地址"})
				})

				renderSettingsDivider(c, "div-form-headers")
				SettingRow(c, "row-form-headers", "请求头", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.headersInput })
					textFunc(get, set, "每行一个 KEY: VALUE，Authorization 等密钥自动加密存凭据库").
						withSecret().
						render(c, st, setting{ID: "row-form-headers", Title: "请求头"})
				})
			} else {
				renderSettingsDivider(c, "div-form-command")
				SettingRow(c, "row-form-command", "执行命令", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.commandInput })
					textFunc(get, set, "例如: npx 或 /usr/local/bin/server").render(c, st, setting{ID: "row-form-command", Title: "执行命令"})
				})

				renderSettingsDivider(c, "div-form-args")
				SettingRow(c, "row-form-args", "运行参数", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.argsInput })
					textFunc(get, set, "空格分隔，例如: -y @modelcontextprotocol/server-everything").render(c, st, setting{ID: "row-form-args", Title: "运行参数"})
				})

				renderSettingsDivider(c, "div-form-env")
				SettingRow(c, "row-form-env", "环境变量", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.envInput })
					textFunc(get, set, "逗号或换行分隔 KEY=VALUE，含 TOKEN/KEY/SECRET 自动加密存凭据库").
						withSecret().
						render(c, st, setting{ID: "row-form-env", Title: "环境变量"})
				})
			}

			if editingName != "" {
				renderSettingsDivider(c, "div-form-actions")
				ui.Row(c.Key("row-form-actions")).
					Padding(settingsRowPadY, settingsRowPadX).
					AlignItems(ui.Center).
					Gap(Space4).
					Children(func() {
						renderMCPFormActions(c, st, uiSt, editingName)
					})
			}
		})
}

func renderMCPFormActions(c *ui.Context, st *SettingsState, uiSt *mcpPageUIState, editingName string) {
	cancelBtn := ui.ButtonBase(c.Key("btn-mcp-form-cancel")).
		Label("取消").
		Height(ControlMD).
		PaddingX(Space4).
		Radius(RadiusSM).
		Border(Hairline, ThemeBorder(c)).
		Cursor(ui.CursorPointer)
	cancelBg := ThemeSurfaceMain(c)
	if cancelBtn.Pressed() {
		cancelBg = ThemeOverlayPressed(c).Over(cancelBg)
	} else if cancelBtn.Hovered() {
		cancelBg = ThemeOverlayHover(c).Over(cancelBg)
	}
	cancelBtn.Background(cancelBg)
	focusRing(c, cancelBtn)
	{
		scale := motionPressScale(c, cancelBtn, "button-press-scale", cancelBtn.Pressed())
		cancelBtn.Scale(scale, scale)
	}
	if cancelBtn.Clicked() {
		uiSt.mu.Lock()
		uiSt.isFormOpen = false
		uiSt.formError = ""
		uiSt.editingServer = ""
		uiSt.editingScope = ""
		uiSt.nameInput = ""
		uiSt.transportInput = ""
		uiSt.commandInput = ""
		uiSt.argsInput = ""
		uiSt.urlInput = ""
		uiSt.envInput = ""
		uiSt.headersInput = ""
		uiSt.editBaseName = ""
		uiSt.editBaseTransport = ""
		uiSt.editBaseCommand = ""
		uiSt.editBaseArgs = ""
		uiSt.editBaseURL = ""
		uiSt.editBaseEnv = ""
		uiSt.editBaseHeaders = ""
		uiSt.mu.Unlock()
		if editingName == "" {
			closeSettingsSheet(st)
		}
		st.invalidate()
	}
	cancelBtn.Children(func() {
		ui.Text(c, "取消").
			FontSize(uiFont(TypeFootnote)).
			FontWeight(500).
			TextColor(ThemePrimaryText(c))
	})

	saveBtn := ui.ButtonBase(c.Key("btn-mcp-form-save")).
		Label("保存").
		Height(ControlMD).
		PaddingX(Space4).
		Radius(RadiusSM).
		Cursor(ui.CursorPointer)
	saveBg := ThemeAccent(c)
	if saveBtn.Pressed() {
		saveBg = ThemeAccentPressed(c)
	} else if saveBtn.Hovered() {
		saveBg = ThemeAccentHover(c)
	}
	saveBtn.Background(saveBg)
	focusRing(c, saveBtn)
	{
		scale := motionPressScale(c, saveBtn, "button-press-scale", saveBtn.Pressed())
		saveBtn.Scale(scale, scale)
	}
	if saveBtn.Clicked() {
		saveMCPCustomForm(st, uiSt)
	}
	saveBtn.Children(func() {
		ui.Text(c, "保存").
			FontSize(uiFont(TypeFootnote)).
			FontWeight(500).
			TextColor(ThemeOnAccent(c))
	})
}

func saveMCPCustomForm(st *SettingsState, uiSt *mcpPageUIState) {
	uiSt.mu.Lock()
	trimmedName := strings.TrimSpace(uiSt.nameInput)
	cmdVal := uiSt.commandInput
	rawURLVal := uiSt.urlInput
	transVal := uiSt.transportInput
	envStrVal := uiSt.envInput
	headersStrVal := uiSt.headersInput
	argsVal := uiSt.argsInput
	editingNameVal := uiSt.editingServer
	editingScopeVal := uiSt.editingScope
	uiSt.mu.Unlock()

	if trimmedName == "" {
		uiSt.mu.Lock()
		uiSt.formError = "服务名称不能为空"
		uiSt.mu.Unlock()
		st.invalidate()
		return
	}
	isStdio := !strings.EqualFold(transVal, "http") && !strings.EqualFold(transVal, "sse")
	if isStdio && strings.TrimSpace(cmdVal) == "" {
		uiSt.mu.Lock()
		uiSt.formError = "执行命令不能为空"
		uiSt.mu.Unlock()
		st.invalidate()
		return
	}
	if !isStdio && strings.TrimSpace(rawURLVal) == "" {
		uiSt.mu.Lock()
		uiSt.formError = "服务地址不能为空"
		uiSt.mu.Unlock()
		st.invalidate()
		return
	}

	envMap := make(map[string]string)
	var envKeys []string
	tokens := strings.FieldsFunc(envStrVal, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		parts := strings.SplitN(t, "=", 2)
		k := strings.TrimSpace(parts[0])
		v := ""
		if len(parts) == 2 {
			v = strings.TrimSpace(parts[1])
		}
		if k != "" {
			envMap[k] = v
			envKeys = append(envKeys, k)
		}
	}

	var argList []string
	if strings.TrimSpace(argsVal) != "" {
		argList = strings.Fields(argsVal)
	}

	// 编辑态沿用原服务的作用域，避免把项目级服务写成全局记录；新建服务仍落全局配置。
	saveScope := "global"
	if editingNameVal != "" && strings.TrimSpace(editingScopeVal) != "" {
		saveScope = editingScopeVal
	}

	srv := agentconf.MCPServer{
		Agent:     "custom",
		Scope:     saveScope,
		Name:      trimmedName,
		Transport: transVal,
		Command:   cmdVal,
		Args:      argList,
		URL:       rawURLVal,
		Env:       envMap,
		EnvKeys:   envKeys,
		Headers:   parseMCPHeaderLines(headersStrVal),
	}

	// 写盘目标与列表扫描同源：切项目后表单也不会把记录写进另一个项目的 .mcode 配置（J-07）。
	opts := st.agentConfWriteOptions()

	if err := agentconf.SaveMCPServer(opts, srv); err != nil {
		uiSt.mu.Lock()
		uiSt.formError = err.Error()
		uiSt.mu.Unlock()
		st.invalidate()
		return
	}

	// 编辑时改了名字：新记录已保存，再把旧名的凭据迁到新名并删掉旧记录。
	// 敏感字段回填为空时新记录里没有对应凭据键，所以不能直接按名字删旧记录。
	if editingNameVal != "" && editingNameVal != trimmedName {
		if err := agentconf.RenameMCPServer(opts, saveScope, editingNameVal, trimmedName); err != nil {
			uiSt.mu.Lock()
			uiSt.formError = fmt.Sprintf("新记录已保存，但迁移原名「%s」失败：%v", editingNameVal, err)
			uiSt.mu.Unlock()
			st.loadAgentConf(st.agentConfWriteOptions().ProjectDir)
			st.invalidate()
			return
		}
	}

	uiSt.mu.Lock()
	uiSt.isFormOpen = false
	uiSt.formError = ""
	uiSt.editingServer = ""
	uiSt.editingScope = ""
	uiSt.nameInput = ""
	uiSt.transportInput = ""
	uiSt.commandInput = ""
	uiSt.argsInput = ""
	uiSt.urlInput = ""
	uiSt.envInput = ""
	uiSt.headersInput = ""
	uiSt.editBaseName = ""
	uiSt.editBaseTransport = ""
	uiSt.editBaseCommand = ""
	uiSt.editBaseArgs = ""
	uiSt.editBaseURL = ""
	uiSt.editBaseEnv = ""
	uiSt.editBaseHeaders = ""
	uiSt.mu.Unlock()
	st.markSaved()
	if editingNameVal == "" {
		closeSettingsSheet(st)
	}
	st.loadAgentConf(st.agentConfWriteOptions().ProjectDir)
}

// renderMCPServerRow 渲染单条 MCP 服务详情卡片内容：顶部启用开关、默认折叠的策略行、状态徽标与操作。
func renderMCPServerRow(
	c *ui.Context,
	st *SettingsState,
	uiSt *mcpPageUIState,
	srv agentconf.MCPServer,
	index int,
	client Client,
	authBusy map[string]bool,
	authErr map[string]string,
	staticAuth map[string]bool,
	isCustom bool,
) {
	srvVal := srv
	// 逐帧拼接的 key 用直接拼接代替 fmt.Sprintf，减少每帧的格式化开销。
	mcpKey := "mcp-" + srvVal.Agent + "-" + srvVal.Scope + "-" + srvVal.Name + "-" + strconv.Itoa(index)

	uiSt.mu.Lock()
	serverKey := mcp.EndpointKey(srvVal.Agent, srvVal.Scope, srvVal.Name)
	isExpanded := uiSt.expandedServers[serverKey]
	advExpanded := uiSt.detailAdvancedExpanded[serverKey]
	tools := uiSt.toolsCache[serverKey]
	disabledFlags := uiSt.disabledTools[srvVal.Name]
	autoApproveFlags := uiSt.autoApproveTools[srvVal.Name]
	uiSt.mu.Unlock()

	if !srvVal.ReadOnly {
		allToolsAvailable := len(tools) > 0
		allApprove := len(tools) > 0
		toolNames := make([]string, 0, len(tools))
		for _, t := range tools {
			toolNames = append(toolNames, t.Name)
			if disabledFlags[t.Name] {
				allToolsAvailable = false
			}
			if !autoApproveFlags[t.Name] {
				allApprove = false
			}
		}

		SettingRow(c, mcpKey+"-row-enabled", "启用", func() {
			enabled := !srvVal.Disabled && !srvVal.Untrusted
			toggleFunc(
				func(*SettingsState) (bool, bool) { return enabled, false },
				func(st *SettingsState, newEnabled bool) {
					toggleMCPServerEnabled(st, uiSt, srvVal, newEnabled, false)
				},
			).render(c, st, setting{ID: mcpKey + "-sw", Title: "启用 " + srvVal.Name})
		})
		renderSettingsDivider(c, mcpKey+"-div-enabled")

		advToggleBtn := ui.ButtonBase(c.Key(mcpKey+"-adv-toggle")).
			Label("新会话与批量策略").
			Expanded(advExpanded).
			FillWidth().
			Padding(settingsRowPadY, settingsRowPadX).
			MinHeight(settingsRowMinHeight).
			Gap(Space4).
			AlignItems(ui.Center).
			Cursor(ui.CursorPointer)
		if advToggleBtn.Hovered() {
			advToggleBtn.Background(ThemeHover(c))
		}
		focusRing(c, advToggleBtn)
		if advToggleBtn.Clicked() {
			uiSt.mu.Lock()
			if uiSt.detailAdvancedExpanded == nil {
				uiSt.detailAdvancedExpanded = make(map[string]bool)
			}
			uiSt.detailAdvancedExpanded[serverKey] = !advExpanded
			uiSt.mu.Unlock()
			st.invalidate()
		}
		advToggleBtn.Children(func() {
			ui.Text(c, "新会话与批量策略").
				FontSize(uiFont(TypeBody)).
				FontWeight(400).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()
			angle := float32(0)
			if advExpanded {
				angle = 90
			}
			arrow := ui.Icon(c.Key(mcpKey+"-adv-arrow"), IconChevronRight()).
				Size(IconSM, IconSM).
				TextColor(ThemeSecondaryText(c)).
				Shrink(0)
			if reduceMotion(c) {
				arrow.Rotate(angle)
			} else {
				arrow.Rotate(arrow.AnimateTo(mcpKey+"-adv-arrow", angle, ui.Animation{Duration: DurFast, Ease: EaseOut}))
			}
		})

		if advExpanded {
			ui.Column(c.Key(mcpKey + "-adv-body")).
				FillWidth().
				Transition(motionCollapse(c)).
				Children(func() {
					renderSettingsDivider(c, mcpKey+"-div-adv-top")
					renderMCPNewThreadDefaultRow(c, st, uiSt, srvVal, serverKey)
					renderSettingsDivider(c, mcpKey+"-div-tools-available")

					SettingRow(c, mcpKey+"-row-tools-available", "全部工具可用", func() {
						toggleFunc(
							func(*SettingsState) (bool, bool) { return allToolsAvailable, false },
							func(st *SettingsState, on bool) {
								setMCPToolFlags(uiSt, uiSt.disabledTools, srvVal.Name, toolNames, !on)
								st.invalidate()
							},
						).withDisabled(func(*SettingsState) bool { return len(toolNames) == 0 }).
							render(c, st, setting{ID: mcpKey + "-batch-available", Title: "全部工具可用"})
					})
					renderSettingsDivider(c, mcpKey+"-div-tools-approve")

					SettingRow(c, mcpKey+"-row-tools-approve", "全部免审批", func() {
						toggleFunc(
							func(*SettingsState) (bool, bool) { return allApprove, false },
							func(st *SettingsState, on bool) {
								setMCPToolFlags(uiSt, uiSt.autoApproveTools, srvVal.Name, toolNames, on)
								st.invalidate()
							},
						).withDisabled(func(*SettingsState) bool { return len(toolNames) == 0 }).
							render(c, st, setting{ID: mcpKey + "-batch-approve", Title: "全部免审批"})
					})
				})
		}
		renderSettingsDivider(c, mcpKey+"-div-newthread")
	}

	// 授权 ID 与登录状态都从缓存读取：AuthID 需要 sha256，登录状态需要访问钥匙串，
	// 两者都不允许直接出现在逐帧渲染路径上。
	// 只读来源不渲染授权控件，不必白白读一次钥匙串。
	serverID := ""
	if srvVal.URL != "" && !srvVal.ReadOnly {
		serverID = mcpCachedAuthID(uiSt, srvVal)
	}
	signedIn := false
	if srvVal.URL != "" && !srvVal.ReadOnly {
		signedIn = mcpServerSignedIn(st, uiSt, srvVal, client)
	}

	ui.Column(c.Key(mcpKey)).
		Padding(settingsRowPadY, settingsRowPadX).
		Gap(Space1).
		Children(func() {
			// 静态请求头鉴权的服务不走 OAuth：未登录（也没在授权中）时不画登录按钮，
			// 否则用户点进去只会在「授权中…」上白等五分钟。
			staticAuthHere := srvVal.URL != "" && staticAuth[srvVal.Agent+":"+srvVal.Scope+":"+srvVal.Name]
			busy := authBusy[serverID]
			// 取消按钮与授权链接来自可选接口，实现方没提供就不显示，不影响现有登录流程。
			var authInspector mcpAuthInspector
			if inspector, ok := client.(mcpAuthInspector); ok {
				authInspector = inspector
			}
			// 授权链接与设备码都由客户端返回当前挂起的授权状态，只在等待授权期间读取。
			// 设备流也会写 authURL（便于旧界面展示），此时用设备码行取代链接行，避免两行重复。
			authLink := ""
			userCode, verifyURI, hasDevice := "", "", false
			if busy && authInspector != nil {
				authLink = strings.TrimSpace(authInspector.MCPAuthURL(serverID))
				userCode, verifyURI, hasDevice = authInspector.MCPDeviceCode(serverID)
			}

			// 徽标与操作行：作用域徽标 + 传输方式徽标 + 授权状态/按钮 + 工具展开 + 编辑
			ui.Row(c.Key(mcpKey + "-header")).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					scopeLabel := "全局"
					if strings.EqualFold(srvVal.Scope, "project") {
						scopeLabel = "项目"
					}
					Badge(c, mcpKey+"-scope", scopeLabel, ToneNeutral)

					trans := srvVal.Transport
					if trans == "" {
						if srvVal.URL != "" {
							trans = "http"
						} else {
							trans = "stdio"
						}
					}
					Badge(c, mcpKey+"-trans", trans, ToneNeutral)

					// 只读来源的标记：来自外部 Agent 配置，不可编辑、不可停用、不注入会话。
					if srvVal.ReadOnly {
						Badge(c, mcpKey+"-readonly", "只读 · 外部配置", ToneNeutral)
					}

					// 引擎侧关闭标记：Claude Code 配置里已停用该服务，仅做展示，不与 mcode 的开关联动。
					if srvVal.DisabledByEngine {
						Badge(c, mcpKey+"-engine-off", "已在 Claude Code 关闭", ToneWarning).
							Tooltip("这个服务在 Claude Code 里被关掉了（~/.claude.json 的 disabledMcpServers）。MCode 是否注入仍由这里的开关决定。")
					}

					// 对 URL 非空的远程服务器（http/sse），显示“已登录 / 未登录”及登录按钮
					// 只读来源不展示登录：凭据由外部 Agent 自己管理，mcode 代授权只会写错地方。
					if srvVal.URL != "" && !srvVal.ReadOnly {
						if staticAuthHere && !signedIn && !busy {
							Badge(c, mcpKey+"-auth-status", "请求头认证", ToneNeutral)
						} else {
							statusLabel, statusTone := "未登录", ToneWarning
							if signedIn {
								statusLabel, statusTone = "已登录", ToneSuccess
							}
							Badge(c, mcpKey+"-auth-status", statusLabel, statusTone)

							if client != nil {
								if busy {
									btn := ui.ButtonBase(c.Key(mcpKey+"-btn-busy")).
										Radius(RadiusSM).
										Padding(Space1, Space3).
										Disabled(true)
									{
										scale := motionPressScale(c, btn, "button-press-scale", false)
										btn.Scale(scale, scale)
									}
									btn.Children(func() {
										busyLabel := "授权中…"
										if hasDevice {
											busyLabel = "等待授权…"
										}
										ui.Text(c, busyLabel).
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeSecondaryText(c))
									})

									// 等待中随时可以放弃：否则用户只能干等最长五分钟。
									if authInspector != nil {
										cancelBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-auth-cancel")).
											Radius(RadiusSM).
											Padding(Space1, Space3).
											Cursor(ui.CursorPointer)
										{
											scale := motionPressScale(c, cancelBtn, "button-press-scale", cancelBtn.Pressed())
											cancelBtn.Scale(scale, scale)
										}
										if cancelBtn.Hovered() {
											cancelBtn.Background(ThemeHover(c))
										}
										focusRing(c, cancelBtn)
										if cancelBtn.Clicked() {
											authInspector.MCPCancelSignIn(serverID)
											st.invalidate()
										}
										cancelBtn.Children(func() {
											ui.Text(c, "取消").
												FontSize(uiFont(TypeFootnote)).
												TextColor(ThemeSecondaryText(c))
										})
									}
								} else if !signedIn {
									btn := ui.ButtonBase(c.Key(mcpKey+"-btn-signin")).
										Radius(RadiusSM).
										Padding(Space1, Space3).
										Cursor(ui.CursorPointer)
									{
										scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
										btn.Scale(scale, scale)
									}
									if btn.Hovered() {
										btn.Background(ThemeHover(c))
									}
									focusRing(c, btn)
									if btn.Clicked() {
										st.startMCPAuth(serverID, func() error {
											return client.MCPSignInWithAuth(context.Background(), serverID, srvVal.URL, srvVal.Auth)
										})
									}
									btn.Children(func() {
										ui.Text(c, "登录").
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeAccent(c))
									})
								} else {
									reauthBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-reauth")).
										Radius(RadiusSM).
										Padding(Space1, Space3).
										Cursor(ui.CursorPointer)
									{
										scale := motionPressScale(c, reauthBtn, "button-press-scale", reauthBtn.Pressed())
										reauthBtn.Scale(scale, scale)
									}
									if reauthBtn.Hovered() {
										reauthBtn.Background(ThemeHover(c))
									}
									focusRing(c, reauthBtn)
									if reauthBtn.Clicked() {
										st.startMCPAuth(serverID, func() error {
											return client.MCPSignInWithAuth(context.Background(), serverID, srvVal.URL, srvVal.Auth)
										})
									}
									reauthBtn.Children(func() {
										ui.Text(c, "重新授权").
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeAccent(c))
									})

									signoutBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-signout")).
										Radius(RadiusSM).
										Padding(Space1, Space3).
										Cursor(ui.CursorPointer)
									{
										scale := motionPressScale(c, signoutBtn, "button-press-scale", signoutBtn.Pressed())
										signoutBtn.Scale(scale, scale)
									}
									if signoutBtn.Hovered() {
										signoutBtn.Background(ThemeHover(c))
									}
									focusRing(c, signoutBtn)
									if signoutBtn.Clicked() {
										st.startMCPAuth(serverID, func() error {
											return client.MCPSignOut(serverID)
										})
									}
									signoutBtn.Children(func() {
										ui.Text(c, "退出").
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeSecondaryText(c))
									})
								}
							}
						}
					}

					// 弹性占位推至右侧控制按钮
					ui.Spacer(c)

					// 工具列表展开/折叠按钮
					toolsToggleBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-tools")).
						Radius(RadiusSM).
						Padding(Space1, Space3).
						Cursor(ui.CursorPointer)
					{
						scale := motionPressScale(c, toolsToggleBtn, "button-press-scale", toolsToggleBtn.Pressed())
						toolsToggleBtn.Scale(scale, scale)
					}
					if toolsToggleBtn.Hovered() {
						toolsToggleBtn.Background(ThemeHover(c))
					}
					focusRing(c, toolsToggleBtn)
					if toolsToggleBtn.Clicked() {
						uiSt.mu.Lock()
						newExp := !uiSt.expandedServers[serverKey]
						uiSt.expandedServers[serverKey] = newExp
						uiSt.mu.Unlock()
						st.invalidate()

						if newExp {
							startMCPToolProbe(st, uiSt, srvVal)
						}
					}
					toolsToggleBtn.Children(func() {
						btnText := "展开工具"
						if isExpanded {
							btnText = "收起工具"
						}
						ui.Text(c, btnText).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
					})

					// 编辑只适用于自定义服务。
					if isCustom && !srvVal.ReadOnly {
						editBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-edit")).
							Radius(RadiusSM).
							Padding(Space1, Space3).
							Cursor(ui.CursorPointer)
						{
							scale := motionPressScale(c, editBtn, "button-press-scale", editBtn.Pressed())
							editBtn.Scale(scale, scale)
						}
						if editBtn.Hovered() {
							editBtn.Background(ThemeHover(c))
						}
						focusRing(c, editBtn)
						if editBtn.Clicked() {
							// 列表扫描不带凭据，编辑前按 Agent/Scope/Name 重新读一次带凭据的完整配置，
							// 否则敏感环境变量与 Authorization 头回填为空，保存时会把凭据库里的值覆盖掉。
							editSrv := srvVal
							// 扫描参数与列表同源，切项目后带回凭据的也是当前展示的那一份（J-07）。
							credOpts := st.agentConfWriteOptions()
							credOpts.WithCredentials = true
							if fullList, err := scanMCP(credOpts); err == nil {
								for _, cand := range fullList {
									if cand.Agent == srvVal.Agent && cand.Scope == srvVal.Scope && cand.Name == srvVal.Name {
										editSrv = cand
										break
									}
								}
							}
							uiSt.mu.Lock()
							uiSt.isFormOpen = true
							uiSt.editingServer = srvVal.Name
							uiSt.editingScope = srvVal.Scope
							uiSt.nameInput = srvVal.Name
							uiSt.transportInput = editSrv.Transport
							if uiSt.transportInput == "" {
								if editSrv.URL != "" {
									uiSt.transportInput = "http"
								} else {
									uiSt.transportInput = "stdio"
								}
							}
							uiSt.commandInput = editSrv.Command
							uiSt.argsInput = strings.Join(editSrv.Args, " ")
							uiSt.urlInput = editSrv.URL
							var envParts []string
							for _, k := range editSrv.EnvKeys {
								envParts = append(envParts, fmt.Sprintf("%s=%s", k, editSrv.Env[k]))
							}
							uiSt.envInput = strings.Join(envParts, ", ")
							var headerParts []string
							for k, v := range editSrv.Headers {
								headerParts = append(headerParts, fmt.Sprintf("%s: %s", k, v))
							}
							sort.Strings(headerParts)
							uiSt.headersInput = strings.Join(headerParts, ", ")
							uiSt.editBaseName = uiSt.nameInput
							uiSt.editBaseTransport = uiSt.transportInput
							uiSt.editBaseCommand = uiSt.commandInput
							uiSt.editBaseArgs = uiSt.argsInput
							uiSt.editBaseURL = uiSt.urlInput
							uiSt.editBaseEnv = uiSt.envInput
							uiSt.editBaseHeaders = uiSt.headersInput
							uiSt.formError = ""
							uiSt.mu.Unlock()
							st.invalidate()
						}
						editBtn.Children(func() {
							ui.Text(c, "编辑").
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c))
						})
					}
				})

			// 详情展示（stdio 命令参数或远程 URL）
			detailLine := ""
			if strings.EqualFold(srvVal.Transport, "http") || strings.EqualFold(srvVal.Transport, "sse") || srvVal.URL != "" {
				detailLine = srvVal.URL
			} else {
				detailLine = srvVal.Command
				if len(srvVal.Args) > 0 {
					detailLine += " " + strings.Join(srvVal.Args, " ")
				}
			}
			if detailLine != "" {
				ui.Text(c, detailLine).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 环境变量提示
			if len(srvVal.EnvKeys) > 0 {
				ui.Text(c, "环境变量："+strings.Join(srvVal.EnvKeys, ", ")).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 仓库内配置未信任时提示用户：启用开关即代表确认信任该命令
			if srvVal.Untrusted {
				ui.Text(c, "来自仓库配置，未信任：启用即表示信任该命令").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 配置文件路径
			if srvVal.Source != "" {
				ui.Text(c, srvVal.Source).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c).Alpha(0.8)).
					SingleLine()
			}

			// 授权中把链接摆出来：用户要自己到浏览器里完成授权，只写「授权中…」等于干等。
			// 设备流已在下面单独展示验证码，这里隐藏链接行避免重复。
			if authLink != "" && !hasDevice {
				ui.Row(c.Key(mcpKey + "-auth-link-row")).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, mcpAuthLinkDisplay(authLink)).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()

						label := mcpAuthURLCopyLabel(c, uiSt, serverID, "复制链接", "已复制")
						CopyButton(c, CopyButtonProps{
							Key: mcpKey + "-btn-auth-copy", Text: label, Mode: CopyText, Copied: label == "已复制",
							Value:  func() string { return authLink },
							OnCopy: func() { markMCPAuthURLCopied(st, uiSt, serverID, c.Now()) },
						})
					})
			}

			// 设备流把验证码直接摆在行内：用户要在别的设备上输入，只写「等待授权…」等于干等。
			// 验证码用等宽字体；复制回执的缓存键加 #device，与链接复制互不影响。
			if hasDevice {
				ui.Row(c.Key(mcpKey + "-device-code-row")).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, "验证码").
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c))

						ui.Text(c, userCode).
							FontSize(uiFont(TypeBody)).
							Font(codeFontFamily(st.cfg.Appearance)).
							TextColor(ThemePrimaryText(c)).
							SingleLine()

						ui.Text(c, "在 "+verifyURI+" 输入").
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()

						label := mcpAuthURLCopyLabel(c, uiSt, serverID+"#device", "复制验证码", "已复制")
						CopyButton(c, CopyButtonProps{
							Key: mcpKey + "-btn-device-copy", Text: label, Mode: CopyText, Copied: label == "已复制",
							Value:  func() string { return userCode },
							OnCopy: func() { markMCPAuthURLCopied(st, uiSt, serverID+"#device", c.Now()) },
						})
					})
			}

			// 错误信息展示：授权失败的原因（含上游 401 报文）通常很长，
			// 不能 SingleLine 截断，靠父列宽度自然换行，让用户能读全。
			if authErr != nil {
				if errStr, ok := authErr[serverID]; ok && errStr != "" {
					ui.Text(c, errStr).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeDanger(c))
				}
			}
		})
}

// renderMCPServerToolsSection 在详情卡片下方渲染工具明细列表。
func renderMCPServerToolsSection(c *ui.Context, st *SettingsState, uiSt *mcpPageUIState, srv agentconf.MCPServer, index int) {
	srvVal := srv
	mcpKey := "mcp-" + srvVal.Agent + "-" + srvVal.Scope + "-" + srvVal.Name + "-" + strconv.Itoa(index)

	uiSt.mu.Lock()
	serverKey := mcp.EndpointKey(srvVal.Agent, srvVal.Scope, srvVal.Name)
	tools, toolsProbed := uiSt.toolsCache[serverKey]
	isLoadingTools := uiSt.toolsLoading[serverKey]
	toolsErr := uiSt.toolsErr[serverKey]
	uiSt.mu.Unlock()

	renderSettingsSection(c, "sec-mcp-tools", "", func() {
		ui.Column(c.Key(mcpKey+"-tools-panel")).
			Padding(settingsRowPadY, settingsRowPadX).
			Gap(Space1).
			Children(func() {
				if isLoadingTools {
					LoadingIndicator(c, mcpKey+"-tools-loading", "正在探测工具列表…", IconSM)
				} else if len(tools) > 0 {
					uiSt.mu.Lock()
					disabledFlags := uiSt.disabledTools[srvVal.Name]
					autoApproveFlags := uiSt.autoApproveTools[srvVal.Name]
					uiSt.mu.Unlock()

					ui.Row(c.Key(mcpKey + "-tools-header-row")).
						FillWidth().
						Gap(Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, fmt.Sprintf("支持的工具 (%d):", len(tools))).
								FontSize(uiFont(TypeFootnote)).
								FontWeight(500).
								TextColor(ThemePrimaryText(c))
							ui.Spacer(c)

							if srvVal.ReadOnly {
								return
							}
							mcpToolColumn(c, mcpKey+"-col-disable", func() {
								ui.Text(c, "禁用").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c)).
									Tooltip(settingHint("mcp-tool-disable"))
							})
							mcpToolColumn(c, mcpKey+"-col-approve", func() {
								ui.Text(c, "免审批").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c)).
									Tooltip(settingHint("mcp-tool-approve"))
							})
						})

					for tIdx, t := range tools {
						toolKey := mcpKey + "-tool-" + strconv.Itoa(tIdx)
						toolDisabled := disabledFlags[t.Name]
						toolAutoApprove := autoApproveFlags[t.Name]
						ui.Row(c.Key(toolKey)).
							FillWidth().
							Gap(Space3).
							AlignItems(ui.Center).
							Children(func() {
								ui.Text(c, "• "+t.Name).
									FontSize(uiFont(TypeFootnote)).
									FontWeight(500).
									TextColor(ThemePrimaryText(c))
								if t.Description != "" {
									ui.Text(c, "- "+t.Description).
										FontSize(uiFont(TypeFootnote)).
										TextColor(ThemeSecondaryText(c)).
										SingleLine()
								}

								ui.Spacer(c)

								if srvVal.ReadOnly {
									return
								}
								mcpToolColumn(c, toolKey+"-disable-col", func() {
									toggleFunc(
										func(*SettingsState) (bool, bool) { return toolDisabled, false },
										func(st *SettingsState, on bool) {
											setMCPToolFlag(uiSt, uiSt.disabledTools, srvVal.Name, t.Name, on)
											st.invalidate()
										},
									).render(c, st, setting{ID: toolKey + "-disable", Title: "禁用 " + t.Name})
								})

								mcpToolColumn(c, toolKey+"-approve-col", func() {
									toggleFunc(
										func(*SettingsState) (bool, bool) { return toolAutoApprove, false },
										func(st *SettingsState, on bool) {
											setMCPToolFlag(uiSt, uiSt.autoApproveTools, srvVal.Name, t.Name, on)
											st.invalidate()
										},
									).render(c, st, setting{ID: toolKey + "-approve", Title: "免审批 " + t.Name})
								})
							})
					}
				} else if toolsErr != "" {
					ui.Text(c, "探测失败："+toolsErr).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeDanger(c))
					ui.Row(c.Key(mcpKey + "-tools-retry-row")).
						Children(func() {
							retryBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-tools-retry")).
								Label("重新检测").
								Radius(RadiusSM).
								Padding(Space1, Space3).
								Cursor(ui.CursorPointer)
							{
								scale := motionPressScale(c, retryBtn, "button-press-scale", retryBtn.Pressed())
								retryBtn.Scale(scale, scale)
							}
							if retryBtn.Hovered() {
								retryBtn.Background(ThemeHover(c))
							}
							focusRing(c, retryBtn)
							if retryBtn.Clicked() {
								resetMCPToolProbe(uiSt, serverKey)
								startMCPToolProbe(st, uiSt, srvVal)
								st.invalidate()
							}
							retryBtn.Children(func() {
								ui.Text(c, "重新检测").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeAccent(c))
							})
						})
				} else if toolsProbed {
					ui.Text(c, "无可用工具").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c))
				} else {
					ui.Text(c, "连接后可见").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c))
				}
			})
	})
}

const mcpToolColWidth = float32(52)

// mcpToolColumn 为工具列表的表头、批量行与各工具行提供固定宽度的居中列，保证开关与表头纵向对齐。
func mcpToolColumn(c *ui.Context, key string, content func()) ui.Element {
	return ui.Row(c.Key(key)).
		Width(mcpToolColWidth).
		Shrink(0).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Children(content)
}

// setMCPToolFlag 更新某服务下某工具的开关状态，并把整份策略持久化到文件。
