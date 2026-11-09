package mcodeui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/skip2/go-qrcode"
	"mcode/internal/config"
	remotepkg "mcode/internal/remote"
)

type remotePageState struct {
	status       RemoteStatus
	statusLoaded bool
	busy         bool
	err          string
	portText     string
	portInit     bool

	pairing         RemotePairing
	pairingOpen     bool
	pairingErr      string
	pairingConfirm  bool
	pairingPolling  bool
	pairingPolledAt time.Time
	qrFor           string
	qr              *ui.Bitmap

	// pairingTarget 是二维码与「复制配对链接」当前使用的入口：lan、tls 或 tailscale。
	pairingTarget string

	// 复制按钮的回执提示：copiedKey 为按钮键，copiedAt 为复制时刻。
	copiedKey string
	copiedAt  time.Time

	devices         []RemoteDevice
	devicesLoaded   bool
	devicesLoading  bool
	devicesErr      string
	revokeConfirmID string
	revokeBusyID    string
	renameDeviceID  string
	renameDraft     string
	renameBusyID    string
	deviceActionErr string

	tls              RemoteTLSStatus
	tlsLoaded        bool
	tlsBusy          bool
	tlsErr           string
	tlsResult        string
	tlsResultErr     bool
	tlsRemoveConfirm bool
	tlsFieldsInit    bool
	tlsDomainText    string
	tlsPortText      string
	tlsTokenInput    string
	tlsHasToken      bool

	tailscaleStatus       RemoteTailscaleStatus
	tailscaleLoaded       bool
	tailscaleBusy         bool
	tailscaleRefreshing   bool   // 静默刷新，与 tailscaleBusy 分开以免轮询让开关闪一下禁用
	tailscaleGen          uint64 // 启停或重置时递增，让在途的静默刷新结果作废
	tailscaleErr          string
	tailscaleResetConfirm bool
	tailscaleHostnameText string
	tailscaleHostInit     bool
	tailscaleHostnameErr  string // 节点名不合法的提示，空表示合法
	tailscalePolledAt     time.Time

	pushErr       string
	pushTestBusy  bool
	pushResult    string
	pushResultErr bool
}

// qrBitmap 把二维码布尔矩阵画成 image.RGBA 并转为像素化 ui.Bitmap。
func qrBitmap(content string, scale int) (*ui.Bitmap, error) {
	if scale <= 0 {
		scale = 1
	}
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = false
	bm := q.Bitmap()
	if len(bm) == 0 || len(bm[0]) == 0 {
		return nil, fmt.Errorf("empty qrcode bitmap")
	}

	h := len(bm)
	w := len(bm[0])
	img := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))

	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := white
			if bm[y][x] {
				c = black
			}
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					img.SetRGBA(x*scale+sx, y*scale+sy, c)
				}
			}
		}
	}

	return ui.NewBitmap(img).Pixelated(), nil
}

// formatCountdown 格式化倒计时文本，d<=0 返回“已过期”，否则返回“m:ss”。
func formatCountdown(d time.Duration) string {
	if d <= 0 {
		return "已过期"
	}
	totalSec := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", totalSec/60, totalSec%60)
}

// formatShortCode 格式化 6 位授权码为“123 456”，其他长度原样返回。
func formatShortCode(code string) string {
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

// parseListenHostPort 解析 host:port，无法解析时回退到默认端口并尽量保留主机部分。
func parseListenHostPort(listen string, defaultPort int) (string, int) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "0.0.0.0", defaultPort
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		host = strings.TrimSuffix(listen, ":")
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		if host == "" {
			host = "0.0.0.0"
		}
		return host, defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return host, defaultPort
	}
	return host, port
}

func remoteListenPort(listen string) int {
	_, port := parseListenHostPort(listen, config.DefaultRemoteHTTPPort)
	return port
}

func remoteTLSListenPort(listen string) int {
	if strings.TrimSpace(listen) == "" {
		return config.DefaultRemoteTLSPort
	}
	_, port := parseListenHostPort(listen, config.DefaultRemoteTLSPort)
	return port
}

