package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
)

// cimdFixture 是同时发布授权码、DCR 与 CIMD 能力的模拟授权服务器。
// 通过回调覆盖单个端点，测试可以模拟 CIMD 被拒、注册失败等场景。
type cimdFixture struct {
	as  *httptest.Server
	url string // 传给 SignInWithHint 的 MCP 服务器地址

	registerCalls atomic.Int64
	tokenCalls    atomic.Int64

	mu               sync.Mutex
	authorizeQueries []url.Values
	registerBodies   []map[string]any
	tokenForms       []url.Values
}

type cimdFixtureOptions struct {
	metadata  func(asURL string) map[string]any
	authorize func(w http.ResponseWriter, r *http.Request)
	register  func(w http.ResponseWriter, body map[string]any, call int)
	token     func(w http.ResponseWriter, form url.Values, call int)
}

// defaultCIMDMetadata 返回完整的默认授权服务器元数据，支持 CIMD 且只允许公共客户端。
func defaultCIMDMetadata(asURL string) map[string]any {
	return map[string]any{
		"issuer":                                asURL,
		"authorization_endpoint":                asURL + "/authorize",
		"token_endpoint":                        asURL + "/token",
		"registration_endpoint":                 asURL + "/register",
		"client_id_metadata_document_supported": true,
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"read"},
	}
}

// cimdMetadataNoSupport 返回不支持 CIMD 的元数据，用于验证默认回退到 DCR。
func cimdMetadataNoSupport(asURL string) map[string]any {
	meta := defaultCIMDMetadata(asURL)
	delete(meta, "client_id_metadata_document_supported")
	delete(meta, "token_endpoint_auth_methods_supported")
	return meta
}

func newCIMDFixture(t *testing.T, opts cimdFixtureOptions) *cimdFixture {
	t.Helper()
	f := &cimdFixture{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.as = srv
	f.url = srv.URL + "/mcp"

	metadata := opts.metadata
	if metadata == nil {
		metadata = defaultCIMDMetadata
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"issuer": srv.URL}
		for k, v := range metadata(srv.URL) {
			body[k] = v
		}
		writeTestJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.authorizeQueries = append(f.authorizeQueries, q)
		f.mu.Unlock()

		if opts.authorize != nil {
			opts.authorize(w, r)
			return
		}
		redirect, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || redirect.Scheme == "" || redirect.Host == "" {
			http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
			return
		}
		rq := redirect.Query()
		rq.Set("code", "auth-code")
		rq.Set("state", q.Get("state"))
		redirect.RawQuery = rq.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		call := int(f.registerCalls.Add(1))
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registerBodies = append(f.registerBodies, body)
		f.mu.Unlock()

		if opts.register != nil {
			opts.register(w, body, call)
			return
		}
		writeTestJSON(w, http.StatusCreated, map[string]any{"client_id": "dcr-client-id"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		call := int(f.tokenCalls.Add(1))
		_ = r.ParseForm()
		form := r.PostForm.Clone()
		f.mu.Lock()
		f.tokenForms = append(f.tokenForms, form)
		f.mu.Unlock()

		if opts.token != nil {
			opts.token(w, form, call)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token":  "cimd-access-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "cimd-refresh-token",
			"scope":         "read",
		})
	})
	return f
}

func (f *cimdFixture) authorizeCalls() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.authorizeQueries...)
}

func (f *cimdFixture) registeredBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.registerBodies...)
}

func (f *cimdFixture) tokenRequests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.tokenForms...)
}

// newCIMDManager 构造带 CIMD 文档地址的 OAuthManager。
// OpenBrowser 用真实 HTTP 请求跟随授权服务器的重定向，从而驱动 loopback 回调。
func newCIMDManager(t *testing.T, secrets config.SecretStore, documentURL string, callbackPort int) *OAuthManager {
	t.Helper()
	if secrets == nil {
		secrets = config.NewMemorySecretStore()
	}
	return NewOAuthManager(OAuthOptions{
		Secrets:           secrets,
		ClientMetadataURL: documentURL,
		CallbackPort:      callbackPort,
		OpenBrowser: func(raw string) error {
			resp, err := http.Get(raw)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.Body.Close()
		},
	})
}

// freeTCPPort 预留一个当前空闲的本机端口；CIMD 需要稳定的 loopback 回调地址。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请空闲端口失败：%v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("关闭临时监听失败：%v", err)
	}
	return port
}

