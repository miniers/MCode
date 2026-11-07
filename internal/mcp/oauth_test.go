package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
)

type authCodeInfo struct {
	challenge   string
	clientID    string
	redirectURI string
}

func TestMCPOAuth(t *testing.T) {
	var (
		mu              sync.Mutex
		authCodes       = make(map[string]authCodeInfo)
		tokenCalls      atomic.Int64
		refreshCalls    atomic.Int64
		refreshResource atomic.Value
		mismatchFlag    bool
		asServer        *httptest.Server
		rsServer        *httptest.Server
	)

	asMux := http.NewServeMux()
	asServer = httptest.NewServer(asMux)
	defer asServer.Close()

	rsMux := http.NewServeMux()
	rsMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="read write"`, asServer.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	rsServer = httptest.NewServer(rsMux)
	defer rsServer.Close()

	// 1. /.well-known/oauth-protected-resource
	asMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              rsServer.URL,
			"authorization_servers": []string{asServer.URL},
			"scopes_supported":      []string{"read", "write"},
		})
	})

	// 2. /.well-known/oauth-authorization-server
	asMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 asServer.URL,
			"authorization_endpoint": asServer.URL + "/authorize",
			"token_endpoint":         asServer.URL + "/token",
			"registration_endpoint":  asServer.URL + "/register",
			"scopes_supported":       []string{"read", "write"},
		})
	})

	// 3. /register
	asMux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "mock-client-id",
			"client_secret": "mock-client-secret",
		})
	})

	// 4. /authorize
	asMux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirectURI := q.Get("redirect_uri")
		state := q.Get("state")
		challenge := q.Get("code_challenge")
		clientID := q.Get("client_id")

		mu.Lock()
		if mismatchFlag {
			state = "mismatched-state"
		}
		code := fmt.Sprintf("code-%d", time.Now().UnixNano())
		authCodes[code] = authCodeInfo{
			challenge:   challenge,
			clientID:    clientID,
			redirectURI: redirectURI,
		}
		mu.Unlock()

		target := fmt.Sprintf("%s?code=%s&state=%s", redirectURI, url.QueryEscape(code), url.QueryEscape(state))
		http.Redirect(w, r, target, http.StatusFound)
	})

	// 5. /token
	asMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		_ = r.ParseForm()
		grantType := r.Form.Get("grant_type")
		w.Header().Set("Content-Type", "application/json")

		if grantType == "authorization_code" {
			code := r.Form.Get("code")
			verifier := r.Form.Get("code_verifier")

			mu.Lock()
			info, ok := authCodes[code]
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "invalid code"})
				return
			}

			// 校验 PKCE：sha256(verifier) 必须和授权时的 challenge 一致
			h := sha256.Sum256([]byte(verifier))
			computedChallenge := base64.RawURLEncoding.EncodeToString(h[:])
			if computedChallenge != info.challenge {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "pkce challenge mismatch"})
				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mock-access-token-initial",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "mock-refresh-token-1",
				"scope":         "read write",
			})
			return
		}

		if grantType == "refresh_token" {
			refreshCalls.Add(1)
			refreshResource.Store(r.Form.Get("resource"))
			refreshToken := r.Form.Get("refresh_token")
			if refreshToken == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "missing refresh token"})
				return
			}

			time.Sleep(20 * time.Millisecond)

			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mock-access-token-refreshed",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "mock-refresh-token-2",
				"scope":         "read write",
			})
			return
		}

		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "unsupported_grant_type"})
	})

	openBrowser := func(authURL string) error {
		resp, err := http.Get(authURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	currentTime := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	nowMu := sync.Mutex{}
	nowFunc := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return currentTime
	}
	advanceTime := func(d time.Duration) {
		nowMu.Lock()
		currentTime = currentTime.Add(d)
		nowMu.Unlock()
	}

	secrets := config.NewMemorySecretStore()
	mgr := NewOAuthManager(OAuthOptions{
		Secrets:     secrets,
		OpenBrowser: openBrowser,
		Now:         nowFunc,
	})

	serverID := "test-server-1"

	// 1. 完整 SignIn 之后 AccessToken 返回签发的 token、IsSignedIn 为 true
	ctx := context.Background()
	if err := mgr.SignIn(ctx, serverID, rsServer.URL); err != nil {
		t.Fatalf("SignIn failed: %v", err)
	}

	if !mgr.IsSignedIn(serverID) {
		t.Fatalf("expected IsSignedIn to be true")
	}

	token, err := mgr.AccessToken(ctx, serverID)
	if err != nil {
		t.Fatalf("AccessToken failed: %v", err)
	}
	if token != "mock-access-token-initial" {
		t.Fatalf("expected mock-access-token-initial, got %s", token)
	}

	// 2. 用注入的 Now 让 token 过期后 AccessToken 走刷新并拿到新 token
	advanceTime(3550 * time.Second) // 剩余 50 秒，不足 60 秒，触发刷新
	refreshedToken, err := mgr.AccessToken(ctx, serverID)
	if err != nil {
		t.Fatalf("AccessToken refresh failed: %v", err)
	}
	if refreshedToken != "mock-access-token-refreshed" {
		t.Fatalf("expected mock-access-token-refreshed, got %s", refreshedToken)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("expected 1 refresh call, got %d", refreshCalls.Load())
	}
	if got, _ := refreshResource.Load().(string); got != rsServer.URL {
		t.Fatalf("expected refresh request to carry resource %q, got %q", rsServer.URL, got)
	}

	// 3. 10 个 goroutine 并发调用 AccessToken 时 /token 的刷新只被请求一次
	refreshCalls.Store(0)
	advanceTime(3600 * time.Second) // 再次过期
	const numGoroutines = 10
	var wg sync.WaitGroup
	errs := make([]error, numGoroutines)
	tokens := make([]string, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tok, e := mgr.AccessToken(ctx, serverID)
			tokens[idx] = tok
			errs[idx] = e
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d failed: %v", i, e)
		}
		if tokens[i] != "mock-access-token-refreshed" {
			t.Fatalf("goroutine %d got unexpected token: %s", i, tokens[i])
		}
	}
	if calls := refreshCalls.Load(); calls != 1 {
		t.Fatalf("expected exactly 1 refresh call across %d goroutines, got %d", numGoroutines, calls)
	}

	// 4. state 不匹配时：回调只回 400 失败页面，登录流程不会被中止，
	// 直到 ctx 结束才以 ErrOAuthStateMismatch 返回。
	mu.Lock()
	mismatchFlag = true
	mu.Unlock()
	mismatchCtx, cancelMismatch := context.WithCancel(ctx)
	defer cancelMismatch()
	// openBrowser 内部已经等回调返回，此时回调一定处理完毕；直接取消 ctx 让 SignIn 结束，
	// 避免依赖固定睡眠或真的等满 5 分钟。
	mismatchStatus := 0
	mismatchMgr := NewOAuthManager(OAuthOptions{
		Secrets: secrets,
		OpenBrowser: func(authURL string) error {
			defer cancelMismatch()
			resp, err := http.Get(authURL)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			mismatchStatus = resp.StatusCode
			return nil
		},
		Now: nowFunc,
	})
	err = mismatchMgr.SignIn(mismatchCtx, "test-server-2", rsServer.URL)
	if mismatchStatus != http.StatusBadRequest {
		t.Fatalf("state 不匹配的回调应返回 400，实际 %d", mismatchStatus)
	}
	if !errors.Is(err, ErrOAuthStateMismatch) {
		t.Fatalf("expected ErrOAuthStateMismatch, got %v", err)
	}
	mu.Lock()
	mismatchFlag = false
	mu.Unlock()

	// 5. SignOut 之后返回 ErrOAuthNotSignedIn
	if err := mgr.SignOut(serverID); err != nil {
		t.Fatalf("SignOut failed: %v", err)
	}
	if mgr.IsSignedIn(serverID) {
		t.Fatalf("expected IsSignedIn to be false after SignOut")
	}
	_, err = mgr.AccessToken(ctx, serverID)
	if !errors.Is(err, ErrOAuthNotSignedIn) {
		t.Fatalf("expected ErrOAuthNotSignedIn after SignOut, got %v", err)
	}
}

// TestMCPOAuthRefreshInvalidGrant 验证刷新时上游判定 refresh token 失效按未登录处理，
// 其余刷新失败仍是 ErrOAuthTokenFailed。
func TestMCPOAuthRefreshInvalidGrant(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "400 invalid_grant", status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_description":"refresh token revoked"}`, wantErr: ErrOAuthNotSignedIn},
		{name: "401 invalid_grant", status: http.StatusUnauthorized, body: `{"error":"invalid_grant"}`, wantErr: ErrOAuthNotSignedIn},
		{name: "500 server error", status: http.StatusInternalServerError, body: `{"error":"server_error"}`, wantErr: ErrOAuthTokenFailed},
		{name: "400 invalid_client", status: http.StatusBadRequest, body: `{"error":"invalid_client"}`, wantErr: ErrOAuthTokenFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer tokenSrv.Close()

			serverID := "refresh-invalid-grant"
			secrets := config.NewMemorySecretStore()
			tok := storedToken{
				AccessToken:   "old-access-token",
				RefreshToken:  "old-refresh-token",
				ExpiresAt:     time.Now().Add(-time.Hour).Format(time.RFC3339),
				TokenEndpoint: tokenSrv.URL,
			}
			rawTok, err := json.Marshal(tok)
			if err != nil {
				t.Fatalf("marshal token: %v", err)
			}
			rawClient, err := json.Marshal(storedClient{ClientID: "client-id", TokenEndpoint: tokenSrv.URL})
			if err != nil {
				t.Fatalf("marshal client: %v", err)
			}
			if err := secrets.Set(tokenAccount(serverID), string(rawTok)); err != nil {
				t.Fatalf("save token: %v", err)
			}
			if err := secrets.Set(clientAccount(serverID), string(rawClient)); err != nil {
				t.Fatalf("save client: %v", err)
			}

			mgr := NewOAuthManager(OAuthOptions{Secrets: secrets})
			_, err = mgr.AccessToken(context.Background(), serverID)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if tc.wantErr == ErrOAuthTokenFailed && errors.Is(err, ErrOAuthNotSignedIn) {
				t.Fatalf("其余刷新失败不应被当成未登录：%v", err)
			}
		})
	}
}

