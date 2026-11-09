package mcodeui

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// tailscaleAdminConsoleURL 是 Tailscale 管理台的 DNS 页，开启 MagicDNS 与 HTTPS 证书都在这里。
const tailscaleAdminConsoleURL = "https://login.tailscale.com/admin/dns"

// modalDragRemoteTailscaleReset 是「重置节点」确认框的拖拽 key（面板与标题行共用）。
const modalDragRemoteTailscaleReset = "modal-drag-remote-tailscale-reset"

// loadRemoteTailscaleStatus 异步拉取内嵌 Tailscale 入口的状态，结果写回 remotePageState。
func loadRemoteTailscaleStatus(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tailscaleBusy {
		st.mu.Unlock()
		return
	}
	st.remote.tailscaleBusy = true
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.RemoteTailscaleStatus(context.Background())

		st.mu.Lock()
		st.remote.tailscaleBusy = false
		st.remote.tailscaleLoaded = true
		st.remote.tailscaleStatus = status
		if err != nil {
			st.remote.tailscaleErr = err.Error()
		} else {
			st.remote.tailscaleErr = ""
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// refreshRemoteTailscaleStatus 静默刷新一次 Tailscale 状态：只置 tailscaleRefreshing，
// 不碰 tailscaleBusy，否则每次轮询都会让开关闪一下禁用。
// 失败时保留旧状态，也不写错误文案：轮询不该把页面弄脏。
func refreshRemoteTailscaleStatus(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tailscaleBusy || st.remote.tailscaleRefreshing {
		st.mu.Unlock()
		return
	}
	st.remote.tailscaleRefreshing = true
	// 记下发起时的代号：启停或重置会把状态整体换掉，这次结果就不要再写回去了。
	gen := st.remote.tailscaleGen
	st.mu.Unlock()

	go func() {
		status, err := client.RemoteTailscaleStatus(context.Background())

		st.mu.Lock()
		st.remote.tailscaleRefreshing = false
		// 用户刚触发启停或重置时在途结果已经过期，不能拿它覆盖状态。
		if err == nil && !st.remote.tailscaleBusy && st.remote.tailscaleGen == gen {
			st.remote.tailscaleStatus = status
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// setRemoteTailscaleEnabled 保存开关并异步通知后端启停内嵌节点，返回值用于刷新状态。
func setRemoteTailscaleEnabled(st *SettingsState, enabled bool) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tailscaleBusy {
		st.mu.Unlock()
		return
	}
	// 已持 st.mu，写 cfg 另需 cfgMu（顺序就是 mu → cfgMu）；save 自己拿 cfgMu 且不碰 mu，
	// 所以要先放开锁再落盘，不能拿着锁去调 updateAndSave（它会再拿一次 st.mu）。
	st.cfgMu.Lock()
	previous := st.cfg.Remote.TailscaleOn
	value := enabled
	st.cfg.Remote.TailscaleOn = &value
	st.cfgMu.Unlock()
	st.remote.tailscaleBusy = true
	st.remote.tailscaleErr = ""
	// 启停时递增代号，让在途的静默刷新结果作废。
	st.remote.tailscaleGen++
	st.mu.Unlock()
	st.invalidate()

	st.save()
	// st.errorMessage 与 cfg 共用 cfgMu：save 成功会把它清空，非空就是这次保存失败。
	st.cfgMu.Lock()
	saveErr := st.errorMessage
	st.cfgMu.Unlock()
	if saveErr != "" {
		// 保存失败就不通知后端启停：开关退回旧值，错误只写进 Tailscale 卡片。
		st.mu.Lock()
		st.cfgMu.Lock()
		st.cfg.Remote.TailscaleOn = previous
		st.cfgMu.Unlock()
		st.remote.tailscaleBusy = false
		st.remote.tailscaleErr = saveErr
		st.mu.Unlock()
		st.invalidate()
		return
	}

	go func() {
		status, err := client.SetRemoteTailscaleEnabled(context.Background(), enabled)

		st.mu.Lock()
		st.remote.tailscaleBusy = false
		st.remote.tailscaleLoaded = true
		st.remote.tailscaleStatus = status
		if err != nil {
			st.remote.tailscaleErr = err.Error()
		} else {
			st.remote.tailscaleErr = ""
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// resetRemoteTailscale 退出 tailnet 并清掉本机节点状态，完成后重新拉取一次状态。
func resetRemoteTailscale(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tailscaleBusy {
		st.mu.Unlock()
		return
	}
	// 已持 st.mu，写 cfg 另需 cfgMu（顺序就是 mu → cfgMu）；save 自拿 cfgMu 且不碰 mu，
	// 所以要先放开锁再落盘，不能拿着锁去调 updateAndSave（它会再拿一次 st.mu）。
	st.cfgMu.Lock()
	previous := st.cfg.Remote.TailscaleOn
	// 重置等于退出 tailnet，开关同步落回关闭，下次开启需要重新授权。
	disabled := false
	st.cfg.Remote.TailscaleOn = &disabled
	st.cfgMu.Unlock()
	st.remote.tailscaleBusy = true
	st.remote.tailscaleErr = ""
	// 重置也会整体换掉状态，递增代号让在途的静默刷新结果作废。
	st.remote.tailscaleGen++
	st.mu.Unlock()
	st.invalidate()

	st.save()
	// st.errorMessage 与 cfg 共用 cfgMu：save 成功会把它清空，非空就是这次保存失败。
	st.cfgMu.Lock()
	saveErr := st.errorMessage
	st.cfgMu.Unlock()
	if saveErr != "" {
		// 保存失败就不动后端：开关退回旧值，错误只写进 Tailscale 卡片。
		st.mu.Lock()
		st.cfgMu.Lock()
		st.cfg.Remote.TailscaleOn = previous
		st.cfgMu.Unlock()
		st.remote.tailscaleBusy = false
		st.remote.tailscaleErr = saveErr
		st.mu.Unlock()
		st.invalidate()
		return
	}

	go func() {
		err := client.ResetRemoteTailscale(context.Background())

		st.mu.Lock()
		st.remote.tailscaleBusy = false
		if err != nil {
			st.remote.tailscaleErr = err.Error()
		} else {
			st.remote.tailscaleErr = ""
			st.remote.tailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleDisabled}
		}
		st.mu.Unlock()

		if err != nil {
			st.invalidate()
			return
		}
		loadRemoteTailscaleStatus(st)
	}()
}

// remoteTailscaleStateText 把内嵌节点状态翻成一句界面文案。
func remoteTailscaleStateText(status RemoteTailscaleStatus) string {
	switch status.State {
	case RemoteTailscaleStarting:
		return "正在连接 tailnet"
	case RemoteTailscaleLoginRequired:
		return "等待登录：在浏览器中登录 Tailscale 并授权本机"
	case RemoteTailscaleNeedsHTTPS:
		return "需在 Tailscale 管理台开启 MagicDNS 与 HTTPS Certificates，开启后点重试"
	case RemoteTailscaleRunning:
		return "运行中"
	case RemoteTailscaleFailed:
		return "启动失败"
	default:
		if status.Error != "" {
			return status.Error
		}
		// 服务在跑但节点还没启动时也走到这里；服务本身没起来时后端会把原因放进 Error。
		return "未连接"
	}
}

// remoteTailscaleStateDesc 汇总状态文案与错误详情，供「连接状态」行展示。
func remoteTailscaleStateDesc(status RemoteTailscaleStatus, errText string) string {
	stateText := remoteTailscaleStateText(status)
	lines := []string{stateText}
	for _, extra := range []string{status.Error, errText} {
		if extra == "" || extra == stateText {
			continue
		}
		lines = append(lines, extra)
	}
	return strings.Join(lines, "\n")
}

// remoteTailscaleWhoisDesc 把最近一次通过 Tailscale 建立连接的身份拼成一行。
func remoteTailscaleWhoisDesc(status RemoteTailscaleStatus) string {
	parts := make([]string, 0, 2)
	for _, part := range []string{status.LastLoginName, status.LastNodeName} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "尚无通过 Tailscale 的连接"
	}
	return strings.Join(parts, "，")
}

// renderRemoteCopyButton 渲染复制某个值的小按钮：按钮文字、提示与复制完成后的回执文案
// 都由调用方给出，写进剪贴板的是 value 原文。
func renderRemoteCopyButton(c *ui.Context, st *SettingsState, key, label, copiedLabel, tooltip, value, copiedKey string, copiedAt time.Time) {
	shown := pairingCopyLabel(c, key, label, copiedLabel, copiedKey, copiedAt)
	CopyButton(c, CopyButtonProps{
		Key: key, Label: label, Tooltip: tooltip, Text: shown, Mode: CopyText,
		Copied: copiedKey == key && !copiedAt.IsZero() && c.Now().Sub(copiedAt) < 1500*time.Millisecond,
		Value:  func() string { return value },
		OnCopy: func() {
			markRemoteCopied(st, key, c.Now())
			st.invalidate()
		},
	})
}

// renderRemoteTailscaleRetryButton 渲染「重试」按钮：启动失败或拉取失败时再来一次。
func renderRemoteTailscaleRetryButton(c *ui.Context, busy bool, tooltip string, onClick func()) {
	btn := ui.ButtonBase(c.Key("btn-remote-tailscale-retry")).
		Label("重试").
		Tooltip(tooltip).
		Radius(RadiusSM).
		Padding(Space1, settingsCompactPadX).
		Background(ThemeHover(c))
	if busy {
		btn.Disabled(true)
	}
	if btn.Clicked() && !busy {
		onClick()
	}
	btn.Children(func() {
		ui.Text(c, "重试").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
	})
}

// remoteTailscaleSection 是 Tailscale 开关；状态、入口、最近连接由后面几个条件区块补上。
var remoteTailscaleSection = settingsSection{
	ID:    "remote.tailscale",
	Title: "Tailscale",
	Rows: append(append(append(append(append(append([]setting{{
		ID:      "row-remote-tailscale-enabled",
		Title:   "通过 Tailscale 访问",
		Aliases: []string{"Tailscale", "tailnet", "HTTPS", "MagicDNS", "节点", "登录"},
		Control: remoteTailscaleToggleControl{},
		Visible: remoteTailscaleVisible,
	}}, remoteTailscaleStateSection.Rows...), remoteTailscaleLoginSection.Rows...), remoteTailscaleEntrySection.Rows...), remoteTailscaleWhoisSection.Rows...), remoteTailscaleErrorSection.Rows...), remoteTailscaleNodeSection.Rows...),
}

// remoteTailscaleStateSection 是连接状态与重试按钮，只在开关打开时出现。
var remoteTailscaleStateSection = settingsSection{
	ID:    "remote.tailscale-state",
	Title: "",
	Rows: []setting{{
		Visible: remoteTailscaleEnabledState,
		ID:      "row-remote-tailscale-state",
		Title:   "连接状态",
		Aliases: []string{"状态", "重试", "管理台", "MagicDNS", "HTTPS Certificates"},
		Control: remoteTailscaleStateControl{},
		Status:  remoteTailscaleStateValue,
	}},
}

// remoteTailscaleErrorSection 在开关没打开或首次拉取失败时写出错误原文。
var remoteTailscaleErrorSection = settingsSection{
	ID:    "remote.tailscale-error",
	Title: "",
	Rows: []setting{{
		Visible: remoteTailscaleErrorVisible,
		ID:      "row-remote-tailscale-error",
		Title:   "Tailscale 错误",
		Aliases: []string{"失败", "原因"},
		Control: remoteValueControl{},
		Status: func(st *SettingsState, _ bool) string {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remote.tailscaleErr
		},
	}},
}

// remoteTailscaleLoginSection 是等待授权时的登录链接。
var remoteTailscaleLoginSection = settingsSection{
	ID:    "remote.tailscale-login",
	Title: "",
	Rows: []setting{{
		Visible: remoteTailscaleLoginVisible,
		ID:      "row-remote-tailscale-login",
		Title:   "授权链接",
		Aliases: []string{"登录", "授权", "打开", "复制"},
		Control: remoteTailscaleLoginControl{},
		Status: func(st *SettingsState, _ bool) string {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remote.tailscaleStatus.AuthURL
		},
	}},
}

// remoteTailscaleEntrySection 是节点上线后的 HTTPS 入口。
var remoteTailscaleEntrySection = settingsSection{
	ID:    "remote.tailscale-entry",
	Title: "",
	Rows: []setting{{
		Visible: remoteTailscaleEntryVisible,
		ID:      "row-remote-tailscale-entry",
		Title:   "HTTPS 入口",
		Aliases: []string{"入口", "地址", "复制"},
		Control: remoteTailscaleEntryControl{},
		Status: func(st *SettingsState, _ bool) string {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remote.tailscaleStatus.URL
		},
	}},
}

// remoteTailscaleWhoisSection 是最近一次连上来的设备。
var remoteTailscaleWhoisSection = settingsSection{
	ID:    "remote.tailscale-whois",
	Title: "",
	Rows: []setting{{
		Visible: remoteTailscaleWhoisVisible,
		ID:      "row-remote-tailscale-whois",
		Title:   "最近连接",
		Aliases: []string{"设备", "账号", "节点"},
		Control: remoteValueControl{},
		Status: func(st *SettingsState, _ bool) string {
			st.mu.Lock()
			defer st.mu.Unlock()
			return remoteTailscaleWhoisDesc(st.remote.tailscaleStatus)
		},
	}},
}

// remoteTailscaleNodeSection 是节点名称与重置节点；服务没跑时重置按钮不可用。
var remoteTailscaleNodeSection = settingsSection{
	ID:    "remote.tailscale-node",
	Title: "Tailscale 节点",
	Rows: []setting{
		{
			ID:      "row-remote-tailscale-hostname",
			Title:   "节点名称",
			Aliases: []string{"hostname", "设备名", "mcode"},
			Control: remoteTailscaleHostnameControl{},
			Status:  remoteTailscaleHostnameStatus,
		},
		{
			ID:      "row-remote-tailscale-reset",
			Title:   "重置节点",
			Aliases: []string{"重置", "退出 tailnet", "删除节点"},
			Control: remoteTailscaleResetControl{},
			Status:  remoteTailscaleResetStatus,
		},
	},
}

// renderRemoteTailscaleSection 保留旧入口：内容已迁到页面树的 remote.tailscale*。
func renderRemoteTailscaleSection(c *ui.Context, st *SettingsState) {
	renderSettingsTreeSection(c, st, remoteTailscaleSection, false)
}

// remoteTailscaleVisible 判断 Tailscale 开关区块是否出现：停服且从未启用时整块收起。
func remoteTailscaleVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return remoteServiceRunning(st.remote.status) || st.cfg.Remote.TailscaleEnabled() || st.remote.tailscaleErr != ""
}

// remoteTailscaleEnabledState 判断开关是否打开。
func remoteTailscaleEnabledState(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cfg.Remote.TailscaleEnabled()
}

// remoteTailscaleErrorVisible 只在有错误原文时出现。
func remoteTailscaleErrorVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.remote.tailscaleErr != ""
}

// remoteTailscaleLoginVisible 只有等待授权且拿到授权链接时才出现。
func remoteTailscaleLoginVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	status := st.remote.tailscaleStatus
	return status.State == RemoteTailscaleLoginRequired && status.AuthURL != ""
}

// remoteTailscaleEntryVisible 只有节点运行且入口就绪时才出现。
func remoteTailscaleEntryVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	status := st.remote.tailscaleStatus
	return status.State == RemoteTailscaleRunning && status.URL != ""
}

// remoteTailscaleWhoisVisible 只有节点运行时才有最近连接。
func remoteTailscaleWhoisVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.remote.tailscaleStatus.State == RemoteTailscaleRunning
}

