package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"mcode/internal/procattr"
)

// Antigravity 官方客户端 OAuth 凭据常量。
const (
	OfficialClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	OfficialClientSecret = "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"

	DefaultAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL    = "https://oauth2.googleapis.com/token"
	DefaultUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

	OAuthScopes = "openid https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs"
)

// OAuthEndpoints 包含可注入的 OAuth 端点配置。
type OAuthEndpoints struct {
	AuthURL     string
	TokenURL    string
	UserInfoURL string
}

func (e OAuthEndpoints) withDefaults() OAuthEndpoints {
	if e.AuthURL == "" {
		e.AuthURL = DefaultAuthURL
	}
	if e.TokenURL == "" {
		e.TokenURL = DefaultTokenURL
	}
	if e.UserInfoURL == "" {
		e.UserInfoURL = DefaultUserInfoURL
	}
	return e
}

type accountResult struct {
	account Account
	err     error
}

// LoginSession 封装单次登录授权的生命周期与交互通道。
type LoginSession struct {
	manager     *Manager
	authURL     string
	state       string
	verifier    string
	redirectURI string
	cancel      context.CancelFunc
	sessionCtx  context.Context
	server      *http.Server
	resChan     chan accountResult
	once        sync.Once
}

// URL 返回浏览器打开的授权 URL。
func (s *LoginSession) URL() string {
	return s.authURL
}

// Cancel 主动取消当前登录流程并关闭本地监听服务器。
func (s *LoginSession) Cancel() {
	s.cancel()
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
	}
}

// Wait 阻塞等待登录结果或外部 context 取消。
func (s *LoginSession) Wait(ctx context.Context) (Account, error) {
	defer s.Cancel()
	select {
	case res := <-s.resChan:
		return res.account, res.err
	case <-ctx.Done():
		s.Cancel()
		return Account{}, ctx.Err()
	case <-s.sessionCtx.Done():
		s.Cancel()
		if errors.Is(s.sessionCtx.Err(), context.Canceled) {
			return Account{}, context.Canceled
		}
		return Account{}, errors.New("oauth login timeout (180s)")
	}
}

// Submit 支持通过备用通道手动提交授权码或完整回调 URL。
func (s *LoginSession) Submit(input string) error {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return errors.New("empty auth input")
	}

	code := raw
	if strings.Contains(raw, "?") || strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return errors.New("invalid callback URL")
		}
		q := u.Query()
		if q.Get("state") != s.state {
			return errors.New("state mismatch")
		}
		code = q.Get("code")
		if code == "" {
			return errors.New("missing authorization code")
		}
	}

	go s.exchangeAndFinish(code)
	return nil
}

// exchangeAndFinish 兑换 token 并完成合并，仅执行一次。
func (s *LoginSession) exchangeAndFinish(code string) {
	s.once.Do(func() {
		acc, err := s.manager.exchangeTokenAndSave(s.sessionCtx, code, s.verifier, s.redirectURI)
		s.resChan <- accountResult{account: acc, err: err}
		if s.server != nil {
			go func() {
				time.Sleep(500 * time.Millisecond)
				_ = s.server.Close()
			}()
		}
	})
}

