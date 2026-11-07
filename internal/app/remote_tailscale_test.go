package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/tailnet"
	mcodeui "mcode/internal/ui"
)

// fakeTailnetManager 替代 tsnet 节点，避免测试真正联网。
type fakeTailnetManager struct {
	mu       sync.Mutex
	status   tailnet.Status
	startErr error
	listener net.Listener
	// listenerErr 让 Listener 报错；listenerStatus 非空时会把管理器状态改成它，
	// 模拟真实 Manager 在 ListenTLS 失败后写状态的行为。
	listenerErr    error
	listenerStatus tailnet.Status
	identity       tailnet.Identity
	whoErr         error
	opts           tailnet.Options
	startN         int
	stopN          int
	// stopErr 是 Stop 的返回值；stopBlock 非 nil 时 Stop 会等它关闭，模拟收尾卡住。
	stopErr   error
	stopBlock chan struct{}
	// done 是 Done 返回的通道；测试用 closeDone 控制旧节点彻底退出的时机。
	done chan struct{}
}

func (f *fakeTailnetManager) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startN++
	return f.startErr
}

func (f *fakeTailnetManager) Stop(ctx context.Context) error {
	f.mu.Lock()
	f.stopN++
	block := f.stopBlock
	err := f.stopErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return err
}

func (f *fakeTailnetManager) Listener() (net.Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listenerErr != nil {
		if f.listenerStatus.State != "" {
			f.status = f.listenerStatus
		}
		return nil, f.listenerErr
	}
	if f.listener == nil {
		return nil, errors.New("fake: no listener")
	}
	return f.listener, nil
}

func (f *fakeTailnetManager) Status() tailnet.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeTailnetManager) Done() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done == nil {
		f.done = make(chan struct{})
	}
	return f.done
}

// closeDone 关闭 Done 通道，模拟节点彻底退出；重复调用安全。
func (f *fakeTailnetManager) closeDone() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done == nil {
		f.done = make(chan struct{})
	}
	select {
	case <-f.done:
	default:
		close(f.done)
	}
}

func (f *fakeTailnetManager) WhoIs(ctx context.Context, remoteAddr string) (tailnet.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, f.whoErr
}

func (f *fakeTailnetManager) setStatus(st tailnet.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = st
}

func (f *fakeTailnetManager) called() (start, stop int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startN, f.stopN
}

func tailscaleBoolPtr(b bool) *bool { return &b }

// installFakeTailnet 替换 newTailnetManager，Cleanup 时还原。
func installFakeTailnet(t *testing.T, fake *fakeTailnetManager) {
	t.Helper()
	prev := newTailnetManager
	newTailnetManager = func(opts tailnet.Options) tailnetManager {
		fake.mu.Lock()
		fake.opts = opts
		fake.mu.Unlock()
		return fake
	}
	t.Cleanup(func() { newTailnetManager = prev })
}

