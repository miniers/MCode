package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

var defaultACMEHTTPClient = &http.Client{Timeout: 30 * time.Second}

// dnsProvider 抽象 ACME DNS-01 质询提供方。
type dnsProvider interface {
	Present(ctx context.Context, fqdn, value string) error
	CleanUp(ctx context.Context, fqdn, value string) error
}

type cloudflareRecordInfo struct {
	zoneID   string
	recordID string
}

// cloudflareDNS 实现基于 Cloudflare API 的 DNS-01 质询提供方。
type cloudflareDNS struct {
	apiBase          string
	token            string
	client           *http.Client
	propagationDelay time.Duration

	mu      sync.Mutex
	records map[string]cloudflareRecordInfo // key: fqdn + "\x00" + value
	zones   map[string]string               // 缓存 domain -> zoneID
}

// newCloudflareDNS 创建 cloudflareDNS 实例。
func newCloudflareDNS(apiBase, token string, client *http.Client, propagationDelay time.Duration) *cloudflareDNS {
	if apiBase == "" {
		apiBase = "https://api.cloudflare.com/client/v4"
	}
	apiBase = strings.TrimRight(apiBase, "/")
	if client == nil {
		client = defaultACMEHTTPClient
	}
	return &cloudflareDNS{
		apiBase:          apiBase,
		token:            token,
		client:           client,
		propagationDelay: propagationDelay,
		records:          make(map[string]cloudflareRecordInfo),
		zones:            make(map[string]string),
	}
}