// remoteTailscaleStateValue 写出连接状态；错误原文并进同一行。
func remoteTailscaleStateValue(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return remoteTailscaleStateDesc(st.remote.tailscaleStatus, st.remote.tailscaleErr)
}

// remoteTailscaleToggleControl 是 Tailscale 开关：启停内嵌节点。
type remoteTailscaleToggleControl struct{}

func (remoteTailscaleToggleControl) render(c *ui.Context, st *SettingsState, s setting) {
	toggleFunc(
		func(st *SettingsState) (bool, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.cfg.Remote.TailscaleEnabled(), st.remote.tailscaleBusy
		},
		setRemoteTailscaleEnabled,
	).render(c, st, s)
}

func (remoteTailscaleToggleControl) reset(cfg *config.Config) { cfg.Remote.TailscaleOn = nil }

// remoteTailscalePoll 在需要时安排状态刷新：瞬态每 2 秒、运行中每 5 秒。
func remoteTailscalePoll(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	status := st.remote.tailscaleStatus
	polledAt := st.remote.tailscalePolledAt
	enabled := st.cfg.Remote.TailscaleEnabled()
	st.mu.Unlock()

	// starting、login_required 是瞬态：每 2 秒拉一次状态并重绘，等 tailnet 连接或用户登录后自动切换。
	// running 但还没有 HTTPS 入口时同样要继续轮询：节点刚上线时入口要等一会儿才开始服务。
	transient := status.State == RemoteTailscaleStarting || status.State == RemoteTailscaleLoginRequired ||
		(status.State == RemoteTailscaleRunning && status.URL == "")
	if transient {
		if c.Now().Sub(polledAt) >= 2*time.Second {
			st.mu.Lock()
			st.remote.tailscalePolledAt = c.Now()
			st.mu.Unlock()
			// 用静默刷新：整页重载会置 tailscaleBusy，login_required 期间开关会每 2 秒变灰一次，点击被丢弃。
			refreshRemoteTailscaleStatus(st)
		}
		c.After(2 * time.Second)
		return
	}
	if enabled && status.State == RemoteTailscaleRunning {
		// 手机随时可能连上，running 时也要低频刷新最近连接。
		if c.Now().Sub(polledAt) >= 5*time.Second {
			st.mu.Lock()
			st.remote.tailscalePolledAt = c.Now()
			st.mu.Unlock()
			refreshRemoteTailscaleStatus(st)
		}
		c.After(5 * time.Second)
	}
}