// saveTailscaleConfig 写入启用 Tailscale 的配置，tlsDomain 为空表示未配自定义域名。
func saveTailscaleConfig(t *testing.T, dataDir, tlsDomain string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Remote.TailscaleOn = tailscaleBoolPtr(true)
	cfg.Remote.TLSDomain = tlsDomain
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// installFakeTailnetSeq 让每次 newTailnetManager 依次返回给定 fake，用完后一直返回最后一个。
// 用于覆盖重新启用、重建入口时会新建管理器的场景。
func installFakeTailnetSeq(t *testing.T, fakes ...*fakeTailnetManager) {
	t.Helper()
	if len(fakes) == 0 {
		t.Fatal("至少需要一个 fake")
	}
	prev := newTailnetManager
	var mu sync.Mutex
	next := 0
	newTailnetManager = func(opts tailnet.Options) tailnetManager {
		mu.Lock()
		idx := next
		if idx >= len(fakes) {
			idx = len(fakes) - 1
		} else {
			next++
		}
		fake := fakes[idx]
		mu.Unlock()
		fake.mu.Lock()
		fake.opts = opts
		fake.mu.Unlock()
		return fake
	}
	t.Cleanup(func() { newTailnetManager = prev })
}

// shortenTailscaleWatch 把运行中监视间隔调小，Cleanup 时还原。
// 启动入口时会在 lifecycle 锁内取值传给后台协程，所以必须在启动前调用。
func shortenTailscaleWatch(t *testing.T, interval time.Duration) {
	t.Helper()
	prev := tailscaleWatchInterval
	tailscaleWatchInterval = interval
	t.Cleanup(func() { tailscaleWatchInterval = prev })
}

// shortenTailscalePrevWait 把新节点等旧节点退出的上限调小，Cleanup 时还原。
// 同上，取值发生在启动入口时，必须在启动前调用。
func shortenTailscalePrevWait(t *testing.T, timeout time.Duration) {
	t.Helper()
	prev := tailscalePrevWaitTimeout
	tailscalePrevWaitTimeout = timeout
	t.Cleanup(func() { tailscalePrevWaitTimeout = prev })
}

// waitForTailscaleRegistered 等到管理器已登记进会话：停用前必须确认会话里已经有它，
// 否则 detach 看不到旧节点，也就记不下它的关闭通道。
func waitForTailscaleRegistered(t *testing.T, rs *RemoteService) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rs.mu.Lock()
		mgr := rs.tailscale
		rs.mu.Unlock()
		if mgr != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待 Tailscale 管理器登记超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForTailscale 轮询 Tailscale 状态，直到条件满足或超时。
func waitForTailscale(t *testing.T, rs *RemoteService, cond func(mcodeui.RemoteTailscaleStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := rs.TailscaleStatus()
		if cond(st) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 Tailscale 状态超时，最后状态 = %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRemoteTailscaleStartFailureKeepsServiceRunning(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	installFakeTailnet(t, &fakeTailnetManager{startErr: errors.New("tailnet: 无法登录")})
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	// Tailscale 启动失败不影响局域网与 TLS 入口。
	if got := rs.Status().State; got != mcodeui.RemoteStateRunning {
		t.Fatalf("远程服务状态 = %q, 期望 running", got)
	}
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return strings.Contains(st.Error, "无法登录")
	})

	st := rs.TailscaleStatus()
	if !st.Enabled {
		t.Fatalf("Enabled = false, 期望 true: %+v", st)
	}
	if st.Hostname != "mcode" {
		t.Fatalf("Hostname = %q, 期望 mcode", st.Hostname)
	}
	// 入口没起来：状态落到 failed，不再永远停在 starting。
	if st.State != string(tailnet.StateFailed) {
		t.Fatalf("State = %q, 期望 failed", st.State)
	}
}

func TestRemoteTailscalePairingURLSwitchesAfterServe(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "mcode.example.com")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	ln := newTailscaleTestListener(t)
	t.Cleanup(func() { _ = ln.Close() })

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{
		status:   tailnet.Status{State: tailnet.StateLoginRequired},
		listener: ln,
	}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	// 入口尚未开始服务：主链接仍是 TLS 域名回落的 http 地址。
	before, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing: %v", err)
	}
	if !strings.HasPrefix(before.PairingURL, "http://mcode.example.com:") {
		t.Fatalf("PairingURL = %q, 期望 TLS 域名回落地址", before.PairingURL)
	}
	if before.TLSPairingURL != before.PairingURL {
		t.Fatalf("TLSPairingURL = %q, 期望与主链接一致 %q", before.TLSPairingURL, before.PairingURL)
	}
	if !strings.HasPrefix(before.LANPairingURL, "http://127.0.0.1:") {
		t.Fatalf("LANPairingURL = %q, 期望局域网地址", before.LANPairingURL)
	}
	if before.TailscalePairingURL != "" {
		t.Fatalf("TailscalePairingURL = %q, 期望空串", before.TailscalePairingURL)
	}

	// 节点上线且入口真正开始服务（URL 已写入）后，主链接才切到 Tailscale。
	fake.setStatus(tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-ts.example.ts.net"})
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == mcodeui.RemoteTailscaleRunning && st.URL != ""
	})

	after, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing after serve: %v", err)
	}
	if !strings.HasPrefix(after.PairingURL, "https://mcode-ts.example.ts.net/#pair=") {
		t.Fatalf("PairingURL = %q, 期望 Tailscale 链接", after.PairingURL)
	}
	if after.TLSPairingURL != before.TLSPairingURL {
		t.Fatalf("TLSPairingURL 变化: %q → %q", before.TLSPairingURL, after.TLSPairingURL)
	}
	if after.LANPairingURL != before.LANPairingURL {
		t.Fatalf("LANPairingURL 变化: %q → %q", before.LANPairingURL, after.LANPairingURL)
	}
	if !strings.HasPrefix(after.TailscalePairingURL, "https://mcode-ts.example.ts.net/#pair=") {
		t.Fatalf("TailscalePairingURL = %q, 期望 Tailscale 链接", after.TailscalePairingURL)
	}

	st := rs.TailscaleStatus()
	if st.FQDN != "mcode-ts.example.ts.net" {
		t.Fatalf("FQDN = %q", st.FQDN)
	}
	if st.URL != "https://mcode-ts.example.ts.net" {
		t.Fatalf("URL = %q, 期望入口地址", st.URL)
	}
}

func TestRemoteTailscaleStopIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 等管理器真正建好，确保 Stop 走的是 Tailscale 清理分支而不是空转。
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, _ := fake.called()
		return start > 0
	})

	if err := rs.Stop(context.Background()); err != nil {
		t.Fatalf("第一次 Stop: %v", err)
	}
	if err := rs.Stop(context.Background()); err != nil {
		t.Fatalf("第二次 Stop: %v", err)
	}

	if _, stop := fake.called(); stop != 1 {
		t.Fatalf("manager.Stop 调用次数 = %d, 期望 1", stop)
	}
	st := rs.TailscaleStatus()
	if st.State != mcodeui.RemoteTailscaleDisabled {
		t.Fatalf("State = %q, 期望 disabled", st.State)
	}
	if st.URL != "" {
		t.Fatalf("URL = %q, 期望清空", st.URL)
	}
}

func TestRemoteTailscaleLoginRequiredWithoutAuthURLIsStarting(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	// 需要登录但还没拿到 AuthURL：映射为 starting，而不是 login_required。
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == mcodeui.RemoteTailscaleStarting
	})
	if st := rs.TailscaleStatus(); st.AuthURL != "" {
		t.Fatalf("AuthURL = %q, 期望空串", st.AuthURL)
	}

	// AuthURL 就绪后原样映射为 login_required。
	fake.setStatus(tailnet.Status{
		State:   tailnet.StateLoginRequired,
		AuthURL: "https://login.tailscale.com/a/test",
	})
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == mcodeui.RemoteTailscaleLoginRequired && st.AuthURL != ""
	})
}

// newTailscaleTestListener 返回包好自签证书的本地监听，模拟 tsnet 已握手的 listener。
func newTailscaleTestListener(t *testing.T) net.Listener {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mcode-tailscale-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"mcode-ts.example.ts.net"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return tls.NewListener(raw, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"http/1.1"},
	})
}

// TestIdentifyTailnetPeerPrefersComputedName 覆盖：WhoIs 成功时 ComputedName 优先于 NodeName，并记下最近连接身份。
func TestIdentifyTailnetPeerPrefersComputedName(t *testing.T) {
	fake := &fakeTailnetManager{
		identity: tailnet.Identity{
			LoginName:    "kai@example.com",
			NodeName:     "iphone.tail1234.ts.net.",
			ComputedName: "iphone",
		},
	}
	rs := &RemoteService{tailscale: fake}

	got, err := rs.identifyTailnetPeer(fake, context.Background(), "100.64.0.2:443")
	if err != nil {
		t.Fatalf("identifyTailnetPeer: %v", err)
	}
	if got.NodeName != "iphone" {
		t.Errorf("ComputedName 应优先于 NodeName：%q", got.NodeName)
	}
	if got.LoginName != "kai@example.com" {
		t.Errorf("登录名应原样保留：%q", got.LoginName)
	}

	rs.mu.Lock()
	who := rs.tailscaleWho
	rs.mu.Unlock()
	if who != got {
		t.Errorf("最近连接身份应记录本次结果：%+v", who)
	}
}

