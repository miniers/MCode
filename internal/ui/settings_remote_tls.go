package mcodeui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// pickRemoteTLSFileDialog 唤起系统文件选择对话框，测试时可替换。
var pickRemoteTLSFileDialog = func(title, message string) (string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:   title,
		Message: message,
	})
	if err != nil {
		return "", false, err
	}
	if len(paths) == 0 {
		return "", false, nil
	}
	return paths[0], true, nil
}

// modalDragRemoteTLSRemove 是「移除证书」确认框的拖拽 key（面板与标题行共用）。
const modalDragRemoteTLSRemove = "modal-drag-remote-tls-remove"

// remoteTLSSection 是「自定义域名 HTTPS」：开关、域名、端口、Token、入口、证书状态与操作。
// TLSListen 为空即后端约定的「不启用 HTTPS」，界面上必须有显式开关，
// 否则改端口会隐式开启监听，用户也没地方关闭。
var remoteTLSSection = settingsSection{
	ID:      "remote.tls",
	Title:   "自定义域名 HTTPS",
	Visible: remoteTLSVisible,
	Rows: []setting{
		{
			ID:      "row-remote-tls-enabled",
			Title:   "启用 HTTPS",
			Aliases: []string{"HTTPS", "TLS", "证书", "Cloudflare", "开关", "开启"},
			Control: remoteTLSToggleControl{},
		},
		{
			ID:      "row-remote-tls-domain",
			Title:   "域名",
			Aliases: []string{"域名", "Cloudflare", "DNS", "托管", "例外"},
			Control: text(val(func(c *config.Config) *string { return &c.Remote.TLSDomain }, ""), "mcode.example.com"),
			Enabled: remoteTLSEnabled,
		},
		{
			ID:      "row-remote-tls-port",
			Title:   "HTTPS 端口",
			Aliases: []string{"端口", "8443", "443"},
			Control: remoteTLSPortControl{},
			Enabled: remoteTLSEnabled,
			Status:  remoteTLSDisabledStatus,
		},
		{
			ID:      "row-remote-tls-token",
			Title:   "Cloudflare API 令牌",
			Aliases: []string{"Token", "令牌", "API", "钥匙串", "保存", "清除"},
			Control: remoteTLSTokenControl{},
			Enabled: remoteTLSEnabled,
		},
		{
			ID:      "row-remote-tls-entry",
			Title:   "HTTPS 入口",
			Aliases: []string{"入口", "地址", "复制"},
			Control: remoteTLSEntryControl{},
			Status:  remoteTLSEntryStatus,
		},
		{
			ID:      "row-remote-tls-status",
			Title:   "证书状态",
			Aliases: []string{"证书", "到期", "颁发者", "域名"},
			Control: remoteValueControl{},
			Status:  remoteTLSStatusValue,
		},
		{
			ID:      "row-remote-tls-actions",
			Title:   "证书操作",
			Aliases: []string{"申请证书", "导入证书", "移除证书", "Let's Encrypt"},
			Control: remoteTLSActionsControl{},
			Status:  remoteTLSFeedback,
		},
	},
}

// remoteTLSVisible 判断 HTTPS 区块是否出现：运行中或曾配置过证书相关项时才显示。
func remoteTLSVisible(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return remoteServiceRunning(st.remote.status) ||
		strings.TrimSpace(st.cfg.Remote.TLSListen) != "" ||
		strings.TrimSpace(st.cfg.Remote.TLSDomain) != "" ||
		st.remote.tls.StartedURL != ""
}

// remoteTLSEnabled 判断 HTTPS 是否已启用，用来表达域名、端口与 Token 的条件可用。
func remoteTLSEnabled(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return strings.TrimSpace(st.cfg.Remote.TLSListen) != ""
}

// remoteTLSDisabledStatus 说明端口与 Token 为什么现在不可改。
func remoteTLSDisabledStatus(st *SettingsState, _ bool) string {
	if remoteTLSEnabled(st) {
		return ""
	}
	return "先启用 HTTPS"
}

// remoteTLSToggleControl 是 HTTPS 开关：开启时按当前端口建立监听，关闭时清空监听地址。
type remoteTLSToggleControl struct{}

func (remoteTLSToggleControl) render(c *ui.Context, st *SettingsState, s setting) {
	toggleFunc(
		func(st *SettingsState) (bool, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return strings.TrimSpace(st.cfg.Remote.TLSListen) != "", st.remote.tlsBusy
		},
		func(st *SettingsState, on bool) {
			// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），save 在两把锁之外调。
			st.mu.Lock()
			portText := st.remote.tlsPortText
			st.cfgMu.Lock()
			if on {
				st.cfg.Remote.TLSListen = remoteTLSListenWithPort("", remoteTLSTextPort(portText))
			} else {
				st.cfg.Remote.TLSListen = ""
			}
			st.cfgMu.Unlock()
			st.mu.Unlock()
			st.save()
		},
	).render(c, st, s)
}

