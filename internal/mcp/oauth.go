package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/config"
)

var (
	// ErrOAuthNotSignedIn 尚未登录或 OAuth 凭据不存在。
	ErrOAuthNotSignedIn = errors.New("oauth: not signed in")
	// ErrOAuthDiscoveryFailed 授权服务或受保护资源元数据发现失败。
	ErrOAuthDiscoveryFailed = errors.New("oauth: discovery failed")
	// ErrOAuthRegistrationFailed RFC 7591 动态客户端注册失败。
	ErrOAuthRegistrationFailed = errors.New("oauth: client registration failed")
	// ErrOAuthStateMismatch 回调 state 与授权发起时不一致。
	ErrOAuthStateMismatch = errors.New("oauth: state mismatch")
	// ErrOAuthTimedOut OAuth 授权流程超时。
	ErrOAuthTimedOut = errors.New("oauth: timed out")
	// ErrOAuthCallbackFailed 回调接收失败或授权服务器返回错误。
	ErrOAuthCallbackFailed = errors.New("oauth: callback failed")
	// ErrOAuthTokenFailed 换取或刷新 Token 失败。
	ErrOAuthTokenFailed = errors.New("oauth: token request failed")
	// ErrOAuthSecretsMissing 未配置凭据存储，OAuth 登录结果无处保存。
	ErrOAuthSecretsMissing = errors.New("oauth: secrets store is not configured")
	// ErrOAuthDeviceFailed 设备授权请求或响应校验失败。
	ErrOAuthDeviceFailed = errors.New("oauth: device authorization request failed")
	// ErrOAuthDeviceExpired 设备码过期，需要重新发起登录。
	ErrOAuthDeviceExpired = errors.New("oauth: device code expired")
	// ErrOAuthAccessDenied 用户在授权端拒绝了本次授权。
	ErrOAuthAccessDenied = errors.New("oauth: authorization denied")
	// ErrOAuthClientRejected 授权服务器拒绝了客户端（注册、换取令牌或刷新时）。
	ErrOAuthClientRejected = errors.New("oauth: client rejected by authorization server")
	// ErrOAuthReauthRequired 登录已失效，必须重新走一次交互式授权。
	ErrOAuthReauthRequired = errors.New("oauth: re-authentication required")

	// errRegistrationEndpointMissing 表示服务器没有注册端点，CIMD 回退 DCR 时用来追加提示。
	errRegistrationEndpointMissing = errors.New("server does not provide registration_endpoint")
)

// oauthClientRejectedError 携带客户端被拒的阶段与错误码，不导出，调用方通过
// ErrOAuthClientRejected 与 errors.As 读取细节。
type oauthClientRejectedError struct {
	Stage string // registration / token / refresh
	Code  string // invalid_client / unauthorized_client / invalid_redirect_uri
	Desc  string
	CIMD  bool // 被拒的客户端是否来自 CIMD
}

func (e *oauthClientRejectedError) Error() string {
	stage := e.Stage
	if stage == "" {
		stage = "unknown"
	}
	msg := "client rejected at " + stage + ": " + e.Code
	if e.Desc != "" {
		msg += " (" + e.Desc + ")"
	}
	return msg
}

// OAuthOptions 包含 OAuth 管理器的配置项。
type OAuthOptions struct {
	Secrets      config.SecretStore
	HTTPClient   *http.Client
	OpenBrowser  func(authURL string) error
	CallbackHost string
	// CallbackPort 是 loopback 回调的固定端口；0 表示动态端口。
	// CIMD 的 redirect_uris 必须与客户端文档一致，所以只在使用 CIMD 时才需要固定端口。
	CallbackPort int
	ClientName   string
	// ClientMetadataURL 是 CIMD（Client ID Metadata Document）文档地址；空表示关闭 CIMD。
	ClientMetadataURL string
	// DeviceAuthorizationURL 是目录覆盖的设备授权端点，服务器元数据没发布设备端点时使用。
	DeviceAuthorizationURL string
	Now                    func() time.Time

	// deviceSlowDown 是 slow_down 时每次累加的间隔，测试可注入更小增量；未导出。
	deviceSlowDown time.Duration
	// deviceInterval 覆盖服务器给出的初始轮询间隔，测试可注入毫秒级值；未导出。
	deviceInterval time.Duration
}

type sfCall struct {
	wg  sync.WaitGroup
	val string
	err error
}

type singleflightGroup struct {
	mu sync.Mutex
	m  map[string]*sfCall
}

func (g *singleflightGroup) Do(key string, fn func() (string, error)) (string, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*sfCall)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := new(sfCall)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}

// OAuthManager 管理 MCP 服务的 OAuth 2.1 鉴权、刷新及凭据存储。
type OAuthManager struct {
	opts         OAuthOptions
	refreshGroup singleflightGroup

	// signInMu 保护 signIns：登录会话表，键为 serverID。
	// 浏览器没有自动打开时界面要靠它拿出授权链接给用户手动打开，也能取消进行中的登录。
	signInMu sync.Mutex
	signIns  map[string]*signInSession
}

// signInSession 记录一次进行中的登录会话。
type signInSession struct {
	authURL string
	device  *pendingDeviceAuth
	cancel  context.CancelFunc
}

// pendingDeviceAuth 是设备流期间会话里的设备码快照，device_code 不放在这里，避免泄漏。
type pendingDeviceAuth struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
}

// DeviceCodePrompt 是设备码的对外只读快照，UI 不应该持有内部指针。
type DeviceCodePrompt struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
}

// signInLogPrefix 返回登录日志前缀，只取 serverID 前 16 位，避免完整标识泄漏到日志。
func signInLogPrefix(serverID string) string {
	short := serverID
	if len(short) > 16 {
		short = short[:16]
	}
	return "mcp oauth[" + short + "]: "
}

