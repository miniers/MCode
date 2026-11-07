package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
)

// deviceFixture 是一个只发布设备端点的模拟授权服务器。
// 时间敏感用例通过 OAuthOptions.deviceInterval / deviceSlowDown 注入毫秒级参数，
// 服务器响应本身仍使用 RFC 的整秒字段。
type deviceFixture struct {
	as  *httptest.Server
	url string // 传给 SignInWithHint 的 MCP 服务器地址

	deviceCalls   atomic.Int64
	registerCalls atomic.Int64
	tokenCalls    atomic.Int64

	mu             sync.Mutex
	lastDeviceForm url.Values
	lastTokenForm  url.Values
	tokenArrivals  []time.Time
}

type deviceFixtureOptions struct {
	metadata func(asURL string) map[string]any
	device   func(asURL string) (int, map[string]any)
	register func(call int) (int, map[string]any)
	token    func(call int, form url.Values) (int, map[string]any)
}

func writeTestJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newDeviceFixture(t *testing.T, opts deviceFixtureOptions) *deviceFixture {
	t.Helper()
	f := &deviceFixture{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.as = srv
	f.url = srv.URL + "/mcp"

	metadata := opts.metadata
	if metadata == nil {
		metadata = func(asURL string) map[string]any {
			return map[string]any{
				"token_endpoint":                asURL + "/token",
				"device_authorization_endpoint": asURL + "/device",
				"registration_endpoint":         asURL + "/register",
			}
		}
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"issuer": srv.URL}
		for k, v := range metadata(srv.URL) {
			body[k] = v
		}
		writeTestJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		f.deviceCalls.Add(1)
		_ = r.ParseForm()
		f.mu.Lock()
		f.lastDeviceForm = r.PostForm
		f.mu.Unlock()

		status := http.StatusOK
		var body map[string]any
		if opts.device != nil {
			status, body = opts.device(srv.URL)
		}
		if body == nil {
			body = map[string]any{
				"device_code":      "device-code-1",
				"user_code":        "ABCD-EFGH",
				"verification_uri": srv.URL + "/verify",
				"expires_in":       60,
				"interval":         1,
			}
		}
		writeTestJSON(w, status, body)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		call := int(f.registerCalls.Add(1))
		if opts.register != nil {
			status, body := opts.register(call)
			writeTestJSON(w, status, body)
			return
		}
		writeTestJSON(w, http.StatusCreated, map[string]any{"client_id": "device-client-id"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		call := int(f.tokenCalls.Add(1))
		_ = r.ParseForm()
		f.mu.Lock()
		f.lastTokenForm = r.PostForm
		f.tokenArrivals = append(f.tokenArrivals, time.Now())
		f.mu.Unlock()

		if opts.token == nil {
			writeTestJSON(w, http.StatusOK, map[string]any{
				"access_token":  "device-access-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "device-refresh-token",
			})
			return
		}
		status, body := opts.token(call-1, r.PostForm)
		writeTestJSON(w, status, body)
	})
	return f
}

func (f *deviceFixture) deviceForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastDeviceForm
}

func (f *deviceFixture) tokenForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTokenForm
}

func (f *deviceFixture) arrivals() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.tokenArrivals...)
}

func newDeviceTestManager(t *testing.T, opts OAuthOptions) *OAuthManager {
	t.Helper()
	if opts.Secrets == nil {
		opts.Secrets = config.NewMemorySecretStore()
	}
	if opts.deviceInterval == 0 {
		opts.deviceInterval = 20 * time.Millisecond
	}
	if opts.deviceSlowDown == 0 {
		opts.deviceSlowDown = 40 * time.Millisecond
	}
	return NewOAuthManager(opts)
}