// LoginStart 启动本地 OAuth 回调监听并拉起浏览器，整体超时 180 秒。
func (m *Manager) LoginStart(ctx context.Context) (*LoginSession, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen local port: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/oauth-callback", port)

	// 生成 PKCE 验证码与挑战码（S256）
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("generate verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	// 生成随机 state
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("generate state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	endpoints := m.opts.Endpoints.withDefaults()
	q := url.Values{}
	q.Set("client_id", OfficialClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", OAuthScopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")

	authURL := endpoints.AuthURL + "?" + q.Encode()

	sessionCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	session := &LoginSession{
		manager:     m,
		authURL:     authURL,
		state:       state,
		verifier:    verifier,
		redirectURI: redirectURI,
		cancel:      cancel,
		sessionCtx:  sessionCtx,
		resChan:     make(chan accountResult, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		reqState := r.URL.Query().Get("state")
		reqCode := r.URL.Query().Get("code")
		reqErr := r.URL.Query().Get("error")

		if reqState != session.state {
			http.Error(w, "Security validation failed: State mismatch", http.StatusBadRequest)
			return
		}

		if reqErr != "" {
			http.Error(w, "OAuth authorization failed", http.StatusBadRequest)
			session.once.Do(func() {
				session.resChan <- accountResult{err: errors.New("oauth authorization denied")}
			})
			return
		}
		if reqCode == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body style=\"font-family:sans-serif;padding:30px;\"><h2>登录成功</h2><p>已成功获取授权，可以关闭此窗口并返回 MCode。</p></body></html>"))

		go session.exchangeAndFinish(reqCode)
	})

	server := &http.Server{Handler: mux}
	session.server = server

	go func() {
		_ = server.Serve(listener)
	}()
	go func() {
		<-sessionCtx.Done()
		_ = server.Close()
	}()

	// 通过 Opener 打开浏览器
	opener := m.opts.Opener
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(authURL); err != nil {
		// 浏览器打开失败不阻塞会话，用户可通过 URL() 与 Submit() 备用通道手动完成
		_ = err
	}

	return session, nil
}

// openBrowser 按平台选择打开 URL 的系统命令；procattr.HideWindow 避免 Windows 上闪出控制台窗口。
func openBrowser(u string) error {
	switch goos {
	case "darwin":
		return procattr.HideWindow(exec.Command("/usr/bin/open", u)).Start()
	case "windows":
		return procattr.HideWindow(exec.Command("rundll32", "url.dll,FileProtocolHandler", u)).Start()
	default:
		return procattr.HideWindow(exec.Command("xdg-open", u)).Start()
	}
}

type tokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

type userInfoResponse struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// exchangeTokenAndSave 调用官方 token 端点兑换凭据，通过 userinfo 校验，并在锁保护下合并落盘。
func (m *Manager) exchangeTokenAndSave(ctx context.Context, code, verifier, redirectURI string) (Account, error) {
	endpoints := m.opts.Endpoints.withDefaults()
	client := m.opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	form := url.Values{}
	form.Set("client_id", OfficialClientID)
	form.Set("client_secret", OfficialClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Account{}, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return Account{}, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Account{}, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tr tokenExchangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return Account{}, fmt.Errorf("decode token exchange response: %w", err)
	}
	if tr.AccessToken == "" {
		return Account{}, errors.New("empty access token received")
	}

	// 调 userinfo 校验邮箱
	userInfo, err := m.fetchUserInfo(ctx, endpoints.UserInfoURL, tr.AccessToken)
	if err != nil {
		return Account{}, fmt.Errorf("userinfo validation failed: %w", err)
	}
	if userInfo.Email == "" {
		return Account{}, errors.New("no email found in userinfo response")
	}

	expiry := m.opts.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	tokenFile := OAuthTokenFile{
		AuthMethod: "consumer",
		IDToken:    tr.IDToken,
		Token: OAuthTokenData{
			AccessToken:  tr.AccessToken,
			TokenType:    tr.TokenType,
			RefreshToken: tr.RefreshToken,
			Expiry:       expiry,
		},
	}

	return m.mergeAndSaveAccount(tokenFile, userInfo.Email, userInfo.Name)
}

func (m *Manager) fetchUserInfo(ctx context.Context, userInfoURL, accessToken string) (userInfoResponse, error) {
	client := m.opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return userInfoResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return userInfoResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return userInfoResponse{}, fmt.Errorf("userinfo failed with status %d: %s", resp.StatusCode, string(body))
	}

	var ui userInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
		return userInfoResponse{}, err
	}
	return ui, nil
}