func seedStoredToken(t *testing.T, secrets config.SecretStore, serverID string, tok storedToken) {
	t.Helper()
	raw, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("序列化令牌失败：%v", err)
	}
	if err := secrets.Set(tokenAccount(serverID), string(raw)); err != nil {
		t.Fatalf("写入令牌失败：%v", err)
	}
}

func seedStoredClient(t *testing.T, secrets config.SecretStore, serverID string, client storedClient) {
	t.Helper()
	raw, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("序列化客户端失败：%v", err)
	}
	if err := secrets.Set(clientAccount(serverID), string(raw)); err != nil {
		t.Fatalf("写入客户端失败：%v", err)
	}
}

func TestClientRegistrationPrefersCIMD(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{})
	port := freeTCPPort(t)
	docURL := f.as.URL + "/client-metadata.json"
	mgr := newCIMDManager(t, nil, docURL, port)

	if err := mgr.SignInWithHint(context.Background(), "cimd-preferred", f.url, AuthHint{}); err != nil {
		t.Fatalf("CIMD 登录失败：%v", err)
	}
	if !mgr.IsSignedIn("cimd-preferred") {
		t.Fatal("CIMD 登录后应已登录")
	}
	if got := f.registerCalls.Load(); got != 0 {
		t.Fatalf("支持 CIMD 时不应调用动态注册，实际 %d 次", got)
	}

	authorizeCalls := f.authorizeCalls()
	if len(authorizeCalls) != 1 {
		t.Fatalf("authorize 调用次数 = %d", len(authorizeCalls))
	}
	if got := authorizeCalls[0].Get("client_id"); got != docURL {
		t.Fatalf("authorize client_id = %q，期望 CIMD 文档地址", got)
	}
	wantRedirect := "http://127.0.0.1:" + strconv.Itoa(port) + "/callback"
	if got := authorizeCalls[0].Get("redirect_uri"); got != wantRedirect {
		t.Fatalf("authorize redirect_uri = %q，期望 %q", got, wantRedirect)
	}

	tokenForms := f.tokenRequests()
	if len(tokenForms) != 1 {
		t.Fatalf("token 调用次数 = %d", len(tokenForms))
	}
	if got := tokenForms[0].Get("client_id"); got != docURL {
		t.Fatalf("token client_id = %q，期望 CIMD 文档地址", got)
	}

	client, err := mgr.loadClient("cimd-preferred")
	if err != nil {
		t.Fatalf("读取客户端失败：%v", err)
	}
	if client.AuthMethod != clientAuthMethodCIMD {
		t.Fatalf("客户端来源 = %q，期望 %q", client.AuthMethod, clientAuthMethodCIMD)
	}
	if client.ClientID != docURL {
		t.Fatalf("存储的 client_id = %q，期望文档地址", client.ClientID)
	}
	if client.RedirectURI != wantRedirect {
		t.Fatalf("存储的 redirect_uri = %q，期望 %q", client.RedirectURI, wantRedirect)
	}
	if client.Flow != AuthModeLoopback {
		t.Fatalf("存储的流程 = %q，期望 loopback", client.Flow)
	}
}

func TestClientRegistrationFallsBackToDCR(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{metadata: cimdMetadataNoSupport})
	docURL := f.as.URL + "/client-metadata.json"
	mgr := newCIMDManager(t, nil, docURL, 0)

	if err := mgr.SignInWithHint(context.Background(), "cimd-fallback", f.url, AuthHint{}); err != nil {
		t.Fatalf("回退 DCR 登录失败：%v", err)
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("不支持 CIMD 时应恰好动态注册一次，实际 %d 次", got)
	}
	authorizeCalls := f.authorizeCalls()
	if len(authorizeCalls) != 1 {
		t.Fatalf("authorize 调用次数 = %d", len(authorizeCalls))
	}
	if got := authorizeCalls[0].Get("client_id"); got != "dcr-client-id" {
		t.Fatalf("authorize client_id = %q，期望 DCR 客户端", got)
	}
	client, err := mgr.loadClient("cimd-fallback")
	if err != nil {
		t.Fatalf("读取客户端失败：%v", err)
	}
	if client.AuthMethod != clientAuthMethodDCR {
		t.Fatalf("客户端来源 = %q，期望 dcr", client.AuthMethod)
	}
}