// remoteTailscaleStateControl 是状态行的操作：打开管理台或在失败后重试。
type remoteTailscaleStateControl struct{}

func (remoteTailscaleStateControl) render(c *ui.Context, st *SettingsState, s setting) {
	remoteTailscalePoll(c, st)

	st.mu.Lock()
	status := st.remote.tailscaleStatus
	busy := st.remote.tailscaleBusy
	errText := st.remote.tailscaleErr
	st.mu.Unlock()

	ui.Row(c.Key("row-remote-tailscale-state-actions")).Gap(Space3).AlignItems(ui.Center).Children(func() {
		if status.State == RemoteTailscaleNeedsHTTPS {
			btn := ui.ButtonBase(c.Key("btn-remote-tailscale-console")).
				Label("打开管理台").
				Tooltip("打开 Tailscale 管理台的 DNS 页").
				Radius(RadiusSM).
				Padding(Space1, settingsCompactPadX).
				Background(ThemeHover(c))
			if btn.Clicked() {
				c.OpenURL(tailscaleAdminConsoleURL)
			}
			btn.Children(func() {
				ui.Text(c, "打开管理台").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
			})
		}

		switch {
		case status.State == RemoteTailscaleNeedsHTTPS || status.State == RemoteTailscaleFailed:
			// 失败与待开启 HTTPS 都能只靠再次启用重试：后者先去管理台打开开关，回来再点。
			renderRemoteTailscaleRetryButton(c, busy, "重新启动内嵌 Tailscale 节点", func() {
				setRemoteTailscaleEnabled(st, true)
			})
		case status.State == "" && errText != "":
			// 首次拉取状态就失败：状态为空串，重试等于再拉一次。
			renderRemoteTailscaleRetryButton(c, busy, "重新拉取 Tailscale 节点状态", func() {
				loadRemoteTailscaleStatus(st)
			})
		}
	})
}

