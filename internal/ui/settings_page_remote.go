package mcodeui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// remoteServiceRunning 判断远程控制服务是否真的在跑：页面与各 section 共用这一处判定，
// 免得两边的“运行中”理解不一，出现只在运行时可用的控件在停服时被点。
func remoteServiceRunning(status RemoteStatus) bool {
	return status.State == RemoteStateRunning
}

// pageRemote 是「远程控制」页：服务、配对、局域网入口、Tailscale、HTTPS 与手机推送。
// 停服后服务状态、设备、网络与 Tailscale 节点配置常驻，运行态区块与行由各自的 Visible 收起。
var pageRemote = settingsPage{
	ID:       "remote",
	Title:    "远程控制",
	Icon:     IconSmartphone(),
	Keywords: []string{"remote", "mobile", "phone", "kanban", "pair", "qr", "qrcode", "lan", "device", "tls", "https", "cert", "domain", "token", "tailscale", "push", "远程", "手机", "看板", "移动端", "配对", "二维码", "局域网", "设备", "撤销", "重启", "端口", "入口", "证书", "域名", "令牌", "推送", "通知"},
	Load:     remotePageLoad,
	Sections: []settingsSection{
		{ID: "remote.status", Title: "服务状态", Rows: append(append([]setting{}, remoteStatusSection.Rows...), remoteErrorSection.Rows...)},
		{ID: "remote.devices", Title: "设备", Rows: append(append(append([]setting{}, remotePairSection.Rows...), remotePairingSection.Rows...), remoteDeviceListSection.Rows...)},
		{ID: "remote.network", Title: "网络", Rows: append(append([]setting{}, remoteLANSettingsSection.Rows...), remotePortsSection.Rows...)},
		remoteTailscaleSection,
		remoteTLSSection,
		remotePushSection,
	},
}

func init() {
	settingsTreePageDefs = append(settingsTreePageDefs, pageRemote)
}

// remoteStatusSection 是服务开关与运行状态；重启服务放在状态行里，只在运行中可用。
var remoteStatusSection = settingsSection{
	ID:    "remote.status",
	Title: "服务状态",
	Rows: []setting{
		{
			ID:      "row-remote-enabled",
			Title:   "启用远程控制服务",
			Aliases: []string{"开关", "启动", "关闭", "服务", "手机连接"},
			Control: remoteEnabledControl{},
		},
		{
			ID:      "row-remote-status",
			Title:   "运行状态",
			Aliases: []string{"端口", "已连接", "运行中", "已停止", "重启"},
			Control: remoteStatusControl{},
			Status:  remoteStatusValue,
		},
	},
}

// remoteErrorSection 只在服务启动失败时出现：错误原文是运行时值，不是常态说明。
var remoteErrorSection = settingsSection{
	ID:    "remote.status-error",
	Title: "",
	Rows: []setting{{
		Visible: remoteErrorVisible,
		ID:      "row-remote-error",
		Title:   "服务错误",
		Aliases: []string{"错误", "失败", "原因", "启动失败"},
		Control: remoteValueControl{},
		Status: func(st *SettingsState, _ bool) string {
			st.mu.Lock()
			defer st.mu.Unlock()
			msg := st.remote.status.Error
			if msg == "" {
				msg = st.remote.err
			}
			if msg == "" {
				msg = "未知错误"
			}
			return msg
		},
	}},
}

// remotePairSection 是配对新设备的入口。
var remotePairSection = settingsSection{
	ID:    "remote.devices",
	Title: "连接设备",
	Rows: []setting{{
		ID:      "row-remote-pair",
		Title:   "添加设备",
		Aliases: []string{"配对", "新设备", "二维码", "授权码", "添加"},
		Control: remotePairControl{},
		// 配对失败时卡片不会打开，错误只能挂在按钮行右侧，否则用户看不到原因。
		Status: remotePairStatus,
	}},
}

// remotePairStatus 在配对卡没打开时显示配对失败原因。
func remotePairStatus(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.remote.pairingOpen && remoteServiceRunning(st.remote.status) {
		return ""
	}
	return st.remote.pairingErr
}

// remotePairingSection 是配对进行中的卡片：二维码、配对码、入口切换与操作按钮。
var remotePairingSection = settingsSection{
	ID:    "remote.pairing",
	Title: "配对码",
	Rows: []setting{{
		Visible: remotePairingVisible,
		ID:      "row-remote-pairing",
		Title:   "",
		Aliases: []string{"配对码", "配对", "二维码", "授权", "设备"},
		Control: custom(renderRemotePairingBlock),
	}},
}