// mergeAndSaveAccount 在锁内将凭证落盘并更新索引。
// 若 email 存在则更新原账号（走 withTokenLock）并激活，杜绝重复新增；否则新建隔离账号目录、指纹与索引项。
func (m *Manager) mergeAndSaveAccount(tokenFile OAuthTokenFile, email, name string) (Account, error) {
	acc, err := m.mergeAndSaveAccountLocked(tokenFile, email, name)
	if err != nil {
		return Account{}, err
	}
	m.refreshQuotaAfterLogin(acc.ID)
	return acc, nil
}

func (m *Manager) mergeAndSaveAccountLocked(tokenFile OAuthTokenFile, email, name string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查是否已有相同 email 的账号
	for i, acc := range m.index.Accounts {
		if acc.ID != SystemAccountID && acc.Email != "" && strings.EqualFold(acc.Email, email) {
			// 防幽灵写入校验：账号存在于当前索引中，仅更新 token 文件并激活状态
			accID := acc.ID
			writeErr := m.withTokenLock(accID, func(tokenPath string) error {
				return atomicWriteJSON(tokenPath, tokenFile)
			})
			if writeErr != nil {
				return Account{}, fmt.Errorf("write updated token: %w", writeErr)
			}

			m.index.Accounts[i].Status = StatusActive
			m.index.Accounts[i].StatusReason = ""
			if name != "" && m.index.Accounts[i].Name == "" {
				m.index.Accounts[i].Name = name
			}
			if err := m.saveIndexLocked(); err != nil {
				return Account{}, err
			}
			return m.index.Accounts[i], nil
		}
	}

	// 新建隔离账号
	accountID := uuid.NewString()
	if err := m.setupAccountLayout(accountID); err != nil {
		return Account{}, fmt.Errorf("setup layout for %s: %w", accountID, err)
	}

	// 写入 token 文件（走 withTokenLock）
	writeErr := m.withTokenLock(accountID, func(tokenPath string) error {
		return atomicWriteJSON(tokenPath, tokenFile)
	})
	if writeErr != nil {
		return Account{}, fmt.Errorf("write initial token for %s: %w", accountID, writeErr)
	}

	devProfile := generateDeviceProfile()
	newAcc := Account{
		ID:            accountID,
		Email:         email,
		Name:          name,
		Label:         "",
		Status:        StatusActive,
		StatusReason:  "",
		On:            true,
		Order:         len(m.index.Accounts),
		Weight:        100,
		ProjectID:     "",
		Tier:          "",
		DeviceProfile: devProfile,
		Quota:         make(map[string]QuotaGroup),
		QuotaReadAt:   "",
		QuotaStale:    false,
		AddedAt:       m.opts.Now().UTC().Format(time.RFC3339),
	}

	m.index.Accounts = append(m.index.Accounts, newAcc)
	if err := m.saveIndexLocked(); err != nil {
		return Account{}, err
	}

	return newAcc, nil
}

// TerminalLogin 为备用终端登录预建一个待定账号的目录布局，返回隔离环境参数。
func (m *Manager) TerminalLogin() (id string, env []string, args []string, err error) {
	tempID := uuid.NewString()
	if err := m.setupAccountLayout(tempID); err != nil {
		return "", nil, nil, fmt.Errorf("setup terminal login layout: %w", err)
	}

	homeDir := m.accountHomeDir(tempID)
	env, args = isolatedAgyEnvArgs(homeDir)

	// 本机 agy --help 未提供类似 login 或 auth 的专门子命令，未登录时直接启动交互式 agy 会话即可触发认证，
	// 因此子命令留空，仅传递 --gemini_dir 隔离目录参数（由 isolatedAgyEnvArgs 一并给出）。
	return tempID, env, args, nil
}