// remoteTailscaleLoginControl 是授权链接的两个操作按钮。
type remoteTailscaleLoginControl struct{}

func (remoteTailscaleLoginControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	authURL := st.remote.tailscaleStatus.AuthURL
	copiedKey := st.remote.copiedKey
	copiedAt := st.remote.copiedAt
	st.mu.Unlock()
	if authURL == "" {
		return
	}

	ui.Row(c.Key("row-remote-tailscale-login-actions")).Gap(Space3).AlignItems(ui.Center).Children(func() {
		openBtn := ui.ButtonBase(c.Key("btn-remote-tailscale-login-open")).
			Label("打开授权页").
			Tooltip("在浏览器中打开 Tailscale 授权页").
			Radius(RadiusSM).
			Padding(Space1, settingsCompactPadX).
			Background(ThemeHover(c))
		if openBtn.Clicked() {
			c.OpenURL(authURL)
		}
		openBtn.Children(func() {
			ui.Text(c, "打开授权页").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
		})

		copyKey := "btn-remote-tailscale-login-copy"
		copyBtn := ui.ButtonBase(c.Key(copyKey)).
			Label("复制链接").
			Tooltip("复制授权链接").
			Radius(RadiusSM).
			Padding(Space1, settingsCompactPadX).
			Background(ThemeHover(c))
		if copyBtn.Clicked() {
			copyAt := c.Now()
			c.WriteClipboard(authURL)
			markRemoteCopied(st, copyKey, copyAt)
			copiedKey, copiedAt = copyKey, copyAt
		}
		copyBtn.Children(func() {
			label := pairingCopyLabel(c, copyKey, "复制链接", "已复制链接", copiedKey, copiedAt)
			ui.Text(c, label).FontSize(TypeFootnote).TextColor(ThemeAccent(c))
		})
	})
}

