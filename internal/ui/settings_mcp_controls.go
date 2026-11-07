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
	if on {
		uiSt.foldThreshold = 0
	} else {
		uiSt.foldThreshold = -1
	}
	saveMCPToolPolicyLocked(uiSt)
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

// renderMCPNewThreadDefaultsSection 渲染「新会话默认」小节：每个可注入服务一行开关，
// 关掉的服务在新建会话时默认关闭。只读来源不会注入会话，不列出。
func renderMCPNewThreadDefaultsBody(c *ui.Context, st *SettingsState) {
	snap := mcpPageSnapshot(st)
	uiSt, servers := snap.uiSt, snap.servers
	visible := make([]agentconf.MCPServer, 0, len(servers))
	uiSt.mu.Lock()
	selected := uiSt.selectedServer
	uiSt.mu.Unlock()
	for _, srv := range servers {
		if srv.ReadOnly || mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name) != selected {
			continue
		}
		visible = append(visible, srv)
	}
	if len(visible) == 0 {
		return
	}

	// 渲染路径只读缓存快照，避免逐帧读盘；快照在读锁内拷一份，供后面的开关回调使用。
	uiSt.mu.Lock()
	disabled := make(map[string]bool, len(uiSt.newThreadDisabled))
	for k, v := range uiSt.newThreadDisabled {
		disabled[k] = v
	}
	saveErr := uiSt.newThreadDisabledErr
	uiSt.mu.Unlock()

	func() {
		if saveErr != "" {
			ui.Text(c, saveErr).
				FontSize(TypeFootnote).
				TextColor(ThemeDanger(c)).
				Margin(0, Space5, Space3, Space5)
		}

		for i, srv := range visible {
			if i > 0 {
				renderSettingsDivider(c, "div-mcp-newthread-"+strconv.Itoa(i))
			}
			key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
			enabled := !disabled[key]
			// 行 key 与服务键都带序号，避免不同来源的同名服务互相覆盖。
			srvKey := key + "-" + strconv.Itoa(i)
			renderSettingRow(c, "row-mcp-newthread-"+srvKey, "新会话默认启用", func() {
				toggleFunc(
					func(*SettingsState) (bool, bool) { return enabled, false },
					func(st *SettingsState, on bool) {
						opts := st.agentConfWriteOptions()
						uiSt.mu.Lock()
						if on {
							delete(uiSt.newThreadDisabled, key)
						} else {
							if uiSt.newThreadDisabled == nil {
								uiSt.newThreadDisabled = make(map[string]bool)
							}
							uiSt.newThreadDisabled[key] = true
						}
						saveMCPNewThreadDisabledLocked(uiSt, opts)
						uiSt.mu.Unlock()
						st.invalidate()
					},
				).render(c, st, setting{ID: "row-mcp-newthread-" + srvKey, Title: srv.Name})
			})
		}
	}()
}

