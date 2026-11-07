package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/multiaccount"
)

// agyPageState 维护设置面板中 agy 账号管理页面的全部交互状态。
type agyPageState struct {
	loading       bool
	loaded        bool
	err           string
	accounts      []accounts.Account
	defaultID     string
	routing       multiaccount.Config
	routingLoaded bool
	// localLogin 表示宿主环境已有 agy 登录凭证（钥匙串或 antigravity-oauth-token）。
	// 账号管理器未接线（例如以 memory provider 启动）时，界面据此显示已登录而不是「未登录」。
	localLogin bool

	// 顶部刷新额度状态
	refreshBusy bool
	refreshErr  string

	// 预热状态
	warmupBusy       bool              // 批量预热中
	warmupErr        string            // 批量预热错误
	warmupBusyIDs    map[string]bool   // 单账号预热中 (id -> bool)
	warmupRowErr     map[string]string // 单账号预热错误 (id -> 错误信息)
	autoWarmup       bool              // 自动预热开关状态
	autoWarmupLoaded bool              // 自动预热是否已加载
	autoWarmupBusy   bool              // 自动预热开关切换中
	autoWarmupErr    string            // 自动预热开关错误

	// 删除与行级操作状态
	deleteConfirmID string            // 正在等待二次确认的账号 ID
	deleteBoundInfo string            // 确认文案中的会话绑定提示
	deleteBusyID    string            // 正在异步删除的账号 ID
	deleteErr       map[string]string // 账号 ID -> 删除错误信息
	rowErr          map[string]string // 账号 ID -> 启用/设为默认等单项错误

	// 浏览器登录状态
	loginBrowserBusy bool
	loginBrowserURL  string
	loginInputCode   string
	loginBrowserErr  string
	loginCancel      context.CancelFunc

	// 终端登录状态
	terminalLoginID   string
	terminalLoginEnv  []string
	terminalLoginArgs []string
	terminalLoginBusy bool
	terminalLoginErr  string

	// clipboardKey/clipboardAt 记录最近一次复制的目标与时间，复制按钮据此短暂显示「已复制」。
	clipboardKey string
	clipboardAt  time.Time

	// 路由配置保存错误
	routingErr  string
	routingBusy bool

	// dispatchOpen 表示 agy 行展开区里的「账号调度」子区是否展开，默认收起。
	dispatchOpen bool
}

// agyClient 获取关联的 Client 实例；未关联时返回 nil。
func (st *SettingsState) agyClient() Client {
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil {
		return chat.client
	}
	return nil
}

// loadAgy 异步拉取账号列表、默认账号 ID 与路由配置，并在需要时刷新额度快照。
func (st *SettingsState) loadAgy(refreshQuota bool) {
	client := st.agyClient()
	if client == nil {
		return
	}

	if refreshQuota {
		st.refreshAgyQuota()
	}

	st.mu.Lock()
	if st.agy.loading {
		st.mu.Unlock()
		return
	}
	// 页面打开时先用缓存里的账号快照立即渲染，随后在后台刷新。
	if !st.agy.loaded {
		if cached, ok := loadProviderStatusCache(st.dataDir); ok {
			if len(cached.AgyAccounts) > 0 {
				st.agy.accounts = cached.AgyAccounts
				st.agy.defaultID = cached.AgyDefaultID
				st.agy.loaded = true
			}
			st.agy.localLogin = cached.AgyLocalLogin
		}
	}
	st.agy.loading = true
	st.agy.err = ""
	st.mu.Unlock()
	st.invalidate()

	providerStatusCacheWriters.Add(1)
	go func() {
		defer providerStatusCacheWriters.Done()
		accs, errAccounts := client.AgyAccounts()
		defID := client.AgyDefaultAccountID()
		cfg, errRouting := client.AgyRouting()
		autoWarmup, errAuto := client.AgyAutoWarmup()
		// 本机登录状态：只看宿主凭证（钥匙串 / antigravity-oauth-token），
		// 账号管理器未接线时也能正确报告 Antigravity 已登录。
		localLogin := accounts.SystemLoginAvailable(context.Background())

		st.mu.Lock()
		st.agy.loading = false
		st.agy.loaded = true
		st.agy.localLogin = localLogin
		if errAccounts != nil {
			st.agy.err = errAccounts.Error()
		} else {
			st.agy.err = ""
			st.agy.accounts = accs
			st.agy.defaultID = defID
			if errRouting == nil {
				st.agy.routing = cfg
				st.agy.routingLoaded = true
				st.agy.routingErr = ""
			} else {
				st.agy.routingLoaded = false
				st.agy.routingErr = errRouting.Error()
			}
			if errAuto == nil {
				st.agy.autoWarmup = autoWarmup
				st.agy.autoWarmupLoaded = true
				st.agy.autoWarmupErr = ""
			} else {
				st.agy.autoWarmupLoaded = false
				st.agy.autoWarmupErr = errAuto.Error()
			}
		}
		st.mu.Unlock()

		// 账号快照写回缓存，供下次打开页面时立即渲染（供应商行状态由 loadProviderRows 写入）。
		updateProviderStatusCache(st.dataDir, func(cache *providerStatusCache) {
			if errAccounts == nil {
				cache.AgyAccounts = accs
				cache.AgyDefaultID = defID
			}
			cache.AgyLocalLogin = localLogin
		})

		st.invalidate()
	}()
}