// remoteTailscaleEntryControl 是 HTTPS 入口的复制按钮。
type remoteTailscaleEntryControl struct{}

func (remoteTailscaleEntryControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	entryURL := st.remote.tailscaleStatus.URL
	copiedKey := st.remote.copiedKey
	copiedAt := st.remote.copiedAt
	st.mu.Unlock()
	if entryURL == "" {
		return
	}
	// 可见文字与 a11y Label 都写成「复制入口」：局域网区块的复制按钮也叫「复制」，
	// 同名会让 tt.Find 命中上面那个按钮，这里必须保持唯一。
	renderRemoteCopyButton(c, st, "btn-remote-tailscale-copy-entry", "复制入口", "已复制入口", "复制 HTTPS 入口", entryURL, copiedKey, copiedAt)
}

// remoteTailscaleHostnameControl 是 tailnet 里的节点名，非法输入只提示、不落盘。
type remoteTailscaleHostnameControl struct{}

func (remoteTailscaleHostnameControl) render(c *ui.Context, st *SettingsState, s setting) {
	remoteTailscaleHostnameControl{}.renderDisabled(c, st, s, false)
}

func (remoteTailscaleHostnameControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	st.mu.Lock()
	if !st.remote.tailscaleHostInit {
		st.remote.tailscaleHostnameText = st.cfg.Remote.TailscaleHostname
		st.remote.tailscaleHostInit = true
	}
	st.mu.Unlock()

	textFunc(
		func(st *SettingsState) (string, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remote.tailscaleHostnameText, st.remote.tailscaleBusy
		},
		func(st *SettingsState, text string) {
			st.mu.Lock()
			st.remote.tailscaleHostnameText = text
			st.mu.Unlock()
			applyRemoteTailscaleHostname(st, text)
		},
		"mcode",
	).renderDisabled(c, st, s, disabled)
}