func TestDeviceFlowSuccess(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		token: func(call int, form url.Values) (int, map[string]any) {
			if call == 0 {
				return http.StatusBadRequest, map[string]any{"error": "authorization_pending"}
			}
			return http.StatusOK, map[string]any{
				"access_token":  "device-access-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "device-refresh-token",
				"scope":         "read",
			}
		},
	})
	var opened string
	mgr := newDeviceTestManager(t, OAuthOptions{OpenBrowser: func(u string) error {
		opened = u
		return nil
	}})
	if err := mgr.SignInWithHint(context.Background(), "device-success", f.url, AuthHint{}); err != nil {
		t.Fatalf("设备流登录失败：%v", err)
	}
	if !mgr.IsSignedIn("device-success") {
		t.Fatal("设备流成功后应已登录")
	}
	if got := mgr.PendingAuthURL("device-success"); got != "" {
		t.Fatalf("成功后不应残留授权链接，实际 %q", got)
	}
	if _, ok := mgr.PendingDeviceCode("device-success"); ok {
		t.Fatal("成功后不应残留设备码")
	}
	if opened != f.as.URL+"/verify" {
		t.Fatalf("打开浏览器地址 = %q", opened)
	}

	deviceForm := f.deviceForm()
	if deviceForm.Get("client_id") != "device-client-id" {
		t.Fatalf("设备授权 client_id = %q", deviceForm.Get("client_id"))
	}
	if deviceForm.Get("resource") != f.url {
		t.Fatalf("设备授权 resource = %q，期望 %q", deviceForm.Get("resource"), f.url)
	}

	tokenForm := f.tokenForm()
	if tokenForm.Get("grant_type") != deviceGrantType {
		t.Fatalf("grant_type = %q", tokenForm.Get("grant_type"))
	}
	if tokenForm.Get("device_code") != "device-code-1" {
		t.Fatalf("device_code = %q", tokenForm.Get("device_code"))
	}

	tok, err := mgr.loadToken("device-success")
	if err != nil {
		t.Fatalf("读取令牌失败：%v", err)
	}
	if tok.AccessToken != "device-access-token" || tok.RefreshToken != "device-refresh-token" {
		t.Fatalf("落库令牌不匹配：%+v", tok)
	}
	if tok.Scope != "read" {
		t.Fatalf("scope = %q", tok.Scope)
	}
	if f.registerCalls.Load() != 1 {
		t.Fatalf("DCR 注册次数 = %d，期望 1", f.registerCalls.Load())
	}
}

// TestDeviceFlowSlowDown 验证 slow_down 会累加轮询间隔。
func TestDeviceFlowSlowDown(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		token: func(call int, form url.Values) (int, map[string]any) {
			switch call {
			case 0, 1:
				return http.StatusBadRequest, map[string]any{"error": "slow_down"}
			default:
				return http.StatusOK, map[string]any{"access_token": "slow-token", "expires_in": 3600}
			}
		},
	})
	mgr := newDeviceTestManager(t, OAuthOptions{deviceInterval: 20 * time.Millisecond, deviceSlowDown: 40 * time.Millisecond})

	if err := mgr.SignInWithHint(context.Background(), "device-slow", f.url, AuthHint{}); err != nil {
		t.Fatalf("slow_down 后应最终成功：%v", err)
	}
	arr := f.arrivals()
	if len(arr) != 3 {
		t.Fatalf("轮询次数 = %d，期望 3", len(arr))
	}
	firstGap := arr[1].Sub(arr[0])
	secondGap := arr[2].Sub(arr[1])
	// 第一轮 20ms，slow_down 后 60ms，再 slow_down 后 100ms；留 10ms 调度抖动。
	if firstGap < 50*time.Millisecond {
		t.Fatalf("第一次 slow_down 后间隔 = %s，期望不少于 50ms", firstGap)
	}
	if secondGap < 90*time.Millisecond {
		t.Fatalf("第二次 slow_down 后间隔 = %s，期望不少于 90ms", secondGap)
	}
	if secondGap <= firstGap {
		t.Fatalf("间隔应递增，实际 %s -> %s", firstGap, secondGap)
	}
}