// TestIdentifyTailnetPeerKeepsOldWhoOnError 覆盖：WhoIs 失败时返回错误，且不覆盖上一次的连接身份。
func TestIdentifyTailnetPeerKeepsOldWhoOnError(t *testing.T) {
	fake := &fakeTailnetManager{whoErr: errors.New("fake: whois 失败")}
	old := remote.ClientIdentity{LoginName: "kai@example.com", NodeName: "iPad"}
	rs := &RemoteService{tailscale: fake, tailscaleWho: old}

	if _, err := rs.identifyTailnetPeer(fake, context.Background(), "100.64.0.3:443"); err == nil {
		t.Fatal("WhoIs 失败时应返回错误")
	}

	rs.mu.Lock()
	who := rs.tailscaleWho
	rs.mu.Unlock()
	if who != old {
		t.Errorf("失败时不应覆盖旧身份：%+v", who)
	}
}

// TestRemoteTailscaleStartFailureIsRetryable 覆盖：启动失败后状态是 failed 而非 starting，
// 且会话已摘下（tailscaleCancel 为 nil），重新启用会重新启动管理器。
func TestRemoteTailscaleStartFailureIsRetryable(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{startErr: errors.New("tailnet: 无法登录")}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == string(tailnet.StateFailed)
	})
	if st := rs.TailscaleStatus(); !strings.Contains(st.Error, "无法登录") {
		t.Fatalf("Error = %q，期望包含启动失败原因", st.Error)
	}

	rs.mu.Lock()
	cancel := rs.tailscaleCancel
	mgr := rs.tailscale
	failState := rs.tailscaleFailState
	rs.mu.Unlock()
	if cancel != nil {
		t.Fatalf("启动失败后 tailscaleCancel 应为 nil")
	}
	if mgr != nil {
		t.Fatalf("启动失败后不应保留管理器: %+v", mgr)
	}
	if failState != tailnet.StateFailed {
		t.Fatalf("tailscaleFailState = %q，期望 %q", failState, tailnet.StateFailed)
	}

	if start, _ := fake.called(); start != 1 {
		t.Fatalf("首次启动的 Start 次数 = %d，期望 1", start)
	}
	// 失败后开关可以重试：重新启用会再建一次管理器。
	if _, err := rs.SetTailscaleEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetTailscaleEnabled: %v", err)
	}
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, _ := fake.called()
		return start >= 2
	})
}

// TestRemoteTailscaleManagerFailedStateCleansUp 覆盖：管理器上报 StateFailed 时等待循环退出并清理入口。
func TestRemoteTailscaleManagerFailedStateCleansUp(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateFailed, Error: "tailnet: 连接失败: 拒绝连接"}}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == string(tailnet.StateFailed) && st.URL == ""
	})
	if st := rs.TailscaleStatus(); !strings.Contains(st.Error, "拒绝连接") {
		t.Fatalf("Error = %q，期望保留管理器上报的失败原因", st.Error)
	}

	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		_, stop := fake.called()
		return stop >= 1
	})
	rs.mu.Lock()
	mgr := rs.tailscale
	cancel := rs.tailscaleCancel
	rs.mu.Unlock()
	if mgr != nil || cancel != nil {
		t.Fatalf("失败后入口应已清理：mgr=%v cancel!=nil=%v", mgr, cancel != nil)
	}
}

// TestRemoteTailscaleListenerNeedsHTTPS 覆盖：ListenTLS 因缺少 MagicDNS / HTTPS 失败时状态为 needs_https。
func TestRemoteTailscaleListenerNeedsHTTPS(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	const text = "tsnet: you must enable HTTPS in the admin panel to proceed. See https://tailscale.com/s/https"
	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{
		status:         tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-ts.example.ts.net"},
		listenerErr:    errors.New(text),
		listenerStatus: tailnet.Status{State: tailnet.StateNeedsHTTPS, Error: text},
	}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == mcodeui.RemoteTailscaleNeedsHTTPS
	})
}