func (remoteTailscaleHostnameControl) reset(cfg *config.Config) { cfg.Remote.TailscaleHostname = "" }

// remoteTailscaleHostnameStatus 只在节点名不合法时写出原因。
func remoteTailscaleHostnameStatus(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.remote.tailscaleHostnameErr
}

// remoteTailscaleResetControl 是退出 tailnet 并删除节点状态，执行前必须确认。
type remoteTailscaleResetControl struct{}

func (remoteTailscaleResetControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	busy := st.remote.tailscaleBusy
	resetConfirm := st.remote.tailscaleResetConfirm
	// 确认框只由本行渲染，而本行只在 Tailscale 块出现时才算：服务没跑时按钮必须禁用，
	// 否则点了只会置一个看不见的标记，等服务启动后突然弹出确认框。
	running := remoteServiceRunning(st.remote.status)
	st.mu.Unlock()

	// Label 用行内按钮文字「重置」而不是行标题「重置节点」：
	// 行标题是普通文本、点了没有任何作用，同名会让 tt.Find 命中标题。
	btn := ui.ButtonBase(c.Key("btn-remote-tailscale-reset")).
		Label("重置").
		Tooltip("退出 tailnet 并删除本机节点状态").
		Radius(RadiusSM).
		Padding(Space1, settingsCompactPadX).
		Background(ThemeHover(c))
	if busy || !running {
		btn.Disabled(true)
	}
	if btn.Clicked() && !busy && running {
		st.mu.Lock()
		st.remote.tailscaleResetConfirm = true
		st.mu.Unlock()
		st.invalidate()
	}
	btn.Children(func() {
		ui.Text(c, "重置").FontSize(TypeFootnote).TextColor(ThemeDanger(c))
	})

	if resetConfirm {
		renderRemoteTailscaleResetConfirmModal(c, st)
	}
}