func TestClientRegistrationRequiresNoneAuthMethod(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{
		metadata: func(asURL string) map[string]any {
			meta := defaultCIMDMetadata(asURL)
			meta["token_endpoint_auth_methods_supported"] = []string{"client_secret_basic"}
			return meta
		},
	})
	mgr := newCIMDManager(t, nil, f.as.URL+"/client-metadata.json", 0)

	if err := mgr.SignInWithHint(context.Background(), "cimd-auth-method", f.url, AuthHint{}); err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("不支持 none 认证方式时应回退 DCR 一次，实际 %d 次", got)
	}
	if got := f.authorizeCalls()[0].Get("client_id"); got != "dcr-client-id" {
		t.Fatalf("authorize client_id = %q，期望 DCR 客户端", got)
	}
}

func TestClientRegistrationReusesStoredClient(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{})
	secrets := config.NewMemorySecretStore()
	mgr := newCIMDManager(t, secrets, f.as.URL+"/client-metadata.json", 0)

	redirectURI := "http://127.0.0.1:33418/callback"
	seedStoredClient(t, secrets, "reuse", storedClient{
		ClientID:      "existing-client",
		RedirectURI:   redirectURI,
		TokenEndpoint: f.as.URL + "/token",
		Flow:          AuthModeLoopback,
		AuthMethod:    clientAuthMethodDCR,
	})
	meta := &authServerMetadata{
		TokenEndpoint:        f.as.URL + "/token",
		RegistrationEndpoint: f.as.URL + "/register",
	}
	client, err := mgr.clientRegistration(context.Background(), "reuse", meta, redirectURI, AuthModeLoopback, AuthHint{}, true)
	if err != nil {
		t.Fatalf("复用客户端失败：%v", err)
	}
	if client.ClientID != "existing-client" {
		t.Fatalf("client_id = %q，期望复用已有客户端", client.ClientID)
	}
	if got := f.registerCalls.Load(); got != 0 {
		t.Fatalf("复用已有客户端时不应重新注册，实际 %d 次", got)
	}
}

func TestClientRegistrationDeviceFlowSkipsRedirectURI(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{})
	secrets := config.NewMemorySecretStore()
	mgr := newCIMDManager(t, secrets, f.as.URL+"/client-metadata.json", 0)

	meta := &authServerMetadata{
		TokenEndpoint:        f.as.URL + "/token",
		RegistrationEndpoint: f.as.URL + "/register",
	}
	client, err := mgr.clientRegistration(context.Background(), "device-reg", meta, "", AuthModeDevice, AuthHint{}, false)
	if err != nil {
		t.Fatalf("设备流注册失败：%v", err)
	}
	if client.Flow != AuthModeDevice {
		t.Fatalf("客户端流程 = %q，期望 device", client.Flow)
	}
	if client.AuthMethod != clientAuthMethodDCR {
		t.Fatalf("客户端来源 = %q，期望 dcr", client.AuthMethod)
	}
	bodies := f.registeredBodies()
	if len(bodies) != 1 {
		t.Fatalf("注册请求次数 = %d", len(bodies))
	}
	if _, ok := bodies[0]["redirect_uris"]; ok {
		t.Fatal("设备流注册不应携带 redirect_uris")
	}
	if !containsString(bodies[0]["grant_types"], deviceGrantType) {
		t.Fatalf("grant_types = %v，缺少设备码类型", bodies[0]["grant_types"])
	}
}

func TestCIMDRegistrationStageRejectionDoesNotSetMarker(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{
		metadata: cimdMetadataNoSupport,
		register: func(w http.ResponseWriter, body map[string]any, call int) {
			if call == 1 {
				writeTestJSON(w, http.StatusBadRequest, map[string]any{
					"error":             "invalid_client",
					"error_description": "client rejected",
				})
				return
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{"client_id": "dcr-retry"})
		},
	})
	secrets := config.NewMemorySecretStore()
	mgr := newCIMDManager(t, secrets, f.as.URL+"/client-metadata.json", 0)

	if err := mgr.SignInWithHint(context.Background(), "cimd-reg-reject", f.url, AuthHint{}); err != nil {
		t.Fatalf("注册阶段回退后应登录成功：%v", err)
	}
	if got := f.registerCalls.Load(); got != 2 {
		t.Fatalf("注册调用次数 = %d，期望回退后重试一次", got)
	}
	if _, err := secrets.Get(cimdAccount("cimd-reg-reject")); err == nil {
		t.Fatal("注册阶段被拒不应写入 CIMD 不可用标记")
	}
}