// refreshAgyQuota 顶部手动触发全局额度刷新。
func (st *SettingsState) refreshAgyQuota() {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.agy.refreshBusy {
		st.mu.Unlock()
		return
	}
	st.agy.refreshBusy = true
	st.agy.refreshErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := st.refreshAgyQuotaAll(client)
		updatedAccs, listErr := client.AgyAccounts()
		if err == nil {
			err = listErr
		}

		st.mu.Lock()
		st.agy.refreshBusy = false
		if err != nil {
			st.agy.refreshErr = err.Error()
		} else {
			st.agy.refreshErr = ""
		}
		if updatedAccs != nil {
			st.agy.accounts = updatedAccs
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// refreshAgyQuotaAll 刷新额度：本机没有系统凭证时跳过系统账号，
// 避免把「找不到 token 文件」的原始路径错误直接展示给用户。
func (st *SettingsState) refreshAgyQuotaAll(client Client) error {
	st.mu.Lock()
	localLogin := st.agy.localLogin
	accs := append([]accounts.Account(nil), st.agy.accounts...)
	st.mu.Unlock()

	ctx := context.Background()
	if localLogin {
		return client.AgyRefreshQuota(ctx, "")
	}

	var errs []error
	for _, acc := range accs {
		if acc.ID == accounts.SystemAccountID {
			continue
		}
		if acc.Status == accounts.StatusDisabled || acc.Status == accounts.StatusForbidden {
			continue
		}
		if err := client.AgyRefreshQuota(ctx, acc.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// updateAgyRouting 原子更新并保存全局路由配置。
func (st *SettingsState) updateAgyRouting(mutate func(cfg *multiaccount.Config)) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if !st.agy.routingLoaded || st.agy.routingBusy {
		st.mu.Unlock()
		return
	}
	newCfg := st.agy.routing
	mutate(&newCfg)
	st.agy.routingBusy = true
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.AgySetRouting(newCfg)
		st.mu.Lock()
		st.agy.routingBusy = false
		if err != nil {
			st.agy.routingErr = err.Error()
		} else {
			st.agy.routingErr = ""
			st.agy.routing = newCfg
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// warmupAgyAll 触发批量预热
func (st *SettingsState) warmupAgyAll() {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.agy.warmupBusy {
		st.mu.Unlock()
		return
	}
	st.agy.warmupBusy = true
	st.agy.warmupErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		ctx := context.Background()
		err := client.AgyWarmup(ctx, "")
		updatedAccs, listErr := client.AgyAccounts()
		if err == nil {
			err = listErr
		}

		st.mu.Lock()
		st.agy.warmupBusy = false
		if err != nil {
			st.agy.warmupErr = err.Error()
		} else {
			st.agy.warmupErr = ""
		}
		if updatedAccs != nil {
			st.agy.accounts = updatedAccs
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// warmupAgyAccount 针对单账号发起预热
func (st *SettingsState) warmupAgyAccount(id string) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.agy.warmupBusyIDs == nil {
		st.agy.warmupBusyIDs = make(map[string]bool)
	}
	if st.agy.warmupBusyIDs[id] {
		st.mu.Unlock()
		return
	}
	st.agy.warmupBusyIDs[id] = true
	if st.agy.warmupRowErr != nil {
		delete(st.agy.warmupRowErr, id)
	}
	st.mu.Unlock()
	st.invalidate()

	go func() {
		ctx := context.Background()
		err := client.AgyWarmup(ctx, id)
		updatedAccs, _ := client.AgyAccounts()

		st.mu.Lock()
		delete(st.agy.warmupBusyIDs, id)
		if st.agy.warmupRowErr == nil {
			st.agy.warmupRowErr = make(map[string]string)
		}
		if err != nil {
			st.agy.warmupRowErr[id] = err.Error()
		} else {
			delete(st.agy.warmupRowErr, id)
		}
		if updatedAccs != nil {
			st.agy.accounts = updatedAccs
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// toggleAgyAutoWarmup 切换自动预热开关
func (st *SettingsState) toggleAgyAutoWarmup(enabled bool) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.agy.autoWarmupBusy {
		st.mu.Unlock()
		return
	}
	st.agy.autoWarmupBusy = true
	st.agy.autoWarmupErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.AgySetAutoWarmup(enabled)
		st.mu.Lock()
		st.agy.autoWarmupBusy = false
		if err != nil {
			st.agy.autoWarmupErr = err.Error()
		} else {
			st.agy.autoWarmup = enabled
			st.agy.autoWarmupErr = ""
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// renderAgyRefreshQuota 渲染顶部说明与“刷新额度”行及刷新错误提示。
func renderAgyRefreshQuota(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	refreshBusy := st.agy.refreshBusy
	refreshErr := st.agy.refreshErr
	warmupBusy := st.agy.warmupBusy
	warmupErr := st.agy.warmupErr
	st.mu.Unlock()

	// “刷新额度 / 批量预热”行：两个动作靠右，错误原文接在下方。
	ui.Row(c.Key("sec-agy-top-bar")).
		AlignItems(ui.Center).
		Margin(0, 0, Space1, 0).
		Gap(Space3).
		Children(func() {
			ui.Spacer(c)

			warmupAllBtn := ui.ButtonBase(c.Key("agy-top-warmup-btn")).
				Label("批量预热").
				Radius(RadiusSM).
				Padding(Space1, Space4)
			if warmupBusy {
				warmupAllBtn.Disabled(true)
			} else if warmupAllBtn.Hovered() {
				warmupAllBtn.Background(ThemeHover(c))
			}
			if warmupAllBtn.Clicked() && !warmupBusy {
				st.warmupAgyAll()
			}
			warmupAllBtn.Children(func() {
				label := "批量预热"
				if warmupBusy {
					label = "正在预热…"
				}
				ui.Text(c, label).
					FontSize(TypeBody).
					TextColor(ThemeAccent(c))
			})

			refreshQuotaBtn := ui.ButtonBase(c.Key("agy-top-refresh-quota-btn")).
				Label("刷新额度").
				Radius(RadiusSM).
				Padding(Space1, Space4)
			if refreshBusy {
				refreshQuotaBtn.Disabled(true)
			} else if refreshQuotaBtn.Hovered() {
				refreshQuotaBtn.Background(ThemeHover(c))
			}
			if refreshQuotaBtn.Clicked() && !refreshBusy {
				st.refreshAgyQuota()
			}
			refreshQuotaBtn.Children(func() {
				label := "刷新额度"
				if refreshBusy {
					label = "正在刷新额度…"
				}
				ui.Text(c, label).
					FontSize(TypeBody).
					TextColor(ThemeAccent(c))
			})
		})

	if refreshErr != "" {
		ui.Text(c, "刷新额度失败: "+refreshErr).
			FontSize(TypeFootnote).
			TextColor(ThemeDanger(c)).
			Margin(0, 0, Space1, 0)
	}
	if warmupErr != "" {
		ui.Text(c, "批量预热失败: "+warmupErr).
			FontSize(TypeFootnote).
			TextColor(ThemeDanger(c)).
			Margin(0, 0, Space1, 0)
	}
}

// renderAgyAccountsSection 渲染 agy 账号列表、删除与登录（添加账号）区。
func renderAgyAccountsSection(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	loaded := st.agy.loaded
	loading := st.agy.loading
	errMsg := st.agy.err
	accs := make([]accounts.Account, len(st.agy.accounts))
	copy(accs, st.agy.accounts)
	defaultID := st.agy.defaultID
	deleteConfirmID := st.agy.deleteConfirmID
	deleteBoundInfo := st.agy.deleteBoundInfo
	deleteBusyID := st.agy.deleteBusyID

	deleteErr := make(map[string]string, len(st.agy.deleteErr))
	for k, v := range st.agy.deleteErr {
		deleteErr[k] = v
	}
	rowErr := make(map[string]string, len(st.agy.rowErr))
	for k, v := range st.agy.rowErr {
		rowErr[k] = v
	}

	warmupBusyIDs := make(map[string]bool, len(st.agy.warmupBusyIDs))
	for k, v := range st.agy.warmupBusyIDs {
		warmupBusyIDs[k] = v
	}
	warmupRowErr := make(map[string]string, len(st.agy.warmupRowErr))
	for k, v := range st.agy.warmupRowErr {
		warmupRowErr[k] = v
	}

	// localLogin 表示本机是否真的有系统凭证；没有时系统账号不能当作正常账号展示。
	localLogin := st.agy.localLogin
	st.mu.Unlock()

	client := st.agyClient()

	if !loaded && !loading {
		st.loadAgy(true)
		loading = true
	}

	// 1. 账号列表部分
	renderSettingsSection(c, "sec-agy-accounts", "账号", func() {
		if loading && !loaded {
			ui.Row(c.Key("agy-accounts-loading")).
				Padding(settingsRowPadY, settingsRowPadX).
				Children(func() {
					ui.Text(c, "正在加载 agy 账号…").
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})
			return
		}

		if errMsg != "" {
			ui.Column(c.Key("agy-accounts-error")).
				Padding(settingsRowPadY, settingsRowPadX).
				Gap(Space3).
				Children(func() {
					ui.Text(c, errMsg).
						FontSize(TypeBody).
						TextColor(ThemeDanger(c))

					retryBtn := ui.ButtonBase(c.Key("agy-retry-btn")).
						Label("重试").
						Radius(RadiusSM).
						Padding(Space1, Space4)
					if retryBtn.Hovered() {
						retryBtn.Background(ThemeHover(c))
					}
					if retryBtn.Clicked() {
						st.loadAgy(true)
					}
					retryBtn.Children(func() {
						ui.Text(c, "重试").
							FontSize(TypeBody).
							TextColor(ThemeAccent(c))
					})
				})
			if len(accs) == 0 {
				return
			}
		}

		if len(accs) == 0 {
			ui.Column(c.Key("agy-accounts-empty")).
				Padding(settingsRowPadY, settingsRowPadX).
				Gap(Space4).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "还没有 agy 账号").
						FontSize(TypeProse).
						TextColor(ThemeSecondaryText(c))

					loginShortcutBtn := ui.ButtonBase(c.Key("agy-empty-login-btn")).
						Label("添加账号").
						Radius(RadiusSM).
						Padding(Space2, Space4).
						Background(ThemeAccent(c).Alpha(0.12))
					if loginShortcutBtn.Hovered() {
						loginShortcutBtn.Background(ThemeAccent(c).Alpha(0.20))
					}
					if loginShortcutBtn.Clicked() && client != nil {
						st.startBrowserLogin()
					}
					loginShortcutBtn.Children(func() {
						ui.Text(c, "登录 agy 账号").
							FontSize(TypeBody).
							FontWeight(500).
							TextColor(ThemeAccent(c))
					})
				})
			return
		}

		now := time.Now()
		for i, acc := range accs {
			if i > 0 {
				renderSettingsDivider(c, fmt.Sprintf("div-agy-acc-%d", i))
			}

			accVal := acc
			accKey := fmt.Sprintf("agy-acc-%s-%d", accVal.ID, i)
			isSystem := accVal.ID == accounts.SystemAccountID
			isDefault := accVal.ID == defaultID

			ui.Column(c.Key(accKey)).
				Padding(settingsRowPadY, settingsRowPadX).
				Gap(Space3).
				Children(func() {
					displayName := accVal.Label
					if displayName == "" {
						displayName = accVal.Email
					}
					if displayName == "" {
						displayName = accVal.ID
					}

					// 顶部：账号名称/邮箱 + 徽章
					ui.Row(c.Key(accKey + "-header")).
						AlignItems(ui.Center).
						Gap(Space3).
						Children(func() {
							ui.Text(c, displayName).
								FontSize(TypeProse).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								SingleLine()

							if accVal.Label != "" && accVal.Email != "" && accVal.Label != accVal.Email {
								ui.Text(c, "("+accVal.Email+")").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							}

							// 系统账号徽章
							if isSystem {
								Badge(c, accKey+"-badge-system", "系统", ToneNeutral)
							}

							// 默认账号徽章
							if isDefault {
								Badge(c, accKey+"-badge-default", "默认", ToneNeutral)
							}

							// Tier 徽章
							if accVal.Tier != "" {
								Badge(c, accKey+"-badge-tier", accVal.Tier, ToneNeutral)
							}

							// 状态徽章 (active/disabled/invalid/forbidden)
							// 启用开关只写 On，关掉的账号 Status 仍是 active，所以这里以 On 为准显示「已停用」。
							statusText, statusTone := "未知", ToneNeutral
							switch accVal.Status {
							case accounts.StatusActive:
								// 系统账号的凭证来自本机，本机没有凭证时不能算「正常」。
								if isSystem && !localLogin {
									statusText, statusTone = "未登录", ToneWarning
								} else if accVal.On {
									statusText, statusTone = "正常", ToneSuccess
								} else {
									statusText = "已停用"
								}
							case accounts.StatusDisabled:
								statusText = "已停用"
							case accounts.StatusInvalid:
								statusText, statusTone = "凭证失效", ToneDanger
							case accounts.StatusForbidden:
								statusText, statusTone = "不可用", ToneDanger
							default:
								if accVal.Status != "" {
									statusText = accVal.Status
								}
							}
							Badge(c, accKey+"-badge-status", statusText, statusTone)
						})

					// 启用/停用开关：只写 On，卡片内的错误由 rowErr 统一展示。
					accOn := accVal.On
					toggleFunc(
						func(*SettingsState) (bool, bool) { return accOn, false },
						func(_ *SettingsState, on bool) {
							if client == nil {
								return
							}
							go func(id string, on bool) {
								err := client.AgyUpdate(id, accounts.UpdatePatch{On: &on})
								st.mu.Lock()
								if st.agy.rowErr == nil {
									st.agy.rowErr = make(map[string]string)
								}
								if err != nil {
									st.agy.rowErr[id] = err.Error()
								} else {
									delete(st.agy.rowErr, id)
									for idx := range st.agy.accounts {
										if st.agy.accounts[idx].ID == id {
											st.agy.accounts[idx].On = on
											break
										}
									}
								}
								st.mu.Unlock()
								st.invalidate()
							}(accVal.ID, on)
						},
					).render(c, st, setting{ID: accKey + "-enable", Title: "启用账号 " + displayName})

					// 系统账号在本机没有凭证时给出提示，避免把缺失的凭证当成正常状态。
					if isSystem && !localLogin {
						ui.Text(c, "本机未检测到 agy 登录凭证").
							FontSize(TypeFootnote).
							TextColor(ThemeWarning(c))
					}

					// 状态说明（如有 StatusReason）
					if accVal.StatusReason != "" {
						ui.Text(c, accVal.StatusReason).
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					}
					if accVal.CoolingUntil.After(now) {
						ui.Text(c, "路由冷却中，至 "+accVal.CoolingUntil.Local().Format("15:04:05")).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					}

					// 额度展示区
					ui.Column(c.Key(accKey + "-quota-box")).
						Gap(Space1).
						Children(func() {
							if accVal.QuotaReadAt == "" {
								ui.Text(c, "尚未拉取额度").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							} else {
								if accVal.QuotaStale {
									ui.Text(c, "额度数据已过期").
										FontSize(TypeFootnote).
										TextColor(ThemeDanger(c))
								}

								// 遍历 Gemini 额度及 7d 周额度
								quotaKeys := []struct {
									key   string
									label string
								}{
									{quota.GroupGemini, "Gemini"},
									{quota.GroupGemini + " 7d", "Gemini 周额度"},
								}

								for _, qk := range quotaKeys {
									qg, ok := accVal.Quota[qk.key]
									if !ok {
										continue
									}
									qg = accounts.EffectiveQuota(qg, accVal.QuotaReadAt, now)

									// 解析重置时间
									resetStr := ""
									isCooling := false
									coolingUntilStr := ""
									if qg.ResetAt != "" {
										if t, err := time.Parse(time.RFC3339, qg.ResetAt); err == nil {
											loc := t.Local()
											if loc.Year() == now.Year() && loc.YearDay() == now.YearDay() {
												resetStr = loc.Format("15:04")
											} else {
												resetStr = loc.Format("1月2日 15:04")
											}
											if qg.UsedPercent >= 100 && t.After(now) {
												isCooling = true
												coolingUntilStr = loc.Format("15:04")
											}
										}
									}

									ui.Row(c.Key(fmt.Sprintf("%s-q-%s", accKey, qk.key))).
										Gap(Space3).
										AlignItems(ui.Center).
										Children(func() {
											ui.Text(c, qk.label+":").
												FontSize(TypeFootnote).
												TextColor(ThemeSecondaryText(c))

											usedText := fmt.Sprintf("已用 %.0f%%", qg.UsedPercent)
											ui.Text(c, usedText).
												FontSize(TypeFootnote).
												FontWeight(500).
												TextColor(ThemePrimaryText(c))

											if resetStr != "" {
												ui.Text(c, "(重置时间: "+resetStr+")").
													FontSize(TypeCaption).
													TextColor(ThemeSecondaryText(c))
											}

											if isCooling {
												ui.Text(c, fmt.Sprintf("冷却中，至 %s", coolingUntilStr)).
													FontSize(TypeCaption).
													FontWeight(500).
													TextColor(ThemeDanger(c))
											}
										})
								}
							}
						})

					// 预热状态展示
					if accVal.LastWarmup != nil {
						lw := accVal.LastWarmup
						ui.Row(c.Key(accKey + "-warmup-info")).
							Gap(Space3).
							AlignItems(ui.Center).
							Children(func() {
								ui.Text(c, "Gemini 预热:").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))

								modelText := lw.Model
								if modelText == "" {
									modelText = "未指定"
								}
								ui.Text(c, modelText).
									FontSize(TypeFootnote).
									FontWeight(500).
									TextColor(ThemePrimaryText(c))

								statusDesc := "状态未知"
								statusColor := ThemeSecondaryText(c)
								switch lw.Status {
								case "success":
									statusDesc = "新周期已生效"
									statusColor = ThemeSuccess(c)
								case "pending_confirmation":
									statusDesc = "已生成，待周期确认"
									statusColor = ThemeWarning(c)
								case "network_unknown":
									statusDesc = "网络结果不明"
									statusColor = ThemeWarning(c)
								case "failed":
									statusDesc = "失败"
									if lw.Message != "" {
										statusDesc += ": " + lw.Message
									}
									statusColor = ThemeDanger(c)
								}
								ui.Text(c, "("+statusDesc+")").
									FontSize(TypeCaption).
									TextColor(statusColor)
							})
					}

					if wErr, ok := warmupRowErr[accVal.ID]; ok && wErr != "" {
						ui.Text(c, "预热失败: "+wErr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					}

					// 操作按钮行：预热、设为默认、删除
					ui.Row(c.Key(accKey+"-actions")).
						AlignItems(ui.Center).
						Gap(Space3).
						Margin(Space1, 0, 0, 0).
						Children(func() {
							// 预热按钮
							isWarming := warmupBusyIDs[accVal.ID]
							canWarmup := accVal.On && accVal.Status != accounts.StatusDisabled && accVal.Status != accounts.StatusForbidden && !accVal.CoolingUntil.After(now)
							warmupBtn := ui.ButtonBase(c.Key(accKey+"-btn-warmup")).
								Label("预热").
								Radius(RadiusSM).
								Padding(Space1, Space3)
							if isWarming || !canWarmup {
								warmupBtn.Disabled(true)
							} else if warmupBtn.Hovered() {
								warmupBtn.Background(ThemeHover(c))
							}
							if warmupBtn.Clicked() && !isWarming && canWarmup && client != nil {
								st.warmupAgyAccount(accVal.ID)
							}
							warmupBtn.Children(func() {
								label := "预热"
								if isWarming {
									label = "预热中…"
								}
								ui.Text(c, label).
									FontSize(TypeFootnote).
									TextColor(ThemeAccent(c))
							})
							// 设为默认按钮（仅对非默认账号显示）
							if !isDefault {
								defBtn := ui.ButtonBase(c.Key(accKey+"-btn-setdefault")).
									Label("设为默认").
									Radius(RadiusSM).
									Padding(Space1, Space3)
								if defBtn.Hovered() {
									defBtn.Background(ThemeHover(c))
								}
								if defBtn.Clicked() && client != nil {
									go func(id string) {
										err := client.AgySetDefault(id)
										st.mu.Lock()
										if st.agy.rowErr == nil {
											st.agy.rowErr = make(map[string]string)
										}
										if err != nil {
											st.agy.rowErr[id] = err.Error()
										} else {
											delete(st.agy.rowErr, id)
											st.agy.defaultID = id
										}
										st.mu.Unlock()
										st.invalidate()
									}(accVal.ID)
								}
								defBtn.Children(func() {
									ui.Text(c, "设为默认").
										FontSize(TypeFootnote).
										TextColor(ThemeAccent(c))
								})

								ui.Text(c, "只修改 MCode 自己的账号配置，不改动系统 ~/.gemini").
									FontSize(TypeCaption).
									TextColor(ThemeSecondaryText(c))
							}

							ui.Spacer(c)

							// 删除按钮处理
							if isSystem {
								// 系统账号不可删除：按钮禁用并文字提示
								delBtn := ui.ButtonBase(c.Key(accKey+"-btn-del-sys")).
									Label("系统账号不可删除").
									Radius(RadiusSM).
									Padding(Space1, Space3).
									Disabled(true)
								delBtn.Children(func() {
									ui.Text(c, "系统账号不可删除").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c).Alpha(0.6))
								})
							} else if deleteConfirmID == accVal.ID {
								// 二次确认态
								confirmPrompt := "确定要删除此账号吗？"
								if deleteBoundInfo != "" {
									confirmPrompt = deleteBoundInfo
								}

								ui.Text(c, confirmPrompt).
									FontSize(TypeFootnote).
									TextColor(ThemeDanger(c))

								confirmBtn := ui.ButtonBase(c.Key("btn-del-confirm-"+accVal.ID)).
									Label("确认删除").
									Radius(RadiusSM).
									Padding(Space1, Space3).
									Background(ThemeDanger(c).Alpha(0.15))
								if confirmBtn.Hovered() {
									confirmBtn.Background(ThemeDanger(c).Alpha(0.25))
								}
								if confirmBtn.Clicked() && client != nil {
									st.mu.Lock()
									st.agy.deleteBusyID = accVal.ID
									st.agy.deleteConfirmID = ""
									st.agy.deleteBoundInfo = ""
									st.mu.Unlock()
									st.invalidate()

									go func(id string) {
										err := client.AgyRemove(id)
										st.mu.Lock()
										st.agy.deleteBusyID = ""
										if st.agy.deleteErr == nil {
											st.agy.deleteErr = make(map[string]string)
										}
										if err != nil {
											st.agy.deleteErr[id] = err.Error()
										} else {
											delete(st.agy.deleteErr, id)
										}
										st.mu.Unlock()

										if err == nil {
											st.loadAgy(false)
										} else {
											st.invalidate()
										}
									}(accVal.ID)
								}
								confirmBtn.Children(func() {
									ui.Text(c, "确认删除").
										FontSize(TypeFootnote).
										TextColor(ThemeDanger(c))
								})

								cancelBtn := ui.ButtonBase(c.Key(accKey+"-btn-del-cancel")).
									Label("取消").
									Radius(RadiusSM).
									Padding(Space1, Space3)
								if cancelBtn.Hovered() {
									cancelBtn.Background(ThemeHover(c))
								}
								if cancelBtn.Clicked() {
									st.mu.Lock()
									st.agy.deleteConfirmID = ""
									st.agy.deleteBoundInfo = ""
									st.mu.Unlock()
									st.invalidate()
								}
								cancelBtn.Children(func() {
									ui.Text(c, "取消").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								})
							} else if deleteBusyID == accVal.ID {
								ui.Text(c, "正在删除…").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							} else {
								// 正常删除按钮
								delBtn := ui.ButtonBase(c.Key("btn-del-"+accVal.ID)).
									Label("btn-del-"+accVal.ID).
									Radius(RadiusSM).
									Padding(Space1, Space3)
								if delBtn.Hovered() {
									delBtn.Background(ThemeHover(c))
								}
								if delBtn.Clicked() && client != nil {
									bound := client.AgyBoundConversations(accVal.ID)
									prompt := "确定要删除此账号吗？"
									if len(bound) > 0 {
										prompt = fmt.Sprintf("有 %d 个会话绑定在此账号，确定删除？", len(bound))
									}

									st.mu.Lock()
									st.agy.deleteConfirmID = accVal.ID
									st.agy.deleteBoundInfo = prompt
									if st.agy.deleteErr != nil {
										delete(st.agy.deleteErr, accVal.ID)
									}
									st.mu.Unlock()
									st.invalidate()
								}
								delBtn.Children(func() {
									ui.Text(c, "删除").
										FontSize(TypeFootnote).
										TextColor(ThemeDanger(c))
								})
							}
						})

					// 行级错误显示
					if errStr, ok := deleteErr[accVal.ID]; ok && errStr != "" {
						ui.Text(c, "删除失败: "+errStr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					}
					if errStr, ok := rowErr[accVal.ID]; ok && errStr != "" {
						ui.Text(c, errStr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					}
				})
		}
	})

	// 2. 登录与添加账号区
	renderSettingsTreeSection(c, st, agyLoginSection, true)
}

// agyLoginSection 是「添加账号」卡片：浏览器授权登录是一整块内容，入口按钮自己写着文案。
var agyLoginSection = settingsSection{
	ID:    "sec-agy-login",
	Title: "添加账号",
	Rows: []setting{{
		ID:      "row-agy-browser-login",
		Title:   "浏览器授权登录",
		Aliases: []string{"浏览器登录", "供应商", "登录", "账号", "浏览器", "oauth"},
		Control: customFullWidth(renderAgyBrowserLoginBody),
	}},
}

// renderAgyBrowserLoginBody 画浏览器授权登录块：发起与取消、登录链接、手动回调码与错误原文。
func renderAgyBrowserLoginBody(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	busy := st.agy.loginBrowserBusy
	loginURL := st.agy.loginBrowserURL
	loginErr := st.agy.loginBrowserErr
	pasted := st.agy.loginInputCode
	st.mu.Unlock()
	client := st.agyClient()

	ui.Column(c.Key("col-agy-browser-login")).
		Gap(Space3).
		FillWidth().
		Children(func() {
			ui.Row(c.Key("row-agy-browser-btns")).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					startBtn := ui.ButtonBase(c.Key("agy-btn-browser-start")).
						Label("浏览器登录").
						Radius(RadiusSM).
						Padding(Space2, Space4).
						Background(ThemeAccent(c).Alpha(0.12))
					if busy {
						startBtn.Disabled(true)
					} else if startBtn.Hovered() {
						startBtn.Background(ThemeAccent(c).Alpha(0.20))
					}
					if startBtn.Clicked() && !busy && client != nil {
						st.startBrowserLogin()
					}
					startBtn.Children(func() {
						btnText := "浏览器登录"
						if busy {
							btnText = "正在等待浏览器回调…"
						}
						ui.Text(c, btnText).
							FontSize(TypeBody).
							FontWeight(500).
							TextColor(ThemeAccent(c))
					})

					if busy {
						cancelBtn := ui.ButtonBase(c.Key("agy-btn-browser-cancel")).
							Label("取消").
							Radius(RadiusSM).
							Padding(Space2, Space4)
						if cancelBtn.Hovered() {
							cancelBtn.Background(ThemeHover(c))
						}
						if cancelBtn.Clicked() && client != nil {
							st.mu.Lock()
							cancel := st.agy.loginCancel
							st.mu.Unlock()
							if cancel != nil {
								cancel()
							}
							client.AgyLoginCancel()
							st.mu.Lock()
							st.agy.loginBrowserURL = ""
							st.agy.loginBrowserErr = ""
							st.mu.Unlock()
							st.invalidate()
						}
						cancelBtn.Children(func() {
							ui.Text(c, "取消").
								FontSize(TypeBody).
								TextColor(ThemeSecondaryText(c))
						})
					}
				})

			if loginURL != "" {
				ui.Row(c.Key("row-agy-browser-url")).
					Gap(Space3).
					AlignItems(ui.Center).
					FillWidth().
					Children(func() {
						ui.Text(c, loginURL).
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c)).
							Grow(1)
						renderAgyCopyButton(c, st, "browser-url", loginURL, "复制链接")
					})
			}

			// 手动提交回调/授权码输入框
			if busy || loginURL != "" {
				ui.Row(c.Key("row-agy-manual-submit")).
					Gap(Space3).
					AlignItems(ui.Center).
					FillWidth().
					Children(func() {
						textFunc(
							func(st *SettingsState) (string, bool) {
								st.mu.Lock()
								defer st.mu.Unlock()
								return st.agy.loginInputCode, false
							},
							func(st *SettingsState, text string) {
								st.mu.Lock()
								st.agy.loginInputCode = text
								st.mu.Unlock()
							},
							"手动粘贴回调 URL 或授权码",
						).render(c, st, setting{ID: "in-agy-manual-code", Title: "手动粘贴回调 URL 或授权码"})

						submitBtn := ui.ButtonBase(c.Key("agy-btn-browser-submit")).
							Label("提交").
							Radius(RadiusSM).
							Padding(Space2, Space4).
							Background(ThemeHover(c))
						if submitBtn.Hovered() {
							submitBtn.Background(ThemeOverlay(c, 0.15))
						}
						if submitBtn.Clicked() && client != nil {
							code := strings.TrimSpace(pasted)
							go func(cInput string) {
								err := client.AgyLoginSubmit(cInput)
								st.mu.Lock()
								if err != nil {
									st.agy.loginBrowserErr = err.Error()
								} else {
									st.agy.loginBrowserErr = ""
								}
								st.mu.Unlock()
								st.invalidate()
							}(code)
						}
						submitBtn.Children(func() {
							ui.Text(c, "提交").
								FontSize(TypeBody).
								TextColor(ThemePrimaryText(c))
						})
					})
			}

			if loginErr != "" {
				ui.Text(c, loginErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			}
		})
}

// agyClipboardNoticeDuration 是复制按钮显示「已复制」的时长。
const agyClipboardNoticeDuration = 2 * time.Second

// renderAgyCopyButton 画复制按钮：把 text 写进剪贴板，短暂显示「已复制」作为反馈。
func renderAgyCopyButton(c *ui.Context, st *SettingsState, key, text, label string) {
	copied := agyClipboardCopied(st, key)
	copyBtn := ui.ButtonBase(c.Key("agy-btn-copy-"+key)).
		Label(label).
		Radius(RadiusSM).
		Padding(Space1, Space3)
	if copyBtn.Hovered() {
		copyBtn.Background(ThemeHover(c))
	}
	if copyBtn.Clicked() {
		c.WriteClipboard(text)
		st.markAgyClipboardCopied(key)
	}
	copyBtn.Children(func() {
		shown, color := label, ThemeAccent(c)
		if copied {
			shown, color = "已复制", ThemeSecondaryText(c)
		}
		ui.Text(c, shown).FontSize(TypeCaption).TextColor(color)
	})
}

// markAgyClipboardCopied 记下这次复制的目标并安排一次重绘，让按钮文案在提示时长后恢复。
func (st *SettingsState) markAgyClipboardCopied(key string) {
	st.mu.Lock()
	st.agy.clipboardKey = key
	st.agy.clipboardAt = time.Now()
	st.mu.Unlock()
	st.invalidate()
	time.AfterFunc(agyClipboardNoticeDuration, st.invalidate)
}

// agyClipboardCopied 判断 key 对应的复制提示是否还在有效期内。
func agyClipboardCopied(st *SettingsState, key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.agy.clipboardKey == key && time.Since(st.agy.clipboardAt) < agyClipboardNoticeDuration
}

// agyStrategyNotes 记录各调度策略的选取逻辑，用于设置面板里随选中项切换的说明文字。
var agyStrategyNotes = map[multiaccount.Strategy]string{
	multiaccount.StrategySmart:  "先把账号按额度分成三档：已用低于 90% 的为「充足」，90%–98% 的为「偏紧」，98% 及以上的为「将耗尽」，依次往下选。充足档里，额度重置时间越早的账号越优先（按小时比较），这样即将重置的额度会先被用掉、不会浪费；重置时间相同时按序号排。偏紧档和将耗尽档按已用比例从低到高排。适合大多数场景。",
	multiaccount.StrategyOrder:  "严格按账号列表的序号从上往下用：只要前面的账号还有额度，就一直用它；额度用满后才换下一个。额度都用满时，仍按序号依次尝试。适合有明确主账号、备用账号的情况。",
	multiaccount.StrategyRotate: "每次请求依次换到下一个可用账号，循环往复，不看额度和用量，请求数在各账号之间平均分配。适合各账号额度相近、希望请求分散的情况。",
	multiaccount.StrategyUsage:  "每次选额度已用比例最低的账号；比例相同时，选近期消耗 token 更少的账号，再相同就按序号。让各账号的额度消耗保持接近。",
	multiaccount.StrategyPace:   "对每个账号计算「剩余额度 ÷ 距离重置的小时数」，得到它每小时还能用多少。数值高的账号先用；数值差距不到 10% 的账号视为同一档，同档内选近期消耗 token 更少的。目标是让各账号在各自重置前都能把额度均匀用完。",
	multiaccount.StrategyWeight: "按每个账号设置的权重分配请求（没设置时按 100 计），权重 200 的账号拿到的请求大约是权重 100 账号的两倍。采用平滑加权轮询，同一账号的请求会穿插分布，不会扎堆连续出现。",
}

// agyStrategyCommonNote 是所有调度策略共用的账号过滤规则说明。
var agyStrategyCommonNote = "已停用或正在冷却的账号不参与选择；开启「冷却账号」后，冷却过的账号排到队尾。"

// agyAffinityNotes 记录各会话保持模式的绑定逻辑，同样用于随选中项切换的说明文字。
var agyAffinityNotes = map[multiaccount.Affinity]string{
	multiaccount.AffinitySession: "会话第一次请求时选定一个账号，之后一直沿用它。只有当这个账号被停用、额度用满、进入冷却，或在本轮被限流换下时，才会按调度策略重新选择，并把会话改绑到新账号。",
	multiaccount.AffinityTurn:    "每一轮对话都按调度策略重新选择账号，不记录会话和账号的绑定。请求分散得最均匀，但同一会话可能在不同轮次落到不同账号上。",
	multiaccount.AffinityStrict:  "会话第一次请求时选定账号后就固定使用，即使这个账号进入冷却、额度用满或被停用也不换号；只有账号被删除后才会重新选择。适合要求同一会话始终落在同一账号上的场景。",
}

// renderAgyRoutingNote 渲染路由设置项下方的说明文字：12 号次要色、允许自动换行，空字符串跳过。
func renderAgyRoutingNote(c *ui.Context, key string, lines ...string) {
	ui.Column(c.Key(key)).
		FillWidth().
		Gap(Space1).
		Children(func() {
			for i, line := range lines {
				if line == "" {
					continue
				}
				ui.Text(c, line).
					Key(fmt.Sprintf("%s-%d", key, i)).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			}
		})
}

// agyRoutingSections 返回「账号调度」展开后的分节。两个图标选择器各有六个 64×44 方块，
// 放不进行内右侧控件列，所以各自成节、标题由分节承载；两个开关连成一张「调度」卡。
func agyRoutingSections() []settingsSection {
	return []settingsSection{agyStrategySection, agyAffinitySection, agyRoutingSection}
}

// agyStrategySection 是调度策略分节。
var agyStrategySection = settingsSection{
	ID:    "sec-agy-strategy",
	Title: "调度策略",
	Rows: []setting{{
		ID:      "sec-agy-routing-strategy",
		Title:   "调度策略",
		Aliases: []string{"账号", "负载均衡", "轮换"},
		Control: customFullWidth(renderAgyStrategyBody),
	}},
}

// agyAffinitySection 是会话保持模式分节。
var agyAffinitySection = settingsSection{
	ID:    "sec-agy-affinity",
	Title: "会话保持模式",
	Rows: []setting{{
		ID:      "sec-agy-routing-affinity",
		Title:   "会话保持模式",
		Aliases: []string{"账号", "负载均衡", "会话"},
		Control: customFullWidth(renderAgyAffinityBody),
	}},
}

// agyRoutingSection 是「调度」卡：配置保存失败的原因挂在对应开关行下方。
var agyRoutingSection = settingsSection{
	ID:    "sec-agy-routing",
	Title: "调度",
	Rows: []setting{
		{
			ID:      "sec-agy-routing-sink",
			Title:   "冷却账号",
			Aliases: []string{"账号", "额度", "队列", "冷却"},
			Control: toggleFunc(agyRoutingSinkValue, func(st *SettingsState, on bool) {
				st.updateAgyRouting(func(cfg *multiaccount.Config) {
					cfg.Sink = on
				})
			}),
			Status: agyRoutingStatus,
		},
		{
			ID:      "sec-agy-auto-warmup",
			Title:   "到期自动预热",
			Aliases: []string{"账号", "额度", "预热"},
			Control: toggleFunc(agyAutoWarmupValue, func(st *SettingsState, on bool) {
				st.toggleAgyAutoWarmup(on)
			}),
			Status: agyAutoWarmupStatus,
		},
	},
}

// agyRoutingSinkValue 读冷却账号开关；路由配置没加载完时接受不了切换。
func agyRoutingSinkValue(st *SettingsState) (on bool, busy bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.agy.routing.Sink, !st.agy.routingLoaded || st.agy.routingBusy
}

// agyAutoWarmupValue 读自动预热开关；加载与写入期间不接受切换。
func agyAutoWarmupValue(st *SettingsState) (on bool, busy bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.agy.autoWarmup, !st.agy.autoWarmupLoaded || st.agy.autoWarmupBusy
}

// agyRoutingStatus 把路由保存失败的原因显示在开关行下方。
func agyRoutingStatus(st *SettingsState, _ bool) string {
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.agy.routingErr == "" {
		return ""
	}
	return "保存路由配置失败: " + st.agy.routingErr
}

// agyAutoWarmupStatus 把自动预热保存失败的原因显示在开关行下方。
func agyAutoWarmupStatus(st *SettingsState, _ bool) string {
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.agy.autoWarmupErr == "" {
		return ""
	}
	return "保存自动预热配置失败: " + st.agy.autoWarmupErr
}

// renderAgyStrategyBody 画调度策略选择器与当前策略的取舍说明。
func renderAgyStrategyBody(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	strategyStr := string(st.agy.routing.Strategy)
	disabled := !st.agy.routingLoaded || st.agy.routingBusy
	st.mu.Unlock()
	if strategyStr == "" {
		strategyStr = string(multiaccount.StrategySmart)
	}
	stratOptions := []visualPickerOption{
		{value: string(multiaccount.StrategySmart), label: "智能调度", icon: IconBrain()},
		{value: string(multiaccount.StrategyOrder), label: "序号优先", icon: IconListOrdered()},
		{value: string(multiaccount.StrategyRotate), label: "轮询调度", icon: IconRepeat()},
		{value: string(multiaccount.StrategyUsage), label: "低用量优先", icon: IconBatteryLow()},
		{value: string(multiaccount.StrategyPace), label: "速率均摊", icon: IconEqual()},
		{value: string(multiaccount.StrategyWeight), label: "加权轮询", icon: IconScale()},
	}
	ui.Column(c.Key("agy-strategy-control")).
		FillWidth().
		Gap(Space4).
		Children(func() {
			renderVisualPicker(c, "picker-agy-strategy", &strategyStr, stratOptions, disabled, func(val string) {
				st.updateAgyRouting(func(cfg *multiaccount.Config) {
					cfg.Strategy = multiaccount.Strategy(val)
				})
			})
			// 在 picker 之后读取 strategyStr，点选后当帧即显示新选项的说明。
			renderAgyRoutingNote(c, "agy-strategy-note", agyStrategyNotes[multiaccount.Strategy(strategyStr)], agyStrategyCommonNote)
		})
}

// renderAgyAffinityBody 画会话保持模式选择器与说明。
func renderAgyAffinityBody(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	affStr := string(st.agy.routing.Affinity)
	disabled := !st.agy.routingLoaded || st.agy.routingBusy
	st.mu.Unlock()
	if affStr == "" {
		affStr = string(multiaccount.AffinitySession)
	}
	affOptions := []visualPickerOption{
		{value: string(multiaccount.AffinitySession), label: "会话内保持", icon: IconMagnet()},
		{value: string(multiaccount.AffinityTurn), label: "每轮重选", icon: IconShuffle()},
		{value: string(multiaccount.AffinityStrict), label: "严格绑定", icon: IconAnchor()},
	}
	ui.Column(c.Key("agy-affinity-control")).
		FillWidth().
		Gap(Space4).
		Children(func() {
			renderVisualPicker(c, "picker-agy-affinity", &affStr, affOptions, disabled, func(val string) {
				st.updateAgyRouting(func(cfg *multiaccount.Config) {
					cfg.Affinity = multiaccount.Affinity(val)
				})
			})
			renderAgyRoutingNote(c, "agy-affinity-note", agyAffinityNotes[multiaccount.Affinity(affStr)])
		})
}

// noteAgyQuotaRefresh 把登录/采纳后的额度刷新失败记录到行级错误：账号已添加但额度没拉到，
// 不能让界面只显示「尚未拉取额度」而不说明原因。
func (st *SettingsState) noteAgyQuotaRefresh(accID string, err error) {
	if err == nil {
		return
	}
	st.mu.Lock()
	if st.agy.rowErr == nil {
		st.agy.rowErr = make(map[string]string)
	}
	st.agy.rowErr[accID] = "账号已添加，额度拉取失败：" + err.Error()
	st.mu.Unlock()
}

// startBrowserLogin 封装发起浏览器 OAuth 流程及后台等待。
func (st *SettingsState) startBrowserLogin() {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.agy.loginBrowserBusy {
		st.mu.Unlock()
		return
	}
	st.agy.loginBrowserBusy = true
	st.agy.loginBrowserURL = ""
	st.agy.loginBrowserErr = ""
	st.agy.loginInputCode = ""
	ctx, cancel := context.WithCancel(context.Background())
	st.agy.loginCancel = cancel
	st.mu.Unlock()
	st.invalidate()

	go func() {
		defer cancel()
		url, err := client.AgyLoginStart(ctx)
		if err != nil {
			st.mu.Lock()
			st.agy.loginBrowserBusy = false
			if ctx.Err() == nil {
				st.agy.loginBrowserErr = err.Error()
			}
			st.mu.Unlock()
			st.invalidate()
			return
		}

		st.mu.Lock()
		st.agy.loginBrowserURL = url
		st.mu.Unlock()
		st.invalidate()

		acc, errWait := client.AgyLoginWait(ctx)
		st.mu.Lock()
		st.agy.loginBrowserBusy = false
		if errWait != nil {
			// 如果是手动提交或取消导致退出，不要覆盖已有状态
			if ctx.Err() == nil && st.agy.loginBrowserURL != "" {
				st.agy.loginBrowserErr = errWait.Error()
			}
			st.agy.loginBrowserURL = ""
		} else {
			st.agy.loginBrowserURL = ""
			st.agy.loginBrowserErr = ""
			st.agy.loginInputCode = ""
		}
		st.mu.Unlock()

		if errWait == nil {
			st.noteAgyQuotaRefresh(acc.ID, client.AgyRefreshQuota(ctx, acc.ID))
			st.loadAgy(false)
		} else {
			st.invalidate()
		}
	}()
}

func agyTerminalCommand(binary string, env, args []string) string {
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
	}
	parts := []string{"env"}
	for _, entry := range env {
		parts = append(parts, quote(entry))
	}
	if binary == "" {
		binary = "agy"
	}
	parts = append(parts, quote(binary))
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}

// agyProviderSheetSections 返回 agy 供应商面板的分节定义：账号、调度策略、会话保持模式、
// 调度、终端登录。后四节直接复用设置树的 section，与行展开区保持同一渲染路径。
func agyProviderSheetSections(st *SettingsState) []providerSheetSection {
	return []providerSheetSection{
		{
			ID:    "sec-agy-accounts",
			Title: "账号",
			Render: func(c *ui.Context, st *SettingsState) {
				renderAgyRefreshQuota(c, st)
				renderAgyAccountsSection(c, st)
				renderProviderTrayQuotaRow(c, st, "agy")
			},
		},
		{
			ID:    "sec-agy-strategy",
			Title: "调度策略",
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsTreeSection(c, st, agyStrategySection, true)
			},
		},
		{
			ID:    "sec-agy-affinity",
			Title: "会话保持模式",
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsTreeSection(c, st, agyAffinitySection, true)
			},
		},
		{
			ID:       "sec-agy-routing",
			Title:    "调度",
			Advanced: true,
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsTreeSection(c, st, agyRoutingSection, true)
			},
		},
		{
			ID:       "sec-agy-advanced-login",
			Title:    "终端登录",
			Advanced: true,
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsTreeSection(c, st, agyTerminalLoginSection, true)
			},
		},
	}
}

