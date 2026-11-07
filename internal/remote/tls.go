package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
)

// TLSOptions 配置 TLS 证书管理项。
type TLSOptions struct {
	Dir               string
	Hosts             []string
	Domain            string
	Email             string
	DirectoryURL      string
	CloudflareToken   string
	CloudflareAPIBase string
	Now               func() time.Time
	HTTPClient        *http.Client
	RenewBefore       time.Duration
	// IPWatchInterval 是局域网 IP 变更监视的轮询间隔，零值表示默认 30 秒。
	IPWatchInterval time.Duration
	// LANIPs 返回候选局域网 IPv4 地址列表（取首个），零值表示使用 LANIPv4s。
	LANIPs func() []string
}

// TLSCertificateInfo 描述当前生效证书的摘要信息。
type TLSCertificateInfo struct {
	Domains  []string
	Issuer   string
	NotAfter time.Time
	Source   string
}

// TLSManager 负责管理 HTTPS 证书的签发、持久化、自动续期与热替换。
type TLSManager struct {
	opts        TLSOptions
	currentCert atomic.Pointer[tls.Certificate]
	now         func() time.Time

	// acmeActive 表示当前证书来自 ACME 签发（false 表示自签证书）。
	// ACME 证书续期失败时若尚未过期则继续沿用，避免提前降级为自签证书。
	acmeActive atomic.Bool

	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	// syncMu 保护 A 记录同步状态：watcher goroutine 与续期流程可能并发调用 syncARecord。
	syncMu       sync.Mutex
	lastSyncedIP string // 最近一次成功写入 Cloudflare 的 IP，用于跳过无变化的同步
	lastSyncErr  string // 最近一次失败的错误文本，连续相同错误只记录一次日志
}

// NewTLSManager 创建 TLSManager 实例并初始化配置默认值。
func NewTLSManager(opts TLSOptions) (*TLSManager, error) {
	if opts.Dir == "" {
		return nil, errors.New("tls dir cannot be empty")
	}

	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create tls dir: %w", err)
	}

	if opts.CloudflareAPIBase == "" {
		opts.CloudflareAPIBase = "https://api.cloudflare.com/client/v4"
	}
	if opts.DirectoryURL == "" {
		opts.DirectoryURL = acme.LetsEncryptURL
	}
	if opts.RenewBefore <= 0 {
		opts.RenewBefore = 30 * 24 * time.Hour
	}
	if opts.IPWatchInterval <= 0 {
		opts.IPWatchInterval = 30 * time.Second
	}
	if opts.LANIPs == nil {
		opts.LANIPs = LANIPv4s
	}
	nowFunc := opts.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}

	return &TLSManager{
		opts:   opts,
		now:    nowFunc,
		stopCh: make(chan struct{}),
	}, nil
}

// GetCertificate 从原子指针中返回当前活跃的证书。
func (m *TLSManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := m.currentCert.Load()
	if cert == nil {
		return nil, errors.New("remote: no certificate available")
	}
	return cert, nil
}

// TLSConfig 返回基于当前证书管理器的 tls.Config 配置。
func (m *TLSManager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: m.GetCertificate,
	}
}

const tlsCertSourceFile = "cert-source.txt"

// CertificateInfo 返回当前生效证书的域名、颁发者、到期时间与来源。
func (m *TLSManager) CertificateInfo() (TLSCertificateInfo, error) {
	source := m.readCertSource()

	var leaf *x509.Certificate
	if cert := m.currentCert.Load(); cert != nil {
		leaf = cert.Leaf
	}
	if leaf == nil {
		_, parsed, valid := m.loadCertPair(
			filepath.Join(m.opts.Dir, "cert.pem"),
			filepath.Join(m.opts.Dir, "key.pem"),
		)
		if valid {
			leaf = parsed
			if source == "" {
				source = "acme"
			}
		}
	}
	if leaf == nil {
		return TLSCertificateInfo{}, errors.New("remote: no certificate available")
	}

	if source == "" {
		if _, err := os.Stat(filepath.Join(m.opts.Dir, "cert.pem")); err == nil {
			source = "acme"
		} else {
			source = "self-signed"
		}
	}

	domains := append([]string(nil), leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		domains = append(domains, ip.String())
	}

	issuer := leaf.Issuer.CommonName
	if issuer == "" {
		issuer = leaf.Issuer.String()
	}

	return TLSCertificateInfo{
		Domains:  domains,
		Issuer:   issuer,
		NotAfter: leaf.NotAfter,
		Source:   source,
	}, nil
}