// failingListener 在 Accept 时返回错误，模拟 Tailscale 入口 listener 异常退出。
type failingListener struct{}

func (l *failingListener) Accept() (net.Conn, error) {
	return nil, errors.New("fake: accept 失败")
}

func (l *failingListener) Close() error { return nil }

func (l *failingListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

// TestRemoteTailscaleServeFailureFallsBackPairingURL 覆盖：入口 serve 失败后状态为 failed，
// 配对主链接回落到 TLS / LAN，不再指向已经死掉的 Tailscale 入口。
func TestRemoteTailscaleServeFailureFallsBackPairingURL(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "mcode.example.com")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{
		status:   tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-ts.example.ts.net"},
		listener: &failingListener{},
	}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })

	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == string(tailnet.StateFailed) && st.URL == ""
	})

	pr, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing: %v", err)
	}
	if strings.HasPrefix(pr.PairingURL, "https://mcode-ts.example.ts.net") {
		t.Fatalf("PairingURL = %q，期望回落到 TLS / LAN 地址", pr.PairingURL)
	}
	if pr.TailscalePairingURL != "" {
		t.Fatalf("TailscalePairingURL = %q，期望空串", pr.TailscalePairingURL)
	}
}

// TestRemoteTailscaleResetRemovesStateDir 覆盖：重置成功时删除节点状态目录。
func TestRemoteTailscaleResetRemovesStateDir(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	stateDir := filepath.Join(dataDir, tailscaleDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("准备状态目录: %v", err)
	}

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, _ := fake.called()
		return start > 0
	})

	if err := rs.ResetTailscale(context.Background()); err != nil {
		t.Fatalf("ResetTailscale: %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("状态目录应被删除，stat err = %v", err)
	}
}

// TestRemoteTailscaleResetKeepsStateDirWhenStopTimesOut 覆盖：管理器没停下来时保留状态目录并返回错误。
func TestRemoteTailscaleResetKeepsStateDirWhenStopTimesOut(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	stateDir := filepath.Join(dataDir, tailscaleDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("准备状态目录: %v", err)
	}

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{
		status:  tailnet.Status{State: tailnet.StateLoginRequired},
		stopErr: context.DeadlineExceeded,
	}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, _ := fake.called()
		return start > 0
	})

	err := rs.ResetTailscale(context.Background())
	if err == nil {
		t.Fatal("节点未停止时应返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应包装 context.DeadlineExceeded，得到 %v", err)
	}
	if _, statErr := os.Stat(stateDir); statErr != nil {
		t.Fatalf("状态目录应保留，stat err = %v", statErr)
	}
}

// TestRemoteTailscaleDisableDoesNotHoldServiceLock 覆盖：停用入口时不在持 s.mu 的情况下等 tsnet 收尾，
// 否则正在 WhoIs 的请求会让状态查询与配对全部冻结。
func TestRemoteTailscaleDisableDoesNotHoldServiceLock(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	rs := NewRemoteService(dataDir, backend)
	fake := &fakeTailnetManager{
		status:    tailnet.Status{State: tailnet.StateLoginRequired},
		stopBlock: make(chan struct{}),
	}
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, _ := fake.called()
		return start > 0
	})

	disabled := make(chan struct{})
	go func() {
		defer close(disabled)
		_, _ = rs.SetTailscaleEnabled(context.Background(), false)
	}()

	// 等收尾真正卡在 mgr.Stop 上，再检查 s.mu 是否还能拿到。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, stop := fake.called(); stop >= 1 {
			break
		}
		if time.Now().After(deadline) {
			close(fake.stopBlock)
			t.Fatal("停用入口后没有等到管理器收尾")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 收尾被卡住期间仍能拿到 s.mu，说明阻塞调用不在锁内。
	locked := make(chan struct{})
	go func() {
		rs.mu.Lock()
		rs.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		close(fake.stopBlock)
		t.Fatal("停用入口时仍持有 s.mu，状态查询会被堵住")
	}
	close(fake.stopBlock)
	<-disabled
}