// renderMCPCustomForm 渲染新增/编辑自定义 MCP 服务的表单。
func renderMCPCustomForm(c *ui.Context, st *SettingsState, uiSt *mcpPageUIState) {
	uiSt.mu.Lock()
	editingName := uiSt.editingServer
	trans := uiSt.transportInput
	formErr := uiSt.formError
	uiSt.mu.Unlock()

	ui.Column(c.Key("mcp-form-container")).
		Gap(Space3).
		Children(func() {
			formTitle := "添加 MCP 服务"
			if editingName != "" {
				formTitle = fmt.Sprintf("编辑 MCP 服务: %s", editingName)
			}
			ui.Text(c, formTitle).
				Margin(0, settingsRowPadX).
				FontSize(TypeProse).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))

			if formErr != "" {
				ui.Text(c, formErr).
					Margin(0, settingsRowPadX).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			}

			// 1. 服务名称
			nameRowKey := "row-form-name"
			renderStackedSettingRow(c, nameRowKey, "服务名称", func() {
				get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.nameInput })
				textFunc(get, set, "例如: sqlite-server").render(c, st, setting{ID: nameRowKey, Title: "服务名称"})
			})

			// 2. 传输方式（本地命令 stdio vs 远程地址 http）
			ui.Row(c.Key("row-form-transport")).
				Padding(0, settingsRowPadX).
				AlignItems(ui.Center).
				Gap(Space4).
				Children(func() {
					ui.Text(c, "传输方式:").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c)).
						Width(80)

					stdioBtn := ui.ButtonBase(c.Key("btn-form-trans-stdio")).
						Radius(RadiusXS).
						Padding(Space1, Space4)
					if strings.EqualFold(trans, "stdio") {
						stdioBtn.Background(ThemeOverlay(c, 0.15))
					} else {
						stdioBtn.Background(ThemeOverlay(c, 0.04))
					}
					if stdioBtn.Clicked() {
						uiSt.mu.Lock()
						uiSt.transportInput = "stdio"
						uiSt.mu.Unlock()
						st.invalidate()
					}
					stdioBtn.Children(func() {
						ui.Text(c, "本地命令 (stdio)").FontSize(TypeFootnote)
					})

					httpBtn := ui.ButtonBase(c.Key("btn-form-trans-http")).
						Radius(RadiusXS).
						Padding(Space1, Space4)
					if strings.EqualFold(trans, "http") || strings.EqualFold(trans, "sse") {
						httpBtn.Background(ThemeOverlay(c, 0.15))
					} else {
						httpBtn.Background(ThemeOverlay(c, 0.04))
					}
					if httpBtn.Clicked() {
						uiSt.mu.Lock()
						uiSt.transportInput = "http"
						uiSt.mu.Unlock()
						st.invalidate()
					}
					httpBtn.Children(func() {
						ui.Text(c, "远程地址 (http/sse)").FontSize(TypeFootnote)
					})
				})

			// 3. 模式对应输入项
			if strings.EqualFold(trans, "http") || strings.EqualFold(trans, "sse") {
				urlRowKey := "row-form-url"
				renderStackedSettingRow(c, urlRowKey, "服务地址", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.urlInput })
					textFunc(get, set, "例如: https://mcp.example.com/sse").render(c, st, setting{ID: urlRowKey, Title: "服务地址"})
				})

				headersRowKey := "row-form-headers"
				renderStackedSettingRow(c, headersRowKey, "请求头", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.headersInput })
					textFunc(get, set, "每行一个 KEY: VALUE，Authorization 等密钥自动加密存凭据库").
						withSecret().
						render(c, st, setting{ID: headersRowKey, Title: "请求头"})
				})
			} else {
				commandRowKey := "row-form-command"
				renderStackedSettingRow(c, commandRowKey, "执行命令", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.commandInput })
					textFunc(get, set, "例如: npx 或 /usr/local/bin/server").render(c, st, setting{ID: commandRowKey, Title: "执行命令"})
				})

				argsRowKey := "row-form-args"
				renderStackedSettingRow(c, argsRowKey, "运行参数", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.argsInput })
					textFunc(get, set, "空格分隔，例如: -y @modelcontextprotocol/server-everything").render(c, st, setting{ID: argsRowKey, Title: "运行参数"})
				})

				envRowKey := "row-form-env"
				renderStackedSettingRow(c, envRowKey, "环境变量", func() {
					get, set := mcpFormTextField(func(s *mcpPageUIState) *string { return &s.envInput })
					textFunc(get, set, "逗号或换行分隔 KEY=VALUE，含 TOKEN/KEY/SECRET 自动加密存凭据库").
						withSecret().
						render(c, st, setting{ID: envRowKey, Title: "环境变量"})
				})
			}

			// 底部操作按钮
			ui.Row(c.Key("row-form-actions")).
				Padding(0, settingsRowPadX).
				AlignItems(ui.Center).
				Gap(Space4).
				Margin(Space2, 0, 0, 0).
				Children(func() {
					saveBtn := ui.ButtonBase(c.Key("btn-mcp-form-save")).
						Radius(RadiusSM).
						Padding(Space1, Space4).
						Background(ThemeAccent(c))
					if saveBtn.Clicked() {
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
						uiSt.mu.Unlock()
						if editingNameVal == "" {
							closeSettingsSheet(st)
						}
						st.loadAgentConf(st.agentConfWriteOptions().ProjectDir)
					}
					saveBtn.Children(func() {
						ui.Text(c, "保存").
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemeOnAccent(c))
					})

					cancelBtn := ui.ButtonBase(c.Key("btn-mcp-form-cancel")).
						Radius(RadiusSM).
						Padding(Space1, Space4).
						Background(ThemeOverlay(c, 0.06))
					if cancelBtn.Clicked() {
						uiSt.mu.Lock()
						uiSt.isFormOpen = false
						uiSt.formError = ""
						uiSt.mu.Unlock()
						if editingName == "" {
							closeSettingsSheet(st)
						}
						st.invalidate()
					}
					cancelBtn.Children(func() {
						ui.Text(c, "取消").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					})
				})
		})
}

