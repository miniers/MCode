package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// 1. 无 token 时生成自签证书并能用于 TLS 握手。
func TestTLSManager_SelfSignedAndHandshake(t *testing.T) {
	dir := t.TempDir()

	mgr, err := NewTLSManager(TLSOptions{
		Dir:   dir,
		Hosts: []string{"127.0.0.1", "localhost"},
	})
	if err != nil {
		t.Fatalf("NewTLSManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Close()

	// 验证自签文件生成及权限
	selfCertFile := filepath.Join(dir, "selfsigned-cert.pem")
	selfKeyFile := filepath.Join(dir, "selfsigned-key.pem")

	fiKey, err := os.Stat(selfKeyFile)
	if err != nil {
		t.Fatalf("stat selfsigned key file: %v", err)
	}
	if fiKey.Mode().Perm() != 0o600 {
		t.Errorf("expected key file mode 0600, got %v", fiKey.Mode().Perm())
	}

	fiCert, err := os.Stat(selfCertFile)
	if err != nil {
		t.Fatalf("stat selfsigned cert file: %v", err)
	}
	if fiCert.Mode().Perm() != 0o644 {
		t.Errorf("expected cert file mode 0644, got %v", fiCert.Mode().Perm())
	}

	// 启动 TLS HTTP 服务器验证握手
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	tlsLn := tls.NewListener(ln, mgr.TLSConfig())
	defer tlsLn.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls ok"))
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(tlsLn)
	}()
	defer func() {
		_ = server.Close()
	}()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 3 * time.Second,
	}

	u := fmt.Sprintf("https://%s/test", ln.Addr().String())
	resp, err := client.Get(u)
	if err != nil {
		t.Fatalf("GET over TLS failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(body) != "tls ok" {
		t.Errorf("expected 'tls ok', got %q", string(body))
	}
}

// 2. 重启后复用磁盘自签证书。
func TestTLSManager_ReuseOnRestart(t *testing.T) {
	dir := t.TempDir()

	mgr1, err := NewTLSManager(TLSOptions{
		Dir:   dir,
		Hosts: []string{"localhost"},
	})
	if err != nil {
		t.Fatalf("mgr1 NewTLSManager failed: %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := mgr1.Start(ctx1); err != nil {
		cancel1()
		t.Fatalf("mgr1 Start failed: %v", err)
	}

	cert1, err := mgr1.GetCertificate(nil)
	if err != nil {
		cancel1()
		mgr1.Close()
		t.Fatalf("mgr1 GetCertificate failed: %v", err)
	}
	rawCert1 := cert1.Certificate[0]

	cancel1()
	mgr1.Close()

	// 重启：创建新实例指向同一个 Dir
	mgr2, err := NewTLSManager(TLSOptions{
		Dir:   dir,
		Hosts: []string{"localhost"},
	})
	if err != nil {
		t.Fatalf("mgr2 NewTLSManager failed: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	if err := mgr2.Start(ctx2); err != nil {
		t.Fatalf("mgr2 Start failed: %v", err)
	}
	defer mgr2.Close()

	cert2, err := mgr2.GetCertificate(nil)
	if err != nil {
		t.Fatalf("mgr2 GetCertificate failed: %v", err)
	}
	rawCert2 := cert2.Certificate[0]

	if string(rawCert1) != string(rawCert2) {
		t.Errorf("expected reused certificate on restart, but certificates differ")
	}
}

// 3. 证书临近到期时触发续期（通过 Now 注入）。
func TestTLSManager_RenewalNearExpiration(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	currTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return currTime
	}

	renewBefore := 30 * 24 * time.Hour

	mgr, err := NewTLSManager(TLSOptions{
		Dir:         dir,
		Hosts:       []string{"localhost"},
		RenewBefore: renewBefore,
		Now:         nowFunc,
	})
	if err != nil {
		t.Fatalf("NewTLSManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Close()

	initialCert, err := mgr.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}
	initialLeaf, err := x509.ParseCertificate(initialCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse initial leaf cert: %v", err)
	}

	// 推进时间到距离到期仅剩 10 天 (< 30 天 RenewBefore)
	mu.Lock()
	currTime = initialLeaf.NotAfter.Add(-10 * 24 * time.Hour)
	mu.Unlock()

	// 触发检查续期
	mgr.checkAndRenew(ctx)

	renewedCert, err := mgr.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate after renew failed: %v", err)
	}
	renewedLeaf, err := x509.ParseCertificate(renewedCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse renewed leaf cert: %v", err)
	}

	if !renewedLeaf.NotAfter.After(initialLeaf.NotAfter) {
		t.Errorf("expected renewed cert NotAfter (%v) to be later than initial (%v)", renewedLeaf.NotAfter, initialLeaf.NotAfter)
	}
}

// 4. Server.ServeTLS 用例：ServeTLS 后用 tls 客户端（InsecureSkipVerify）GET / 能拿到 HTTP 响应。
func TestServer_ServeTLS(t *testing.T) {
	dir := t.TempDir()

	mgr, err := NewTLSManager(TLSOptions{
		Dir:   dir,
		Hosts: []string{"127.0.0.1", "localhost"},
	})
	if err != nil {
		t.Fatalf("NewTLSManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start TLSManager failed: %v", err)
	}
	defer mgr.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	testFS := fstest.MapFS{
		"remote-index.html": &fstest.MapFile{
			Data: []byte("<!DOCTYPE html><html><body><h1>mcode remote</h1></body></html>"),
		},
	}

	srv := NewServer(ServerOptions{
		Assets:       testFS,
		ListenAddr:   ln.Addr().String(),
		Entry:        EntryTLS,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})

	go func() {
		_ = srv.ServeTLS(ln, mgr.TLSConfig())
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer func() {
		shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 3 * time.Second,
	}

	u := fmt.Sprintf("https://%s/", ln.Addr().String())
	resp, err := client.Get(u)
	if err != nil {
		t.Fatalf("GET / over TLS failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if !stringContains(string(body), "mcode remote") {
		t.Errorf("expected response to contain 'mcode remote', got: %s", string(body))
	}
}

func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// 5. ACME 续期失败时保留未过期的现有证书，仅在其真正过期后才降级为自签证书。
func TestTLSManager_KeepValidCertWhenACMERenewalFails(t *testing.T) {
	dir := t.TempDir()
	baseTime := time.Now()

	var mu sync.Mutex
	currTime := baseTime
	nowFunc := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return currTime
	}

	// 生成 CA 与由 CA 签发的叶子证书，模拟此前已通过 ACME 签发的证书
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             baseTime.Add(-1 * time.Hour),
		NotAfter:              baseTime.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafNotAfter := baseTime.Add(10 * 24 * time.Hour)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "remote.example.com"},
		DNSNames:     []string{"remote.example.com"},
		NotBefore:    baseTime.Add(-1 * time.Hour),
		NotAfter:     leafNotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o644); err != nil {
		t.Fatalf("write cert.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatalf("write key.pem: %v", err)
	}

	mgr, err := NewTLSManager(TLSOptions{
		Dir:             dir,
		Domain:          "remote.example.com",
		CloudflareToken: "dummy-token",
		// 指向不可达的目录 URL，使 ACME 续期必然失败
		DirectoryURL: "http://127.0.0.1:1/directory",
		HTTPClient:   &http.Client{Timeout: 2 * time.Second},
		Now:          nowFunc,
		// 本用例只关心证书续期，注入空 IP 列表让 IP 监视不发起真实 Cloudflare 请求
		LANIPs: func() []string { return nil },
	})
	if err != nil {
		t.Fatalf("NewTLSManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Close()

	before, err := mgr.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	// 距到期 10 天，已进入续期窗口；ACME 续期失败后仍应继续使用现有证书
	mgr.checkAndRenew(ctx)

	after, err := mgr.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate after failed renewal failed: %v", err)
	}
	if !bytes.Equal(before.Certificate[0], after.Certificate[0]) {
		t.Fatalf("expected still-valid certificate to be kept when ACME renewal fails")
	}

	// 证书过期后再检查：应降级为自签证书
	mu.Lock()
	currTime = leafNotAfter.Add(time.Hour)
	mu.Unlock()

	mgr.checkAndRenew(ctx)

	replaced, err := mgr.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate after expiry failed: %v", err)
	}
	if bytes.Equal(before.Certificate[0], replaced.Certificate[0]) {
		t.Fatalf("expected expired certificate to be replaced")
	}
	replacedLeaf, err := x509.ParseCertificate(replaced.Certificate[0])
	if err != nil {
		t.Fatalf("parse replaced leaf: %v", err)
	}
	if replacedLeaf.Issuer.String() != replacedLeaf.Subject.String() {
		t.Errorf("expected fallback self-signed certificate, got issuer %q", replacedLeaf.Issuer)
	}
	if _, err := os.Stat(filepath.Join(dir, "selfsigned-cert.pem")); err != nil {
		t.Errorf("expected selfsigned-cert.pem to be written: %v", err)
	}
}

// fakeCloudflareA 模拟 Cloudflare A 记录接口：记录 PATCH 次数与内容，可注入失败。
type fakeCloudflareA struct {
	mu       sync.Mutex
	content  string
	patches  int
	lastIP   string
	failures int // 剩余需要返回 success=false 的次数
}

func (f *fakeCloudflareA) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"errors":  []any{},
			"result":  []map[string]any{{"id": "zone-1", "name": "example.com"}},
		})
	})
	mux.HandleFunc("/zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		content := f.content
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"errors":  []any{},
			"result": []map[string]any{{
				"id":      "rec-a",
				"type":    "A",
				"name":    "remote.example.com",
				"content": content,
			}},
		})
	})
	mux.HandleFunc("/zones/zone-1/dns_records/rec-a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var payload struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failures > 0 {
			f.failures--
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []map[string]any{{"code": 9000, "message": "injected failure"}},
				"result":  []any{},
			})
			return
		}
		f.patches++
		f.lastIP = payload.Content
		f.content = payload.Content
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"errors":  []any{},
			"result":  map[string]any{"id": "rec-a", "type": "A", "name": "remote.example.com", "content": payload.Content},
		})
	})
	return mux
}