// ErrDNSSyncFailed 表示证书已签发并落盘，但 Cloudflare 域名的 A 记录同步失败。
// 调用方应把这种情况当作「证书可用、仅解析待修复」处理：仍然热加载新证书，
// 再把错误交给设置界面提示用户。
var ErrDNSSyncFailed = errors.New("remote: dns a-record sync failed")

// IssueCertificate 立即通过 ACME 签发证书并热加载，成功后同步 Cloudflare 域名的 A 记录。
func (m *TLSManager) IssueCertificate(ctx context.Context) error {
	if m.opts.Domain == "" {
		return errors.New("remote: tls domain is not configured")
	}
	if m.opts.CloudflareToken == "" {
		return errors.New("remote: cloudflare api token is not configured")
	}

	cfDNS := newCloudflareDNS(m.opts.CloudflareAPIBase, m.opts.CloudflareToken, m.opts.HTTPClient, 10*time.Second)
	certPEM, keyPEM, err := issueCertificate(ctx, m.opts, cfDNS)
	if err != nil {
		return err
	}

	certFile := filepath.Join(m.opts.Dir, "cert.pem")
	keyFile := filepath.Join(m.opts.Dir, "key.pem")
	if err := writeAtomic(certFile, certPEM, 0o644); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	if err := writeAtomic(keyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write certificate key: %w", err)
	}
	_ = m.writeCertSource("acme")

	cert, _, valid := m.loadCertPair(certFile, keyFile)
	if !valid {
		return errors.New("remote: issued certificate is not valid")
	}
	m.currentCert.Store(cert)
	m.acmeActive.Store(true)

	// 证书已安装成功，但 A 记录未同步时仍要把错误回传给设置界面，
	// 避免用户误以为域名解析已经指向本机；错误同时带上 ErrDNSSyncFailed，
	// 供调用方区分「证书已落盘」与真正的签发失败。
	if err := m.syncARecord(ctx); err != nil {
		return fmt.Errorf("remote: 证书已签发，但域名解析未更新: %w: %w", ErrDNSSyncFailed, err)
	}
	return nil
}

// ImportCertificate 校验并导入外部证书链与私钥，覆盖数据目录中的证书并立即热加载。
func (m *TLSManager) ImportCertificate(chainPath, keyPath string) error {
	chainPEM, err := os.ReadFile(chainPath)
	if err != nil {
		return fmt.Errorf("read certificate chain: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read certificate key: %w", err)
	}
	if _, err := tls.X509KeyPair(chainPEM, keyPEM); err != nil {
		return fmt.Errorf("remote: invalid certificate pair: %w", err)
	}

	certFile := filepath.Join(m.opts.Dir, "cert.pem")
	keyFile := filepath.Join(m.opts.Dir, "key.pem")
	if err := writeAtomic(certFile, chainPEM, 0o644); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	if err := writeAtomic(keyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write certificate key: %w", err)
	}
	if err := m.writeCertSource("imported"); err != nil {
		return fmt.Errorf("write certificate source: %w", err)
	}

	cert, _, valid := m.loadCertPair(certFile, keyFile)
	if !valid {
		return errors.New("remote: imported certificate is not currently valid")
	}
	m.currentCert.Store(cert)
	m.acmeActive.Store(true)
	return nil
}

