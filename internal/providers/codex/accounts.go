package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
)

// Account 描述一个 Codex 多账号条目：Home 为该账号的 CODEX_HOME 目录。
type Account struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Home  string `json:"home"`
	Email string `json:"email"`
}

// AccountStore 是账号列表及其激活状态的持久化载体。
type AccountStore struct {
	path string

	Accounts []Account `json:"accounts"`
	ActiveID string    `json:"activeId"`
}

// LoadAccounts 从 path 读取账号存储；文件不存在时返回一个空的 store，不视为错误。
func LoadAccounts(path string) (*AccountStore, error) {
	s := &AccountStore{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("codex: 读取账号存储: %w", err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("codex: 解析账号存储: %w", err)
	}
	return s, nil
}

// List 返回账号列表的副本。
func (s *AccountStore) List() []Account {
	out := make([]Account, len(s.Accounts))
	copy(out, s.Accounts)
	return out
}

// Add 追加一个账号；ID 为空、ID 重复或 Home 重复时报错。
func (s *AccountStore) Add(a Account) error {
	if a.ID == "" {
		return errors.New("codex: 账号 ID 不能为空")
	}
	for _, cur := range s.Accounts {
		if cur.ID == a.ID {
			return fmt.Errorf("codex: 账号 ID 已存在: %s", a.ID)
		}
		if a.Home != "" && cur.Home == a.Home {
			return fmt.Errorf("codex: 账号 Home 已存在: %s", a.Home)
		}
	}
	s.Accounts = append(s.Accounts, a)
	if a.Home != "" {
		if err := EnsureSharedLayout(a.Home); err != nil {
			log.Printf("codex: 为账号 %s 建立共享目录失败: %v", a.ID, err)
		}
	}
	return nil
}

// Remove 删除指定账号；若删除的是当前激活账号，则清空 ActiveID。
func (s *AccountStore) Remove(id string) error {
	idx := -1
	for i, cur := range s.Accounts {
		if cur.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("codex: 账号不存在: %s", id)
	}
	s.Accounts = append(s.Accounts[:idx], s.Accounts[idx+1:]...)
	if s.ActiveID == id {
		s.ActiveID = ""
	}
	return nil
}

// SetActive 切换激活账号；id 不存在时报错。
func (s *AccountStore) SetActive(id string) error {
	for _, cur := range s.Accounts {
		if cur.ID == id {
			s.ActiveID = id
			if cur.Home != "" {
				if err := EnsureSharedLayout(cur.Home); err != nil {
					log.Printf("codex: 为激活账号 %s 建立共享目录失败: %v", id, err)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("codex: 账号不存在: %s", id)
}

// Active 返回当前激活账号；无激活账号或已失效时返回 false。
func (s *AccountStore) Active() (Account, bool) {
	if s.ActiveID == "" {
		return Account{}, false
	}
	for _, cur := range s.Accounts {
		if cur.ID == s.ActiveID {
			return cur, true
		}
	}
	return Account{}, false
}

// Save 以原子方式写回账号存储。
func (s *AccountStore) Save() error {
	if s.path == "" {
		return errors.New("codex: 账号存储路径为空")
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("codex: 创建账号存储目录: %w", err)
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("codex: 序列化账号存储: %w", err)
	}
	if err := fsutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("codex: 写入账号存储: %w", err)
	}
	return nil
}

// ReadAccountEmail 从 home/auth.json 的 tokens.id_token 中解出邮箱；任何失败都返回空字符串。
func ReadAccountEmail(home string) string {
	claims, ok := idTokenClaimsFromToken(readAuthIDToken(home))
	if !ok {
		return ""
	}
	return claims.Email
}

// ReadAccountPlan 从 home/auth.json 的 tokens.id_token 中解出套餐类型并转成展示名；
// 取值路径为：claims["https://api.openai.com/auth"].chatgpt_plan_type。任何失败都返回空字符串。
func ReadAccountPlan(home string) string {
	claims, ok := idTokenClaimsFromToken(readAuthIDToken(home))
	if !ok {
		return ""
	}
	return FormatCodexPlanName(claims.Auth.ChatGPTPlanType)
}

// readAuthIDToken 读取 home/auth.json 中的 tokens.id_token；任何失败都返回空字符串。
func readAuthIDToken(home string) string {
	if home == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return ""
	}
	var auth struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}
	return auth.Tokens.IDToken
}

// idTokenClaims 是 Codex 登录 ID Token 中与账号身份相关的声明。
type idTokenClaims struct {
	Email string `json:"email"`
	Auth  struct {
		ChatGPTPlanType string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

// idTokenClaimsFromToken 解析 JWT 的 payload 段并取出声明；token 非法时 ok 为 false。
func idTokenClaimsFromToken(token string) (idTokenClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return idTokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// 部分签发方带 padding，补齐后再试一次。
		padded := parts[1] + strings.Repeat("=", (4-len(parts[1])%4)%4)
		payload, err = base64.URLEncoding.DecodeString(padded)
		if err != nil {
			return idTokenClaims{}, false
		}
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return idTokenClaims{}, false
	}
	return claims, true
}

// emailFromIDToken 解析 JWT 的 payload 段并取出 email 字段。
func emailFromIDToken(token string) string {
	claims, ok := idTokenClaimsFromToken(token)
	if !ok {
		return ""
	}
	return claims.Email
}

// AccountEnv 返回该账号对应的追加环境变量；Home 为空时不追加任何变量。
func AccountEnv(a Account) []string {
	if a.Home == "" {
		return nil
	}
	return []string{"CODEX_HOME=" + a.Home}
}