func TestCIMDTokenStageInvalidClientFallsBackToDCR(t *testing.T) {
	docURL := ""
	f := newCIMDFixture(t, cimdFixtureOptions{
		token: func(w http.ResponseWriter, form url.Values, call int) {
			if form.Get("client_id") == docURL {
				writeTestJSON(w, http.StatusBadRequest, map[string]any{
					"error":             "invalid_client",
					"error_description": "metadata document revoked",
				})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{
				"access_token":  "dcr-access-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "dcr-refresh-token",
			})
		},
	})
	docURL = f.as.URL + "/client-metadata.json"
	mgr := newCIMDManager(t, nil, docURL, freeTCPPort(t))

	if err := mgr.SignInWithHint(context.Background(), "cimd-token-reject", f.url, AuthHint{}); err != nil {
		t.Fatalf("token 阶段回退后应登录成功：%v", err)
	}
	if !mgr.IsSignedIn("cimd-token-reject") {
		t.Fatal("回退 DCR 后应已登录")
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("动态注册次数 = %d，期望回退时恰好一次", got)
	}
	calls := f.authorizeCalls()
	if len(calls) != 2 {
		t.Fatalf("authorize 调用次数 = %d，期望 2 次", len(calls))
	}
	if got := calls[0].Get("client_id"); got != docURL {
		t.Fatalf("第一次 authorize client_id = %q，期望 CIMD 文档地址", got)
	}
	if got := calls[1].Get("client_id"); got != "dcr-client-id" {
		t.Fatalf("第二次 authorize client_id = %q，期望 DCR 客户端", got)
	}
	marker, ok := mgr.loadCIMDUnavailable("cimd-token-reject")
	if !ok {
		t.Fatal("CIMD 在 token 阶段被拒后应写入不可用标记")
	}
	if marker.Reason != "invalid_client" {
		t.Fatalf("标记原因 = %q，期望 invalid_client", marker.Reason)
	}
	client, err := mgr.loadClient("cimd-token-reject")
	if err != nil {
		t.Fatalf("读取客户端失败：%v", err)
	}
	if client.AuthMethod != clientAuthMethodDCR {
		t.Fatalf("回退后的客户端来源 = %q，期望 dcr", client.AuthMethod)
	}
}

func TestCIMDTokenStageUnauthorizedClientRetriesOnce(t *testing.T) {
	docURL := ""
	f := newCIMDFixture(t, cimdFixtureOptions{
		token: func(w http.ResponseWriter, form url.Values, call int) {
			if form.Get("client_id") == docURL {
				writeTestJSON(w, http.StatusBadRequest, map[string]any{
					"error":             "unauthorized_client",
					"error_description": "client is not allowed",
				})
				return
			}
			writeTestJSON(w, http.StatusInternalServerError, map[string]any{"error": "server_error"})
		},
	})
	docURL = f.as.URL + "/client-metadata.json"
	mgr := newCIMDManager(t, nil, docURL, freeTCPPort(t))

	err := mgr.SignInWithHint(context.Background(), "cimd-unauthorized", f.url, AuthHint{})
	if !errors.Is(err, ErrOAuthTokenFailed) {
		t.Fatalf("错误 = %v，期望 ErrOAuthTokenFailed", err)
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("动态注册次数 = %d，期望一次", got)
	}
	calls := f.authorizeCalls()
	if len(calls) != 2 {
		t.Fatalf("authorize 调用次数 = %d，期望只回退一次", len(calls))
	}
	if got := calls[0].Get("client_id"); got != docURL {
		t.Fatalf("第一次 authorize client_id = %q，期望 CIMD 文档地址", got)
	}
	if got := calls[1].Get("client_id"); got != "dcr-client-id" {
		t.Fatalf("第二次 authorize client_id = %q，期望 DCR 客户端", got)
	}
	marker, ok := mgr.loadCIMDUnavailable("cimd-unauthorized")
	if !ok {
		t.Fatal("CIMD 在 token 阶段被拒后应写入不可用标记")
	}
	if marker.Reason != "unauthorized_client" {
		t.Fatalf("标记原因 = %q，期望 unauthorized_client", marker.Reason)
	}
}