// remoteDeviceListSection 是已授权设备列表，空态与错误态也画在同一行里。
var remoteDeviceListSection = settingsSection{
	ID:    "remote.device-list",
	Title: "已授权设备",
	Rows: []setting{{
		ID:      "row-remote-devices",
		Title:   "",
		Aliases: []string{"已授权设备", "设备", "撤销", "重命名", "授权"},
		Control: custom(renderRemoteDevicesBlock),
	}},
}

// remoteLANSettingsSection 是局域网入口地址，只在服务运行时出现。
var remoteLANSettingsSection = settingsSection{
	ID:    "remote.network",
	Title: "局域网访问",
	Rows: []setting{{
		Visible: remoteRunningState,
		ID:      "row-remote-entry",
		Title:   "入口地址",
		Aliases: []string{"地址", "局域网", "IP", "复制", "入口"},
		Control: remoteEntryControl{},
		Status:  remoteEntryStatus,
	}},
}

// remotePushSection 是手机推送开关与订阅数，只在服务运行时出现。
var remotePushSection = settingsSection{
	ID:      "remote.push",
	Title:   "手机推送",
	Visible: func(st *SettingsState) bool { return remoteRunningState(st) },
	Rows: []setting{
		{
			ID:      "row-remote-push-enabled",
			Title:   "应用关闭时也推送",
			Aliases: []string{"推送", "通知", "Web Push", "离线推送"},
			Control: remotePushToggleControl{},
		},
		{
			ID:      "row-remote-push-subscriptions",
			Title:   "已订阅设备",
			Aliases: []string{"订阅", "测试推送", "设备", "推送"},
			Control: remotePushTestControl{},
			Status:  remotePushStatus,
		},
	},
}

// remotePortsSection 是监听端口与额外允许的访问域名；改动后要重启服务才生效。
var remotePortsSection = settingsSection{
	ID:    "remote.ports",
	Title: "网络端口与域名",
	Rows: []setting{
		{
			ID:      "row-remote-port",
			Title:   "监听端口",
			Aliases: []string{"端口", "43117", "监听", "重启"},
			Control: remotePortControl{},
			Status:  remotePortStatus,
		},
		{
			ID:      "row-remote-hosts",
			Title:   "额外允许的访问域名",
			Aliases: []string{"域名", "hosts", "访问", "自定义域名", "重启"},
			Control: remoteHostsControl{},
			Status:  remotePortStatus,
		},
	},
}

// remotePageLoad 拉取远程页的运行时数据，并在服务停止时清掉只属于运行态的确认标记。
func remotePageLoad(st *SettingsState) {
	st.mu.Lock()
	running := remoteServiceRunning(st.remote.status)
	if !running {
		// 重置确认框只由 Tailscale 区块渲染，服务停了还留着标记会在下次启动时突然弹窗。
		st.remote.tailscaleResetConfirm = false
	}
	if !st.remote.portInit {
		st.remote.portText = fmt.Sprintf("%d", remoteListenPort(st.cfg.Remote.Listen))
		st.remote.portInit = true
	}
	if !st.remote.tlsFieldsInit {
		st.remote.tlsDomainText = st.cfg.Remote.TLSDomain
		st.remote.tlsPortText = fmt.Sprintf("%d", remoteTLSListenPort(st.cfg.Remote.TLSListen))
		st.remote.tlsFieldsInit = true
	}
	statusLoaded := st.remote.statusLoaded
	devicesLoaded := st.remote.devicesLoaded
	busy := st.remote.busy
	tlsLoaded, tlsBusy := st.remote.tlsLoaded, st.remote.tlsBusy
	tlsConfigured := strings.TrimSpace(st.cfg.Remote.TLSListen) != "" ||
		strings.TrimSpace(st.cfg.Remote.TLSDomain) != "" || st.remote.tls.StartedURL != ""
	tsLoaded, tsBusy := st.remote.tailscaleLoaded, st.remote.tailscaleBusy
	st.mu.Unlock()

	if !statusLoaded && !busy {
		loadRemoteStatus(st)
	}
	if !devicesLoaded {
		loadRemoteDevices(st)
	}
	// TLS 与 Tailscale 的状态只在对应区块可能显示时才拉，停服时不必打后端。
	if (running || tlsConfigured) && !tlsLoaded && !tlsBusy {
		loadRemoteTLSStatus(st)
	}
	if running && !tsLoaded && !tsBusy {
		loadRemoteTailscaleStatus(st)
	}
}