// AdoptTerminalLogin 读取指定待定账号目录下的 token 文件并核验合并。
func (m *Manager) AdoptTerminalLogin(ctx context.Context, id string) (Account, error) {
	// 系统账号映射宿主机真实凭证，禁止通过本入口读取/采纳。
	if id == SystemAccountID {
		return Account{}, errors.New("cannot adopt terminal login for the system account")
	}

	var tokenFile OAuthTokenFile
	readErr := m.withTokenLock(id, func(tokenPath string) error {
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &tokenFile)
	})
	if readErr != nil {
		return Account{}, fmt.Errorf("read terminal token for %s: %w", id, readErr)
	}

	if tokenFile.Token.AccessToken == "" {
		return Account{}, errors.New("terminal login token contains empty access_token")
	}

	endpoints := m.opts.Endpoints.withDefaults()
	userInfo, err := m.fetchUserInfo(ctx, endpoints.UserInfoURL, tokenFile.Token.AccessToken)
	if err != nil {
		return Account{}, fmt.Errorf("validate terminal login userinfo: %w", err)
	}
	if userInfo.Email == "" {
		return Account{}, errors.New("no email found for terminal login account")
	}

	acc, err := m.adoptTerminalLoginLocked(id, userInfo, tokenFile)
	if err != nil {
		return Account{}, err
	}
	m.refreshQuotaAfterLogin(acc.ID)
	return acc, nil
}

func (m *Manager) refreshQuotaAfterLogin(id string) {
	m.lastRefreshMu.Lock()
	delete(m.lastRefresh, id)
	m.lastRefreshMu.Unlock()
	if m.autoQuota.Load() {
		go func() {
			if err := m.RefreshQuota(context.Background(), id); err != nil {
				log.Printf("agy: async quota refresh for %s: %v", id, err)
			}
		}()
	}
}

func (m *Manager) adoptTerminalLoginLocked(id string, userInfo userInfoResponse, tokenFile OAuthTokenFile) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查是否已有相同 email 的账号
	for i, acc := range m.index.Accounts {
		if acc.ID != SystemAccountID && acc.Email != "" && strings.EqualFold(acc.Email, userInfo.Email) {
			// 与已有账号重复：把新 token 写进原账号并删掉临时目录
			existingID := acc.ID
			writeErr := m.withTokenLock(existingID, func(tokenPath string) error {
				return atomicWriteJSON(tokenPath, tokenFile)
			})
			if writeErr != nil {
				return Account{}, fmt.Errorf("write token to existing account %s: %w", existingID, writeErr)
			}

			// 清理临时目录。仅当临时 id 与既有账号不同时才删除，避免重复采纳同一
			// 临时账号（existingID == id）时把已生效账号自己的隔离目录删掉。
			if existingID != id {
				_ = os.RemoveAll(m.accountDir(id))
			}

			m.index.Accounts[i].Status = StatusActive
			m.index.Accounts[i].StatusReason = ""
			if userInfo.Name != "" && m.index.Accounts[i].Name == "" {
				m.index.Accounts[i].Name = userInfo.Name
			}
			if err := m.saveIndexLocked(); err != nil {
				return Account{}, err
			}
			return m.index.Accounts[i], nil
		}
	}

	// id 已被其他账号占用（例如误传 system 或既有账号 id）时拒绝，避免索引出现重复 id。
	if _, exists := m.findAccountLocked(id); exists {
		return Account{}, fmt.Errorf("account id %s already exists", id)
	}

	// 转换为正式账号条目
	devProfile := generateDeviceProfile()
	newAcc := Account{
		ID:            id,
		Email:         userInfo.Email,
		Name:          userInfo.Name,
		Label:         "",
		Status:        StatusActive,
		StatusReason:  "",
		On:            true,
		Order:         len(m.index.Accounts),
		Weight:        100,
		ProjectID:     "",
		Tier:          "",
		DeviceProfile: devProfile,
		Quota:         make(map[string]QuotaGroup),
		QuotaReadAt:   "",
		QuotaStale:    false,
		AddedAt:       m.opts.Now().UTC().Format(time.RFC3339),
	}

	m.index.Accounts = append(m.index.Accounts, newAcc)
	if err := m.saveIndexLocked(); err != nil {
		return Account{}, err
	}

	return newAcc, nil
}
