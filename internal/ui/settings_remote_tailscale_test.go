package mcodeui

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
)

// tailscaleBool 返回指向 bool 的指针，用于构造 RemoteConfig.TailscaleOn。
func tailscaleBool(v bool) *bool { return &v }

func TestRemoteTailscale_MergedRows(t *testing.T) {
	st := &SettingsState{cfg: config.Defaults()}
	st.cfg.Remote.TailscaleOn = tailscaleBool(false)
	check := func(want ...string) {
		t.Helper()
		if got := remoteTestRows(t, st, "remote.tailscale"); !reflect.DeepEqual(got, want) {
			t.Errorf("Tailscale 行 = %v，期望 %v", got, want)
		}
	}
	check("row-remote-tailscale-hostname", "row-remote-tailscale-reset")
	if got := remoteTailscaleResetStatus(st, false); got != "需先启动远程控制服务" {
		t.Errorf("服务关闭时重置状态 = %q", got)
	}
	st.remote.status.State = RemoteStateRunning
	check("row-remote-tailscale-enabled", "row-remote-tailscale-hostname", "row-remote-tailscale-reset")
	st.cfg.Remote.TailscaleOn = tailscaleBool(true)
	st.remote.tailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleLoginRequired, AuthURL: "https://example.com/auth"}
	check("row-remote-tailscale-enabled", "row-remote-tailscale-state", "row-remote-tailscale-login", "row-remote-tailscale-hostname", "row-remote-tailscale-reset")
	st.remote.tailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleRunning, URL: "https://example.com"}
	check("row-remote-tailscale-enabled", "row-remote-tailscale-state", "row-remote-tailscale-entry", "row-remote-tailscale-whois", "row-remote-tailscale-hostname", "row-remote-tailscale-reset")
	st.remote.tailscaleErr = "连接失败"
	check("row-remote-tailscale-enabled", "row-remote-tailscale-state", "row-remote-tailscale-entry", "row-remote-tailscale-whois", "row-remote-tailscale-error", "row-remote-tailscale-hostname", "row-remote-tailscale-reset")
	st.cfg.Remote.TailscaleOn = tailscaleBool(false)
	st.remote.tailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleFailed}
	check("row-remote-tailscale-enabled", "row-remote-tailscale-error", "row-remote-tailscale-hostname", "row-remote-tailscale-reset")
}

// TestRemoteTailscaleStateCopy 覆盖各连接状态的文案与错误详情拼接。
func TestRemoteTailscaleStateCopy(t *testing.T) {
	cases := []struct {
		name   string
		status RemoteTailscaleStatus
		want   string
	}{
		{"starting", RemoteTailscaleStatus{State: RemoteTailscaleStarting}, "正在连接 tailnet"},
		{"login", RemoteTailscaleStatus{State: RemoteTailscaleLoginRequired}, "等待登录：在浏览器中登录 Tailscale 并授权本机"},
		{"needs_https", RemoteTailscaleStatus{State: RemoteTailscaleNeedsHTTPS}, "需在 Tailscale 管理台开启 MagicDNS 与 HTTPS Certificates，开启后点重试"},
		{"running", RemoteTailscaleStatus{State: RemoteTailscaleRunning}, "运行中"},
		{"failed", RemoteTailscaleStatus{State: RemoteTailscaleFailed, Error: "tailnet 未登录"}, "启动失败"},
		{"idle", RemoteTailscaleStatus{}, "未连接"},
		{"disabled", RemoteTailscaleStatus{State: RemoteTailscaleDisabled}, "未连接"},
		{"disabled_with_error", RemoteTailscaleStatus{State: RemoteTailscaleDisabled, Error: "tailnet 未登录"}, "tailnet 未登录"},
	}
	for _, tc := range cases {
		if got := remoteTailscaleStateText(tc.status); got != tc.want {
			t.Errorf("%s 状态文案 = %q，应为 %q", tc.name, got, tc.want)
		}
	}

	desc := remoteTailscaleStateDesc(RemoteTailscaleStatus{State: RemoteTailscaleRunning, Error: "旧错误"}, "新错误")
	if !strings.Contains(desc, "运行中") || !strings.Contains(desc, "旧错误") || !strings.Contains(desc, "新错误") {
		t.Errorf("连接状态说明应包含状态与错误详情：%q", desc)
	}

	whois := remoteTailscaleWhoisDesc(RemoteTailscaleStatus{LastLoginName: "kai@example.com", LastNodeName: "iPhone"})
	if whois != "kai@example.com，iPhone" {
		t.Errorf("最近连接说明 = %q", whois)
	}
	if empty := remoteTailscaleWhoisDesc(RemoteTailscaleStatus{}); empty != "尚无通过 Tailscale 的连接" {
		t.Errorf("无连接记录时说明 = %q", empty)
	}
}