func TestCIMDUnavailableMarkerSkipsCIMDOnNextSignIn(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{})
	docURL := f.as.URL + "/client-metadata.json"
	secrets := config.NewMemorySecretStore()
	mgr := newCIMDManager(t, secrets, docURL, 0)
	if err := mgr.markCIMDUnavailable("cimd-marker", "invalid_client"); err != nil {
		t.Fatalf("写入标记失败：%v", err)
	}

	if err := mgr.SignInWithHint(context.Background(), "cimd-marker", f.url, AuthHint{}); err != nil {
		t.Fatalf("有标记时登录失败：%v", err)
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("动态注册次数 = %d，期望一次", got)
	}
	calls := f.authorizeCalls()
	if len(calls) != 1 {
		t.Fatalf("authorize 调用次数 = %d", len(calls))
	}
	if got := calls[0].Get("client_id"); got != "dcr-client-id" {
		t.Fatalf("authorize client_id = %q，有标记时不应再使用 CIMD", got)
	}
}

func TestCIMDMarkerIgnoredAfterDocumentURLChange(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{})
	oldDocURL := "https://old.example/client-metadata.json"
	newDocURL := f.as.URL + "/new-client-metadata.json"
	secrets := config.NewMemorySecretStore()
	raw, err := json.Marshal(cimdUnavailable{
		DocumentURL: oldDocURL,
		Reason:      "invalid_client",
		At:          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("序列化标记失败：%v", err)
	}
	if err := secrets.Set(cimdAccount("cimd-url-change"), string(raw)); err != nil {
		t.Fatalf("写入旧标记失败：%v", err)
	}
	mgr := newCIMDManager(t, secrets, newDocURL, freeTCPPort(t))

	if err := mgr.SignInWithHint(context.Background(), "cimd-url-change", f.url, AuthHint{}); err != nil {
		t.Fatalf("更换文档地址后登录失败：%v", err)
	}
	if got := f.registerCalls.Load(); got != 0 {
		t.Fatalf("新文档地址应优先尝试 CIMD，实际注册 %d 次", got)
	}
	if got := f.authorizeCalls()[0].Get("client_id"); got != newDocURL {
		t.Fatalf("authorize client_id = %q，期望新文档地址", got)
	}
	if _, err := secrets.Get(cimdAccount("cimd-url-change")); err == nil {
		t.Fatal("文档地址变化后旧标记应被惰性删除")
	}
}

func TestRefreshInvalidClientClearsTokenAndClient(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{
		token: func(w http.ResponseWriter, form url.Values, call int) {
			if form.Get("grant_type") == "refresh_token" {
				writeTestJSON(w, http.StatusBadRequest, map[string]any{
					"error":             "invalid_client",
					"error_description": "unknown client",
				})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{
				"access_token":  "dcr-access-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "dcr-refresh-token",
			})
		},
	})
	const serverID = "cimd-refresh-reject"
	docURL := f.as.URL + "/client-metadata.json"
	secrets := config.NewMemorySecretStore()
	seedStoredToken(t, secrets, serverID, storedToken{
		AccessToken:   "old-access-token",
		RefreshToken:  "old-refresh-token",
		ExpiresAt:     time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
		TokenType:     "Bearer",
		Resource:      f.url,
		TokenEndpoint: f.as.URL + "/token",
	})
	seedStoredClient(t, secrets, serverID, storedClient{
		ClientID:      docURL,
		RedirectURI:   "http://127.0.0.1:33418/callback",
		TokenEndpoint: f.as.URL + "/token",
		Flow:          AuthModeLoopback,
		AuthMethod:    clientAuthMethodCIMD,
	})
	mgr := newCIMDManager(t, secrets, docURL, 0)

	if _, err := mgr.AccessToken(context.Background(), serverID); !errors.Is(err, ErrOAuthReauthRequired) {
		t.Fatalf("刷新错误 = %v，期望 ErrOAuthReauthRequired", err)
	}
	if mgr.IsSignedIn(serverID) {
		t.Fatal("刷新被拒后不应仍处于登录状态")
	}
	if _, err := secrets.Get(tokenAccount(serverID)); err == nil {
		t.Fatal("刷新被拒后应删除令牌")
	}
	if _, err := secrets.Get(clientAccount(serverID)); err == nil {
		t.Fatal("刷新被拒后应删除客户端记录")
	}
	marker, ok := mgr.loadCIMDUnavailable(serverID)
	if !ok || marker.Reason != "invalid_client" {
		t.Fatalf("CIMD 标记 = %+v, %v，期望保留 invalid_client", marker, ok)
	}

	// 标记会让下一次登录直接走 DCR。
	if err := mgr.SignInWithHint(context.Background(), serverID, f.url, AuthHint{}); err != nil {
		t.Fatalf("刷新失败后重新登录失败：%v", err)
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("重新登录的动态注册次数 = %d，期望一次", got)
	}
}

