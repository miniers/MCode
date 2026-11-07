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
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/acme"
)

func TestUpsertARecordGuardsExistingRecord(t *testing.T) {
	const (
		name  = "remote.example.com"
		lanIP = "192.168.1.20"
	)

	tests := []struct {
		name       string
		existing   string // 空字符串表示该域名尚无 A 记录
		wantErr    bool
		wantPatch  bool
		wantCreate bool
	}{
		{name: "已有记录指向局域网地址时更新", existing: "192.168.1.5", wantPatch: true},
		{name: "已有记录指向公网地址时拒绝更新", existing: "203.0.113.5", wantErr: true},
		{name: "记录不存在时创建", wantCreate: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				patched int
				created int
			)

			mux := http.NewServeMux()
			mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("name") != "example.com" {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": []any{}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"errors":  []any{},
					"result":  []map[string]any{{"id": "zone-1", "name": "example.com"}},
				})
			})

			mux.HandleFunc("/zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case http.MethodGet:
					result := []any{}
					if tc.existing != "" {
						result = append(result, map[string]any{
							"id":      "rec-a",
							"type":    "A",
							"name":    name,
							"content": tc.existing,
						})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
				case http.MethodPost:
					mu.Lock()
					created++
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{
						"success": true,
						"errors":  []any{},
						"result":  map[string]any{"id": "rec-new", "type": "A", "name": name, "content": lanIP},
					})
				}
			})

			mux.HandleFunc("/zones/zone-1/dns_records/rec-a", func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				patched++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"errors":  []any{},
					"result":  map[string]any{"id": "rec-a", "type": "A", "name": name, "content": lanIP},
				})
			})

			srv := httptest.NewServer(mux)
			defer srv.Close()

			cfDNS := newCloudflareDNS(srv.URL, "dummy-token", srv.Client(), 0)
			err := cfDNS.upsertARecord(context.Background(), name, lanIP)

			mu.Lock()
			patchCount, createCount := patched, created
			mu.Unlock()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error when existing record points to a public address")
				}
				if !strings.Contains(err.Error(), "公网地址") || !strings.Contains(err.Error(), tc.existing) {
					t.Fatalf("unexpected error message: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantPatch != (patchCount > 0) {
				t.Errorf("expected patch request: %v, got %d patch request(s)", tc.wantPatch, patchCount)
			}
			if tc.wantCreate != (createCount > 0) {
				t.Errorf("expected create request: %v, got %d create request(s)", tc.wantCreate, createCount)
			}
		})
	}
}