// TestSettingsRemoteTailscale_LoginLink 覆盖：登录链接只在 login_required 且有 AuthURL 时出现，可复制。
func TestSettingsRemoteTailscale_LoginLink(t *testing.T) {
	const authURL = "https://login.tailscale.com/a/abc123"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled: true,
		State:   RemoteTailscaleLoginRequired,
		AuthURL: authURL,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("Tailscale") && tt.HasText("授权链接") })
	if !tt.HasText(authURL) {
		t.Fatalf("登录链接应显示授权地址：%v", tt.Texts())
	}

	// Tester 只能按可见文字或 a11y Label 找元素（key 不参与查找），按钮 Label 是「复制链接」。
	clickRemoteSettingsLabel(t, tt, "复制链接")
	waitRemoteCondition(t, func() bool { return tt.Clipboard() == authURL })
}

// TestSettingsRemoteTailscale_RunningEntry 覆盖：运行中显示 HTTPS 入口与最近连接，入口可复制。
func TestSettingsRemoteTailscale_RunningEntry(t *testing.T) {
	const entryURL = "https://mcode.tail1234.ts.net"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:       true,
		Hostname:      "mcode",
		State:         RemoteTailscaleRunning,
		FQDN:          "mcode.tail1234.ts.net",
		URL:           entryURL,
		AuthURL:       "https://login.tailscale.com/a/old",
		LastLoginName: "kai@example.com",
		LastNodeName:  "iPhone",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("运行中") && tt.HasText(entryURL) })
	if !tt.HasText(entryURL) {
		t.Fatalf("运行中应显示 HTTPS 入口：%v", tt.Texts())
	}
	if !tt.HasText("kai@example.com，iPhone") {
		t.Errorf("运行中应显示最近连接身份：%v", tt.Texts())
	}
	if tt.HasText("授权链接") {
		t.Errorf("运行中不应再显示授权链接")
	}
	// 域名并进入口一行展示，不再单独占一行。
	if tt.HasText("MagicDNS 域名") {
		t.Errorf("运行中不应再显示 MagicDNS 域名行：%v", tt.Texts())
	}
	if _, ok := tt.Find("复制域名"); ok {
		t.Errorf("运行中不应再有「复制域名」按钮")
	}

	// 「复制入口」与配对卡的复制按钮同款：悬停、按下有背景变化，点击后短暂回执「已复制入口」。
	clickRemoteSettingsLabel(t, tt, "复制入口")
	waitFor(t, tt, func() bool { return tt.Clipboard() == entryURL && tt.HasText("已复制入口") })
}