// NewOAuthManager 创建并返回一个新的 OAuthManager 实例。
func NewOAuthManager(opts OAuthOptions) *OAuthManager {
	if opts.CallbackHost == "" {
		opts.CallbackHost = "127.0.0.1"
	}
	if opts.ClientName == "" {
		opts.ClientName = "mcode"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	return &OAuthManager{
		opts: opts,
	}
}

func (m *OAuthManager) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

func (m *OAuthManager) httpClient() *http.Client {
	if m.opts.HTTPClient != nil {
		return m.opts.HTTPClient
	}
	return http.DefaultClient
}

// PendingAuthURL 返回指定服务进行中登录的授权 URL，没有会话时返回空串。
// 浏览器没能自动打开时，界面可以用它把链接给用户手动打开。
func (m *OAuthManager) PendingAuthURL(serverID string) string {
	m.signInMu.Lock()
	defer m.signInMu.Unlock()
	sess, ok := m.signIns[serverID]
	if !ok {
		return ""
	}
	return sess.authURL
}

// CancelSignIn 取消指定服务进行中的登录，没有会话时返回 false。
func (m *OAuthManager) CancelSignIn(serverID string) bool {
	m.signInMu.Lock()
	sess, ok := m.signIns[serverID]
	m.signInMu.Unlock()
	if !ok {
		return false
	}
	sess.cancel()
	return true
}

// registerSignIn 登记（并覆盖）一个进行中的登录会话；同一服务重复登录时先取消旧会话。
func (m *OAuthManager) registerSignIn(serverID string, sess *signInSession) {
	m.signInMu.Lock()
	if m.signIns == nil {
		m.signIns = make(map[string]*signInSession)
	}
	old := m.signIns[serverID]
	m.signIns[serverID] = sess
	m.signInMu.Unlock()
	if old != nil {
		old.cancel()
	}
}

// setSignInAuthURL 写入已生成的授权链接，会话已被替换或移除时忽略。
func (m *OAuthManager) setSignInAuthURL(serverID string, sess *signInSession, authURL string) {
	m.signInMu.Lock()
	if cur, ok := m.signIns[serverID]; ok && cur == sess {
		cur.authURL = authURL
	}
	m.signInMu.Unlock()
}

// PendingDeviceCode 返回设备流进行中的验证码提示，没有设备流会话时 ok 为 false。
func (m *OAuthManager) PendingDeviceCode(serverID string) (DeviceCodePrompt, bool) {
	m.signInMu.Lock()
	defer m.signInMu.Unlock()
	sess, ok := m.signIns[serverID]
	if !ok || sess.device == nil {
		return DeviceCodePrompt{}, false
	}
	d := sess.device
	return DeviceCodePrompt{UserCode: d.UserCode, VerificationURI: d.VerificationURI, ExpiresAt: d.ExpiresAt}, true
}

// setSignInDevice 写入设备码快照，与 setSignInAuthURL 一样在会话已被移除时静默返回。
func (m *OAuthManager) setSignInDevice(serverID string, sess *signInSession, p *pendingDeviceAuth) {
	m.signInMu.Lock()
	if cur, ok := m.signIns[serverID]; ok && cur == sess {
		cur.device = p
	}
	m.signInMu.Unlock()
}

// unregisterSignIn 清除会话登记，只在当前登记的还是这一份时才删。
func (m *OAuthManager) unregisterSignIn(serverID string, sess *signInSession) {
	m.signInMu.Lock()
	if cur, ok := m.signIns[serverID]; ok && cur == sess {
		delete(m.signIns, serverID)
	}
	m.signInMu.Unlock()
}

type storedToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// Resource 为 RFC 8707 的资源标识（受保护资源元数据的 resource，缺省为 MCP 服务器地址）；
	// 刷新时要原样回传，按资源绑定令牌的授权服务器才不会拒绝。
	Resource      string `json:"resource,omitempty"`
	TokenEndpoint string `json:"token_endpoint"`
}

type storedClient struct {
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	RedirectURI   string `json:"redirect_uri,omitempty"`
	TokenEndpoint string `json:"token_endpoint"`
	// Flow 是注册时使用的登录方式（loopback/device）；旧记录没有该字段，按 loopback 处理。
	Flow string `json:"flow,omitempty"`
	// AuthMethod 是客户端来源（cimd/dcr）；旧记录没有该字段，按 dcr 处理。
	AuthMethod string `json:"auth_method,omitempty"`
}

type authServerMetadata struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	// DeviceAuthorizationEndpoint 是 RFC 8628 设备授权端点；只有设备流服务器才会发布。
	DeviceAuthorizationEndpoint string
	// ClientIDMetadataDocumentSupported 对应 CIMD 的 client_id_metadata_document_supported。
	ClientIDMetadataDocumentSupported bool
	TokenEndpointAuthMethodsSupported []string
	ScopesSupported                   []string
	// ResourceScopesSupported 来自受保护资源元数据的 scopes_supported。
	ResourceScopesSupported []string
	Resource                string
	Scope                   string
}