// renderAgyProviderSheetSections 把各节整块堆叠，仅供测试与调试；生产路径走 renderProviderSheetBody。
func renderAgyProviderSheetSections(c *ui.Context, st *SettingsState) {
	for _, def := range agyProviderSheetSections(st) {
		def.Render(c, st)
	}
}

// renderAgyRowAdvancedSections 在 agy 行的展开区里渲染默认收起的「账号调度」子区：
// 展开后依次渲染路由与负载均衡、终端登录（备用）。
func renderAgyRowAdvancedSections(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	open := st.agy.dispatchOpen
	st.mu.Unlock()

	ui.Column(c.Key("agy-dispatch")).Gap(Space3).Children(func() {
		btn := ui.ButtonBase(c.Key("row-agy-dispatch")).
			Label("账号调度").
			Padding(Space4, Space5).
			Radius(RadiusMD)
		if btn.Hovered() {
			btn.Background(ThemeHover(c))
		} else {
			btn.Background(ui.Transparent)
		}
		btn.OnClick(func() {
			st.mu.Lock()
			st.agy.dispatchOpen = !st.agy.dispatchOpen
			st.mu.Unlock()
			st.invalidate()
		})
		btn.Children(func() {
			ui.Row(c.Key("row-agy-dispatch-inner")).
				Grow(1).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Column(c.Key("row-agy-dispatch-left")).
						Grow(1).
						Gap(Space1).
						Children(func() {
							ui.Text(c, "账号调度").
								FontSize(TypeProse).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
						})
					icon := IconChevronRight()
					if open {
						icon = IconChevronDown()
					}
					ui.Icon(c, icon).Size(14, 14).TextColor(ThemeSecondaryText(c))
				})
		})

		if open {
			renderAgyDispatchContent(c, st)
		}
	})
}