// TestSettingsRemoteTailscale_NeedsHTTPS 覆盖：needs_https 提示开 MagicDNS 与证书，并给出管理台按钮。
func TestSettingsRemoteTailscale_NeedsHTTPS(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled: true,
		State:   RemoteTailscaleNeedsHTTPS,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool {
		return tt.HasText("需在 Tailscale 管理台开启 MagicDNS 与 HTTPS Certificates，开启后点重试")
	})
	// 管理台按钮的 key 是 btn-remote-tailscale-console，Tester 只能按可见文字/Label「打开管理台」查找。
	if _, ok := tt.Find("打开管理台"); !ok {
		t.Errorf("needs_https 时应有打开管理台的按钮")
	}

	// 管理台里的开关可能早就打开了，这里要能直接重试：点重试会再让后端启动一次节点。
	if _, ok := tt.Find("重试"); !ok {
		t.Fatalf("needs_https 时应有重试按钮：%v", tt.Texts())
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && st.remote.tailscaleLoaded
	})
	clickRemoteSettingsLabel(t, tt, "重试")
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		calls := append([]bool(nil), fake.setRemoteTailscaleCalls...)
		return len(calls) == 1 && calls[0]
	})
}

// TestSettingsRemoteTailscale_ToggleEnabled 覆盖：开关写配置后异步调用后端，参数与保存结果都正确。
func TestSettingsRemoteTailscale_ToggleEnabled(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleDisabled}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("通过 Tailscale 访问") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy
	})

	clickRemoteSettingsLabel(t, tt, "切换通过 Tailscale 访问")

	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		calls := append([]bool(nil), fake.setRemoteTailscaleCalls...)
		fake.mu.Unlock()
		return len(calls) == 1 && calls[0]
	})

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Remote.TailscaleEnabled() {
		t.Errorf("打开开关后配置应写入 TailscaleOn=true")
	}

	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && st.remote.tailscaleStatus.Enabled
	})
}

// TestSettingsRemoteTailscale_ResetConfirm 覆盖：重置节点先弹确认框，确认后才调用后端并关闭开关。
func TestSettingsRemoteTailscale_ResetConfirm(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		URL:      "https://mcode.tail1234.ts.net",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("重置节点") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy
	})

	clickRemoteSettingsLabel(t, tt, "重置")
	waitFor(t, tt, func() bool { return tt.HasText("重置 Tailscale 节点？") })

	fake.mu.Lock()
	pendingCalls := fake.resetRemoteTailscaleCalls
	fake.mu.Unlock()
	if pendingCalls != 0 {
		t.Fatalf("确认前不应调用重置：%d", pendingCalls)
	}

	if err := tt.Click("确认重置 Tailscale 节点"); err != nil {
		t.Fatalf("click '确认重置 Tailscale 节点' failed: %v", err)
	}
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.resetRemoteTailscaleCalls == 1
	})

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Remote.TailscaleEnabled() {
		t.Errorf("重置后应关闭 TailscaleOn")
	}

	waitRemoteCondition(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy
	})
}

// activeTailscalePairing 构造一份可渲染的 active 配对信息，供配对卡相关用例复用。
func activeTailscalePairing() RemotePairing {
	const lanPairingURL = "http://192.168.1.5:43117/#pair=tok"
	return RemotePairing{
		PairingURL:    lanPairingURL,
		LANPairingURL: lanPairingURL,
		ShortCode:     "123456",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		State:         "active",
	}
}

// TestSettingsRemoteTailscale_PairingMagicDNS 覆盖：配对卡里也能复制 MagicDNS 域名，复制的是裸域名。
func TestSettingsRemoteTailscale_PairingMagicDNS(t *testing.T) {
	const fqdn = "mcode.tail1234.ts.net"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		FQDN:     fqdn,
		URL:      "https://" + fqdn,
	}
	fake.remotePairing = activeTailscalePairing()
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)

	// 配对卡默认收起，先点「配对新设备」把配对信息渲染出来；按钮可见即说明卡片已展开。
	waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
	clickRemoteSettingsLabel(t, tt, "配对新设备")
	waitFor(t, tt, func() bool { return tt.HasText("复制配对码") })
	waitFor(t, tt, func() bool { return tt.HasText("复制 MagicDNS 域名") })

	clickRemoteSettingsLabel(t, tt, "复制 MagicDNS 域名")
	waitRemoteCondition(t, func() bool { return tt.Clipboard() == fqdn })
}