// TestDeviceFlowExpiredToken 验证 expired_token 失败、清理会话，并且可以再次登录。
func TestDeviceFlowExpiredToken(t *testing.T) {
	var expired atomic.Bool
	expired.Store(true)
	f := newDeviceFixture(t, deviceFixtureOptions{
		token: func(call int, form url.Values) (int, map[string]any) {
			if expired.Load() {
				return http.StatusBadRequest, map[string]any{"error": "expired_token"}
			}
			return http.StatusOK, map[string]any{"access_token": "recovered-token", "expires_in": 3600}
		},
	})
	mgr := newDeviceTestManager(t, OAuthOptions{})

	err := mgr.SignInWithHint(context.Background(), "device-expired", f.url, AuthHint{})
	if !errors.Is(err, ErrOAuthDeviceExpired) {
		t.Fatalf("期望 ErrOAuthDeviceExpired，实际 %v", err)
	}
	if _, ok := mgr.PendingDeviceCode("device-expired"); ok {
		t.Fatal("过期后不应残留设备码")
	}
	if _, err := mgr.AccessToken(context.Background(), "device-expired"); !errors.Is(err, ErrOAuthNotSignedIn) {
		t.Fatalf("失败后不应写入令牌，实际 %v", err)
	}

	expired.Store(false)
	if err := mgr.SignInWithHint(context.Background(), "device-expired", f.url, AuthHint{}); err != nil {
		t.Fatalf("过期后应允许重新登录：%v", err)
	}
	if !mgr.IsSignedIn("device-expired") {
		t.Fatal("重新登录后应已登录")
	}
}

func TestDeviceFlowAccessDenied(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		token: func(call int, form url.Values) (int, map[string]any) {
			return http.StatusBadRequest, map[string]any{"error": "access_denied", "error_description": "用户拒绝"}
		},
	})
	mgr := newDeviceTestManager(t, OAuthOptions{})

	err := mgr.SignInWithHint(context.Background(), "device-denied", f.url, AuthHint{})
	if !errors.Is(err, ErrOAuthAccessDenied) {
		t.Fatalf("期望 ErrOAuthAccessDenied，实际 %v", err)
	}
	if mgr.IsSignedIn("device-denied") {
		t.Fatal("拒绝授权后不应登录")
	}
}

// TestDeviceFlowAuthorizationPendingUntilTimeout 验证设备码到期后不再轮询并返回过期错误。
func TestDeviceFlowAuthorizationPendingUntilTimeout(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		device: func(asURL string) (int, map[string]any) {
			return http.StatusOK, map[string]any{
				"device_code":      "short-lived",
				"user_code":        "TIME-OUT",
				"verification_uri": asURL + "/verify",
				"expires_in":       1,
				"interval":         1,
			}
		},
		token: func(call int, form url.Values) (int, map[string]any) {
			return http.StatusBadRequest, map[string]any{"error": "authorization_pending"}
		},
	})
	mgr := newDeviceTestManager(t, OAuthOptions{})

	start := time.Now()
	err := mgr.SignInWithHint(context.Background(), "device-timeout", f.url, AuthHint{})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrOAuthDeviceExpired) {
		t.Fatalf("期望 ErrOAuthDeviceExpired，实际 %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("设备码 1s 过期，实际等待了 %s", elapsed)
	}
	if f.tokenCalls.Load() == 0 {
		t.Fatal("到期前应至少轮询一次")
	}
	if _, err := mgr.AccessToken(context.Background(), "device-timeout"); !errors.Is(err, ErrOAuthNotSignedIn) {
		t.Fatalf("超时后不应写入令牌，实际 %v", err)
	}
}

func TestDeviceFlowCancelSignIn(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		token: func(call int, form url.Values) (int, map[string]any) {
			return http.StatusBadRequest, map[string]any{"error": "authorization_pending"}
		},
	})
	started := make(chan struct{}, 1)
	mgr := newDeviceTestManager(t, OAuthOptions{
		deviceInterval: 50 * time.Millisecond,
		OpenBrowser: func(u string) error {
			select {
			case started <- struct{}{}:
			default:
			}
			return nil
		},
	})

	done := make(chan error, 1)
	go func() {
		done <- mgr.SignInWithHint(context.Background(), "device-cancel", f.url, AuthHint{})
	}()
	<-started

	mgr.CancelSignIn("device-cancel")
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
	}
	if _, ok := mgr.PendingDeviceCode("device-cancel"); ok {
		t.Fatal("取消后不应残留设备码")
	}
}