func TestACMEIssuanceAndCloudflareDNS(t *testing.T) {
	testDomain := "remote.example.com"
	chalToken := "test-challenge-token-98765"

	// 1. 生成测试 CA
	caPrivKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(100),
		Subject: pkix.Name{
			CommonName: "ACME Test Root CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}

	// 2. 模拟 ACME 服务器（RFC 8555）
	var (
		acmeMu        sync.Mutex
		authzStatus   = "pending"
		chalStatus    = "pending"
		orderStatus   = "pending"
		issuedCertPEM []byte
		acmeServer    *httptest.Server
	)

	acmeMux := http.NewServeMux()

	acmeMux.HandleFunc("/directory", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Content-Type", "application/json")
		base := acmeServer.URL
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce":   base + "/new-nonce",
			"newAccount": base + "/new-account",
			"newOrder":   base + "/new-order",
		})
	})

	acmeMux.HandleFunc("/new-nonce", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.WriteHeader(http.StatusOK)
	})

	acmeMux.HandleFunc("/new-account", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Location", acmeServer.URL+"/accounts/1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	})

	acmeMux.HandleFunc("/new-order", func(w http.ResponseWriter, r *http.Request) {
		acmeMu.Lock()
		authzStatus = "pending"
		chalStatus = "pending"
		orderStatus = "pending"
		acmeMu.Unlock()

		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Location", acmeServer.URL+"/orders/1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "pending",
			"authorizations": []string{acmeServer.URL + "/authz/1"},
			"finalize":       acmeServer.URL + "/orders/1/finalize",
		})
	})

	acmeMux.HandleFunc("/authz/1", func(w http.ResponseWriter, r *http.Request) {
		acmeMu.Lock()
		aStatus := authzStatus
		cStatus := chalStatus
		acmeMu.Unlock()

		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": aStatus,
			"identifier": map[string]string{
				"type":  "dns",
				"value": testDomain,
			},
			"challenges": []map[string]any{
				{
					"type":   "dns-01",
					"url":    acmeServer.URL + "/chal/1",
					"token":  chalToken,
					"status": cStatus,
				},
			},
		})
	})

	acmeMux.HandleFunc("/chal/1", func(w http.ResponseWriter, r *http.Request) {
		acmeMu.Lock()
		chalStatus = "valid"
		authzStatus = "valid"
		orderStatus = "ready"
		acmeMu.Unlock()

		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":   "dns-01",
			"url":    acmeServer.URL + "/chal/1",
			"token":  chalToken,
			"status": "valid",
		})
	})

	acmeMux.HandleFunc("/orders/1", func(w http.ResponseWriter, r *http.Request) {
		acmeMu.Lock()
		oStatus := orderStatus
		acmeMu.Unlock()

		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"status":         oStatus,
			"authorizations": []string{acmeServer.URL + "/authz/1"},
			"finalize":       acmeServer.URL + "/orders/1/finalize",
		}
		if oStatus == "valid" {
			resp["certificate"] = acmeServer.URL + "/cert/1"
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	acmeMux.HandleFunc("/orders/1/finalize", func(w http.ResponseWriter, r *http.Request) {
		var jws struct {
			Payload string `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&jws)
		payloadBytes, _ := base64.RawURLEncoding.DecodeString(jws.Payload)

		var csrReq struct {
			CSR string `json:"csr"`
		}
		_ = json.Unmarshal(payloadBytes, &csrReq)
		csrDER, _ := base64.RawURLEncoding.DecodeString(csrReq.CSR)

		csr, err := x509.ParseCertificateRequest(csrDER)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid csr: %v", err), http.StatusBadRequest)
			return
		}

		leafTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(200),
			Subject:      csr.Subject,
			DNSNames:     csr.DNSNames,
			NotBefore:    time.Now().Add(-1 * time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}

		leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, csr.PublicKey, caPrivKey)
		if err != nil {
			http.Error(w, fmt.Sprintf("create cert: %v", err), http.StatusInternalServerError)
			return
		}

		var pemBuf bytes.Buffer
		_ = pem.Encode(&pemBuf, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
		_ = pem.Encode(&pemBuf, &pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

		acmeMu.Lock()
		issuedCertPEM = pemBuf.Bytes()
		orderStatus = "valid"
		acmeMu.Unlock()

		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Location", acmeServer.URL+"/orders/1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "valid",
			"authorizations": []string{acmeServer.URL + "/authz/1"},
			"finalize":       acmeServer.URL + "/orders/1/finalize",
			"certificate":    acmeServer.URL + "/cert/1",
		})
	})

	acmeMux.HandleFunc("/cert/1", func(w http.ResponseWriter, r *http.Request) {
		acmeMu.Lock()
		certBytes := issuedCertPEM
		acmeMu.Unlock()

		w.Header().Set("Replay-Nonce", "nonce-"+uuid.NewString())
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(certBytes)
	})

	acmeServer = httptest.NewTLSServer(acmeMux)
	defer acmeServer.Close()

	// 3. 模拟 Cloudflare API
	var (
		cfMu              sync.Mutex
		createdTXTName    string
		createdTXTContent string
		deletedRecordID   string
	)

	cfMux := http.NewServeMux()
	cfMux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(testDomain, name) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"errors":  []any{},
				"result": []map[string]any{
					{
						"id":   "zone-123456",
						"name": name,
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"errors":  []any{},
			"result":  []any{},
		})
	})

	cfMux.HandleFunc("/zones/zone-123456/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var rec struct {
				Type    string `json:"type"`
				Name    string `json:"name"`
				Content string `json:"content"`
				TTL     int    `json:"ttl"`
			}
			_ = json.NewDecoder(r.Body).Decode(&rec)

			cfMu.Lock()
			createdTXTName = rec.Name
			createdTXTContent = rec.Content
			cfMu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"errors":  []any{},
				"result": map[string]any{
					"id":      "rec-9999",
					"type":    rec.Type,
					"name":    rec.Name,
					"content": rec.Content,
				},
			})
		}
	})

	cfMux.HandleFunc("/zones/zone-123456/dns_records/rec-9999", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			cfMu.Lock()
			deletedRecordID = "rec-9999"
			cfMu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"errors":  []any{},
				"result": map[string]any{
					"id": "rec-9999",
				},
			})
		}
	})

	cfServer := httptest.NewServer(cfMux)
	defer cfServer.Close()

	// 4. 执行 ACME 签发
	dir := t.TempDir()
	opts := TLSOptions{
		Dir:               dir,
		Domain:            testDomain,
		Email:             "test@example.com",
		DirectoryURL:      acmeServer.URL + "/directory",
		CloudflareToken:   "dummy-token",
		CloudflareAPIBase: cfServer.URL,
		HTTPClient:        acmeServer.Client(),
	}

	cfDNS := newCloudflareDNS(cfServer.URL, "dummy-token", cfServer.Client(), 0)

	certPEM, keyPEM, err := issueCertificate(context.Background(), opts, cfDNS)
	if err != nil {
		t.Fatalf("issueCertificate failed: %v", err)
	}

	// 5. 校验 Cloudflare DNS 操作
	cfMu.Lock()
	actualName := createdTXTName
	actualContent := createdTXTContent
	actualDeleted := deletedRecordID
	cfMu.Unlock()

	expectedName := "_acme-challenge." + testDomain
	if actualName != expectedName {
		t.Errorf("expected TXT name %q, got %q", expectedName, actualName)
	}

	// 计算预期 token 对应的 dns-01 record
	// 签发流程使用持久化的 ACME 账户密钥（<Dir>/account.key）计算 TXT 值，
	// 因此这里也必须用同一账户密钥计算期望值，而不是用模拟 CA 的私钥。
	accountKey, err := loadOrGenerateAccountKey(dir)
	if err != nil {
		t.Fatalf("load account key: %v", err)
	}
	dummyClient := &acme.Client{Key: accountKey}
	expectedTXTVal, err := dummyClient.DNS01ChallengeRecord(chalToken)
	if err != nil {
		t.Fatalf("compute expected challenge record: %v", err)
	}
	if actualContent != expectedTXTVal {
		t.Errorf("expected TXT content %q, got %q", expectedTXTVal, actualContent)
	}

	if actualDeleted != "rec-9999" {
		t.Errorf("expected record rec-9999 to be deleted, got %q", actualDeleted)
	}

	// 6. 校验签发证书内容
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse issued x509 key pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf certificate: %v", err)
	}

	sanFound := false
	for _, san := range leaf.DNSNames {
		if san == testDomain {
			sanFound = true
			break
		}
	}
	if !sanFound {
		t.Errorf("expected SAN %q in certificate, got: %v", testDomain, leaf.DNSNames)
	}

	// 7. 校验 account.key 持久化并在第二次签发时复用
	accountKeyPath := filepath.Join(dir, "account.key")
	initialKeyBytes, err := os.ReadFile(accountKeyPath)
	if err != nil {
		t.Fatalf("read account.key: %v", err)
	}
	if len(initialKeyBytes) == 0 {
		t.Fatalf("account.key is empty")
	}

	// 第二次签发
	certPEM2, _, err := issueCertificate(context.Background(), opts, cfDNS)
	if err != nil {
		t.Fatalf("second issueCertificate failed: %v", err)
	}
	if len(certPEM2) == 0 {
		t.Fatalf("second certPEM is empty")
	}

	secondKeyBytes, err := os.ReadFile(accountKeyPath)
	if err != nil {
		t.Fatalf("read account.key after second issuance: %v", err)
	}
	if !bytes.Equal(initialKeyBytes, secondKeyBytes) {
		t.Errorf("account.key was not reused between issuances")
	}
}