// TestSettingsRemoteTailscale_PairingMagicDNSHidden 覆盖：没有域名时配对卡里不出现 MagicDNS 域名行。
func TestSettingsRemoteTailscale_PairingMagicDNSHidden(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleStarting,
	}
	fake.remotePairing = activeTailscalePairing()
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("配对新设备") })
	clickRemoteSettingsLabel(t, tt, "配对新设备")
	waitFor(t, tt, func() bool { return tt.HasText("复制配对码") })

	if tt.HasText("复制 MagicDNS 域名") {
		t.Errorf("没有域名时配对卡不应出现复制域名按钮：%v", tt.Texts())
	}
}

// TestSettingsRemoteTailscale_RefreshAfterRestart 覆盖：重启服务后重新拉取状态，改过的节点名会刷新。
func TestSettingsRemoteTailscale_RefreshAfterRestart(t *testing.T) {
	const (
		oldFQDN = "mcode-old.tail1234.ts.net"
		newFQDN = "mcode-new.tail1234.ts.net"
	)

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode-old",
		State:    RemoteTailscaleRunning,
		FQDN:     oldFQDN,
		URL:      "https://" + oldFQDN,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	// 配对卡未打开时页面上只有入口地址，所以带 https:// 前缀断言。
	waitFor(t, tt, func() bool { return tt.HasText("https://" + oldFQDN) })
	// 等两边的异步请求都结束，避免残留的在途响应覆盖重启后的新状态。
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.busy && !st.remote.tailscaleBusy
	})

	// 改节点名后重启：Tailscale 状态必须重新拉取，页面显示新域名。
	fake.mu.Lock()
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode-new",
		State:    RemoteTailscaleRunning,
		FQDN:     newFQDN,
		URL:      "https://" + newFQDN,
	}
	fake.mu.Unlock()

	restartRemote(st)
	waitFor(t, tt, func() bool { return tt.HasText("https://" + newFQDN) })
}

// TestSettingsRemoteTailscale_EntryAppearsAfterRunning 覆盖：节点运行但入口还没起来时继续轮询，入口随后自动出现。
func TestSettingsRemoteTailscale_EntryAppearsAfterRunning(t *testing.T) {
	const fqdn = "mcode.tail1234.ts.net"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		FQDN:     fqdn,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	// 入口后面才有 URL，此时页面只靠「运行中」证明状态已加载；裸 fqdn 不再单独展示。
	waitFor(t, tt, func() bool { return tt.HasText("运行中") })
	if tt.HasText("https://" + fqdn) {
		t.Fatalf("入口未起来前不应显示 HTTPS 入口：%v", tt.Texts())
	}

	// 入口稍后才开始服务：running 且没有 URL 时页面继续轮询，几秒内应自动补上这一行。
	fake.mu.Lock()
	fake.remoteTailscaleStatus.URL = "https://" + fqdn
	fake.mu.Unlock()

	// 测试时钟要手动推进：物理时间不走，After(2s) 的轮询定时器不会自己到点。
	tt.Advance(2 * time.Second)
	waitFor(t, tt, func() bool { return tt.HasText("https://" + fqdn) })
}

// TestSettingsRemoteTailscale_RecentConnectionRefreshes 覆盖：入口已在时仍低频刷新，
// 手机后来连上也能在几秒内显示最近连接。
func TestSettingsRemoteTailscale_RecentConnectionRefreshes(t *testing.T) {
	const entryURL = "https://mcode.tail1234.ts.net"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		FQDN:     "mcode.tail1234.ts.net",
		URL:      entryURL,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText(entryURL) && tt.HasText("尚无通过 Tailscale 的连接") })

	// 轮询不应该占用 tailscaleBusy，否则开关会跟着闪一下禁用。
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && !st.remote.tailscaleRefreshing
	})

	fake.mu.Lock()
	fake.remoteTailscaleStatus.LastLoginName = "kai@example.com"
	fake.remoteTailscaleStatus.LastNodeName = "iPhone"
	fake.mu.Unlock()

	// 运行中的低频刷新靠 After(5s)，测试时钟要手动推进。
	tt.Advance(5 * time.Second)
	waitFor(t, tt, func() bool { return tt.HasText("kai@example.com，iPhone") })
}