func remoteListenWithPort(listen string, port int) string {
	host, _ := parseListenHostPort(listen, config.DefaultRemoteHTTPPort)
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func remoteTLSListenWithPort(listen string, port int) string {
	host, _ := parseListenHostPort(listen, config.DefaultRemoteTLSPort)
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func remoteCurrentLANIP() string {
	ips := remotepkg.LANIPv4s()
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func remoteURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// remoteURLString 拼接 scheme://host:port，IPv6 主机自动加方括号。
func remoteURLString(scheme, host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// remoteExposureNotice 说明当前 HTTP 入口的暴露范围与传输保护，让用户看到 0.0.0.0 监听的真实含义。
func remoteExposureNotice(listen string) string {
	if strings.TrimSpace(listen) == "" {
		listen = config.DefaultRemoteListen
	}
	host, _ := parseListenHostPort(listen, config.DefaultRemoteHTTPPort)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "HTTP 监听 " + listen + "，本机所有网卡都能访问，传输为明文 HTTP"
	case "localhost", "127.0.0.1", "[::1]", "::1":
		return "HTTP 监听 " + listen + "，只有本机能访问，传输仍是明文 HTTP"
	default:
		return "HTTP 监听 " + listen + "，只有该地址上的设备能访问，传输为明文 HTTP"
	}
}

// remotePairingTargetOption 描述配对页可选的一个二维码入口。
// Value 是 remotePageState.pairingTarget 的取值，URL 为该入口对应的配对链接。
type remotePairingTargetOption struct {
	Value string
	Label string
	URL   string
}

// remotePairingTargetValue 归一化二维码入口取值，未知取值按局域网处理。
func remotePairingTargetValue(target string) string {
	switch target {
	case "tls", "tailscale":
		return target
	default:
		return "lan"
	}
}

// remotePairingTargetOptions 按固定顺序列出三个二维码入口及其配对链接。
func remotePairingTargetOptions(p RemotePairing) []remotePairingTargetOption {
	return []remotePairingTargetOption{
		{Value: "lan", Label: "局域网", URL: p.LANPairingURL},
		{Value: "tls", Label: "域名 HTTPS", URL: p.TLSPairingURL},
		{Value: "tailscale", Label: "Tailscale", URL: p.TailscalePairingURL},
	}
}

// remotePairingEffectiveTarget 返回二维码实际使用的入口：所选入口还没就绪时二维码会回退到
// 别的入口，高亮也要落在真正生效的那个上，否则会出现“禁用却高亮”的入口按钮。
func remotePairingEffectiveTarget(p RemotePairing, target string) string {
	target = remotePairingTargetValue(target)
	url := remotePairingTargetURL(p, target)
	if url == "" {
		return target
	}
	for _, opt := range remotePairingTargetOptions(p) {
		if opt.URL == url {
			return opt.Value
		}
	}
	return target
}

// remotePairingTargetURL 取配对页应编码进二维码的链接：优先所选入口，
// 所选入口尚未就绪时按局域网、域名 HTTPS、Tailscale、主链接依次回退，避免二维码突然空白。
func remotePairingTargetURL(p RemotePairing, target string) string {
	target = remotePairingTargetValue(target)
	for _, opt := range remotePairingTargetOptions(p) {
		if opt.Value == target && opt.URL != "" {
			return opt.URL
		}
	}
	for _, opt := range remotePairingTargetOptions(p) {
		if opt.URL != "" {
			return opt.URL
		}
	}
	return p.PairingURL
}

// setRemotePairingTarget 切换二维码入口：只改所选入口并按新链接重画二维码，不重新签发配对码。
func setRemotePairingTarget(st *SettingsState, pairing RemotePairing, target string) {
	target = remotePairingTargetValue(target)
	targetURL := remotePairingTargetURL(pairing, target)

	var bm *ui.Bitmap
	if targetURL != "" {
		bm, _ = qrBitmap(targetURL, 6)
	}

	st.mu.Lock()
	st.remote.pairingTarget = target
	if bm != nil {
		st.remote.qr = bm
		st.remote.qrFor = targetURL
	} else {
		st.remote.qr = nil
		st.remote.qrFor = ""
	}
	st.mu.Unlock()
	st.invalidate()
}

// remoteConcreteHost 返回监听地址中的具体主机名，跳过 0.0.0.0、::、localhost 等无信息的主机。
func remoteConcreteHost(listen string) string {
	host, _ := parseListenHostPort(listen, 0)
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	switch host {
	case "", "0.0.0.0", "::", "localhost", "127.0.0.1":
		return ""
	}
	return host
}

func remoteServingIPv4(status RemoteStatus) string {
	if host := remoteConcreteHost(status.StartedListen); host != "" {
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}
	for _, raw := range status.LANURLs {
		if ip := net.ParseIP(remoteURLHost(raw)); ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}
	if host := remoteConcreteHost(status.ListenAddr); host != "" {
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}
	return ""
}

// remoteServingHost 返回服务实际使用的入口主机名，用于展示当前入口地址。
func remoteServingHost(status RemoteStatus) string {
	if host := remoteConcreteHost(status.StartedListen); host != "" {
		return host
	}
	for _, raw := range status.LANURLs {
		if host := remoteURLHost(raw); host != "" {
			return host
		}
	}
	return remoteConcreteHost(status.ListenAddr)
}

func sameRemoteHosts(a, b []string) bool {
	na := parseHosts(strings.Join(a, ","))
	nb := parseHosts(strings.Join(b, ","))
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

// remoteAddressChanged 判断正在运行的服务所用局域网 IPv4 是否已与当前地址不同。
func remoteAddressChanged(status RemoteStatus, currentLAN string) bool {
	if status.State != RemoteStateRunning || currentLAN == "" {
		return false
	}
	served := remoteServingIPv4(status)
	return served != "" && served != currentLAN
}

// remotePairingState 归一化配对状态；旧后端未提供 State 时按有效期推断。
func remotePairingState(p RemotePairing, now time.Time) string {
	switch p.State {
	case "active", "expired", "used", "unavailable":
		return p.State
	}
	if p.ShortCode == "" && p.PairingURL == "" && p.LANPairingURL == "" {
		return "unavailable"
	}
	if !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now) {
		return "expired"
	}
	return "active"
}

func remoteEntryLabel(entry string) string {
	switch entry {
	case "tls":
		return "域名 HTTPS"
	case "lan":
		return "局域网 HTTP"
	case "tailscale":
		return "Tailscale"
	default:
		return ""
	}
}

func formatRemotePairedAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func uaVersion(ua, marker string) string {
	idx := strings.Index(ua, marker)
	if idx < 0 {
		return ""
	}
	rest := ua[idx+len(marker):]
	end := 0
	for end < len(rest) {
		ch := rest[end]
		if (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' {
			end++
			continue
		}
		break
	}
	return strings.ReplaceAll(rest[:end], "_", ".")
}

func uaWindowsName(ua string) string {
	version := uaVersion(ua, "Windows NT ")
	if version == "" {
		return ""
	}
	names := map[string]string{"10.0": "10", "6.3": "8.1", "6.2": "8", "6.1": "7", "6.0": "Vista"}
	name, ok := names[version]
	if !ok {
		name = version
	}
	return "Windows " + name
}

func uaBrowser(ua string) string {
	tokens := []struct {
		marker string
		name   string
	}{
		{"EdgiOS/", "Edge"}, {"EdgA/", "Edge"}, {"Edg/", "Edge"},
		{"FxiOS/", "Firefox"}, {"Firefox/", "Firefox"},
		{"CriOS/", "Chrome"}, {"Chrome/", "Chrome"},
		{"Safari/", "Safari"},
	}
	for _, token := range tokens {
		if strings.Contains(ua, token.marker) {
			return token.name
		}
	}
	return ""
}

// summarizeRemoteUserAgent 将 User-Agent 概括为“设备 · 系统 · 浏览器”，无法识别时返回空串。
func summarizeRemoteUserAgent(ua string) string {
	if ua == "" {
		return ""
	}
	var device, system string
	switch {
	case strings.Contains(ua, "iPad"):
		device = "iPad"
		if v := uaVersion(ua, "CPU OS "); v != "" {
			system = "iPadOS " + v
		} else if v := uaVersion(ua, "iPhone OS "); v != "" {
			system = "iPadOS " + v
		}
	case strings.Contains(ua, "iPhone"):
		device = "iPhone"
		if v := uaVersion(ua, "iPhone OS "); v != "" {
			system = "iOS " + v
		}
	case strings.Contains(ua, "Android"):
		if v := uaVersion(ua, "Android "); v != "" {
			system = "Android " + v
		}
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		device = "Mac"
		if v := uaVersion(ua, "Mac OS X "); v != "" {
			system = "macOS " + v
		}
	case strings.Contains(ua, "Windows"):
		system = uaWindowsName(ua)
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{device, system, uaBrowser(ua)} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " · ")
}

func (st *SettingsState) remoteClient() Client {
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil {
		return chat.client
	}
	return nil
}

// loadRemoteStatus 异步获取远程控制服务状态。
func loadRemoteStatus(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.busy {
		st.mu.Unlock()
		return
	}
	st.remote.busy = true
	st.remote.err = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.RemoteStatus(context.Background())
		st.mu.Lock()
		st.remote.busy = false
		st.remote.statusLoaded = true
		if err != nil {
			st.remote.err = err.Error()
		} else {
			st.remote.err = ""
			st.remote.status = status
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// setRemoteEnabled 启用或停用远程控制服务，持久化配置后调用客户端接口更新状态。
func setRemoteEnabled(st *SettingsState, enabled bool) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.busy {
		st.mu.Unlock()
		return
	}
	// 已持 st.mu，写 cfg 另需 cfgMu（顺序就是 mu → cfgMu）；save 自己拿 cfgMu，
	// 所以必须先释放 cfgMu 再调，不能拿着 cfgMu 去保存。
	st.cfgMu.Lock()
	previous := st.cfg.Remote.Enabled
	st.cfg.Remote.Enabled = enabled
	st.cfgMu.Unlock()
	st.save()
	// st.errorMessage 与 cfg 共用 cfgMu，失败判断与回滚写入都要在它下面做。
	st.cfgMu.Lock()
	saveErr := st.errorMessage
	st.cfgMu.Unlock()
	if saveErr != "" {
		st.cfgMu.Lock()
		st.cfg.Remote.Enabled = previous
		st.cfgMu.Unlock()
		st.remote.err = saveErr
		st.mu.Unlock()
		st.invalidate()
		return
	}
	if !enabled {
		st.remote.pairingOpen = false
		st.remote.pairingConfirm = false
		st.remote.pairing = RemotePairing{}
		st.remote.qr = nil
		st.remote.qrFor = ""
	}
	st.remote.busy = true
	st.remote.err = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.SetRemoteEnabled(context.Background(), enabled)
		st.mu.Lock()
		st.remote.busy = false
		st.remote.statusLoaded = true
		// 服务重启后节点可能换过名字，Tailscale 状态要重新拉取；
		// renderRemoteTailscaleSection 的 !loaded && !busy 判断会负责重载。
		st.remote.tailscaleLoaded = false
		st.remote.status = status
		if err != nil {
			st.remote.err = err.Error()
		} else {
			st.remote.err = ""
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// loadRemotePairing 异步拉取或换发设备配对码，并生成/缓存配对二维码 Bitmap。
// revokeAll 为 true 时先撤销所有已配对设备再换发新码。
func loadRemotePairing(st *SettingsState, refresh, revokeAll bool) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.busy {
		st.mu.Unlock()
		return
	}
	st.remote.busy = true
	st.remote.pairingErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		ctx := context.Background()
		if revokeAll {
			if err := client.RevokeAllRemoteDevices(ctx); err != nil {
				st.mu.Lock()
				st.remote.busy = false
				st.remote.pairingErr = err.Error()
				st.mu.Unlock()
				st.invalidate()
				return
			}
		}

		pairing, err := client.RemotePairing(ctx, refresh)
		if err != nil {
			st.mu.Lock()
			st.remote.busy = false
			st.remote.pairingErr = err.Error()
			st.mu.Unlock()
			st.invalidate()
			return
		}

		st.mu.Lock()
		target := st.remote.pairingTarget
		st.mu.Unlock()
		targetURL := remotePairingTargetURL(pairing, target)

		var bm *ui.Bitmap
		var qrErr error

		st.mu.Lock()
		needGen := targetURL != "" && (targetURL != st.remote.qrFor || st.remote.qr == nil)
		st.mu.Unlock()

		if needGen {
			bm, qrErr = qrBitmap(targetURL, 6)
		}

		st.mu.Lock()
		st.remote.busy = false
		st.remote.pairingOpen = true
		st.remote.pairing = pairing
		if qrErr != nil {
			st.remote.pairingErr = qrErr.Error()
		} else {
			st.remote.pairingErr = ""
			if needGen {
				st.remote.qr = bm
				st.remote.qrFor = targetURL
			}
		}
		st.mu.Unlock()
		st.invalidate()

		if revokeAll {
			loadRemoteDevices(st)
		}
	}()
}

// pollRemotePairing 静默拉取一次配对状态，用于手机上完成配对后自动刷新界面。
// 它既不占用 st.remote.busy，也不重新签发配对码：只有状态或配对码变化时才更新本地状态并重绘。
func pollRemotePairing(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.pairingPolling {
		st.mu.Unlock()
		return
	}
	st.remote.pairingPolling = true
	current := st.remote.pairing
	st.mu.Unlock()

	go func() {
		pairing, err := client.RemotePairing(context.Background(), false)
		if err != nil {
			st.mu.Lock()
			st.remote.pairingPolling = false
			st.mu.Unlock()
			return
		}

		now := time.Now()
		fromState := remotePairingState(current, now)
		toState := remotePairingState(pairing, now)

		// 二维码只在仍处于 active 且配对链接变化时重新生成，其余情况保留现有图像。
		// 这里按当前选择的入口重算链接，避免轮询期间用户切了入口又被旧链接覆盖。
		st.mu.Lock()
		st.remote.pairingPolling = false
		targetURL := remotePairingTargetURL(pairing, st.remote.pairingTarget)
		needGen := toState == "active" && targetURL != "" && (targetURL != st.remote.qrFor || st.remote.qr == nil)
		// 配对码换发后状态仍是 active：只比较状态会让界面停在旧二维码与旧短码上，
		// 所以链接或短码任一变化也要更新本地状态并重绘。
		codeChanged := pairing.ShortCode != current.ShortCode || targetURL != remotePairingTargetURL(current, st.remote.pairingTarget)
		if fromState == toState && !codeChanged && !needGen {
			st.mu.Unlock()
			return
		}
		st.remote.pairing = pairing
		st.mu.Unlock()

		if needGen {
			bm, qrErr := qrBitmap(targetURL, 6)
			st.mu.Lock()
			if qrErr == nil {
				st.remote.qr = bm
				st.remote.qrFor = targetURL
			}
			st.mu.Unlock()
		}

		st.invalidate()

		// 配对成功后刷新设备列表与已连接数量。
		if fromState == "active" && toState == "used" {
			loadRemoteDevices(st)
			loadRemoteStatus(st)
		}
	}()
}

// restartRemote 异步重启远程控制服务并刷新状态。
func restartRemote(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.busy {
		st.mu.Unlock()
		return
	}
	st.remote.busy = true
	st.remote.err = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.RestartRemote(context.Background())
		st.mu.Lock()
		st.remote.busy = false
		st.remote.statusLoaded = true
		// 服务重启后节点可能已改名，Tailscale 状态要重新拉取；
		// renderRemoteTailscaleSection 的 !loaded && !busy 判断会负责重载。
		st.remote.tailscaleLoaded = false
		if err != nil {
			st.remote.err = err.Error()
			st.remote.status = status
		} else {
			st.remote.err = ""
			st.remote.status = status
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// loadRemoteDevices 异步获取已授权设备列表。
func loadRemoteDevices(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.devicesLoading {
		st.mu.Unlock()
		return
	}
	st.remote.devicesLoading = true
	st.remote.devicesErr = ""
	st.mu.Unlock()

	go func() {
		devices, err := client.RemoteDevices(context.Background())
		st.mu.Lock()
		st.remote.devicesLoading = false
		st.remote.devicesLoaded = true
		if err != nil {
			st.remote.devicesErr = err.Error()
		} else {
			st.remote.devicesErr = ""
			st.remote.devices = devices
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// revokeRemoteDevice 异步撤销指定设备的授权，成功后重新拉取设备列表。
func revokeRemoteDevice(st *SettingsState, deviceID string) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	st.remote.revokeBusyID = deviceID
	st.remote.revokeConfirmID = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.RevokeRemoteDevice(context.Background(), deviceID)
		st.mu.Lock()
		st.remote.revokeBusyID = ""
		if err != nil {
			st.remote.devicesErr = err.Error()
		} else {
			st.remote.devicesErr = ""
			if st.remote.renameDeviceID == deviceID {
				st.remote.renameDeviceID = ""
				st.remote.renameDraft = ""
			}
		}
		st.mu.Unlock()

		if err == nil {
			loadRemoteDevices(st)
		} else {
			st.invalidate()
		}
	}()
}

// renameRemoteDevice 异步重命名指定设备，成功后重新拉取设备列表。
func renameRemoteDevice(st *SettingsState, deviceID, name string) {
	client := st.remoteClient()
	if client == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		st.mu.Lock()
		st.remote.renameDeviceID = ""
		st.remote.renameDraft = ""
		st.mu.Unlock()
		st.invalidate()
		return
	}

	st.mu.Lock()
	if st.remote.renameBusyID != "" {
		st.mu.Unlock()
		return
	}
	st.remote.renameBusyID = deviceID
	st.remote.deviceActionErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.RenameRemoteDevice(context.Background(), deviceID, name)
		st.mu.Lock()
		st.remote.renameBusyID = ""
		if err != nil {
			st.remote.deviceActionErr = err.Error()
		} else {
			st.remote.deviceActionErr = ""
			st.remote.renameDeviceID = ""
			st.remote.renameDraft = ""
		}
		st.mu.Unlock()

		if err == nil {
			loadRemoteDevices(st)
		} else {
			st.invalidate()
		}
	}()
}

// sendRemoteTestPush 异步发送一条测试推送，便于在手机上确认推送链路（VAPID、订阅、service worker）是否打通。
func sendRemoteTestPush(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.remote.pushTestBusy {
		st.mu.Unlock()
		return
	}
	st.remote.pushTestBusy = true
	st.remote.pushResult = ""
	st.remote.pushResultErr = false
	st.mu.Unlock()
	st.invalidate()

	go func() {
		sent, err := client.SendRemoteTestPush(context.Background())
		st.mu.Lock()
		st.remote.pushTestBusy = false
		switch {
		case err != nil:
			st.remote.pushResult = err.Error()
			st.remote.pushResultErr = true
		case sent <= 0:
			st.remote.pushResult = "没有可用的推送订阅，请先在手机上打开远程页面并开启通知。"
			st.remote.pushResultErr = true
		default:
			st.remote.pushResult = "已发送，请查看手机通知。"
			st.remote.pushResultErr = false
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// remoteExposureLabel 把 HTTP 监听的暴露范围压成一个短徽标：仅本机 / 局域网 / 指定地址。
// 监听地址的真实含义不写成说明句，放在状态行右侧，一眼能看到。
func remoteExposureLabel(listen string) string {
	if strings.TrimSpace(listen) == "" {
		listen = config.DefaultRemoteListen
	}
	host, _ := parseListenHostPort(listen, config.DefaultRemoteHTTPPort)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "局域网"
	case "localhost", "127.0.0.1", "[::1]", "::1":
		return "仅本机"
	default:
		return "指定地址"
	}
}

// remoteEnabledControl 是服务总开关：写配置之外还要让后端启动或停止服务，所以不能只用 toggle。
type remoteEnabledControl struct{}

func (remoteEnabledControl) render(c *ui.Context, st *SettingsState, s setting) {
	toggleFunc(
		func(st *SettingsState) (bool, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.cfg.Remote.Enabled, st.remote.busy
		},
		setRemoteEnabled,
	).render(c, st, s)
}

func (remoteEnabledControl) reset(cfg *config.Config) { cfg.Remote.Enabled = false }

// remoteStatusControl 是运行状态行右侧的操作：运行中给重启，失败给重试，其余状态留空。
type remoteStatusControl struct{}

func (remoteStatusControl) render(c *ui.Context, st *SettingsState, s setting) {
	// 页面树没有统一的加载钩子：状态行是常驻行，借它每帧跑一次幂等的拉取与清理。
	remotePageLoad(st)

	st.mu.Lock()
	status := st.remote.status
	busy := st.remote.busy
	listen := st.cfg.Remote.Listen
	enabled := st.cfg.Remote.Enabled
	stErr := st.remote.err
	st.mu.Unlock()

	if remoteServiceRunning(status) {
		ui.Text(c, remoteExposureLabel(listen)).
			FontSize(TypeFootnote).
			TextColor(ThemeSecondaryText(c))
		renderRemoteRestartButton(c, st, busy)
		return
	}
	if status.State == RemoteStateError || (stErr != "" && enabled) {
		renderRemoteRetryButton(c, st, busy)
	}
}

// renderRemoteRestartButton 渲染「重启服务」按钮：端口与访问域名改完都要重启才生效。
func renderRemoteRestartButton(c *ui.Context, st *SettingsState, busy bool) {
	btn := ui.ButtonBase(c.Key("btn-remote-restart")).
		Label("重启服务").
		Tooltip("重启远程服务").
		Radius(RadiusSM).
		Padding(Space1, Space4).
		Background(ThemeHover(c))
	if busy {
		btn.Disabled(true)
	} else if btn.Hovered() {
		btn.Background(ThemeOverlay(c, 0.15))
	}
	if btn.Clicked() && !busy {
		restartRemote(st)
	}
	btn.Children(func() {
		label := "重启服务"
		if busy {
			label = "重启中…"
		}
		ui.Text(c, label).FontSize(TypeFootnote).TextColor(ThemeAccent(c))
	})
}

// renderRemoteRetryButton 渲染「重试」按钮：启动失败后再试一次。
func renderRemoteRetryButton(c *ui.Context, st *SettingsState, busy bool) {
	btn := ui.ButtonBase(c.Key("btn-remote-retry")).
		Label("重试").
		Tooltip("重试启动远程服务").
		Radius(RadiusSM).
		Padding(Space1, Space4).
		Background(ThemeAccent(c).Alpha(0.12))
	if busy {
		btn.Disabled(true)
	} else if btn.Hovered() {
		btn.Background(ThemeAccent(c).Alpha(0.20))
	}
	if btn.Clicked() && !busy {
		restartRemote(st)
	}
	btn.Children(func() {
		label := "重试"
		if busy {
			label = "重试中…"
		}
		ui.Text(c, label).FontSize(TypeFootnote).TextColor(ThemeAccent(c))
	})
}

// remoteValueControl 是纯取值行：不画控件，只让渲染器把 Status 写到行下方。
// customControl 会跳过 Status，所以这类行不能用 custom。
type remoteValueControl struct{}

func (remoteValueControl) render(c *ui.Context, st *SettingsState, s setting) {}

// remotePairControl 是「配对新设备」按钮：除了显示二维码，还要跑一次后端的配对流程。
type remotePairControl struct{}

func (remotePairControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	busy := st.remote.busy
	running := remoteServiceRunning(st.remote.status)
	pairingConfirm := st.remote.pairingConfirm
	st.mu.Unlock()

	btn := ui.ButtonBase(c.Key("btn-remote-pair")).
		Label("配对新设备").
		Radius(RadiusSM).
		Padding(Space2, Space4).
		Background(ThemeAccent(c).Alpha(0.12))
	stylePairingButton(c, btn, busy || !running)
	if btn.Clicked() && !busy && running {
		loadRemotePairing(st, false, false)
	}
	btn.Children(func() {
		label := "配对新设备"
		if busy {
			label = "配对中…"
		}
		ui.Text(c, label).FontSize(TypeBody).TextColor(ThemeAccent(c))
	})

	// 刷新按钮在配对卡里，确认框由常驻的本行渲染，卡片收起时也不会丢。
	if pairingConfirm {
		renderRemotePairingConfirmModal(c, st)
	}
}

// remoteEntryControl 是入口地址行：地址原文加复制按钮，地址变化时再给一个复制新地址的按钮。
type remoteEntryControl struct{}

func (remoteEntryControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	status := st.remote.status
	configuredPort := remoteListenPort(st.cfg.Remote.Listen)
	st.mu.Unlock()

	running := remoteServiceRunning(status)
	currentLAN := remoteCurrentLANIP()
	startedPort := status.Port
	if status.StartedListen != "" {
		startedPort = remoteListenPort(status.StartedListen)
	}
	entryHost := currentLAN
	if running {
		if host := remoteServingHost(status); host != "" {
			entryHost = host
		}
	}
	if entryHost == "" {
		if running {
			entryHost = "127.0.0.1"
		} else {
			entryHost = "localhost"
		}
	}
	entryPort := configuredPort
	if running && startedPort > 0 {
		entryPort = startedPort
	}
	entryURL := remoteURLString("http", entryHost, entryPort)

	ui.Row(c.Key("row-remote-entry-actions")).
		Gap(Space4).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, entryURL).FontSize(TypeBody).TextColor(ThemePrimaryText(c)).SingleLine()
			if !running || currentLAN == "" {
				// 没有局域网地址时不提供可复制入口，避免复制出打不开的 loopback 地址。
				return
			}
			CopyButton(c, CopyButtonProps{Key: "btn-copy-entry", Label: "复制入口地址", Tooltip: "复制入口地址", Text: "复制", Mode: CopyText, Value: func() string { return entryURL }})
			if remoteAddressChanged(status, currentLAN) {
				CopyButton(c, CopyButtonProps{Key: "btn-copy-new-entry", Label: "复制新入口地址", Tooltip: "复制新入口地址", Text: "复制新入口地址", Mode: CopyText, Value: func() string { return remoteURLString("http", currentLAN, entryPort) }})
			}
		})
}

// remotePortControl 是监听端口：端口存在 Listen 字符串里，没有独立 int 字段，
// 因此用文本输入加范围校验，并就地给重启按钮。
type remotePortControl struct{}

func (remotePortControl) render(c *ui.Context, st *SettingsState, s setting) {
	remotePortControl{}.renderDisabled(c, st, s, false)
}

func (remotePortControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	textFunc(
		func(st *SettingsState) (string, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remote.portText, false
		},
		func(st *SettingsState, text string) {
			st.mu.Lock()
			st.remote.portText = text
			st.mu.Unlock()
			port, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil || port < 1024 || port > 65535 {
				return
			}
			commitSettingsControl(st, func(cfg *config.Config) {
				cfg.Remote.Listen = remoteListenWithPort(cfg.Remote.Listen, port)
			})
		},
		strconv.Itoa(config.DefaultRemoteHTTPPort),
	).renderDisabled(c, st, s, disabled)

	st.mu.Lock()
	busy := st.remote.busy
	st.mu.Unlock()
	renderRemoteRestartButton(c, st, busy)
}

func (remotePortControl) reset(cfg *config.Config) { cfg.Remote.Listen = config.DefaultRemoteListen }

// remoteHostsControl 是额外访问域名：逗号分隔的 []string，bind 表达不了，单独处理。
type remoteHostsControl struct{}

func (remoteHostsControl) render(c *ui.Context, st *SettingsState, s setting) {
	remoteHostsControl{}.renderDisabled(c, st, s, false)
}

func (remoteHostsControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	textFunc(
		func(st *SettingsState) (string, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.remoteHostsText, false
		},
		func(st *SettingsState, text string) {
			st.mu.Lock()
			st.remoteHostsText = text
			st.mu.Unlock()
			commitSettingsControl(st, func(cfg *config.Config) {
				cfg.Remote.ExtraHosts = parseHosts(text)
			})
		},
		"example.com, myhost.lan",
	).renderDisabled(c, st, s, disabled)
}

func (remoteHostsControl) reset(cfg *config.Config) { cfg.Remote.ExtraHosts = nil }

// remotePushToggleControl 是离线推送开关：改完要重启服务才能让运行中的实例生效。
type remotePushToggleControl struct{}

func (remotePushToggleControl) render(c *ui.Context, st *SettingsState, s setting) {
	toggleFunc(
		func(st *SettingsState) (bool, bool) {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.cfg.Remote.PushEnabled(), false
		},
		func(st *SettingsState, on bool) {
			// nil 表示默认开启，所以关闭时才写入显式的 false。
			persisted := on
			st.mu.Lock()
			oldVal := st.cfg.Remote.PushEnabled()
			st.cfg.Remote.PushOn = &persisted
			st.save()
			pushErr := st.errorMessage
			st.mu.Unlock()
			if pushErr != "" {
				st.mu.Lock()
				st.cfg.Remote.PushOn = &oldVal
				st.remote.pushErr = pushErr
				st.mu.Unlock()
				return
			}
			restartRemoteAfterPushChange(st)
		},
	).render(c, st, s)
}

func (remotePushToggleControl) reset(cfg *config.Config) { cfg.Remote.PushOn = nil }

// remotePushTestControl 是已订阅设备行右侧的测试按钮，没有订阅时留空。
type remotePushTestControl struct{}

func (remotePushTestControl) render(c *ui.Context, st *SettingsState, s setting) {
	st.mu.Lock()
	subs := st.remote.status.PushSubscriptions
	busy := st.remote.pushTestBusy
	running := remoteServiceRunning(st.remote.status)
	st.mu.Unlock()
	if subs <= 0 {
		return
	}
	renderPushTestButton(c, st, running, busy)
}

// renderRemoteDeviceRow 渲染一台设备：名称、在线圆点、最近活跃与重命名、撤销操作。
func renderRemoteDeviceRow(c *ui.Context, st *SettingsState, dev RemoteDevice, renameDeviceID, renameBusyID, revokeConfirmID, revokeBusyID string) {
	devVal := dev
	devKey := fmt.Sprintf("row-remote-dev-%s", devVal.ID)

	name := devVal.Name
	if name == "" {
		name = devVal.ID
	}

	var lines []string
	if ua := summarizeRemoteUserAgent(devVal.UserAgent); ua != "" {
		lines = append(lines, ua)
	} else if devVal.Platform != "" {
		lines = append(lines, devVal.Platform)
	}
	if pairedAt := formatRemotePairedAt(devVal.PairedAt); pairedAt != "" {
		lines = append(lines, "配对于 "+pairedAt)
	}
	if !devVal.LastSeenAt.IsZero() {
		if relTime := formatRelativeTime(devVal.LastSeenAt); relTime != "" {
			lines = append(lines, "最后活跃 "+relTime)
		}
	}
	if devVal.LastAddress != "" {
		access := "最后访问：" + devVal.LastAddress
		if entry := remoteEntryLabel(devVal.LastEntry); entry != "" {
			access += " · " + entry
		}
		lines = append(lines, access)
	}
	if len(lines) == 0 {
		lines = append(lines, "暂无活跃记录")
	}
	desc := strings.Join(lines, "\n")

	ui.Row(c.Key(devKey)).
		Gap(Space5).
		AlignItems(ui.Center).
		Children(func() {
			ui.Column(c.Key(devKey + "-left")).
				Grow(1).
				MinWidth(160).
				Gap(Space1).
				Children(func() {
					ui.Row(c.Key(devKey + "-title")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							dotColor := ThemeSecondaryText(c)
							if devVal.Online {
								dotColor = ThemeSuccess(c)
							}
							ui.Box(c.Key(devKey+"-online")).
								Size(Space3, Space3).
								Radius(RadiusXS).
								Background(dotColor)
							ui.Text(c, name).
								FontSize(TypeProse).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
						})
					ui.Text(c, desc).
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})

			ui.Row(c.Key(devKey + "-actions")).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					if renameDeviceID == devVal.ID {
						textFunc(
							func(st *SettingsState) (string, bool) {
								st.mu.Lock()
								defer st.mu.Unlock()
								return st.remote.renameDraft, false
							},
							func(st *SettingsState, text string) {
								st.mu.Lock()
								st.remote.renameDraft = text
								st.mu.Unlock()
							},
							"设备名称",
						).withWidth(150).withSubmit(func(st *SettingsState, text string) {
							renameRemoteDevice(st, devVal.ID, text)
						}).withDisabled(func(*SettingsState) bool {
							return renameBusyID == devVal.ID
						}).render(c, st, setting{ID: "input-rename-" + devVal.ID, Title: "设备名称"})

						st.mu.Lock()
						draft := st.remote.renameDraft
						st.mu.Unlock()
						saveBtn := ui.ButtonBase(c.Key("btn-rename-save-"+devVal.ID)).
							Label("保存").
							Radius(RadiusSM).
							Padding(Space1, Space4).
							Background(ThemeAccent(c).Alpha(0.12))
						if renameBusyID == devVal.ID {
							saveBtn.Disabled(true)
						}
						if saveBtn.Clicked() && renameBusyID != devVal.ID {
							renameRemoteDevice(st, devVal.ID, draft)
						}
						saveBtn.Children(func() {
							label := "保存"
							if renameBusyID == devVal.ID {
								label = "保存中…"
							}
							ui.Text(c, label).FontSize(TypeFootnote).TextColor(ThemeAccent(c))
						})

						cancelBtn := ui.ButtonBase(c.Key("btn-rename-cancel-"+devVal.ID)).
							Label("取消").
							Radius(RadiusSM).
							Padding(Space1, Space4).
							Background(ThemeHover(c))
						if cancelBtn.Clicked() {
							st.mu.Lock()
							st.remote.renameDeviceID = ""
							st.remote.renameDraft = ""
							st.mu.Unlock()
							st.invalidate()
						}
						cancelBtn.Children(func() {
							ui.Text(c, "取消").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
						})
						return
					}

					renameBtn := ui.ButtonBase(c.Key("btn-rename-"+devVal.ID)).
						Label("重命名").
						Tooltip("重命名设备").
						Radius(RadiusSM).
						Padding(Space1, Space4).
						Background(ThemeHover(c))
					if renameBtn.Hovered() {
						renameBtn.Background(ThemeOverlay(c, 0.15))
					}
					if renameBtn.Clicked() {
						st.mu.Lock()
						st.remote.renameDeviceID = devVal.ID
						st.remote.renameDraft = name
						st.remote.deviceActionErr = ""
						st.mu.Unlock()
						st.invalidate()
					}
					renameBtn.Children(func() {
						ui.Text(c, "重命名").FontSize(TypeFootnote).TextColor(ThemeAccent(c))
					})

					isConfirm := revokeConfirmID == devVal.ID
					isBusy := revokeBusyID == devVal.ID

					delLabel := "撤销"
					if isConfirm {
						delLabel = "确认撤销"
					}
					delBtn := ui.ButtonBase(c.Key("btn-revoke-"+devVal.ID)).
						Label("btn-revoke-"+devVal.ID).
						Tooltip(delLabel).
						Disabled(isBusy).
						Radius(RadiusSM).
						Padding(Space1, Space4)

					if isConfirm {
						delBtn.Background(ThemeDanger(c).Alpha(0.12))
					} else {
						delBtn.Background(ThemeHover(c))
					}
					if delBtn.Hovered() {
						if isConfirm {
							delBtn.Background(ThemeDanger(c).Alpha(0.2))
						} else {
							delBtn.Background(ThemeOverlay(c, 0.15))
						}
					}
					if delBtn.Clicked() && !isBusy {
						if isConfirm {
							revokeRemoteDevice(st, devVal.ID)
						} else {
							st.mu.Lock()
							st.remote.revokeConfirmID = devVal.ID
							st.mu.Unlock()
							st.invalidate()
						}
					}
					delBtn.Children(func() {
						textColor := ThemeSecondaryText(c)
						if isConfirm {
							textColor = ThemeDanger(c)
						}
						label := delLabel
						if isBusy {
							label = "撤销中…"
						}
						ui.Text(c, label).FontSize(TypeFootnote).TextColor(textColor)
					})

					if isConfirm {
						cancelBtn := ui.ButtonBase(c.Key("btn-cancel-revoke-"+devVal.ID)).
							Label("取消").
							Radius(RadiusSM).
							Padding(Space1, Space4).
							Background(ThemeHover(c))
						if cancelBtn.Hovered() {
							cancelBtn.Background(ThemeOverlay(c, 0.15))
						}
						if cancelBtn.Clicked() {
							st.mu.Lock()
							st.remote.revokeConfirmID = ""
							st.mu.Unlock()
							st.invalidate()
						}
						cancelBtn.Children(func() {
							ui.Text(c, "取消").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
						})
					}
				})
		})
}

// stylePairingButton 统一配对区按钮的交互反馈：指针光标、悬停加深与按下加深。
// disabled 为 true 时只保持禁用外观，不做悬停变化。
func stylePairingButton(c *ui.Context, btn ui.Element, disabled bool) {
	if disabled {
		btn.Disabled(true)
		return
	}
	btn.Cursor(ui.CursorPointer)
	switch {
	case btn.Pressed():
		btn.Background(ThemeAccent(c).Alpha(0.24))
	case btn.Hovered():
		btn.Background(ThemeAccent(c).Alpha(0.14))
	}
}

// markRemoteCopied 记录复制按钮的回执，供按钮短暂显示「已复制」。
func markRemoteCopied(st *SettingsState, key string, at time.Time) {
	st.mu.Lock()
	st.remote.copiedKey = key
	st.remote.copiedAt = at
	st.mu.Unlock()
}

// pairingCopyLabel 在复制成功后的 1.5 秒内返回复制完成文案，并安排一次重绘让文案复原。
func pairingCopyLabel(c *ui.Context, key, idle, copied, copiedKey string, copiedAt time.Time) string {
	if copiedKey != key || copiedAt.IsZero() {
		return idle
	}
	remaining := 1500*time.Millisecond - c.Now().Sub(copiedAt)
	if remaining <= 0 {
		return idle
	}
	c.After(remaining)
	return copied
}

// disabledCopyButton 渲染不可用的复制按钮，保留按钮位置与提示。
func disabledCopyButton(c *ui.Context, key, label string) {
	CopyButton(c, CopyButtonProps{Key: key, Label: label, Tooltip: label, Text: label, Mode: CopyText, Disabled: true})
}

// renderPairingNotice 渲染非 active 配对状态的标题与“配对新设备”按钮。
// success 为 true 时在标题上方渲染成功标记，用于刚完成配对的提示。
func renderPairingNotice(c *ui.Context, title string, pairButton func(), pairingErr string, success bool) {
	ui.Column(c.Key("card-remote-pairing-notice")).
		Gap(Space4).
		AlignItems(ui.Center).
		Children(func() {
			if success {
				ui.Row(c.Key("row-remote-pairing-success-mark")).
					Size(ControlMD, ControlMD).
					Radius(RadiusXL).
					AlignItems(ui.Center).
					Justify(ui.Center).
					Background(ThemeSuccess(c).Alpha(0.16)).
					Children(func() {
						ui.Text(c, "✓").
							FontSize(TypeTitle).
							FontWeight(600).
							TextColor(ThemeSuccess(c))
					})
			}
			ui.Text(c, title).
				FontSize(TypeTitle).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))
			pairButton()
			if pairingErr != "" {
				ui.Text(c, pairingErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			}
		})
}