// renderAgyDispatchContent 渲染「账号调度」展开后的内容。
func renderAgyDispatchContent(c *ui.Context, st *SettingsState) {
	for _, sec := range agyRoutingSections() {
		renderSettingsTreeSection(c, st, sec, true)
	}
	renderSettingsTreeSection(c, st, agyTerminalLoginSection, true)
}

// agyTerminalLoginSection 是终端登录（备用）分节：整块内容，按钮自己写着文案。
var agyTerminalLoginSection = settingsSection{
	ID:    "sec-agy-advanced-login",
	Title: "",
	Rows: []setting{{
		ID:      "row-agy-terminal-login",
		Title:   "终端登录（备用）",
		Aliases: []string{"终端登录（备用）", "供应商", "登录", "账号", "终端", "回调"},
		Control: customFullWidth(renderAgyTerminalLoginBody),
	}},
}

// agyProviderIndexSections 是供应商面板里 agy 行的索引来源：面板本身用 Body 渲染，
// Sections 只为搜索与跳转提供行标题和别名，不会产生第二条渲染路径。
func agyProviderIndexSections() []settingsSection {
	return []settingsSection{agyLoginSection, agyRoutingSection, agyStrategySection, agyAffinitySection, agyTerminalLoginSection}
}

// registerAgyProviderSheetSections 在供应商面板注册之后调用，把 agy 行登记进 settingsTreeSheetDefs。
func registerAgyProviderSheetSections() {
	for i := range settingsTreeSheetDefs {
		if settingsTreeSheetDefs[i].ID == "provider" {
			settingsTreeSheetDefs[i].Sections = agyProviderIndexSections()
		}
	}
}