// snapshot 返回 PATCH 次数与最近一次写入的 IP。
func (f *fakeCloudflareA) snapshot() (int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.patches, f.lastIP
}

// 6. IP 变更监视：启动即同步一次，IP 未变不重复请求，IP 变化后更新，失败后下个 tick 重试。
func TestTLSManager_WatchIPSyncsCloudflareARecord(t *testing.T) {
	const domain = "remote.example.com"

	var mu sync.Mutex
	ip := "192.168.1.20"
	ipsFn := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return []string{ip}
	}
	setIP := func(v string) {
		mu.Lock()
		ip = v
		mu.Unlock()
	}

	fake := &fakeCloudflareA{content: "192.168.1.5"}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	mgr, err := NewTLSManager(TLSOptions{
		Dir:               t.TempDir(),
		Domain:            domain,
		CloudflareToken:   "dummy-token",
		CloudflareAPIBase: srv.URL,
		HTTPClient:        srv.Client(),
		IPWatchInterval:   20 * time.Millisecond,
		LANIPs:            ipsFn,
	})
	if err != nil {
		t.Fatalf("NewTLSManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 直接启动 watcher，避免 Start 依赖真实 ACME 签发；
	// 用例内出现 t.Fatalf 时由 ctx 取消保证 goroutine 退出。
	mgr.wg.Add(1)
	go func() {
		defer mgr.wg.Done()
		mgr.watchIP(ctx)
	}()

	// (a) 启动后立即把记录更新为注入 IP
	if n := waitPatchCount(fake, 1, 2*time.Second); n != 1 {
		t.Fatalf("expected exactly 1 patch after start, got %d", n)
	}
	if _, got := fake.snapshot(); got != "192.168.1.20" {
		t.Fatalf("expected patch content 192.168.1.20, got %q", got)
	}

	// (b) IP 未变化时多个 tick 内不再产生新的 PATCH
	time.Sleep(150 * time.Millisecond)
	if n, _ := fake.snapshot(); n != 1 {
		t.Fatalf("expected no extra patch when ip unchanged, got %d", n)
	}

	// (c) IP 变化后写入新地址
	setIP("192.168.1.30")
	if n := waitPatchCount(fake, 2, 2*time.Second); n != 2 {
		t.Fatalf("expected 2 patches after ip change, got %d", n)
	}
	if got := lastSyncedIPOf(mgr); got != "192.168.1.30" {
		t.Fatalf("expected lastSyncedIP 192.168.1.30, got %q", got)
	}

	// (d) 注入一次失败：同步失败不更新 lastSyncedIP，下个 tick 重试成功
	fake.mu.Lock()
	fake.failures = 1
	fake.mu.Unlock()

	setIP("192.168.1.40")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := lastSyncedIPOf(mgr); got == "192.168.1.40" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := lastSyncedIPOf(mgr); got != "192.168.1.40" {
		t.Fatalf("expected retry to sync 192.168.1.40, got %q", got)
	}
	if n, lastIP := fake.snapshot(); n != 3 || lastIP != "192.168.1.40" {
		t.Fatalf("expected 3 successful patches ending at 192.168.1.40, got %d/%q", n, lastIP)
	}

	// (e) Close 后 goroutine 退出，不阻塞
	done := make(chan struct{})
	go func() {
		mgr.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Close did not return in time")
	}
}

// waitPatchCount 等待 PATCH 次数达到 want，返回最终次数；超时返回当前次数。
func waitPatchCount(f *fakeCloudflareA, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		n, _ := f.snapshot()
		if n >= want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lastSyncedIPOf 在持锁状态下读取 manager 最近一次成功同步的 IP。
func lastSyncedIPOf(m *TLSManager) string {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	return m.lastSyncedIP
}