func TestDeviceFlowOpenBrowserUsesCompleteURI(t *testing.T) {
	cases := []struct {
		name     string
		complete string
		want     string
	}{
		{"优先完整地址", "/verify?user_code=ABCD-EFGH", "complete"},
		{"没有完整地址时用 verification_uri", "", "base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeviceFixture(t, deviceFixtureOptions{
				device: func(asURL string) (int, map[string]any) {
					body := map[string]any{
						"device_code":      "device-code-1",
						"user_code":        "ABCD-EFGH",
						"verification_uri": asURL + "/verify",
						"expires_in":       60,
						"interval":         1,
					}
					if tc.complete != "" {
						body["verification_uri_complete"] = asURL + tc.complete
					}
					return http.StatusOK, body
				},
			})
			var opened string
			mgr := newDeviceTestManager(t, OAuthOptions{OpenBrowser: func(u string) error {
				opened = u
				return nil
			}})
			if err := mgr.SignInWithHint(context.Background(), "device-complete", f.url, AuthHint{}); err != nil {
				t.Fatalf("登录失败：%v", err)
			}
			want := f.as.URL + "/verify"
			if tc.want == "complete" {
				want = f.as.URL + tc.complete
			}
			if opened != want {
				t.Fatalf("打开浏览器地址 = %q，期望 %q", opened, want)
			}
		})
	}
}

func TestDeviceFlowRejectsNonHTTPVerificationURI(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		device: func(asURL string) (int, map[string]any) {
			return http.StatusOK, map[string]any{
				"device_code":      "device-code-1",
				"user_code":        "BAD-URI",
				"verification_uri": "file:///tmp/verify",
				"expires_in":       60,
				"interval":         1,
			}
		},
	})
	mgr := newDeviceTestManager(t, OAuthOptions{})

	err := mgr.SignInWithHint(context.Background(), "device-bad-uri", f.url, AuthHint{})
	if !errors.Is(err, ErrOAuthDeviceFailed) {
		t.Fatalf("期望 ErrOAuthDeviceFailed，实际 %v", err)
	}
	if f.tokenCalls.Load() != 0 {
		t.Fatal("verification_uri 非法时不应开始轮询")
	}
}

// TestDeviceFlowUsesDirectoryDeviceEndpoint 验证目录覆盖的设备端点优先于元数据缺失。
func TestDeviceFlowUsesDirectoryDeviceEndpoint(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{
		metadata: func(asURL string) map[string]any {
			return map[string]any{
				"authorization_endpoint": asURL + "/authorize",
				"token_endpoint":         asURL + "/token",
				"registration_endpoint":  asURL + "/register",
			}
		},
	})
	mgr := newDeviceTestManager(t, OAuthOptions{DeviceAuthorizationURL: f.as.URL + "/device"})

	if err := mgr.SignInWithHint(context.Background(), "device-directory", f.url, AuthHint{Mode: AuthModeDevice}); err != nil {
		t.Fatalf("目录指定设备端点时登录失败：%v", err)
	}
	if f.deviceCalls.Load() != 1 {
		t.Fatalf("设备授权请求次数 = %d，期望 1", f.deviceCalls.Load())
	}
	if !mgr.IsSignedIn("device-directory") {
		t.Fatal("目录指定设备端点后应已登录")
	}
}

// TestDeviceFlowReusesTokenAccount 验证设备流令牌走既有存储路径，SignOut 能清理干净。
func TestDeviceFlowReusesTokenAccount(t *testing.T) {
	f := newDeviceFixture(t, deviceFixtureOptions{})
	secrets := config.NewMemorySecretStore()
	mgr := newDeviceTestManager(t, OAuthOptions{Secrets: secrets})

	if err := mgr.SignInWithHint(context.Background(), "device-store", f.url, AuthHint{}); err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if raw, err := secrets.Get(tokenAccount("device-store")); err != nil || raw == "" {
		t.Fatalf("令牌应写入 tokenAccount，实际 %q / %v", raw, err)
	}
	if raw, err := secrets.Get(clientAccount("device-store")); err != nil || raw == "" {
		t.Fatalf("客户端应写入 clientAccount，实际 %q / %v", raw, err)
	}

	if err := mgr.SignOut("device-store"); err != nil {
		t.Fatalf("SignOut 失败：%v", err)
	}
	if _, err := secrets.Get(tokenAccount("device-store")); err == nil {
		t.Fatal("SignOut 后不应残留令牌")
	}
	if _, err := secrets.Get(clientAccount("device-store")); err == nil {
		t.Fatal("SignOut 后不应残留客户端")
	}
}