// renderRemotePairingConfirmModal 渲染刷新配对码前的确认框。
func renderRemotePairingConfirmModal(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	deviceCount := len(st.remote.devices)
	st.mu.Unlock()
	// 已拉到设备列表时写清具体台数，否则用不依赖列表的泛指文案。
	body := "刷新将撤销所有已配对设备，并生成新的 5 分钟有效配对码。"
	if deviceCount > 0 {
		body = fmt.Sprintf("刷新将撤销全部 %d 台已配对设备，并生成新的 5 分钟有效配对码。", deviceCount)
	}
	closeModal := func() {
		st.mu.Lock()
		st.remote.pairingConfirm = false
		st.mu.Unlock()
		st.invalidate()
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragRemotePairing,
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "刷新配对码？",
		Body:         body,
		ConfirmLabel: "撤销所有设备",
		CancelLabel:  "取消",
		OnConfirm: func() {
			st.mu.Lock()
			st.remote.pairingConfirm = false
			st.mu.Unlock()
			loadRemotePairing(st, true, true)
		},
		OnCancel: closeModal,
	})
}

// renderPushTestButton 渲染“发送测试推送”按钮，向所有已登记订阅发一条测试通知。
func renderPushTestButton(c *ui.Context, st *SettingsState, running, busy bool) {
	btn := ui.ButtonBase(c.Key("btn-remote-push-test")).
		Label("发送测试推送").
		Radius(RadiusSM).
		Padding(Space2, Space4).
		Background(ThemeAccent(c).Alpha(0.12))
	if !running || busy {
		btn.Disabled(true)
	} else if btn.Hovered() {
		btn.Background(ThemeAccent(c).Alpha(0.20))
	}
	if btn.Clicked() && running && !busy {
		sendRemoteTestPush(st)
	}
	btn.Children(func() {
		label := "发送测试推送"
		if busy {
			label = "发送中…"
		}
		ui.Text(c, label).FontSize(TypeBody).TextColor(ThemeAccent(c))
	})
}

// restartRemoteAfterPushChange 异步重启远程控制服务，让推送开关在运行中的服务上立即生效；服务未运行时只刷新状态。
func restartRemoteAfterPushChange(st *SettingsState) {
	client := st.remoteClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	st.remote.pushErr = ""
	st.remote.pushResult = ""
	st.remote.pushResultErr = false
	wasRunning := st.remote.status.State == RemoteStateRunning
	st.mu.Unlock()

	if !wasRunning {
		loadRemoteStatus(st)
		return
	}

	st.mu.Lock()
	st.remote.busy = true
	st.mu.Unlock()
	st.invalidate()

	go func() {
		status, err := client.RestartRemote(context.Background())
		st.mu.Lock()
		st.remote.busy = false
		st.remote.statusLoaded = true
		// 服务重启后节点可能已改名，Tailscale 状态要重新拉取；
		// renderRemoteTailscaleSection 的 !loaded && !busy 判断会负责重载。
		st.remote.tailscaleLoaded = false
		if err != nil {
			st.remote.pushErr = err.Error()
		} else {
			st.remote.status = status
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}