// RemoveCertificate 删除数据目录中已配置的证书及来源标记，并清空当前证书。
func (m *TLSManager) RemoveCertificate() error {
	var firstErr error
	for _, name := range []string{"cert.pem", "key.pem", tlsCertSourceFile} {
		if err := os.Remove(filepath.Join(m.opts.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	m.currentCert.Store(nil)
	m.acmeActive.Store(false)
	return firstErr
}

// Reload 从磁盘重新加载已配置的证书到当前证书。
func (m *TLSManager) Reload() error {
	cert, _, valid := m.loadCertPair(
		filepath.Join(m.opts.Dir, "cert.pem"),
		filepath.Join(m.opts.Dir, "key.pem"),
	)
	if !valid {
		return errors.New("remote: no valid certificate to load")
	}
	m.currentCert.Store(cert)
	m.acmeActive.Store(true)
	return nil
}

func (m *TLSManager) readCertSource() string {
	data, err := os.ReadFile(filepath.Join(m.opts.Dir, tlsCertSourceFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (m *TLSManager) writeCertSource(source string) error {
	return writeAtomic(filepath.Join(m.opts.Dir, tlsCertSourceFile), []byte(source), 0o644)
}

// syncARecord 在有 Cloudflare Token 时把域名 A 记录更新为当前局域网 IPv4。
// 返回错误表示未能同步（例如域名已指向公网地址而被拒绝覆盖），调用方需将错误暴露给设置界面。
// 与最近一次成功同步的 IP 相同则直接返回，避免 IP 未变化时反复请求 Cloudflare；
// 失败时不更新已同步 IP，下一个 tick（或下次调用）会自然重试。
func (m *TLSManager) syncARecord(ctx context.Context) error {
	if m.opts.Domain == "" || m.opts.CloudflareToken == "" {
		return nil
	}

	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	ips := m.opts.LANIPs()
	if len(ips) == 0 {
		return nil
	}
	ip := ips[0]
	if ip == m.lastSyncedIP {
		return nil
	}

	cfDNS := newCloudflareDNS(m.opts.CloudflareAPIBase, m.opts.CloudflareToken, m.opts.HTTPClient, 0)
	if err := cfDNS.upsertARecord(ctx, m.opts.Domain, ip); err != nil {
		// 连续遇到同一个错误时只在首次记录日志，避免每个 tick 刷屏。
		if msg := err.Error(); msg != m.lastSyncErr {
			log.Printf("remote: update cloudflare A record failed: %v", err)
			m.lastSyncErr = msg
		}
		return err
	}

	m.lastSyncedIP = ip
	m.lastSyncErr = ""
	return nil
}

// watchIP 启动时同步一次域名 A 记录，之后按 IPWatchInterval 轮询局域网 IP，
// 发现变化时自动更新 Cloudflare A 记录，让域名始终指向当前局域网地址。
func (m *TLSManager) watchIP(ctx context.Context) {
	_ = m.syncARecord(ctx)

	ticker := time.NewTicker(m.opts.IPWatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			_ = m.syncARecord(ctx)
		}
	}
}

// Start 启动证书加载与后台定期续期 goroutine。
func (m *TLSManager) Start(ctx context.Context) error {
	if err := m.initialLoad(ctx); err != nil {
		return err
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(12 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.checkAndRenew(ctx)
			}
		}
	}()

	// 配置了域名与 Cloudflare Token 时启动 IP 变更监视：
	// 已有有效证书的启动路径不会经过 ACME 签发，需要独立 goroutine 保证 A 记录同步。
	if m.opts.Domain != "" && m.opts.CloudflareToken != "" {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.watchIP(ctx)
		}()
	}

	return nil
}

// Close 关闭后台轮询 goroutine。
func (m *TLSManager) Close() {
	m.closeOnce.Do(func() {
		close(m.stopCh)
		m.wg.Wait()
	})
}

// initialLoad 启动时加载证书：优先已有证书，过期或无证书时走 ACME 签发，失败则回退至自签证书。
func (m *TLSManager) initialLoad(ctx context.Context) error {
	certFile := filepath.Join(m.opts.Dir, "cert.pem")
	keyFile := filepath.Join(m.opts.Dir, "key.pem")

	// 1. 尝试加载并验证已有 ACME 证书
	if cert, _, valid := m.loadCertPair(certFile, keyFile); valid {
		m.currentCert.Store(cert)
		m.acmeActive.Store(true)
		return nil
	}

	// 2. 无证书或证书过期时，若满足 ACME 条件尝试签发
	if m.opts.Domain != "" && m.opts.CloudflareToken != "" {
		cfDNS := newCloudflareDNS(m.opts.CloudflareAPIBase, m.opts.CloudflareToken, m.opts.HTTPClient, 10*time.Second)
		certPEM, keyPEM, err := issueCertificate(ctx, m.opts, cfDNS)
		if err == nil {
			if err := writeAtomic(certFile, certPEM, 0o644); err == nil {
				if err := writeAtomic(keyFile, keyPEM, 0o600); err == nil {
					if cert, _, valid := m.loadCertPair(certFile, keyFile); valid {
						m.currentCert.Store(cert)
						m.acmeActive.Store(true)
						_ = m.writeCertSource("acme")
						_ = m.syncARecord(ctx)
						return nil
					}
				}
			}
		}
		log.Printf("remote: ACME certificate issuance failed, fallback to self-signed certificate: %v", err)
	}

	// 3. 回退到自签证书
	return m.ensureSelfSignedCertificate()
}

// ensureSelfSignedCertificate 加载或生成自签证书。
func (m *TLSManager) ensureSelfSignedCertificate() error {
	selfCertFile := filepath.Join(m.opts.Dir, "selfsigned-cert.pem")
	selfKeyFile := filepath.Join(m.opts.Dir, "selfsigned-key.pem")

	if cert, _, valid := m.loadCertPair(selfCertFile, selfKeyFile); valid {
		m.currentCert.Store(cert)
		m.acmeActive.Store(false)
		return nil
	}

	certPEM, keyPEM, err := m.generateSelfSignedCert()
	if err != nil {
		return fmt.Errorf("generate self-signed cert: %w", err)
	}

	if err := writeAtomic(selfCertFile, certPEM, 0o644); err != nil {
		return fmt.Errorf("write self-signed cert: %w", err)
	}
	if err := writeAtomic(selfKeyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write self-signed key: %w", err)
	}

	cert, _, valid := m.loadCertPair(selfCertFile, selfKeyFile)
	if !valid {
		return errors.New("failed to load newly generated self-signed certificate")
	}

	m.currentCert.Store(cert)
	m.acmeActive.Store(false)
	return nil
}

// checkAndRenew 检查证书是否即将过期，并在到期前少于 RenewBefore 时续期。
func (m *TLSManager) checkAndRenew(ctx context.Context) {
	current := m.currentCert.Load()
	if current == nil || len(current.Certificate) == 0 {
		return
	}

	leaf := current.Leaf
	if leaf == nil {
		var err error
		leaf, err = x509.ParseCertificate(current.Certificate[0])
		if err != nil {
			return
		}
	}

	now := m.now()
	if leaf.NotAfter.Sub(now) >= m.opts.RenewBefore {
		return
	}

	// 需重新签发
	if m.opts.Domain != "" && m.opts.CloudflareToken != "" {
		cfDNS := newCloudflareDNS(m.opts.CloudflareAPIBase, m.opts.CloudflareToken, m.opts.HTTPClient, 10*time.Second)
		certPEM, keyPEM, err := issueCertificate(ctx, m.opts, cfDNS)
		if err == nil {
			certFile := filepath.Join(m.opts.Dir, "cert.pem")
			keyFile := filepath.Join(m.opts.Dir, "key.pem")
			if err := writeAtomic(certFile, certPEM, 0o644); err == nil {
				if err := writeAtomic(keyFile, keyPEM, 0o600); err == nil {
					if cert, _, valid := m.loadCertPair(certFile, keyFile); valid {
						m.currentCert.Store(cert)
						m.acmeActive.Store(true)
						_ = m.writeCertSource("acme")
						_ = m.syncARecord(ctx)
						return
					}
				}
			}
		}
		log.Printf("remote: ACME certificate renewal failed: %v", err)

		// 若当前仍持有未过期的 ACME 证书，继续使用现有证书并等待下一轮重试，
		// 避免提前降级为自签证书导致客户端信任校验失败。
		if m.acmeActive.Load() && now.Before(leaf.NotAfter) {
			return
		}
	}

	// 自签证书续期
	certPEM, keyPEM, err := m.generateSelfSignedCert()
	if err == nil {
		selfCertFile := filepath.Join(m.opts.Dir, "selfsigned-cert.pem")
		selfKeyFile := filepath.Join(m.opts.Dir, "selfsigned-key.pem")
		if err := writeAtomic(selfCertFile, certPEM, 0o644); err == nil {
			if err := writeAtomic(selfKeyFile, keyPEM, 0o600); err == nil {
				if cert, _, valid := m.loadCertPair(selfCertFile, selfKeyFile); valid {
					m.currentCert.Store(cert)
					m.acmeActive.Store(false)
				}
			}
		}
	}
}

// loadCertPair 从磁盘加载证书及私钥，并判断是否仍在有效期内。
func (m *TLSManager) loadCertPair(certPath, keyPath string) (*tls.Certificate, *x509.Certificate, bool) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, false
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, false
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(tlsCert.Certificate) == 0 {
		return nil, nil, false
	}

	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, nil, false
	}
	tlsCert.Leaf = leaf

	now := m.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, leaf, false
	}

	return &tlsCert, leaf, true
}