// renderMCPServerRow 渲染单条 MCP 服务详情行，包含状态徽标、启用开关、工具列表展开、编辑与删除。
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
	tools := uiSt.toolsCache[serverKey]
	isLoadingTools := uiSt.toolsLoading[serverKey]
	toolsErr := uiSt.toolsErr[serverKey]
	uiSt.mu.Unlock()

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

			// 首行：名称 + 作用域徽标 + 传输方式徽标 + 授权状态/按钮 + 工具展开 + 操作 + 启停开关
			ui.Row(c.Key(mcpKey + "-header")).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Text(c, srvVal.Name).
						FontSize(TypeProse).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine()

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
						Badge(c, mcpKey+"-engine-off", "已在 Claude Code 关闭", ToneWarning)
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
									btn.Children(func() {
										busyLabel := "授权中…"
										if hasDevice {
											busyLabel = "等待授权…"
										}
										ui.Text(c, busyLabel).
											FontSize(TypeFootnote).
											TextColor(ThemeSecondaryText(c))
									})

									// 等待中随时可以放弃：否则用户只能干等最长五分钟。
									if authInspector != nil {
										cancelBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-auth-cancel")).
											Radius(RadiusSM).
											Padding(Space1, Space3)
										if cancelBtn.Hovered() {
											cancelBtn.Background(ThemeHover(c))
										}
										if cancelBtn.Clicked() {
											authInspector.MCPCancelSignIn(serverID)
											st.invalidate()
										}
										cancelBtn.Children(func() {
											ui.Text(c, "取消").
												FontSize(TypeFootnote).
												TextColor(ThemeSecondaryText(c))
										})
									}
								} else if !signedIn {
									btn := ui.ButtonBase(c.Key(mcpKey+"-btn-signin")).
										Radius(RadiusSM).
										Padding(Space1, Space3)
									if btn.Hovered() {
										btn.Background(ThemeHover(c))
									}
									if btn.Clicked() {
										st.startMCPAuth(serverID, func() error {
											return client.MCPSignInWithAuth(context.Background(), serverID, srvVal.URL, srvVal.Auth)
										})
									}
									btn.Children(func() {
										ui.Text(c, "登录").
											FontSize(TypeFootnote).
											TextColor(ThemeAccent(c))
									})
								} else {
									reauthBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-reauth")).
										Radius(RadiusSM).
										Padding(Space1, Space3)
									if reauthBtn.Hovered() {
										reauthBtn.Background(ThemeHover(c))
									}
									if reauthBtn.Clicked() {
										st.startMCPAuth(serverID, func() error {
											return client.MCPSignInWithAuth(context.Background(), serverID, srvVal.URL, srvVal.Auth)
										})
									}
									reauthBtn.Children(func() {
										ui.Text(c, "重新授权").
											FontSize(TypeFootnote).
											TextColor(ThemeAccent(c))
									})

									signoutBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-signout")).
										Radius(RadiusSM).
										Padding(Space1, Space3)
									if signoutBtn.Hovered() {
										signoutBtn.Background(ThemeHover(c))
									}
									if signoutBtn.Clicked() {
										st.startMCPAuth(serverID, func() error {
											return client.MCPSignOut(serverID)
										})
									}
									signoutBtn.Children(func() {
										ui.Text(c, "退出").
											FontSize(TypeFootnote).
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
						Padding(Space1, Space3)
					if toolsToggleBtn.Hovered() {
						toolsToggleBtn.Background(ThemeHover(c))
					}
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
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					})

					// 自定义服务专属的 编辑 与 删除 按钮；只读来源不允许改，按钮一并隐藏。
					if isCustom && !srvVal.ReadOnly {
						editBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-edit")).
							Radius(RadiusSM).
							Padding(Space1, Space3)
						if editBtn.Hovered() {
							editBtn.Background(ThemeHover(c))
						}
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
							uiSt.formError = ""
							uiSt.mu.Unlock()
							st.invalidate()
						}
						editBtn.Children(func() {
							ui.Text(c, "编辑").
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						})

						delBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-delete")).
							Radius(RadiusSM).
							Padding(Space1, Space3)
						if delBtn.Hovered() {
							delBtn.Background(ThemeHover(c))
						}
						if delBtn.Clicked() {
							// 删除会连同凭据库里的条目一起清掉且不可撤销，先弹确认框（S-076）。
							uiSt.mu.Lock()
							uiSt.deleteConfirmServer = srvVal.Name
							uiSt.deleteConfirmScope = srvVal.Scope
							uiSt.mu.Unlock()
							st.invalidate()
						}
						delBtn.Children(func() {
							ui.Text(c, "删除").
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c))
						})
					}

					// 启用/停用开关；只读来源不可停用，不渲染开关（开关是本行最后一个控件，直接返回）。
					if srvVal.ReadOnly {
						return
					}
					enabled := !srvVal.Disabled
					toggleFunc(
						func(*SettingsState) (bool, bool) { return enabled, false },
						func(st *SettingsState, newEnabled bool) {
							// 启停写盘与列表扫描同源，避免切项目后改到另一个项目（J-07）。
							opts := st.agentConfWriteOptions()
							// 写盘失败时保持内存里的旧状态：先乐观改内存会让界面谎报成功，
							// 而开关下次重建又弹回去，用户看不出到底存了什么。
							if err := agentconf.SetMCPServerEnabledFor(opts, srvVal, newEnabled); err != nil {
								st.mu.Lock()
								st.errorMessage = fmt.Sprintf("更新 MCP 服务 %s 的启用状态失败：%v", srvVal.Name, err)
								st.mu.Unlock()
								st.invalidate()
								return
							}
							st.mu.Lock()
							st.errorMessage = ""
							for idx := range st.mcpServers {
								item := &st.mcpServers[idx]
								if item.Name != srvVal.Name {
									continue
								}
								if newEnabled && item.Agent == srvVal.Agent && item.Source == srvVal.Source {
									// 启用即视为信任这一条仓库内命令，清掉未信任标记与探测错误。
									item.Untrusted = false
								}
								// 其它来源的同名仓库服务没有被信任，仍保持禁用。
								item.Disabled = !newEnabled || item.Untrusted
							}
							st.mu.Unlock()
							if newEnabled {
								resetMCPToolProbe(uiSt, serverKey)
							}
							st.invalidate()
						},
					).render(c, st, setting{ID: mcpKey + "-sw", Title: "启用 " + srvVal.Name})
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
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 环境变量提示
			if len(srvVal.EnvKeys) > 0 {
				ui.Text(c, "环境变量："+strings.Join(srvVal.EnvKeys, ", ")).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 仓库内配置未信任时提示用户：启用开关即代表确认信任该命令
			if srvVal.Untrusted {
				ui.Text(c, "来自仓库配置，未信任：启用即表示信任该命令").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 配置文件路径
			if srvVal.Source != "" {
				ui.Text(c, srvVal.Source).
					FontSize(TypeCaption).
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
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()

						copyBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-auth-copy")).
							Radius(RadiusSM).
							Padding(Space0, Space2)
						if copyBtn.Hovered() {
							copyBtn.Background(ThemeHover(c))
						}
						if copyBtn.Clicked() {
							c.WriteClipboard(authLink)
							markMCPAuthURLCopied(st, uiSt, serverID, c.Now())
						}
						copyBtn.Children(func() {
							ui.Text(c, mcpAuthURLCopyLabel(c, uiSt, serverID, "复制链接", "已复制")).
								FontSize(TypeCaption).
								TextColor(ThemeAccent(c))
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
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))

						ui.Text(c, userCode).
							FontSize(TypeBody).
							Font(codeFontFamily(st.cfg.Appearance)).
							TextColor(ThemePrimaryText(c)).
							SingleLine()

						ui.Text(c, "在 "+verifyURI+" 输入").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()

						copyBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-device-copy")).
							Radius(RadiusSM).
							Padding(Space0, Space2)
						if copyBtn.Hovered() {
							copyBtn.Background(ThemeHover(c))
						}
						if copyBtn.Clicked() {
							c.WriteClipboard(userCode)
							markMCPAuthURLCopied(st, uiSt, serverID+"#device", c.Now())
						}
						copyBtn.Children(func() {
							ui.Text(c, mcpAuthURLCopyLabel(c, uiSt, serverID+"#device", "复制验证码", "已复制")).
								FontSize(TypeCaption).
								TextColor(ThemeAccent(c))
						})
					})
			}

			// 错误信息展示：授权失败的原因（含上游 401 报文）通常很长，
			// 不能 SingleLine 截断，靠父列宽度自然换行，让用户能读全。
			if authErr != nil {
				if errStr, ok := authErr[serverID]; ok && errStr != "" {
					ui.Text(c, errStr).
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c))
				}
			}

			// 展开的工具列表面板
			if isExpanded {
				ui.Column(c.Key(mcpKey+"-tools-panel")).
					Padding(settingsRowPadY, settingsRowPadX).
					Margin(Space1, 0, 0, 0).
					Radius(RadiusSM).
					Gap(Space1).
					Background(ThemeOverlay(c, 0.04)).
					Children(func() {
						// 引擎侧已关闭的说明：只解释原因，不改变这里的开关行为。
						if srvVal.DisabledByEngine {
							ui.Text(c, "这个服务在 Claude Code 里被关掉了（~/.claude.json 的 disabledMcpServers）。MCode 是否注入仍由这里的开关决定。").
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						}
						if isLoadingTools {
							ui.Text(c, "正在探测工具列表…").
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						} else if len(tools) > 0 {
							// 工具开关状态一次性取出，避免每个工具每帧加两次锁；
							// 批量开关的初始值也要据此推导，所以提前到标题行之前。
							uiSt.mu.Lock()
							disabledFlags := uiSt.disabledTools[srvVal.Name]
							autoApproveFlags := uiSt.autoApproveTools[srvVal.Name]
							uiSt.mu.Unlock()

							// 批量开关的初值由当前状态推导：全部工具都已开关才显示为开。
							allDisabled := true
							allApprove := true
							toolNames := make([]string, 0, len(tools))
							for _, t := range tools {
								toolNames = append(toolNames, t.Name)
								if !disabledFlags[t.Name] {
									allDisabled = false
								}
								if !autoApproveFlags[t.Name] {
									allApprove = false
								}
							}

							ui.Row(c.Key(mcpKey + "-tools-batch-row")).
								Gap(Space3).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, fmt.Sprintf("支持的工具 (%d):", len(tools))).
										FontSize(TypeFootnote).
										FontWeight(500).
										TextColor(ThemePrimaryText(c))
									ui.Spacer(c)

									if srvVal.ReadOnly {
										return
									}
									ui.Text(c, "全部禁用").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
									toggleFunc(
										func(*SettingsState) (bool, bool) { return allDisabled, false },
										func(st *SettingsState, on bool) {
											setMCPToolFlags(uiSt, uiSt.disabledTools, srvVal.Name, toolNames, on)
											st.invalidate()
										},
									).render(c, st, setting{ID: mcpKey + "-batch-disable", Title: "全部禁用"})

									ui.Text(c, "全部免审批").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
									toggleFunc(
										func(*SettingsState) (bool, bool) { return allApprove, false },
										func(st *SettingsState, on bool) {
											setMCPToolFlags(uiSt, uiSt.autoApproveTools, srvVal.Name, toolNames, on)
											st.invalidate()
										},
									).render(c, st, setting{ID: mcpKey + "-batch-approve", Title: "全部免审批"})
								})

							for tIdx, t := range tools {
								toolKey := mcpKey + "-tool-" + strconv.Itoa(tIdx)
								toolDisabled := disabledFlags[t.Name]
								toolAutoApprove := autoApproveFlags[t.Name]
								ui.Row(c.Key(toolKey)).
									Gap(Space3).
									Children(func() {
										ui.Text(c, "• "+t.Name).
											FontSize(TypeFootnote).
											FontWeight(500).
											TextColor(ThemePrimaryText(c))
										if t.Description != "" {
											ui.Text(c, "- "+t.Description).
												FontSize(TypeFootnote).
												TextColor(ThemeSecondaryText(c)).
												SingleLine()
										}

										ui.Spacer(c)

										if srvVal.ReadOnly {
											return
										}
										ui.Text(c, "禁用").
											FontSize(TypeFootnote).
											TextColor(ThemeSecondaryText(c))
										toggleFunc(
											func(*SettingsState) (bool, bool) { return toolDisabled, false },
											func(st *SettingsState, on bool) {
												setMCPToolFlag(uiSt, uiSt.disabledTools, srvVal.Name, t.Name, on)
												st.invalidate()
											},
										).render(c, st, setting{ID: toolKey + "-disable", Title: "禁用 " + t.Name})

										ui.Text(c, "免审批").
											FontSize(TypeFootnote).
											TextColor(ThemeSecondaryText(c))
										toggleFunc(
											func(*SettingsState) (bool, bool) { return toolAutoApprove, false },
											func(st *SettingsState, on bool) {
												setMCPToolFlag(uiSt, uiSt.autoApproveTools, srvVal.Name, t.Name, on)
												st.invalidate()
											},
										).render(c, st, setting{ID: toolKey + "-approve", Title: "免审批 " + t.Name})
									})
							}
						} else if toolsErr != "" {
							// 探测失败（含认证失败、命令跑不起来）不再笼统写成「连接后可见」，
							// 否则用户不知道是自己没登录还是服务本身坏了。
							ui.Text(c, "探测失败："+toolsErr).
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c))
							ui.Row(c.Key(mcpKey + "-tools-retry-row")).
								Children(func() {
									retryBtn := ui.ButtonBase(c.Key(mcpKey+"-btn-tools-retry")).
										Label("重新检测").
										Radius(RadiusSM).
										Padding(Space1, Space3)
									if retryBtn.Hovered() {
										retryBtn.Background(ThemeHover(c))
									}
									if retryBtn.Clicked() {
										resetMCPToolProbe(uiSt, serverKey)
										startMCPToolProbe(st, uiSt, srvVal)
										st.invalidate()
									}
									retryBtn.Children(func() {
										ui.Text(c, "重新检测").
											FontSize(TypeFootnote).
											TextColor(ThemeAccent(c))
									})
								})
						} else {
							ui.Text(c, "连接后可见").
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						}
					})
			}
		})
}

// setMCPToolFlag 更新某服务下某工具的开关状态，并把整份策略持久化到文件。