// TestSettingsRemoteTailscale_RefreshFailureKeepsEntry 覆盖：后台刷新失败时保留旧状态，也不污染错误文案。
func TestSettingsRemoteTailscale_RefreshFailureKeepsEntry(t *testing.T) {
	const entryURL = "https://mcode.tail1234.ts.net"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:       true,
		Hostname:      "mcode",
		State:         RemoteTailscaleRunning,
		FQDN:          "mcode.tail1234.ts.net",
		URL:           entryURL,
		LastLoginName: "kai@example.com",
		LastNodeName:  "iPhone",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText(entryURL) && tt.HasText("kai@example.com，iPhone") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && !st.remote.tailscaleRefreshing
	})

	// 后台刷新失败：页面保持原样，也不把错误写成卡片提示。
	fake.mu.Lock()
	fake.remoteTailscaleErr = errors.New("tailscale 状态读取失败")
	fake.mu.Unlock()

	tt.Advance(5 * time.Second)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleRefreshing
	})
	tt.Frame()
	if !tt.HasText(entryURL) {
		t.Errorf("刷新失败后仍应显示 HTTPS 入口：%v", tt.Texts())
	}
	if !tt.HasText("kai@example.com，iPhone") {
		t.Errorf("刷新失败后应保留旧状态：%v", tt.Texts())
	}
	if tt.HasText("tailscale 状态读取失败") {
		t.Errorf("后台刷新失败不应显示错误文案：%v", tt.Texts())
	}
}

// TestSettingsRemoteTailscale_FailedShowsRetry 覆盖：启动失败时显示失败原因，并且能就地重试。
func TestSettingsRemoteTailscale_FailedShowsRetry(t *testing.T) {
	const failure = "tailnet 节点启动失败：监听 443 端口被占用"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled: true,
		State:   RemoteTailscaleFailed,
		Error:   failure,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("启动失败") && tt.HasText(failure) })
	if _, ok := tt.Find("重试"); !ok {
		t.Fatalf("启动失败时应有重试按钮：%v", tt.Texts())
	}

	// 后端的「启动失败」可以重试：再调一次启用就会重新拉起节点。
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && st.remote.tailscaleLoaded
	})
	clickRemoteSettingsLabel(t, tt, "重试")
	waitRemoteCondition(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		calls := append([]bool(nil), fake.setRemoteTailscaleCalls...)
		return len(calls) == 1 && calls[0]
	})
}

// TestSettingsRemoteTailscale_ErrorVisibleWhenDisabled 覆盖：开关关闭时也要能看到状态读取失败的原因。
func TestSettingsRemoteTailscale_ErrorVisibleWhenDisabled(t *testing.T) {
	const failure = "读取 Tailscale 状态失败：找不到 tailscale 命令"

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleDisabled}
	fake.remoteTailscaleErr = errors.New(failure)
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}

	tt, _ := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText(failure) })
	if _, ok := tt.Find("Tailscale 错误"); !ok {
		t.Errorf("开关关闭时也应有一行「Tailscale 错误」：%v", tt.Texts())
	}
}

