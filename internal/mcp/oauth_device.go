package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// deviceAuthorization 是 RFC 8628 §3.2 的设备授权响应。
type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

const (
	// deviceGrantType 是 RFC 8628 定义的设备码换令牌 grant_type。
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	defaultDeviceCodeLifetime = 600 * time.Second
	maxDeviceCodeLifetime     = 3600 * time.Second
	defaultDevicePollInterval = 5 * time.Second
	minDevicePollInterval     = time.Second
	maxDevicePollInterval     = 60 * time.Second
	defaultDeviceSlowDownStep = 5 * time.Second
)

// 轮询过程中的内部哨兵错误，不导出；设备流状态机用 errors.Is 区分后继续或结束。
var (
	errDeviceAuthorizationPending = errors.New("oauth: device authorization pending")
	errDeviceSlowDown             = errors.New("oauth: device slow_down")
	errDeviceExpiredToken         = errors.New("oauth: device token expired")
	errDeviceAccessDenied         = errors.New("oauth: device access denied")
)

// deviceSignIn 执行 RFC 8628 设备授权流程：申请设备码、把验证码登记到会话、轮询令牌。
// device_code 只留在本函数的局部变量里，不写会话快照，避免泄漏。
func (m *OAuthManager) deviceSignIn(ctx context.Context, serverID string, meta *authServerMetadata, client *storedClient, sess *signInSession, scope, resource string) error {
	endpoint := strings.TrimSpace(meta.DeviceAuthorizationEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(m.opts.DeviceAuthorizationURL)
	}
	if endpoint == "" {
		return fmt.Errorf("%w: server does not provide device_authorization_endpoint", ErrOAuthDeviceFailed)
	}

	logPrefix := signInLogPrefix(serverID)
	auth, err := m.deviceAuthorization(ctx, endpoint, client, scope, resource)
	if err != nil {
		log.Printf("%s设备授权请求失败: %v", logPrefix, err)
		return err
	}
	if auth.DeviceCode == "" || auth.UserCode == "" || auth.VerificationURI == "" {
		return fmt.Errorf("%w: device authorization response is missing device_code/user_code/verification_uri", ErrOAuthDeviceFailed)
	}
	if !isBrowserSafeURL(auth.VerificationURI) {
		return fmt.Errorf("%w: verification_uri is not an http(s) URL", ErrOAuthDeviceFailed)
	}
	complete := ""
	if auth.VerificationURIComplete != "" && isBrowserSafeURL(auth.VerificationURIComplete) {
		complete = auth.VerificationURIComplete
	}

	interval := clampDeviceInterval(auth.Interval)
	if m.opts.deviceInterval > 0 {
		// 测试注入：让时间敏感的用例用毫秒级参数，生产永远走服务器给出的整秒间隔。
		interval = m.opts.deviceInterval
	}
	deadline := m.now().Add(clampDeviceCodeLifetime(auth.ExpiresIn))

	m.setSignInDevice(serverID, sess, &pendingDeviceAuth{
		UserCode:        auth.UserCode,
		VerificationURI: auth.VerificationURI,
		ExpiresAt:       deadline,
	})
	browserURL := auth.VerificationURI
	if complete != "" {
		browserURL = complete
	}
	// 设备流没有授权链接，但旧 UI 仍读 PendingAuthURL；用验证地址兜底，避免显示空白。
	m.setSignInAuthURL(serverID, sess, browserURL)
	log.Printf("%s设备授权已发起 verification_uri=%s expires_in=%ds interval=%s", logPrefix, auth.VerificationURI, auth.ExpiresIn, interval)

	if m.opts.OpenBrowser == nil {
		log.Printf("%s未配置打开浏览器，等待用户在浏览器手动打开验证地址", logPrefix)
	} else if err := m.opts.OpenBrowser(browserURL); err != nil {
		log.Printf("%s打开浏览器失败: %v", logPrefix, err)
	} else {
		log.Printf("%s已打开浏览器", logPrefix)
	}

	tokenEndpoint := meta.TokenEndpoint
	if tokenEndpoint == "" {
		tokenEndpoint = client.TokenEndpoint
	}
	if tokenEndpoint == "" {
		return fmt.Errorf("%w: missing token_endpoint", ErrOAuthDeviceFailed)
	}

	for {
		if !m.now().Before(deadline) {
			return ErrOAuthDeviceExpired
		}
		// 最后一轮只等到设备码过期为止，避免在过期后还白打一次请求。
		wait := interval
		if remaining := deadline.Sub(m.now()); remaining < wait {
			wait = remaining
		}
		if wait < 0 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		tok, err := m.pollDeviceToken(ctx, tokenEndpoint, client, auth.DeviceCode, resource)
		if err == nil {
			if storeErr := m.storeTokenResponse(serverID, meta, tok, resource, scope); storeErr != nil {
				log.Printf("%s保存设备流令牌失败: %v", logPrefix, storeErr)
				return storeErr
			}
			log.Printf("%s设备流登录完成", logPrefix)
			return nil
		}
		switch {
		case errors.Is(err, errDeviceAuthorizationPending):
			// 用户还没在另一台设备上完成授权，保持间隔继续等。
		case errors.Is(err, errDeviceSlowDown):
			interval = m.slowDownInterval(interval)
			log.Printf("%s收到 slow_down，轮询间隔延长到 %s", logPrefix, interval)
		case errors.Is(err, errDeviceExpiredToken):
			return ErrOAuthDeviceExpired
		case errors.Is(err, errDeviceAccessDenied):
			return ErrOAuthAccessDenied
		default:
			// 其他错误（invalid_client/5xx/网络）立即失败，不重试。
			return err
		}
	}
}