func (remoteTailscaleResetControl) reset(cfg *config.Config) { cfg.Remote.TailscaleOn = nil }

// remoteTailscaleResetStatus 在服务没跑时写明重置为什么不可用。
func remoteTailscaleResetStatus(st *SettingsState, _ bool) string {
	if remoteRunningState(st) {
		return ""
	}
	return "需先启动远程控制服务"
}

// renderRemoteTailscaleResetConfirmModal 渲染重置节点前的确认框。
func renderRemoteTailscaleResetConfirmModal(c *ui.Context, st *SettingsState) {
	closeModal := func() {
		st.mu.Lock()
		st.remote.tailscaleResetConfirm = false
		st.mu.Unlock()
		st.invalidate()
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragRemoteTailscaleReset,
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "重置 Tailscale 节点？",
		Body:         "本机会退出 tailnet 并删除节点状态，HTTPS 入口随之下线；下次开启需要重新在浏览器中授权。",
		ConfirmLabel: "重置节点",
		ConfirmA11y:  "确认重置 Tailscale 节点",
		CancelLabel:  "取消",
		CancelA11y:   "取消重置 Tailscale 节点",
		OnConfirm: func() {
			st.mu.Lock()
			st.remote.tailscaleResetConfirm = false
			st.mu.Unlock()
			resetRemoteTailscale(st)
		},
		OnCancel: closeModal,
	})
}

// remoteTailscaleHostnameErrText 是节点名不合法时的提示，写在一行的说明里。
const remoteTailscaleHostnameErrText = "只能用字母、数字和连字符，最长 63 位，不能以连字符开头或结尾"

// applyRemoteTailscaleHostname 校验并保存节点名：空或合法才落盘，不合法只记错误文案。
// 不能持 st.mu 调用：updateAndSave 会自己拿 mu 与 cfgMu。
func applyRemoteTailscaleHostname(st *SettingsState, hostname string) {
	hostname = strings.TrimSpace(hostname)
	if hostname != "" && !config.ValidTailscaleHostname(hostname) {
		st.mu.Lock()
		st.remote.tailscaleHostnameErr = remoteTailscaleHostnameErrText
		st.mu.Unlock()
		st.invalidate()
		return
	}

	st.mu.Lock()
	st.remote.tailscaleHostnameErr = ""
	changed := hostname != st.cfg.Remote.TailscaleHostname
	st.mu.Unlock()
	if !changed {
		st.invalidate()
		return
	}
	st.updateAndSave(func(cfg *config.Config) {
		cfg.Remote.TailscaleHostname = hostname
	})
	st.invalidate()
}