// renderPageRemote 渲染远程控制页；结构与运行时区块都在 pageRemote 里声明。
// 三个确认框由触发的行自己渲染，页面这里不再兜底。
func renderPageRemote(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageRemote)
}

// renderRemoteAdvancedSections 保留旧入口：端口与访问域名已并入页面树的 remote.ports。
func renderRemoteAdvancedSections(c *ui.Context, st *SettingsState) {
	renderSettingsTreeSection(c, st, remotePortsSection, false)
}

// remoteRunningState 是“服务运行中”的可见性判断，供各 section 共用。
func remoteRunningState(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return remoteServiceRunning(st.remote.status)
}

// remoteErrorVisible 判断是否要显示启动失败区块。
func remoteErrorVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.remote.status.State == RemoteStateError {
		return true
	}
	return st.remote.err != "" && !remoteServiceRunning(st.remote.status) && st.cfg.Remote.Enabled
}

// remotePairingVisible 判断配对卡片是否要出现：只有配对进行中且服务在跑时才有码可扫。
func remotePairingVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.remote.pairingOpen && remoteServiceRunning(st.remote.status)
}

// remoteStatusValue 把服务状态写成一行值；停服时运行态信息没有意义，只写「已停止」。
func remoteStatusValue(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	status := st.remote.status
	switch status.State {
	case RemoteStateRunning:
		port := status.Port
		if status.StartedListen != "" {
			port = remoteListenPort(status.StartedListen)
		}
		if port <= 0 {
			port = remoteListenPort(st.cfg.Remote.Listen)
		}
		return fmt.Sprintf("运行中 · 端口 %d · 已连接 %d 台设备", port, status.ConnectedClients)
	case RemoteStateError:
		return "服务启动失败"
	default:
		return "已停止"
	}
}

// remoteEntryStatus 给出入口地址行下方需要补的一句运行时提示：没有局域网地址，或地址变了。
func remoteEntryStatus(st *SettingsState, _ bool) string {
	st.mu.Lock()
	status := st.remote.status
	running := remoteServiceRunning(status)
	st.mu.Unlock()
	if !running {
		return ""
	}
	if remoteCurrentLANIP() == "" {
		return "未找到活动的局域网 IPv4"
	}
	if remoteAddressChanged(status, remoteCurrentLANIP()) {
		served := remoteServingIPv4(status)
		if served == "" {
			served = remoteServingHost(status)
		}
		return fmt.Sprintf("局域网 IP 从 %s 变为 %s，请在手机上重新保存入口", served, remoteCurrentLANIP())
	}
	return ""
}

// remotePortStatus 提示端口或访问域名的改动还没生效。
func remotePortStatus(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	status := st.remote.status
	if !remoteServiceRunning(status) {
		return ""
	}
	configured := remoteListenPort(st.cfg.Remote.Listen)
	started := status.Port
	if status.StartedListen != "" {
		started = remoteListenPort(status.StartedListen)
	}
	portChanged := started > 0 && started != configured
	hostsChanged := !sameRemoteHosts(status.StartedExtraHosts, st.cfg.Remote.ExtraHosts)
	if portChanged || hostsChanged {
		return "重启后生效"
	}
	return ""
}

// remotePushStatus 写出订阅数与最近一次测试推送的回执。
func remotePushStatus(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	text := ""
	if subs := st.remote.status.PushSubscriptions; subs > 0 {
		text = fmt.Sprintf("已登记 %d 条推送订阅", subs)
	}
	if st.remote.pushErr != "" {
		return st.remote.pushErr
	}
	if st.remote.pushResult != "" {
		if text != "" {
			text += " · "
		}
		text += st.remote.pushResult
	}
	return text
}