// generateSelfSignedCert 使用 ECDSA P-256 生成自签证书，有效期 1 年。
func (m *TLSManager) generateSelfSignedCert() ([]byte, []byte, error) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial number: %w", err)
	}

	now := m.now()
	notBefore := now.Add(-1 * time.Hour)
	notAfter := now.Add(365 * 24 * time.Hour)

	commonName := "localhost"
	if m.opts.Domain != "" {
		commonName = m.opts.Domain
	} else if len(m.opts.Hosts) > 0 {
		commonName = m.opts.Hosts[0]
	}

	dnsSet := make(map[string]struct{})
	ipSet := make(map[string]net.IP)

	// 基础回环
	dnsSet["localhost"] = struct{}{}
	ipSet["127.0.0.1"] = net.ParseIP("127.0.0.1")

	// Domain
	if m.opts.Domain != "" {
		if ip := net.ParseIP(m.opts.Domain); ip != nil {
			ipSet[ip.String()] = ip
		} else {
			dnsSet[strings.ToLower(m.opts.Domain)] = struct{}{}
		}
	}

	// Hosts
	for _, h := range m.opts.Hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ipSet[ip.String()] = ip
		} else {
			dnsSet[strings.ToLower(h)] = struct{}{}
		}
	}

	var dnsNames []string
	for d := range dnsSet {
		dnsNames = append(dnsNames, d)
	}
	var ipAddresses []net.IP
	for _, ip := range ipSet {
		ipAddresses = append(ipAddresses, ip)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"mcode"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create self-signed cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal ec private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	return certPEM, keyPEM, nil
}

// writeAtomic 原子写入文件（先写入同目录下临时文件再重命名）。
func writeAtomic(targetPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(targetPath)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		return err
	}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}

	if err := tmpFile.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		return err
	}

	tmpName = ""
	return nil
}