func (remoteTLSToggleControl) reset(cfg *config.Config) { cfg.Remote.TLSListen = "" }

// remoteTLSPortControl 是 HTTPS 端口：只在 HTTPS 已启用时写回监听地址。
type remoteTLSPortControl struct{}

func (remoteTLSPortControl) render(c *ui.Context, st *SettingsState, s setting) {
	remoteTLSPortControl{}.renderDisabled(c, st, s, false)
}

func (remoteTLSPortControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	textFunc(
		func(st *SettingsState) (string, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remote.tlsPortText, false
		},
		func(st *SettingsState, text string) {
			st.mu.Lock()
			st.remote.tlsPortText = text
			enabled := strings.TrimSpace(st.cfg.Remote.TLSListen) != ""
			st.mu.Unlock()
			port, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil || port < 1 || port > 65535 {
				return
			}
			// 只在 HTTPS 已启用时写回：否则改端口会隐式开启 TLS 监听。
			if !enabled {
				return
			}
			commitSettingsControl(st, func(cfg *config.Config) {
				cfg.Remote.TLSListen = remoteTLSListenWithPort(cfg.Remote.TLSListen, port)
			})
		},
		strconv.Itoa(config.DefaultRemoteTLSPort),
	).renderDisabled(c, st, s, disabled)
}

func (remoteTLSPortControl) reset(cfg *config.Config) { cfg.Remote.TLSListen = "" }

// remoteTLSTokenControl 是 Cloudflare API 令牌：已保存只写「已保存」，输入框只收新值。
type remoteTLSTokenControl struct{}

func (remoteTLSTokenControl) render(c *ui.Context, st *SettingsState, s setting) {
	remoteTLSTokenControl{}.renderDisabled(c, st, s, false)
}

func (remoteTLSTokenControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	st.mu.Lock()
	hasToken := st.remote.tlsHasToken
	busy := st.remote.tlsBusy
	st.mu.Unlock()

	ui.Row(c.Key("row-remote-tls-token-controls")).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			if hasToken {
				ui.Text(c, "已保存").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			}

			placeholder := "填入 API Token"
			if hasToken {
				placeholder = "输入新 Token 以替换"
			}
			textFunc(
				func(st *SettingsState) (string, bool) {
					st.mu.Lock()
					defer st.mu.Unlock()
					return st.remote.tlsTokenInput, st.remote.tlsBusy
				},
				func(st *SettingsState, text string) {
					st.mu.Lock()
					st.remote.tlsTokenInput = text
					st.mu.Unlock()
				},
				placeholder,
			).withSecret().renderDisabled(c, st, s, disabled)

			st.mu.Lock()
			trimmed := strings.TrimSpace(st.remote.tlsTokenInput)
			st.mu.Unlock()
			saveBtn := ui.ButtonBase(c.Key("btn-remote-tls-token-save")).
				Label("保存 Token").
				Tooltip("保存 Cloudflare API Token").
				Radius(RadiusSM).
				Padding(Space1, settingsCompactPadX).
				Background(ThemeHover(c))
			if disabled || busy || trimmed == "" {
				saveBtn.Disabled(true)
			}
			if saveBtn.Clicked() && !disabled && !busy && trimmed != "" {
				saveRemoteCloudflareToken(st, trimmed)
			}
			saveBtn.Children(func() {
				ui.Text(c, "保存").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
			})

			if hasToken {
				clearBtn := ui.ButtonBase(c.Key("btn-remote-tls-token-clear")).
					Label("清除 Token").
					Tooltip("清除已保存的 Cloudflare API Token").
					Radius(RadiusSM).
					Padding(Space1, settingsCompactPadX).
					Background(ThemeHover(c))
				if disabled || busy {
					clearBtn.Disabled(true)
				}
				if clearBtn.Clicked() && !disabled && !busy {
					saveRemoteCloudflareToken(st, "")
				}
				clearBtn.Children(func() {
					ui.Text(c, "清除").FontSize(TypeFootnote).TextColor(ThemeDanger(c))
				})
			}
		})
}

// remoteTLSEntryControl 是 HTTPS 入口：没真正启动监听时不给可复制地址。
type remoteTLSEntryControl struct{}