// renderAgyTerminalLoginBody 画终端登录块：生成命令、复制命令、采纳登录与错误原文。
func renderAgyTerminalLoginBody(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	terminalLoginID := st.agy.terminalLoginID
	terminalLoginEnv := append([]string(nil), st.agy.terminalLoginEnv...)
	terminalLoginArgs := append([]string(nil), st.agy.terminalLoginArgs...)
	terminalLoginBusy := st.agy.terminalLoginBusy
	terminalLoginErr := st.agy.terminalLoginErr
	terminalBinary := st.cfg.Providers.Agy.Bin
	st.mu.Unlock()
	client := st.agyClient()

	ui.Column(c.Key("col-agy-terminal-login")).
		Gap(Space3).
		FillWidth().
		Children(func() {
			startTermBtn := ui.ButtonBase(c.Key("agy-btn-terminal-start")).
				Label("终端登录（备用）").
				Radius(RadiusSM).
				Padding(Space2, Space4).
				Background(ThemeHover(c))
			if terminalLoginBusy {
				startTermBtn.Disabled(true)
			} else if startTermBtn.Hovered() {
				startTermBtn.Background(ThemeOverlay(c, 0.12))
			}
			if startTermBtn.Clicked() && client != nil {
				id, env, args, err := client.AgyTerminalLogin()
				st.mu.Lock()
				if err != nil {
					st.agy.terminalLoginErr = err.Error()
				} else {
					st.agy.terminalLoginErr = ""
					st.agy.terminalLoginID = id
					st.agy.terminalLoginEnv = env
					st.agy.terminalLoginArgs = args
				}
				st.mu.Unlock()
				st.invalidate()
			}
			startTermBtn.Children(func() {
				ui.Text(c, "终端登录（备用）").
					FontSize(TypeBody).
					TextColor(ThemePrimaryText(c))
			})

			if terminalLoginID != "" {
				fullCmd := agyTerminalCommand(terminalBinary, terminalLoginEnv, terminalLoginArgs)

				ui.Column(c.Key("col-agy-terminal-cmd-box")).
					Gap(Space2).
					Children(func() {
						ui.Text(c, fullCmd).
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))

						ui.Row(c.Key("row-agy-terminal-adopt")).
							AlignItems(ui.Center).
							Gap(Space3).
							Children(func() {
								renderAgyCopyButton(c, st, "terminal-cmd", fullCmd, "复制命令")

								adoptBtn := ui.ButtonBase(c.Key("agy-btn-terminal-adopt")).
									Label("我已完成登录").
									Radius(RadiusSM).
									Padding(Space2, Space4).
									Background(ThemeAccent(c).Alpha(0.12))
								if adoptBtn.Hovered() {
									adoptBtn.Background(ThemeAccent(c).Alpha(0.20))
								}
								adoptBtn.Disabled(terminalLoginBusy)
								if adoptBtn.Clicked() && !terminalLoginBusy && client != nil {
									st.mu.Lock()
									st.agy.terminalLoginBusy = true
									st.mu.Unlock()
									st.invalidate()

									go func(tid string) {
										acc, err := client.AgyAdoptTerminalLogin(context.Background(), tid)
										st.mu.Lock()
										st.agy.terminalLoginBusy = false
										if err != nil {
											st.agy.terminalLoginErr = err.Error()
										} else {
											st.agy.terminalLoginErr = ""
											st.agy.terminalLoginID = ""
											st.agy.terminalLoginEnv = nil
											st.agy.terminalLoginArgs = nil
										}
										st.mu.Unlock()
										st.invalidate()

										if err == nil {
											st.noteAgyQuotaRefresh(acc.ID, client.AgyRefreshQuota(context.Background(), acc.ID))
											st.loadAgy(false)
										}
									}(terminalLoginID)
								}
								adoptBtn.Children(func() {
									ui.Text(c, "我已完成登录").
										FontSize(TypeBody).
										TextColor(ThemeAccent(c))
								})

								if terminalLoginBusy {
									ui.Text(c, "正在校验登录状态…").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								}
							})
					})
			}

			if terminalLoginErr != "" {
				ui.Text(c, terminalLoginErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			}
		})
}