// tokenResponse 是授权码交换、设备流轮询和刷新共用的令牌响应。
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    *int64 `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

const (
	clientAuthMethodCIMD = "cimd"
	clientAuthMethodDCR  = "dcr"
)

func tokenAccount(serverID string) string {
	return "mcp-oauth:" + serverID + ":token"
}

func clientAccount(serverID string) string {
	return "mcp-oauth:" + serverID + ":client"
}

// cimdAccount 是「CIMD 不可用」标记的凭据账户；必须与 token/client 分开存放，
// 否则 token 阶段回退删除客户端记录时会把标记一起删掉。
func cimdAccount(serverID string) string {
	return "mcp-oauth:" + serverID + ":cimd"
}

// cimdUnavailable 记录某个服务器在 token 或 refresh 阶段拒绝过 CIMD 客户端。
type cimdUnavailable struct {
	DocumentURL string `json:"document_url"`
	Reason      string `json:"reason,omitempty"`
	At          string `json:"at,omitempty"`
}

func (m *OAuthManager) loadToken(serverID string) (storedToken, error) {
	if m.opts.Secrets == nil {
		return storedToken{}, ErrOAuthNotSignedIn
	}
	raw, err := m.opts.Secrets.Get(tokenAccount(serverID))
	if err != nil {
		if errors.Is(err, config.ErrSecretNotFound) || strings.Contains(err.Error(), "not found") {
			return storedToken{}, ErrOAuthNotSignedIn
		}
		return storedToken{}, fmt.Errorf("%w: %v", ErrOAuthNotSignedIn, err)
	}
	var tok storedToken
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return storedToken{}, ErrOAuthNotSignedIn
	}
	if tok.AccessToken == "" {
		return storedToken{}, ErrOAuthNotSignedIn
	}
	return tok, nil
}

func (m *OAuthManager) loadClient(serverID string) (storedClient, error) {
	if m.opts.Secrets == nil {
		return storedClient{}, ErrOAuthNotSignedIn
	}
	raw, err := m.opts.Secrets.Get(clientAccount(serverID))
	if err != nil {
		if errors.Is(err, config.ErrSecretNotFound) || strings.Contains(err.Error(), "not found") {
			return storedClient{}, ErrOAuthNotSignedIn
		}
		return storedClient{}, fmt.Errorf("%w: %v", ErrOAuthNotSignedIn, err)
	}
	var client storedClient
	if err := json.Unmarshal([]byte(raw), &client); err != nil {
		return storedClient{}, ErrOAuthNotSignedIn
	}
	return client, nil
}

// IsSignedIn 检查指定 serverID 是否已登录有效凭据。
func (m *OAuthManager) IsSignedIn(serverID string) bool {
	tok, err := m.loadToken(serverID)
	return err == nil && tok.AccessToken != ""
}

// SignOut 退出登录并删除指定 serverID 的 token 与 client 注册信息。
// CIMD 不可用标记故意保留：它记录的是服务器的能力边界，与登录状态无关。
func (m *OAuthManager) SignOut(serverID string) error {
	if m.opts.Secrets == nil {
		return nil
	}
	errToken := m.opts.Secrets.Delete(tokenAccount(serverID))
	errClient := m.opts.Secrets.Delete(clientAccount(serverID))
	if errToken != nil {
		return errToken
	}
	return errClient
}

func (m *OAuthManager) saveClient(serverID string, client *storedClient) error {
	if m.opts.Secrets == nil {
		return ErrOAuthSecretsMissing
	}
	raw, err := json.Marshal(client)
	if err != nil {
		return fmt.Errorf("marshal stored client: %w", err)
	}
	if err := m.opts.Secrets.Set(clientAccount(serverID), string(raw)); err != nil {
		return fmt.Errorf("save client registration: %w", err)
	}
	return nil
}

func (m *OAuthManager) deleteClient(serverID string) error {
	if m.opts.Secrets == nil {
		return nil
	}
	return m.opts.Secrets.Delete(clientAccount(serverID))
}

// cimdSupported 判断授权服务器是否声明支持 CIMD，且允许公共客户端（token_endpoint_auth_method=none）。
func cimdSupported(meta *authServerMetadata) bool {
	if meta == nil || !meta.ClientIDMetadataDocumentSupported {
		return false
	}
	for _, method := range meta.TokenEndpointAuthMethodsSupported {
		if strings.EqualFold(method, "none") {
			return true
		}
	}
	return false
}

// cimdUsable 汇总本次登录能否使用 CIMD：全局配置、服务器能力、目录提示与不可用标记。
func (m *OAuthManager) cimdUsable(serverID string, meta *authServerMetadata, hint AuthHint, allowCIMD bool) bool {
	if !allowCIMD || m.opts.ClientMetadataURL == "" || !cimdSupported(meta) {
		return false
	}
	// 目录声明的 clientId 不是我们的文档地址时，说明该服务器指定了别的客户端，不能用 CIMD。
	if hint.ClientID != "" && hint.ClientID != m.opts.ClientMetadataURL {
		return false
	}
	if _, marked := m.loadCIMDUnavailable(serverID); marked {
		return false
	}
	return true
}

// markCIMDUnavailable 写入 CIMD 不可用标记；token/refresh 阶段被拒时调用。
func (m *OAuthManager) markCIMDUnavailable(serverID, reason string) error {
	if m.opts.Secrets == nil {
		return ErrOAuthSecretsMissing
	}
	rec := cimdUnavailable{
		DocumentURL: m.opts.ClientMetadataURL,
		Reason:      reason,
		At:          m.now().Format(time.RFC3339),
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal cimd marker: %w", err)
	}
	return m.opts.Secrets.Set(cimdAccount(serverID), string(raw))
}

// loadCIMDUnavailable 读取不可用标记；文档地址与当前配置不一致时视为无标记并惰性删除，
// 这样换一个新的客户端文档地址后 CIMD 会重新启用。
func (m *OAuthManager) loadCIMDUnavailable(serverID string) (cimdUnavailable, bool) {
	if m.opts.Secrets == nil {
		return cimdUnavailable{}, false
	}
	raw, err := m.opts.Secrets.Get(cimdAccount(serverID))
	if err != nil {
		return cimdUnavailable{}, false
	}
	var rec cimdUnavailable
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return cimdUnavailable{}, false
	}
	if rec.DocumentURL == "" || rec.DocumentURL != m.opts.ClientMetadataURL {
		_ = m.opts.Secrets.Delete(cimdAccount(serverID))
		return cimdUnavailable{}, false
	}
	return rec, true
}

// ClearCIMDUnavailable 清除「CIMD 不可用」标记，供诊断或手动恢复 CIMD 使用。
func (m *OAuthManager) ClearCIMDUnavailable(serverID string) error {
	if m.opts.Secrets == nil {
		return nil
	}
	return m.opts.Secrets.Delete(cimdAccount(serverID))
}

// isClientRejected 判断注册阶段的错误是否属于「客户端不被接受」，该类错误可以回退 DCR。
func isClientRejected(err error) bool {
	var rejected *oauthClientRejectedError
	if !errors.As(err, &rejected) {
		return false
	}
	switch rejected.Code {
	case "invalid_client", "invalid_redirect_uri", "unauthorized_client":
		return true
	}
	return false
}

// isCIMDTokenRejected 判断错误是否只由 CIMD 客户端引起，只有这种情况才值得整体回退 DCR。
func isCIMDTokenRejected(err error) bool {
	if !errors.Is(err, ErrOAuthClientRejected) {
		return false
	}
	var rejected *oauthClientRejectedError
	if !errors.As(err, &rejected) {
		return false
	}
	return rejected.CIMD
}

// AccessToken 获取有效的 access token。若剩余有效期不足 60 秒且有 refresh_token，则执行刷新并保存。
func (m *OAuthManager) AccessToken(ctx context.Context, serverID string) (string, error) {
	tok, err := m.loadToken(serverID)
	if err != nil {
		return "", err
	}

	needsRefresh := false
	if tok.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, tok.ExpiresAt)
		if err == nil {
			if exp.Sub(m.now()) < 60*time.Second {
				needsRefresh = true
			}
		}
	}

	if !needsRefresh {
		return tok.AccessToken, nil
	}

	if tok.RefreshToken == "" {
		if tok.ExpiresAt != "" {
			exp, err := time.Parse(time.RFC3339, tok.ExpiresAt)
			if err == nil && m.now().After(exp) {
				return "", ErrOAuthNotSignedIn
			}
		}
		return tok.AccessToken, nil
	}

	return m.refreshGroup.Do(serverID, func() (string, error) {
		curTok, err := m.loadToken(serverID)
		if err == nil && curTok.ExpiresAt != "" {
			exp, err := time.Parse(time.RFC3339, curTok.ExpiresAt)
			if err == nil && exp.Sub(m.now()) >= 60*time.Second {
				return curTok.AccessToken, nil
			}
		}
		targetTok := tok
		if err == nil && curTok.AccessToken != "" {
			targetTok = curTok
		}
		return m.refreshToken(ctx, serverID, targetTok)
	})
}

func (m *OAuthManager) refreshToken(ctx context.Context, serverID string, tok storedToken) (string, error) {
	client, err := m.loadClient(serverID)
	if err != nil {
		return "", err
	}

	tokenEndpoint := tok.TokenEndpoint
	if tokenEndpoint == "" {
		tokenEndpoint = client.TokenEndpoint
	}
	if tokenEndpoint == "" {
		return "", fmt.Errorf("%w: missing token_endpoint for refresh", ErrOAuthTokenFailed)
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {client.ClientID},
	}
	if client.ClientSecret != "" {
		form.Set("client_secret", client.ClientSecret)
	}
	if tok.Resource != "" {
		form.Set("resource", tok.Resource)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOAuthTokenFailed, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		// refresh token 已失效（RFC 6749 invalid_grant）：按「未登录」处理而不是刷新失败，
		// 调用方会不带 token 继续请求，上游 401 后再提示用户重新登录。
		if (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) &&
			errResp.Error == "invalid_grant" {
			return "", fmt.Errorf("%w: refresh token 已失效: %s", ErrOAuthNotSignedIn, msg)
		}
		// 授权服务器不再认识这个客户端（CIMD 文档被撤下、DCR 客户端被删）：清掉令牌与客户端，
		// 让调用方重新走交互式授权；同时保留 ErrOAuthTokenFailed 以兼容旧的刷新失败语义。
		if errResp.Error == "invalid_client" || errResp.Error == "unauthorized_client" {
			if client.AuthMethod == clientAuthMethodCIMD {
				if markErr := m.markCIMDUnavailable(serverID, errResp.Error); markErr != nil {
					log.Printf("%s写入 CIMD 不可用标记失败: %v", signInLogPrefix(serverID), markErr)
				}
			}
			if delErr := m.opts.Secrets.Delete(tokenAccount(serverID)); delErr != nil {
				log.Printf("%s删除失效令牌失败: %v", signInLogPrefix(serverID), delErr)
			}
			if delErr := m.opts.Secrets.Delete(clientAccount(serverID)); delErr != nil {
				log.Printf("%s删除客户端记录失败: %v", signInLogPrefix(serverID), delErr)
			}
			return "", fmt.Errorf("%w: %w: %w", ErrOAuthReauthRequired, ErrOAuthTokenFailed, &oauthClientRejectedError{
				Stage: "refresh",
				Code:  errResp.Error,
				Desc:  msg,
				CIMD:  client.AuthMethod == clientAuthMethodCIMD,
			})
		}
		return "", fmt.Errorf("%w: %s", ErrOAuthTokenFailed, msg)
	}

	var tokResp tokenResponse
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return "", fmt.Errorf("unmarshal refresh response: %w", err)
	}
	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("%w: empty access_token in refresh response", ErrOAuthTokenFailed)
	}

	newRefreshToken := tokResp.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = tok.RefreshToken
	}

	var expiresAt string
	if tokResp.ExpiresIn != nil {
		expiresAt = m.now().Add(time.Duration(*tokResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	} else {
		expiresAt = tok.ExpiresAt
	}

	scope := tokResp.Scope
	if scope == "" {
		scope = tok.Scope
	}
	tokenType := tokResp.TokenType
	if tokenType == "" {
		tokenType = tok.TokenType
	}

	newTok := storedToken{
		AccessToken:   tokResp.AccessToken,
		RefreshToken:  newRefreshToken,
		ExpiresAt:     expiresAt,
		TokenType:     tokenType,
		Scope:         scope,
		Resource:      tok.Resource,
		TokenEndpoint: tokenEndpoint,
	}

	raw, err := json.Marshal(newTok)
	if err != nil {
		return "", fmt.Errorf("marshal refreshed token: %w", err)
	}
	if err := m.opts.Secrets.Set(tokenAccount(serverID), string(raw)); err != nil {
		return "", fmt.Errorf("save refreshed token: %w", err)
	}

	return newTok.AccessToken, nil
}

// SignIn 执行完整的 OAuth 登录流程，按服务器元数据自动选择授权码或设备流。
// 浏览器没能自动打开时不会直接失败：授权链接会登记在会话表里，调用方可用
// PendingAuthURL 取出让用户手动打开；用 CancelSignIn 可以取消正在等待的登录。
func (m *OAuthManager) SignIn(ctx context.Context, serverID, serverURL string) error {
	return m.SignInWithHint(ctx, serverID, serverURL, AuthHint{})
}

// SignInWithHint 按 hint 选择登录方式（授权码 + PKCE 或 RFC 8628 设备流）。
// CIMD 客户端在换取令牌时被授权服务器拒绝时，会写入不可用标记并用 DCR 重新登录一次；
// 第二次 attempt 不论成败都直接返回，不再重试，避免来回循环。
func (m *OAuthManager) SignInWithHint(ctx context.Context, serverID, serverURL string, hint AuthHint) error {
	err := m.signInAttempt(ctx, serverID, serverURL, hint, true)
	if err == nil || !isCIMDTokenRejected(err) {
		return err
	}

	var rejected *oauthClientRejectedError
	code := ""
	if errors.As(err, &rejected) {
		code = rejected.Code
	}
	if markErr := m.markCIMDUnavailable(serverID, code); markErr != nil {
		log.Printf("%s写入 CIMD 不可用标记失败: %v", signInLogPrefix(serverID), markErr)
	}
	if delErr := m.deleteClient(serverID); delErr != nil {
		log.Printf("%s删除 CIMD 客户端记录失败: %v", signInLogPrefix(serverID), delErr)
	}
	log.Printf("%sCIMD 客户端在换取令牌时被拒（%s），改用 DCR 重新登录一次", signInLogPrefix(serverID), code)

	retryErr := m.signInAttempt(ctx, serverID, serverURL, hint, false)
	if retryErr != nil && errors.Is(retryErr, errRegistrationEndpointMissing) {
		return fmt.Errorf("%w: CIMD rejected and server has no registration_endpoint", retryErr)
	}
	return retryErr
}

// signInAttempt 完成一次完整登录：发现元数据、选择流程、注册客户端，再走授权码或设备流。
func (m *OAuthManager) signInAttempt(ctx context.Context, serverID, serverURL string, hint AuthHint, allowCIMD bool) error {
	if m.opts.Secrets == nil {
		return ErrOAuthSecretsMissing
	}

	// 从入口就换上可取消的上下文，并登记会话：CancelSignIn 与「重复登录先取消旧的」都靠它。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess := &signInSession{cancel: cancel}
	m.registerSignIn(serverID, sess)
	defer m.unregisterSignIn(serverID, sess)

	logPrefix := signInLogPrefix(serverID)

	meta, err := m.discover(ctx, serverURL)
	if err != nil {
		log.Printf("%s发现授权服务失败: %v", logPrefix, err)
		return err
	}
	flow := chooseAuthFlow(meta, hint)
	if flow == "" {
		return fmt.Errorf("%w: server provides neither authorization_endpoint nor device_authorization_endpoint", ErrOAuthDiscoveryFailed)
	}
	// 只打端点，不打查询串。
	log.Printf("%s发现完成 issuer=%s flow=%s authorization_endpoint=%s device_authorization_endpoint=%s", logPrefix, meta.Issuer, flow, meta.AuthorizationEndpoint, meta.DeviceAuthorizationEndpoint)

	scope := meta.Scope
	if len(hint.Scopes) > 0 {
		scope = strings.Join(hint.Scopes, " ")
	}
	resource := meta.Resource
	if resource == "" {
		resource = serverURL
	}

	useCIMD := m.cimdUsable(serverID, meta, hint, allowCIMD)
	var redirectURI string
	// listener 在 loopback 分支内赋值，块外（srv.Serve）还要用，所以在块外声明。
	// 设备流在前面已经 return，走到 srv.Serve 时只可能是 loopback 流程。
	var listener net.Listener
	if flow == AuthModeLoopback {
		if useCIMD && m.opts.CallbackPort == 0 {
			// CIMD 文档里的 redirect_uris 必须与回调完全一致，没有固定端口就用不了。
			log.Printf("%s未配置 CallbackPort，CIMD 不可用，改用 DCR", logPrefix)
			useCIMD = false
		}
		listener, err = m.listenCallback(useCIMD)
		if err != nil && useCIMD {
			log.Printf("%s固定回调端口不可用（%v），退回 DCR + 动态端口", logPrefix, err)
			useCIMD = false
			listener, err = m.listenCallback(false)
		}
		if err != nil {
			return fmt.Errorf("callback listener: %w", err)
		}
		defer listener.Close()
		redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	}

	client, err := m.clientRegistration(ctx, serverID, meta, redirectURI, flow, hint, useCIMD)
	if err != nil && isClientRejected(err) {
		// 注册阶段被拒只回退 DCR，不写 CIMD 标记：此时还没走到 token，服务器可能只是
		// 不认识文档里的某个字段，不代表它会拒绝 CIMD。
		log.Printf("%s客户端注册被拒，改用 DCR 就地重试: %v", logPrefix, err)
		if delErr := m.deleteClient(serverID); delErr != nil {
			log.Printf("%s删除客户端记录失败: %v", logPrefix, delErr)
		}
		client, err = m.clientRegistration(ctx, serverID, meta, redirectURI, flow, hint, false)
	}
	if err != nil {
		log.Printf("%s客户端注册失败: %v", logPrefix, err)
		return err
	}
	log.Printf("%s客户端注册完成/复用 client_id=%s auth_method=%s", logPrefix, client.ClientID, client.AuthMethod)

	if flow == AuthModeDevice {
		return m.deviceSignIn(ctx, serverID, meta, client, sess, scope, resource)
	}

	verifier, err := randomToken(32)
	if err != nil {
		return fmt.Errorf("generate code verifier: %w", err)
	}
	challenge := pkceChallenge(verifier)
	state, err := randomToken(24)
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	authURL, err := buildAuthURL(meta.AuthorizationEndpoint, client.ClientID, redirectURI, challenge, state, scope, resource)
	if err != nil {
		return err
	}
	// 先登记再开浏览器：浏览器启动失败或用户手动打开时都能拿到链接。
	m.setSignInAuthURL(serverID, sess, authURL)

	type callbackRes struct {
		code string
		err  error
	}
	resultCh := make(chan callbackRes, 1)

	// stateMismatch 记录回调期间是否来过 state 不匹配的请求。
	// 这种请求只回失败页面、不写入 resultCh：本机任意进程错发一次就能把用户的登录流程搞停，
	// 所以继续等正确的回调；真要因超时/取消结束时再把这个原因报上去。
	var stateMismatch atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		// 先校验 state：state 不对的请求一律不进入后续处理，避免它冒充授权结果。
		if q.Get("state") != state {
			stateMismatch.Store(true)
			log.Printf("%s收到回调：state 不匹配", logPrefix)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(failureHTML("State 不匹配")))
			return
		}

		if errParam := q.Get("error"); errParam != "" {
			desc := q.Get("error_description")
			msg := errParam
			if desc != "" {
				msg = fmt.Sprintf("%s: %s", errParam, desc)
			}
			log.Printf("%s收到回调：授权服务器返回错误 error=%s", logPrefix, errParam)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(failureHTML(msg)))
			select {
			case resultCh <- callbackRes{err: fmt.Errorf("%w: %s", ErrOAuthCallbackFailed, msg)}:
			default:
			}
			return
		}

		code := q.Get("code")
		if code == "" {
			log.Printf("%s收到回调：缺少授权码", logPrefix)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(failureHTML("未收到授权码")))
			select {
			case resultCh <- callbackRes{err: fmt.Errorf("%w: missing code in callback", ErrOAuthCallbackFailed)}:
			default:
			}
			return
		}

		log.Printf("%s收到回调：成功", logPrefix)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(successHTML()))
		select {
		case resultCh <- callbackRes{code: code}:
		default:
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(listener)
	}()
	defer srv.Close()

	// 浏览器打不开时不能把登录流程直接结束：用户还可以用 PendingAuthURL 手动打开链接。
	if m.opts.OpenBrowser == nil {
		log.Printf("%s未配置打开浏览器，等待用户手动打开授权链接", logPrefix)
	} else if err := m.opts.OpenBrowser(authURL); err != nil {
		log.Printf("%s打开浏览器失败: %v", logPrefix, err)
	} else {
		log.Printf("%s已打开浏览器", logPrefix)
	}

	var code string
	select {
	case <-ctx.Done():
		// 期间出现过 state 不匹配时报出真正的结束原因，方便用户理解为什么没登录成功。
		if stateMismatch.Load() {
			return ErrOAuthStateMismatch
		}
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		if stateMismatch.Load() {
			return ErrOAuthStateMismatch
		}
		return ErrOAuthTimedOut
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		code = res.code
	}

	if err := m.exchangeCode(ctx, serverID, meta, client, code, redirectURI, verifier, resource); err != nil {
		log.Printf("%s换取令牌失败: %v", logPrefix, err)
		return err
	}
	log.Printf("%s换取令牌完成", logPrefix)
	return nil
}

// listenCallback 监听回路回调端口：CIMD 用固定端口（文档里的 redirect_uris 必须一致），
// 否则让系统分配动态端口，保持既有行为。
func (m *OAuthManager) listenCallback(fixedPort bool) (net.Listener, error) {
	host := m.opts.CallbackHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := 0
	if fixedPort {
		port = m.opts.CallbackPort
	}
	return net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

func (m *OAuthManager) discover(ctx context.Context, serverURL string) (*authServerMetadata, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid server URL %s", ErrOAuthDiscoveryFailed, serverURL)
	}

	metadataURL, challengeScope := m.protectedResourceMetadata(ctx, serverURL)

	var issuer, resource string
	var resourceScopes []string

	if metadataURL != "" {
		iss, res, scopes, err := m.fetchResourceMetadata(ctx, metadataURL)
		if err == nil {
			issuer = iss
			resource = res
			resourceScopes = scopes
		}
	}

	if issuer == "" {
		origin, err := originOf(serverURL)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOAuthDiscoveryFailed, err)
		}
		path := u.Path
		if path == "/" {
			path = ""
		}
		var candidates []string
		if path != "" {
			candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+path)
		}
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")

		for _, cand := range candidates {
			iss, res, scopes, err := m.fetchResourceMetadata(ctx, cand)
			if err == nil && (iss != "" || res != "") {
				issuer = iss
				resource = res
				resourceScopes = scopes
				break
			}
		}
	}

	if issuer == "" {
		origin, err := originOf(serverURL)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOAuthDiscoveryFailed, err)
		}
		issuer = origin
	}

	issuer = strings.TrimRight(issuer, "/")
	meta, err := m.fetchAuthServerMetadata(ctx, issuer)
	if err != nil {
		return nil, err
	}

	if resource != "" {
		meta.Resource = resource
	} else {
		meta.Resource = serverURL
	}
	meta.ResourceScopesSupported = resourceScopes

	if challengeScope != "" {
		meta.Scope = challengeScope
	} else if len(resourceScopes) > 0 {
		meta.Scope = strings.Join(resourceScopes, " ")
	}

	return meta, nil
}

func (m *OAuthManager) protectedResourceMetadata(ctx context.Context, serverURL string) (metadataURL string, challengeScope string) {
	initPayload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"mcode","version":"1.0.0"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewReader(initPayload))
	if err != nil {
		return "", ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		return "", ""
	}

	authHeaders := resp.Header.Values("WWW-Authenticate")
	for _, h := range authHeaders {
		if metadataURL == "" {
			if found := parseChallengeParam("resource_metadata", h); found != "" {
				metadataURL = resolveURL(serverURL, found)
			}
		}
		if challengeScope == "" {
			if found := parseChallengeParam("scope", h); found != "" {
				challengeScope = found
			}
		}
	}
	return metadataURL, challengeScope
}

func (m *OAuthManager) fetchResourceMetadata(ctx context.Context, targetURL string) (string, string, []string, error) {
	var body struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := m.fetchJSON(ctx, targetURL, &body); err != nil {
		return "", "", nil, err
	}
	var issuer string
	if len(body.AuthorizationServers) > 0 {
		issuer = body.AuthorizationServers[0]
	}
	return issuer, body.Resource, body.ScopesSupported, nil
}

func (m *OAuthManager) fetchAuthServerMetadata(ctx context.Context, issuer string) (*authServerMetadata, error) {
	base := strings.TrimRight(issuer, "/")
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid issuer %s", ErrOAuthDiscoveryFailed, base)
	}
	origin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	path := strings.TrimRight(u.Path, "/")
	if path == "/" {
		path = ""
	}

	var candidates []string
	if path != "" {
		candidates = append(candidates,
			origin+"/.well-known/oauth-authorization-server"+path,
			origin+"/.well-known/openid-configuration"+path,
		)
	}
	candidates = append(candidates,
		base+"/.well-known/oauth-authorization-server",
		base+"/.well-known/openid-configuration",
	)

	var lastErr error
	for _, cand := range candidates {
		var resp struct {
			AuthorizationEndpoint string   `json:"authorization_endpoint"`
			TokenEndpoint         string   `json:"token_endpoint"`
			RegistrationEndpoint  string   `json:"registration_endpoint"`
			ScopesSupported       []string `json:"scopes_supported"`
			// 以下字段用于设备流与 CIMD 能力判定。
			DeviceAuthorizationEndpoint       string   `json:"device_authorization_endpoint"`
			ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
			TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
		}
		if err := m.fetchJSON(ctx, cand, &resp); err != nil {
			lastErr = err
			continue
		}
		if resp.TokenEndpoint == "" {
			lastErr = fmt.Errorf("missing token_endpoint in %s", cand)
			continue
		}
		// 允许只有设备端点的服务器通过发现；两个授权端点都没有才是真正的发现失败。
		if resp.AuthorizationEndpoint == "" && resp.DeviceAuthorizationEndpoint == "" {
			lastErr = fmt.Errorf("missing authorization_endpoint or device_authorization_endpoint in %s", cand)
			continue
		}
		meta := &authServerMetadata{
			Issuer:                            base,
			AuthorizationEndpoint:             resp.AuthorizationEndpoint,
			TokenEndpoint:                     resp.TokenEndpoint,
			RegistrationEndpoint:              resp.RegistrationEndpoint,
			DeviceAuthorizationEndpoint:       resp.DeviceAuthorizationEndpoint,
			ClientIDMetadataDocumentSupported: resp.ClientIDMetadataDocumentSupported,
			TokenEndpointAuthMethodsSupported: resp.TokenEndpointAuthMethodsSupported,
			ScopesSupported:                   resp.ScopesSupported,
		}
		if len(resp.ScopesSupported) > 0 {
			meta.Scope = strings.Join(resp.ScopesSupported, " ")
		}
		return meta, nil
	}
	return nil, fmt.Errorf("%w: could not discover auth server metadata for %s (last error: %v)", ErrOAuthDiscoveryFailed, base, lastErr)
}

// clientRegistration 选择本次使用的客户端：优先 CIMD（client_id 就是元数据文档地址），
// 否则复用同 Flow + RedirectURI 的旧注册，再否则走动态客户端注册（DCR）。
func (m *OAuthManager) clientRegistration(ctx context.Context, serverID string, meta *authServerMetadata, redirectURI, flow string, hint AuthHint, allowCIMD bool) (*storedClient, error) {
	if allowCIMD && cimdSupported(meta) && m.opts.ClientMetadataURL != "" &&
		(hint.ClientID == "" || hint.ClientID == m.opts.ClientMetadataURL) {
		client := &storedClient{
			ClientID:      m.opts.ClientMetadataURL,
			RedirectURI:   redirectURI,
			TokenEndpoint: meta.TokenEndpoint,
			Flow:          flow,
			AuthMethod:    clientAuthMethodCIMD,
		}
		if err := m.saveClient(serverID, client); err != nil {
			return nil, err
		}
		return client, nil
	}

	existing, err := m.loadClient(serverID)
	if err == nil && existing.ClientID != "" && existing.RedirectURI == redirectURI {
		existingFlow := existing.Flow
		if existingFlow == "" {
			// 旧记录没有 Flow 字段，按 loopback 处理。
			existingFlow = AuthModeLoopback
		}
		// 本次不允许 CIMD 时，旧的 CIMD 客户端记录作废，必须重新注册 DCR 客户端。
		if existingFlow == flow && !(existing.AuthMethod == clientAuthMethodCIMD && !allowCIMD) {
			return &existing, nil
		}
	}

	if meta.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("%w: %w", ErrOAuthRegistrationFailed, errRegistrationEndpointMissing)
	}

	clientName := m.opts.ClientName
	if clientName == "" {
		clientName = "mcode"
	}

	regBody := map[string]any{
		"client_name":                clientName,
		"token_endpoint_auth_method": "none",
	}
	if flow == AuthModeDevice {
		regBody["grant_types"] = []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"}
	} else {
		regBody["redirect_uris"] = []string{redirectURI}
		regBody["grant_types"] = []string{"authorization_code", "refresh_token"}
		regBody["response_types"] = []string{"code"}
	}
	if meta.Scope != "" {
		regBody["scope"] = meta.Scope
	}

	bodyBytes, err := json.Marshal(regBody)
	if err != nil {
		return nil, fmt.Errorf("marshal registration body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOAuthRegistrationFailed, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read registration response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		msg := errResp.Description
		if msg == "" {
			msg = errResp.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		// 只有「客户端不被接受」类错误才值得换一种注册方式重试，其余直接失败。
		if errResp.Error == "invalid_client" || errResp.Error == "invalid_redirect_uri" || errResp.Error == "unauthorized_client" {
			return nil, fmt.Errorf("%w: %w: %w", ErrOAuthRegistrationFailed, ErrOAuthClientRejected, &oauthClientRejectedError{
				Stage: "registration",
				Code:  errResp.Error,
				Desc:  msg,
			})
		}
		return nil, fmt.Errorf("%w: %s", ErrOAuthRegistrationFailed, msg)
	}

	var regResp struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return nil, fmt.Errorf("unmarshal registration response: %w", err)
	}
	if regResp.ClientID == "" {
		return nil, fmt.Errorf("%w: client_id is empty", ErrOAuthRegistrationFailed)
	}

	sc := storedClient{
		ClientID:      regResp.ClientID,
		ClientSecret:  regResp.ClientSecret,
		RedirectURI:   redirectURI,
		TokenEndpoint: meta.TokenEndpoint,
		Flow:          flow,
		AuthMethod:    clientAuthMethodDCR,
	}
	if err := m.saveClient(serverID, &sc); err != nil {
		return nil, err
	}

	return &sc, nil
}

func (m *OAuthManager) exchangeCode(ctx context.Context, serverID string, meta *authServerMetadata, client *storedClient, code, redirectURI, verifier, resource string) error {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {client.ClientID},
		"code_verifier": {verifier},
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	if client.ClientSecret != "" {
		form.Set("client_secret", client.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOAuthTokenFailed, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		msg := errResp.Description
		if msg == "" {
			msg = errResp.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		// 只有 400/401 且明确是客户端不被接受时，才留给 SignInWithHint 决定是否回退 DCR。
		if (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) &&
			(errResp.Error == "invalid_client" || errResp.Error == "unauthorized_client") {
			return fmt.Errorf("%w: %w: %w", ErrOAuthTokenFailed, ErrOAuthClientRejected, &oauthClientRejectedError{
				Stage: "token",
				Code:  errResp.Error,
				Desc:  msg,
				CIMD:  client.AuthMethod == clientAuthMethodCIMD,
			})
		}
		return fmt.Errorf("%w: %s", ErrOAuthTokenFailed, msg)
	}

	var tokResp tokenResponse
	if err := json.Unmarshal(respBody, &tokResp); err != nil {
		return fmt.Errorf("unmarshal token response: %w", err)
	}
	return m.storeTokenResponse(serverID, meta, &tokResp, resource, meta.Scope)
}

// storeTokenResponse 把令牌响应落库；授权码交换与设备流共用，保证两条路径写出的记录一致。
func (m *OAuthManager) storeTokenResponse(serverID string, meta *authServerMetadata, tokResp *tokenResponse, resource, fallbackScope string) error {
	if tokResp.AccessToken == "" {
		return fmt.Errorf("%w: empty access_token", ErrOAuthTokenFailed)
	}

	var expiresAt string
	if tokResp.ExpiresIn != nil {
		expiresAt = m.now().Add(time.Duration(*tokResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	scope := tokResp.Scope
	if scope == "" {
		scope = fallbackScope
	}

	st := storedToken{
		AccessToken:   tokResp.AccessToken,
		RefreshToken:  tokResp.RefreshToken,
		ExpiresAt:     expiresAt,
		TokenType:     tokResp.TokenType,
		Scope:         scope,
		Resource:      resource,
		TokenEndpoint: meta.TokenEndpoint,
	}

	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal stored token: %w", err)
	}
	if err := m.opts.Secrets.Set(tokenAccount(serverID), string(raw)); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	return nil
}

func (m *OAuthManager) fetchJSON(ctx context.Context, targetURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func buildAuthURL(authEndpoint, clientID, redirectURI, challenge, state, scope, resource string) (string, error) {
	u, err := url.Parse(authEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if scope != "" {
		q.Set("scope", scope)
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseChallengeParam(name, header string) string {
	idx := strings.Index(header, name+"=")
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(header[idx+len(name)+1:])
	if strings.HasPrefix(rest, "\"") {
		inner := rest[1:]
		end := strings.Index(inner, "\"")
		if end == -1 {
			return ""
		}
		return inner[:end]
	}
	end := strings.IndexAny(rest, ", \t\r\n")
	if end == -1 {
		return rest
	}
	return rest[:end]
}

func resolveURL(baseStr, refStr string) string {
	baseURL, err := url.Parse(baseStr)
	if err != nil {
		return refStr
	}
	refURL, err := url.Parse(refStr)
	if err != nil {
		return refStr
	}
	return baseURL.ResolveReference(refURL).String()
}

func originOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid URL: %s", rawURL)
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host), nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func successHTML() string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>授权成功 - MCode</title>
<style>
* { box-sizing: border-box; }
body {
  margin: 0; padding: 0;
  background: #0B0B0F; color: #F5F5F7;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  min-height: 100vh;
  display: flex; align-items: center; justify-content: center;
}
.card {
  width: 420px; max-width: calc(100vw - 48px); padding: 40px;
  border-radius: 20px; background: rgba(255, 255, 255, 0.06);
  text-align: center; box-shadow: 0 8px 32px rgba(0,0,0,0.37);
}
h1 { font-size: 22px; font-weight: 600; margin: 16px 0 8px; }
p { font-size: 14px; color: rgba(255, 255, 255, 0.65); line-height: 1.5; margin: 0 0 16px; }
.wordmark { font-size: 12px; color: rgba(255, 255, 255, 0.35); letter-spacing: 0.05em; }
</style>
</head>
<body>
<div class="card">
<h1>授权成功</h1>
<p>您可以关闭此标签页并返回 MCode。</p>
<div class="wordmark">MCode</div>
</div>
<script>setTimeout(function(){ window.close(); }, 1500);</script>
</body>
</html>`
}

func failureHTML(message string) string {
	escaped := html.EscapeString(message)
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>授权失败 - MCode</title>
<style>
* { box-sizing: border-box; }
body {
  margin: 0; padding: 0;
  background: #0B0B0F; color: #F5F5F7;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  min-height: 100vh;
  display: flex; align-items: center; justify-content: center;
}
.card {
  width: 420px; max-width: calc(100vw - 48px); padding: 40px;
  border-radius: 20px; background: rgba(255, 255, 255, 0.06);
  text-align: center; box-shadow: 0 8px 32px rgba(0,0,0,0.37);
}
h1 { font-size: 22px; font-weight: 600; margin: 16px 0 8px; color: #ff6b6b; }
p { font-size: 14px; color: rgba(255, 255, 255, 0.65); line-height: 1.5; margin: 0 0 16px; word-break: break-all; }
.wordmark { font-size: 12px; color: rgba(255, 255, 255, 0.35); letter-spacing: 0.05em; }
</style>
</head>
<body>
<div class="card">
<h1>授权未完成</h1>
<p>%s</p>
<div class="wordmark">MCode</div>
</div>
</body>
</html>`, escaped)
}