func (remoteTLSEntryControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	startedURL := st.remote.tls.StartedURL
	st.mu.Unlock()
	if startedURL == "" {
		return
	}
	ui.Row(c.Key("row-remote-tls-entry-control")).Gap(Space3).AlignItems(ui.Center).Children(func() {
		ui.Text(c, startedURL).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).SingleLine()
		CopyButton(c, CopyButtonProps{Key: "btn-copy-tls-entry", Label: "复制 HTTPS 入口", Tooltip: "复制 HTTPS 入口", Text: "复制", Mode: CopyText, Value: func() string { return startedURL }})
	})
}

// remoteTLSEntryStatus 在监听没起来时写明入口不可用，而不是画一个空地址。
func remoteTLSEntryStatus(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.remote.tls.StartedURL != "" {
		return ""
	}
	return "未生效：申请或导入证书后可用"
}

// remoteTLSStatusValue 汇总证书域名、颁发者、到期时间、来源与过期/不匹配提示。
func remoteTLSStatusValue(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	text := remoteTLSStatusDetail(st.remote.tls)
	if badge := remoteTLSStatusBadge(st.remote.tls, st.cfg.Remote.TLSDomain, time.Now()); badge != "" {
		text += " · " + badge
	}
	return text
}

// remoteTLSFeedback 写出证书操作的进行中、成功或失败结果。
func remoteTLSFeedback(st *SettingsState, _ bool) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	statusErr := st.remote.tls.Error
	if statusErr == "" {
		statusErr = st.remote.tlsErr
	}
	if st.remote.tlsBusy {
		if st.remote.tlsResult != "" {
			return st.remote.tlsResult
		}
		return "正在处理证书操作…"
	}
	if statusErr != "" {
		if st.remote.tlsResult != "" {
			return st.remote.tlsResult + " · " + statusErr
		}
		return statusErr
	}
	return st.remote.tlsResult
}

// remoteTLSActionsControl 是证书操作：申请、导入与移除，移除前弹确认框。
type remoteTLSActionsControl struct{}

func (remoteTLSActionsControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	tls := st.remote.tls
	tlsBusy := st.remote.tlsBusy
	tlsRemoveConfirm := st.remote.tlsRemoveConfirm
	hasToken := st.remote.tlsHasToken
	cfgDomain := st.cfg.Remote.TLSDomain
	st.mu.Unlock()

	// 移除按钮只看证书本身是否装好：Configured 仅表示 TLSListen 非空，
	// 只填过端口没签发时不该允许「移除证书」。
	certInstalled := remoteTLSCertInstalled(tls)
	ui.Row(c.Key("row-remote-tls-action-buttons")).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			issueBtn := ui.ButtonBase(c.Key("btn-remote-tls-issue")).
				Label("申请证书").
				Tooltip("通过 Cloudflare DNS-01 申请证书").
				Radius(RadiusSM).
				Padding(Space1, settingsCompactPadX).
				Background(ThemeHover(c))
			if tlsBusy || cfgDomain == "" || !hasToken {
				issueBtn.Disabled(true)
			}
			if issueBtn.Clicked() && !tlsBusy && cfgDomain != "" && hasToken {
				issueRemoteTLSCertificate(st)
			}
			issueBtn.Children(func() {
				ui.Text(c, "申请证书").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
			})

			importBtn := ui.ButtonBase(c.Key("btn-remote-tls-import")).
				Label("导入证书…").
				Tooltip("导入证书链与私钥").
				Radius(RadiusSM).
				Padding(Space1, settingsCompactPadX).
				Background(ThemeHover(c))
			if tlsBusy {
				importBtn.Disabled(true)
			}
			if importBtn.Clicked() && !tlsBusy {
				importRemoteTLSCertificate(st)
			}
			importBtn.Children(func() {
				ui.Text(c, "导入证书…").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
			})

			removeBtn := ui.ButtonBase(c.Key("btn-remote-tls-remove")).
				Label("移除证书").
				Tooltip("移除已安装的证书").
				Radius(RadiusSM).
				Padding(Space1, settingsCompactPadX).
				Background(ThemeHover(c))
			if tlsBusy || !certInstalled {
				removeBtn.Disabled(true)
			}
			if removeBtn.Clicked() && !tlsBusy && certInstalled {
				// 移除后 HTTPS 入口立即下线，与撤销设备一样先二次确认。
				st.mu.Lock()
				st.remote.tlsRemoveConfirm = true
				st.mu.Unlock()
				st.invalidate()
			}
			removeBtn.Children(func() {
				ui.Text(c, "移除证书").FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			})
		})

	if tlsRemoveConfirm {
		renderRemoteTLSRemoveConfirmModal(c, st)
	}
}