// renderRemotePairingBlock 渲染配对进行中的卡片：二维码、配对码、入口切换与操作按钮。
func renderRemotePairingBlock(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	pairing := st.remote.pairing
	pairingErr := st.remote.pairingErr
	pairingPolledAt := st.remote.pairingPolledAt
	pairingTarget := st.remote.pairingTarget
	tailscaleFQDN := ""
	// MagicDNS 域名只在开关打开时展示：关掉 Tailscale 后状态里的旧域名没有意义。
	if st.cfg.Remote.TailscaleEnabled() {
		tailscaleFQDN = st.remote.tailscaleStatus.FQDN
	}
	copiedKey := st.remote.copiedKey
	copiedAt := st.remote.copiedAt
	qr := st.remote.qr
	busy := st.remote.busy
	st.mu.Unlock()

	pairingState := remotePairingState(pairing, c.Now())
	if pairingState == "active" && !pairing.ExpiresAt.IsZero() && !pairing.ExpiresAt.After(c.Now()) {
		pairingState = "expired"
	}

	renderPairNewButton := func() {
		btn := ui.ButtonBase(c.Key("btn-remote-pair-new")).
			Label("配对新设备").
			Radius(RadiusSM).
			Padding(Space1, Space4).
			Background(ThemeAccent(c).Alpha(0.12))
		stylePairingButton(c, btn, busy)
		if btn.Clicked() && !busy {
			loadRemotePairing(st, false, false)
		}
		btn.Children(func() {
			label := "配对新设备"
			if busy {
				label = "配对中…"
			}
			ui.Text(c, label).FontSize(TypeBody).TextColor(ThemeAccent(c))
		})
	}

	switch pairingState {
	case "active":
		// 每 2 秒静默确认一次配对状态：手机扫码授权后无需手动刷新即可看到结果。
		if c.Now().Sub(pairingPolledAt) >= 2*time.Second {
			st.mu.Lock()
			st.remote.pairingPolledAt = c.Now()
			st.mu.Unlock()
			pollRemotePairing(st)
		}
		c.After(2 * time.Second)

		// 先算出实际生效的入口：所选入口的链接没就绪时二维码会回退到别的入口，
		// 高亮要跟着落在真正生效的那个上，否则会出现“已禁用却高亮”的按钮。
		selectedTarget := remotePairingEffectiveTarget(pairing, remotePairingTargetValue(pairingTarget))
		targetURL := remotePairingTargetURL(pairing, selectedTarget)
		targetOptions := remotePairingTargetOptions(pairing)
		availableTargets := 0
		for _, opt := range targetOptions {
			if opt.URL != "" {
				availableTargets++
			}
		}

		ui.Column(c.Key("card-remote-pairing")).
			Padding(Space1, 0).
			Gap(Space4).
			AlignItems(ui.Center).
			Children(func() {
				if qr != nil {
					ui.Image(c, qr).Size(settingsQRCodeSize, settingsQRCodeSize)
				}

				ui.Row(c.Key("row-remote-shortcode")).
					Gap(Space4).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, formatShortCode(pairing.ShortCode)).
							Font("monospace").
							FontSize(settingsRemoteHeadlineFont).
							FontWeight(600).
							TextColor(ThemePrimaryText(c))

						renderRemoteCopyButton(c, st, "btn-remote-copy-code", "复制配对码", "已复制配对码", "复制配对码", pairing.ShortCode, copiedKey, copiedAt)
					})

				if !pairing.ExpiresAt.IsZero() {
					remaining := pairing.ExpiresAt.Sub(c.Now())
					if remaining > 0 {
						c.After(time.Second)
					}
					countdownText := "有效期剩余 " + formatCountdown(remaining)
					if remaining <= 0 {
						countdownText = "已过期，请点击刷新"
					}
					ui.Text(c, countdownText).
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				}

				if availableTargets >= 2 {
					ui.Row(c.Key("row-remote-pairing-target")).
						Gap(Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, "二维码入口").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
							for _, opt := range targetOptions {
								selected := opt.Value == selectedTarget
								btn := ui.ButtonBase(c.Key("btn-remote-pairing-target-"+opt.Value)).
									Label(opt.Label).
									Tooltip("二维码入口："+opt.Label).
									Padding(Space1, settingsCompactPadX).
									Radius(RadiusSM)
								if selected {
									btn.Background(ThemeAccent(c).Alpha(0.14).Over(ThemeSurface(c)))
								} else {
									btn.Background(ThemeHover(c))
								}
								if opt.URL == "" {
									btn.Disabled(true)
								}
								if btn.Clicked() && opt.URL != "" && !selected {
									setRemotePairingTarget(st, pairing, opt.Value)
								}
								btn.Children(func() {
									color := ThemeSecondaryText(c)
									if selected {
										color = ThemeAccent(c)
									}
									ui.Text(c, opt.Label).FontSize(TypeFootnote).TextColor(color)
								})
							}
						})
				}

				// 只要有域名就单独给一行，不管二维码入口有多少个。
				// 按钮文字要足够独特：UI 测试按文字定位，和其它复制按钮重名会命中别的按钮。
				if tailscaleFQDN != "" {
					fqdn := tailscaleFQDN
					ui.Row(c.Key("row-remote-pairing-magicdns")).
						Gap(Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, "MagicDNS 域名").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
							ui.Text(c, fqdn).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).SingleLine()
							renderRemoteCopyButton(c, st, "btn-remote-pairing-copy-fqdn", "复制 MagicDNS 域名", "已复制域名", "复制 MagicDNS 域名", fqdn, copiedKey, copiedAt)
						})
				}

				ui.Row(c.Key("row-remote-pairing-actions")).
					Gap(Space3).
					AlignItems(ui.Center).
					Children(func() {
						btnRefresh := ui.ButtonBase(c.Key("btn-remote-pair-refresh")).
							Label("刷新配对码").
							Tooltip("刷新将撤销所有已配对设备").
							Padding(Space1, settingsCompactPadX).
							Radius(RadiusSM).
							Background(ThemeHover(c))
						stylePairingButton(c, btnRefresh, busy)
						if btnRefresh.Clicked() && !busy {
							st.mu.Lock()
							st.remote.pairingConfirm = true
							st.mu.Unlock()
							st.invalidate()
						}
						btnRefresh.Children(func() {
							label := "刷新配对码"
							if busy {
								label = "刷新中…"
							}
							ui.Text(c, label).FontSize(TypeFootnote).TextColor(ThemeAccent(c))
						})

						if targetURL != "" {
							renderRemoteCopyButton(c, st, "btn-remote-copy-link", "复制配对链接", "已复制链接", "复制配对链接", targetURL, copiedKey, copiedAt)
						} else {
							disabledCopyButton(c, "btn-remote-copy-link", "复制配对链接")
						}

						if pairing.LANPairingURL != "" {
							renderRemoteCopyButton(c, st, "btn-remote-copy-lan-link", "复制局域网链接", "已复制局域网链接", "复制局域网配对链接", pairing.LANPairingURL, copiedKey, copiedAt)
						} else {
							disabledCopyButton(c, "btn-remote-copy-lan-link", "复制局域网链接")
						}
					})

				if pairingErr != "" {
					ui.Text(c, pairingErr).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
				}
			})

	case "expired":
		renderPairingNotice(c, "配对二维码已过期", renderPairNewButton, pairingErr, false)
	case "used":
		renderPairingNotice(c, "设备配对成功", renderPairNewButton, pairingErr, true)
	default:
		renderPairingNotice(c, "设备已完成配对", renderPairNewButton, pairingErr, false)
	}
}

