package tailnet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/tsnet"
)

// TestNewStartsDisabled 验证 New 之后不联网、状态为未启用，Hostname 落默认值。
func TestNewStartsDisabled(t *testing.T) {
	m := New(Options{Dir: t.TempDir()})
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("New 后状态 = %q，期望 %q", got, StateDisabled)
	}
	if got := m.opts.Hostname; got != DefaultHostname {
		t.Fatalf("默认 Hostname = %q，期望 %q", got, DefaultHostname)
	}
	if DefaultHostname != "mcode" {
		t.Fatalf("DefaultHostname = %q，期望 mcode", DefaultHostname)
	}
	if m.opts.Dir == "" {
		t.Fatalf("Dir 未被保留")
	}
	if got := m.Status(); got.AuthURL != "" || got.FQDN != "" || got.Error != "" {
		t.Fatalf("未启用时不应有其他字段，得到 %+v", got)
	}
}

// TestNewKeepsHostname 验证显式 Hostname 不被默认值覆盖。
func TestNewKeepsHostname(t *testing.T) {
	m := New(Options{Dir: t.TempDir(), Hostname: "mcode-dev"})
	if got := m.opts.Hostname; got != "mcode-dev" {
		t.Fatalf("Hostname = %q，期望 mcode-dev", got)
	}
}

// TestStopWithoutStart 验证从未 Start 时 Stop 返回 nil 且可重复调用，状态保持未启用；
// 未启动时 Listener / WhoIs 直接报错，不去碰 tsnet。
func TestStopWithoutStart(t *testing.T) {
	m := New(Options{Dir: t.TempDir()})
	for i := 0; i < 3; i++ {
		if err := m.Stop(context.Background()); err != nil {
			t.Fatalf("第 %d 次 Stop 返回错误: %v", i+1, err)
		}
	}
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("Stop 后状态 = %q，期望 %q", got, StateDisabled)
	}
	if _, err := m.Listener(); err == nil {
		t.Fatalf("未启动时 Listener 应报错")
	}
	if _, err := m.WhoIs(context.Background(), "100.64.0.1:1234"); err == nil {
		t.Fatalf("未启动时 WhoIs 应报错")
	}
}

// TestDoneClosedBeforeStart 验证从未 Start 时 Done 已关闭，新节点不会白等。
func TestDoneClosedBeforeStart(t *testing.T) {
	m := New(Options{Dir: t.TempDir()})
	select {
	case <-m.Done():
	default:
		t.Fatalf("从未 Start 时 Done 应已关闭")
	}
}

// TestFinishClosesOnceAndSignalsDone 验证 finish 调两次也只关一次服务器，Done 通道只关一次。
func TestFinishClosesOnceAndSignalsDone(t *testing.T) {
	prev := closeServer
	closes := 0
	closeServer = func(*tsnet.Server) error {
		closes++
		return nil
	}
	t.Cleanup(func() { closeServer = prev })

	server := &tsnet.Server{}
	once := new(sync.Once)
	closed := make(chan struct{})
	m := &Manager{
		server:    server,
		cancel:    func() {},
		done:      make(chan struct{}),
		closed:    closed,
		closeOnce: once,
		started:   true,
	}
	if err := m.finish(server, once, closed); err != nil {
		t.Fatalf("第一次 finish 返回错误: %v", err)
	}
	select {
	case <-closed:
	default:
		t.Fatalf("finish 后 Done 通道应关闭")
	}

	// Stop 超时后再次 Stop 会再调一次 finish：不能 panic，也不能再关一次。
	if err := m.finish(server, once, closed); err != nil {
		t.Fatalf("第二次 finish 返回错误: %v", err)
	}
	if closes != 1 {
		t.Fatalf("Close 调用次数 = %d，期望 1", closes)
	}
	if m.started || m.server != nil || m.closeOnce != nil {
		t.Fatalf("finish 后应复位：started=%v server=%v closeOnce=%v", m.started, m.server, m.closeOnce)
	}
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("finish 后状态 = %q，期望 %q", got, StateDisabled)
	}
}