type cfAPIResponse[T any] struct {
	Success  bool         `json:"success"`
	Errors   []cfAPIError `json:"errors"`
	Messages []string     `json:"messages"`
	Result   T            `json:"result"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfZoneItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfRecordItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// cgnatPrefix 是 100.64.0.0/10（运营商级 NAT / Tailscale 等使用的地址段），不属于 RFC1918。
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// isOverwritableAddr 判断 A 记录内容是否为可安全覆盖的私有地址，
// 覆盖范围包括 RFC1918、100.64/10、链路本地 169.254/16 与回环地址。
func isOverwritableAddr(content string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(content))
	if err != nil || !addr.Is4() {
		return false
	}
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || cgnatPrefix.Contains(addr)
}

func formatCloudflareErrors(errs []cfAPIError) error {
	if len(errs) == 0 {
		return errors.New("cloudflare api error: request unsuccessful")
	}
	var parts []string
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("[%d] %s", e.Code, e.Message))
	}
	return fmt.Errorf("cloudflare api error: %s", strings.Join(parts, "; "))
}

// findZoneID 根据 domain 逐级去掉最左一段寻找 Cloudflare Zone ID。
func (c *cloudflareDNS) findZoneID(ctx context.Context, domain string) (string, error) {
	cleanDomain := strings.TrimSuffix(strings.TrimSpace(domain), ".")
	c.mu.Lock()
	if id, ok := c.zones[cleanDomain]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	parts := strings.Split(cleanDomain, ".")
	for i := 0; i < len(parts)-1; i++ {
		candidate := strings.Join(parts[i:], ".")
		u := fmt.Sprintf("%s/zones?name=%s", c.apiBase, url.QueryEscape(candidate))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)

		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			return "", err
		}

		var apiResp cfAPIResponse[[]cfZoneItem]
		if err := json.Unmarshal(body, &apiResp); err != nil {
			return "", fmt.Errorf("decode cloudflare zones response: %w", err)
		}
		if !apiResp.Success {
			return "", formatCloudflareErrors(apiResp.Errors)
		}

		if len(apiResp.Result) > 0 {
			zoneID := apiResp.Result[0].ID
			c.mu.Lock()
			c.zones[cleanDomain] = zoneID
			c.mu.Unlock()
			return zoneID, nil
		}
	}

	return "", fmt.Errorf("no matching cloudflare zone found for domain: %s", domain)
}

func (c *cloudflareDNS) Present(ctx context.Context, fqdn, value string) error {
	zoneID, err := c.findZoneID(ctx, fqdn)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"type":    "TXT",
		"name":    fqdn,
		"content": value,
		"ttl":     120,
	})
	if err != nil {
		return err
	}

	u := fmt.Sprintf("%s/zones/%s/dns_records", c.apiBase, zoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}

	var apiResp cfAPIResponse[cfRecordItem]
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("decode cloudflare create dns record response: %w", err)
	}
	if !apiResp.Success {
		return formatCloudflareErrors(apiResp.Errors)
	}

	recordID := apiResp.Result.ID
	key := fqdn + "\x00" + value
	c.mu.Lock()
	c.records[key] = cloudflareRecordInfo{
		zoneID:   zoneID,
		recordID: recordID,
	}
	c.mu.Unlock()

	if c.propagationDelay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.propagationDelay):
		}
	}
	return nil
}

func (c *cloudflareDNS) CleanUp(ctx context.Context, fqdn, value string) error {
	key := fqdn + "\x00" + value
	c.mu.Lock()
	info, ok := c.records[key]
	if ok {
		delete(c.records, key)
	}
	c.mu.Unlock()

	if !ok || info.zoneID == "" || info.recordID == "" {
		return nil
	}

	u := fmt.Sprintf("%s/zones/%s/dns_records/%s", c.apiBase, info.zoneID, info.recordID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}

	var apiResp cfAPIResponse[any]
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("decode cloudflare delete dns record response: %w", err)
	}
	if !apiResp.Success {
		return formatCloudflareErrors(apiResp.Errors)
	}

	return nil
}

// upsertARecord 创建或更新域名的 A 记录，指向给定的局域网 IPv4 地址；
// 已有记录指向公网地址时拒绝更新并返回错误，避免改坏线上服务。
func (c *cloudflareDNS) upsertARecord(ctx context.Context, name, ipv4 string) error {
	zoneID, err := c.findZoneID(ctx, name)
	if err != nil {
		return err
	}

	queryURL := fmt.Sprintf("%s/zones/%s/dns_records?type=A&name=%s", c.apiBase, zoneID, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}

	var listResp cfAPIResponse[[]cfRecordItem]
	if err := json.Unmarshal(body, &listResp); err != nil {
		return fmt.Errorf("decode cloudflare dns records response: %w", err)
	}
	if !listResp.Success {
		return formatCloudflareErrors(listResp.Errors)
	}

	if len(listResp.Result) == 0 {
		payload, err := json.Marshal(map[string]any{
			"type":    "A",
			"name":    name,
			"content": ipv4,
			"ttl":     60,
			"proxied": false,
		})
		if err != nil {
			return err
		}
		createURL := fmt.Sprintf("%s/zones/%s/dns_records", c.apiBase, zoneID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}

		var createResp cfAPIResponse[cfRecordItem]
		if err := json.Unmarshal(body, &createResp); err != nil {
			return fmt.Errorf("decode cloudflare create a record response: %w", err)
		}
		if !createResp.Success {
			return formatCloudflareErrors(createResp.Errors)
		}
		return nil
	}

	record := listResp.Result[0]
	if record.Content == ipv4 {
		return nil
	}
	if record.ID == "" {
		return errors.New("cloudflare api error: missing a record id")
	}
	// 已有 A 记录若指向公网地址，说明该域名很可能正在承载线上站点，
	// 直接覆盖会立刻改坏线上服务，因此只更新指向私有/回环/链路本地地址的记录。
	if !isOverwritableAddr(record.Content) {
		return fmt.Errorf("域名 %s 的 A 记录当前指向公网地址 %s，为避免改坏线上服务已跳过更新；请改用专用子域名（例如 remote.%s）", name, record.Content, name)
	}

	payload, err := json.Marshal(map[string]any{
		"content": ipv4,
		"ttl":     60,
		"proxied": false,
	})
	if err != nil {
		return err
	}
	patchURL := fmt.Sprintf("%s/zones/%s/dns_records/%s", c.apiBase, zoneID, url.PathEscape(record.ID))
	req, err = http.NewRequestWithContext(ctx, http.MethodPatch, patchURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = c.client.Do(req)
	if err != nil {
		return err
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}

	var patchResp cfAPIResponse[cfRecordItem]
	if err := json.Unmarshal(body, &patchResp); err != nil {
		return fmt.Errorf("decode cloudflare patch a record response: %w", err)
	}
	if !patchResp.Success {
		return formatCloudflareErrors(patchResp.Errors)
	}
	return nil
}

// loadOrGenerateAccountKey 从 opts.Dir/account.key 读取现有账户密钥，不存在则生成并持久化。
func loadOrGenerateAccountKey(dir string) (*ecdsa.PrivateKey, error) {
	keyPath := filepath.Join(dir, "account.key")
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read acme account.key: %w", err)
	}
	if err == nil {
		block, _ := pem.Decode(keyBytes)
		if block != nil {
			if privKey, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				return privKey, nil
			}
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if ecKey, ok := key.(*ecdsa.PrivateKey); ok {
					return ecKey, nil
				}
			}
		}
		return nil, errors.New("acme account.key is corrupt or unparseable")
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate acme account key: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("marshal acme account key: %w", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	if err := writeAtomic(keyPath, pemData, 0o600); err != nil {
		return nil, fmt.Errorf("save acme account key: %w", err)
	}
	return privKey, nil
}

// issueCertificate 通过 ACME 协议及 DNS-01 质询申请并签发证书。
func issueCertificate(ctx context.Context, opts TLSOptions, dns dnsProvider) (certPEM, keyPEM []byte, err error) {
	accountKey, err := loadOrGenerateAccountKey(opts.Dir)
	if err != nil {
		return nil, nil, err
	}

	directoryURL := opts.DirectoryURL
	if directoryURL == "" {
		directoryURL = acme.LetsEncryptURL
	}

	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: directoryURL,
		HTTPClient:   opts.HTTPClient,
	}

	acct := &acme.Account{}
	if opts.Email != "" {
		acct.Contact = []string{"mailto:" + opts.Email}
	}

	_, err = client.Register(ctx, acct, func(tosURL string) bool { return true })
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		var acmeErr *acme.Error
		if !(errors.As(err, &acmeErr) && strings.Contains(strings.ToLower(acmeErr.Detail), "already exists")) {
			return nil, nil, fmt.Errorf("acme register account: %w", err)
		}
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(opts.Domain))
	if err != nil {
		return nil, nil, fmt.Errorf("acme authorize order: %w", err)
	}

	for _, authzURL := range order.AuthzURLs {
		if err := processAuthorization(ctx, client, dns, opts.Domain, authzURL); err != nil {
			return nil, nil, err
		}
	}

	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return nil, nil, fmt.Errorf("acme wait order: %w", err)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate certificate key: %w", err)
	}

	csrReq := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: opts.Domain,
		},
		DNSNames: []string{opts.Domain},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrReq, certKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create csr: %w", err)
	}

	derChain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		return nil, nil, fmt.Errorf("acme create order cert: %w", err)
	}

	var certBuf bytes.Buffer
	for _, der := range derChain {
		if err := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, nil, fmt.Errorf("encode certificate pem: %w", err)
		}
	}

	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal certificate key: %w", err)
	}
	keyBuf := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certBuf.Bytes(), keyBuf, nil
}

// processAuthorization 处理单个 authorization 的 DNS-01 质询。
func processAuthorization(ctx context.Context, client *acme.Client, dns dnsProvider, domain, authzURL string) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("acme get authorization: %w", err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}

	var chal *acme.Challenge
	for _, c := range authz.Challenges {
		if c.Type == "dns-01" {
			chal = c
			break
		}
	}
	if chal == nil {
		return fmt.Errorf("no dns-01 challenge available in authorization %s", authzURL)
	}

	txtValue, err := client.DNS01ChallengeRecord(chal.Token)
	if err != nil {
		return fmt.Errorf("calculate dns-01 record value: %w", err)
	}

	fqdn := "_acme-challenge." + domain
	if err := dns.Present(ctx, fqdn, txtValue); err != nil {
		return fmt.Errorf("dns present %s: %w", fqdn, err)
	}
	defer func() {
		_ = dns.CleanUp(ctx, fqdn, txtValue)
	}()

	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("acme accept challenge: %w", err)
	}

	if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
		return fmt.Errorf("acme wait authorization: %w", err)
	}

	return nil
}