// TestSettingsRemoteTailscale_SaveFailureRollsBack 覆盖：配置保存失败时不启动后端，开关与错误都落回本地状态。
func TestSettingsRemoteTailscale_SaveFailureRollsBack(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleDisabled}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("通过 Tailscale 访问") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && st.remote.tailscaleLoaded
	})

	// 改出一个过不了校验的值，让接下来这次保存必然失败。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.Startup.QuitAction = "bogus"
	st.cfgMu.Unlock()
	st.mu.Unlock()

	clickRemoteSettingsLabel(t, tt, "切换通过 Tailscale 访问")
	waitRemoteCondition(t, func() bool {
		st.cfgMu.Lock()
		defer st.cfgMu.Unlock()
		return st.errorMessage != ""
	})

	fake.mu.Lock()
	calls := append([]bool(nil), fake.setRemoteTailscaleCalls...)
	fake.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("保存失败时不应调用后端启停：%v", calls)
	}

	st.mu.Lock()
	enabled := st.cfg.Remote.TailscaleEnabled()
	errText := st.remote.tailscaleErr
	busy := st.remote.tailscaleBusy
	st.mu.Unlock()
	if enabled {
		t.Errorf("保存失败时开关应回滚为关闭")
	}
	if busy {
		t.Errorf("保存失败后不应留着 busy")
	}
	if !strings.Contains(errText, "startup.quit_action") {
		t.Errorf("Tailscale 卡片应显示保存失败原因：%q", errText)
	}
	// 开关已经退回关闭，错误只能在关闭分支里看到。
	waitFor(t, tt, func() bool { return tt.HasText(errText) })
}

// TestSettingsRemoteTailscale_HostnameValidation 覆盖：非法节点名不写配置并提示，合法名保存后提示消失。
func TestSettingsRemoteTailscale_HostnameValidation(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		URL:      "https://mcode.tail1234.ts.net",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("节点名称") })

	// 下划线不合 DNS label 规则：只提示，不写内存也不落盘。
	applyRemoteTailscaleHostname(st, "a_b")
	waitFor(t, tt, func() bool { return tt.HasText(remoteTailscaleHostnameErrText) })

	st.mu.Lock()
	host := st.cfg.Remote.TailscaleHostname
	errText := st.remote.tailscaleHostnameErr
	st.mu.Unlock()
	if host != "" {
		t.Errorf("非法节点名不应写入配置，得到 %q", host)
	}
	if errText == "" {
		t.Errorf("非法节点名应记下提示文案")
	}
	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Remote.TailscaleHostname != "" {
		t.Errorf("非法节点名不应落盘，得到 %q", loaded.Remote.TailscaleHostname)
	}

	// 合法名去空白后保存，提示退回原来的说明。
	applyRemoteTailscaleHostname(st, " my-mac ")
	waitFor(t, tt, func() bool { return !tt.HasText(remoteTailscaleHostnameErrText) })
	waitRemoteCondition(t, func() bool {
		saved, err := config.Load(dataDir)
		return err == nil && saved.Remote.TailscaleHostname == "my-mac"
	})
	st.mu.Lock()
	errText = st.remote.tailscaleHostnameErr
	st.mu.Unlock()
	if errText != "" {
		t.Errorf("合法节点名应清空提示，得到 %q", errText)
	}
}

// TestSettingsRemoteTailscale_ResetSaveFailureAborts 覆盖：重置时保存失败就不动后端，开关回滚、错误可见。
func TestSettingsRemoteTailscale_ResetSaveFailureAborts(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		URL:      "https://mcode.tail1234.ts.net",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("重置节点") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && st.remote.tailscaleLoaded
	})

	// 改出一个过不了校验的值，让重置前的这次保存必然失败。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.Startup.QuitAction = "bogus"
	st.cfgMu.Unlock()
	st.mu.Unlock()

	resetRemoteTailscale(st)
	waitRemoteCondition(t, func() bool {
		st.cfgMu.Lock()
		defer st.cfgMu.Unlock()
		return st.errorMessage != ""
	})

	fake.mu.Lock()
	calls := fake.resetRemoteTailscaleCalls
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("保存失败时不应调用后端重置：%d", calls)
	}

	st.mu.Lock()
	enabled := st.cfg.Remote.TailscaleEnabled()
	busy := st.remote.tailscaleBusy
	errText := st.remote.tailscaleErr
	st.mu.Unlock()
	if !enabled {
		t.Errorf("保存失败时开关应回滚为开启")
	}
	if busy {
		t.Errorf("保存失败后不应留着 busy")
	}
	if !strings.Contains(errText, "startup.quit_action") {
		t.Errorf("Tailscale 卡片应显示保存失败原因：%q", errText)
	}
	// 开关仍是开启状态，错误显示在「连接状态」行里。
	waitFor(t, tt, func() bool { return tt.HasText(errText) })

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Remote.TailscaleEnabled() {
		t.Errorf("保存失败不应改动磁盘上的开关")
	}
}