// TestIdentifyTailnetPeerIgnoresStaleManager 覆盖：已经换掉的旧入口不能覆盖新会话的身份。
func TestIdentifyTailnetPeerIgnoresStaleManager(t *testing.T) {
	stale := &fakeTailnetManager{identity: tailnet.Identity{LoginName: "kai@example.com", ComputedName: "iphone"}}
	current := &fakeTailnetManager{}
	old := remote.ClientIdentity{LoginName: "kai@example.com", NodeName: "iPad"}
	rs := &RemoteService{tailscale: current, tailscaleWho: old}

	if _, err := rs.identifyTailnetPeer(stale, context.Background(), "100.64.0.9:443"); err != nil {
		t.Fatalf("identifyTailnetPeer: %v", err)
	}
	rs.mu.Lock()
	who := rs.tailscaleWho
	rs.mu.Unlock()
	if who != old {
		t.Fatalf("旧入口的连接不应覆盖新会话身份：%+v", who)
	}
}

// TestRemoteTailscaleWaitsPreviousDone 覆盖：上一个节点还没彻底退出时，重新启用不会新建节点，
// 只有旧节点的 Done 关闭后才真正启动，避免两个节点共用一个状态目录。
func TestRemoteTailscaleWaitsPreviousDone(t *testing.T) {
	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	oldFake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	newFake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	rs := NewRemoteService(dataDir, backend)
	installFakeTailnetSeq(t, oldFake, newFake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscaleRegistered(t, rs)

	// 停用后再启用：旧节点的 Done 故意不关，新节点必须停在启动中。
	if _, err := rs.SetTailscaleEnabled(context.Background(), false); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := rs.SetTailscaleEnabled(context.Background(), true); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == mcodeui.RemoteTailscaleStarting
	})
	if start, _ := newFake.called(); start != 0 {
		t.Fatalf("旧节点未退出时不应启动新节点，Start 次数 = %d", start)
	}

	// 旧节点彻底退出后，新节点才开始启动。
	oldFake.closeDone()
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, _ := newFake.called()
		return start > 0
	})
}

// TestRemoteTailscalePreviousDoneTimeout 覆盖：旧节点迟迟不退出时落成 failed，并提示稍后重试。
func TestRemoteTailscalePreviousDoneTimeout(t *testing.T) {
	shortenTailscalePrevWait(t, 50*time.Millisecond)

	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	oldFake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	newFake := &fakeTailnetManager{status: tailnet.Status{State: tailnet.StateLoginRequired}}
	rs := NewRemoteService(dataDir, backend)
	installFakeTailnetSeq(t, oldFake, newFake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscaleRegistered(t, rs)

	if _, err := rs.SetTailscaleEnabled(context.Background(), false); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := rs.SetTailscaleEnabled(context.Background(), true); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.State == string(tailnet.StateFailed)
	})
	if st := rs.TailscaleStatus(); !strings.Contains(st.Error, "仍未退出") {
		t.Fatalf("Error = %q，期望提示旧节点仍未退出", st.Error)
	}
	if start, _ := newFake.called(); start != 0 {
		t.Fatalf("超时后不应启动新节点，Start 次数 = %d", start)
	}
}