// TestMCPOAuthSignInRequiresSecrets 验证未配置凭据存储时 SignIn 返回明确错误而非 panic。
func TestMCPOAuthSignInRequiresSecrets(t *testing.T) {
	mgr := NewOAuthManager(OAuthOptions{})
	err := mgr.SignIn(context.Background(), "srv-no-secrets", "http://127.0.0.1:1/mcp")
	if !errors.Is(err, ErrOAuthSecretsMissing) {
		t.Fatalf("expected ErrOAuthSecretsMissing, got %v", err)
	}
}

// newTestOAuthServers 起一组 httptest 授权服务器，供会话类用例复用。
// authorizeMode 决定 /authorize 的行为：normal 正常重定向回调，hang 完全不响应（模拟用户没操作）。
func newTestOAuthServers(t *testing.T, authorizeMode string) (rsURL string) {
	t.Helper()

	var asServer *httptest.Server
	asMux := http.NewServeMux()
	asServer = httptest.NewServer(asMux)
	t.Cleanup(asServer.Close)

	rsMux := http.NewServeMux()
	rsMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, asServer.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	rsServer := httptest.NewServer(rsMux)
	t.Cleanup(rsServer.Close)

	asMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              rsServer.URL,
			"authorization_servers": []string{asServer.URL},
		})
	})
	asMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 asServer.URL,
			"authorization_endpoint": asServer.URL + "/authorize",
			"token_endpoint":         asServer.URL + "/token",
			"registration_endpoint":  asServer.URL + "/register",
		})
	})
	asMux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "session-client-id"})
	})
	asMux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if authorizeMode == "hang" {
			// 模拟用户打开授权页后什么都没做：挂着直到请求被取消。
			<-r.Context().Done()
			return
		}
		target := fmt.Sprintf("%s?code=session-code&state=%s", q.Get("redirect_uri"), url.QueryEscape(q.Get("state")))
		http.Redirect(w, r, target, http.StatusFound)
	})
	asMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "session-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	return rsServer.URL
}