// TestSettingsRemoteTailscale_StaleRefreshDiscarded 覆盖：启停/重置之后，在途的静默刷新结果不再写回。
func TestSettingsRemoteTailscale_StaleRefreshDiscarded(t *testing.T) {
	const (
		oldEntry = "https://mcode-a.tail1234.ts.net"
		newEntry = "https://mcode-b.tail1234.ts.net"
	)

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode-a",
		State:    RemoteTailscaleRunning,
		FQDN:     "mcode-a.tail1234.ts.net",
		URL:      oldEntry,
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText(oldEntry) })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy && !st.remote.tailscaleRefreshing && st.remote.tailscaleLoaded
	})

	// 让下一次刷新先读到新快照，再卡在返回之前。
	gate := make(chan struct{})
	fake.mu.Lock()
	fake.remoteTailscaleGate = gate
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode-b",
		State:    RemoteTailscaleRunning,
		FQDN:     "mcode-b.tail1234.ts.net",
		URL:      newEntry,
	}
	fake.mu.Unlock()

	refreshRemoteTailscaleStatus(st)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.tailscaleRefreshing
	})

	// 启停与重置都会递增代号（这里直接递增模拟并发的启停），在途结果就该作废。
	st.mu.Lock()
	st.remote.tailscaleGen++
	st.mu.Unlock()
	close(gate)

	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleRefreshing
	})
	tt.Frame()
	if tt.HasText(newEntry) {
		t.Errorf("代号已变，过期的刷新结果不应写回：%v", tt.Texts())
	}
	if !tt.HasText(oldEntry) {
		t.Errorf("过期结果被丢弃后应保持原入口：%v", tt.Texts())
	}
}

// TestSettingsRemoteTailscale_ResetDisabledWhenStopped 覆盖：服务没跑时重置按钮禁用，不留下看不见的确认标记。
func TestSettingsRemoteTailscale_ResetDisabledWhenStopped(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateStopped}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleDisabled}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("需先启动远程控制服务") })

	clickRemoteSettingsLabel(t, tt, "重置")
	tt.Frame()
	st.mu.Lock()
	confirm := st.remote.tailscaleResetConfirm
	st.mu.Unlock()
	if confirm {
		t.Errorf("服务未运行时点重置不应置确认标记")
	}
	if tt.HasText("重置 Tailscale 节点？") {
		t.Errorf("服务未运行时不应弹出重置确认框：%v", tt.Texts())
	}
}

// TestSettingsRemoteTailscale_ResetConfirmClearedWhenStopped 覆盖：确认框开着时服务停了，下次启动不该残留弹窗。
func TestSettingsRemoteTailscale_ResetConfirmClearedWhenStopped(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.remoteStatus = RemoteStatus{State: RemoteStateRunning, Port: 43117}
	fake.remoteTailscaleStatus = RemoteTailscaleStatus{
		Enabled:  true,
		Hostname: "mcode",
		State:    RemoteTailscaleRunning,
		URL:      "https://mcode.tail1234.ts.net",
	}
	fake.mu.Unlock()

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBool(true)
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	waitFor(t, tt, func() bool { return tt.HasText("重置节点") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleBusy
	})

	clickRemoteSettingsLabel(t, tt, "重置")
	waitFor(t, tt, func() bool { return tt.HasText("重置 Tailscale 节点？") })

	// 确认框开着的时候服务被停掉：卡片不再渲染，确认标记必须一起清掉。
	st.mu.Lock()
	st.remote.status = RemoteStatus{State: RemoteStateStopped}
	st.mu.Unlock()
	st.invalidate()

	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.remote.tailscaleResetConfirm
	})
	if tt.HasText("重置 Tailscale 节点？") {
		t.Errorf("服务停止后确认框应消失：%v", tt.Texts())
	}
}