// renderRemoteDevicesBlock 渲染已授权设备列表：错误、加载中、空态与逐台设备复用原有行结构。
func renderRemoteDevicesBlock(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	devices := st.remote.devices
	devicesLoaded := st.remote.devicesLoaded
	devicesErr := st.remote.devicesErr
	revokeConfirmID := st.remote.revokeConfirmID
	revokeBusyID := st.remote.revokeBusyID
	renameDeviceID := st.remote.renameDeviceID
	renameBusyID := st.remote.renameBusyID
	deviceActionErr := st.remote.deviceActionErr
	st.mu.Unlock()

	if devicesErr != "" {
		ui.Row(c.Key("row-remote-devices-err")).
			Padding(settingsRowPadY, settingsRowPadX).
			Children(func() {
				ui.Text(c, "无法获取已授权设备列表："+devicesErr).
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))
			})
		return
	}

	if !devicesLoaded {
		ui.Row(c.Key("row-remote-devices-loading")).
			Padding(settingsRowPadY, settingsRowPadX).
			Children(func() {
				ui.Text(c, "正在加载设备…").
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))
			})
		return
	}

	if len(devices) == 0 {
		ui.Column(c.Key("remote-devices-empty")).Padding(settingsRowPadY, settingsRowPadX).Children(func() {
			settingStatus(c, "row-remote-devices-empty-status", "暂无已授权设备")
		})
		return
	}

	if deviceActionErr != "" {
		ui.Row(c.Key("row-remote-device-action-err")).
			Padding(settingsRowPadY, settingsRowPadX).
			Children(func() {
				ui.Text(c, deviceActionErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			})
	}

	ui.Column(c.Key("remote-device-list")).Children(func() {
		for i, dev := range devices {
			if i > 0 {
				renderSettingsDivider(c, fmt.Sprintf("remote-device-divider-%d", i))
			}
			ui.Column(c.Key("remote-device-wrapper-"+dev.ID)).Padding(settingsRowPadY, settingsRowPadX).Children(func() {
				renderRemoteDeviceRow(c, st, dev, renameDeviceID, renameBusyID, revokeConfirmID, revokeBusyID)
			})
		}
	})
}