// countingSecretStore 统计 Delete 调用次数，用于验证 singleflight 只清理一次。
type countingSecretStore struct {
	inner config.SecretStore

	mu      sync.Mutex
	deletes map[string]int
}

func newCountingSecretStore(inner config.SecretStore) *countingSecretStore {
	return &countingSecretStore{inner: inner, deletes: make(map[string]int)}
}

func (s *countingSecretStore) Get(account string) (string, error) {
	return s.inner.Get(account)
}

func (s *countingSecretStore) Set(account, value string) error {
	return s.inner.Set(account, value)
}

func (s *countingSecretStore) Delete(account string) error {
	s.mu.Lock()
	s.deletes[account]++
	s.mu.Unlock()
	return s.inner.Delete(account)
}

func (s *countingSecretStore) deleteCount(account string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes[account]
}

func TestRefreshInvalidClientSingleflightCleansOnce(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{
		token: func(w http.ResponseWriter, form url.Values, call int) {
			time.Sleep(200 * time.Millisecond)
			writeTestJSON(w, http.StatusBadRequest, map[string]any{
				"error":             "invalid_client",
				"error_description": "unknown client",
			})
		},
	})
	const serverID = "cimd-refresh-singleflight"
	docURL := f.as.URL + "/client-metadata.json"
	secrets := newCountingSecretStore(config.NewMemorySecretStore())
	seedStoredToken(t, secrets, serverID, storedToken{
		AccessToken:   "old-access-token",
		RefreshToken:  "old-refresh-token",
		ExpiresAt:     time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
		TokenType:     "Bearer",
		Resource:      f.url,
		TokenEndpoint: f.as.URL + "/token",
	})
	seedStoredClient(t, secrets, serverID, storedClient{
		ClientID:      docURL,
		TokenEndpoint: f.as.URL + "/token",
		Flow:          AuthModeLoopback,
		AuthMethod:    clientAuthMethodCIMD,
	})
	mgr := newCIMDManager(t, secrets, docURL, 0)

	const workers = 10
	start := make(chan struct{})
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = mgr.AccessToken(context.Background(), serverID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, ErrOAuthReauthRequired) {
			t.Fatalf("第 %d 个并发刷新错误 = %v，期望 ErrOAuthReauthRequired", i, err)
		}
	}
	if got := f.tokenCalls.Load(); got != 1 {
		t.Fatalf("token 调用次数 = %d，期望 singleflight 只刷新一次", got)
	}
	if got := secrets.deleteCount(tokenAccount(serverID)); got != 1 {
		t.Fatalf("令牌 Delete 次数 = %d，期望一次", got)
	}
	if got := secrets.deleteCount(clientAccount(serverID)); got != 1 {
		t.Fatalf("客户端 Delete 次数 = %d，期望一次", got)
	}
}

func TestSignOutKeepsCIMDMarker(t *testing.T) {
	f := newCIMDFixture(t, cimdFixtureOptions{})
	docURL := f.as.URL + "/client-metadata.json"
	secrets := config.NewMemorySecretStore()
	mgr := newCIMDManager(t, secrets, docURL, 0)
	const serverID = "cimd-signout"
	if err := mgr.markCIMDUnavailable(serverID, "invalid_client"); err != nil {
		t.Fatalf("写入标记失败：%v", err)
	}

	mgr.SignOut(serverID)
	if _, ok := mgr.loadCIMDUnavailable(serverID); !ok {
		t.Fatal("SignOut 不应删除 CIMD 不可用标记")
	}

	if err := mgr.SignInWithHint(context.Background(), serverID, f.url, AuthHint{}); err != nil {
		t.Fatalf("退出后重新登录失败：%v", err)
	}
	if got := f.registerCalls.Load(); got != 1 {
		t.Fatalf("标记仍在时应回退 DCR，动态注册次数 = %d", got)
	}
}

// containsString 判断 JSON 解码后的数组是否包含目标字符串。
func containsString(value any, want string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}
