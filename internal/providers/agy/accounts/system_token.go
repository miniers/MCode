package accounts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 只读取系统登录凭证，刷新结果仅留在内存，绝不写系统凭据存储或宿主目录。
// 原始字节由平台实现读取：macOS 用 /usr/bin/security 读钥匙串，
// Windows 用凭据管理器的 CredReadW 读 Generic Credential。
func readSystemKeychainToken(ctx context.Context) (OAuthTokenFile, error) {
	data, err := readSystemCredentialBytes(ctx)
	if err != nil {
		return OAuthTokenFile{}, err
	}
	return decodeSystemToken(data)
}

func decodeSystemToken(data []byte) (OAuthTokenFile, error) {
	data = bytes.TrimSpace(data)
	if encoded, ok := bytes.CutPrefix(data, []byte("go-keyring-base64:")); ok {
		decoded, err := base64.StdEncoding.DecodeString(string(encoded))
		if err != nil {
			return OAuthTokenFile{}, errors.New("invalid system agy credential encoding")
		}
		data = decoded
	}
	var token OAuthTokenFile
	if err := json.Unmarshal(data, &token); err != nil {
		return OAuthTokenFile{}, errors.New("invalid system agy credential format")
	}
	return token, nil
}

// SystemTokenPath 返回宿主环境 agy CLI 在文件存储模式下的凭证文件路径：
// <hostHome>/.gemini/antigravity-cli/antigravity-oauth-token。
func SystemTokenPath(hostHome string) string {
	if hostHome == "" {
		return ""
	}
	return filepath.Join(hostHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}

// SystemLoginAvailable 报告宿主环境是否已有 agy 登录凭证，即「本机已登录」：
// 先看文件存储模式的 antigravity-oauth-token（token.access_token 或 refresh_token 非空），
// 再看系统凭据存储（macOS 钥匙串 / Windows 凭据管理器，service "gemini"、account "antigravity"）
// 里是否存在该条目。
// 账号管理器未接线（例如以 memory provider 启动）时，界面据此判断 Antigravity 的登录状态。
func SystemLoginAvailable(ctx context.Context) bool {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if tokenFileHasCredentials(SystemTokenPath(home)) {
			return true
		}
	}
	return systemKeychainHasEntry(ctx)
}

// SystemAccountEmail 解析系统登录凭证对应的邮箱，供额度弹窗把系统账号与同邮箱的隔离账号去重。
// 先读宿主环境的 token 文件（文件存储模式），再退回系统凭证读取器
// （macOS 钥匙串 / Windows 凭据管理器）；
// 只解析 id_token 的 payload，不发网络请求、不写凭证；解析不到时返回空串。
func (m *Manager) SystemAccountEmail(ctx context.Context) string {
	if email := tokenFileEmail(SystemTokenPath(m.opts.HostHome)); email != "" {
		return email
	}
	if m.opts.SystemTokenReader == nil {
		return ""
	}
	tok, err := m.opts.SystemTokenReader(ctx)
	if err != nil {
		return ""
	}
	return idTokenEmail(tok)
}

// tokenFileEmail 读取 token 文件并解析其中的邮箱。
func tokenFileEmail(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var token OAuthTokenFile
	if err := json.Unmarshal(data, &token); err != nil {
		return ""
	}
	return idTokenEmail(token)
}

// idTokenEmail 从 id_token（JWT）的 payload 里读 email，payload 是 base64url 编码的 JSON。
func idTokenEmail(token OAuthTokenFile) string {
	parts := strings.Split(strings.TrimSpace(token.IDToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Email)
}

// tokenFileHasCredentials 读取凭证文件并检查 token 字段；文件缺失或内容不是 JSON 时返回 false。
func tokenFileHasCredentials(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var token OAuthTokenFile
	if err := json.Unmarshal(data, &token); err != nil {
		return false
	}
	return strings.TrimSpace(token.Token.AccessToken) != "" || strings.TrimSpace(token.Token.RefreshToken) != ""
}

// systemKeychainHasEntry 只查询系统凭据存储里是否存在该条目，不解析其中的凭证内容。
func systemKeychainHasEntry(ctx context.Context) bool {
	return systemCredentialHasEntry(ctx)
}