// TestRunningStatusUpdate 验证运行中同步的映射：等登录带授权地址、正常运行用去尾点的域名、其余状态不动。
func TestRunningStatusUpdate(t *testing.T) {
	const authURL = "https://login.tailscale.com/a/example"
	cases := []struct {
		name         string
		backendState string
		authURL      string
		dnsName      string
		want         Status
		wantOK       bool
	}{
		{
			name:         "密钥过期需要重新登录",
			backendState: backendStateNeedsLogin,
			authURL:      authURL,
			want:         Status{State: StateLoginRequired, AuthURL: authURL},
			wantOK:       true,
		},
		{
			name:         "正常运行取域名并去掉尾点",
			backendState: backendStateRunning,
			dnsName:      "mcode-renamed.example.ts.net.",
			want:         Status{State: StateRunning, FQDN: "mcode-renamed.example.ts.net"},
			wantOK:       true,
		},
		{
			name:         "启动中不改状态",
			backendState: "Starting",
			dnsName:      "mcode.example.ts.net.",
			wantOK:       false,
		},
		{
			name:         "运行中但缺域名不改状态",
			backendState: backendStateRunning,
			wantOK:       false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := runningStatusUpdate(tc.backendState, tc.authURL, tc.dnsName)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，期望 %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("状态 = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

// TestStartRequiresDir 验证 Dir 为空时 Start 报错且状态不变。
func TestStartRequiresDir(t *testing.T) {
	m := New(Options{})
	if err := m.Start(context.Background()); err == nil {
		t.Fatalf("Dir 为空时 Start 应报错")
	}
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("Start 失败后状态 = %q，期望 %q", got, StateDisabled)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Start 失败后 Stop 应返回 nil，得到 %v", err)
	}
}

// TestStatusConcurrentAccess 验证 Status 与内部状态写入并发安全（配合 go test -race）。
func TestStatusConcurrentAccess(t *testing.T) {
	m := New(Options{Dir: t.TempDir()})
	const (
		workers = 8
		rounds  = 200
	)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				st := Status{State: StateRunning, FQDN: "mcode.example.ts.net"}
				if (i+r)%2 == 0 {
					st = Status{State: StateLoginRequired, AuthURL: "https://login.tailscale.com/a/example"}
				}
				m.setStatus(st)
			}
		}(i)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				st := m.Status()
				if st.State != StateDisabled && st.State != StateRunning && st.State != StateLoginRequired {
					t.Errorf("读到意外状态 %q", st.State)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := m.Status().State; got != StateRunning && got != StateLoginRequired {
		t.Fatalf("并发写完后状态 = %q，期望 running 或 login_required", got)
	}
}

// TestNeedsHTTPS 验证只按 tsnet 的两条真实文案判断「没开 MagicDNS / HTTPS」，
// 其他带 https / certificate 字样的错误不能误判。
func TestNeedsHTTPS(t *testing.T) {
	const (
		magicDNS = "tsnet: you must enable MagicDNS in the DNS page of the admin panel to proceed. See https://tailscale.com/s/https"
		https    = "tsnet: you must enable HTTPS in the admin panel to proceed. See https://tailscale.com/s/https"
	)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"MagicDNS 未开", errors.New(magicDNS), true},
		{"HTTPS 未开", errors.New(https), true},
		{"超时", context.DeadlineExceeded, false},
		{"连接被拒", errors.New("Get https://x: connection refused"), false},
		{"证书不被信任", errors.New("certificate signed by unknown authority"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsHTTPS(tc.err); got != tc.want {
				t.Fatalf("needsHTTPS(%q) = %v，期望 %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRedactAuthURL 验证登录链接（等同于登录令牌）不会进入日志。
func TestRedactAuthURL(t *testing.T) {
	const token = "https://login.tailscale.com/a/abcd1234"
	in := "To start this tsnet server, restart with TS_AUTHKEY set, or go to: " + token
	got := redactAuthURL(in)
	if strings.Contains(got, "abcd1234") {
		t.Fatalf("脱敏后仍然包含令牌：%q", got)
	}
	if !strings.Contains(got, redactedAuthURL) {
		t.Fatalf("脱敏后应包含占位符 %q，得到 %q", redactedAuthURL, got)
	}
	if !strings.HasPrefix(got, "To start this tsnet server, restart with TS_AUTHKEY set, or go to: ") {
		t.Fatalf("链接以外的文字应保留：%q", got)
	}

	plain := "AuthLoop: state is Running; done"
	if got := redactAuthURL(plain); got != plain {
		t.Fatalf("无链接时不应改动，得到 %q", got)
	}
}

// TestPrepareDirTightensExistingPermissions 验证已存在的 0755 状态目录会被收紧到 0700。
func TestPrepareDirTightensExistingPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tailnet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("设置权限失败: %v", err)
	}

	if err := prepareDir(dir); err != nil {
		t.Fatalf("prepareDir 失败: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat 目录失败: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o700 {
		t.Fatalf("目录权限 = %o，期望 700", got)
	}
}

// TestTailnetEndToEnd 会连真实控制面，只有设置 MCODE_TAILNET_E2E 时才跑。
func TestTailnetEndToEnd(t *testing.T) {
	if os.Getenv("MCODE_TAILNET_E2E") == "" {
		t.Skip("未设置 MCODE_TAILNET_E2E，跳过真实 tailnet 用例")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	m := New(Options{Dir: t.TempDir()})
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	deadline := time.Now().Add(2 * time.Minute)
	for {
		st := m.Status()
		if st.State == StateRunning {
			if st.FQDN == "" {
				t.Fatalf("运行中但 FQDN 为空")
			}
			break
		}
		if st.State == StateNeedsHTTPS {
			t.Fatalf("tailnet 未开启 MagicDNS 或 HTTPS Certificates: %s", st.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待运行中超时，最后状态 %+v", st)
		}
		time.Sleep(time.Second)
	}

	ln, err := m.Listener()
	if err != nil {
		t.Fatalf("Listener 失败: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("关闭 listener 失败: %v", err)
	}

	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("Stop 后状态 = %q，期望 %q", got, StateDisabled)
	}
	// Stop 返回后 Done 必须已关闭，下一次启动才能接着用同一个状态目录。
	select {
	case <-m.Done():
	default:
		t.Fatalf("Stop 后 Done 应已关闭")
	}
}