// TestRemoteTailscaleRebuildsEntryWhenFQDNChanges 覆盖：节点改名后入口重建，配对主链接换成新域名。
func TestRemoteTailscaleRebuildsEntryWhenFQDNChanges(t *testing.T) {
	shortenTailscaleWatch(t, 10*time.Millisecond)

	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "mcode.example.com")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	oldLn := newTailscaleTestListener(t)
	t.Cleanup(func() { _ = oldLn.Close() })
	newLn := newTailscaleTestListener(t)
	t.Cleanup(func() { _ = newLn.Close() })

	oldFake := &fakeTailnetManager{
		status:   tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-old.example.ts.net"},
		listener: oldLn,
	}
	newFake := &fakeTailnetManager{
		status:   tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-new.example.ts.net"},
		listener: newLn,
	}
	rs := NewRemoteService(dataDir, backend)
	installFakeTailnetSeq(t, oldFake, newFake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.URL == "https://mcode-old.example.ts.net"
	})

	// 节点改名：旧域名不再解析，入口必须换成新域名。
	oldFake.setStatus(tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-new.example.ts.net"})
	waitForTailscale(t, rs, func(mcodeui.RemoteTailscaleStatus) bool {
		start, stop := oldFake.called()
		return start > 0 && stop > 0
	})
	// 旧节点的 Done 在 tsnet 真正收完之前不会关，这里手动放行。
	oldFake.closeDone()
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.URL == "https://mcode-new.example.ts.net"
	})

	pr, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing: %v", err)
	}
	if !strings.HasPrefix(pr.PairingURL, "https://mcode-new.example.ts.net/#pair=") {
		t.Fatalf("PairingURL = %q，期望新域名", pr.PairingURL)
	}
	if !strings.HasPrefix(pr.TailscalePairingURL, "https://mcode-new.example.ts.net/#pair=") {
		t.Fatalf("TailscalePairingURL = %q，期望新域名", pr.TailscalePairingURL)
	}
}

// TestRemoteTailscaleLoginRequiredFallsBackPairingURL 覆盖：运行中转为需要重新登录时配对主链接回落，
// 恢复运行后重新指向 Tailscale 入口。
func TestRemoteTailscaleLoginRequiredFallsBackPairingURL(t *testing.T) {
	shortenTailscaleWatch(t, 10*time.Millisecond)

	dataDir := t.TempDir()
	saveTailscaleConfig(t, dataDir, "mcode.example.com")
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()

	ln := newTailscaleTestListener(t)
	t.Cleanup(func() { _ = ln.Close() })

	fake := &fakeTailnetManager{
		status:   tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-ts.example.ts.net"},
		listener: ln,
	}
	rs := NewRemoteService(dataDir, backend)
	installFakeTailnet(t, fake)
	if err := rs.Start("127.0.0.1:0", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rs.Stop(context.Background()) })
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.URL == "https://mcode-ts.example.ts.net"
	})

	// 节点密钥过期：配对主链接回落到 TLS 域名地址，界面能拿到授权链接。
	fake.setStatus(tailnet.Status{State: tailnet.StateLoginRequired, AuthURL: "https://login.tailscale.com/a/test"})
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.URL == "" && st.State == mcodeui.RemoteTailscaleLoginRequired && st.AuthURL != ""
	})
	pr, err := rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing: %v", err)
	}
	if strings.HasPrefix(pr.PairingURL, "https://mcode-ts.example.ts.net") {
		t.Fatalf("PairingURL = %q，期望不再指向失效入口", pr.PairingURL)
	}
	if !strings.HasPrefix(pr.PairingURL, "http://mcode.example.com:") {
		t.Fatalf("PairingURL = %q，期望回落到 TLS 域名地址", pr.PairingURL)
	}
	if pr.TailscalePairingURL != "" {
		t.Fatalf("TailscalePairingURL = %q，期望空串", pr.TailscalePairingURL)
	}

	// 重新登录成功：Tailscale 主链接恢复。
	fake.setStatus(tailnet.Status{State: tailnet.StateRunning, FQDN: "mcode-ts.example.ts.net"})
	waitForTailscale(t, rs, func(st mcodeui.RemoteTailscaleStatus) bool {
		return st.URL == "https://mcode-ts.example.ts.net"
	})
	pr, err = rs.Pairing(false)
	if err != nil {
		t.Fatalf("Pairing: %v", err)
	}
	if !strings.HasPrefix(pr.PairingURL, "https://mcode-ts.example.ts.net/#pair=") {
		t.Fatalf("PairingURL = %q，期望恢复 Tailscale 链接", pr.PairingURL)
	}
}