// deviceAuthorization 发送 RFC 8628 §3.1 的设备授权请求。
func (m *OAuthManager) deviceAuthorization(ctx context.Context, endpoint string, client *storedClient, scope, resource string) (*deviceAuthorization, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid device authorization endpoint", ErrOAuthDeviceFailed)
	}

	form := url.Values{"client_id": {client.ClientID}}
	if client.ClientSecret != "" {
		form.Set("client_secret", client.ClientSecret)
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	if resource != "" {
		form.Set("resource", resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create device authorization request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOAuthDeviceFailed, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read device authorization response: %v", ErrOAuthDeviceFailed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %s", ErrOAuthDeviceFailed, oauthErrorText(body, resp.StatusCode))
	}

	var out deviceAuthorization
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: unmarshal device authorization response: %v", ErrOAuthDeviceFailed, err)
	}
	return &out, nil
}

// pollDeviceToken 发送一次设备码换令牌请求；authorization_pending/slow_down 等
// 用内部哨兵返回，由 deviceSignIn 的状态机决定继续等还是结束。
func (m *OAuthManager) pollDeviceToken(ctx context.Context, tokenEndpoint string, client *storedClient, deviceCode, resource string) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":  {deviceGrantType},
		"device_code": {deviceCode},
		"client_id":   {client.ClientID},
	}
	if client.ClientSecret != "" {
		form.Set("client_secret", client.ClientSecret)
	}
	if resource != "" {
		form.Set("resource", resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create device token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOAuthTokenFailed, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read device token response: %v", ErrOAuthTokenFailed, err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var tok tokenResponse
		if err := json.Unmarshal(body, &tok); err != nil {
			return nil, fmt.Errorf("%w: unmarshal device token response: %v", ErrOAuthTokenFailed, err)
		}
		if tok.AccessToken == "" {
			return nil, fmt.Errorf("%w: empty access_token", ErrOAuthTokenFailed)
		}
		return &tok, nil
	}

	var errResp struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &errResp)
	msg := errResp.Description
	if msg == "" {
		msg = errResp.Error
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	switch errResp.Error {
	case "authorization_pending":
		return nil, errDeviceAuthorizationPending
	case "slow_down":
		return nil, errDeviceSlowDown
	case "expired_token":
		return nil, errDeviceExpiredToken
	case "access_denied":
		return nil, errDeviceAccessDenied
	case "invalid_client", "unauthorized_client":
		return nil, fmt.Errorf("%w: %w", ErrOAuthClientRejected, &oauthClientRejectedError{
			Stage: "token",
			Code:  errResp.Error,
			Desc:  msg,
			CIMD:  client.AuthMethod == clientAuthMethodCIMD,
		})
	}
	return nil, fmt.Errorf("%w: %s", ErrOAuthTokenFailed, msg)
}

// oauthErrorText 从 OAuth 错误响应里取可读信息，取不到时退回 HTTP 状态码。
func oauthErrorText(body []byte, status int) string {
	var errResp struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &errResp)
	msg := errResp.Description
	if msg == "" {
		msg = errResp.Error
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	return msg
}

// isBrowserSafeURL 限制浏览器只能打开 http/https 地址，防止设备授权响应把用户带到本地协议。
func isBrowserSafeURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// clampDeviceCodeLifetime 应用方案 §2.2 的设备码有效期缺省值与上限。
func clampDeviceCodeLifetime(seconds int64) time.Duration {
	if seconds <= 0 {
		return defaultDeviceCodeLifetime
	}
	d := time.Duration(seconds) * time.Second
	if d > maxDeviceCodeLifetime {
		return maxDeviceCodeLifetime
	}
	return d
}

// clampDeviceInterval 应用方案 §2.2 的轮询间隔缺省值、下限与上限。
func clampDeviceInterval(seconds int64) time.Duration {
	if seconds <= 0 {
		return defaultDevicePollInterval
	}
	d := time.Duration(seconds) * time.Second
	if d < minDevicePollInterval {
		return minDevicePollInterval
	}
	if d > maxDevicePollInterval {
		return maxDevicePollInterval
	}
	return d
}

// slowDownInterval 按 RFC 8628 §3.5 把轮询间隔增加 5 秒（测试可注入更小增量），并守住上限。
func (m *OAuthManager) slowDownInterval(interval time.Duration) time.Duration {
	step := m.opts.deviceSlowDown
	if step <= 0 {
		step = defaultDeviceSlowDownStep
	}
	next := interval + step
	if next > maxDevicePollInterval {
		next = maxDevicePollInterval
	}
	return next
}