// renderRemoteTLSRemoveConfirmModal 渲染移除证书前的确认框。
func renderRemoteTLSRemoveConfirmModal(c *ui.Context, st *SettingsState) {
	closeModal := func() {
		st.mu.Lock()
		st.remote.tlsRemoveConfirm = false
		st.mu.Unlock()
		st.invalidate()
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragRemoteTLSRemove,
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "移除证书？",
		Body:         "移除后 HTTPS 入口会立即下线，手机需要改用局域网 HTTP 地址访问；域名与 Cloudflare API Token 会保留，可重新申请或导入证书。",
		ConfirmLabel: "移除证书",
		ConfirmA11y:  "确认移除证书",
		CancelLabel:  "取消",
		CancelA11y:   "取消移除证书",
		OnConfirm: func() {
			st.mu.Lock()
			st.remote.tlsRemoveConfirm = false
			st.mu.Unlock()
			removeRemoteTLSCertificate(st)
		},
		OnCancel: closeModal,
	})
}

// remoteTLSCertInstalled 判断证书是否真的已装好。Configured 只表示 TLSListen 非空，
// 证书自身的信息（域名/到期时间）才代表证书与私钥已经安装。
func remoteTLSCertInstalled(tls RemoteTLSStatus) bool {
	return len(tls.CertDomains) > 0 || !tls.NotAfter.IsZero()
}

// remoteTLSTextPort 解析 HTTPS 端口输入框；输入非法时回落到默认 TLS 端口。
func remoteTLSTextPort(text string) int {
	if port, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && port >= 1 && port <= 65535 {
		return port
	}
	return remoteTLSListenPort("")
}

// remoteTLSStatusDetail 汇总证书域名、颁发者、到期时间与来源。
func remoteTLSStatusDetail(tls RemoteTLSStatus) string {
	if !remoteTLSCertInstalled(tls) {
		return "尚未安装证书"
	}
	var parts []string
	domains := tls.CertDomains
	if len(domains) == 0 && tls.Domain != "" {
		domains = []string{tls.Domain}
	}
	if len(domains) > 0 {
		parts = append(parts, strings.Join(domains, ", "))
	}
	issuer := tls.Issuer
	if issuer == "" {
		issuer = "未知颁发者"
	}
	parts = append(parts, issuer)
	if !tls.NotAfter.IsZero() {
		parts = append(parts, "到期 "+tls.NotAfter.Format("2006-01-02"))
	}
	if source := remoteTLSSourceLabel(tls.Source); source != "" {
		parts = append(parts, source)
	}
	return strings.Join(parts, " · ")
}

func remoteTLSSourceLabel(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "acme":
		return "自动申请"
	case "import", "imported", "manual":
		return "手动导入"
	default:
		return source
	}
}

// remoteTLSStatusBadge 在证书过期或与配置域名不匹配时返回警示文字。
func remoteTLSStatusBadge(tls RemoteTLSStatus, configuredDomain string, now time.Time) string {
	if !tls.NotAfter.IsZero() && tls.NotAfter.Before(now) {
		return "证书已过期"
	}
	if configuredDomain != "" {
		certDomains := tls.CertDomains
		if len(certDomains) == 0 && tls.Domain != "" {
			certDomains = []string{tls.Domain}
		}
		if len(certDomains) > 0 && !remoteCertCoversDomain(certDomains, configuredDomain) {
			return "证书与域名不匹配"
		}
	}
	return ""
}

// remoteCertCoversDomain 判断证书域名列表是否覆盖指定主机，支持单层通配符。
func remoteCertCoversDomain(domains []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return true
	}
	for _, raw := range domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if domain == "" {
			continue
		}
		if domain == host {
			return true
		}
		if strings.HasPrefix(domain, "*.") {
			suffix := domain[1:]
			if strings.HasSuffix(host, suffix) {
				label := strings.TrimSuffix(host, suffix)
				if label != "" && !strings.Contains(label, ".") {
					return true
				}
			}
		}
	}
	return false
}