// TestMCPOAuthPendingAuthURL 验证登录会话表：进行中可以取出授权链接，结束后清空。
func TestMCPOAuthPendingAuthURL(t *testing.T) {
	rsURL := newTestOAuthServers(t, "hang")
	secrets := config.NewMemorySecretStore()

	seen := make(chan string, 1)
	release := make(chan struct{})
	mgr := NewOAuthManager(OAuthOptions{
		Secrets: secrets,
		OpenBrowser: func(authURL string) error {
			seen <- authURL
			<-release
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- mgr.SignIn(ctx, "pending-server", rsURL)
	}()

	// 打开浏览器这一步先拿到链接，此时登记应该已经就位。
	browserURL := <-seen
	authURL := mgr.PendingAuthURL("pending-server")
	if authURL == "" {
		t.Fatal("进行中的登录应能取出授权链接")
	}
	if authURL != browserURL {
		t.Fatalf("取出的链接应与打开浏览器时的相同")
	}
	if mgr.PendingAuthURL("other-server") != "" {
		t.Fatal("没有会话的服务应返回空串")
	}

	close(release)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
	}
	if mgr.PendingAuthURL("pending-server") != "" {
		t.Fatal("登录结束后应清空会话登记")
	}
}

// TestMCPOAuthCancelSignIn 验证 CancelSignIn 能中止等待，取消后登记被清空；无会话时返回 false。
func TestMCPOAuthCancelSignIn(t *testing.T) {
	rsURL := newTestOAuthServers(t, "hang")
	secrets := config.NewMemorySecretStore()

	opened := make(chan struct{}, 1)
	mgr := NewOAuthManager(OAuthOptions{
		Secrets: secrets,
		OpenBrowser: func(authURL string) error {
			opened <- struct{}{}
			return nil
		},
	})

	if mgr.CancelSignIn("never-started") {
		t.Fatal("没有进行中的登录时 CancelSignIn 应返回 false")
	}

	done := make(chan error, 1)
	go func() {
		done <- mgr.SignIn(context.Background(), "cancel-server", rsURL)
	}()

	<-opened
	if !mgr.CancelSignIn("cancel-server") {
		t.Fatal("进行中的登录应能被取消")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
	}
	if mgr.PendingAuthURL("cancel-server") != "" {
		t.Fatal("取消后应清空会话登记")
	}
}

// TestMCPOAuthOpenBrowserFailureKeepsWaiting 验证打开浏览器失败只记日志，
// 登录继续等待回调；用户手动打开 PendingAuthURL 给的链接就能完成登录。
func TestMCPOAuthOpenBrowserFailureKeepsWaiting(t *testing.T) {
	rsURL := newTestOAuthServers(t, "normal")
	secrets := config.NewMemorySecretStore()

	mgr := NewOAuthManager(OAuthOptions{
		Secrets: secrets,
		OpenBrowser: func(authURL string) error {
			return errors.New("no browser available")
		},
	})

	done := make(chan error, 1)
	go func() {
		done <- mgr.SignIn(context.Background(), "manual-server", rsURL)
	}()

	// 等授权链接登记就绪后，模拟用户手动打开它。
	var authURL string
	for i := 0; i < 200; i++ {
		if authURL = mgr.PendingAuthURL("manual-server"); authURL != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if authURL == "" {
		t.Fatal("打开浏览器失败后仍应能取出授权链接")
	}
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("手动打开授权链接失败：%v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if err := <-done; err != nil {
		t.Fatalf("打开浏览器失败不应让 SignIn 报错：%v", err)
	}
	if !mgr.IsSignedIn("manual-server") {
		t.Fatal("手动完成授权后应已登录")
	}
}

// TestMCPOAuthSignInWithoutOpenBrowser 验证未配置打开浏览器时继续等待回调，而不是直接报错。
func TestMCPOAuthSignInWithoutOpenBrowser(t *testing.T) {
	rsURL := newTestOAuthServers(t, "normal")
	secrets := config.NewMemorySecretStore()

	mgr := NewOAuthManager(OAuthOptions{Secrets: secrets})

	done := make(chan error, 1)
	go func() {
		done <- mgr.SignIn(context.Background(), "no-browser-server", rsURL)
	}()

	var authURL string
	for i := 0; i < 200; i++ {
		if authURL = mgr.PendingAuthURL("no-browser-server"); authURL != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if authURL == "" {
		t.Fatal("未配置打开浏览器时应能取出授权链接")
	}
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("手动打开授权链接失败：%v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if err := <-done; err != nil {
		t.Fatalf("未配置打开浏览器不应让 SignIn 报错：%v", err)
	}
}

// newMetadataOnlyAuthServer 起一个只发布授权服务器元数据的最小服务器。
// metadata 在服务器启动后生成，便于填写 asURL 相关的端点地址。
func newMetadataOnlyAuthServer(t *testing.T, metadata func(asURL string) map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"issuer": srv.URL}
		for k, v := range metadata(srv.URL) {
			body[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	return srv
}

func TestChooseAuthFlow(t *testing.T) {
	both := &authServerMetadata{
		AuthorizationEndpoint:       "https://as.example/authorize",
		DeviceAuthorizationEndpoint: "https://as.example/device",
	}
	deviceOnly := &authServerMetadata{DeviceAuthorizationEndpoint: "https://as.example/device"}
	authOnly := &authServerMetadata{AuthorizationEndpoint: "https://as.example/authorize"}

	cases := []struct {
		name string
		meta *authServerMetadata
		hint AuthHint
		want string
	}{
		{"显式设备流", both, AuthHint{Mode: AuthModeDevice}, AuthModeDevice},
		{"显式 loopback", both, AuthHint{Mode: AuthModeLoopback}, AuthModeLoopback},
		{"仅设备端点", deviceOnly, AuthHint{}, AuthModeDevice},
		{"两个端点都有", both, AuthHint{}, AuthModeLoopback},
		{"都没有", &authServerMetadata{}, AuthHint{}, ""},
		{"auto 等同未声明", authOnly, AuthHint{Mode: AuthModeAuto}, AuthModeLoopback},
		{"未知模式按 auto", deviceOnly, AuthHint{Mode: "pkce"}, AuthModeDevice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseAuthFlow(tc.meta, tc.hint); got != tc.want {
				t.Fatalf("chooseAuthFlow() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestOAuthDiscoverDeviceOnlyServer 验证只有设备端点的服务器能通过发现阶段。
func TestOAuthDiscoverDeviceOnlyServer(t *testing.T) {
	srv := newMetadataOnlyAuthServer(t, func(asURL string) map[string]any {
		return map[string]any{
			"token_endpoint":                        asURL + "/token",
			"device_authorization_endpoint":         asURL + "/device",
			"client_id_metadata_document_supported": true,
			"token_endpoint_auth_methods_supported": []string{"none"},
			"scopes_supported":                      []string{"read"},
		}
	})

	mgr := NewOAuthManager(OAuthOptions{Secrets: config.NewMemorySecretStore()})
	meta, err := mgr.discover(context.Background(), srv.URL+"/mcp")
	if err != nil {
		t.Fatalf("只有设备端点的服务器应通过发现：%v", err)
	}
	if meta.DeviceAuthorizationEndpoint != srv.URL+"/device" {
		t.Fatalf("device_authorization_endpoint = %q", meta.DeviceAuthorizationEndpoint)
	}
	if meta.AuthorizationEndpoint != "" {
		t.Fatalf("不应有 authorization_endpoint，实际 %q", meta.AuthorizationEndpoint)
	}
	if !meta.ClientIDMetadataDocumentSupported {
		t.Fatal("应保留 client_id_metadata_document_supported")
	}
	if got := chooseAuthFlow(meta, AuthHint{}); got != AuthModeDevice {
		t.Fatalf("只有设备端点时应选择设备流，实际 %q", got)
	}
}

// TestOAuthDiscoverMissingBothEndpoints 验证两个授权端点都缺失时按发现失败处理。
func TestOAuthDiscoverMissingBothEndpoints(t *testing.T) {
	srv := newMetadataOnlyAuthServer(t, func(asURL string) map[string]any {
		return map[string]any{"token_endpoint": asURL + "/token"}
	})

	mgr := NewOAuthManager(OAuthOptions{Secrets: config.NewMemorySecretStore()})
	if _, err := mgr.discover(context.Background(), srv.URL+"/mcp"); !errors.Is(err, ErrOAuthDiscoveryFailed) {
		t.Fatalf("缺少全部授权端点应返回 ErrOAuthDiscoveryFailed，实际 %v", err)
	}
}