// loadRemoteTLSStatus 异步获取远程 TLS 证书状态与 Token 保存情况。
func loadRemoteTLSStatus(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tlsBusy {
		st.mu.Unlock()
		return
	}
	st.remote.tlsBusy = true
	st.remote.tlsErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.RemoteTLSStatus(context.Background())
		hasToken := client.HasRemoteCloudflareToken()
		st.mu.Lock()
		st.remote.tlsBusy = false
		st.remote.tlsLoaded = true
		st.remote.tlsHasToken = hasToken || status.HasCloudflareToken
		if err != nil {
			st.remote.tlsErr = err.Error()
		} else {
			st.remote.tls = status
			st.remote.tlsErr = ""
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// saveRemoteCloudflareToken 异步保存或清除 Cloudflare API Token。
func saveRemoteCloudflareToken(st *SettingsState, token string) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tlsBusy {
		st.mu.Unlock()
		return
	}
	st.remote.tlsBusy = true
	st.remote.tlsResult = ""
	st.remote.tlsResultErr = false
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.SaveRemoteCloudflareToken(token)
		st.mu.Lock()
		st.remote.tlsBusy = false
		if err != nil {
			st.remote.tlsResult = err.Error()
			st.remote.tlsResultErr = true
		} else {
			st.remote.tlsHasToken = token != ""
			st.remote.tls.HasCloudflareToken = token != ""
			if token != "" {
				st.remote.tlsTokenInput = ""
				st.remote.tlsResult = "Cloudflare API Token 已保存"
			} else {
				st.remote.tlsResult = "Cloudflare API Token 已清除"
			}
			st.remote.tlsResultErr = false
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// updateRemoteTLSStatus 把证书操作返回的状态写回页面。
func updateRemoteTLSStatus(st *SettingsState, status RemoteTLSStatus, err error, successMessage string, failureMessage string) {
	st.mu.Lock()
	st.remote.tlsBusy = false
	st.remote.tlsLoaded = true
	if err != nil {
		st.remote.tlsResult = failureMessage + "：" + err.Error()
		st.remote.tlsResultErr = true
	} else {
		st.remote.tls = status
		st.remote.tlsHasToken = status.HasCloudflareToken || st.remote.tlsHasToken
		st.remote.tlsResult = successMessage
		st.remote.tlsResultErr = false
	}
	st.mu.Unlock()
	st.invalidate()
}

func issueRemoteTLSCertificate(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tlsBusy {
		st.mu.Unlock()
		return
	}
	st.remote.tlsBusy = true
	st.remote.tlsResult = "正在申请证书…"
	st.remote.tlsResultErr = false
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.IssueRemoteTLSCertificate(context.Background())
		updateRemoteTLSStatus(st, status, err, "证书申请并安装成功", "证书申请失败")
	}()
}

func importRemoteTLSCertificate(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tlsBusy {
		st.mu.Unlock()
		return
	}
	st.remote.tlsBusy = true
	st.remote.tlsResult = "正在导入证书…"
	st.remote.tlsResultErr = false
	st.mu.Unlock()
	st.invalidate()

	go func() {
		chainPath, ok, err := pickRemoteTLSFileDialog("选择证书链文件", "请选择证书链 PEM 文件（如 fullchain.pem）")
		if err != nil {
			updateRemoteTLSStatus(st, RemoteTLSStatus{}, err, "", "选择证书链失败")
			return
		}
		if !ok {
			st.mu.Lock()
			st.remote.tlsBusy = false
			st.remote.tlsResult = ""
			st.mu.Unlock()
			st.invalidate()
			return
		}

		keyPath, ok, err := pickRemoteTLSFileDialog("选择私钥文件", "请选择私钥 PEM 文件（如 privkey.pem）")
		if err != nil {
			updateRemoteTLSStatus(st, RemoteTLSStatus{}, err, "", "选择私钥失败")
			return
		}
		if !ok {
			st.mu.Lock()
			st.remote.tlsBusy = false
			st.remote.tlsResult = ""
			st.mu.Unlock()
			st.invalidate()
			return
		}

		status, err := client.ImportRemoteTLSCertificate(context.Background(), chainPath, keyPath)
		updateRemoteTLSStatus(st, status, err, "证书导入成功", "证书导入失败")
	}()
}

func removeRemoteTLSCertificate(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.tlsBusy {
		st.mu.Unlock()
		return
	}
	st.remote.tlsBusy = true
	st.remote.tlsResult = "正在移除证书…"
	st.remote.tlsResultErr = false
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.RemoveRemoteTLSCertificate(context.Background())
		st.mu.Lock()
		st.remote.tlsBusy = false
		if err != nil {
			st.remote.tlsResult = "移除证书失败：" + err.Error()
			st.remote.tlsResultErr = true
		} else {
			// 就地清掉证书信息：只清 Configured 会让 NotAfter 残留，徽标仍报「证书已过期」。
			st.remote.tls.Configured = false
			st.remote.tls.CertDomains = nil
			st.remote.tls.NotAfter = time.Time{}
			st.remote.tls.StartedURL = ""
			st.remote.tlsResult = "证书已移除"
			st.remote.tlsResultErr = false
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}
